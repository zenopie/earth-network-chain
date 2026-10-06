package keeper

import (
	"context"
	"errors"

	"cosmossdk.io/collections"
	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/x/personhood/types"
	shieldedtypes "github.com/earth-network/earth/x/shielded/types"
	"github.com/earth-network/earth/zk/privacy"
)

// This module's private msgs, as x/shielded private actions. The shielded
// ante runs Check and Verify for each, before it spends the fee notes; the
// msg handlers apply what Check prepared, once AuthorizedAction confirms the
// ante did so for this very msg. See x/shielded/types.PrivateActionHandler.

// RegisterPrivateActions attaches this module's private msgs to the pool.
// Called once, from module wiring.
func (k Keeper) RegisterPrivateActions(sk types.ShieldedKeeper) {
	sk.RegisterPrivateAction(sdk.MsgTypeURL(&types.MsgRegister{}), registerAction{k})
	sk.RegisterPrivateAction(sdk.MsgTypeURL(&types.MsgClaimAnml{}), claimAction{k})
	sk.RegisterPrivateAction(sdk.MsgTypeURL(&types.MsgSetCaretaker{}), caretakerAction{k})
	sk.RegisterPrivateAction(sdk.MsgTypeURL(&types.MsgBindHandle{}), handleAction{k})
	sk.RegisterPrivateAction(sdk.MsgTypeURL(&types.MsgMoveHandle{}), moveHandleAction{k})
	sk.RegisterPrivateAction(sdk.MsgTypeURL(&types.MsgMoveCaretaker{}), moveCaretakerAction{k})
}

// --- MsgRegister ---------------------------------------------------------

type registerAction struct{ k Keeper }

// PrivateActionGas: the passport proof, the DSC chain, and up to five
// note-sized writes (two identity-leaf writes, three minted notes).
func (a registerAction) PrivateActionGas(ctx context.Context, _ shieldedtypes.PrivateMsg) (uint64, error) {
	params, err := a.k.Params.Get(ctx)
	if err != nil {
		return 0, err
	}
	_, note, err := a.k.shieldedKeeper.PrivateGasPrices(ctx)
	if err != nil {
		return 0, err
	}
	// Five note-sized writes, and a sixth for the succession leaf a switch
	// or re-entry appends.
	return params.ProofVerificationGasOrDefault() + params.DscVerificationGasOrDefault() + 6*note, nil
}

func (a registerAction) CheckPrivateAction(ctx context.Context, msg shieldedtypes.PrivateMsg) (any, error) {
	return a.k.checkRegistration(ctx, msg.(*types.MsgRegister))
}

func (a registerAction) VerifyPrivateAction(ctx context.Context, msg shieldedtypes.PrivateMsg, prepared any) error {
	return a.k.verifyRegistrationProofIn(ctx, msg.(*types.MsgRegister), prepared.(preparedRegistration))
}

// --- MsgClaimAnml --------------------------------------------------------

type claimAction struct{ k Keeper }

func (a claimAction) PrivateActionGas(ctx context.Context, _ shieldedtypes.PrivateMsg) (uint64, error) {
	return a.k.MembershipActionGas(ctx, 2) // the ANML note, the claim nullifier
}

// claimStatement is the membership a claim for day proves: scope claim/day,
// no excluded signer, and an identity activated before the previous UTC day
// began. That bound is what stops a switch to a new identity secret from
// claiming a second time on a day the old one already claimed: the new leaf
// is activated at the switch, so it cannot claim until the day after next,
// by which time the old leaf has long been zeroed and its roots expired.
func (k Keeper) claimStatement(ctx context.Context, m *types.MsgClaimAnml) (MembershipStatement, error) {
	signal, err := k.SignalOf(ctx, m)
	if err != nil {
		return MembershipStatement{}, err
	}
	return MembershipStatement{
		Scope:          privacy.ClaimScope(m.Day),
		Signal:         signal,
		MaxActivation:  (int64(m.Day) - 1) * types.SecondsPerDay,
		MaxPredecessor: types.NoBound,
	}, nil
}

func (k Keeper) checkClaim(ctx context.Context, m *types.MsgClaimAnml) (MembershipStatement, error) {
	today := uint64(sdk.UnwrapSDKContext(ctx).BlockTime().Unix()) / types.SecondsPerDay
	if m.Day != today {
		return MembershipStatement{}, errorsmod.Wrapf(types.ErrWrongDay, "claim for day %d, today is %d", m.Day, today)
	}
	used, err := k.ClaimNullifiers.Has(ctx, collections.Join(m.Day, m.Membership.Nullifier))
	if err != nil {
		return MembershipStatement{}, err
	}
	if used {
		return MembershipStatement{}, types.ErrClaimTooSoon
	}
	if err := k.CheckMembership(ctx, m.Membership); err != nil {
		return MembershipStatement{}, err
	}
	return k.claimStatement(ctx, m)
}

func (a claimAction) CheckPrivateAction(ctx context.Context, msg shieldedtypes.PrivateMsg) (any, error) {
	return a.k.checkClaim(ctx, msg.(*types.MsgClaimAnml))
}

func (a claimAction) VerifyPrivateAction(ctx context.Context, msg shieldedtypes.PrivateMsg, prepared any) error {
	return a.k.VerifyMembership(ctx, msg.(*types.MsgClaimAnml).Membership, prepared.(MembershipStatement))
}

// --- MsgSetCaretaker -----------------------------------------------------

type caretakerAction struct{ k Keeper }

func (a caretakerAction) PrivateActionGas(ctx context.Context, _ shieldedtypes.PrivateMsg) (uint64, error) {
	// The lease, its expiry index, and the allocation resync of up to
	// MaxVoterOptions entries (old and new), priced as four note writes.
	return a.k.MembershipActionGas(ctx, 4)
}

// caretakerStatement: scope caretaker, any activation, and a predecessor (the
// switch or re-entry that made the leaf) by the msg's max_predecessor, which
// must be strictly before LeaseActivationBound: R (or a held, longer R) + the
// activation margin ago. A split lasts R, and a zeroed leaf keeps proving for
// one root window (<= the margin), so by the time an identity that replaced
// another may cast a split, every split its predecessor could have cast has
// lapsed, whatever governance did to either parameter. A fresh registrant
// (predecessor_at 0) has no predecessor and casts at once.
func (k Keeper) caretakerStatement(ctx context.Context, m *types.MsgSetCaretaker) (MembershipStatement, error) {
	signal, err := k.SignalOf(ctx, m)
	if err != nil {
		return MembershipStatement{}, err
	}
	bound, err := k.LeaseActivationBound(ctx)
	if err != nil {
		return MembershipStatement{}, err
	}
	// Strictly before the bound (audit 4, C7): at max_activation == bound a
	// successor activated at the bound could file a lease in the very block
	// its predecessor's last lease lapses, both counted until the sweep.
	// Only a new split is bounded: refreshing, changing or clearing a live
	// one the prover holds (cast, or moved to it) creates none. A lapsed
	// split the sweep has not reached yet is not held (audit 5 P2, the
	// handle's switch-and-switch-back: refreshing it unbounded would revive
	// it beside a successor's).
	nf := m.Membership.Nullifier
	holds := false
	if exp, err := k.CaretakerVotes.Get(ctx, nf); err == nil {
		holds = exp > sdk.UnwrapSDKContext(ctx).BlockTime().Unix()
	} else if !errors.Is(err, collections.ErrNotFound) {
		return MembershipStatement{}, err
	}
	if !holds && len(m.Percentages) > 0 {
		if moved, err := k.CaretakerMovedOut.Has(ctx, nf); err != nil {
			return MembershipStatement{}, err
		} else if moved {
			return MembershipStatement{}, types.ErrCaretakerMovedOut
		}
		if err := checkPredecessorBound(m.MaxPredecessor, bound); err != nil {
			return MembershipStatement{}, err
		}
	}
	return MembershipStatement{
		Scope:          privacy.CaretakerScope(),
		Signal:         signal,
		MaxActivation:  types.NoBound,
		MaxPredecessor: int64(m.MaxPredecessor),
	}, nil
}

// checkPredecessorBound refuses a msg's max_predecessor at or after bound
// (strictly before: at the bound a successor could file in the very block
// its predecessor's last lease lapses, both counted until the sweep).
func checkPredecessorBound(maxPredecessor uint64, bound int64) error {
	if bound <= 0 || maxPredecessor >= uint64(bound) {
		return errorsmod.Wrapf(types.ErrInvalidMsg,
			"max_predecessor %d is not before %d (now - lease length - activation margin)", maxPredecessor, bound)
	}
	return nil
}

func (k Keeper) checkCaretaker(ctx context.Context, m *types.MsgSetCaretaker) (MembershipStatement, error) {
	if err := k.allocationKeeper.ValidateSplit(ctx, types.AllocationStream, m.Percentages); err != nil {
		return MembershipStatement{}, err
	}
	if err := k.CheckMembership(ctx, m.Membership); err != nil {
		return MembershipStatement{}, err
	}
	return k.caretakerStatement(ctx, m)
}

func (a caretakerAction) CheckPrivateAction(ctx context.Context, msg shieldedtypes.PrivateMsg) (any, error) {
	return a.k.checkCaretaker(ctx, msg.(*types.MsgSetCaretaker))
}

func (a caretakerAction) VerifyPrivateAction(ctx context.Context, msg shieldedtypes.PrivateMsg, prepared any) error {
	return a.k.VerifyMembership(ctx, msg.(*types.MsgSetCaretaker).Membership, prepared.(MembershipStatement))
}

// ReleasedDenoms: personhood's private msgs only pay a fee; they take no
// value from the pool.
func (registerAction) ReleasedDenoms(shieldedtypes.PrivateMsg) []string  { return nil }
func (claimAction) ReleasedDenoms(shieldedtypes.PrivateMsg) []string     { return nil }
func (caretakerAction) ReleasedDenoms(shieldedtypes.PrivateMsg) []string { return nil }

package keeper

import (
	"context"
	"errors"

	"cosmossdk.io/collections"
	errorsmod "cosmossdk.io/errors"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
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
	sk.RegisterPrivateAction(sdk.MsgTypeURL(&types.MsgClaimAnmlDays{}), claimDaysAction{k})
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

// claimAction refuses MsgClaimAnml, retired by v1.4.0, before anything is
// checked or charged. It stays registered (and its type decodes) so the
// claims made before it read back; MsgClaimAnmlDays claims one day or more.
type claimAction struct{ k Keeper }

func (claimAction) PrivateActionGas(context.Context, shieldedtypes.PrivateMsg) (uint64, error) {
	return 0, nil
}

func (claimAction) CheckPrivateAction(context.Context, shieldedtypes.PrivateMsg) (any, error) {
	return nil, types.ErrClaimRetired
}

func (claimAction) VerifyPrivateAction(context.Context, shieldedtypes.PrivateMsg, any) error {
	return types.ErrClaimRetired
}

// --- MsgClaimAnmlDays ----------------------------------------------------

type claimDaysAction struct{ k Keeper }

// PrivateActionGas: one claim proof, the ANML note and a nullifier per day.
func (a claimDaysAction) PrivateActionGas(ctx context.Context, msg shieldedtypes.PrivateMsg) (uint64, error) {
	return a.k.MembershipActionGas(ctx, 1+msg.(*types.MsgClaimAnmlDays).Days())
}

// ClaimDaysStatement is what a claim proof is verified against: the claim
// scope of each day of the run, the msg's sighash, and the activation bound
// of its first day.
type ClaimDaysStatement struct {
	Scopes        []fr.Element
	Signal        fr.Element
	MaxActivation int64
}

// RaiseClaimFloor sets the claim floor to the day before the one blockTime
// falls in, unless it is already later: the oldest day whose claim
// nullifiers a binary before v1.4.0 (a two-day prune) still held. Used by the
// v1.4.0 upgrade and by an import that carries no floor.
func (k Keeper) RaiseClaimFloor(ctx context.Context, blockTime int64) error {
	var floor uint64
	if d := dayOf(blockTime); d > 0 {
		floor = d - 1
	}
	prev, err := k.ClaimFloorDay.Get(ctx)
	switch {
	case err == nil:
		floor = max(floor, prev)
	case !errors.Is(err, collections.ErrNotFound):
		return err
	}
	return k.ClaimFloorDay.Set(ctx, floor)
}

// ClaimEarliestDay is the earliest day a MsgClaimAnmlDays run may start
// now: ClaimWindowDays-1 days before today, or the claim floor if later.
func (k Keeper) ClaimEarliestDay(ctx context.Context) (uint64, error) {
	today := dayOf(sdk.UnwrapSDKContext(ctx).BlockTime().Unix())
	earliest := uint64(0)
	if today >= types.ClaimWindowDays-1 {
		earliest = today - (types.ClaimWindowDays - 1)
	}
	floor, err := k.ClaimFloorDay.Get(ctx)
	if err != nil && !errors.Is(err, collections.ErrNotFound) {
		return 0, err
	}
	return max(earliest, floor), nil
}

// checkClaimDays: the run ends today, starts no earlier than
// ClaimEarliestDay (the window and the claim floor), and none of its days is
// claimed. Its activation bound is first_day's: an identity activated before
// the day before first_day began. Later days'
// bounds are later, so the one bound covers the run, and the reason it
// exists (a switch to a new identity secret cannot claim a day the old one
// may have claimed) holds for each day as it does for a one-day claim.
func (k Keeper) checkClaimDays(ctx context.Context, m *types.MsgClaimAnmlDays) (ClaimDaysStatement, error) {
	today := dayOf(sdk.UnwrapSDKContext(ctx).BlockTime().Unix())
	if m.Day != today {
		return ClaimDaysStatement{}, errorsmod.Wrapf(types.ErrWrongDay, "claim through day %d, today is %d", m.Day, today)
	}
	earliest, err := k.ClaimEarliestDay(ctx)
	if err != nil {
		return ClaimDaysStatement{}, err
	}
	if m.FirstDay < earliest {
		return ClaimDaysStatement{}, errorsmod.Wrapf(types.ErrWrongDay, "claim from day %d, claims reach back to day %d", m.FirstDay, earliest)
	}
	// What the circuit binds (ClaimSlots slots) is what is recorded and
	// minted: one nullifier per day of the run. ValidateBasic says so too;
	// this holds it whatever path reached here.
	if n := m.Days(); n == 0 || n > types.ClaimSlots || uint64(len(m.Claim.Nullifiers)) != n {
		return ClaimDaysStatement{}, errorsmod.Wrapf(types.ErrInvalidMsg, "%d nullifiers for first_day %d to day %d", len(m.Claim.Nullifiers), m.FirstDay, m.Day)
	}
	scopes := make([]fr.Element, len(m.Claim.Nullifiers))
	claimed, last := false, uint64(0)
	for i, nf := range m.Claim.Nullifiers {
		day := m.FirstDay + uint64(i)
		used, err := k.ClaimNullifiers.Has(ctx, collections.Join(day, nf))
		if err != nil {
			return ClaimDaysStatement{}, err
		}
		if used {
			claimed, last = true, day
		}
		scopes[i] = privacy.ClaimScope(day)
	}
	// The last claimed day: a wallet that did not know it starts its run
	// after it, in one retry.
	if claimed {
		return ClaimDaysStatement{}, errorsmod.Wrapf(types.ErrDayClaimed, "day %d", last)
	}
	if err := k.CheckIdentityAnchor(ctx, m.Claim.Root); err != nil {
		return ClaimDaysStatement{}, err
	}
	signal, err := k.SignalOf(ctx, m)
	if err != nil {
		return ClaimDaysStatement{}, err
	}
	return ClaimDaysStatement{
		Scopes:        scopes,
		Signal:        signal,
		MaxActivation: (int64(m.FirstDay) - 1) * types.SecondsPerDay,
	}, nil
}

func (a claimDaysAction) CheckPrivateAction(ctx context.Context, msg shieldedtypes.PrivateMsg) (any, error) {
	return a.k.checkClaimDays(ctx, msg.(*types.MsgClaimAnmlDays))
}

func (a claimDaysAction) VerifyPrivateAction(ctx context.Context, msg shieldedtypes.PrivateMsg, prepared any) error {
	return a.k.VerifyClaimDays(ctx, msg.(*types.MsgClaimAnmlDays).Claim, prepared.(ClaimDaysStatement))
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
func (claimDaysAction) ReleasedDenoms(shieldedtypes.PrivateMsg) []string { return nil }
func (caretakerAction) ReleasedDenoms(shieldedtypes.PrivateMsg) []string { return nil }

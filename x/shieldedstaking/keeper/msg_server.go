package keeper

import (
	"bytes"
	"context"
	"errors"
	"strconv"

	"cosmossdk.io/collections"
	errorsmod "cosmossdk.io/errors"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"

	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	"github.com/earth-network/earth/x/shieldedstaking/types"
)

type msgServer struct{ Keeper }

// NewMsgServerImpl returns the Msg service.
func NewMsgServerImpl(k Keeper) types.MsgServer { return &msgServer{Keeper: k} }

var _ types.MsgServer = msgServer{}

func (k msgServer) UpdateParams(ctx context.Context, req *types.MsgUpdateParams) (*types.MsgUpdateParamsResponse, error) {
	a, err := k.addressCodec.StringToBytes(req.Authority)
	if err != nil {
		return nil, errorsmod.Wrap(err, "invalid authority address")
	}
	if !bytes.Equal(k.authority, a) {
		return nil, types.ErrInvalidSigner
	}
	if err := req.Params.Validate(); err != nil {
		return nil, err
	}
	if err := k.checkUnbondingEntries(ctx, req.Params); err != nil {
		return nil, err
	}
	return &types.MsgUpdateParamsResponse{}, k.Params.Set(ctx, req.Params)
}

// Delegate: the ERTH is delegated to the validator at once (bonded in this
// block, earning from it), and the derth it buys at the live rate (the msg's
// derth, checked) is credited to the owner's derth/v note: the proof spends
// it (or pads) and creates the merged note. Priced at the live rate and
// earning from the same block, a delegation shares in no reward it did not
// earn. A validator that cannot take a delegation now (bondNow) queues it
// for the epoch end, as rewards are.
func (k msgServer) Delegate(goCtx context.Context, m *types.MsgDelegate) (*types.MsgDelegateResponse, error) {
	ctx, err := k.authorized(goCtx, m)
	if err != nil {
		return nil, err
	}
	if err := k.checkDelegate(ctx, m); err != nil {
		return nil, err
	}
	d := math.NewIntFromUint64(m.Derth)
	paid, err := k.shielded.ReleaseToModule(ctx, m, types.BondDenom, types.ModuleName)
	if err != nil {
		return nil, err
	}
	vs, err := k.ValidatorState(ctx, m.Validator)
	if err != nil {
		return nil, err
	}
	if err := k.checkpointSupply(ctx, &vs); err != nil {
		return nil, err
	}
	bonded, late, err := k.bondNow(ctx, m.Validator, paid.Amount)
	if err != nil {
		return nil, err
	}
	if !bonded {
		vs.PendingDelegation = vs.PendingDelegation.Add(paid.Amount)
	}
	vs.PendingDelegation = vs.PendingDelegation.Add(late)
	vs.DerthSupply = vs.DerthSupply.Add(d)
	if err := k.Validators.Set(ctx, m.Validator, vs); err != nil {
		return nil, err
	}
	pos, err := k.applyStake(ctx, m)
	if err != nil {
		return nil, err
	}
	ctx.EventManager().EmitEvent(sdk.NewEvent(types.EventTypeDelegate,
		sdk.NewAttribute(types.AttributeKeyValidator, m.Validator),
		sdk.NewAttribute(types.AttributeKeyAmount, paid.Amount.String()),
		sdk.NewAttribute(types.AttributeKeyDerth, d.String()),
		sdk.NewAttribute(types.AttributeKeyDelegated, strconv.FormatBool(bonded)),
	))
	return &types.MsgDelegateResponse{Derth: m.Derth, Position: firstPosition(pos)}, nil
}

// bondNow delegates amount uerth from the module to valoper at once, unless
// the validator cannot take it (gone, or slashed to nothing: then the caller
// queues it, as the epoch end would). late is what the delegation change
// paid out: x/distribution withdraws the module's rewards at v on every
// change, and they join v's queue, so the book's backing grows by exactly
// the amount (its rate does not move, up to the delegation's share
// truncation).
func (k Keeper) bondNow(ctx context.Context, valoper string, amount math.Int) (bonded bool, late math.Int, err error) {
	val, err := k.valAddr(valoper)
	if err != nil {
		return false, math.Int{}, err
	}
	v, err := k.staking.GetValidator(ctx, val)
	if errors.Is(err, stakingtypes.ErrNoValidatorFound) {
		return false, math.ZeroInt(), nil
	} else if err != nil {
		return false, math.Int{}, err
	}
	if !v.Tokens.IsPositive() || v.InvalidExRate() {
		return false, math.ZeroInt(), nil
	}
	before := k.bank.GetBalance(ctx, k.modAddr, types.BondDenom).Amount
	if _, err := k.staking.Delegate(ctx, k.modAddr, amount, stakingtypes.Unbonded, v, true); err != nil {
		return false, math.Int{}, err
	}
	late = k.bank.GetBalance(ctx, k.modAddr, types.BondDenom).Amount.Sub(before.Sub(amount))
	return true, late, nil
}

// Restake merges the owner's stake notes: the proof's output is appended,
// its inputs spent, their Groundworks votes cancelled and the output's cast
// (a note voting, changing its split or renewing it in place). Nothing else
// changes.
func (k msgServer) Restake(goCtx context.Context, m *types.MsgRestake) (*types.MsgRestakeResponse, error) {
	ctx, err := k.authorized(goCtx, m)
	if err != nil {
		return nil, err
	}
	pos, err := k.applyStake(ctx, m)
	if err != nil {
		return nil, err
	}
	return &types.MsgRestakeResponse{Positions: pos}, nil
}

// Undelegate: amount of derth/v leaves the owner's notes (any change back to
// them); its live value joins this epoch's undelegation for v, and a payout
// of it to the msg's pc is queued (payouts.go): the chain mints it at
// maturity, by itself.
func (k msgServer) Undelegate(goCtx context.Context, m *types.MsgUndelegate) (*types.MsgUndelegateResponse, error) {
	ctx, err := k.authorized(goCtx, m)
	if err != nil {
		return nil, err
	}
	u, err := k.checkUndelegate(ctx, m)
	if err != nil {
		return nil, err
	}
	epoch, err := k.Epoch.Get(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := k.applyStake(ctx, m); err != nil {
		return nil, err
	}
	d := math.NewIntFromUint64(m.Amount)
	key := collections.Join(m.Validator, epoch.Number)
	r, err := k.UnbondRecords.Get(ctx, key)
	if errors.Is(err, collections.ErrNotFound) {
		r = types.UnbondRecord{
			Validator: m.Validator, Epoch: epoch.Number, Status: types.UNBOND_STATUS_PENDING,
			Requested: math.ZeroInt(), Target: math.ZeroInt(), Undelegated: math.ZeroInt(),
			Payout: math.ZeroInt(), Outstanding: math.ZeroInt(), Paid: math.ZeroInt(),
		}
	} else if err != nil {
		return nil, err
	}
	r.Requested = r.Requested.Add(u)
	r.Target = r.Target.Add(u)
	r.Outstanding = r.Outstanding.Add(u)
	if err := k.UnbondRecords.Set(ctx, key, r); err != nil {
		return nil, err
	}
	if err := k.PendingRecords.Set(ctx, key); err != nil {
		return nil, err
	}
	vs, err := k.ValidatorState(ctx, m.Validator)
	if err != nil {
		return nil, err
	}
	if err := k.checkpointSupply(ctx, &vs); err != nil {
		return nil, err
	}
	vs.PendingUndelegation = vs.PendingUndelegation.Add(u)
	vs.DerthSupply = vs.DerthSupply.Sub(d)
	if err := k.Validators.Set(ctx, m.Validator, vs); err != nil {
		return nil, err
	}
	id, err := k.queuePayout(ctx, m.Validator, epoch.Number, u, m.Pc, m.Ciphertext)
	if err != nil {
		return nil, err
	}
	ctx.EventManager().EmitEvent(sdk.NewEvent(types.EventTypeUndelegate,
		sdk.NewAttribute(types.AttributeKeyValidator, m.Validator),
		sdk.NewAttribute(types.AttributeKeyDerth, d.String()),
		sdk.NewAttribute(types.AttributeKeyValue, u.String()),
		sdk.NewAttribute(types.AttributeKeyEpoch, strconv.FormatUint(epoch.Number, 10)),
		sdk.NewAttribute(types.AttributeKeyPayoutID, strconv.FormatUint(id, 10)),
	))
	return &types.MsgUndelegateResponse{Value: u.Uint64(), PayoutId: id}, nil
}

// StakeVote records one vote of up to two stake notes, its weight once.
// Nothing is spent or minted: the vote nullifiers (checked unused by
// checkStakeVote, recorded by putVote) stop the notes voting on this
// proposal again, and nothing else.
func (k msgServer) StakeVote(goCtx context.Context, m *types.MsgStakeVote) (*types.MsgStakeVoteResponse, error) {
	ctx, err := k.authorized(goCtx, m)
	if err != nil {
		return nil, err
	}
	_, d, err := k.checkStakeVote(ctx, m)
	if err != nil {
		return nil, err
	}
	vnfs := append([][]byte(nil), m.VoteNullifiers...)
	v := types.StakeVote{
		ProposalId: m.ProposalId, Key: noteVoteKey(vnfs[0]), Validator: m.Validator,
		Derth: d, Options: m.Options, VoteNullifiers: vnfs,
	}
	if err := k.putVote(ctx, v); err != nil {
		return nil, err
	}
	return &types.MsgStakeVoteResponse{}, nil
}

// applyStake applies a stake msg's proof (its spends and outputs, stake_tree.go)
// and then its Groundworks effect (groundworks.go). Every stake msg's handler
// goes through it.
func (k Keeper) applyStake(ctx context.Context, m types.StakeMsg) ([]uint64, error) {
	// The ante refused these already (CheckPrivateAction); the handler
	// repeats them on the same state, so the two cannot disagree.
	if err := k.checkGroundworks(ctx, m); err != nil {
		return nil, err
	}
	pos, err := k.applyStakeProof(ctx, m.StakeProofOf())
	if err != nil {
		return nil, err
	}
	return pos, k.applyGroundworks(ctx, m)
}

// firstPosition is lane A's output's position (the merged note's), 0 when
// the proof created none (never, for a msg its shape admits).
func firstPosition(pos []uint64) uint64 {
	if len(pos) == 0 {
		return 0
	}
	return pos[0]
}

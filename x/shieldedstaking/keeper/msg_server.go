package keeper

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"strconv"

	"cosmossdk.io/collections"
	errorsmod "cosmossdk.io/errors"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"

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

// Delegate: the ERTH joins the validator's queue, and the derth it buys at
// the live rate (the msg's derth, checked) is credited to the owner's
// derth/v note: the proof spends it (or pads) and creates the merged note.
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
	vs.PendingDelegation = vs.PendingDelegation.Add(paid.Amount)
	vs.DerthSupply = vs.DerthSupply.Add(d)
	if err := k.Validators.Set(ctx, m.Validator, vs); err != nil {
		return nil, err
	}
	pos, err := k.applyStakeProof(ctx, &m.Stake)
	if err != nil {
		return nil, err
	}
	ctx.EventManager().EmitEvent(sdk.NewEvent(types.EventTypeDelegate,
		sdk.NewAttribute(types.AttributeKeyValidator, m.Validator),
		sdk.NewAttribute(types.AttributeKeyAmount, paid.Amount.String()),
		sdk.NewAttribute(types.AttributeKeyDerth, d.String()),
	))
	return &types.MsgDelegateResponse{Derth: m.Derth, Position: firstPosition(pos)}, nil
}

// Restake merges the owner's stake notes: the proof's output is appended,
// its inputs spent. Nothing else changes.
func (k msgServer) Restake(goCtx context.Context, m *types.MsgRestake) (*types.MsgRestakeResponse, error) {
	ctx, err := k.authorized(goCtx, m)
	if err != nil {
		return nil, err
	}
	pos, err := k.applyStakeProof(ctx, &m.Stake)
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
	if _, err := k.applyStakeProof(ctx, &m.Stake); err != nil {
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

// StakeVote records one vote of up to four stake notes, its weight once.
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
	vnfs := m.UsedVoteNullifiers()
	v := types.StakeVote{
		ProposalId: m.ProposalId, Key: noteVoteKey(vnfs[0]), Validator: m.Validator,
		Derth: d, Options: m.Options, VoteNullifiers: vnfs,
	}
	if err := k.putVote(ctx, v); err != nil {
		return nil, err
	}
	return &types.MsgStakeVoteResponse{}, nil
}

// LockPosition moves amount of the owner's derth from notes into a new
// position owned by the proof's owner tag.
func (k msgServer) LockPosition(goCtx context.Context, m *types.MsgLockPosition) (*types.MsgLockPositionResponse, error) {
	ctx, err := k.authorized(goCtx, m)
	if err != nil {
		return nil, err
	}
	if err := k.checkLock(ctx, m); err != nil {
		return nil, err
	}
	if _, err := k.applyStakeProof(ctx, &m.Stake); err != nil {
		return nil, err
	}
	id, err := k.PositionSeq.Next(ctx)
	if err != nil {
		return nil, err
	}
	p := types.Position{
		Id: id, Validator: m.Validator, Derth: math.NewIntFromUint64(m.Amount),
		OwnerTag: m.Stake.OwnerTag, CreatedHeight: ctx.BlockHeight(), Weight: math.ZeroInt(),
	}
	if p, err = k.applyPositionSplit(ctx, p, m.Splits, false); err != nil {
		return nil, err
	}
	if err := k.setPosition(ctx, p); err != nil {
		return nil, err
	}
	if err := k.PositionsByVal.Set(ctx, collections.Join(p.Validator, id)); err != nil {
		return nil, err
	}
	k.positionEvent(ctx, "lock", p)
	return &types.MsgLockPositionResponse{PositionId: id}, nil
}

// UpdatePosition replaces a position's split.
func (k msgServer) UpdatePosition(goCtx context.Context, m *types.MsgUpdatePosition) (*types.MsgUpdatePositionResponse, error) {
	ctx, err := k.authorized(goCtx, m)
	if err != nil {
		return nil, err
	}
	p, err := k.checkUpdate(ctx, m)
	if err != nil {
		return nil, err
	}
	if p, err = k.applyPositionSplit(ctx, p, m.Splits, true); err != nil {
		return nil, err
	}
	if err := k.setPosition(ctx, p); err != nil {
		return nil, err
	}
	k.positionEvent(ctx, "update", p)
	return &types.MsgUpdatePositionResponse{}, nil
}

// UnlockPosition closes a position; its derth is credited to its owner's
// stake note (the proof spends it, or pads, and creates the merged note).
func (k msgServer) UnlockPosition(goCtx context.Context, m *types.MsgUnlockPosition) (*types.MsgUnlockPositionResponse, error) {
	ctx, err := k.authorized(goCtx, m)
	if err != nil {
		return nil, err
	}
	p, err := k.checkPositionOwner(ctx, m.PositionId, &m.Stake)
	if err != nil {
		return nil, err
	}
	if _, err := k.applyPositionSplit(ctx, p, nil, true); err != nil {
		return nil, err
	}
	pos, err := k.applyStakeProof(ctx, &m.Stake)
	if err != nil {
		return nil, err
	}
	if err := k.Positions.Remove(ctx, p.Id); err != nil {
		return nil, err
	}
	if err := k.PositionsByVal.Remove(ctx, collections.Join(p.Validator, p.Id)); err != nil {
		return nil, err
	}
	k.positionEvent(ctx, "unlock", p)
	return &types.MsgUnlockPositionResponse{Position: firstPosition(pos)}, nil
}

// PositionVote records (or replaces) a position's vote.
func (k msgServer) PositionVote(goCtx context.Context, m *types.MsgPositionVote) (*types.MsgPositionVoteResponse, error) {
	ctx, err := k.authorized(goCtx, m)
	if err != nil {
		return nil, err
	}
	p, _, err := k.checkPositionVote(ctx, m)
	if err != nil {
		return nil, err
	}
	v := types.StakeVote{
		ProposalId: m.ProposalId, Key: binary.BigEndian.AppendUint64([]byte{1}, p.Id), Position: true,
		Validator: p.Validator, Derth: p.Derth, Options: m.Options,
	}
	if err := k.putVote(ctx, v); err != nil {
		return nil, err
	}
	return &types.MsgPositionVoteResponse{}, nil
}

func (k Keeper) positionEvent(ctx sdk.Context, action string, p types.Position) {
	p = k.withLiveWeight(ctx, p)
	if action == "unlock" {
		p.Weight = math.ZeroInt()
	}
	ctx.EventManager().EmitEvent(sdk.NewEvent(types.EventTypePosition,
		sdk.NewAttribute(types.AttributeKeyAction, action),
		sdk.NewAttribute(types.AttributeKeyPosition, strconv.FormatUint(p.Id, 10)),
		sdk.NewAttribute(types.AttributeKeyValidator, p.Validator),
		sdk.NewAttribute(types.AttributeKeyDerth, p.Derth.String()),
		sdk.NewAttribute(types.AttributeKeyWeight, p.Weight.String()),
	))
}

// firstPosition is lane A's output's position (the merged note's), 0 when
// the proof created none (never, for a msg its shape admits).
func firstPosition(pos []uint64) uint64 {
	if len(pos) == 0 {
		return 0
	}
	return pos[0]
}

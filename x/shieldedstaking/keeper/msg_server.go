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

	allocationtypes "github.com/earth-network/earth/x/allocation/types"
	shieldedkeeper "github.com/earth-network/earth/x/shielded/keeper"
	shieldedtypes "github.com/earth-network/earth/x/shielded/types"
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

// Delegate: the ERTH joins the validator's queue, and derth/v is minted at the
// live rate as a stake note to the proof's spc_mint (the delegator's own).
func (k msgServer) Delegate(goCtx context.Context, m *types.MsgDelegate) (*types.MsgDelegateResponse, error) {
	ctx, err := k.authorized(goCtx, m)
	if err != nil {
		return nil, err
	}
	d, err := k.checkDelegate(ctx, m)
	if err != nil {
		return nil, err
	}
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
	pos, err := k.mintStake(ctx, types.DerthDenom(m.Validator), d, m.Stake.SpcMint)
	if err != nil {
		return nil, err
	}
	ctx.EventManager().EmitEvent(sdk.NewEvent(types.EventTypeDelegate,
		sdk.NewAttribute(types.AttributeKeyValidator, m.Validator),
		sdk.NewAttribute(types.AttributeKeyAmount, paid.Amount.String()),
		sdk.NewAttribute(types.AttributeKeyDerth, d.String()),
	))
	return &types.MsgDelegateResponse{Derth: d.Uint64(), Position: pos}, nil
}

// Restake merges or splits the owner's stake notes: the proof's outputs are
// appended, its inputs spent. Nothing else changes.
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
// them) and an owner-locked unbond/v/e claim of its live value is minted to
// the proof's spc_mint; the value joins this epoch's undelegation for v.
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
	denom := types.UnbondDenom(m.Validator, epoch.Number)
	pos, err := k.mintStake(ctx, denom, u, m.Stake.SpcMint)
	if err != nil {
		return nil, err
	}
	ctx.EventManager().EmitEvent(sdk.NewEvent(types.EventTypeUndelegate,
		sdk.NewAttribute(types.AttributeKeyValidator, m.Validator),
		sdk.NewAttribute(types.AttributeKeyDerth, d.String()),
		sdk.NewAttribute(types.AttributeKeyValue, u.String()),
		sdk.NewAttribute(types.AttributeKeyDenom, denom),
	))
	return &types.MsgUndelegateResponse{Denom: denom, Value: u.Uint64(), Position: pos}, nil
}

// ClaimUnbonding returns the claim the private ante already executed
// (executeClaim): a claim is atomic with its spend, so it can pay its fee
// from what it claims.
func (k msgServer) ClaimUnbonding(goCtx context.Context, m *types.MsgClaimUnbonding) (*types.MsgClaimUnbondingResponse, error) {
	res, executed, err := shieldedkeeper.AuthorizedResult(goCtx, m)
	if err != nil {
		return nil, err
	}
	r, ok := res.(*types.MsgClaimUnbondingResponse)
	if !executed || !ok {
		return nil, shieldedtypes.ErrUnauthorized.Wrap("the claim was not executed by the private ante")
	}
	return r, nil
}

// executeClaim: amount of the owner's unbond/v/e claim notes is spent (any
// change back to them) and ERTH = amount x payout / requested is paid, so a
// slash of the unbonding entry reaches every claimant pro rata:
// fee_from_output to fee_collector, the rest minted as an ordinary note to pc
// in the shielded pool. Runs in the private ante; any error fails the whole
// tx.
func (k Keeper) executeClaim(ctx sdk.Context, m *types.MsgClaimUnbonding) (*types.MsgClaimUnbondingResponse, error) {
	r, pay, err := k.checkClaim(ctx, m)
	if err != nil {
		return nil, err
	}
	if _, err := k.applyStakeProof(ctx, &m.Stake); err != nil {
		return nil, err
	}
	claimed := math.NewIntFromUint64(m.Amount)
	r.Outstanding = r.Outstanding.Sub(claimed)
	r.Paid = r.Paid.Add(pay)
	note := pay
	if m.FeeFromOutput > 0 {
		fee := math.NewIntFromUint64(m.FeeFromOutput)
		if err := k.shielded.PayFeeFromModule(ctx, types.ModuleName, fee); err != nil {
			return nil, err
		}
		note = pay.Sub(fee)
	}
	var pos uint64
	if note.IsPositive() {
		if pos, _, err = k.shielded.MintNote(ctx, types.ModuleName, sdk.NewCoin(types.BondDenom, note), m.Pc, m.Ciphertext); err != nil {
			return nil, err
		}
	}
	key := collections.Join(m.Validator, m.Epoch)
	if r.Outstanding.IsZero() {
		// Every claim is in: the floor division's dust goes to the community
		// pool, and the record is done.
		if dust := r.Payout.Sub(r.Paid); dust.IsPositive() {
			if err := k.distr.FundCommunityPool(ctx, sdk.NewCoins(sdk.NewCoin(types.BondDenom, dust)), k.modAddr); err != nil {
				return nil, err
			}
		}
		if err := k.UnbondRecords.Remove(ctx, key); err != nil {
			return nil, err
		}
	} else if err := k.UnbondRecords.Set(ctx, key, r); err != nil {
		return nil, err
	}
	ctx.EventManager().EmitEvent(sdk.NewEvent(types.EventTypeClaim,
		sdk.NewAttribute(types.AttributeKeyDenom, m.StakeDenom()),
		sdk.NewAttribute(types.AttributeKeyValue, claimed.String()),
		sdk.NewAttribute(types.AttributeKeyAmount, pay.String()),
	))
	return &types.MsgClaimUnbondingResponse{Amount: note.Uint64(), Position: pos}, nil
}

// StakeVote records the spent notes' vote and mints their derth straight back
// to a new stake note of the same owner. Final: the spent nullifiers are
// spent, and the new note is not in the snapshot root.
func (k msgServer) StakeVote(goCtx context.Context, m *types.MsgStakeVote) (*types.MsgStakeVoteResponse, error) {
	ctx, err := k.authorized(goCtx, m)
	if err != nil {
		return nil, err
	}
	d, err := k.checkStakeVote(ctx, m)
	if err != nil {
		return nil, err
	}
	if _, err := k.applyStakeProof(ctx, &m.Stake); err != nil {
		return nil, err
	}
	v := types.StakeVote{
		ProposalId: m.ProposalId, Key: append([]byte{0}, m.Stake.SpentNullifiers()[0]...), Validator: m.Validator,
		Derth: d, Options: m.Options,
	}
	if err := k.putVote(ctx, v); err != nil {
		return nil, err
	}
	pos, err := k.mintStake(ctx, types.DerthDenom(m.Validator), d, m.Stake.SpcMint)
	if err != nil {
		return nil, err
	}
	return &types.MsgStakeVoteResponse{Position: pos}, nil
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
		Id: id, Validator: m.Validator, Derth: math.NewIntFromUint64(m.Amount), Splits: m.Splits,
		OwnerTag: m.Stake.OwnerTag, CreatedHeight: ctx.BlockHeight(), Weight: math.ZeroInt(),
	}
	if err := k.setPosition(ctx, p); err != nil {
		return nil, err
	}
	if err := k.PositionsByVal.Set(ctx, collections.Join(p.Validator, id)); err != nil {
		return nil, err
	}
	if err := k.addPositionCount(ctx, 1); err != nil {
		return nil, err
	}
	if len(m.Splits) > 0 {
		w, err := k.allocation.ApplySplit(ctx, allocationtypes.STREAM_ID_GROUNDWORKS, types.PositionVoterKey(id), m.Splits)
		if err != nil {
			return nil, err
		}
		p.Weight = w
		if err := k.setPosition(ctx, p); err != nil {
			return nil, err
		}
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
	w, err := k.allocation.ApplySplit(ctx, allocationtypes.STREAM_ID_GROUNDWORKS, types.PositionVoterKey(p.Id), m.Splits)
	if err != nil {
		return nil, err
	}
	p.Splits, p.Weight = m.Splits, w
	if len(m.Splits) == 0 {
		p.Weight = math.ZeroInt()
	}
	if err := k.setPosition(ctx, p); err != nil {
		return nil, err
	}
	k.positionEvent(ctx, "update", p)
	return &types.MsgUpdatePositionResponse{}, nil
}

// UnlockPosition closes a position; its derth goes back to a stake note of
// its owner.
func (k msgServer) UnlockPosition(goCtx context.Context, m *types.MsgUnlockPosition) (*types.MsgUnlockPositionResponse, error) {
	ctx, err := k.authorized(goCtx, m)
	if err != nil {
		return nil, err
	}
	p, err := k.checkPositionOwner(ctx, m.PositionId, &m.Stake)
	if err != nil {
		return nil, err
	}
	if err := k.allocation.RemoveVoter(ctx, allocationtypes.STREAM_ID_GROUNDWORKS, types.PositionVoterKey(p.Id)); err != nil {
		return nil, err
	}
	pos, err := k.mintStake(ctx, types.DerthDenom(p.Validator), p.Derth, m.Stake.SpcMint)
	if err != nil {
		return nil, err
	}
	if err := k.Positions.Remove(ctx, p.Id); err != nil {
		return nil, err
	}
	if err := k.PositionsByVal.Remove(ctx, collections.Join(p.Validator, p.Id)); err != nil {
		return nil, err
	}
	if err := k.addPositionCount(ctx, -1); err != nil {
		return nil, err
	}
	k.positionEvent(ctx, "unlock", p)
	return &types.MsgUnlockPositionResponse{Position: pos}, nil
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
	ctx.EventManager().EmitEvent(sdk.NewEvent(types.EventTypePosition,
		sdk.NewAttribute(types.AttributeKeyAction, action),
		sdk.NewAttribute(types.AttributeKeyPosition, strconv.FormatUint(p.Id, 10)),
		sdk.NewAttribute(types.AttributeKeyValidator, p.Validator),
		sdk.NewAttribute(types.AttributeKeyDerth, p.Derth.String()),
		sdk.NewAttribute(types.AttributeKeyWeight, p.Weight.String()),
	))
}

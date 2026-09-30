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
	return &types.MsgUpdateParamsResponse{}, k.Params.Set(ctx, req.Params)
}

// mintTo mints coin into this module and appends it as a note to pc.
func (k Keeper) mintTo(ctx context.Context, coin sdk.Coin, pc, ct []byte) (uint64, error) {
	if err := k.bank.MintCoins(ctx, types.ModuleName, sdk.NewCoins(coin)); err != nil {
		return 0, err
	}
	pos, _, err := k.shielded.MintNote(ctx, types.ModuleName, coin, pc, ct)
	return pos, err
}

// Delegate: the ERTH joins the validator's queue, and derth/v is minted at the
// live rate to pc.
func (k msgServer) Delegate(goCtx context.Context, m *types.MsgDelegate) (*types.MsgDelegateResponse, error) {
	ctx, err := k.authorized(goCtx, m)
	if err != nil {
		return nil, err
	}
	d, err := k.checkDelegate(ctx, m)
	if err != nil {
		return nil, err
	}
	paid, err := k.shielded.SpendToModule(ctx, &m.Transfer, types.ModuleName)
	if err != nil {
		return nil, err
	}
	vs, err := k.ValidatorState(ctx, m.Validator)
	if err != nil {
		return nil, err
	}
	vs.PendingDelegation = vs.PendingDelegation.Add(paid.Amount)
	if err := k.Validators.Set(ctx, m.Validator, vs); err != nil {
		return nil, err
	}
	denom := types.DerthDenom(m.Validator)
	if _, err := k.shielded.RegisterAsset(ctx, denom); err != nil {
		return nil, err
	}
	pos, err := k.mintTo(ctx, sdk.NewCoin(denom, d), m.Pc, m.Ciphertext)
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

// Undelegate: derth/v is burned and an unbond/v/e note of its live value is
// minted; the value joins this epoch's undelegation for v.
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
	derth, err := k.shielded.SpendToModule(ctx, &m.Transfer, types.ModuleName)
	if err != nil {
		return nil, err
	}
	if err := k.bank.BurnCoins(ctx, types.ModuleName, sdk.NewCoins(derth)); err != nil {
		return nil, err
	}
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
	vs.PendingUndelegation = vs.PendingUndelegation.Add(u)
	if err := k.Validators.Set(ctx, m.Validator, vs); err != nil {
		return nil, err
	}
	denom := types.UnbondDenom(m.Validator, epoch.Number)
	if _, err := k.shielded.RegisterAsset(ctx, denom); err != nil {
		return nil, err
	}
	pos, err := k.mintTo(ctx, sdk.NewCoin(denom, u), m.Pc, m.Ciphertext)
	if err != nil {
		return nil, err
	}
	ctx.EventManager().EmitEvent(sdk.NewEvent(types.EventTypeUndelegate,
		sdk.NewAttribute(types.AttributeKeyValidator, m.Validator),
		sdk.NewAttribute(types.AttributeKeyDerth, derth.Amount.String()),
		sdk.NewAttribute(types.AttributeKeyValue, u.String()),
		sdk.NewAttribute(types.AttributeKeyDenom, denom),
	))
	return &types.MsgUndelegateResponse{Denom: denom, Value: u.Uint64(), Position: pos}, nil
}

// ClaimUnbonding: the unbond notes are burned and ERTH = value x payout /
// requested is minted as a note, so a slash of the unbonding entry reaches
// every claimant pro rata.
func (k msgServer) ClaimUnbonding(goCtx context.Context, m *types.MsgClaimUnbonding) (*types.MsgClaimUnbondingResponse, error) {
	ctx, err := k.authorized(goCtx, m)
	if err != nil {
		return nil, err
	}
	r, pay, err := k.checkClaim(ctx, m)
	if err != nil {
		return nil, err
	}
	claim, err := k.shielded.SpendToModule(ctx, &m.Transfer, types.ModuleName)
	if err != nil {
		return nil, err
	}
	if err := k.bank.BurnCoins(ctx, types.ModuleName, sdk.NewCoins(claim)); err != nil {
		return nil, err
	}
	r.Outstanding = r.Outstanding.Sub(claim.Amount)
	r.Paid = r.Paid.Add(pay)
	var pos uint64
	if pay.IsPositive() {
		if pos, _, err = k.shielded.MintNote(ctx, types.ModuleName, sdk.NewCoin(types.BondDenom, pay), m.Pc, m.Ciphertext); err != nil {
			return nil, err
		}
	}
	key := collections.Join(m.Validator, m.Epoch)
	if r.Outstanding.IsZero() {
		// Every note is in: the floor division's dust goes to the community
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
		sdk.NewAttribute(types.AttributeKeyDenom, claim.Denom),
		sdk.NewAttribute(types.AttributeKeyValue, claim.Amount.String()),
		sdk.NewAttribute(types.AttributeKeyAmount, pay.String()),
	))
	return &types.MsgClaimUnbondingResponse{Amount: pay.Uint64(), Position: pos}, nil
}

// StakeVote records (or replaces) a note's vote.
func (k msgServer) StakeVote(goCtx context.Context, m *types.MsgStakeVote) (*types.MsgStakeVoteResponse, error) {
	ctx, err := k.authorized(goCtx, m)
	if err != nil {
		return nil, err
	}
	if _, err := k.checkStakeVote(ctx, m); err != nil {
		return nil, err
	}
	v := types.StakeVote{
		ProposalId: m.ProposalId, Key: append([]byte{0}, m.VoteNullifier...), Validator: m.Validator,
		Derth: math.NewIntFromUint64(m.Value), Options: m.Options,
	}
	if err := k.putVote(ctx, v); err != nil {
		return nil, err
	}
	return &types.MsgStakeVoteResponse{}, nil
}

// LockPosition moves derth from a note into a new position.
func (k msgServer) LockPosition(goCtx context.Context, m *types.MsgLockPosition) (*types.MsgLockPositionResponse, error) {
	ctx, err := k.authorized(goCtx, m)
	if err != nil {
		return nil, err
	}
	if err := k.checkLock(ctx, m); err != nil {
		return nil, err
	}
	derth, err := k.shielded.SpendToModule(ctx, &m.Transfer, types.ModuleName)
	if err != nil {
		return nil, err
	}
	id, err := k.PositionSeq.Next(ctx)
	if err != nil {
		return nil, err
	}
	p := types.Position{
		Id: id, Validator: m.Validator, Derth: derth.Amount, Splits: m.Splits, Pubkey: m.Pubkey,
		CreatedHeight: ctx.BlockHeight(), Weight: math.ZeroInt(),
	}
	if err := k.setPosition(ctx, p); err != nil {
		return nil, err
	}
	if err := k.PositionsByVal.Set(ctx, collections.Join(p.Validator, id)); err != nil {
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
	p, err := k.checkPositionSig(ctx, m.PositionId, "update", m.SignPayload(), m.Signature)
	if err != nil {
		return nil, err
	}
	w, err := k.allocation.ApplySplit(ctx, allocationtypes.STREAM_ID_GROUNDWORKS, types.PositionVoterKey(p.Id), m.Splits)
	if err != nil {
		return nil, err
	}
	p.Splits, p.Weight, p.Nonce = m.Splits, w, p.Nonce+1
	if len(m.Splits) == 0 {
		p.Weight = math.ZeroInt()
	}
	if err := k.setPosition(ctx, p); err != nil {
		return nil, err
	}
	k.positionEvent(ctx, "update", p)
	return &types.MsgUpdatePositionResponse{}, nil
}

// UnlockPosition closes a position; its derth goes back to a note.
func (k msgServer) UnlockPosition(goCtx context.Context, m *types.MsgUnlockPosition) (*types.MsgUnlockPositionResponse, error) {
	ctx, err := k.authorized(goCtx, m)
	if err != nil {
		return nil, err
	}
	p, err := k.checkPositionSig(ctx, m.PositionId, "unlock", m.SignPayload(), m.Signature)
	if err != nil {
		return nil, err
	}
	if err := k.allocation.RemoveVoter(ctx, allocationtypes.STREAM_ID_GROUNDWORKS, types.PositionVoterKey(p.Id)); err != nil {
		return nil, err
	}
	pos, _, err := k.shielded.MintNote(ctx, types.ModuleName, sdk.NewCoin(types.DerthDenom(p.Validator), p.Derth), m.Pc, m.Ciphertext)
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
	p.Nonce++
	if err := k.setPosition(ctx, p); err != nil {
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

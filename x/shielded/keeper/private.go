package keeper

import (
	"context"
	"errors"

	"cosmossdk.io/collections"
	errorsmod "cosmossdk.io/errors"
	"cosmossdk.io/math"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/x/shielded/types"
	"github.com/earth-network/earth/zk/privacy"
	"github.com/earth-network/earth/zk/ultrahonk"
)

// The private ante (x/shielded/ante) drives a private msg through these, in
// order: CheckPrivateMsg (state only, cheap), VerifyPrivateMsg (the proof),
// ExecutePrivateMsg (spend, append, pay). Each is exported for the ante
// alone; other modules use MintNote, SpendToModule and AuthorizedNullifiers.

// PreparedPrivateMsg is what CheckPrivateMsg derived for verification.
type PreparedPrivateMsg struct {
	Msg      types.PrivateMsg
	AssetPub fr.Element
	Signal   fr.Element
}

// CheckPrivateMsg runs every stateful check on a private msg that can be run
// before its proof: the anchor, the nullifiers, the asset, the room left in
// the tree and, for an unshield, whether the bank would pay the receiver.
// Anything that could make the msg fail after the ante spends its inputs is
// refused here instead.
func (k Keeper) CheckPrivateMsg(ctx context.Context, msg types.PrivateMsg) (PreparedPrivateMsg, error) {
	t := msg.PrivateTransfer()
	if err := k.checkAnchor(ctx, t.Root); err != nil {
		return PreparedPrivateMsg{}, err
	}
	for _, nf := range t.Nullifiers {
		spent, err := k.Nullifiers.Has(ctx, nf)
		if err != nil {
			return PreparedPrivateMsg{}, err
		}
		if spent {
			return PreparedPrivateMsg{}, types.ErrNullifierSpent.Wrapf("%X", nf)
		}
	}
	var assetPub fr.Element // 0 unless value leaves the pool
	if t.ValueOut > 0 {
		id, err := k.AssetID(ctx, t.DenomOut)
		if err != nil {
			return PreparedPrivateMsg{}, err
		}
		if assetPub, err = privacy.FieldFromBytes(id); err != nil {
			return PreparedPrivateMsg{}, err
		}
	}
	if err := k.checkCapacity(ctx, types.TransferArity); err != nil {
		return PreparedPrivateMsg{}, err
	}
	if m, ok := msg.(*types.MsgTransfer); ok && t.ValueOut > 0 {
		recv, err := m.ReceiverBytes(k.addressCodec)
		if err != nil {
			return PreparedPrivateMsg{}, err
		}
		coin := sdk.NewCoin(t.DenomOut, math.NewIntFromUint64(t.ValueOut))
		if err := k.checkUnshield(ctx, coin, recv); err != nil {
			return PreparedPrivateMsg{}, err
		}
	}
	signal, err := msg.Signal(sdk.UnwrapSDKContext(ctx).ChainID(), k.addressCodec)
	if err != nil {
		return PreparedPrivateMsg{}, err
	}
	return PreparedPrivateMsg{Msg: msg, AssetPub: assetPub, Signal: signal}, nil
}

// VerifyPrivateMsg verifies the transfer proof against the public inputs the
// chain computed: the msg's own fields, asset_pub from the registry and the
// signal from the msg.
func (k Keeper) VerifyPrivateMsg(ctx context.Context, p PreparedPrivateMsg) error {
	params, err := k.Params.Get(ctx)
	if err != nil {
		return err
	}
	vk := params.VerifyingKeys[types.CircuitTransfer]
	if len(vk) == 0 {
		return types.ErrMissingVerifyingKey.Wrap(types.CircuitTransfer)
	}
	t := p.Msg.PrivateTransfer()
	ok, err := ultrahonk.Verify(vk, t.Proof, t.PublicInputs(p.AssetPub, p.Signal))
	if err != nil {
		return errorsmod.Wrap(types.ErrInvalidProof, err.Error())
	}
	if !ok {
		return types.ErrInvalidProof
	}
	return nil
}

// ExecutePrivateMsg spends the transfer's nullifiers, appends its outputs and
// pays its fee, and returns ctx carrying the authorization the msg's handler
// requires.
func (k Keeper) ExecutePrivateMsg(ctx sdk.Context, msg types.PrivateMsg) (sdk.Context, error) {
	t := msg.PrivateTransfer()
	positions, err := k.executeTransfer(ctx, t)
	if err != nil {
		return ctx, err
	}
	if err := k.payFee(ctx, t.FeeInt()); err != nil {
		return ctx, err
	}
	return WithAuthorizedTransfer(ctx, t, positions), nil
}

// CountPrivateTx admits one more private tx into the current block, or
// refuses once max_private_txs_per_block have run. EndBlock resets it.
func (k Keeper) CountPrivateTx(ctx context.Context) error {
	params, err := k.Params.Get(ctx)
	if err != nil {
		return err
	}
	n, err := k.PrivateTxCount.Get(ctx)
	if errors.Is(err, collections.ErrNotFound) {
		n = 0
	} else if err != nil {
		return err
	}
	if n >= uint64(params.MaxPrivateTxsPerBlock) {
		return types.ErrBlockCap.Wrapf("%d", params.MaxPrivateTxsPerBlock)
	}
	return k.PrivateTxCount.Set(ctx, n+1)
}

// MinFee is the consensus floor on a private tx's fee, never below 1.
func (k Keeper) MinFee(ctx context.Context) (sdk.Coin, error) {
	params, err := k.Params.Get(ctx)
	if err != nil {
		return sdk.Coin{}, err
	}
	minFee := params.MinFee
	if minFee.IsNil() || !minFee.IsPositive() {
		minFee = math.OneInt()
	}
	return sdk.NewCoin(types.FeeDenom, minFee), nil
}

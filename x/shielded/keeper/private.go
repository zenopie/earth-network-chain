package keeper

import (
	"context"
	"errors"

	"cosmossdk.io/collections"
	errorsmod "cosmossdk.io/errors"
	"cosmossdk.io/math"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"

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
	Msg       types.PrivateMsg
	Transfers []*types.Transfer
	// AssetPubs[i] is Transfers[i]'s asset_pub.
	AssetPubs []fr.Element
	Signal    fr.Element
}

// CheckPrivateMsg runs every stateful check on a private msg that can be run
// before its proofs, for each of its transfers: the anchor, the nullifiers,
// the asset, the room left in the tree and, for an unshield, whether the bank
// would pay the receiver. Anything that could make the msg fail after the
// ante spends its inputs is refused here instead.
func (k Keeper) CheckPrivateMsg(ctx context.Context, msg types.PrivateMsg) (PreparedPrivateMsg, error) {
	ts := types.TransfersOf(msg)
	p := PreparedPrivateMsg{Msg: msg, Transfers: ts}
	for i, t := range ts {
		// Only the primary transfer may be vouched for outside the window
		// (a stake vote's snapshot root); any other (the fee transfer paying
		// for it) spends against a current root.
		var err error
		if i == 0 {
			err = k.checkPrivateAnchor(ctx, msg, t.Root)
		} else {
			err = k.checkAnchor(ctx, t.Root)
		}
		if err != nil {
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
		p.AssetPubs = append(p.AssetPubs, assetPub)
	}
	// Every transfer's outputs, plus a note the action may mint.
	if err := k.checkCapacity(ctx, uint64(types.TransferArity*len(ts)+1)); err != nil {
		return PreparedPrivateMsg{}, err
	}
	if m, ok := msg.(*types.MsgTransfer); ok && m.Transfer.ValueOut > 0 {
		recv, err := m.ReceiverBytes(k.addressCodec)
		if err != nil {
			return PreparedPrivateMsg{}, err
		}
		coin := sdk.NewCoin(m.Transfer.DenomOut, math.NewIntFromUint64(m.Transfer.ValueOut-m.FeeFromOutput))
		if err := k.checkUnshield(ctx, coin, recv); err != nil {
			return PreparedPrivateMsg{}, err
		}
	}
	signal, err := msg.Signal(sdk.UnwrapSDKContext(ctx).ChainID(), k.addressCodec)
	if err != nil {
		return PreparedPrivateMsg{}, err
	}
	p.Signal = signal
	return p, nil
}

// checkPrivateAnchor is checkAnchor, except that a msg whose action handler
// implements types.PrivateAnchorAcceptor may vouch for a root outside the
// window (a stake vote's proposal snapshot root).
func (k Keeper) checkPrivateAnchor(ctx context.Context, msg types.PrivateMsg, root []byte) error {
	err := k.checkAnchor(ctx, root)
	if err == nil || !errors.Is(err, types.ErrUnknownRoot) {
		return err
	}
	h, ok := k.PrivateAction(msg)
	if !ok {
		return err
	}
	acc, ok := h.(types.PrivateAnchorAcceptor)
	if !ok {
		return err
	}
	accepted, aerr := acc.AcceptsPrivateAnchor(ctx, msg, root)
	if aerr != nil {
		return aerr
	}
	if !accepted {
		return err
	}
	return nil
}

// VerifyPrivateMsg verifies each transfer's proof against the public inputs
// the chain computed: the transfer's own fields, its asset_pub from the
// registry and the msg's one signal.
func (k Keeper) VerifyPrivateMsg(ctx context.Context, p PreparedPrivateMsg) error {
	params, err := k.Params.Get(ctx)
	if err != nil {
		return err
	}
	vk := params.VerifyingKeys[types.CircuitTransfer]
	if len(vk) == 0 {
		return types.ErrMissingVerifyingKey.Wrap(types.CircuitTransfer)
	}
	for i, t := range p.Transfers {
		ok, err := ultrahonk.Verify(vk, t.Proof, t.PublicInputs(p.AssetPubs[i], p.Signal))
		if err != nil {
			return errorsmod.Wrapf(types.ErrInvalidProof, "transfer %d: %s", i, err.Error())
		}
		if !ok {
			return types.ErrInvalidProof.Wrapf("transfer %d", i)
		}
	}
	return nil
}

// ExecutePrivateMsg spends every transfer's nullifiers, appends its outputs
// and pays its fee, and returns ctx carrying the authorization the msg's
// handler requires. An unshield paying its fee from its output pays it here,
// out of the uerth leaving the pool; the receiver is paid the rest.
func (k Keeper) ExecutePrivateMsg(ctx sdk.Context, msg types.PrivateMsg) (sdk.Context, error) {
	ts := types.TransfersOf(msg)
	positions := make([][]uint64, len(ts))
	for i, t := range ts {
		pos, err := k.executeTransfer(ctx, t)
		if err != nil {
			return ctx, err
		}
		positions[i] = pos
		if t.Fee > 0 {
			if err := k.payFee(ctx, t.FeeInt()); err != nil {
				return ctx, err
			}
		}
	}
	ctx = WithAuthorizedTransfers(ctx, ts, positions, types.FeeFromOutputOf(msg))
	if m, ok := msg.(*types.MsgTransfer); ok && m.FeeFromOutput > 0 {
		if err := k.withholdFee(ctx, &m.Transfer, m.FeeFromOutput); err != nil {
			return ctx, err
		}
	}
	return ctx, nil
}

// withholdFee pays fee out of t's released uerth, before the handler pays the
// rest of it out.
func (k Keeper) withholdFee(ctx sdk.Context, t *types.Transfer, fee uint64) error {
	a, at, err := authorizedFor(ctx, t)
	if err != nil {
		return err
	}
	if t.DenomOut != types.FeeDenom || at.value <= fee {
		return errorsmod.Wrap(types.ErrInvalidTransfer, "fee from output exceeds the unshield")
	}
	if err := k.payFee(ctx, math.NewIntFromUint64(fee)); err != nil {
		return err
	}
	at.withheld += fee
	a.feePaid += fee
	return nil
}

// ExecutePrivateAction runs msg's action in the ante, if its handler asks to
// (types.PrivateActionExecutor), and then requires the msg's fee from output,
// if any, paid in full. ctx must carry the authorization ExecutePrivateMsg
// made and the action's prepared value.
func (k Keeper) ExecutePrivateAction(ctx sdk.Context, msg types.PrivateMsg, prepared any) error {
	if h, ok := k.PrivateAction(msg); ok {
		if ex, ok := h.(types.PrivateActionExecutor); ok && ex.ExecutesInAnte(msg) {
			result, err := ex.ExecutePrivateAction(ctx, msg, prepared)
			if err != nil {
				return err
			}
			withExecutedAction(ctx, result)
		}
	}
	a, ok := authorizationOf(ctx)
	if !ok {
		return types.ErrUnauthorized
	}
	if a.feePaid != a.feeFromOutput {
		return errorsmod.Wrapf(sdkerrors.ErrInsufficientFee, "fee from output: %d%s owed, %d paid", a.feeFromOutput, types.FeeDenom, a.feePaid)
	}
	return nil
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

// VerifyCircuit verifies proof against the verifying key params hold for
// circuit (types.CircuitMembership, ...), for the modules whose private
// actions carry a proof of their own. ErrMissingVerifyingKey until governance
// or genesis sets that key; ErrInvalidProof for a proof that does not verify.
func (k Keeper) VerifyCircuit(ctx context.Context, circuit string, proof []byte, publicInputs [][]byte) error {
	params, err := k.Params.Get(ctx)
	if err != nil {
		return err
	}
	vk := params.VerifyingKeys[circuit]
	if len(vk) == 0 {
		return types.ErrMissingVerifyingKey.Wrap(circuit)
	}
	if len(proof) == 0 || len(proof) > types.MaxProofBytes {
		return errorsmod.Wrapf(types.ErrInvalidProof, "proof must be 1..%d bytes", types.MaxProofBytes)
	}
	ok, err := ultrahonk.Verify(vk, proof, publicInputs)
	if err != nil {
		return errorsmod.Wrap(types.ErrInvalidProof, err.Error())
	}
	if !ok {
		return types.ErrInvalidProof
	}
	return nil
}

// PrivateGasPrices is what the pool charges for one proof verification and
// one note write, for a private action pricing its own work the same way.
func (k Keeper) PrivateGasPrices(ctx context.Context) (proof, note uint64, err error) {
	params, err := k.Params.Get(ctx)
	if err != nil {
		return 0, 0, err
	}
	return params.ProofVerificationGas, params.NoteGas, nil
}

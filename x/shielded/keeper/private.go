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
	"github.com/earth-network/earth/zk/orchard"
)

// The private ante (x/shielded/ante) drives a private msg through these, in
// order: CheckPrivateMsg (state only, cheap), VerifyPrivateMsg (binding
// signatures, then every action proof), ExecutePrivateMsg (spend, append,
// pay, unshield). Each is exported for the ante alone; other modules use
// MintNote, ReleaseToModule and AuthorizedAction.

// PreparedPrivateMsg is what CheckPrivateMsg derived for verification.
type PreparedPrivateMsg struct {
	Msg types.PrivateMsg
	// Bundles are the msg's bundles in zk/orchard's terms, in order.
	Bundles []*orchard.Bundle
	// Sighash is what every action proof binds and every binding signature
	// signs.
	Sighash fr.Element
}

// CheckPrivateMsg runs every stateful check on a private msg that can be run
// before its proofs: the bundle size cap, every action's anchor, every
// nullifier, every balance's asset, the room left in the tree, and the
// release map (an unshield the bank would refuse, value with nowhere to go).
// Anything that could make the msg fail after the ante spends its inputs is
// refused here instead.
func (k Keeper) CheckPrivateMsg(ctx context.Context, msg types.PrivateMsg) (PreparedPrivateMsg, error) {
	params, err := k.Params.Get(ctx)
	if err != nil {
		return PreparedPrivateMsg{}, err
	}
	bs := msg.PrivateBundles()
	p := PreparedPrivateMsg{Msg: msg, Bundles: make([]*orchard.Bundle, len(bs))}
	for i, b := range bs {
		if len(b.Actions) > int(params.MaxActionsPerBundle) {
			return PreparedPrivateMsg{}, errorsmod.Wrapf(types.ErrInvalidBundle,
				"bundle %d: %d actions, max_actions_per_bundle is %d", i, len(b.Actions), params.MaxActionsPerBundle)
		}
	}
	// Anchors: every action, dummies included, spends against an anchor in
	// the pool's window.
	valid := map[string]bool{}
	for i, b := range bs {
		for j := range b.Actions {
			root := b.Actions[j].Anchor
			if valid[string(root)] {
				continue
			}
			if err = k.checkAnchor(ctx, root); err != nil {
				return PreparedPrivateMsg{}, errorsmod.Wrapf(err, "bundle %d action %d", i, j)
			}
			valid[string(root)] = true
		}
	}
	for _, b := range bs {
		for _, nf := range b.Nullifiers() {
			spent, err := k.Nullifiers.Has(ctx, nf)
			if err != nil {
				return PreparedPrivateMsg{}, err
			}
			if spent {
				return PreparedPrivateMsg{}, types.ErrNullifierSpent.Wrapf("%X", nf)
			}
		}
		// Only registered denoms can leave: their asset id is AssetID(denom)
		// (the registry is checked to agree), which is what the digest and
		// the binding key use.
		for _, bal := range b.Balances {
			if _, err := k.AssetID(ctx, bal.Denom); err != nil {
				return PreparedPrivateMsg{}, err
			}
		}
	}
	// Every bundle's outputs, plus a note the action may mint.
	if err := k.checkCapacity(ctx, uint64(types.ActionCount(msg)+1)); err != nil {
		return PreparedPrivateMsg{}, err
	}
	if err := k.checkReleaseMap(ctx, msg); err != nil {
		return PreparedPrivateMsg{}, err
	}
	sighash, err := types.SighashOf(ctx, msg, k.addressCodec)
	if err != nil {
		return PreparedPrivateMsg{}, err
	}
	p.Sighash = sighash
	for i, b := range bs {
		if p.Bundles[i], err = b.ToOrchard(); err != nil {
			return PreparedPrivateMsg{}, err
		}
	}
	return p, nil
}

// checkReleaseMap refuses a msg whose released value (types.Remainders) has
// no destination the chain can pay before anything is spent: an unshield's
// receiver must be able to take every coin; a msg with an action handler
// must release exactly the denoms the handler takes to its module
// (ReleasedDenoms); and a msg with neither must release nothing beyond its
// fee.
func (k Keeper) checkReleaseMap(ctx context.Context, msg types.PrivateMsg) error {
	rem, err := types.Remainders(msg)
	if err != nil {
		return err
	}
	if um, ok := msg.(types.UnshieldMsg); ok {
		recv, err := um.UnshieldReceiver(k.addressCodec)
		if err != nil {
			return err
		}
		if (recv == nil) != (len(rem) == 0) {
			return types.ErrReleaseMap.Wrap("a receiver is named exactly when the balances exceed the fee")
		}
		for _, r := range rem {
			if err := k.checkUnshield(ctx, sdk.NewCoin(r.Denom, math.NewIntFromUint64(r.Amount)), recv); err != nil {
				return err
			}
		}
		return nil
	}
	h, ok := k.PrivateAction(msg)
	if !ok {
		if len(rem) > 0 {
			return types.ErrReleaseMap.Wrap("the msg releases value it has nowhere to send")
		}
		return nil
	}
	declared := map[string]bool{}
	for _, d := range h.ReleasedDenoms(msg) {
		declared[d] = true
	}
	released := map[string]bool{}
	for _, r := range rem {
		if !declared[r.Denom] {
			return types.ErrReleaseMap.Wrapf("the msg releases %s, which its action does not take", r.Denom)
		}
		released[r.Denom] = true
	}
	for d := range declared {
		if !released[d] {
			return types.ErrReleaseMap.Wrapf("the msg's action takes %q, which the msg does not release", d)
		}
	}
	return nil
}

// VerifyPrivateMsg verifies, under the msg's one sighash, each bundle's
// binding signature (cheap: the value balance) and then every action proof
// of every bundle. In a block (and simulate) the proofs run in parallel and
// the first failure in bundle and action order is reported, whatever
// finished first. In CheckTx they run one at a time and the first failure
// stops the rest: a mempool tx is free (a tx failing CheckTx pays nothing),
// and anyone can make a binding signature over a forged balance, so a junk
// tx must cost the node one proof verification, not one per action. (Block
// verification is bounded by block gas: every proof is paid for by the
// private gas charge before any is verified, failed txs included.)
func (k Keeper) VerifyPrivateMsg(ctx context.Context, p PreparedPrivateMsg) error {
	params, err := k.Params.Get(ctx)
	if err != nil {
		return err
	}
	vk := params.VerifyingKeys[types.CircuitAction]
	if len(vk) == 0 {
		return types.ErrMissingVerifyingKey.Wrap(types.CircuitAction)
	}
	for i, b := range p.Bundles {
		if err := b.CheckBalance(p.Sighash, orchard.CanonicalBase); err != nil {
			return errorsmod.Wrapf(types.ErrInvalidBindingSig, "bundle %d: %v", i, err)
		}
	}
	verify := func(proof []byte, in [][]byte) (bool, error) { return k.proofVerifier(vk, proof, in) }
	if sdk.UnwrapSDKContext(ctx).IsCheckTx() {
		err = orchard.VerifyProofsSequential(p.Bundles, p.Sighash, verify)
	} else {
		err = orchard.VerifyProofs(p.Bundles, p.Sighash, verify)
	}
	if err != nil {
		return errorsmod.Wrap(types.ErrInvalidProof, err.Error())
	}
	return nil
}

// ExecutePrivateMsg spends every bundle's nullifiers, appends its outputs and
// pays the fee, records the authorization the msg's handler requires, and,
// for an unshield, pays the receiver everything the bundles released beyond
// the fee. It returns ctx carrying the authorization.
func (k Keeper) ExecutePrivateMsg(ctx sdk.Context, msg types.PrivateMsg) (sdk.Context, error) {
	bs := msg.PrivateBundles()
	positions := make([][]uint64, len(bs))
	for i, b := range bs {
		pos, err := k.executeBundle(ctx, b)
		if err != nil {
			return ctx, err
		}
		positions[i] = pos
	}
	if fee := msg.PrivateFee(); fee > 0 {
		if err := k.payFee(ctx, math.NewIntFromUint64(fee)); err != nil {
			return ctx, err
		}
	}
	ctx, err := AuthorizeMsg(ctx, msg, positions, types.FeeFromOutputOf(msg))
	if err != nil {
		return ctx, err
	}
	if um, ok := msg.(types.UnshieldMsg); ok {
		recv, err := um.UnshieldReceiver(k.addressCodec)
		if err != nil {
			return ctx, err
		}
		if recv != nil {
			if err := k.unshield(ctx, msg, recv); err != nil {
				return ctx, err
			}
		}
	}
	return ctx, nil
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

// CountPrivateActions admits n more actions into the current block, or
// refuses once max_private_actions_per_block would be exceeded. EndBlock
// resets the count.
func (k Keeper) CountPrivateActions(ctx context.Context, n uint64) error {
	params, err := k.Params.Get(ctx)
	if err != nil {
		return err
	}
	c, err := k.PrivateActionCount.Get(ctx)
	if errors.Is(err, collections.ErrNotFound) {
		c = 0
	} else if err != nil {
		return err
	}
	if c+n > uint64(params.MaxPrivateActionsPerBlock) {
		return types.ErrBlockCap.Wrapf("%d actions in this block, %d more exceed %d", c, n, params.MaxPrivateActionsPerBlock)
	}
	return k.PrivateActionCount.Set(ctx, c+n)
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
	if err := types.CheckProofLength(proof); err != nil {
		return errorsmod.Wrap(types.ErrInvalidProof, err.Error())
	}
	ok, err := k.proofVerifier(vk, proof, publicInputs)
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

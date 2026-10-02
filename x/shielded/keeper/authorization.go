package keeper

import (
	"context"
	"crypto/sha256"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/cosmos/gogoproto/proto"

	"github.com/earth-network/earth/x/shielded/types"
)

// The private ante, having verified a msg's bundles and executed their note
// side (nullifiers spent, outputs appended, fee paid), records that in the
// tx's context. A private msg's handler refuses unless the msg is recorded
// there.
//
// This is the defence against zero-signer msgs arriving any other way. A
// contract's CosmosMsg::Any, an ICA host tx, a group proposal: each authorizes
// a msg by checking that every signer is the caller, and a msg with no
// signers passes that loop vacuously. None of them runs the private ante, so
// none of them carries this value.
//
// The record is keyed by the msg's bytes (SHA-256 of its proto encoding), so a
// handler can only consume the authorization of the very msg the ante
// verified, field for field.

type authorizedKey struct{}

// authorization is everything the private ante accepted in this tx.
type authorization struct {
	// key is msgKey of the msg the ante verified.
	key        [32]byte
	nullifiers map[string]bool
	// positions[b] is where bundle b's outputs were appended, in action order.
	positions [][]uint64
	// remaining is, per denom, the value released from the pool (the bundles'
	// balances less the fee) and not yet paid out. Each denom is paid once,
	// whole, to one destination.
	remaining map[string]uint64
	// action is what the msg's PrivateActionHandler prepared, nil for a msg
	// with no action.
	action any
	// executed is set once the ante ran the action itself
	// (PrivateActionExecutor), and result is what it returned.
	executed bool
	result   any
	// feeFromOutput is what the msg owes from its output; feePaid what
	// PayFeeFromModule has paid of it.
	feeFromOutput uint64
	feePaid       uint64
}

// msgKey identifies msg by its bytes.
func msgKey(msg types.PrivateMsg) ([32]byte, error) {
	bz, err := proto.Marshal(msg)
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(bz), nil
}

// AuthorizeMsg records msg as verified and executed by the private ante, its
// bundles' outputs appended at positions, owing feeFromOutput from its
// output. Only the ante calls this (and tests standing in for it).
func AuthorizeMsg(ctx sdk.Context, msg types.PrivateMsg, positions [][]uint64, feeFromOutput uint64) (sdk.Context, error) {
	key, err := msgKey(msg)
	if err != nil {
		return ctx, err
	}
	rem, err := types.Remainders(msg)
	if err != nil {
		return ctx, err
	}
	a := &authorization{key: key, nullifiers: map[string]bool{}, positions: positions,
		remaining: map[string]uint64{}, feeFromOutput: feeFromOutput}
	for _, b := range msg.PrivateBundles() {
		for _, nf := range b.Nullifiers() {
			a.nullifiers[string(nf)] = true
		}
	}
	for _, r := range rem {
		a.remaining[r.Denom] = r.Amount
	}
	return ctx.WithValue(authorizedKey{}, a), nil
}

func authorizationOf(ctx context.Context) (*authorization, bool) {
	a, ok := sdk.UnwrapSDKContext(ctx).Value(authorizedKey{}).(*authorization)
	return a, ok && a != nil
}

// authorizedFor returns msg's authorization, or ErrUnauthorized.
func authorizedFor(ctx context.Context, msg types.PrivateMsg) (*authorization, error) {
	a, ok := authorizationOf(ctx)
	if !ok {
		return nil, types.ErrUnauthorized
	}
	key, err := msgKey(msg)
	if err != nil {
		return nil, err
	}
	if key != a.key {
		return nil, types.ErrUnauthorized.Wrap("msg differs from the one the ante executed")
	}
	return a, nil
}

// AuthorizedNullifiers reports whether the private ante verified and spent
// every one of nfs in this tx. False for an empty list.
func AuthorizedNullifiers(ctx context.Context, nfs ...[]byte) bool {
	a, ok := authorizationOf(ctx)
	if !ok || len(nfs) == 0 {
		return false
	}
	for _, nf := range nfs {
		if !a.nullifiers[string(nf)] {
			return false
		}
	}
	return true
}

// WithAuthorizedAction records what the msg's action handler prepared on the
// authorization the ante just made. Only the ante calls this (and tests).
func WithAuthorizedAction(ctx sdk.Context, prepared any) sdk.Context {
	if a, ok := authorizationOf(ctx); ok {
		a.action = prepared
	}
	return ctx
}

// withExecutedAction records that the ante ran the action and what it
// returned.
func withExecutedAction(ctx sdk.Context, result any) {
	if a, ok := authorizationOf(ctx); ok {
		a.executed, a.result = true, result
	}
}

// AuthorizedAction returns what msg's PrivateActionHandler prepared, once the
// private ante has verified and executed msg's bundles and the action's
// proofs in this tx; ErrUnauthorized otherwise. A private action's handler
// calls this before anything else.
func AuthorizedAction(ctx context.Context, msg types.PrivateMsg) (any, error) {
	a, err := authorizedFor(ctx, msg)
	if err != nil {
		return nil, err
	}
	if a.action == nil {
		return nil, types.ErrUnauthorized.Wrap("no private action was checked for this msg")
	}
	return a.action, nil
}

// AuthorizedResult returns what the ante's run of msg's action returned
// (PrivateActionExecutor), and whether it ran it. A handler whose action the
// ante executes returns this and does nothing else.
func AuthorizedResult(ctx context.Context, msg types.PrivateMsg) (any, bool, error) {
	a, err := authorizedFor(ctx, msg)
	if err != nil {
		return nil, false, err
	}
	if a.action == nil {
		return nil, false, types.ErrUnauthorized.Wrap("no private action was checked for this msg")
	}
	return a.result, a.executed, nil
}

// AuthorizedPositions returns where the ante appended msg's outputs, per
// bundle in action order.
func AuthorizedPositions(ctx context.Context, msg types.PrivateMsg) ([][]uint64, error) {
	a, err := authorizedFor(ctx, msg)
	if err != nil {
		return nil, err
	}
	return a.positions, nil
}

// release claims, whole and once, the value of denom msg's bundles released.
func release(ctx context.Context, msg types.PrivateMsg, denom string) (sdk.Coin, error) {
	a, err := authorizedFor(ctx, msg)
	if err != nil {
		return sdk.Coin{}, err
	}
	v, ok := a.remaining[denom]
	if !ok {
		return sdk.Coin{}, types.ErrReleaseMap.Wrapf("the msg releases no %s", denom)
	}
	if v == 0 {
		return sdk.Coin{}, types.ErrAlreadyReleased
	}
	a.remaining[denom] = 0
	return sdk.NewCoin(denom, math.NewIntFromUint64(v)), nil
}

// CarryAuthorization copies the authorization recorded on from into to. The
// ante executes a private msg on a context with an infinite gas meter and
// hands the msg the tx's own meter, with the authorization, back.
func CarryAuthorization(to, from sdk.Context) sdk.Context {
	if a, ok := authorizationOf(from); ok {
		return to.WithValue(authorizedKey{}, a)
	}
	return to
}

// PayFeeFromModule pays the msg's fee from output, out of fromModule's uerth,
// to fee_collector. Only a PrivateActionExecutor running in the ante calls
// it, once, for exactly the msg's fee_from_output; the ante refuses the tx
// unless the whole fee was paid this way.
func (k Keeper) PayFeeFromModule(ctx context.Context, fromModule string, fee math.Int) error {
	a, ok := authorizationOf(ctx)
	if !ok {
		return types.ErrUnauthorized
	}
	if !fee.IsPositive() || !fee.IsUint64() || fee.Uint64() != a.feeFromOutput-a.feePaid {
		return types.ErrUnauthorized.Wrapf("fee from output must be exactly the %d%s still owed", a.feeFromOutput-a.feePaid, types.FeeDenom)
	}
	coins := sdk.NewCoins(sdk.NewCoin(types.FeeDenom, fee))
	if err := k.bankKeeper.SendCoinsFromModuleToModule(ctx, fromModule, authtypes.FeeCollectorName, coins); err != nil {
		return err
	}
	a.feePaid += fee.Uint64()
	sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(sdk.NewEvent(types.EventTypeFee,
		sdk.NewAttribute(types.AttributeKeyAmount, coins.String()),
		sdk.NewAttribute(types.AttributeKeyModule, fromModule),
	))
	return nil
}

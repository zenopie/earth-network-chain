package keeper

import (
	"context"
	"strings"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"

	"github.com/earth-network/earth/x/shielded/types"
)

// The private ante chain, having verified a msg's transfers and executed
// their note side (nullifiers spent, outputs appended, fees paid), records
// that in the tx's context. A private msg's handler refuses unless its
// transfer is recorded there.
//
// This is the defence against zero-signer msgs arriving any other way. A
// contract's CosmosMsg::Any, an ICA host tx, a group proposal: each authorizes
// a msg by checking that every signer is the caller, and a msg with no
// signers passes that loop vacuously. None of them runs the private ante, so
// none of them carries this value.

type authorizedKey struct{}

// authorizedTransfer is one transfer the private ante accepted in this tx.
type authorizedTransfer struct {
	key       string // the transfer's nullifiers, concatenated
	positions []uint64
	denom     string
	value     uint64
	// withheld is the part of value the ante already paid out as the msg's
	// fee (an unshield paying its fee from its output); release pays the
	// rest.
	withheld uint64
	// released is set once value has left the pool, so a handler (or a
	// module it calls) cannot pay it out twice.
	released bool
}

// authorization is everything the private ante accepted in this tx.
type authorization struct {
	nullifiers map[string]bool
	transfers  []*authorizedTransfer
	// action is what the msg's PrivateActionHandler prepared, nil for a msg
	// with no action.
	action any
	// executed is set once the ante ran the action itself
	// (PrivateActionExecutor), and result is what it returned.
	executed bool
	result   any
	// feeFromOutput is what the msg owes from its output; feePaid what
	// PayFeeFromModule (or the pool, for an unshield) has paid of it.
	feeFromOutput uint64
	feePaid       uint64
}

func transferKey(t *types.Transfer) string {
	return strings.Join(func() []string {
		out := make([]string, len(t.Nullifiers))
		for i, nf := range t.Nullifiers {
			out[i] = string(nf)
		}
		return out
	}(), "")
}

// WithAuthorizedTransfer marks t as paid for and executed by the private
// ante. Only the ante calls this (and tests standing in for it).
func WithAuthorizedTransfer(ctx sdk.Context, t *types.Transfer, positions []uint64) sdk.Context {
	return WithAuthorizedTransfers(ctx, []*types.Transfer{t}, [][]uint64{positions}, 0)
}

// WithAuthorizedTransfers marks every one of ts as paid for and executed,
// for a msg owing feeFromOutput from its output.
func WithAuthorizedTransfers(ctx sdk.Context, ts []*types.Transfer, positions [][]uint64, feeFromOutput uint64) sdk.Context {
	a := &authorization{nullifiers: map[string]bool{}, feeFromOutput: feeFromOutput}
	for i, t := range ts {
		at := &authorizedTransfer{key: transferKey(t), denom: t.DenomOut, value: t.ValueOut}
		if i < len(positions) {
			at.positions = positions[i]
		}
		for _, nf := range t.Nullifiers {
			a.nullifiers[string(nf)] = true
		}
		a.transfers = append(a.transfers, at)
	}
	return ctx.WithValue(authorizedKey{}, a)
}

func authorizationOf(ctx context.Context) (*authorization, bool) {
	a, ok := sdk.UnwrapSDKContext(ctx).Value(authorizedKey{}).(*authorization)
	return a, ok && a != nil
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

// authorizedFor returns t's authorization, or ErrUnauthorized.
func authorizedFor(ctx context.Context, t *types.Transfer) (*authorization, *authorizedTransfer, error) {
	a, ok := authorizationOf(ctx)
	if !ok {
		return nil, nil, types.ErrUnauthorized
	}
	key := transferKey(t)
	for _, at := range a.transfers {
		if at.key != key {
			continue
		}
		if at.denom != t.DenomOut || at.value != t.ValueOut {
			return nil, nil, types.ErrUnauthorized.Wrap("transfer differs from the one the ante executed")
		}
		return a, at, nil
	}
	return nil, nil, types.ErrUnauthorized
}

// WithAuthorizedAction records what the msg's action handler prepared on the
// authorization ExecutePrivateMsg just made. Only the ante calls this.
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

// AuthorizedAction returns what the msg's PrivateActionHandler prepared, once
// the private ante has verified t and the action's proofs and executed t in
// this tx; ErrUnauthorized otherwise. A private action's handler calls this
// before anything else.
func AuthorizedAction(ctx context.Context, t *types.Transfer) (any, error) {
	a, _, err := authorizedFor(ctx, t)
	if err != nil {
		return nil, err
	}
	if a.action == nil {
		return nil, types.ErrUnauthorized.Wrap("no private action was checked for this msg")
	}
	return a.action, nil
}

// AuthorizedResult returns what the ante's run of the msg's action returned
// (PrivateActionExecutor), and whether it ran it. A handler whose action the
// ante executes returns this and does nothing else.
func AuthorizedResult(ctx context.Context, t *types.Transfer) (any, bool, error) {
	a, _, err := authorizedFor(ctx, t)
	if err != nil {
		return nil, false, err
	}
	if a.action == nil {
		return nil, false, types.ErrUnauthorized.Wrap("no private action was checked for this msg")
	}
	return a.result, a.executed, nil
}

// AuthorizedPositions returns where the ante appended t's outputs.
func AuthorizedPositions(ctx context.Context, t *types.Transfer) ([]uint64, error) {
	_, at, err := authorizedFor(ctx, t)
	if err != nil {
		return nil, err
	}
	return at.positions, nil
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

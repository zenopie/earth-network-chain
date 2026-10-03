package types

import (
	"context"

	"cosmossdk.io/core/address"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/zk/orchard"
)

// TxFields are the tx-level fields every private msg's sighash binds (memo,
// timeout height, gas limit): zk/orchard.TxFields.
type TxFields = orchard.TxFields

type txFieldsKey struct{}

// WithTxFields records the private tx's bound fields in ctx. The private ante
// does this before anything computes a sighash; everything downstream (the
// pool's checks, an action handler's membership signal, the msg's handler)
// reads them back with SighashOf.
func WithTxFields(ctx sdk.Context, f TxFields) sdk.Context {
	return ctx.WithValue(txFieldsKey{}, f)
}

// TxFieldsOf returns the tx fields the private ante recorded in ctx.
func TxFieldsOf(ctx context.Context) (TxFields, bool) {
	f, ok := sdk.UnwrapSDKContext(ctx).Value(txFieldsKey{}).(TxFields)
	return f, ok
}

// SighashOf is msg's sighash in ctx: the chain id and the tx fields the
// private ante recorded (WithTxFields). Outside a private tx there are none,
// and it refuses (ErrUnauthorized) rather than bind made-up values.
func SighashOf(ctx context.Context, msg PrivateMsg, ac address.Codec) (fr.Element, error) {
	f, ok := TxFieldsOf(ctx)
	if !ok {
		return fr.Element{}, ErrUnauthorized.Wrap("no private tx fields in context")
	}
	return Sighash(msg, sdk.UnwrapSDKContext(ctx).ChainID(), f, ac)
}

// moduleReleaseKey marks the context in which x/shielded's ReleaseToModule
// pays the pool's coins to a module for one of its private msgs. A module
// that accepts coins from the pool only that way (x/shieldedstaking's send
// restriction) checks IsModuleRelease.
type moduleReleaseKey struct{}

// WithModuleRelease marks ctx as a ReleaseToModule payment to module.
func WithModuleRelease(ctx context.Context, module string) context.Context {
	return sdk.UnwrapSDKContext(ctx).WithValue(moduleReleaseKey{}, module)
}

// IsModuleRelease reports whether ctx is a ReleaseToModule payment to module.
func IsModuleRelease(ctx context.Context, module string) bool {
	v, _ := ctx.Value(moduleReleaseKey{}).(string)
	return v != "" && v == module
}

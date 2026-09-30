package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/x/shielded/types"
)

// The private ante chain, having verified a transfer's proof and executed its
// note side (nullifiers spent, outputs appended, fee paid), records that in
// the tx's context. A private msg's handler refuses unless its transfer is
// recorded there.
//
// This is the defence against zero-signer msgs arriving any other way. A
// contract's CosmosMsg::Any, an ICA host tx, a group proposal: each authorizes
// a msg by checking that every signer is the caller, and a msg with no
// signers passes that loop vacuously. None of them runs the private ante, so
// none of them carries this value.

type authorizedKey struct{}

// authorizedTransfer is one transfer the private ante accepted in this tx.
type authorizedTransfer struct {
	nullifiers map[string]bool
	positions  []uint64
	denom      string
	value      uint64
	// released is set once value has left the pool, so a handler (or a
	// module it calls) cannot pay it out twice.
	released bool
}

// WithAuthorizedTransfer marks t as paid for and executed by the private
// ante. Only the ante calls this.
func WithAuthorizedTransfer(ctx sdk.Context, t *types.Transfer, positions []uint64) sdk.Context {
	a := &authorizedTransfer{
		nullifiers: make(map[string]bool, len(t.Nullifiers)),
		positions:  positions,
		denom:      t.DenomOut,
		value:      t.ValueOut,
	}
	for _, nf := range t.Nullifiers {
		a.nullifiers[string(nf)] = true
	}
	return ctx.WithValue(authorizedKey{}, a)
}

func authorization(ctx context.Context) (*authorizedTransfer, bool) {
	a, ok := sdk.UnwrapSDKContext(ctx).Value(authorizedKey{}).(*authorizedTransfer)
	return a, ok && a != nil
}

// AuthorizedNullifiers reports whether the private ante verified and spent
// every one of nfs in this tx. False for an empty list.
func AuthorizedNullifiers(ctx context.Context, nfs ...[]byte) bool {
	a, ok := authorization(ctx)
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
func authorizedFor(ctx context.Context, t *types.Transfer) (*authorizedTransfer, error) {
	if !AuthorizedNullifiers(ctx, t.Nullifiers...) {
		return nil, types.ErrUnauthorized
	}
	a, _ := authorization(ctx)
	if len(a.nullifiers) != len(t.Nullifiers) || a.denom != t.DenomOut || a.value != t.ValueOut {
		return nil, types.ErrUnauthorized.Wrap("transfer differs from the one the ante executed")
	}
	return a, nil
}

// AuthorizedPositions returns where the ante appended t's outputs.
func AuthorizedPositions(ctx context.Context, t *types.Transfer) ([]uint64, error) {
	a, err := authorizedFor(ctx, t)
	if err != nil {
		return nil, err
	}
	return a.positions, nil
}

// CarryAuthorization copies the authorization recorded on from into to. The
// ante executes a private msg on a context with an infinite gas meter and
// hands the msg the tx's own meter, with the authorization, back.
func CarryAuthorization(to, from sdk.Context) sdk.Context {
	if a, ok := authorization(from); ok {
		return to.WithValue(authorizedKey{}, a)
	}
	return to
}

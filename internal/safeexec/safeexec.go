// Package safeexec contains a block-hook item: work done for one entry of a
// Begin/EndBlock loop runs on its own cache branch, and a panic inside it
// (math.Int overflow, a nil Int, an index out of range on corrupt state)
// becomes an error for that entry instead of a panic out of the hook.
//
// A panic out of BeginBlock/EndBlock is not recovered by baseapp's
// FinalizeBlock: every validator panics at the same height, and because the
// state that caused it is still there on restart, the halt is permanent. A
// cache branch alone does not contain it (discarding writes does not unwind a
// panic). The callers here already had a "drop this entry and continue" path
// for errors; recovering turns the panic into that path.
//
// Out-of-gas is not recovered: it is the gas meter's control flow, not a
// fault, and a tx context relies on it propagating (block hooks run on an
// infinite meter, so it never fires there).
package safeexec

import (
	"fmt"

	storetypes "cosmossdk.io/store/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
)

// Recover runs fn, converting a panic (other than out-of-gas) into an error.
func Recover(fn func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			if _, oog := r.(storetypes.ErrorOutOfGas); oog {
				panic(r)
			}
			err = fmt.Errorf("recovered panic: %v", r)
		}
	}()
	return fn()
}

// Cached runs fn on a cache branch of ctx and writes the branch back only if
// fn returns nil without panicking. Events emitted inside a discarded branch
// are dropped with it.
func Cached(ctx sdk.Context, fn func(cache sdk.Context) error) error {
	cache, write := ctx.CacheContext()
	if err := Recover(func() error { return fn(cache) }); err != nil {
		return err
	}
	write()
	return nil
}

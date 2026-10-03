package safeexec_test

import (
	"errors"
	"math/big"
	"testing"

	"cosmossdk.io/math"
	storetypes "cosmossdk.io/store/types"
	"github.com/cosmos/cosmos-sdk/testutil"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"github.com/earth-network/earth/internal/safeexec"
)

func TestCached(t *testing.T) {
	key := storetypes.NewKVStoreKey("t")
	ctx := testutil.DefaultContextWithDB(t, key, storetypes.NewTransientStoreKey("tt")).Ctx
	set := func(c sdk.Context, v string) { c.KVStore(key).Set([]byte("k"), []byte(v)) }
	get := func() string { return string(ctx.KVStore(key).Get([]byte("k"))) }

	require.NoError(t, safeexec.Cached(ctx, func(c sdk.Context) error { set(c, "a"); return nil }))
	require.Equal(t, "a", get())

	err := safeexec.Cached(ctx, func(c sdk.Context) error { set(c, "b"); return errors.New("no") })
	require.Error(t, err)
	require.Equal(t, "a", get(), "an error discards the branch")

	// A math.Int overflow panics; it becomes an error and the branch is dropped.
	err = safeexec.Cached(ctx, func(c sdk.Context) error {
		set(c, "c")
		x := math.NewIntFromBigInt(new(big.Int).Lsh(big.NewInt(1), 200))
		_ = x.Mul(x)
		return nil
	})
	require.ErrorContains(t, err, "recovered panic")
	require.Equal(t, "a", get())

	// Out of gas is not swallowed.
	require.Panics(t, func() {
		_ = safeexec.Recover(func() error { panic(storetypes.ErrorOutOfGas{Descriptor: "x"}) })
	})
}

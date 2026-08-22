package keeper_test

import (
	"context"
	"testing"

	storetypes "cosmossdk.io/store/types"
	addresscodec "github.com/cosmos/cosmos-sdk/codec/address"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/cosmos/cosmos-sdk/testutil"
	sdk "github.com/cosmos/cosmos-sdk/types"
	moduletestutil "github.com/cosmos/cosmos-sdk/types/module/testutil"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/stretchr/testify/require"

	"github.com/earth-network/earth/x/earth/keeper"
	module "github.com/earth-network/earth/x/earth/module"
	"github.com/earth-network/earth/x/earth/types"
)

// feeBank models the fee collector as an actual balance so the split can be
// observed from both ends: what left to be burned, and what stayed behind for
// x/distribution to sweep on the next block.
type feeBank struct {
	collector sdk.Coins // the fee collector's balance
	held      sdk.Coins // moved into the earth module, pending burn
	burned    sdk.Coins
}

func (b *feeBank) SpendableCoins(context.Context, sdk.AccAddress) sdk.Coins { return b.collector }

func (b *feeBank) SendCoinsFromModuleToModule(_ context.Context, from, _ string, amt sdk.Coins) error {
	if from == authtypes.FeeCollectorName {
		b.collector = b.collector.Sub(amt...)
		b.held = b.held.Add(amt...)
	}
	return nil
}
func (b *feeBank) SendCoinsFromModuleToAccount(context.Context, string, sdk.AccAddress, sdk.Coins) error {
	return nil
}
func (b *feeBank) MintCoins(context.Context, string, sdk.Coins) error { return nil }
func (b *feeBank) BurnCoins(_ context.Context, _ string, amt sdk.Coins) error {
	b.held = b.held.Sub(amt...)
	b.burned = b.burned.Add(amt...)
	return nil
}

func feeFixture(t *testing.T, collected sdk.Coins) (keeper.Keeper, sdk.Context, *feeBank) {
	t.Helper()
	encCfg := moduletestutil.MakeTestEncodingConfig(module.AppModule{})
	storeKey := storetypes.NewKVStoreKey(types.StoreKey)
	ctx := testutil.DefaultContextWithDB(t, storeKey, storetypes.NewTransientStoreKey("transient_test")).Ctx

	bank := &feeBank{collector: collected}
	k := keeper.NewKeeper(
		runtime.NewKVStoreService(storeKey),
		encCfg.Codec,
		addresscodec.NewBech32Codec(sdk.GetConfig().GetBech32AccountAddrPrefix()),
		authtypes.NewModuleAddress(types.GovModuleName),
		bank,
	)
	require.NoError(t, k.Params.Set(ctx, types.DefaultParams()))
	return k, ctx, bank
}

// TestFeesSplitInHalf is the change itself: gas stops being burned outright and
// starts paying the validators who executed the block.
func TestFeesSplitInHalf(t *testing.T) {
	k, ctx, bank := feeFixture(t, sdk.NewCoins(sdk.NewInt64Coin("uerth", 1_000)))
	require.NoError(t, k.SplitCollectedFees(ctx))

	require.Equal(t, sdk.NewCoins(sdk.NewInt64Coin("uerth", 500)), bank.burned)
	require.Equal(t, sdk.NewCoins(sdk.NewInt64Coin("uerth", 500)), bank.collector,
		"the validators' half must stay in the fee collector for distribution to sweep")
	require.True(t, bank.held.IsZero(), "nothing may be left parked in the module account")
}

// TestOddUnitIsBurned pins the rounding. Where a fee cannot be halved evenly the
// extra unit is destroyed rather than paid out, matching splitFee on the swap
// fee so that every fee on the chain rounds the same way.
func TestOddUnitIsBurned(t *testing.T) {
	k, ctx, bank := feeFixture(t, sdk.NewCoins(sdk.NewInt64Coin("uerth", 1_001)))
	require.NoError(t, k.SplitCollectedFees(ctx))

	require.Equal(t, sdk.NewCoins(sdk.NewInt64Coin("uerth", 501)), bank.burned, "the burn takes the odd unit")
	require.Equal(t, sdk.NewCoins(sdk.NewInt64Coin("uerth", 500)), bank.collector)
}

// TestSplitIsExact: burned + paid must equal collected, at every parity. A split
// that loses or invents a unit is a supply bug, and it would be invisible at any
// realistic fee size.
func TestSplitIsExact(t *testing.T) {
	for _, collected := range []int64{1, 2, 3, 4, 5, 999, 1_000, 7_654_321} {
		k, ctx, bank := feeFixture(t, sdk.NewCoins(sdk.NewInt64Coin("uerth", collected)))
		require.NoError(t, k.SplitCollectedFees(ctx))

		burned := bank.burned.AmountOf("uerth").Int64()
		paid := bank.collector.AmountOf("uerth").Int64()
		require.Equal(t, collected, burned+paid, "collected %d split into %d burned + %d paid", collected, burned, paid)
		require.GreaterOrEqual(t, burned, paid, "the burn never gets the smaller half (collected %d)", collected)
		require.True(t, bank.held.IsZero())
	}
}

// TestSingleUnitFeeIsBurned is the smallest case: one unit cannot be shared, and
// the rounding rule sends it to the burn.
func TestSingleUnitFeeIsBurned(t *testing.T) {
	k, ctx, bank := feeFixture(t, sdk.NewCoins(sdk.NewInt64Coin("uerth", 1)))
	require.NoError(t, k.SplitCollectedFees(ctx))

	require.Equal(t, sdk.NewCoins(sdk.NewInt64Coin("uerth", 1)), bank.burned)
	require.True(t, bank.collector.IsZero())
}

// TestSplitHandlesEveryDenom: fees may be paid in anything a validator accepts,
// so each denom has to be halved on its own rather than the set being treated as
// one quantity.
func TestSplitHandlesEveryDenom(t *testing.T) {
	k, ctx, bank := feeFixture(t, sdk.NewCoins(
		sdk.NewInt64Coin("uerth", 100),
		sdk.NewInt64Coin("uusdc", 51),
	))
	require.NoError(t, k.SplitCollectedFees(ctx))

	require.Equal(t, int64(50), bank.burned.AmountOf("uerth").Int64())
	require.Equal(t, int64(26), bank.burned.AmountOf("uusdc").Int64(), "odd denom rounds to the burn")
	require.Equal(t, int64(50), bank.collector.AmountOf("uerth").Int64())
	require.Equal(t, int64(25), bank.collector.AmountOf("uusdc").Int64())
}

// TestEmptyFeeCollectorIsANoOp: an empty block must not move or emit anything.
func TestEmptyFeeCollectorIsANoOp(t *testing.T) {
	k, ctx, bank := feeFixture(t, sdk.NewCoins())
	require.NoError(t, k.SplitCollectedFees(ctx))
	require.True(t, bank.burned.IsZero())
	require.True(t, bank.held.IsZero())
}

package keeper_test

import (
	"context"
	"testing"

	"cosmossdk.io/core/address"
	"cosmossdk.io/math"
	storetypes "cosmossdk.io/store/types"
	addresscodec "github.com/cosmos/cosmos-sdk/codec/address"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/cosmos/cosmos-sdk/testutil"
	sdk "github.com/cosmos/cosmos-sdk/types"
	moduletestutil "github.com/cosmos/cosmos-sdk/types/module/testutil"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	"github.com/earth-network/earth/x/earth/keeper"
	module "github.com/earth-network/earth/x/earth/module"
	"github.com/earth-network/earth/x/earth/types"
)

type fixture struct {
	ctx          context.Context
	keeper       keeper.Keeper
	addressCodec address.Codec
}

// Minimal stubs: these tests cover params and genesis, not tokenomics, which is
// exercised end to end against a running chain instead.
type stubBank struct{}

func (stubBank) SpendableCoins(context.Context, sdk.AccAddress) sdk.Coins { return nil }
func (stubBank) SendCoinsFromModuleToModule(context.Context, string, string, sdk.Coins) error {
	return nil
}
func (stubBank) SendCoinsFromModuleToAccount(context.Context, string, sdk.AccAddress, sdk.Coins) error {
	return nil
}
func (stubBank) MintCoins(context.Context, string, sdk.Coins) error { return nil }
func (stubBank) BurnCoins(context.Context, string, sdk.Coins) error { return nil }

type stubStaking struct{}

func (stubStaking) BondDenom(context.Context) (string, error) { return "uerth", nil }
func (stubStaking) TotalBondedTokens(context.Context) (math.Int, error) {
	return math.ZeroInt(), nil
}
func (stubStaking) GetBondedValidatorsByPower(context.Context) ([]stakingtypes.Validator, error) {
	return nil, nil
}
func (stubStaking) GetValidator(context.Context, sdk.ValAddress) (stakingtypes.Validator, error) {
	return stakingtypes.Validator{}, nil
}
func (stubStaking) DeleteValidatorByPowerIndex(context.Context, stakingtypes.Validator) error {
	return nil
}
func (stubStaking) SetValidator(context.Context, stakingtypes.Validator) error { return nil }
func (stubStaking) SetValidatorByPowerIndex(context.Context, stakingtypes.Validator) error {
	return nil
}
func (stubStaking) Delegate(
	context.Context, sdk.AccAddress, math.Int, stakingtypes.BondStatus, stakingtypes.Validator, bool,
) (math.LegacyDec, error) {
	return math.LegacyZeroDec(), nil
}

func initFixture(t *testing.T) *fixture {
	t.Helper()

	encCfg := moduletestutil.MakeTestEncodingConfig(module.AppModule{})
	addressCodec := addresscodec.NewBech32Codec(sdk.GetConfig().GetBech32AccountAddrPrefix())
	storeKey := storetypes.NewKVStoreKey(types.StoreKey)

	storeService := runtime.NewKVStoreService(storeKey)
	ctx := testutil.DefaultContextWithDB(t, storeKey, storetypes.NewTransientStoreKey("transient_test")).Ctx

	authority := authtypes.NewModuleAddress(types.GovModuleName)

	k := keeper.NewKeeper(
		storeService,
		encCfg.Codec,
		addressCodec,
		authority,
		stubBank{},
		stubStaking{},
	)

	// Initialize params
	if err := k.Params.Set(ctx, types.DefaultParams()); err != nil {
		t.Fatalf("failed to set params: %v", err)
	}

	return &fixture{
		ctx:          ctx,
		keeper:       k,
		addressCodec: addressCodec,
	}
}

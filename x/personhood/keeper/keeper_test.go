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

	allocationkeeper "github.com/earth-network/earth/x/allocation/keeper"
	allocationtypes "github.com/earth-network/earth/x/allocation/types"
	"github.com/earth-network/earth/x/personhood/keeper"
	module "github.com/earth-network/earth/x/personhood/module"
	"github.com/earth-network/earth/x/personhood/types"
)

type fixture struct {
	ctx context.Context
	// keeper is the module under test; allocation is the real x/allocation keeper
	// it delegates the human emission stream to. Stubbing that out would hide the
	// thing most worth testing here — that a lapsed registration really does give
	// its vote weight back.
	keeper       keeper.Keeper
	allocation   allocationkeeper.Keeper
	addressCodec address.Codec
}

// stubDexKeeper is a minimal DexKeeper for unit tests.
type stubDexKeeper struct{}

func (stubDexKeeper) HubDenom(context.Context) (string, error) { return "uerth", nil }
func (stubDexKeeper) HasPoolForToken(context.Context, string) (bool, error) {
	return false, nil
}
func (stubDexKeeper) SwapExactIn(context.Context, sdk.AccAddress, sdk.Coin, string, math.Int) (sdk.Coin, error) {
	return sdk.Coin{}, nil
}

// stubStaking feeds the allocation keeper's capital stream, which these tests
// never touch — the human stream's weight comes from the personhood keeper
// registered as its source below.
type stubStaking struct{}

func (stubStaking) BondDenom(context.Context) (string, error) { return "uerth", nil }
func (stubStaking) GetDelegatorBonded(context.Context, sdk.AccAddress) (math.Int, error) {
	return math.ZeroInt(), nil
}
func (stubStaking) GetDelegation(context.Context, sdk.AccAddress, sdk.ValAddress) (stakingtypes.Delegation, error) {
	return stakingtypes.Delegation{}, nil
}
func (stubStaking) GetValidator(context.Context, sdk.ValAddress) (stakingtypes.Validator, error) {
	return stakingtypes.Validator{}, nil
}

func initFixture(t *testing.T) *fixture {
	t.Helper()

	encCfg := moduletestutil.MakeTestEncodingConfig(module.AppModule{})
	addressCodec := addresscodec.NewBech32Codec(sdk.GetConfig().GetBech32AccountAddrPrefix())

	personhoodKey := storetypes.NewKVStoreKey(types.StoreKey)
	allocationKey := storetypes.NewKVStoreKey(allocationtypes.StoreKey)
	ctx := testutil.DefaultContextWithKeys(
		map[string]*storetypes.KVStoreKey{
			types.StoreKey:           personhoodKey,
			allocationtypes.StoreKey: allocationKey,
		},
		map[string]*storetypes.TransientStoreKey{
			"transient_test": storetypes.NewTransientStoreKey("transient_test"),
		},
		nil,
	)

	authority := authtypes.NewModuleAddress(types.GovModuleName)

	ak := allocationkeeper.NewKeeper(
		runtime.NewKVStoreService(allocationKey),
		encCfg.Codec,
		addressCodec,
		authority,
		nil,
		stubStaking{},
	)

	k := keeper.NewKeeper(
		runtime.NewKVStoreService(personhoodKey),
		encCfg.Codec,
		addressCodec,
		authority,
		nil,
		stubDexKeeper{},
		nil, // pkiKeeper
		ak,
	)

	// The same wiring ProvideModule does: the human stream asks this keeper who
	// may vote and with how much weight.
	ak.RegisterWeightSource(types.AllocationStream, k)

	// Initialize params
	if err := k.Params.Set(ctx, types.DefaultParams()); err != nil {
		t.Fatalf("failed to set params: %v", err)
	}

	return &fixture{
		ctx:          ctx,
		keeper:       k,
		allocation:   ak,
		addressCodec: addressCodec,
	}
}

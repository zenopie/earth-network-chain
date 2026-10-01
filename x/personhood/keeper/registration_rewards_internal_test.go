package keeper

import (
	"context"
	"testing"

	"cosmossdk.io/math"
	storetypes "cosmossdk.io/store/types"
	addresscodec "github.com/cosmos/cosmos-sdk/codec/address"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/cosmos/cosmos-sdk/testutil"
	sdk "github.com/cosmos/cosmos-sdk/types"
	moduletestutil "github.com/cosmos/cosmos-sdk/types/module/testutil"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/stretchr/testify/require"

	allocationtypes "github.com/earth-network/earth/x/allocation/types"
	"github.com/earth-network/earth/x/personhood/types"
)

// recordingAllocation captures the ppm a draw was requested at, and what moved
// to this module. The unmatched half stays in the pool by never being drawn,
// so the ppm is the mechanism under test.
type recordingAllocation struct {
	stubAllocation
	drawnAtPpm int64
	payout     math.Int
	toModule   math.Int
	toAccount  map[string]math.Int
}

func (r *recordingAllocation) PayOut(_ context.Context, to sdk.AccAddress, amt math.Int) error {
	if r.toAccount == nil {
		r.toAccount = map[string]math.Int{}
	}
	r.toAccount[string(to)] = amt
	return nil
}

func (r *recordingAllocation) DrawFromOption(_ context.Context, _ allocationtypes.StreamId, _ uint64, ppm int64) (math.Int, error) {
	r.drawnAtPpm = ppm
	return r.payout, nil
}

func (r *recordingAllocation) PayOutToModule(_ context.Context, module string, amt math.Int) error {
	if module != types.ModuleName {
		panic(module)
	}
	r.toModule = amt
	return nil
}

func rewardKeeper(t *testing.T, alloc *recordingAllocation, minted *[]sdk.Coin) (Keeper, context.Context) {
	t.Helper()
	encCfg := moduletestutil.MakeTestEncodingConfig()
	ac := addresscodec.NewBech32Codec(sdk.GetConfig().GetBech32AccountAddrPrefix())
	storeKey := storetypes.NewKVStoreKey(types.StoreKey)
	ctx := testutil.DefaultContextWithDB(t, storeKey, storetypes.NewTransientStoreKey("transient_test")).Ctx
	k := NewKeeper(runtime.NewKVStoreService(storeKey), encCfg.Codec, ac, authtypes.NewModuleAddress(types.GovModuleName),
		nil, stubDex{}, nil, alloc, &burnLog{}, stubShielded{minted: minted})
	return k, ctx
}

// A referred registration draws the full rate: the registrant's half is
// minted as a note, the referrer's paid in transparent ERTH to its address.
func TestRegistrationRewardSplitsWithReferrer(t *testing.T) {
	alloc := &recordingAllocation{payout: math.NewInt(1001)}
	var minted []sdk.Coin
	k, ctx := rewardKeeper(t, alloc, &minted)
	referrer := sdk.AccAddress{2}
	got, err := k.payRegistrationReward(ctx, rewardNote{pc: []byte{1}}, referrer)
	require.NoError(t, err)
	require.Equal(t, int64(types.RegistrationRewardPpm), alloc.drawnAtPpm)
	require.Equal(t, math.NewInt(501), got)
	require.Equal(t, math.NewInt(501), alloc.toModule)
	require.Equal(t, math.NewInt(500), alloc.toAccount[string(referrer)])
	require.Equal(t, []sdk.Coin{sdk.NewInt64Coin("uerth", 501)}, minted)
}

// An unreferred registration draws half the rate: the registrant is paid what
// a referred one is, and the referrer's half stays in the pool.
func TestRegistrationRewardKeepsReferrerShareInPool(t *testing.T) {
	alloc := &recordingAllocation{payout: math.NewInt(500)}
	var minted []sdk.Coin
	k, ctx := rewardKeeper(t, alloc, &minted)
	got, err := k.payRegistrationReward(ctx, rewardNote{pc: []byte{1}}, nil)
	require.NoError(t, err)
	require.Equal(t, int64(types.RegistrationRewardPpm/2), alloc.drawnAtPpm)
	require.Equal(t, math.NewInt(500), got)
	require.Equal(t, []sdk.Coin{sdk.NewInt64Coin("uerth", 500)}, minted)
}

// An empty pool pays nothing and mints no note.
func TestRegistrationRewardEmptyPool(t *testing.T) {
	alloc := &recordingAllocation{payout: math.ZeroInt()}
	var minted []sdk.Coin
	k, ctx := rewardKeeper(t, alloc, &minted)
	got, err := k.payRegistrationReward(ctx, rewardNote{pc: []byte{1}}, sdk.AccAddress{2})
	require.NoError(t, err)
	require.True(t, got.IsZero())
	require.Empty(t, minted)
}

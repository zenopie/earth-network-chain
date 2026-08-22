package keeper

import (
	"testing"
	"time"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/stretchr/testify/require"

	"github.com/earth-network/earth/x/allocation/types"
)

// The emergency fund is seeded at genesis, so stakers can direct weight at it
// from height 1 without a governance proposal first.
func TestGenesisSeedsTheEmergencyFund(t *testing.T) {
	e := newTestEnv(t)
	require.NoError(t, e.k.InitGenesis(e.ctx, *types.DefaultGenesis()))

	key := optionKey(types.STREAM_ID_GROUNDWORKS, types.CommunityPoolOptionID)
	opt, err := e.k.Options.Get(e.ctx, key)
	require.NoError(t, err)
	require.Equal(t, types.ALLOCATION_KIND_INTEGRATED, opt.Kind)
	require.Equal(t, types.HandlerCommunityPool, opt.Handler)
	require.Equal(t, types.STREAM_ID_GROUNDWORKS, opt.Stream)

	has, err := e.k.IntegratedOptions.Has(e.ctx, key)
	require.NoError(t, err)
	require.True(t, has, "it has to be resolved every block, not claimed")

	// The human stream gets no such option: its id 2 is free.
	_, err = e.k.Options.Get(e.ctx, optionKey(types.STREAM_ID_CARETAKER, types.CommunityPoolOptionID))
	require.Error(t, err)
}

// The whole accrual reaches the pool through FundCommunityPool — which is the
// only call that credits FeePool as well as moving the coins — and the option is
// left owing nothing.
func TestEmergencyFundCreditsTheCommunityPool(t *testing.T) {
	e := newTestEnv(t)
	require.NoError(t, e.k.InitGenesis(e.ctx, *types.DefaultGenesis()))

	key := optionKey(types.STREAM_ID_GROUNDWORKS, types.CommunityPoolOptionID)
	opt, err := e.k.Options.Get(e.ctx, key)
	require.NoError(t, err)
	opt.Accumulated = math.NewInt(400)
	require.NoError(t, e.k.Options.Set(e.ctx, key, opt))

	e.ctx = e.ctx.WithBlockTime(time.Unix(1000, 0))
	require.NoError(t, e.k.BeginBlocker(e.ctx))

	require.Equal(t, "400uerth", e.pool.funded.String())
	require.Equal(t, sdk.AccAddress(authtypes.NewModuleAddress(types.ModuleName)), e.pool.sender,
		"the module account pays as an ordinary sender; the distribution account is blocked to module-to-account payouts")

	opt, err = e.k.Options.Get(e.ctx, key)
	require.NoError(t, err)
	require.True(t, opt.Accumulated.IsZero(), "the accrual resolves in full, with nothing carried")
}

// Nothing accrued means nothing minted: a block where the fund holds no weight
// must not credit the pool a zero coin or mint against it.
func TestEmergencyFundSkipsAnEmptyAccrual(t *testing.T) {
	e := newTestEnv(t)
	require.NoError(t, e.k.InitGenesis(e.ctx, *types.DefaultGenesis()))

	e.ctx = e.ctx.WithBlockTime(time.Unix(1000, 0))
	require.NoError(t, e.k.BeginBlocker(e.ctx))
	require.True(t, e.pool.funded.IsZero())
}

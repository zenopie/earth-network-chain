package keeper_test

import (
	"testing"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"github.com/earth-network/earth/x/dex/keeper"
	"github.com/earth-network/earth/x/dex/types"
)

// Audit 3 L3: a deposit's pulled legs are floor(shares*R/T). A share worth
// less than one unit of a leg used to be minted against zero of that leg,
// diluting the pool's providers. Such a deposit is refused.
func TestAudit3DepositPullingZeroLegRefused(t *testing.T) {
	k, ctx, bank := initRewardFixture(t)
	ms := keeper.NewMsgServerImpl(k)
	seedFundedPool(t, k, ctx, bank, 1, 1_000_000_000_000, 1, 0)
	bank.setSupply(types.LPShareDenom(1), math.NewInt(1_000_000))
	before, err := k.Pool.Get(ctx, 1)
	require.NoError(t, err)
	_, err = ms.AddLiquidity(ctx, &types.MsgAddLiquidity{
		Creator: bech32(t, sdk.AccAddress("provider____________")),
		PoolId:  1,
		AmountA: sdk.NewInt64Coin("uerth", 1_000_000),
		AmountB: sdk.NewInt64Coin("utok", 1),
	})
	require.ErrorIs(t, err, types.ErrZeroShares)
	after, err := k.Pool.Get(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, before.ReserveErth, after.ReserveErth)
	require.Equal(t, before.ReserveToken, after.ReserveToken)
}

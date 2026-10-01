package keeper_test

import (
	"testing"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/earth-network/earth/x/dex/keeper"
	"github.com/earth-network/earth/x/dex/types"
)

// TestSimulateSwapExactInIsTheSwap: for every route shape the query returns
// exactly what the swap then pays, with LP rewards pending (which the swap
// settles into the reserve before pricing, so maths over the stored reserves
// would be wrong), and writes nothing: reserves, pending rewards and the
// event stream are as they were.
func TestSimulateSwapExactInIsTheSwap(t *testing.T) {
	for _, tc := range []struct {
		name     string
		in       sdk.Coin
		denomOut string
		hops     int
		hubToken bool // single ERTH -> token hop: the fee is a fixed share of the input
	}{
		{"token to erth", sdk.NewInt64Coin("utok", 50_000), "uerth", 1, false},
		{"erth to token", sdk.NewInt64Coin("uerth", 50_000), "utok2", 1, true},
		{"token to token", sdk.NewInt64Coin("utok", 50_000), "utok2", 2, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			k, ctx, bank := initRewardFixture(t)
			seedFundedPool(t, k, ctx, bank, 1, 1_000_000, 2_000_000, 1_000)
			seedFundedPool(t, k, ctx, bank, 2, 3_000_000, 1_000_000, 3_000)
			distributeLP(t, k, ctx, bank, math.NewInt(200_000))

			pool1, err := k.Pool.Get(ctx, 1)
			require.NoError(t, err)
			pool2, err := k.Pool.Get(ctx, 2)
			require.NoError(t, err)
			pending, err := k.PendingLpRewards.Get(ctx)
			require.NoError(t, err)
			require.True(t, pending.IsPositive())
			burned := bank.burned
			events := len(ctx.EventManager().Events())

			q := keeper.NewQueryServerImpl(k)
			res, err := q.SimulateSwapExactIn(ctx, &types.QuerySimulateSwapExactInRequest{
				OfferDenom: tc.in.Denom, OfferAmount: tc.in.Amount, AskDenom: tc.denomOut,
			})
			require.NoError(t, err)
			require.Equal(t, tc.denomOut, res.TokenOut.Denom)
			require.Equal(t, "uerth", res.Fee.Denom)
			require.True(t, res.Fee.Amount.IsPositive())
			// The burn takes the odd unit of each hop's fee.
			require.True(t, res.ErthBurned.MulRaw(2).GTE(res.Fee.Amount))
			require.True(t, res.ErthBurned.MulRaw(2).LTE(res.Fee.Amount.AddRaw(int64(tc.hops))))
			if tc.hubToken {
				require.Equal(t, tc.in.Amount.MulRaw(3).QuoRaw(1000), res.Fee.Amount, "0.3% of the ERTH input")
			}

			// Nothing written.
			p1, err := k.Pool.Get(ctx, 1)
			require.NoError(t, err)
			p2, err := k.Pool.Get(ctx, 2)
			require.NoError(t, err)
			require.Equal(t, pool1, p1)
			require.Equal(t, pool2, p2)
			pend, err := k.PendingLpRewards.Get(ctx)
			require.NoError(t, err)
			require.Equal(t, pending, pend)
			require.Len(t, ctx.EventManager().Events(), events)
			// The stub bank is not store-backed, so the cache cannot discard
			// its burn; the real bank's is (app TestDexSimulateSwapQuery).
			burned = bank.burned

			// The swap pays exactly that and burns exactly that.
			out, err := k.SwapExactIn(ctx, sdk.AccAddress("trader______________"), tc.in, tc.denomOut, res.TokenOut.Amount)
			require.NoError(t, err)
			require.Equal(t, res.TokenOut, out)
			require.Equal(t, res.ErthBurned, bank.burned.Sub(burned...).AmountOf("uerth"))

			// And keeper.SimulateSwapExactIn agrees with the query.
			again, err := k.SimulateSwapExactIn(ctx, tc.in, tc.denomOut)
			require.NoError(t, err)
			res2, err := q.SimulateSwapExactIn(ctx, &types.QuerySimulateSwapExactInRequest{
				OfferDenom: tc.in.Denom, OfferAmount: tc.in.Amount, AskDenom: tc.denomOut,
			})
			require.NoError(t, err)
			require.Equal(t, again, res2.TokenOut)
		})
	}
}

// TestSimulateSwapExactInSeesPendingRewards: settling the pool's pending LP
// rewards before pricing moves the output away from what the stored reserves
// alone would give, which is why wallets ask the chain.
func TestSimulateSwapExactInSeesPendingRewards(t *testing.T) {
	quote := func(withRewards bool) math.Int {
		k, ctx, bank := initRewardFixture(t)
		seedFundedPool(t, k, ctx, bank, 1, 1_000_000, 1_000_000, 1_000)
		if withRewards {
			distributeLP(t, k, ctx, bank, math.NewInt(100_000))
		}
		res, err := keeper.NewQueryServerImpl(k).SimulateSwapExactIn(ctx, &types.QuerySimulateSwapExactInRequest{
			OfferDenom: "utok", OfferAmount: math.NewInt(10_000), AskDenom: "uerth",
		})
		require.NoError(t, err)
		return res.TokenOut.Amount
	}
	require.True(t, quote(true).GT(quote(false)), "the settled reward deepens the ERTH reserve")
}

func TestSimulateSwapExactInRejects(t *testing.T) {
	k, ctx, bank := initRewardFixture(t)
	seedFundedPool(t, k, ctx, bank, 1, 1_000_000, 1_000_000, 1_000)
	q := keeper.NewQueryServerImpl(k)

	_, err := q.SimulateSwapExactIn(ctx, nil)
	require.Equal(t, codes.InvalidArgument, status.Code(err))

	for _, req := range []*types.QuerySimulateSwapExactInRequest{
		{OfferDenom: "", OfferAmount: math.NewInt(1), AskDenom: "uerth"},
		{OfferDenom: "utok", OfferAmount: math.NewInt(1), AskDenom: "!"},
		{OfferDenom: "utok", OfferAmount: math.ZeroInt(), AskDenom: "uerth"},
		{OfferDenom: "utok", OfferAmount: math.NewInt(-5), AskDenom: "uerth"},
		{OfferDenom: "utok", AskDenom: "uerth"},
	} {
		_, err := q.SimulateSwapExactIn(ctx, req)
		require.Equal(t, codes.InvalidArgument, status.Code(err), "%+v", req)
	}
	for _, req := range []*types.QuerySimulateSwapExactInRequest{
		{OfferDenom: "utok", OfferAmount: math.NewInt(1_000), AskDenom: "utok"},
		{OfferDenom: "unone", OfferAmount: math.NewInt(1_000), AskDenom: "uerth"},
		{OfferDenom: "utok", OfferAmount: math.NewInt(1), AskDenom: "uerth"}, // rounds to zero
	} {
		_, err := q.SimulateSwapExactIn(ctx, req)
		require.Equal(t, codes.FailedPrecondition, status.Code(err), "%+v", req)
	}
}

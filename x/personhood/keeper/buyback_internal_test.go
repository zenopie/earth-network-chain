package keeper

import (
	"context"
	"errors"
	"testing"
	"time"

	"cosmossdk.io/math"
	storetypes "cosmossdk.io/store/types"
	addresscodec "github.com/cosmos/cosmos-sdk/codec/address"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/cosmos/cosmos-sdk/testutil"
	sdk "github.com/cosmos/cosmos-sdk/types"
	moduletestutil "github.com/cosmos/cosmos-sdk/types/module/testutil"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"

	"github.com/earth-network/earth/x/personhood/types"
)

// oracleDex is a DexKeeper whose price the test drives directly. The buyback's
// decision is a pure function of the numbers the oracle hands back, so driving
// those is enough to exercise every branch without standing up a real pool.
type oracleDex struct {
	spot math.LegacyDec
	// cum advances by twapPrice per second, so the average the buyback computes
	// is twapPrice regardless of where spot has been pushed to.
	cum       math.LegacyDec
	twapPrice math.LegacyDec
	lastAt    int64

	quote math.Int

	swaps   []sdk.Coin // token_in of every swap that reached the dex
	minOuts []math.Int // the min_out demanded on each
	swapErr error
}

func newOracleDex(twap, spot string) *oracleDex {
	return &oracleDex{
		spot:      math.LegacyMustNewDecFromStr(spot),
		cum:       math.LegacyZeroDec(),
		twapPrice: math.LegacyMustNewDecFromStr(twap),
		quote:     math.NewInt(1_000_000),
	}
}

func (d *oracleDex) HubDenom(context.Context) (string, error)              { return "uerth", nil }
func (d *oracleDex) HasPoolForToken(context.Context, string) (bool, error) { return true, nil }

func (d *oracleDex) TwapObservation(ctx context.Context, _ string) (math.LegacyDec, math.LegacyDec, int64, error) {
	now := sdk.UnwrapSDKContext(ctx).BlockTime().Unix()
	if d.lastAt != 0 && now > d.lastAt {
		d.cum = d.cum.Add(d.twapPrice.MulInt64(now - d.lastAt))
	}
	d.lastAt = now
	return d.cum, d.spot, now, nil
}

func (d *oracleDex) QuoteHubToToken(context.Context, string, math.Int) (math.Int, error) {
	return d.quote, nil
}

func (d *oracleDex) SwapExactInForModule(_ context.Context, _ string, tokenIn sdk.Coin, denomOut string, minOut math.Int) (sdk.Coin, error) {
	if d.swapErr != nil {
		return sdk.Coin{}, d.swapErr
	}
	d.swaps = append(d.swaps, tokenIn)
	d.minOuts = append(d.minOuts, minOut)
	return sdk.NewCoin(denomOut, d.quote), nil
}

// countingBank records mints and burns so the tests can assert what the buyback
// actually spent and destroyed.
type countingBank struct{ minted, burned sdk.Coins }

func (b *countingBank) GetSupply(_ context.Context, denom string) sdk.Coin {
	return sdk.NewCoin(denom, math.ZeroInt())
}
func (b *countingBank) SendCoinsFromModuleToAccount(context.Context, string, sdk.AccAddress, sdk.Coins) error {
	return nil
}
func (b *countingBank) SendCoinsFromAccountToModule(context.Context, sdk.AccAddress, string, sdk.Coins) error {
	return nil
}
func (b *countingBank) MintCoins(_ context.Context, _ string, amt sdk.Coins) error {
	b.minted = b.minted.Add(amt...)
	return nil
}
func (b *countingBank) BurnCoins(_ context.Context, _ string, amt sdk.Coins) error {
	b.burned = b.burned.Add(amt...)
	return nil
}

func newBuybackKeeper(t *testing.T, dex types.DexKeeper) (Keeper, *countingBank, sdk.Context) {
	t.Helper()
	encCfg := moduletestutil.MakeTestEncodingConfig()
	storeKey := storetypes.NewKVStoreKey(types.StoreKey)
	base := testutil.DefaultContextWithDB(t, storeKey, storetypes.NewTransientStoreKey("transient_test")).Ctx
	bank := &countingBank{}
	k := NewKeeper(
		runtime.NewKVStoreService(storeKey),
		encCfg.Codec,
		addresscodec.NewBech32Codec(sdk.GetConfig().GetBech32AccountAddrPrefix()),
		authtypes.NewModuleAddress(types.GovModuleName),
		bank, dex, nil, stubAllocation{},
	)
	ctx := base.WithBlockTime(time.Unix(1_700_000_000, 0).UTC())
	if err := k.Params.Set(ctx, types.DefaultParams()); err != nil {
		t.Fatal(err)
	}
	return k, bank, ctx
}

func advance(ctx sdk.Context, d time.Duration) sdk.Context {
	return ctx.WithBlockTime(ctx.BlockTime().Add(d))
}

// TestBuybackWaitsForItsWindow: the first blocks start the clock and fill the
// averaging window rather than trading, because there is no average to price
// against yet.
func TestBuybackWaitsForItsWindow(t *testing.T) {
	dex := newOracleDex("1.0", "1.0")
	k, _, ctx := newBuybackKeeper(t, dex)

	if err := k.buybackAndBurn(ctx); err != nil { // starts the clock
		t.Fatal(err)
	}
	ctx = advance(ctx, time.Minute)
	if err := k.buybackAndBurn(ctx); err != nil { // first observation
		t.Fatal(err)
	}
	ctx = advance(ctx, time.Minute)
	if err := k.buybackAndBurn(ctx); err != nil { // window not full yet
		t.Fatal(err)
	}
	if len(dex.swaps) != 0 {
		t.Fatalf("traded before the window was full: %v", dex.swaps)
	}

	ctx = advance(ctx, 11*time.Minute) // now past the 600s window
	if err := k.buybackAndBurn(ctx); err != nil {
		t.Fatal(err)
	}
	if len(dex.swaps) != 1 {
		t.Fatalf("expected one trade once the window filled, got %d", len(dex.swaps))
	}
}

// fastForward runs the buyback until it has traded once and returns the context
// at that trade, so callers can measure accrual from a known instant. It exists
// so the interesting tests start from a keeper whose window is already primed.
func fastForward(t *testing.T, k Keeper, dex *oracleDex, ctx sdk.Context) sdk.Context {
	t.Helper()
	for i := 0; i < 5 && len(dex.swaps) == 0; i++ {
		if err := k.buybackAndBurn(ctx); err != nil {
			t.Fatal(err)
		}
		if len(dex.swaps) > 0 {
			break // leave ctx sitting exactly at the trade
		}
		ctx = advance(ctx, 11*time.Minute)
	}
	if len(dex.swaps) == 0 {
		t.Fatal("buyback never primed")
	}
	return ctx
}

// TestBuybackRefusesSpotAboveTwap is the anti-sandwich property. With the pool
// pushed above its average, the trade must not happen at all — the attacker who
// moved the price gets no protocol bid to sell into.
func TestBuybackRefusesSpotAboveTwap(t *testing.T) {
	dex := newOracleDex("1.0", "1.0")
	k, bank, ctx := newBuybackKeeper(t, dex)
	ctx = fastForward(t, k, dex, ctx)
	tradesBefore := len(dex.swaps)
	mintedBefore := bank.minted.AmountOf("uerth")

	// Sandwich: spot pushed 10% above the average, well past the 2% band.
	dex.spot = math.LegacyMustNewDecFromStr("1.10")
	ctx = advance(ctx, 11*time.Minute)
	if err := k.buybackAndBurn(ctx); err != nil {
		t.Fatal(err)
	}

	if len(dex.swaps) != tradesBefore {
		t.Fatal("bought into a pool pushed above its average — this is the sandwich")
	}
	if got := bank.minted.AmountOf("uerth"); !got.Equal(mintedBefore) {
		t.Fatalf("minted ERTH for a trade that did not happen: %s -> %s", mintedBefore, got)
	}
}

// TestBuybackAccruesWhatItRefused: refusing a window must defer the emission,
// never drop it. Otherwise holding the price up would be a way to destroy the
// pillar's emission rather than merely delay it.
func TestBuybackAccruesWhatItRefused(t *testing.T) {
	dex := newOracleDex("1.0", "1.0")
	k, _, ctx := newBuybackKeeper(t, dex)
	ctx = fastForward(t, k, dex, ctx)
	tradedAt := ctx.BlockTime()

	// Two windows refused.
	dex.spot = math.LegacyMustNewDecFromStr("1.10")
	for i := 0; i < 2; i++ {
		ctx = advance(ctx, 11*time.Minute)
		if err := k.buybackAndBurn(ctx); err != nil {
			t.Fatal(err)
		}
	}
	// Price comes back to the average; the next window trades.
	dex.spot = math.LegacyOneDec()
	ctx = advance(ctx, 11*time.Minute)
	if err := k.buybackAndBurn(ctx); err != nil {
		t.Fatal(err)
	}

	// The resumed trade must cover every second since the last one — the two
	// refused windows included, to the unit.
	elapsed := int64(ctx.BlockTime().Sub(tradedAt).Seconds())
	want := math.NewInt(types.EmissionPerSecond).MulRaw(elapsed)
	spent := dex.swaps[len(dex.swaps)-1].Amount
	if !spent.Equal(want) {
		t.Fatalf("refused windows were not fully deferred: spent %s over %ds, want %s", spent, elapsed, want)
	}
}

// TestBuybackAllowsSpotBelowTwap: the gate is one-sided. A pool cheaper than its
// average is the case the buyback exists for, and stalling on it would forgo a
// good trade for no gain.
func TestBuybackAllowsSpotBelowTwap(t *testing.T) {
	dex := newOracleDex("1.0", "1.0")
	k, _, ctx := newBuybackKeeper(t, dex)
	ctx = fastForward(t, k, dex, ctx)
	tradesBefore := len(dex.swaps)

	dex.spot = math.LegacyMustNewDecFromStr("0.80")
	ctx = advance(ctx, 11*time.Minute)
	if err := k.buybackAndBurn(ctx); err != nil {
		t.Fatal(err)
	}
	if len(dex.swaps) != tradesBefore+1 {
		t.Fatal("refused to buy a pool trading below its average")
	}
}

// TestBuybackDemandsMinOut is the finding this replaced: the swap must never go
// out with min_out of zero.
func TestBuybackDemandsMinOut(t *testing.T) {
	dex := newOracleDex("1.0", "1.0")
	k, _, ctx := newBuybackKeeper(t, dex)
	fastForward(t, k, dex, ctx)

	minOut := dex.minOuts[0]
	if !minOut.IsPositive() {
		t.Fatalf("buyback swapped with min_out %s — unlimited slippage", minOut)
	}
	// Derived from the quote, less the tolerance.
	want := dex.quote.MulRaw(types.BpsDenominator - types.BuybackQuoteToleranceBps).QuoRaw(types.BpsDenominator)
	if !minOut.Equal(want) {
		t.Fatalf("min_out %s, want %s (quote %s less %d bps)", minOut, want, dex.quote, types.BuybackQuoteToleranceBps)
	}
}

// TestBuybackCapsCatchUp bounds the trade after a long gap. Without the cap a
// halt turns into one unbounded market order the moment the chain resumes.
func TestBuybackCapsCatchUp(t *testing.T) {
	dex := newOracleDex("1.0", "1.0")
	k, _, ctx := newBuybackKeeper(t, dex)
	ctx = fastForward(t, k, dex, ctx)

	ctx = advance(ctx, 30*24*time.Hour) // a month of downtime
	if err := k.buybackAndBurn(ctx); err != nil {
		t.Fatal(err)
	}
	spent := dex.swaps[len(dex.swaps)-1].Amount
	cap := math.NewInt(types.EmissionPerSecond).MulRaw(types.DefaultBuybackMaxAccrualSeconds)
	if spent.GT(cap) {
		t.Fatalf("catch-up trade %s exceeds the one-day cap %s", spent, cap)
	}
}

// TestBuybackKeepsAccrualWhenTheSwapFails: a dex-side failure must leave the
// clock alone, so the emission is retried rather than silently burned off.
func TestBuybackKeepsAccrualWhenTheSwapFails(t *testing.T) {
	dex := newOracleDex("1.0", "1.0")
	k, bank, ctx := newBuybackKeeper(t, dex)
	ctx = fastForward(t, k, dex, ctx)
	before := bank.burned

	dex.swapErr = errors.New("pool too thin")
	ctx = advance(ctx, 11*time.Minute)
	if err := k.buybackAndBurn(ctx); err != nil {
		t.Fatal(err)
	}
	if !bank.burned.Equal(before) {
		t.Fatal("a failed swap must not burn anything")
	}

	dex.swapErr = nil
	ctx = advance(ctx, 11*time.Minute)
	if err := k.buybackAndBurn(ctx); err != nil {
		t.Fatal(err)
	}
	spent := dex.swaps[len(dex.swaps)-1].Amount
	twoWindows := math.NewInt(types.EmissionPerSecond).MulRaw(22 * 60)
	if spent.LT(twoWindows) {
		t.Fatalf("emission from the failed window was lost: spent %s over two windows, want >= %s", spent, twoWindows)
	}
}

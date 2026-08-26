package keeper_test

import (
	"context"
	"fmt"
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
	"github.com/stretchr/testify/require"

	"github.com/earth-network/earth/x/dex/keeper"
	module "github.com/earth-network/earth/x/dex/module"
	"github.com/earth-network/earth/x/dex/types"
)

// mintingBank is a BankKeeper stub for the LP-reward path. Mints and burns are
// tallied separately: a swap burns half its fee, and netting that against the
// mint tally would quietly move the reward numbers the tests are pinning.
//
// Payouts out of the module are recorded per recipient so the unbonding tests
// can assert who was actually paid, and how much.
//
// It also keeps a real running balance for the module account, because the
// solvency invariant (keeper/invariants.go) compares what the module's records
// say it owes against what it actually holds — and a stub that always answers
// zero, or always answers enough, would make that check meaningless. Every path
// that moves coins in or out of the module moves modBal by the same amount.
type mintingBank struct {
	minted, burned sdk.Coins
	sent           sdk.Coins
	sentByAddr     map[string]sdk.Coins
	modBal         sdk.Coins
	// counted is what the keeper reported to x/earth's burn counters, by source.
	// Kept on the bank so the two can be compared directly: whatever BurnCoins
	// destroyed, RecordBurn should have counted — minus the LP shares, which are
	// a claim on the pool rather than supply.
	counted map[string]sdk.Coins
}

func (b *mintingBank) RecordBurn(_ context.Context, source string, coins sdk.Coins) error {
	if b.counted == nil {
		b.counted = map[string]sdk.Coins{}
	}
	b.counted[source] = b.counted[source].Add(coins...)
	return nil
}

func (b *mintingBank) SpendableCoins(context.Context, sdk.AccAddress) sdk.Coins { return nil }
func (b *mintingBank) GetSupply(_ context.Context, denom string) sdk.Coin {
	return sdk.NewCoin(denom, b.minted.AmountOf(denom).Sub(b.burned.AmountOf(denom)))
}
func (b *mintingBank) SendCoinsFromAccountToModule(_ context.Context, _ sdk.AccAddress, _ string, amt sdk.Coins) error {
	b.modBal = b.modBal.Add(amt...)
	return nil
}
func (b *mintingBank) SendCoinsFromModuleToAccount(_ context.Context, _ string, to sdk.AccAddress, amt sdk.Coins) error {
	b.sent = b.sent.Add(amt...)
	if b.sentByAddr == nil {
		b.sentByAddr = map[string]sdk.Coins{}
	}
	b.sentByAddr[to.String()] = b.sentByAddr[to.String()].Add(amt...)
	b.debit(amt)
	return nil
}

// GetBalance and GetAllBalances answer for the module account; nothing in these
// tests asks about anyone else's, and a per-address ledger would be a bank
// reimplementation.
func (b *mintingBank) GetBalance(_ context.Context, _ sdk.AccAddress, denom string) sdk.Coin {
	return sdk.NewCoin(denom, b.modBal.AmountOf(denom))
}
func (b *mintingBank) GetAllBalances(_ context.Context, _ sdk.AccAddress) sdk.Coins {
	return b.modBal
}

// debit subtracts without letting the ledger go negative — sdk.Coins panics on
// a negative amount, and a test asserting insolvency wants a failed invariant,
// not a panic in the stub.
func (b *mintingBank) debit(amt sdk.Coins) {
	for _, c := range amt {
		have := b.modBal.AmountOf(c.Denom)
		take := c.Amount
		if take.GT(have) {
			take = have
		}
		if take.IsPositive() {
			b.modBal = b.modBal.Sub(sdk.NewCoin(c.Denom, take))
		}
	}
}

// fundModule credits the module account without touching the mint tally, for
// seeding a fixture into the state a funded genesis would have left.
func (b *mintingBank) fundModule(amt ...sdk.Coin) { b.modBal = b.modBal.Add(amt...) }

// sentTo returns everything the module has paid out to addr.
func (b *mintingBank) sentTo(addr sdk.AccAddress) sdk.Coins { return b.sentByAddr[addr.String()] }

// setSupply seeds an outstanding coin supply, standing in for shares that
// AddLiquidity would have minted. It does not credit the module account: the
// caller says who holds them, because "supply exists" and "the module holds it"
// are exactly the two facts the share-backing invariant tells apart.
func (b *mintingBank) setSupply(denom string, amount math.Int) {
	b.minted = b.minted.Add(sdk.NewCoin(denom, amount))
}
func (b *mintingBank) SendCoinsFromModuleToModule(context.Context, string, string, sdk.Coins) error {
	return nil
}
func (b *mintingBank) MintCoins(_ context.Context, _ string, amt sdk.Coins) error {
	b.minted = b.minted.Add(amt...)
	b.modBal = b.modBal.Add(amt...)
	return nil
}
func (b *mintingBank) BurnCoins(_ context.Context, _ string, amt sdk.Coins) error {
	b.burned = b.burned.Add(amt...)
	b.debit(amt)
	return nil
}

// distributeLP mirrors what the production handler does. x/allocation resolves
// the LP-rewards option and then pays the resolved ERTH into this module's
// account; calling DistributeLPRewards on its own advances the index without the
// coins ever arriving, which the solvency invariant is right to reject.
//
// The ERTH is funded rather than minted because x/allocation minted it already,
// when the capital stream's index advanced. Nothing in x/dex issues it.
func distributeLP(t *testing.T, k keeper.Keeper, ctx sdk.Context, bank *mintingBank, amount math.Int) math.Int {
	t.Helper()
	resolved, err := k.DistributeLPRewards(ctx, amount)
	require.NoError(t, err)
	if resolved.IsPositive() {
		bank.fundModule(sdk.NewCoin("uerth", resolved))
	}
	return resolved
}

func initRewardFixture(t *testing.T) (keeper.Keeper, sdk.Context, *mintingBank) {
	t.Helper()
	encCfg := moduletestutil.MakeTestEncodingConfig(module.AppModule{})
	storeKey := storetypes.NewKVStoreKey(types.StoreKey)
	ctx := testutil.DefaultContextWithDB(t, storeKey, storetypes.NewTransientStoreKey("transient_test")).Ctx.
		WithBlockTime(time.Unix(86400*100, 0).UTC())

	bank := &mintingBank{}
	k := keeper.NewKeeper(
		runtime.NewKVStoreService(storeKey),
		encCfg.Codec,
		addresscodec.NewBech32Codec(sdk.GetConfig().GetBech32AccountAddrPrefix()),
		authtypes.NewModuleAddress(types.GovModuleName),
		bank,
		stubStakingKeeper{},
		bank,
	)
	require.NoError(t, k.Params.Set(ctx, types.DefaultParams()))
	return k, ctx, bank
}

// seedPool writes a pool with the given reserves and volume, keeping the global
// LP denominator in step the way a real swap would.
//
// It does not fund the module account — seedFundedPool does. The two exist
// separately because most tests here care about reward arithmetic and not about
// whether the coins are really there, while the invariant tests care about
// exactly that.
// seedTokenDenom mirrors the one-pool-per-token rule PoolByToken enforces. The
// fixture used to give every pool the same denom, which no real chain state can
// reach and which quietly made two pools claim the same coins.
func seedTokenDenom(id uint64) string {
	if id == 1 {
		return "utok"
	}
	return fmt.Sprintf("utok%d", id)
}

func seedPool(t *testing.T, k keeper.Keeper, ctx sdk.Context, id uint64, erth, token, volume int64) {
	t.Helper()
	denom := seedTokenDenom(id)
	require.NoError(t, k.SetPool(ctx, id, types.Pool{
		PoolId:        id,
		ReserveErth:   sdk.NewInt64Coin("uerth", erth),
		ReserveToken:  sdk.NewInt64Coin(denom, token),
		VolumeWeight:  math.NewInt(volume),
		LastTradedDay: uint64(ctx.BlockTime().Unix()) / 86400,
	}))
	require.NoError(t, k.PoolByToken.Set(ctx, denom, id))

	total, err := k.LpTotalVolume.Get(ctx)
	if err != nil {
		total = math.ZeroInt()
	}
	require.NoError(t, k.LpTotalVolume.Set(ctx, total.Add(math.NewInt(volume))))
}

// seedFundedPool is seedPool plus the coins to back it, which is the state a
// funded genesis actually leaves: reserves recorded in the pool AND sitting on
// the module account.
func seedFundedPool(t *testing.T, k keeper.Keeper, ctx sdk.Context, bank *mintingBank,
	id uint64, erth, token, volume int64) {
	t.Helper()
	seedPool(t, k, ctx, id, erth, token, volume)
	bank.fundModule(sdk.NewInt64Coin("uerth", erth), sdk.NewInt64Coin(seedTokenDenom(id), token))
}

// TestDistributeLPRewards_IsLazy is the point of the whole refactor: handing
// rewards to the dex must not touch the pool set. Nothing is minted until a pool
// is actually used.
func TestDistributeLPRewards_IsLazy(t *testing.T) {
	k, ctx, bank := initRewardFixture(t)
	seedPool(t, k, ctx, 1, 1_000_000, 1_000_000, 1_000)

	consumed := distributeLP(t, k, ctx, bank, math.NewInt(500))
	require.True(t, consumed.IsPositive(), "reward with volume present should be consumed")
	require.True(t, bank.minted.IsZero(), "x/dex must never mint LP rewards; x/allocation issues them")

	pending, err := k.PendingLpRewards.Get(ctx)
	require.NoError(t, err)
	require.Equal(t, math.NewInt(500), pending, "the reward is owed by the module until a pool settles it")

	// The reward is owed, not lost: the pool collects it on its next swap.
	_, err = k.SwapExactIn(ctx, sdk.AccAddress("trader______________"), sdk.NewInt64Coin("utok", 1_000), "uerth", math.ZeroInt())
	require.NoError(t, err)
	require.True(t, bank.minted.IsZero(), "collecting it must not mint either")
	pending, err = k.PendingLpRewards.Get(ctx)
	require.NoError(t, err)
	require.True(t, pending.IsZero(), "pool should collect the full reward on first touch")
}

// TestDistributeLPRewards_NoVolumeCarriesForward keeps the caller's carry-forward
// contract: with nothing to weight by, the option must keep its ERTH rather than
// have it silently written off.
func TestDistributeLPRewards_NoVolumeCarriesForward(t *testing.T) {
	k, ctx, _ := initRewardFixture(t)
	seedPool(t, k, ctx, 1, 1_000_000, 1_000_000, 0)

	consumed, err := k.DistributeLPRewards(ctx, math.NewInt(500))
	require.NoError(t, err)
	require.True(t, consumed.IsZero(), "no volume means nothing consumed")
}

// TestSettleBeforeAddLiquidity guards the exploit lazy settlement would
// otherwise open: rewards accrued before a depositor arrives must already be in
// the reserve when their shares are priced, or they buy into someone else's
// earnings.
func TestSettleBeforeAddLiquidity(t *testing.T) {
	k, ctx, bank := initRewardFixture(t)
	seedPool(t, k, ctx, 1, 1_000_000, 1_000_000, 1_000)

	distributeLP(t, k, ctx, bank, math.NewInt(100_000))

	ms := keeper.NewMsgServerImpl(k)
	addr, err := addresscodec.NewBech32Codec(sdk.GetConfig().GetBech32AccountAddrPrefix()).
		BytesToString(sdk.AccAddress("latecomer___________"))
	require.NoError(t, err)

	_, err = ms.AddLiquidity(ctx, &types.MsgAddLiquidity{
		Creator: addr,
		PoolId:  1,
		AmountA: sdk.NewInt64Coin("uerth", 1_000_000),
		AmountB: sdk.NewInt64Coin("utok", 1_000_000),
	})
	require.NoError(t, err)

	// The reward landed in the reserve before shares were priced — moved in from
	// the module's pending pile, not minted: x/allocation issued it already.
	require.True(t, bank.minted.AmountOf("uerth").IsZero(),
		"x/dex must not mint LP rewards (the LP shares AddLiquidity mints are a different denom)")
	pending, err := k.PendingLpRewards.Get(ctx)
	require.NoError(t, err)
	require.True(t, pending.IsZero(), "the reward should have moved out of pending and into the reserve")
	pool, err := k.Pool.Get(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, math.NewInt(1_000_000+100_000+1_000_000), pool.ReserveErth.Amount,
		"reserve should hold the settled reward plus the new deposit")
}

// TestDormantPoolCollectsWhatItWasAllocated pins the rule the old decay got
// wrong. A pool that goes quiet keeps the weight it had, because the denominator
// keeps it too — the two never part company, so the pool collects exactly what
// the index set aside for it.
//
// Settling at a decayed figure against an undecayed denominator was the bug:
// the pool was credited less than the stream had released on its behalf, and the
// difference was ERTH nobody ever received. Over a year of mixed pool activity
// that was 9-11% of the whole LP emission.
func TestDormantPoolCollectsWhatItWasAllocated(t *testing.T) {
	k, ctx, bank := initRewardFixture(t)
	seedPool(t, k, ctx, 1, 1_000_000, 1_000_000, 1_000)

	distributeLP(t, k, ctx, bank, math.NewInt(500))

	// Three days of silence. Nothing ages: the pool is the only holder of
	// weight, so it is owed the whole 500 whenever it comes back.
	later := ctx.WithBlockTime(ctx.BlockTime().Add(3 * 24 * time.Hour))
	before, err := k.Pool.Get(later, 1)
	require.NoError(t, err)
	_, err = k.SwapExactIn(later, sdk.AccAddress("trader______________"),
		sdk.NewInt64Coin("utok", 1_000), "uerth", math.ZeroInt())
	require.NoError(t, err)

	require.True(t, bank.minted.IsZero(), "x/dex must not mint LP rewards")
	pending, err := k.PendingLpRewards.Get(later)
	require.NoError(t, err)
	require.True(t, pending.IsZero(),
		"a dormant pool must still collect everything the index set aside for it, %s left owed", pending)
	_ = before

	// The denominator and the pools must never disagree: DistributeLPRewards
	// reports delta*LpTotalVolume as handed out, and the pools between them claim
	// delta*sum(Volume). With one pool the two are the same number.
	pool, err := k.Pool.Get(later, 1)
	require.NoError(t, err)
	total, err := k.LpTotalVolume.Get(later)
	require.NoError(t, err)
	require.Equal(t, pool.VolumeWeight, total, "LpTotalVolume must track the sum of stored pool volumes")
}

// TestNewVolumeOutweighsOld is the decay, expressed the way the scheme expresses
// it: nothing is taken away from old volume, new volume is simply worth more.
func TestNewVolumeOutweighsOld(t *testing.T) {
	k, ctx, bank := initRewardFixture(t)
	seedPool(t, k, ctx, 1, 1_000_000_000, 1_000_000_000, 0)
	seedPool(t, k, ctx, 2, 1_000_000_000, 1_000_000_000, 0)
	_ = bank

	// Both pools trade the same amount, a fortnight apart.
	require.NoError(t, k.AdvanceVolumeIndex(ctx))
	p1, err := k.Pool.Get(ctx, 1)
	require.NoError(t, err)
	require.NoError(t, k.ApplyVolumeForTest(ctx, &p1, math.NewInt(1_000_000)))
	require.NoError(t, k.SetPool(ctx, 1, p1))

	later := ctx.WithBlockTime(ctx.BlockTime().Add(types.VolumeDecayWindowDays * 24 * time.Hour))
	require.NoError(t, k.AdvanceVolumeIndex(later))
	p2, err := k.Pool.Get(later, 2)
	require.NoError(t, err)
	require.NoError(t, k.ApplyVolumeForTest(later, &p2, math.NewInt(1_000_000)))
	require.NoError(t, k.SetPool(later, 2, p2))

	p1, err = k.Pool.Get(later, 1)
	require.NoError(t, err)
	p2, err = k.Pool.Get(later, 2)
	require.NoError(t, err)
	require.True(t, p2.VolumeWeight.GT(p1.VolumeWeight),
		"a fortnight-old trade must weigh less than a fresh one of the same size: %s vs %s",
		p1.VolumeWeight, p2.VolumeWeight)
	// (14/13)^14 is about 2.75, so the fresh trade should be worth roughly
	// that much more. Loose bounds: this pins the direction and the order of
	// magnitude, not the rounding.
	ratio := p2.VolumeWeight.Mul(math.NewInt(100)).Quo(p1.VolumeWeight)
	require.True(t, ratio.GTE(math.NewInt(250)) && ratio.LTE(math.NewInt(300)),
		"fresh volume should be ~2.75x a fortnight-old one, got %s/100", ratio)
}

// TestStalePoolIsSweptOutOfTheDenominator is the other half of the scheme.
// Scaled volume never reaches zero on its own, so a pool nobody trades would
// hold a dwindling slice of every reward forever. The timer retires it — after
// paying it what it was owed right up to the moment it goes.
func TestStalePoolIsSweptOutOfTheDenominator(t *testing.T) {
	k, ctx, bank := initRewardFixture(t)
	seedPool(t, k, ctx, 1, 1_000_000, 1_000_000, 0)

	// Trading is what starts the clock.
	_, err := k.SwapExactIn(ctx, sdk.AccAddress("trader______________"),
		sdk.NewInt64Coin("utok", 1_000), "uerth", math.ZeroInt())
	require.NoError(t, err)
	pool, err := k.Pool.Get(ctx, 1)
	require.NoError(t, err)
	require.True(t, pool.VolumeWeight.IsPositive(), "the swap should have recorded volume")

	distributeLP(t, k, ctx, bank, math.NewInt(500))

	// Still inside the window: nothing is retired.
	early := ctx.WithBlockTime(ctx.BlockTime().Add(time.Duration(types.PoolStaleSeconds-1) * time.Second))
	require.NoError(t, k.SweepStalePools(early))
	pool, err = k.Pool.Get(early, 1)
	require.NoError(t, err)
	require.True(t, pool.VolumeWeight.IsPositive(), "a pool inside its window keeps its weight")

	// Past it: swept, and paid what it held right up to the sweep.
	late := ctx.WithBlockTime(ctx.BlockTime().Add(time.Duration(types.PoolStaleSeconds+1) * time.Second))
	require.NoError(t, k.SweepStalePools(late))
	pool, err = k.Pool.Get(late, 1)
	require.NoError(t, err)
	require.True(t, pool.VolumeWeight.IsZero(), "a stale pool must lose its weight")
	total, err := k.LpTotalVolume.Get(late)
	require.NoError(t, err)
	require.True(t, total.IsZero(), "and must leave the denominator with it")
	pending, err := k.PendingLpRewards.Get(late)
	require.NoError(t, err)
	require.True(t, pending.IsZero(), "the sweep must settle before it retires, %s stranded", pending)
}

// TestNewPoolCannotClaimPastRewards pins the index initialization: a pool created
// after rewards accrued starts at the current index and collects nothing
// retroactively.
func TestNewPoolCannotClaimPastRewards(t *testing.T) {
	k, ctx, bank := initRewardFixture(t)
	seedPool(t, k, ctx, 1, 1_000_000, 1_000_000, 1_000)
	distributeLP(t, k, ctx, bank, math.NewInt(100_000))

	ms := keeper.NewMsgServerImpl(k)
	addr, err := addresscodec.NewBech32Codec(sdk.GetConfig().GetBech32AccountAddrPrefix()).
		BytesToString(sdk.AccAddress("creator_____________"))
	require.NoError(t, err)

	before := bank.minted.AmountOf("uerth")
	_, err = ms.CreatePool(ctx, &types.MsgCreatePool{
		Creator: addr,
		AmountA: sdk.NewInt64Coin("uerth", 500_000),
		AmountB: sdk.NewInt64Coin("other", 500_000),
	})
	require.NoError(t, err)

	pool2, err := k.PoolForToken(ctx, "other")
	require.NoError(t, err)
	idx, err := k.PoolLpIndex.Get(ctx, pool2.PoolId)
	require.NoError(t, err)
	global, err := k.LpRewardIndex.Get(ctx)
	require.NoError(t, err)
	require.Equal(t, global, idx, "new pool must start at the current index")
	require.Equal(t, before, bank.minted.AmountOf("uerth"),
		"creating a pool must not mint historical rewards")
}

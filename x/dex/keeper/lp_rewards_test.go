package keeper_test

import (
	"context"
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
func seedPool(t *testing.T, k keeper.Keeper, ctx sdk.Context, id uint64, erth, token, volume int64) {
	t.Helper()
	require.NoError(t, k.Pool.Set(ctx, id, types.Pool{
		PoolId:        id,
		ReserveErth:   sdk.NewInt64Coin("uerth", erth),
		ReserveToken:  sdk.NewInt64Coin("utok", token),
		Volume:        math.NewInt(volume),
		LastVolumeDay: uint64(ctx.BlockTime().Unix()) / 86400,
	}))
	require.NoError(t, k.PoolByToken.Set(ctx, "utok", id))

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
	bank.fundModule(sdk.NewInt64Coin("uerth", erth), sdk.NewInt64Coin("utok", token))
}

// TestDistributeLPRewards_IsLazy is the point of the whole refactor: handing
// rewards to the dex must not touch the pool set. Nothing is minted until a pool
// is actually used.
func TestDistributeLPRewards_IsLazy(t *testing.T) {
	k, ctx, bank := initRewardFixture(t)
	seedPool(t, k, ctx, 1, 1_000_000, 1_000_000, 1_000)

	consumed, err := k.DistributeLPRewards(ctx, math.NewInt(500))
	require.NoError(t, err)
	require.True(t, consumed.IsPositive(), "reward with volume present should be consumed")
	require.True(t, bank.minted.IsZero(), "distribution must not mint before a pool is touched")

	// The reward is owed, not lost: the pool collects it on its next swap.
	_, err = k.SwapExactIn(ctx, sdk.AccAddress("trader______________"), sdk.NewInt64Coin("utok", 1_000), "uerth", math.ZeroInt())
	require.NoError(t, err)
	require.Equal(t, math.NewInt(500), bank.minted.AmountOf("uerth"),
		"pool should collect the full reward on first touch")
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

	_, err := k.DistributeLPRewards(ctx, math.NewInt(100_000))
	require.NoError(t, err)

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

	// The reward landed in the reserve before shares were priced.
	require.Equal(t, math.NewInt(100_000), bank.minted.AmountOf("uerth"))
	pool, err := k.Pool.Get(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, math.NewInt(1_000_000+100_000+1_000_000), pool.ReserveErth.Amount,
		"reserve should hold the settled reward plus the new deposit")
}

// TestSettleDecaysDormantVolumeFirst pins the rule for a pool that goes quiet:
// it collects against the volume it currently has, not the volume it last
// recorded. Settling at the stale figure over-paid dormant pools and left a
// wash-trade opening — spike volume, go quiet, then touch the pool to harvest
// the whole silent stretch at the peak rate.
func TestSettleDecaysDormantVolumeFirst(t *testing.T) {
	k, ctx, bank := initRewardFixture(t)
	seedPool(t, k, ctx, 1, 1_000_000, 1_000_000, 1_000)

	_, err := k.DistributeLPRewards(ctx, math.NewInt(500))
	require.NoError(t, err)

	// Three days of silence. Volume decays by (7-1)/7 each day, truncating:
	// 1000 -> 857 -> 734 -> 629. The pool therefore collects 629/1000 of the
	// 500 on offer, not all of it.
	later := ctx.WithBlockTime(ctx.BlockTime().Add(3 * 24 * time.Hour))
	_, err = k.SwapExactIn(later, sdk.AccAddress("trader______________"),
		sdk.NewInt64Coin("utok", 1_000), "uerth", math.ZeroInt())
	require.NoError(t, err)

	require.Equal(t, math.NewInt(314), bank.minted.AmountOf("uerth"),
		"a dormant pool must collect at its decayed volume, not its last recorded one")

	// The denominator and the pools must never disagree: DistributeLPRewards
	// reports delta*LpTotalVolume as handed out, and the pools between them claim
	// delta*sum(Volume). With one pool the two are the same number.
	pool, err := k.Pool.Get(later, 1)
	require.NoError(t, err)
	total, err := k.LpTotalVolume.Get(later)
	require.NoError(t, err)
	require.Equal(t, pool.Volume, total, "LpTotalVolume must track the sum of stored pool volumes")
}

// TestSettlePastWindowCollectsNothing is the far end of the same rule: a pool
// silent for a full window has no weight left, so the rewards its stale volume
// was holding a claim on are never minted.
func TestSettlePastWindowCollectsNothing(t *testing.T) {
	k, ctx, bank := initRewardFixture(t)
	seedPool(t, k, ctx, 1, 1_000_000, 1_000_000, 1_000)

	_, err := k.DistributeLPRewards(ctx, math.NewInt(500))
	require.NoError(t, err)

	later := ctx.WithBlockTime(ctx.BlockTime().Add(types.VolumeWindowDays * 24 * time.Hour))
	_, err = k.SwapExactIn(later, sdk.AccAddress("trader______________"),
		sdk.NewInt64Coin("utok", 1_000), "uerth", math.ZeroInt())
	require.NoError(t, err)

	require.True(t, bank.minted.AmountOf("uerth").IsZero(),
		"a pool dormant past the window has no volume to collect against, got %s",
		bank.minted.AmountOf("uerth"))
}

// TestNewPoolCannotClaimPastRewards pins the index initialization: a pool created
// after rewards accrued starts at the current index and collects nothing
// retroactively.
func TestNewPoolCannotClaimPastRewards(t *testing.T) {
	k, ctx, bank := initRewardFixture(t)
	seedPool(t, k, ctx, 1, 1_000_000, 1_000_000, 1_000)
	_, err := k.DistributeLPRewards(ctx, math.NewInt(100_000))
	require.NoError(t, err)

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

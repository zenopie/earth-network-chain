package keeper_test

import (
	"context"
	"testing"
	"time"

	"cosmossdk.io/collections"
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
	dex "github.com/earth-network/earth/x/dex/module"
	"github.com/earth-network/earth/x/dex/types"
	shieldedtypes "github.com/earth-network/earth/x/shielded/types"
	"github.com/earth-network/earth/zk/privacy"
)

// noteShielded is a shielded pool that records the notes it mints and, like
// the real one, refuses a note value above a u64.
type noteShielded struct {
	notes []sdk.Coin
	bank  *mintingBank
}

func (*noteShielded) IsShieldedOnly(denom string) bool { return denom == "uanml" }
func (*noteShielded) AssetID(context.Context, string) ([]byte, error) {
	return privacy.FieldBytes(privacy.U64(1)), nil
}
func (*noteShielded) CheckMint(context.Context, []byte, []byte) error { return nil }
func (s *noteShielded) MintNote(_ context.Context, _ string, c sdk.Coin, _, _ []byte) (uint64, []byte, error) {
	if !c.Amount.IsUint64() || !c.IsPositive() {
		return 0, nil, shieldedtypes.ErrInvalidNote.Wrapf("note value %s must be positive and fit a u64", c)
	}
	s.notes = append(s.notes, c)
	s.bank.debit(sdk.NewCoins(c))
	return uint64(len(s.notes) - 1), nil, nil
}
func (s *noteShielded) MintNoteSplit(ctx context.Context, from string, c sdk.Coin, pc, ct []byte) ([]uint64, error) {
	values, err := shieldedtypes.SplitNoteValues(c.Amount)
	if err != nil {
		return nil, shieldedtypes.ErrInvalidNote.Wrap(err.Error())
	}
	var out []uint64
	for _, v := range values {
		p, _, err := s.MintNote(ctx, from, sdk.NewCoin(c.Denom, math.NewIntFromUint64(v)), pc, ct)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}
func (*noteShielded) RegisterAsset(context.Context, string) ([]byte, error) { return nil, nil }
func (*noteShielded) ReleaseToModule(context.Context, shieldedtypes.PrivateMsg, string, string) (sdk.Coin, error) {
	return sdk.Coin{}, nil
}
func (*noteShielded) PayFeeFromModule(context.Context, string, math.Int) error { return nil }
func (*noteShielded) PrivateGasPrices(context.Context) (uint64, uint64, error) {
	return 1, 1, nil
}

func initNoteFixture(t *testing.T) (keeper.Keeper, sdk.Context, *mintingBank, *noteShielded) {
	t.Helper()
	encCfg := moduletestutil.MakeTestEncodingConfig(dex.AppModule{})
	storeKey := storetypes.NewKVStoreKey(types.StoreKey)
	ctx := testutil.DefaultContextWithDB(t, storeKey, storetypes.NewTransientStoreKey("transient_test")).Ctx.
		WithBlockTime(time.Unix(86400*100, 0).UTC())
	bank := &mintingBank{}
	sh := &noteShielded{bank: bank}
	k := keeper.NewKeeper(runtime.NewKVStoreService(storeKey), encCfg.Codec,
		addresscodec.NewBech32Codec(sdk.GetConfig().GetBech32AccountAddrPrefix()),
		authtypes.NewModuleAddress(types.GovModuleName), bank, stubStakingKeeper{}, bank, sh)
	require.NoError(t, k.Params.Set(ctx, types.DefaultParams()))
	return k, ctx, bank, sh
}

func bigInt(s string) math.Int {
	v, ok := math.NewIntFromString(s)
	if !ok {
		panic(s)
	}
	return v
}

// Audit 5 D1: a private LP withdrawal whose token leg ends above 2^64-1 at
// maturity (an attacker swapped the token in that block: 1.5e19 -> 1.95e19)
// is paid as two notes, not dropped.
func TestAudit5PrivateLegAboveU64IsSplit(t *testing.T) {
	k, ctx, bank, sh := initNoteFixture(t)
	const id = 1
	pool := types.Pool{PoolId: id, ReserveErth: sdk.NewCoin("uerth", bigInt("1000000000000")),
		ReserveToken: sdk.NewCoin("ufoo", bigInt("1300000000000000000000000")), VolumeWeight: math.ZeroInt()}
	require.NoError(t, k.SetPool(ctx, id, pool))
	require.NoError(t, k.PoolByToken.Set(ctx, "ufoo", id))
	bank.fundModule(pool.ReserveErth, pool.ReserveToken)
	total := bigInt("1000000000000000000") // 1e18 shares
	bank.setSupply(types.LPShareDenom(id), total)
	shares := sdk.NewCoin(types.LPShareDenom(id), bigInt("15000000000000")) // 1.5e13: token leg 1.95e19
	bank.fundModule(shares)
	entry := types.LpUnbonding{PoolId: id, Shares: shares, CompletionTime: ctx.BlockTime().Unix(),
		Pc: []byte{1}, Ciphertext: []byte{2}, ErthPc: []byte{3}, ErthCiphertext: []byte{4}, WithdrawalId: []byte{0, 9}}
	key, err := k.LpUnbondingKey(entry)
	require.NoError(t, err)
	require.NoError(t, k.LpUnbondings.Set(ctx, key, entry))

	require.NoError(t, k.SweepMaturedUnbondings(ctx))
	has, err := k.LpUnbondings.Has(ctx, key)
	require.NoError(t, err)
	require.False(t, has, "paid out")
	var token []math.Int
	for _, n := range sh.notes {
		if n.Denom == "ufoo" {
			token = append(token, n.Amount)
		}
	}
	require.Len(t, token, 2, "the token leg as two notes")
	require.Equal(t, math.NewIntFromUint64(shieldedtypes.MaxNoteValue), token[0])
	require.Equal(t, bigInt("19500000000000000000"), token[0].Add(token[1]))
}

// A payout that cannot be made (a leg past MaxSplitNotes notes) is never
// dropped: the entry stays with its shares escrowed, re-filed at a later
// retry time, and pays once the pool lets it.
func TestAudit5FailedPayoutIsRetriedNotDropped(t *testing.T) {
	k, ctx, bank, sh := initNoteFixture(t)
	const id = 1
	huge := math.NewIntFromUint64(shieldedtypes.MaxNoteValue).MulRaw(shieldedtypes.MaxSplitNotes + 1)
	pool := types.Pool{PoolId: id, ReserveErth: sdk.NewCoin("uerth", math.NewInt(1_000_000)),
		ReserveToken: sdk.NewCoin("ufoo", huge), VolumeWeight: math.ZeroInt()}
	require.NoError(t, k.SetPool(ctx, id, pool))
	require.NoError(t, k.PoolByToken.Set(ctx, "ufoo", id))
	bank.fundModule(pool.ReserveErth, pool.ReserveToken)
	bank.setSupply(types.LPShareDenom(id), math.NewInt(1000))
	shares := sdk.NewCoin(types.LPShareDenom(id), math.NewInt(1000)) // the whole pool
	bank.fundModule(shares)
	entry := types.LpUnbonding{PoolId: id, Shares: shares, CompletionTime: ctx.BlockTime().Unix(),
		Pc: []byte{1}, Ciphertext: []byte{2}, ErthPc: []byte{3}, ErthCiphertext: []byte{4}, WithdrawalId: []byte{0, 9}}
	key, err := k.LpUnbondingKey(entry)
	require.NoError(t, err)
	require.NoError(t, k.LpUnbondings.Set(ctx, key, entry))

	ctx = ctx.WithEventManager(sdk.NewEventManager())
	require.NoError(t, k.SweepMaturedUnbondings(ctx))
	// (The stub records notes the discarded branch minted; the store has none.)
	for _, n := range sh.notes {
		require.NotEqual(t, "ufoo", n.Denom, "the token leg was not paid")
	}
	var kept []types.LpUnbonding
	require.NoError(t, k.LpUnbondings.Walk(ctx, nil, func(_ collections.Triple[int64, uint64, []byte], u types.LpUnbonding) (bool, error) {
		kept = append(kept, u)
		return false, nil
	}))
	require.Len(t, kept, 1, "kept, not dropped")
	require.Equal(t, uint32(1), kept[0].PayoutAttempts)
	require.Equal(t, ctx.BlockTime().Unix()+types.LpUnbondRetryDelay(1), kept[0].CompletionTime)
	require.Equal(t, shares, kept[0].Shares, "still escrowed")

	// Not retried before its time; a second failure backs off further.
	require.NoError(t, k.SweepMaturedUnbondings(ctx))
	later := ctx.WithBlockTime(time.Unix(kept[0].CompletionTime, 0))
	require.NoError(t, k.SweepMaturedUnbondings(later))
	kept = kept[:0]
	require.NoError(t, k.LpUnbondings.Walk(later, nil, func(_ collections.Triple[int64, uint64, []byte], u types.LpUnbonding) (bool, error) {
		kept = append(kept, u)
		return false, nil
	}))
	require.Len(t, kept, 1)
	require.Equal(t, uint32(2), kept[0].PayoutAttempts)
	require.Equal(t, later.BlockTime().Unix()+types.LpUnbondRetryDelay(2), kept[0].CompletionTime)

	// The pool comes back within what notes can pay: the retry pays.
	pool.ReserveToken = sdk.NewCoin("ufoo", math.NewInt(5_000_000))
	require.NoError(t, k.SetPool(later, id, pool))
	last := later.WithBlockTime(time.Unix(kept[0].CompletionTime, 0))
	sh.notes = nil
	// The stub bank is not branch-aware: put back the shares the two
	// discarded branches burned.
	bank.setSupply(types.LPShareDenom(id), math.NewInt(2000))
	require.NoError(t, k.SweepMaturedUnbondings(last))
	n := 0
	require.NoError(t, k.LpUnbondings.Walk(last, nil, func(collections.Triple[int64, uint64, []byte], types.LpUnbonding) (bool, error) {
		n++
		return false, nil
	}))
	require.Zero(t, n)
	require.Len(t, sh.notes, 2, "both legs paid")
}

func TestAudit5SplitNoteValues(t *testing.T) {
	max := math.NewIntFromUint64(shieldedtypes.MaxNoteValue)
	v, err := shieldedtypes.SplitNoteValues(max)
	require.NoError(t, err)
	require.Equal(t, []uint64{shieldedtypes.MaxNoteValue}, v)
	v, err = shieldedtypes.SplitNoteValues(max.AddRaw(5))
	require.NoError(t, err)
	require.Equal(t, []uint64{shieldedtypes.MaxNoteValue, 5}, v)
	_, err = shieldedtypes.SplitNoteValues(max.MulRaw(shieldedtypes.MaxSplitNotes))
	require.NoError(t, err)
	_, err = shieldedtypes.SplitNoteValues(max.MulRaw(shieldedtypes.MaxSplitNotes).AddRaw(1))
	require.Error(t, err)
	_, err = shieldedtypes.SplitNoteValues(math.ZeroInt())
	require.Error(t, err)
}

// A withdrawal whose note leg is already above a quarter of what a payout can
// mint as notes is refused when it starts (withdraw in smaller parts), so it
// does not sit failing at maturity.
func TestAudit5WithdrawalNoteLegCappedAtStart(t *testing.T) {
	k, ctx, bank, _ := initNoteFixture(t)
	const id = 1
	quarter := math.NewIntFromUint64(shieldedtypes.MaxNoteValue).MulRaw(shieldedtypes.MaxSplitNotes / 4)
	pool := types.Pool{PoolId: id, ReserveErth: sdk.NewCoin("uerth", math.NewInt(1_000_000)),
		ReserveToken: sdk.NewCoin("uanml", quarter.MulRaw(2)), VolumeWeight: math.ZeroInt()}
	require.NoError(t, k.SetPool(ctx, id, pool))
	require.NoError(t, k.PoolByToken.Set(ctx, "uanml", id))
	bank.setSupply(types.LPShareDenom(id), math.NewInt(1000))
	ms := keeper.NewMsgServerImpl(k)
	creator := sdk.AccAddress("provider____________")
	ct := make([]byte, shieldedtypes.BlindCiphertextBytes)
	_, err := ms.RemoveLiquidity(ctx, &types.MsgRemoveLiquidity{Creator: bech32(t, creator), PoolId: id,
		Shares: sdk.NewInt64Coin(types.LPShareDenom(id), 600), Pc: privacy.FieldBytes(privacy.U64(1)), Ciphertext: ct})
	require.ErrorIs(t, err, types.ErrInvalidAmount)
	_, err = ms.RemoveLiquidity(ctx, &types.MsgRemoveLiquidity{Creator: bech32(t, creator), PoolId: id,
		Shares: sdk.NewInt64Coin(types.LPShareDenom(id), 400), Pc: privacy.FieldBytes(privacy.U64(1)), Ciphertext: ct})
	require.NoError(t, err)
}

// Audit 5 L-DX3: the TWAP accumulator survives an export and import.
func TestAudit5TwapAccumulatorExported(t *testing.T) {
	k, ctx, bank := initRewardFixture(t)
	seedFundedPool(t, k, ctx, bank, 1, 1_000_000, 1_000_000, 0)
	require.NoError(t, k.PriceCumulative.Set(ctx, 1, math.LegacyNewDec(12345)))
	require.NoError(t, k.PriceObservedAt.Set(ctx, 1, 777))
	gs, err := k.ExportGenesis(ctx)
	require.NoError(t, err)
	require.NoError(t, gs.Validate())
	require.Equal(t, []types.PriceAccumulator{{PoolId: 1, Cumulative: math.LegacyNewDec(12345), ObservedAt: 777}}, gs.PriceAccumulators)
	k2, ctx2, _ := initRewardFixture(t)
	require.NoError(t, k2.InitGenesis(ctx2, *gs))
	cum, err := k2.PriceCumulative.Get(ctx2, 1)
	require.NoError(t, err)
	require.Equal(t, math.LegacyNewDec(12345), cum)
	at, err := k2.PriceObservedAt.Get(ctx2, 1)
	require.NoError(t, err)
	require.Equal(t, int64(777), at)
}

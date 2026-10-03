package keeper

import (
	"testing"
	"time"

	storetypes "cosmossdk.io/store/types"
	addresscodec "github.com/cosmos/cosmos-sdk/codec/address"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/cosmos/cosmos-sdk/testutil"
	sdk "github.com/cosmos/cosmos-sdk/types"
	moduletestutil "github.com/cosmos/cosmos-sdk/types/module/testutil"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/stretchr/testify/require"

	"github.com/earth-network/earth/x/personhood/types"
	"github.com/earth-network/earth/zk/privacy"
)

func codeKeeper(t *testing.T) (Keeper, sdk.Context) {
	t.Helper()
	encCfg := moduletestutil.MakeTestEncodingConfig()
	ac := addresscodec.NewBech32Codec(sdk.GetConfig().GetBech32AccountAddrPrefix())
	storeKey := storetypes.NewKVStoreKey(types.StoreKey)
	base := testutil.DefaultContextWithDB(t, storeKey, storetypes.NewTransientStoreKey("transient_test")).Ctx
	k := NewKeeper(runtime.NewKVStoreService(storeKey), encCfg.Codec, ac, authtypes.NewModuleAddress(types.GovModuleName),
		&countingBank{}, stubDex{}, nil, stubAllocation{}, &burnLog{}, stubShielded{})
	ctx := base.WithBlockTime(time.Unix(1_800_000_000, 0).UTC())
	p := types.DefaultParams()
	p.CaretakerVoteSeconds = 1000
	require.NoError(t, k.Params.Set(ctx, p))
	return k, ctx
}

// Referral codes: claimed with a binding, unique, one active per binding,
// reserved for the grace period after the binding lapses, kept across a
// rebind to a new address, released after a clear, resolvable to the bound
// address while live, swept once released, and carried through genesis.
func TestReferralCodes(t *testing.T) {
	k, ctx := codeKeeper(t)
	nfA := privacy.FieldBytes(privacy.U64(1))
	nfB := privacy.FieldBytes(privacy.U64(2))
	addr := func(s string) string { return sdk.AccAddress([]byte(s + "____________________")[:20]).String() }
	resolve := func(c sdk.Context, code string) (string, bool) {
		bz, live, _, err := k.resolveReferralCode(c, code)
		require.NoError(t, err)
		if bz == nil {
			return "", live
		}
		return sdk.AccAddress(bz).String(), live
	}

	// Claim.
	_, code, err := k.applyBindReferrer(ctx, nfA, addr("a1"), "alice")
	require.NoError(t, err)
	require.Equal(t, "alice", code)
	got, live := resolve(ctx, "alice")
	require.True(t, live)
	require.Equal(t, addr("a1"), got)

	// Uniqueness: another binding cannot take it (the ante's check too).
	_, _, err = k.applyBindReferrer(ctx, nfB, addr("b1"), "alice")
	require.ErrorIs(t, err, types.ErrReferralCodeTaken)
	require.ErrorIs(t, k.codeAvailable(ctx, nfB, "alice"), types.ErrReferralCodeTaken)
	_, _, err = k.applyBindReferrer(ctx, nfB, addr("b1"), "bob")
	require.NoError(t, err)

	// Rebind to a new address with no code keeps the code.
	later := ctx.WithBlockTime(ctx.BlockTime().Add(500 * time.Second))
	_, code, err = k.applyBindReferrer(later, nfA, addr("a2"), "")
	require.NoError(t, err)
	require.Equal(t, "alice", code)
	got, live = resolve(later, "alice")
	require.True(t, live)
	require.Equal(t, addr("a2"), got, "the code follows the binding's new address")

	// One active code per binding: moving to another code releases the old
	// one, which stays reserved to A for the grace period.
	_, code, err = k.applyBindReferrer(later, nfA, addr("a2"), "alice2")
	require.NoError(t, err)
	require.Equal(t, "alice2", code)
	_, live = resolve(later, "alice")
	require.False(t, live, "no longer A's active code")
	require.ErrorIs(t, k.codeAvailable(later, nfB, "alice"), types.ErrReferralCodeTaken, "reserved to A")
	require.NoError(t, k.codeAvailable(later, nfA, "alice"))

	// Lapse: B's binding expires; its code is not live but stays reserved
	// for the grace period after the expiry, then anyone may take it.
	bExp := ctx.BlockTime().Unix() + 1000
	lapsed := ctx.WithBlockTime(time.Unix(bExp+1, 0))
	_, live = resolve(lapsed, "bob")
	require.False(t, live, "a lapsed binding's code names no live referrer")
	require.ErrorIs(t, k.codeAvailable(lapsed, nfA, "bob"), types.ErrReferralCodeTaken)
	free := ctx.WithBlockTime(time.Unix(bExp+types.ReferralCodeGraceSeconds, 0))
	require.NoError(t, k.codeAvailable(free, nfA, "bob"))

	// Clear: A's active code enters its grace period from the clear.
	clearAt := later.WithBlockTime(later.BlockTime().Add(10 * time.Second))
	_, _, err = k.applyBindReferrer(clearAt, nfA, "", "")
	require.NoError(t, err)
	rec, err := k.ReferralCodes.Get(clearAt, "alice2")
	require.NoError(t, err)
	require.Equal(t, clearAt.BlockTime().Unix()+types.ReferralCodeGraceSeconds, rec.ReleasesAt)
	_, live = resolve(clearAt, "alice2")
	require.False(t, live)
	// A rebind within the grace period with no code takes it back.
	_, code, err = k.applyBindReferrer(clearAt, nfA, addr("a3"), "")
	require.NoError(t, err)
	require.Equal(t, "alice2", code)

	// Genesis round trip.
	gs, err := k.ExportGenesis(clearAt)
	require.NoError(t, err)
	require.NoError(t, gs.Validate())
	require.Len(t, gs.ReferralCodes, 3) // alice (reserved), alice2 (active), bob
	k2, ctx2 := codeKeeper(t)
	ctx2 = ctx2.WithBlockTime(clearAt.BlockTime())
	require.NoError(t, k2.InitGenesis(ctx2, *gs))
	bz, live, _, err := k2.resolveReferralCode(ctx2, "alice2")
	require.NoError(t, err)
	require.True(t, live)
	require.Equal(t, addr("a3"), sdk.AccAddress(bz).String())
	_, live, _, err = k2.resolveReferralCode(ctx2, "alice")
	require.NoError(t, err)
	require.False(t, live, "the reserved code is not A's active one after import")

	// The sweep deletes codes past their release, never live ones.
	far := ctx.WithBlockTime(time.Unix(bExp+types.ReferralCodeGraceSeconds+1, 0))
	n, err := k.sweepReferralCodes(far, 100)
	require.NoError(t, err)
	require.Equal(t, 2, n, "bob and alice")
	_, err = k.ReferralCodes.Get(far, "bob")
	require.Error(t, err)
	_, err = k.ReferralCodes.Get(far, "alice2")
	require.NoError(t, err)
}

func TestReferralCodeFormat(t *testing.T) {
	for _, ok := range []string{"abc", "alice-2", "a1b2c3", "x-y-z", "0123456789abcdefghijklmnopqrstuv"} {
		require.NoError(t, types.ValidateReferralCode(ok), ok)
	}
	for _, bad := range []string{"", "ab", "Alice", "-abc", "abc-", "a_b", "a b", "ålice", "0123456789abcdefghijklmnopqrstuvw"} {
		require.Error(t, types.ValidateReferralCode(bad), bad)
	}
}

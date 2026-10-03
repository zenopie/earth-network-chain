package keeper

import (
	"path/filepath"
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
	pkikeeper "github.com/earth-network/earth/x/pki/keeper"
	pkitypes "github.com/earth-network/earth/x/pki/types"
	shieldedtypes "github.com/earth-network/earth/x/shielded/types"
	"github.com/earth-network/earth/zk/privacy"
)

// Audit 3 L1: a governance raise of current_date_max_skew_seconds must not
// re-open the A -> B -> A replay of a landed registration. The used binding
// is held for the largest skew governance may set, counted from the proof's
// own current_date.
func TestAudit3SkewRaiseDoesNotReopenReplay(t *testing.T) {
	encCfg := moduletestutil.MakeTestEncodingConfig()
	ac := addresscodec.NewBech32Codec(sdk.GetConfig().GetBech32AccountAddrPrefix())
	pkiStore := storetypes.NewKVStoreKey(pkitypes.StoreKey)
	phStore := storetypes.NewKVStoreKey(types.StoreKey)
	ctx := testutil.DefaultContextWithKeys(map[string]*storetypes.KVStoreKey{
		pkitypes.StoreKey: pkiStore, types.StoreKey: phStore,
	}, nil, nil).WithBlockTime(passportTime)
	ctx = shieldedtypes.WithTxFields(ctx, shieldedtypes.TxFields{})
	pki := pkikeeper.NewKeeper(runtime.NewKVStoreService(pkiStore), encCfg.Codec, ac, authtypes.NewModuleAddress(pkitypes.GovModuleName))
	require.NoError(t, pki.InitGenesis(ctx, pkitypes.GenesisState{
		Params: pkitypes.NewParams(),
		Cscas:  []pkitypes.Csca{{CertificateDer: readFileAt(t, filepath.Join(passportDir, "A1", "csca.der"))}},
	}))
	k := NewKeeper(runtime.NewKVStoreService(phStore), encCfg.Codec, ac, authtypes.NewModuleAddress(types.GovModuleName),
		nil, stubDex{}, pki, stubAllocation{}, &burnLog{}, stubShielded{})
	params := leanParams(t)
	require.NoError(t, k.Params.Set(ctx, params))

	replayed := passportMsg(t, "A1")
	p, err := k.checkRegistration(ctx, replayed)
	require.NoError(t, err)
	require.NoError(t, k.markBindingUsed(ctx, p.binding, p.proofDate))
	b := privacy.FieldBytes(privacy.U64(424242))
	require.NoError(t, k.addRegistration(ctx, types.Registration{Nullifier: p.nullifier, RegisteredAt: ctx.BlockTime().Unix(),
		ActivatedAt: ctx.BlockTime().Unix(), Idc: b}))

	// Three days on (past the old skew + grace) the entry is still there.
	ctx3 := ctx.WithBlockTime(passportTime.Add(72 * time.Hour))
	n, err := k.sweepUsedBindings(ctx3, 10)
	require.NoError(t, err)
	require.Zero(t, n)

	// Governance raises the skew to 30 days, then to the maximum: refused.
	for _, skew := range []uint64{30 * types.SecondsPerDay, types.MaxCurrentDateMaxSkewSeconds} {
		params.CurrentDateMaxSkewSeconds = skew
		require.NoError(t, params.Validate())
		require.NoError(t, k.Params.Set(ctx3, params))
		_, err = k.checkRegistration(ctx3, replayed)
		require.ErrorIs(t, err, types.ErrBindingUsed)
	}
	// The hold covers the whole maximal window.
	last := time.Unix(p.proofDate+types.MaxCurrentDateMaxSkewSeconds, 0)
	_, err = k.checkRegistration(ctx.WithBlockTime(last), replayed)
	require.ErrorIs(t, err, types.ErrBindingUsed)
	params.CurrentDateMaxSkewSeconds = types.MaxCurrentDateMaxSkewSeconds + 1
	require.Error(t, params.Validate())
}

// Audit 3 L2: an export taken mid-purge carries the purge (and the daily rate
// counters), so the import finishes retiring the revoked signer's
// registrations and keeps today's caps.
func TestAudit3GenesisKeepsPendingPurgeAndRates(t *testing.T) {
	k, _, ctx := capKeeper(t)
	dsc := privacy.FieldBytes(privacy.U64(777))
	for i := 0; i < 50; i++ {
		seedReg(t, k, ctx, i, dsc, ctx.BlockTime().Unix())
	}
	require.NoError(t, k.StartDscPurge(ctx, dsc))
	_, err := k.purgeRevokedDscs(ctx, 10)
	require.NoError(t, err)
	require.Equal(t, 40, countRegistrations(t, k, ctx))
	day := dayOf(ctx.BlockTime().Unix())
	require.NoError(t, k.NetworkRate.Set(ctx, types.RateCounter{Day: day, Count: 7, PreviousCount: 30}))
	require.NoError(t, k.DscRate.Set(ctx, dsc, types.RateCounter{Day: day, Count: 5}))
	require.NoError(t, k.CountryRate.Set(ctx, "DE", types.RateCounter{Day: day, Count: 6}))

	gs, err := k.ExportGenesis(ctx)
	require.NoError(t, err)
	require.NoError(t, gs.Validate())

	k2, _, ctx2 := capKeeper(t)
	require.NoError(t, k2.InitGenesis(ctx2, *gs))
	nr, err := k2.NetworkRate.Get(ctx2)
	require.NoError(t, err)
	require.Equal(t, types.RateCounter{Day: day, Count: 7, PreviousCount: 30}, nr)
	dr, err := k2.DscRate.Get(ctx2, dsc)
	require.NoError(t, err)
	require.Equal(t, uint64(5), dr.Count)
	cr, err := k2.CountryRate.Get(ctx2, "DE")
	require.NoError(t, err)
	require.Equal(t, uint64(6), cr.Count)

	for i := 0; i < 20; i++ {
		ctx2 = ctx2.WithBlockTime(ctx2.BlockTime().Add(6 * time.Second))
		require.NoError(t, k2.runSweeps(ctx2, types.DefaultRegistrationSweepLimit))
	}
	has, err := k2.PendingDscPurge.Has(ctx2, dsc)
	require.NoError(t, err)
	require.False(t, has)
	require.Zero(t, countRegistrations(t, k2, ctx2), "revoked signer's registrations all retired after import")
}

// Audit 3 I1: an impossible current_date is refused, not normalised.
func TestAudit3ImpossibleDateRefused(t *testing.T) {
	for _, d := range []uint64{250231, 250230, 250431, 230229} {
		_, err := yymmddToUnix(privacy.FieldBytes(privacy.U64(d)))
		require.Error(t, err, "%d", d)
	}
	u, err := yymmddToUnix(privacy.FieldBytes(privacy.U64(240229))) // leap day
	require.NoError(t, err)
	require.Equal(t, time.Date(2024, 2, 29, 0, 0, 0, 0, time.UTC).Unix(), u)
}

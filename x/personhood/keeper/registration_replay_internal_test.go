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

// Re-audit R1 (A -> B -> A replay): a holder registered to idc A (a public
// tx), then switched to idc B. Anyone replaying the public A registration
// within the current_date skew would have it accepted as a switch back to A.
// A landed binding is refused for reuse, by the ante and by `earthd
// gas-check registration` (CheckRegistration) alike, until the date check
// refuses the proof anyway.
func TestRegistrationABAReplayRefused(t *testing.T) {
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

	replayed := passportMsg(t, "A1") // the original registration to idc A, public on chain
	nf, _, err := k.CheckRegistration(ctx, replayed)
	require.NoError(t, err)
	p, err := k.checkRegistration(ctx, replayed)
	require.NoError(t, err)
	// It lands (what Register records), then the holder switches to idc B.
	require.NoError(t, k.markBindingUsed(ctx, p.binding, p.proofDate))
	b := privacy.FieldBytes(privacy.U64(424242))
	require.NoError(t, k.addRegistration(ctx, types.Registration{Nullifier: nf, RegisteredAt: ctx.BlockTime().Unix(),
		ActivatedAt: ctx.BlockTime().Unix(), Idc: b}))

	// The replay of A is refused, by the ante's check and by gas-check.
	_, err = k.checkRegistration(ctx, replayed)
	require.ErrorIs(t, err, types.ErrBindingUsed)
	_, _, err = k.CheckRegistration(ctx, replayed)
	require.ErrorIs(t, err, types.ErrBindingUsed)
	// ...a day and a half later, still inside the skew: still refused.
	ctx2 := ctx.WithBlockTime(passportTime.Add(36 * time.Hour))
	_, err = k.checkRegistration(ctx2, replayed)
	require.ErrorIs(t, err, types.ErrBindingUsed)

	// Exported and imported with the rest of genesis.
	gs, err := k.ExportGenesis(ctx)
	require.NoError(t, err)
	require.Len(t, gs.UsedBindings, 1)
	require.Equal(t, p.binding, gs.UsedBindings[0].Binding)

	// The entry outlives the skew: once it is swept, the date check refuses.
	until, err := k.UsedBindings.Get(ctx, p.binding)
	require.NoError(t, err)
	require.Equal(t, p.proofDate+types.MaxCurrentDateMaxSkewSeconds+types.UsedBindingGraceSeconds, until)
	ctx3 := ctx.WithBlockTime(time.Unix(until, 0))
	n, err := k.sweepUsedBindings(ctx3, 10)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	has, err := k.UsedBindings.Has(ctx3, p.binding)
	require.NoError(t, err)
	require.False(t, has)
	_, err = k.checkRegistration(ctx3, replayed)
	require.ErrorIs(t, err, types.ErrBadPublicInputs, "past the entry's expiry the proof's current_date is out of the skew")
}

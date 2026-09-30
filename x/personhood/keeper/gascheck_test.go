package keeper

import (
	"path/filepath"
	"testing"

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
	"github.com/earth-network/earth/zk/privacy"
)

// TestCheckRegistrationAgreesWithTheChain drives CheckRegistration (what
// `earthd gas-check registration` runs for the gas backend) with a real x/pki
// keeper holding the fixture's CSCA and a real passport proof: it accepts the
// registration the chain would take, reports a live one as a switch, refuses
// one whose notes were swapped, and refuses once the DSC is revoked.
func TestCheckRegistrationAgreesWithTheChain(t *testing.T) {
	encCfg := moduletestutil.MakeTestEncodingConfig()
	ac := addresscodec.NewBech32Codec(sdk.GetConfig().GetBech32AccountAddrPrefix())
	pkiStore := storetypes.NewKVStoreKey(pkitypes.StoreKey)
	phStore := storetypes.NewKVStoreKey(types.StoreKey)
	ctx := testutil.DefaultContextWithKeys(map[string]*storetypes.KVStoreKey{
		pkitypes.StoreKey: pkiStore, types.StoreKey: phStore,
	}, nil, nil).WithBlockTime(passportTime)

	pki := pkikeeper.NewKeeper(runtime.NewKVStoreService(pkiStore), encCfg.Codec, ac, authtypes.NewModuleAddress(pkitypes.GovModuleName))
	require.NoError(t, pki.InitGenesis(ctx, pkitypes.GenesisState{
		Params: pkitypes.NewParams(),
		Cscas:  []pkitypes.Csca{{CertificateDer: readFileAt(t, filepath.Join(passportDir, "A1", "csca.der"))}},
	}))
	k := NewKeeper(runtime.NewKVStoreService(phStore), encCfg.Codec, ac, authtypes.NewModuleAddress(types.GovModuleName),
		nil, stubDex{}, pki, stubAllocation{}, &burnLog{}, stubShielded{})
	require.NoError(t, k.Params.Set(ctx, leanParams(t)))

	m := passportMsg(t, "A1")
	nf, switched, err := k.CheckRegistration(ctx, m)
	require.NoError(t, err)
	require.False(t, switched)
	require.Len(t, nf, 32)

	// A live registration under the passport: the same check reports a switch.
	require.NoError(t, k.addRegistration(ctx, types.Registration{Nullifier: nf, RegisteredAt: ctx.BlockTime().Unix(),
		ActivatedAt: ctx.BlockTime().Unix(), Idc: m.Idc}))
	_, switched, err = k.CheckRegistration(ctx, m)
	require.NoError(t, err)
	require.True(t, switched)

	swapped := passportMsg(t, "A1")
	swapped.PcErth = privacy.FieldBytes(privacy.U64(7))
	_, _, err = k.CheckRegistration(ctx, swapped)
	require.ErrorIs(t, err, types.ErrBadPublicInputs)

	pub, err := pki.VerifyDsc(ctx, m.DscDer)
	require.NoError(t, err)
	require.NoError(t, pki.RevokeDsc(ctx, pub))
	_, _, err = k.CheckRegistration(ctx, m)
	require.Error(t, err)
}

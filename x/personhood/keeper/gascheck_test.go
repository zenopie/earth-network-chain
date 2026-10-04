package keeper

import (
	shieldedtest "github.com/earth-network/earth/x/shielded/testutil"
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
	shieldedtypes "github.com/earth-network/earth/x/shielded/types"
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
	}, nil, nil).WithBlockTime(passportTime).WithChainID(shieldedtest.ChainID)
	ctx = shieldedtypes.WithTxFields(ctx, shieldedtypes.TxFields{}) // as the private ante records them

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
	// The msg as a wallet sends it, its fee bundle in place (unfunded,
	// unproven: the backend has not paid for it yet): the same answer.
	withFee := passportMsg(t, "A1")
	withFee.Fee = feeStub()
	nf2, _, err := k.CheckRegistration(ctx, withFee)
	require.NoError(t, err)
	require.Equal(t, nf, nf2)

	prepared, err := k.checkRegistration(ctx, m)
	require.NoError(t, err)
	dsc := prepared.dsc.key
	require.Len(t, dsc, 32)

	// A live registration under the passport to another identity: the same
	// check reports a switch.
	other := types.Registration{Nullifier: nf, RegisteredAt: ctx.BlockTime().Unix(),
		ActivatedAt: ctx.BlockTime().Unix(), Idc: privacy.FieldBytes(privacy.U64(424242)),
		DscKey: dsc, Country: prepared.dsc.country}
	require.NoError(t, k.addRegistration(ctx, other))
	_, switched, err = k.CheckRegistration(ctx, m)
	require.NoError(t, err)
	require.True(t, switched)

	// Made under another Document Signer: not a switch the holder could make
	// (a re-proof is signed by the same signer). Refused (audit 6 B6-1).
	foreign := other
	foreign.DscKey = append(make([]byte, 31), 7)
	foreign.Country = "DE"
	require.NoError(t, k.Registrations.Set(ctx, nf, foreign))
	_, _, err = k.CheckRegistration(ctx, m)
	require.ErrorIs(t, err, types.ErrSwitchSignerMismatch)
	require.NoError(t, k.Registrations.Set(ctx, nf, other))

	// A switch counts against its signer's daily cap: at the cap, deferred;
	// the network and country caps do not apply to it.
	day := dayOf(ctx.BlockTime().Unix())
	params, err := k.Params.Get(ctx)
	require.NoError(t, err)
	require.NoError(t, k.DscRate.Set(ctx, dsc, types.RateCounter{Day: day, Count: params.DscDailyCap(0)}))
	_, _, err = k.CheckRegistration(ctx, m)
	require.ErrorIs(t, err, types.ErrRegistrationRateLimited)
	require.NoError(t, k.DscRate.Set(ctx, dsc, types.RateCounter{Day: day, Count: params.DscDailyCap(0) - 1}))
	require.NoError(t, k.NetworkRate.Set(ctx, types.RateCounter{Day: day, Count: params.NetworkDailyCap(0)}))
	_, switched, err = k.CheckRegistration(ctx, m)
	require.NoError(t, err)
	require.True(t, switched)
	require.NoError(t, k.recordSwitchRate(ctx, dsc))
	c, err := k.DscRate.Get(ctx, dsc)
	require.NoError(t, err)
	require.Equal(t, params.DscDailyCap(0), c.Count)
	nw, err := k.NetworkRate.Get(ctx)
	require.NoError(t, err)
	require.Equal(t, params.NetworkDailyCap(0), nw.Count, "a switch is not a new registration")
	require.NoError(t, k.DscRate.Remove(ctx, dsc))
	require.NoError(t, k.NetworkRate.Remove(ctx))

	// Live under this very identity: a replay of the registration, refused.
	same := other
	same.Idc = m.Idc
	require.NoError(t, k.Registrations.Set(ctx, nf, same))
	_, _, err = k.CheckRegistration(ctx, m)
	require.ErrorIs(t, err, types.ErrRegistrationReplay)

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

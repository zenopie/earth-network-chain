package keeper

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/consensys/gnark-crypto/ecc/bn254/fr"

	storetypes "cosmossdk.io/store/types"
	addresscodec "github.com/cosmos/cosmos-sdk/codec/address"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/cosmos/cosmos-sdk/testutil"
	sdk "github.com/cosmos/cosmos-sdk/types"
	moduletestutil "github.com/cosmos/cosmos-sdk/types/module/testutil"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/stretchr/testify/require"

	personhoodtest "github.com/earth-network/earth/x/personhood/testutil"
	"github.com/earth-network/earth/x/personhood/types"
	pkikeeper "github.com/earth-network/earth/x/pki/keeper"
	pkitypes "github.com/earth-network/earth/x/pki/types"
	shieldedkeeper "github.com/earth-network/earth/x/shielded/keeper"
	shieldedtypes "github.com/earth-network/earth/x/shielded/types"
	"github.com/earth-network/earth/zk/merkle"
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

// gasTime is the gas-membership test's block time: 2026-10-15, so month
// 202610.
var gasTime = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)

const gasChainID = "earth-1"

// TestCheckGasMembershipAgreesWithTheCircuit drives CheckGasMembership (what
// `earthd gas-check membership` runs for the transparent gas grant) with a
// real membership proof and the real verifier: a proof for this month,
// bound to the address, against a live identity root, passes; the same proof
// fails for another month, another address, another chain, an unknown or
// lapsed root, and a max_activation in the future.
//
// The proof is committed under testdata/gas; EARTH_PROVE_CIRCUITS (see
// scripts/personhood-fixtures.sh gas) remakes it.
func TestCheckGasMembershipAgreesWithTheCircuit(t *testing.T) {
	encCfg := moduletestutil.MakeTestEncodingConfig()
	ac := addresscodec.NewBech32Codec(sdk.GetConfig().GetBech32AccountAddrPrefix())
	shStore := storetypes.NewKVStoreKey(shieldedtypes.StoreKey)
	phStore := storetypes.NewKVStoreKey(types.StoreKey)
	ctx := testutil.DefaultContextWithKeys(map[string]*storetypes.KVStoreKey{
		shieldedtypes.StoreKey: shStore, types.StoreKey: phStore,
	}, nil, nil).WithBlockTime(gasTime).WithChainID(gasChainID)

	prover := &personhoodtest.Prover{Dir: filepath.Join("..", "testdata", "gas")}
	vk, err := prover.VK(shieldedtypes.CircuitMembership)
	require.NoError(t, err)
	sh := shieldedkeeper.NewKeeper(runtime.NewKVStoreService(shStore), encCfg.Codec, ac,
		authtypes.NewModuleAddress(types.GovModuleName), nil, nil, nil, nil)
	shParams := shieldedtypes.DefaultParams()
	shParams.VerifyingKeys = map[string][]byte{shieldedtypes.CircuitMembership: vk}
	require.NoError(t, sh.Params.Set(ctx, shParams))
	k := NewKeeper(runtime.NewKVStoreService(phStore), encCfg.Codec, ac, authtypes.NewModuleAddress(types.GovModuleName),
		nil, stubDex{}, nil, stubAllocation{}, &burnLog{}, sh)
	require.NoError(t, k.Params.Set(ctx, types.DefaultParams()))

	// An identity tree of five leaves; ours is index 2, activated a day ago.
	now := uint64(gasTime.Unix())
	idSecret := personhoodtest.Det("gas/id", 0)
	activatedAt := now - 86_400
	tree := merkle.NewMem()
	for i := uint64(0); i < 5; i++ {
		leaf := privacy.IdentityLeaf(privacy.IDC(personhoodtest.Det("gas/other", i)), personhoodtest.Det("gas/dsc", i),
			privacy.CountryField("DE"), now-10*86_400+i)
		if i == 2 {
			leaf = privacy.IdentityLeaf(privacy.IDC(idSecret), personhoodtest.Det("gas/dsc", 2), privacy.CountryField("FR"), activatedAt)
		}
		_, err := tree.Append(leaf)
		require.NoError(t, err)
	}
	root, err := tree.Root()
	require.NoError(t, err)
	sib, err := tree.Path(2)
	require.NoError(t, err)

	addr := personhoodtest.ReferralAddress("gas")
	const month = 202610
	maxAct := now - 3_600
	w := personhoodtest.Membership{
		IDSecret: idSecret, DscKey: personhoodtest.Det("gas/dsc", 2), Country: privacy.CountryField("FR"),
		ActivatedAt: activatedAt, LeafIndex: 2, Root: root, Siblings: sib,
		Scope: privacy.GasScope(month), Signal: privacy.GasTransparentSignal(gasChainID, addr),
		MaxActivation: maxAct,
	}
	toml, pub := w.Witness()
	proof, err := prover.Proof("gas_transparent", shieldedtypes.CircuitMembership, toml, pub)
	require.NoError(t, err)
	nf := w.Nullifier()
	m := types.Membership{Proof: proof, Root: privacy.FieldBytes(root), Nullifier: privacy.FieldBytes(nf)}

	// The root is an anchor: recorded ten minutes ago and still the latest.
	rootBz := privacy.FieldBytes(root)
	require.NoError(t, k.IdentityRoots.Set(ctx, rootBz, types.IdentityRoot{Root: rootBz, Height: 10,
		Time: gasTime.Unix() - 600, TreeSize: 5}))
	require.NoError(t, k.LatestIdentityRoot.Set(ctx, rootBz))

	require.NoError(t, k.CheckGasMembership(ctx, m, month, addr, maxAct))

	// The same proof, read as anything else, fails the verifier.
	require.ErrorIs(t, k.CheckGasMembership(ctx, m, 202611, addr, maxAct), types.ErrInvalidMembership)
	require.ErrorIs(t, k.CheckGasMembership(ctx, m, month, personhoodtest.ReferralAddress("other"), maxAct), types.ErrInvalidMembership)
	require.ErrorIs(t, k.CheckGasMembership(ctx, m, month, addr, maxAct-1), types.ErrInvalidMembership)
	require.ErrorIs(t, k.CheckGasMembership(ctx.WithChainID("earth-2"), m, month, addr, maxAct), types.ErrInvalidMembership)
	forged := m
	forged.Nullifier = privacy.FieldBytes(privacy.ScopeNullifier(idSecret, privacy.GasScope(202611)))
	require.ErrorIs(t, k.CheckGasMembership(ctx, forged, month, addr, maxAct), types.ErrInvalidMembership)

	// Shape and statement checks before the verifier.
	require.ErrorIs(t, k.CheckGasMembership(ctx, m, 202613, addr, maxAct), types.ErrInvalidMsg)
	require.ErrorIs(t, k.CheckGasMembership(ctx, m, month, nil, maxAct), types.ErrInvalidMsg)
	require.ErrorIs(t, k.CheckGasMembership(ctx, m, month, addr, now+1), types.ErrInvalidMsg)

	// Superseded within the window: still an anchor. Past it: refused.
	require.NoError(t, k.LatestIdentityRoot.Set(ctx, privacy.FieldBytes(fr.NewElement(1))))
	require.NoError(t, k.CheckGasMembership(ctx, m, month, addr, maxAct))
	params, err := k.Params.Get(ctx)
	require.NoError(t, err)
	late := ctx.WithBlockTime(gasTime.Add(time.Duration(params.IdentityRootWindowSecondsOrDefault()) * time.Second))
	require.ErrorIs(t, k.CheckGasMembership(late, m, month, addr, maxAct), types.ErrUnknownIdentityRoot)

	// A root the chain never recorded.
	require.NoError(t, k.IdentityRoots.Remove(ctx, rootBz))
	require.ErrorIs(t, k.CheckGasMembership(ctx, m, month, addr, maxAct), types.ErrUnknownIdentityRoot)
}

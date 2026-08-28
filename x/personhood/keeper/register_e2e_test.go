package keeper

import (
	"math/big"
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

	"github.com/earth-network/earth/x/personhood/types"
	pkikeeper "github.com/earth-network/earth/x/pki/keeper"
	pkitypes "github.com/earth-network/earth/x/pki/types"
)

// TestRegisterEndToEnd_RealPki drives the whole redesigned trust path with no
// stubs: a real x/pki keeper holding the fixture's CSCA, the real Document
// Signer certificate, and a real UltraHonk proof from the current circuit.
//
// It is the test that would have caught the old design's blind spot — it proves
// the chain can independently identify *which* Document Signer produced a
// registration, and reject one it no longer trusts.
func TestRegisterEndToEnd_RealPki(t *testing.T) {
	vk := readFileAt(t, filepath.Join(leanDir, "vk"))
	proof := readFileAt(t, filepath.Join(leanDir, "proof"))
	pub := readFileAt(t, filepath.Join(leanDir, "public_inputs"))
	cscaDER := readFileAt(t, filepath.Join(leanDir, "csca.der"))
	dscDER := readFileAt(t, filepath.Join(leanDir, "dsc.der"))

	var signals []string
	for i := 0; i+32 <= len(pub); i += 32 {
		signals = append(signals, new(big.Int).SetBytes(pub[i:i+32]).String())
	}

	encCfg := moduletestutil.MakeTestEncodingConfig()
	ac := addresscodec.NewBech32Codec(sdk.GetConfig().GetBech32AccountAddrPrefix())

	// Both modules share one multistore so x/personhood can call into x/pki
	// within a single context, the way the app wires them.
	pkiStore := storetypes.NewKVStoreKey(pkitypes.StoreKey)
	personhoodStore := storetypes.NewKVStoreKey(types.StoreKey)
	ctx := testutil.DefaultContextWithKeys(
		map[string]*storetypes.KVStoreKey{
			pkitypes.StoreKey: pkiStore,
			types.StoreKey:    personhoodStore,
		}, nil, nil,
	).WithBlockTime(time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC))

	// Real x/pki keeper, seeded with the CSCA that issued the fixture's DSC.
	pki := pkikeeper.NewKeeper(runtime.NewKVStoreService(pkiStore), encCfg.Codec, ac,
		authtypes.NewModuleAddress(pkitypes.GovModuleName))
	if err := pki.InitGenesis(ctx, pkitypes.GenesisState{
		Params: pkitypes.NewParams(),
		Cscas:  []pkitypes.Csca{{CertificateDer: cscaDER}},
	}); err != nil {
		t.Fatalf("seed pki: %v", err)
	}

	k := NewKeeper(runtime.NewKVStoreService(personhoodStore), encCfg.Codec, ac,
		authtypes.NewModuleAddress(types.GovModuleName), nil, stubDex{}, pki, stubAllocation{}, &burnLog{})

	params := types.Params{
		VerifyingKeys:             map[string][]byte{"lean_poa": vk},
		NullifierIndex:            2,
		DscKeyIndex:               3,
		CurrentDateIndex:          0,
		AddressIndex:              1,
		CurrentDateMaxSkewSeconds: 2 * 24 * 60 * 60,
	}
	if err := k.Params.Set(ctx, params); err != nil {
		t.Fatalf("set params: %v", err)
	}

	// The genuine DSC is accepted and the nullifier comes back.
	nullifier, _, err := k.verifyRegistrationProof(ctx, fixtureAddr(t), proof, signals, "lean_poa", dscDER)
	if err != nil {
		t.Fatalf("registration with a genuine CSCA-issued DSC rejected: %v", err)
	}
	if len(nullifier) != 32 {
		t.Fatalf("nullifier length = %d, want 32", len(nullifier))
	}

	// The verification also reports which Document Signer produced the proof and
	// which country issued it — the facts the per-DSC query and the explorer's
	// country map are built from.
	_, facts, err := k.verifyRegistrationProof(ctx, fixtureAddr(t), proof, signals, "lean_poa", dscDER)
	if err != nil {
		t.Fatalf("re-verify: %v", err)
	}
	if len(facts.key) != 32 {
		t.Fatalf("dsc key length = %d, want 32", len(facts.key))
	}
	if facts.country != "UT" {
		t.Fatalf("country = %q, want UT (the fixture CSCA's country)", facts.country)
	}

	// Revoking that Document Signer makes the very same proof unusable — the
	// capability the Merkle-membership design could not offer.
	pubkey, err := pki.VerifyDsc(ctx, dscDER)
	if err != nil {
		t.Fatalf("VerifyDsc: %v", err)
	}
	if err := pki.RevokeDsc(ctx, pubkey); err != nil {
		t.Fatalf("RevokeDsc: %v", err)
	}
	if _, _, err := k.verifyRegistrationProof(ctx, fixtureAddr(t), proof, signals, "lean_poa", dscDER); err == nil {
		t.Fatal("expected rejection after the DSC was revoked")
	}
}

package keeper

import (
	"errors"
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

// TestCheckRegistrationAgreesWithTheChain drives CheckRegistration and
// CheckHuman — what the gas-grant backend asks, through `earthd gas-check` —
// over the same real proof, DSC and CSCA as TestRegisterEndToEnd_RealPki.
func TestCheckRegistrationAgreesWithTheChain(t *testing.T) {
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
	pkiStore := storetypes.NewKVStoreKey(pkitypes.StoreKey)
	personhoodStore := storetypes.NewKVStoreKey(types.StoreKey)
	ctx := testutil.DefaultContextWithKeys(
		map[string]*storetypes.KVStoreKey{pkitypes.StoreKey: pkiStore, types.StoreKey: personhoodStore}, nil, nil,
	).WithBlockTime(time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC))

	pki := pkikeeper.NewKeeper(runtime.NewKVStoreService(pkiStore), encCfg.Codec, ac,
		authtypes.NewModuleAddress(pkitypes.GovModuleName))
	if err := pki.InitGenesis(ctx, pkitypes.GenesisState{
		Params: pkitypes.NewParams(),
		Cscas:  []pkitypes.Csca{{CertificateDer: cscaDER}},
	}); err != nil {
		t.Fatalf("seed pki: %v", err)
	}
	// No bank keeper: nothing on the check path may reach one.
	k := NewKeeper(runtime.NewKVStoreService(personhoodStore), encCfg.Codec, ac,
		authtypes.NewModuleAddress(types.GovModuleName), nil, stubDex{}, pki, stubAllocation{}, &burnLog{})
	if err := k.Params.Set(ctx, types.Params{
		VerifyingKeys:               map[string][]byte{"lean_poa": vk},
		NullifierIndex:              2,
		DscKeyIndex:                 3,
		CurrentDateIndex:            0,
		AddressIndex:                1,
		CurrentDateMaxSkewSeconds:   2 * 24 * 60 * 60,
		RegistrationValiditySeconds: 365 * 24 * 60 * 60,
	}); err != nil {
		t.Fatalf("set params: %v", err)
	}

	creator := fixtureAddr(t)
	creatorStr, _ := ac.BytesToString(creator)
	msg := &types.MsgRegister{
		Creator: creatorStr, Proof: proof, PublicSignals: signals,
		SignatureAlgorithm: "lean_poa", DscDer: dscDER,
	}

	// A fresh registration the chain would take: nullifier back, not a switch.
	nullifier, switched, err := k.CheckRegistration(ctx, msg)
	if err != nil {
		t.Fatalf("CheckRegistration refused a genuine registration: %v", err)
	}
	if len(nullifier) != 32 || switched {
		t.Fatalf("nullifier len %d switched %v, want 32 and false", len(nullifier), switched)
	}
	// It wrote nothing.
	if _, err := k.CheckHuman(ctx, creator); !errors.Is(err, types.ErrNotRegistered) {
		t.Fatalf("CheckHuman before registering = %v, want ErrNotRegistered", err)
	}

	// Another address cannot use the proof.
	other := sdk.AccAddress(make([]byte, 20))
	otherStr, _ := ac.BytesToString(other)
	lifted := *msg
	lifted.Creator = otherStr
	if _, _, err := k.CheckRegistration(ctx, &lifted); err == nil {
		t.Fatal("CheckRegistration accepted a proof lifted onto another address")
	}

	// Once registered, the wallet counts as a human and cannot register again.
	now := ctx.BlockTime().Unix()
	reg := types.Registration{Nullifier: nullifier, Address: creatorStr, RegisteredAt: now}
	if err := k.Registrations.Set(ctx, nullifier, reg); err != nil {
		t.Fatal(err)
	}
	if err := k.RegByAddr.Set(ctx, creator, nullifier); err != nil {
		t.Fatal(err)
	}
	if got, err := k.CheckHuman(ctx, creator); err != nil || string(got.Nullifier) != string(nullifier) {
		t.Fatalf("CheckHuman after registering = %v, %v", got, err)
	}
	if _, _, err := k.CheckRegistration(ctx, msg); !errors.Is(err, types.ErrAlreadyReg) {
		t.Fatalf("CheckRegistration for a registered wallet = %v, want ErrAlreadyReg", err)
	}

	// And after it expires it is no longer a human.
	later := ctx.WithBlockTime(ctx.BlockTime().Add(366 * 24 * time.Hour))
	if _, err := k.CheckHuman(later, creator); !errors.Is(err, types.ErrRegExpired) {
		t.Fatalf("CheckHuman after expiry = %v, want ErrRegExpired", err)
	}
}

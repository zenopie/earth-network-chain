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
)

// gasFixture builds a keeper holding the real lean_poa verifying key, plus the
// proof and signals that verify against it, on a context with a metered gas
// meter. Everything here is about what the verifier costs, so the fixture has to
// be the real one — a stub verifier would measure nothing.
func gasFixture(t *testing.T) (Keeper, sdk.Context, []byte, []string) {
	t.Helper()
	vk := readFileAt(t, filepath.Join(leanDir, "vk"))
	proof := readFileAt(t, filepath.Join(leanDir, "proof"))
	pub := readFileAt(t, filepath.Join(leanDir, "public_inputs"))
	var signals []string
	for i := 0; i+32 <= len(pub); i += 32 {
		signals = append(signals, new(big.Int).SetBytes(pub[i:i+32]).String())
	}

	encCfg := moduletestutil.MakeTestEncodingConfig()
	storeKey := storetypes.NewKVStoreKey(types.StoreKey)
	base := testutil.DefaultContextWithDB(t, storeKey, storetypes.NewTransientStoreKey("transient_test")).Ctx
	k := NewKeeper(
		runtime.NewKVStoreService(storeKey),
		encCfg.Codec,
		addresscodec.NewBech32Codec(sdk.GetConfig().GetBech32AccountAddrPrefix()),
		authtypes.NewModuleAddress(types.GovModuleName),
		nil, stubDex{}, nil, stubAllocation{}, &burnLog{},
	)
	params := types.DefaultParams()
	params.VerifyingKeys = map[string][]byte{"lean_poa": vk}
	params.NullifierIndex = 2
	params.DscKeyIndex = 3
	params.CurrentDateIndex = 0
	params.AddressIndex = 1

	ctx := base.
		WithBlockTime(time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)).
		WithGasMeter(storetypes.NewGasMeter(50_000_000))
	if err := k.Params.Set(ctx, params); err != nil {
		t.Fatal(err)
	}
	return k, ctx, proof, signals
}

// TestProofVerificationIsMetered is the finding this closes. Verifying an
// UltraHonk proof is the most expensive thing this chain does, and until it was
// charged for, the block gas limit did not bound it: a block could sit far under
// its limit while costing every validator seconds of proof CPU.
func TestProofVerificationIsMetered(t *testing.T) {
	k, ctx, proof, signals := gasFixture(t)

	before := ctx.GasMeter().GasConsumed()
	if _, _, err := k.verifyRegistrationProof(ctx, fixtureAddr(t), proof, signals, "lean_poa", nil); err != nil {
		t.Fatalf("valid proof rejected: %v", err)
	}
	charged := ctx.GasMeter().GasConsumed() - before

	if charged < types.DefaultProofVerificationGas {
		t.Fatalf("verification charged %d gas, want at least %d — the verifier is not metered",
			charged, types.DefaultProofVerificationGas)
	}
}

// TestProofVerificationRespectsTheGasLimit: the charge has to be able to stop
// the work, not merely account for it after the fact. A transaction that cannot
// afford the verifier must run out of gas instead of running it.
func TestProofVerificationRespectsTheGasLimit(t *testing.T) {
	k, ctx, proof, signals := gasFixture(t)
	ctx = ctx.WithGasMeter(storetypes.NewGasMeter(types.DefaultProofVerificationGas / 2))

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("verification ran on a gas meter that could not pay for it")
		}
		// An out-of-gas panic is how the SDK unwinds this; the ante handler
		// turns it into a failed transaction.
	}()
	_, _, _ = k.verifyRegistrationProof(ctx, fixtureAddr(t), proof, signals, "lean_poa", nil)
}

// TestLiftedProofIsRejectedBeforeTheVerifier is the anti-replay guard, on its
// new footing.
//
// It used to work by refusing any nullifier that was already live: replaying a
// captured proof was the cheapest way to make a validator run the verifier, and
// such a proof was going to be refused anyway. That refusal is gone, because a
// live nullifier is now a wallet switch -- the only way back for somebody who
// lost the wallet they registered from.
//
// What replaced it is stronger. The circuit takes the registrant's address as a
// public input, so a proof lifted out of somebody else's transaction is refused
// on one integer comparison, still without reaching the verifier. It is also a
// filter the old one could not be: the attacker cannot sign as the address the
// proof is bound to, so the transaction does not get past the ante handler
// either.
func TestLiftedProofIsRejectedBeforeTheVerifier(t *testing.T) {
	k, ctx, proof, signals := gasFixture(t)

	// Submitted by the account it was proved for: verifies, full price.
	before := ctx.GasMeter().GasConsumed()
	if _, _, err := k.verifyRegistrationProof(ctx, fixtureAddr(t), proof, signals, "lean_poa", nil); err != nil {
		t.Fatalf("valid proof rejected: %v", err)
	}
	full := ctx.GasMeter().GasConsumed() - before
	if full < types.DefaultProofVerificationGas {
		t.Fatalf("a real verification cost %d gas, expected at least %d", full, types.DefaultProofVerificationGas)
	}

	// The same bytes, presented by somebody else.
	attacker := sdk.AccAddress([]byte("mallory-other-wallet"))
	before = ctx.GasMeter().GasConsumed()
	if _, _, err := k.verifyRegistrationProof(ctx, attacker, proof, signals, "lean_poa", nil); err == nil {
		t.Fatal("a proof bound to one address was accepted for another -- registration is stealable")
	}
	charged := ctx.GasMeter().GasConsumed() - before

	if charged >= types.DefaultProofVerificationGas {
		t.Fatalf("lifted proof cost %d gas: it is still paying to verify a proof it did not need to verify", charged)
	}
	t.Logf("rejected for %d gas, against %d for a real verification", charged, full)
}

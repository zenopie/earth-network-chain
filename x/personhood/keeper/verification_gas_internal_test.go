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
		nil, stubDex{}, nil, stubAllocation{},
	)
	params := types.DefaultParams()
	params.VerifyingKeys = map[string][]byte{"lean_poa": vk}
	params.NullifierIndex = 1
	params.DscKeyIndex = 2
	params.CurrentDateIndex = 0

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
	if _, _, err := k.verifyRegistrationProof(ctx, proof, signals, "lean_poa", nil); err != nil {
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
	_, _, _ = k.verifyRegistrationProof(ctx, proof, signals, "lean_poa", nil)
}

// TestReplayIsRejectedBeforeTheVerifier is the reorder, and it is the half of
// the fix that metering alone does not give you.
//
// Replaying a captured proof is the cheapest way to make a validator run the
// verifier: the attacker needs no passport and no proving, just a copy of
// somebody else's valid transaction. Charging for the verification makes that
// expensive for them, but the work still happens. Checking the nullifier first
// means a replay is thrown out for the price of one keyed read.
func TestReplayIsRejectedBeforeTheVerifier(t *testing.T) {
	k, ctx, proof, signals := gasFixture(t)

	// First submission verifies and is charged the full price.
	nullifier, _, err := k.verifyRegistrationProof(ctx, proof, signals, "lean_poa", nil)
	if err != nil {
		t.Fatalf("valid proof rejected: %v", err)
	}

	// Record it as a live registration, which is what Register would do.
	if err := k.Registrations.Set(ctx, nullifier, types.Registration{
		Nullifier:    nullifier,
		Address:      "earth1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq",
		RegisteredAt: ctx.BlockTime().Unix(),
	}); err != nil {
		t.Fatal(err)
	}

	// Replay the very same proof.
	before := ctx.GasMeter().GasConsumed()
	if _, _, err := k.verifyRegistrationProof(ctx, proof, signals, "lean_poa", nil); err == nil {
		t.Fatal("replayed proof accepted")
	}
	charged := ctx.GasMeter().GasConsumed() - before

	if charged >= types.DefaultProofVerificationGas {
		t.Fatalf("replay cost %d gas: it is still paying to verify a proof it did not need to verify", charged)
	}
}

// TestStaleDateIsRejectedBeforeTheVerifier: the same argument for the other
// public-input check that can reject without the proof. A backdated
// current_date is refused on arithmetic alone.
func TestStaleDateIsRejectedBeforeTheVerifier(t *testing.T) {
	k, ctx, proof, signals := gasFixture(t)
	ctx = ctx.WithBlockTime(time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)) // years past current_date

	before := ctx.GasMeter().GasConsumed()
	if _, _, err := k.verifyRegistrationProof(ctx, proof, signals, "lean_poa", nil); err == nil {
		t.Fatal("proof with a stale current_date accepted")
	}
	charged := ctx.GasMeter().GasConsumed() - before

	if charged >= types.DefaultProofVerificationGas {
		t.Fatalf("stale-date rejection cost %d gas: it verified the proof first", charged)
	}
}

// TestBlockGasLimitBoundsProofsPerBlock states the property the charge exists
// for, in the terms that actually matter: how many verifications a full block
// can be made to contain, and therefore how long one can take to execute.
func TestBlockGasLimitBoundsProofsPerBlock(t *testing.T) {
	const blockMaxGas = 100_000_000 // networks/genesis/chain.json

	perBlock := blockMaxGas / types.DefaultProofVerificationGas
	if perBlock > 128 {
		t.Fatalf("a full block admits %d proof verifications; at ~6ms each that is %.1fs of CPU "+
			"in one block, which is a liveness problem rather than a fee problem",
			perBlock, float64(perBlock)*0.006)
	}
}

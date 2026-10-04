package ultrahonk

import (
	crand "crypto/rand"
	"math/big"
	"math/rand"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	bb "github.com/burnt-labs/barretenberg-go/barretenberg"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"

	"github.com/earth-network/earth/zk/orchard"
)

// TestVerifyOracle drives the on-chain UltraHonk verifier against a real bb
// v5.0.0 poseidon2 proof (generated with nargo + bb 5.0.0). It confirms the
// verifier is wired correctly and sound: it accepts the valid proof and rejects
// a tampered public input.
func TestVerifyOracle(t *testing.T) {
	dir := "testdata"
	vk := readOrSkip(t, filepath.Join(dir, "vk"))
	proof := readOrSkip(t, filepath.Join(dir, "proof"))
	pub := readOrSkip(t, filepath.Join(dir, "public_inputs"))

	ok, err := VerifyRaw(vk, proof, pub)
	if err != nil {
		t.Fatalf("VerifyRaw: %v", err)
	}
	if !ok {
		t.Fatal("valid proof reported INVALID")
	}
	t.Log("valid bb v5.0.0 UltraHonk proof verified on-chain")

	// Soundness: flip a byte of the first public input -> must fail.
	bad := make([]byte, len(pub))
	copy(bad, pub)
	bad[FieldSize-1] ^= 0x01
	ok, err = VerifyRaw(vk, proof, bad)
	if err != nil {
		t.Fatalf("VerifyRaw (tampered): %v", err)
	}
	if ok {
		t.Fatal("tampered public input reported VALID — verifier is unsound")
	}
	t.Log("tampered public input correctly rejected")
}

func readOrSkip(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("fixture %s missing (%v)", path, err)
	}
	return b
}

// Audit 4 (core PoC, ported): mutate every 32-byte element of a valid action proof to canonical
// junk (0, 1, 2, r-1, random < r) and check bb returns instead of aborting.
func TestProofElementJunkNoCrash(t *testing.T) {
	b, sighash, vk := loadBundle(t, 1)
	good := b.Actions[0].Proof
	in := b.PublicInputs(0, sighash)
	rng := rand.New(rand.NewSource(1))
	rm1 := new(big.Int).Sub(fr.Modulus(), big.NewInt(1))
	vals := func() []*big.Int {
		r := new(big.Int).Rand(rng, fr.Modulus())
		return []*big.Int{big.NewInt(0), big.NewInt(1), big.NewInt(2), rm1, r}
	}
	errs, falses := 0, 0
	for off := 0; off+FieldSize <= len(good); off += FieldSize {
		for _, v := range vals() {
			p := append([]byte(nil), good...)
			v.FillBytes(p[off : off+FieldSize])
			ok, err := Verify(vk, p, in)
			if ok && err == nil && v.Cmp(new(big.Int).SetBytes(good[off:off+FieldSize])) != 0 {
				t.Errorf("element %d value %s verified", off/FieldSize, v)
			}
			if err != nil {
				errs++
			} else if !ok {
				falses++
			}
		}
	}
	// whole-proof junk
	for i := 0; i < 20; i++ {
		p := make([]byte, len(good))
		for off := 0; off < len(p); off += FieldSize {
			new(big.Int).Rand(rng, fr.Modulus()).FillBytes(p[off : off+FieldSize])
		}
		_, _ = Verify(vk, p, in)
	}
	t.Logf("errs=%d false=%d", errs, falses)
}

// Audit (shielded M1): bb read the proof elements it needed and ignored the
// rest, so a proof with up to 31 bytes appended verified: one tx, two
// encodings, two hashes. Every length but ProofSize is now refused before bb
// sees it.
func TestProofLengthIsExact(t *testing.T) {
	b, sighash, vk := loadBundle(t, 1)
	good := b.Actions[0].Proof
	in := b.PublicInputs(0, sighash)
	if len(good) != ProofSize {
		t.Fatalf("fixture proof is %d bytes, ProofSize %d", len(good), ProofSize)
	}
	if ok, err := Verify(vk, good, in); !ok || err != nil {
		t.Fatalf("good proof: ok=%v err=%v", ok, err)
	}
	for _, n := range []int{0, 512, len(good) / 2, len(good) - 32, len(good) - 1, len(good) + 1, len(good) + 31, len(good) + 32, 2 * len(good)} {
		p := make([]byte, n)
		copy(p, good)
		if n > len(good) {
			_, _ = crand.Read(p[len(good):])
		}
		if ok, err := Verify(vk, p, in); ok || err == nil {
			t.Fatalf("len %d: ok=%v err=%v, want refused", n, ok, err)
		}
	}
}

// Audit (shielded M2): a bundle whose first proof fails had every proof
// verified. In CheckTx the chain now verifies one at a time and stops at the
// first failure; in a block it still verifies them all in parallel (same
// result, the first failure in order).
func TestSequentialVerificationStopsAtFirstFailure(t *testing.T) {
	b, _, vk := loadBundle(t, 10)
	var wrong fr.Element
	wrong.SetUint64(42) // every proof fails, only after full verification
	var calls int64
	count := func(p []byte, in [][]byte) (bool, error) {
		atomic.AddInt64(&calls, 1)
		return Verify(vk, p, in)
	}
	err := orchard.VerifyProofsSequential([]*orchard.Bundle{b}, wrong, count)
	if ae, ok := err.(*orchard.ActionError); !ok || ae.Bundle != 0 || ae.Action != 0 {
		t.Fatalf("sequential: %v", err)
	}
	if calls != 1 {
		t.Fatalf("sequential verified %d proofs, want 1", calls)
	}
	calls = 0
	err = orchard.VerifyProofs([]*orchard.Bundle{b}, wrong, count)
	if ae, ok := err.(*orchard.ActionError); !ok || ae.Bundle != 0 || ae.Action != 0 {
		t.Fatalf("parallel: %v", err)
	}
	if calls != int64(len(b.Actions)) {
		t.Fatalf("parallel verified %d proofs, want %d", calls, len(b.Actions))
	}
}

// Audit (info): bb printed a line to stderr for every failed verification.
// The package lowers bb's log level below info at start-up.
func TestBarretenbergLogLevelLowered(t *testing.T) {
	if os.Getenv("BB_VERBOSE") != "" {
		t.Skip("BB_VERBOSE keeps bb's level")
	}
	if lvl := bb.LogLevel(); lvl != bb.LogLevelWarn {
		t.Fatalf("bb log level %d, want %d", lvl, bb.LogLevelWarn)
	}
}

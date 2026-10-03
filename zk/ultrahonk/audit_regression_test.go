package ultrahonk

import (
	"crypto/rand"
	"os"
	"sync/atomic"
	"testing"

	bb "github.com/burnt-labs/barretenberg-go/barretenberg"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"

	"github.com/earth-network/earth/zk/orchard"
)

// Audit (shielded M1): bb read the proof elements it needed and ignored the
// rest, so a proof with up to 31 bytes appended verified: one tx, two
// encodings, two hashes. Every length but ProofSize is now refused before bb
// sees it.
func TestAuditProofLengthIsExact(t *testing.T) {
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
			_, _ = rand.Read(p[len(good):])
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
func TestAuditSequentialVerificationStopsAtFirstFailure(t *testing.T) {
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

package ultrahonk

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/consensys/gnark-crypto/ecc/bn254/fr"

	"github.com/earth-network/earth/zk/orchard"
	"github.com/earth-network/earth/zk/privacy"
)

// Orchard bundles written by scripts/orchard-bundles.sh: every action proven
// by bb v5.0.0, the binding signature made in Go.
var orchardSizes = []int{1, 2, 3, 10}

type orchardFixture struct {
	MsgType string `json:"msg_type"`
	ChainID string `json:"chain_id"`
	Actions []struct {
		Anchor, Nf, Cm, Cv, Ct string
	} `json:"actions"`
	Balances []struct {
		Asset string `json:"asset"`
		Value uint64 `json:"value"`
	} `json:"balances"`
	BindingSig string `json:"binding_sig"`
	Sighash    string `json:"sighash"`
	Tx         struct {
		Memo          string `json:"memo"`
		TimeoutHeight uint64 `json:"timeout_height"`
		GasLimit      uint64 `json:"gas_limit"`
	} `json:"tx"`
}

func fhex(t testing.TB, s string) fr.Element {
	t.Helper()
	b, err := hex.DecodeString(strings.TrimPrefix(s, "0x"))
	if err != nil {
		t.Fatal(err)
	}
	e, err := privacy.FieldFromBytes(b)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func bhex(t testing.TB, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// loadBundle rebuilds the bundle the chain would receive and recomputes its
// sighash (which must equal the one the proofs bind).
func loadBundle(t testing.TB, n int) (*orchard.Bundle, fr.Element, []byte) {
	t.Helper()
	dir := filepath.Join("testdata", "orchard", fmt.Sprintf("bundle_%d", n))
	raw, err := os.ReadFile(filepath.Join(dir, "bundle.json"))
	if err != nil {
		t.Skipf("fixture missing (%v); run scripts/orchard-bundles.sh", err)
	}
	var f orchardFixture
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	vk, err := os.ReadFile(filepath.Join(dir, "vk"))
	if err != nil {
		t.Fatal(err)
	}
	b := &orchard.Bundle{BindingSig: bhex(t, f.BindingSig)}
	for i, a := range f.Actions {
		cv, err := orchard.PointFromBytes(bhex(t, a.Cv))
		if err != nil {
			t.Fatal(err)
		}
		proof, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf("action_%d", i), "proof"))
		if err != nil {
			t.Fatal(err)
		}
		b.Actions = append(b.Actions, orchard.Action{
			Anchor: fhex(t, a.Anchor), Nullifier: fhex(t, a.Nf), Commitment: fhex(t, a.Cm), Cv: cv,
			Ciphertext: bhex(t, a.Ct), Proof: proof,
		})
	}
	for _, x := range f.Balances {
		b.Balances = append(b.Balances, orchard.Balance{Asset: fhex(t, x.Asset), Value: x.Value})
	}
	sighash := orchard.Sighash(f.MsgType, f.ChainID, orchard.TxFields(f.Tx), []*orchard.Bundle{b})
	if sighash != fhex(t, f.Sighash) {
		t.Fatal("recomputed sighash differs from the fixture's")
	}
	return b, sighash, vk
}

func verifier(vk []byte) orchard.ProofVerifier {
	return func(proof []byte, in [][]byte) (bool, error) { return Verify(vk, proof, in) }
}

func TestOrchardBundles(t *testing.T) {
	for _, n := range orchardSizes {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			b, sighash, vk := loadBundle(t, n)
			if err := b.Verify(sighash, orchard.CanonicalBase, verifier(vk)); err != nil {
				t.Fatalf("valid bundle: %v", err)
			}

			// The fee claimed one higher: the binding signature fails.
			c := *b
			c.Balances = append([]orchard.Balance(nil), b.Balances...)
			c.Balances[0].Value++
			if err := c.Verify(sighash, orchard.CanonicalBase, verifier(vk)); err == nil {
				t.Fatal("inflated balance accepted")
			}
			// A proof moved to another tx (a different sighash) fails, even
			// if its binding signature were somehow re-made.
			other := orchard.Sighash("/earth.orchard.fixture", "earth-2", orchard.TxFields{}, []*orchard.Bundle{b})
			ok, err := Verify(vk, b.Actions[0].Proof, b.PublicInputs(0, other))
			if err != nil || ok {
				t.Fatalf("proof under another sighash: ok=%v err=%v", ok, err)
			}
			// Every public input is bound: flip each one of action 0's.
			for k, in := range b.PublicInputs(0, sighash) {
				pub := b.PublicInputs(0, sighash)
				bad := append([]byte(nil), in...)
				bad[31] ^= 1
				pub[k] = bad
				if ok, _ := Verify(vk, b.Actions[0].Proof, pub); ok {
					t.Fatalf("public input %d tampered but proof valid", k)
				}
			}
			// cv swapped between two actions: proofs bind their own cv.
			if n > 1 {
				d := *b
				d.Actions = append([]orchard.Action(nil), b.Actions...)
				d.Actions[0].Cv, d.Actions[1].Cv = d.Actions[1].Cv, d.Actions[0].Cv
				ok, _ := Verify(vk, d.Actions[0].Proof, d.PublicInputs(0, sighash))
				if ok {
					t.Fatal("proof accepted another action's cv")
				}
			}
		})
	}
}

// BenchmarkOrchardAction is one action proof's verification, the per-action
// CheckTx cost.
func BenchmarkOrchardAction(b *testing.B) {
	bun, sighash, vk := loadBundle(b, 1)
	in := bun.PublicInputs(0, sighash)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if ok, err := Verify(vk, bun.Actions[0].Proof, in); err != nil || !ok {
			b.Fatal(ok, err)
		}
	}
}

// BenchmarkOrchardBinding is the balance check alone: bvk from the actions'
// cv and the public balance, then the Schnorr verification.
func BenchmarkOrchardBinding(b *testing.B) {
	for _, n := range orchardSizes {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			bun, sighash, _ := loadBundle(b, n)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := bun.CheckBalance(sighash, orchard.CanonicalBase); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkOrchardBundle is a whole bundle's stateless verification: shape,
// sighash, binding signature, every proof (one goroutine per core).
func BenchmarkOrchardBundle(b *testing.B) {
	for _, n := range orchardSizes {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			bun, _, vk := loadBundle(b, n)
			v := verifier(vk)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				sighash := orchard.Sighash("/earth.orchard.fixture", "earth-1",
					orchard.TxFields{Memo: "orchard fixture", TimeoutHeight: 1_000_000, GasLimit: 3_000_000}, []*orchard.Bundle{bun})
				if err := bun.Verify(sighash, orchard.CanonicalBase, v); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

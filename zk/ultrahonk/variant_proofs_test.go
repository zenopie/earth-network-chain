package ultrahonk

import (
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/earth-network/earth/x/pki/certs"
)

// registerVariants lists the passport register-circuit fixtures: one per
// variant (DSC key type × signature scheme × hash profile), written by
// scripts/regen-poa-fixtures.sh from the mobile repo's circuits/variants.json
// and its shared synthetic passports.
func registerVariants(t *testing.T) []string {
	t.Helper()
	dirs, err := filepath.Glob(filepath.Join("testdata", "lean_poa_*"))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, d := range dirs {
		out = append(out, filepath.Base(d))
	}
	// PASSPORT_COVERAGE.md: 33 variants. Fewer means a fixture went missing
	// and the tests below would pass vacuously for it.
	if len(out) < 33 {
		t.Fatalf("found %d register-circuit fixtures, want 33", len(out))
	}
	return out
}

func readFixture(t *testing.T, dir, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("read %s/%s: %v", dir, name, err)
	}
	return b
}

// TestRegisterVariantProofs verifies that the chain's UltraHonk verifier accepts
// every register-circuit variant's proof. Each is a distinct circuit and VK;
// x/personhood selects it by signature_algorithm, so accepting the proof here
// is what "the chain accepts them" means. Public inputs are [current_date,
// address, nullifier, dsc_key].
func TestRegisterVariantProofs(t *testing.T) {
	for _, variant := range registerVariants(t) {
		t.Run(variant, func(t *testing.T) {
			dir := filepath.Join("testdata", variant)
			vk := readFixture(t, dir, "vk")
			proof := readFixture(t, dir, "proof")
			pub := readFixture(t, dir, "public_inputs")
			var pubInputs [][]byte
			for i := 0; i+32 <= len(pub); i += 32 {
				pubInputs = append(pubInputs, pub[i:i+32])
			}
			ok, err := Verify(vk, proof, pubInputs)
			if err != nil {
				t.Fatalf("verify: %v", err)
			}
			if !ok {
				t.Fatal("variant proof did NOT verify on chain")
			}
			if len(pubInputs) != 4 {
				t.Fatalf("expected 4 public inputs, got %d", len(pubInputs))
			}
			if cd := new(big.Int).SetBytes(pubInputs[0]); cd.String() != "250101" {
				t.Fatalf("current_date public input = %s, want 250101", cd)
			}
			bound := readFixture(t, dir, "expected_address")
			if got, want := new(big.Int).SetBytes(pubInputs[1]), new(big.Int).SetBytes(bound); got.Cmp(want) != 0 {
				t.Fatalf("address public input = %s, want %s", got, want)
			}
			if got, want := new(big.Int).SetBytes(pubInputs[2]).String(), strings.TrimSpace(string(readFixture(t, dir, "expected_nullifier"))); got != want {
				t.Fatalf("nullifier = %s, want %s", got, want)
			}
			// A proof bound to another address must not verify.
			other := append([][]byte{}, pubInputs...)
			flipped := append([]byte{}, pubInputs[1]...)
			flipped[31] ^= 1
			other[1] = flipped
			if ok, _ := Verify(vk, proof, other); ok {
				t.Fatal("proof verified for another address")
			}
		})
	}
}

// TestDscCommitmentMatchesCircuit checks, for every register-circuit variant,
// that the commitment the chain computes from the Document Signer certificate
// is the value the circuit put in its proof.
//
// This is the hinge of the whole design. The circuit proves "the passport was
// signed by the key committed to here", and x/pki separately proves "that key
// belongs to a trusted signer". If the two sides hashed the key even slightly
// differently (a coordinate padded to the wrong length on one curve, the RSA
// exponent left out) no registration would ever be accepted, and only for that
// key type. The certificates are the fixture passports' own (Python-built DER,
// Brainpool with explicit parameters), so this also covers the parser.
func TestDscCommitmentMatchesCircuit(t *testing.T) {
	// The tag each circuit was compiled with, named here by the variant's key
	// rather than looked up through certs: this test exists to catch the two
	// sides disagreeing, and a wrong table agrees with itself.
	tags := map[string]certs.CurveTag{
		"p224": certs.TagP224, "p256": certs.TagP256, "p384": certs.TagP384, "p521": certs.TagP521,
		"bp224": certs.TagBrainpoolP224r1, "bp256": certs.TagBrainpoolP256r1,
		"bp384": certs.TagBrainpoolP384r1, "bp512": certs.TagBrainpoolP512r1,
		"rsa2048": certs.TagRSAExponent, "rsa3072": certs.TagRSAExponent, "rsa4096": certs.TagRSAExponent,
	}
	const dscKeyIndex = 3
	for _, variant := range registerVariants(t) {
		t.Run(variant, func(t *testing.T) {
			dir := filepath.Join("testdata", variant)
			key := strings.Split(strings.TrimPrefix(variant, "lean_poa_"), "_")[0]
			wantTag, ok := tags[key]
			if !ok {
				t.Fatalf("no tag for key %q", key)
			}
			cert, err := certs.ParseCert(readFixture(t, dir, "dsc.der"))
			if err != nil {
				t.Fatalf("parse DSC: %v", err)
			}
			if tag, err := cert.PublicKey.CurveTagOf(); err != nil || tag != wantTag {
				t.Fatalf("CurveTagOf = %v, %v; want %v", tag, err, wantTag)
			}
			commitment, err := certs.DscCommitmentOf(cert.PublicKey)
			if err != nil {
				t.Fatal(err)
			}
			bz := commitment.Bytes()
			fromChain := new(big.Int).SetBytes(bz[:])
			signals := readFixture(t, dir, "public_inputs")
			fromProof := new(big.Int).SetBytes(signals[dscKeyIndex*32 : (dscKeyIndex+1)*32])
			if fromChain.Cmp(fromProof) != 0 {
				t.Fatalf("commitment mismatch:\n  chain   = %s\n  circuit = %s", fromChain, fromProof)
			}
			if want := strings.TrimSpace(string(readFixture(t, dir, "expected_dsc_key"))); fromChain.String() != want {
				t.Fatalf("commitment %s, generator expected %s", fromChain, want)
			}
			// The DSC chains to the fixture CSCA as x/pki requires.
			csca, err := certs.ParseCert(readFixture(t, dir, "csca.der"))
			if err != nil {
				t.Fatal(err)
			}
			if err := certs.VerifySignedBy(cert, csca.PublicKey); err != nil {
				t.Fatalf("DSC not signed by its CSCA: %v", err)
			}
		})
	}
}

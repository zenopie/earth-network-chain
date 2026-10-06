// Package ultrahonk verifies Barretenberg (bb v5.0.0) UltraHonk proofs on-chain,
// via CGo bindings to Barretenberg's own verifier (github.com/burnt-labs/
// barretenberg-go, vendored under third_party with the native lib rebuilt against
// bb v5.0.0). This is the verifier for zkPassport-style Noir proofs.
//
// Flavor: default poseidon2 (UltraZKFlavor) — the natural choice for a non-EVM
// chain and zkPassport's internal recursive format. Proofs and verifying keys are
// Barretenberg binary blobs (`bb prove` / `bb write_vk`); public inputs are
// 32-byte big-endian field elements.
//
// NOTE: requires CGO_ENABLED=1 and the native libbarretenberg.a for the build
// platform (third_party/barretenberg-go/lib/<platform>/). Only this package (and
// anything importing it) needs CGo; the rest of the chain stays pure Go.
package ultrahonk

import (
	"crypto/sha256"
	"fmt"
	"math/big"
	"os"
	"sync"

	bb "github.com/burnt-labs/barretenberg-go/barretenberg"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
)

// init lowers Barretenberg's log level below info unless BB_VERBOSE is set:
// at info, every proof that fails prints "UltraVerifier: verification failed
// ..." to stderr, so anyone sending junk proofs (each refused, most for free
// in CheckTx) could fill a node's logs.
func init() {
	if os.Getenv("BB_VERBOSE") == "" {
		bb.SetLogLevel(bb.LogLevelWarn)
	}
}

// FieldSize is the byte length of a BN254 field element.
const FieldSize = 32

// ProofSize is the length of every bb v5.0.0 UltraHonk (ZK flavor) proof,
// whatever the circuit: the sumcheck is padded to a constant log size, so a
// proof is always 458 field elements. Verify refuses any other length. bb
// itself does not: it reads the elements it needs and ignores what follows,
// so a proof with a byte appended verified as the same proof, one tx
// re-spelled under another hash.
const ProofSize = 14_656

// PairingPointInputs is how many public inputs a bb v5.0.0 UltraHonk
// verifying key counts beyond the circuit's own: the aggregated pairing point
// object, eight limbs, which the proof carries itself. A key's declared count
// minus this is the number of inputs a caller supplies.
const PairingPointInputs = 8

// Verify checks a bb v5.0.0 UltraHonk proof against vk with the given public
// inputs (each a 32-byte big-endian field element). It returns true iff the
// proof is valid. A malformed vk/proof yields an error; a well-formed but
// invalid proof yields (false, nil).
//
// The proof must be exactly ProofSize bytes. Each input must be exactly
// FieldSize bytes holding a value below the BN254
// scalar modulus, and there must be exactly as many as the key declares. The
// library checks neither. A value of p or more is reduced mod p inside the
// verifier, so p+x verifies wherever x does — two encodings of one input, and
// any caller comparing the bytes it passed in against something else is then
// comparing the wrong thing. So both are checked here, before the native
// code runs.
func Verify(vk, proof []byte, publicInputs [][]byte) (bool, error) {
	if len(proof) != ProofSize {
		return false, fmt.Errorf("proof is %d bytes, want %d", len(proof), ProofSize)
	}
	modulus := fr.Modulus()
	// Every 32-byte element of the proof is read by bb as a scalar field
	// element (commitments travel as limbs), reduced mod r on the way in: an
	// element x and x+r (both under 2^256) verify alike, so one proof had up to
	// 2^458 spellings, each a different tx hash. Only the canonical one is
	// accepted.
	for off := 0; off < len(proof); off += FieldSize {
		if new(big.Int).SetBytes(proof[off:off+FieldSize]).Cmp(modulus) >= 0 {
			return false, fmt.Errorf("proof element %d is not a canonical field element", off/FieldSize)
		}
	}
	for i, in := range publicInputs {
		if len(in) != FieldSize {
			return false, fmt.Errorf("public input %d is %d bytes, want %d", i, len(in), FieldSize)
		}
		if new(big.Int).SetBytes(in).Cmp(modulus) >= 0 {
			return false, fmt.Errorf("public input %d is not a canonical field element", i)
		}
	}
	v, cached, err := verifierFor(vk)
	if err != nil {
		return false, fmt.Errorf("parse verification key: %w", err)
	}
	if !cached {
		defer v.Close()
	}
	declared, err := v.NumPublicInputs()
	if err != nil {
		return false, fmt.Errorf("read verification key: %w", err)
	}
	if want := declared - PairingPointInputs; len(publicInputs) != want {
		return false, fmt.Errorf("got %d public inputs, the verification key takes %d", len(publicInputs), want)
	}
	p, err := bb.ParseProof(proof)
	if err != nil {
		return false, fmt.Errorf("parse proof: %w", err)
	}
	return v.VerifyWithBytes(p, publicInputs)
}

// VerifyRaw is like Verify but takes the public inputs as one concatenated
// buffer of 32-byte big-endian field elements (the layout of bb's
// `public_inputs` file).
func VerifyRaw(vk, proof, publicInputs []byte) (bool, error) {
	if len(publicInputs)%FieldSize != 0 {
		return false, fmt.Errorf("public inputs length %d not a multiple of %d", len(publicInputs), FieldSize)
	}
	chunks := make([][]byte, 0, len(publicInputs)/FieldSize)
	for i := 0; i+FieldSize <= len(publicInputs); i += FieldSize {
		c := make([]byte, FieldSize)
		copy(c, publicInputs[i:i+FieldSize])
		chunks = append(chunks, c)
	}
	return Verify(vk, proof, chunks)
}

// verifiers caches the parsed verifying keys by sha256 of their bytes (audit
// A-3): Verify parsed its key natively on every proof, besides the parse
// bb's verify does itself. A Verifier is safe for concurrent use and a key's
// parse is deterministic, so the cache changes nothing but cost. Bounded:
// the chain holds 37 keys; past maxCachedVerifiers a key is parsed per call
// as before.
var (
	verifiersMu sync.Mutex
	verifiers   = map[[32]byte]*bb.Verifier{}
)

const maxCachedVerifiers = 128

// verifierFor returns the Verifier for vk, from the cache or freshly parsed,
// and whether it is cached (never closed); the caller closes one that is not.
func verifierFor(vk []byte) (*bb.Verifier, bool, error) {
	h := sha256.Sum256(vk)
	verifiersMu.Lock()
	defer verifiersMu.Unlock()
	if v, ok := verifiers[h]; ok {
		return v, true, nil
	}
	v, err := bb.NewVerifierFromBytes(vk)
	if err != nil {
		return nil, false, err
	}
	if len(verifiers) >= maxCachedVerifiers {
		return v, false, nil
	}
	verifiers[h] = v
	return v, true, nil
}

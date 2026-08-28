package ultrahonk_test

import (
	"encoding/hex"
	"math/big"
	"os"
	"path/filepath"
	"testing"

	"github.com/earth-network/earth/zk/ultrahonk"
)

// A proof is not a bearer token.
//
// The proof bytes ride in the clear inside MsgRegister, so anyone who reads a
// block has them. Before the registrant's address was a public input, the only
// thing that made replaying those bytes pointless was the chain refusing a
// nullifier that was already live -- which also meant that losing the wallet you
// registered from stranded your personhood until the registration lapsed a year
// later.
//
// Letting a re-registration MOVE a registration is what unstrands that, and it
// is only safe if the proof cannot be used from an address it was not made for.
// This test is the evidence for that claim, rather than an argument for it: the
// same proof is verified twice, changing nothing but the address in the public
// input vector.
func TestProofDoesNotVerifyForAnotherAddress(t *testing.T) {
	dir := filepath.Join("testdata", "lean_poa")
	read := func(name string) []byte {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		return b
	}

	vk, proof, pub := read("vk"), read("proof"), read("public_inputs")
	boundTo := read("expected_address")

	const addressIndex = 1
	if len(pub) != 4*ultrahonk.FieldSize {
		t.Fatalf("expected 4 public signals, got %d", len(pub)/ultrahonk.FieldSize)
	}

	// The address the fixture was proved for really is the one in the vector.
	off := addressIndex * ultrahonk.FieldSize
	if got, want := new(big.Int).SetBytes(pub[off:off+ultrahonk.FieldSize]), new(big.Int).SetBytes(boundTo); got.Cmp(want) != 0 {
		t.Fatalf("public signal %d is %s, expected the fixture address %s", addressIndex, got, want)
	}

	// As proved: it verifies.
	ok, err := ultrahonk.VerifyRaw(vk, proof, pub)
	if err != nil {
		t.Fatalf("verify as proved: %v", err)
	}
	if !ok {
		t.Fatal("the fixture proof does not verify against its own public inputs")
	}

	// Now the attack, which is the whole of it: take the proof off the chain
	// unchanged and present it as your own by naming your address instead.
	stolen := make([]byte, len(pub))
	copy(stolen, pub)
	attacker := []byte("mallory-other-wallet")[:20]
	copy(stolen[off:off+ultrahonk.FieldSize], make([]byte, ultrahonk.FieldSize))
	copy(stolen[off+ultrahonk.FieldSize-len(attacker):off+ultrahonk.FieldSize], attacker)

	ok, err = ultrahonk.VerifyRaw(vk, proof, stolen)
	if err == nil && ok {
		t.Fatal("a proof made for one address verified for another -- registration is stealable")
	}
	t.Logf("replay from another address rejected (ok=%v, err=%v)", ok, err)

	// And the rejection is specifically the address, not incidental damage to
	// the vector: put the real address back and it verifies again.
	copy(stolen[off:off+ultrahonk.FieldSize], pub[off:off+ultrahonk.FieldSize])
	ok, err = ultrahonk.VerifyRaw(vk, proof, stolen)
	if err != nil || !ok {
		t.Fatalf("restoring the bound address should verify again (ok=%v, err=%v)", ok, err)
	}

	t.Logf("proof is bound to %s (0x%s)", boundTo, hex.EncodeToString(boundTo))
}

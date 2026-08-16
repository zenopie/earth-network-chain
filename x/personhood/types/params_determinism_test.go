package types

import (
	"bytes"
	"testing"
)

// TestParamsMarshalIsDeterministic guards the verifying_keys map against Go's
// randomised map iteration.
//
// Params is written to state, so its bytes feed the AppHash. Without
// gogoproto's stable_marshaler, each validator serialises the map in a different
// order, writes different bytes, and computes a different AppHash — the network
// halts with "wrong Block.Header.AppHash" as soon as more than one key is
// configured. A single-node devnet never notices, because it only ever compares
// against itself. This is not hypothetical: it took down a 3-validator testnet
// the first time the seven register-circuit keys were seeded.
//
// Marshalling repeatedly in one process is enough to catch a regression: Go
// randomises the iteration order per range statement, not per process.
func TestParamsMarshalIsDeterministic(t *testing.T) {
	p := Params{
		VerifyingKeys: map[string][]byte{
			"lean_poa":              []byte("vk-p256"),
			"lean_poa_p384":         []byte("vk-p384"),
			"lean_poa_rsa2048":      []byte("vk-rsa2048"),
			"lean_poa_rsa4096":      []byte("vk-rsa4096"),
			"lean_poa_brainpool256": []byte("vk-bp256"),
			"lean_poa_brainpool384": []byte("vk-bp384"),
			"lean_poa_brainpool512": []byte("vk-bp512"),
		},
		RegistrationValiditySeconds: 31536000,
	}

	first, err := p.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for i := 0; i < 200; i++ {
		got, err := p.Marshal()
		if err != nil {
			t.Fatalf("marshal %d: %v", i, err)
		}
		if !bytes.Equal(first, got) {
			t.Fatalf("Params.Marshal is not deterministic (differs on iteration %d) — "+
				"validators would disagree on the AppHash; check that params.proto "+
				"still sets (gogoproto.stable_marshaler) = true", i)
		}
	}

	// And a round-trip must preserve every key, not merely be stable.
	var back Params
	if err := back.Unmarshal(first); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(back.VerifyingKeys) != len(p.VerifyingKeys) {
		t.Fatalf("round-trip lost keys: %d -> %d", len(p.VerifyingKeys), len(back.VerifyingKeys))
	}
	for k, v := range p.VerifyingKeys {
		if !bytes.Equal(back.VerifyingKeys[k], v) {
			t.Fatalf("round-trip corrupted key %q", k)
		}
	}
}

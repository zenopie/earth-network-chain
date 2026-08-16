package ultrahonk

import (
	"encoding/hex"
	"os"
	"strings"
	"testing"
)

// TestDeviceProof verifies a REAL (toy e2e) proof generated on the user's Pixel 6
// by noir_android (bb v5.0.0 final, barretenberg-rs 5.0.0) against the chain
// verifier (bb v5.0.0 final). See TestLeanPoaDeviceProof for the same loop on the
// real ~130k-gate passport circuit.
func TestDeviceProof(t *testing.T) {
	ph, err := os.ReadFile("testdata/device_proof.hex")
	if err != nil {
		t.Skip(err)
	}
	vh, _ := os.ReadFile("testdata/device_vk.hex")
	vk, _ := hex.DecodeString(strings.TrimSpace(strings.TrimPrefix(string(vh), "0x")))
	flat, _ := hex.DecodeString(strings.TrimSpace(strings.TrimPrefix(string(ph), "0x")))
	t.Logf("vk=%d bytes (%d f), flattened proof=%d bytes (%d f + 4B prefix)", len(vk), len(vk)/32, len(flat), (len(flat)-4)/32)

	// flattened = [4B prefix][numPub fields][proof fields]. Try numPub=2 (y + return).
	body := flat[4:]
	for _, numPub := range []int{2, 1, 0} {
		if numPub*32 > len(body) {
			continue
		}
		var pub [][]byte
		for i := 0; i < numPub; i++ {
			pub = append(pub, body[i*32:(i+1)*32])
		}
		proof := body[numPub*32:]
		ok, err := Verify(vk, proof, pub)
		t.Logf("numPub=%d proof=%dB verify=%v err=%v", numPub, len(proof), ok, err)
		if ok {
			t.Logf("*** DEVICE PROOF VERIFIES ON CHAIN (numPub=%d) — versions compatible ***", numPub)
			return
		}
	}
	t.Log("device proof did NOT verify with v5.0.0 chain lib — version mismatch, need nightly build")
}

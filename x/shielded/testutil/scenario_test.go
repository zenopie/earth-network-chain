package testutil

import (
	"testing"

	"github.com/earth-network/earth/zk/privacy"
)

// Every output of the scenario carries a real note ciphertext its owner (and
// only its owner) opens to the note's exact opening.
func TestScenarioCiphertextsOpen(t *testing.T) {
	s := Default()
	for _, sp := range s.Transfers {
		for i, out := range sp.Out {
			n, err := privacy.DecryptNote(sp.Ciphertexts[i], out.CM(), out.Owner.EK)
			if err != nil {
				t.Fatalf("%s output %d: %v", sp.Name, i, err)
			}
			if n.Value != out.Value || n.Rho != out.Rho || n.Rcm != out.Rcm || n.AssetID != privacy.AssetID(out.Denom) {
				t.Fatalf("%s output %d: wrong opening", sp.Name, i)
			}
			if n.CM(out.Owner.OwnerPK()) != out.CM() {
				t.Fatalf("%s output %d: cm mismatch", sp.Name, i)
			}
			other := Alice
			if out.Owner == Alice {
				other = Bob
			}
			if _, err := privacy.DecryptNote(sp.Ciphertexts[i], out.CM(), other.EK); err == nil {
				t.Fatalf("%s output %d: opened by the wrong wallet", sp.Name, i)
			}
		}
	}
}

package testutil

import (
	"testing"

	"github.com/earth-network/earth/zk/privacy"
)

// Every output of the scenario (dummies too) carries a real note ciphertext
// its owner (and only its owner) opens to the note's exact opening.
func TestScenarioCiphertextsOpen(t *testing.T) {
	s := Default()
	for k, sp := range s.Sends {
		b, err := s.Bundle(k)
		if err != nil {
			t.Fatal(err)
		}
		for i, a := range sp.Actions {
			out := a.Out
			ct := b.Actions[i].Ciphertext
			n, err := privacy.DecryptNote(ct, out.CM(), out.Owner.EK)
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
			if _, err := privacy.DecryptNote(ct, out.CM(), other.EK); err == nil {
				t.Fatalf("%s output %d: opened by the wrong wallet", sp.Name, i)
			}
		}
	}
}

// Every send balances: per denom, spends = outputs + balance, and the fee is
// covered.
func TestScenarioBalances(t *testing.T) {
	s := Default()
	for _, sp := range s.Sends {
		var erth uint64
		for _, b := range sp.Balances() {
			if b.Denom == "uerth" {
				erth = b.Amount
			}
		}
		if erth < sp.Fee {
			t.Fatalf("%s: fee %d over its uerth balance %d", sp.Name, sp.Fee, erth)
		}
		if (erth > sp.Fee) != (sp.Receiver != nil) {
			t.Fatalf("%s: receiver must be set exactly when something is unshielded", sp.Name)
		}
	}
}

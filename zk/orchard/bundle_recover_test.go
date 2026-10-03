package orchard

import (
	"errors"
	"testing"

	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
)

// Audit 4 I3: a verifier that panics inside VerifyProofs' worker goroutines
// (where baseapp's recovery does not reach) yields that action's
// ActionError instead of crashing the process; the same for the sequential
// path.
func TestAudit4VerifierPanicIsActionError(t *testing.T) {
	b := &Bundle{Actions: make([]Action, 3)}
	var sighash fr.Element
	boom := func([]byte, [][]byte) (bool, error) { panic("verifier blew up") }
	for name, run := range map[string]func() error{
		"parallel":   func() error { return VerifyProofs([]*Bundle{b}, sighash, boom) },
		"sequential": func() error { return VerifyProofsSequential([]*Bundle{b}, sighash, boom) },
	} {
		var err error
		require := func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("%s: panic escaped: %v", name, r)
				}
			}()
			err = run()
		}
		require()
		var ae *ActionError
		if !errors.As(err, &ae) || ae.Bundle != 0 || ae.Action != 0 || ae.Err == nil {
			t.Fatalf("%s: got %v, want action 0's ActionError", name, err)
		}
	}
}

package types

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Audit 5 L-AS2: the private gas prices are capped, so governance cannot
// price the chamber's votes out of every block.
func TestGasParamsCapped(t *testing.T) {
	p := DefaultParams()
	require.NoError(t, p.Validate())
	for _, set := range []func(*Params){
		func(p *Params) { p.ProofVerificationGas = MaxProofVerificationGas + 1 },
		func(p *Params) { p.NoteGas = MaxNoteGas + 1 },
		func(p *Params) { p.BundleGas = MaxBundleGas + 1 },
	} {
		q := DefaultParams()
		set(&q)
		require.Error(t, q.Validate())
	}
	q := DefaultParams()
	q.ProofVerificationGas, q.NoteGas, q.BundleGas = MaxProofVerificationGas, MaxNoteGas, MaxBundleGas
	require.NoError(t, q.Validate())
}

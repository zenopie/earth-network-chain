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
	// At the caps the largest bundle must shrink to fit MaxBundleShapeGas.
	q := DefaultParams()
	q.ProofVerificationGas, q.NoteGas, q.BundleGas = MaxProofVerificationGas, MaxNoteGas, MaxBundleGas
	require.Error(t, q.Validate(), "16 actions at the caps do not fit a block")
	q.MaxActionsPerBundle = 4
	require.NoError(t, q.Validate())
}

// Audit A-5: params whose largest bundle could never be included are
// refused: 32 actions at the default prices is 73.7M gas.
func TestLargestBundleFitsTheBudget(t *testing.T) {
	q := DefaultParams()
	q.MaxActionsPerBundle = 32
	require.Error(t, q.Validate())
	q.MaxActionsPerBundle = 25 // 0.1M + 25 x 2.3M = 57.6M
	require.NoError(t, q.Validate())
}

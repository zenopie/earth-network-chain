package types

import (
	"testing"

	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	"github.com/stretchr/testify/require"

	shieldedtypes "github.com/earth-network/earth/x/shielded/types"
	"github.com/earth-network/earth/zk/privacy"
)

// A ClaimProof carries one distinct, nonzero, canonical nullifier per day of
// a run of 1 to ClaimWindowDays days.
func TestClaimProofValidateBasic(t *testing.T) {
	nfs := func(n int) [][]byte {
		out := make([][]byte, n)
		for i := range out {
			out[i] = privacy.FieldBytes(privacy.U64(uint64(i + 1)))
		}
		return out
	}
	ok := ClaimProof{Proof: make([]byte, shieldedtypes.ProofBytes), Root: privacy.FieldBytes(privacy.U64(9)), Nullifiers: nfs(3)}
	require.NoError(t, ok.ValidateBasic(3))
	require.ErrorIs(t, ok.ValidateBasic(4), ErrInvalidMsg, "one nullifier per day")

	dup := ok
	dup.Nullifiers = [][]byte{nfs(1)[0], nfs(2)[1], nfs(1)[0]}
	require.ErrorContains(t, dup.ValidateBasic(3), "repeats")

	zero := ok
	zero.Nullifiers = append(nfs(2), make([]byte, 32))
	require.ErrorContains(t, zero.ValidateBasic(3), "is 0", "0 marks an unused slot")

	short := ok
	short.Proof = short.Proof[1:]
	require.ErrorIs(t, short.ValidateBasic(3), ErrInvalidMembership)

	require.Zero(t, (&MsgClaimAnmlDays{FirstDay: 5, Day: 4}).Days())
	require.Equal(t, uint64(ClaimWindowDays), (&MsgClaimAnmlDays{FirstDay: 10, Day: 10 + ClaimWindowDays - 1}).Days())
}

// The claim circuit's public inputs: root, 7 scopes, 7 nullifiers (the slots
// after the run 0 in both), signal, max_activation.
func TestClaimPublicInputs(t *testing.T) {
	c := ClaimProof{Root: privacy.FieldBytes(privacy.U64(1)), Nullifiers: [][]byte{privacy.FieldBytes(privacy.U64(20)), privacy.FieldBytes(privacy.U64(21))}}
	pub := ClaimPublicInputs(c, []fr.Element{privacy.U64(10), privacy.U64(11)}, privacy.U64(30), 40)
	require.Len(t, pub, 3+2*ClaimSlots)
	want := []uint64{1, 10, 11, 0, 0, 0, 0, 0, 20, 21, 0, 0, 0, 0, 0, 30, 40}
	for i, w := range want {
		require.Equal(t, privacy.FieldBytes(privacy.U64(w)), pub[i], "input %d", i)
	}
}

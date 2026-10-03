package keeper_test

import (
	"crypto/rand"
	"sync/atomic"
	"testing"
	"time"

	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	gfr "github.com/consensys/gnark-crypto/ecc/grumpkin/fr"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	shieldedtest "github.com/earth-network/earth/x/shielded/testutil"
	"github.com/earth-network/earth/x/shielded/types"
	"github.com/earth-network/earth/zk/orchard"
	"github.com/earth-network/earth/zk/privacy"
	"github.com/earth-network/earth/zk/ultrahonk"
)

// forgedSend is the auditor's free-CheckTx tx: n actions with fresh
// nullifiers, a valid anchor, recycled proofs, and a VALID binding signature
// over a forged uerth balance (anyone can make one: the value commitments are
// unproven until the proofs are checked). No notes, funds or keys needed.
func forgedSend(t *testing.T, f *fixture, n int, anchor, proof []byte) *types.MsgSend {
	t.Helper()
	const fee = 1_000_000
	erthG := orchard.ValueBase(privacy.AssetID(types.FeeDenom))
	var bsk gfr.Element
	b := types.Bundle{Balances: []types.ValueBalance{{Denom: types.FeeDenom, Amount: fee}}}
	for i := 0; i < n; i++ {
		var r gfr.Element
		_, _ = r.SetRandom()
		bsk.Add(&bsk, &r)
		cv := orchard.Mul(orchard.R, r)
		if i == 0 {
			cv = orchard.Add(cv, orchard.Mul(erthG, orchard.ScalarU64(fee)))
		}
		var nf, cm fr.Element
		_, _ = nf.SetRandom()
		_, _ = cm.SetRandom()
		b.Actions = append(b.Actions, types.Action{Anchor: anchor, Nullifier: privacy.FieldBytes(nf),
			Commitment: privacy.FieldBytes(cm), Cv: orchard.PointBytes(cv), Proof: proof, Ciphertext: shieldedtest.NoteCT("junk")})
	}
	b.BindingSig = make([]byte, orchard.BindingSigSize)
	m := &types.MsgSend{Bundle: b, Fee: fee}
	sh, err := types.Sighash(m, shieldedtest.ChainID, testTx, f.ac)
	require.NoError(t, err)
	m.Bundle.BindingSig, err = orchard.SignBinding(bsk, sh, rand.Reader)
	require.NoError(t, err)
	return m
}

// Audit (shielded M2): a forged tx passing every check up to the proofs made
// CheckTx verify all of its proofs, for free. CheckTx now verifies one at a
// time and stops at the first failure: one verification per junk tx. In a
// block (paid for by the gas charged before verification) it still verifies
// them all, in parallel, deterministically reporting the first failure.
func TestCheckPrivateMsgAuditOneJunkProof(t *testing.T) {
	f := initFixture(t)
	s := shieldedtest.Default()
	f.runScenario(s, shieldedtest.Send2)
	f.nextBlock(5 * time.Second)
	real := f.scenarioMsg(s, shieldedtest.Send2)
	anchor, err := f.k.LatestRoot.Get(f.ctx)
	require.NoError(t, err)
	const n = 16
	m := forgedSend(t, f, n, anchor, real.Bundle.Actions[0].Proof)
	require.NoError(t, m.ValidateBasic())

	var calls int64
	k := f.k.WithProofVerifier(func(vk, proof []byte, in [][]byte) (bool, error) {
		atomic.AddInt64(&calls, 1)
		return ultrahonk.Verify(vk, proof, in)
	})
	for _, c := range []struct {
		name string
		ctx  sdk.Context
		want int64
	}{
		{"CheckTx", f.ctx.WithIsCheckTx(true), 1},
		{"FinalizeBlock", f.ctx.WithExecMode(sdk.ExecModeFinalize), n},
	} {
		calls = 0
		prepared, err := k.CheckPrivateMsg(c.ctx, m)
		require.NoError(t, err, "every check before the proofs passes")
		err = k.VerifyPrivateMsg(c.ctx, prepared)
		require.ErrorIs(t, err, types.ErrInvalidProof, c.name)
		require.Contains(t, err.Error(), "bundle 0 action 0", c.name)
		require.Equal(t, c.want, calls, c.name)
	}
}

// Audit (shielded L2): the pool's own account cannot be the other side of a
// module mint, a release or a fee payment.
func TestAuditPoolCannotPayItself(t *testing.T) {
	f := initFixture(t)
	pc := privacy.FieldBytes(shieldedtest.Det("pc", 1))
	_, _, err := f.k.MintNote(f.ctx, types.ModuleName, sdk.NewInt64Coin(types.FeeDenom, 1), pc, shieldedtest.BlindCT("x"))
	require.ErrorIs(t, err, types.ErrUnauthorized)
	require.ErrorIs(t, f.k.PayFeeFromModule(f.ctx, types.ModuleName, sdk.NewInt64Coin(types.FeeDenom, 1).Amount), types.ErrUnauthorized)
	_, err = f.k.ReleaseToModule(f.ctx, &types.MsgSend{}, types.FeeDenom, types.ModuleName)
	require.ErrorIs(t, err, types.ErrUnauthorized)
}

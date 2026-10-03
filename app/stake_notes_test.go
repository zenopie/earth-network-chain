package app

// The stake side of the private staking harness: the test wallet's
// owner-locked stake notes (x/shieldedstaking's stake note tree), synced from
// the chain like the shielded pool's notes, and stake circuit proofs built
// for them. Proofs are cached by public inputs beside the action proofs
// (stake-<hash>.proof), proven with nargo + bb when EARTH_CIRCUITS is set.

import (
	"encoding/hex"
	"fmt"
	"strings"

	abci "github.com/cometbft/cometbft/abci/types"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	"github.com/stretchr/testify/require"

	shieldedtest "github.com/earth-network/earth/x/shielded/testutil"
	shieldedtypes "github.com/earth-network/earth/x/shielded/types"
	sstypes "github.com/earth-network/earth/x/shieldedstaking/types"
	"github.com/earth-network/earth/zk/merkle"
	"github.com/earth-network/earth/zk/privacy"
)

// snote is a stake note the wallet owns (or will).
type snote struct {
	denom    string
	amount   uint64
	rho, rcm fr.Element
	pos      uint64
	known    bool
	spent    bool
}

type stakeWallet struct {
	notes   []*snote
	leaves  []fr.Element
	scanned uint64
}

func (e *stakeEnv) freshStake(denom string, amount uint64) *snote {
	e.w.seq++
	return &snote{denom: denom, amount: amount, rho: ssDet("srho", e.w.seq), rcm: ssDet("srcm", e.w.seq)}
}

func (e *stakeEnv) spc(n *snote) fr.Element {
	return privacy.StakePC(privacy.OwnerPK(e.w.nk), n.rho, n.rcm)
}

func (e *stakeEnv) scm(n *snote) fr.Element {
	return privacy.StakeCM(privacy.AssetID(n.denom), n.amount, e.spc(n))
}

func (e *stakeEnv) snf(n *snote) fr.Element { return privacy.StakeNF(e.w.nk, n.rho, uint32(n.pos)) }

func (e *stakeEnv) trackStake(ns ...*snote) {
	for _, n := range ns {
		if n != nil && n.amount > 0 {
			e.sw.notes = append(e.sw.notes, n)
		}
	}
}

// scanStake reads the stake leaves appended since the last scan and places
// tracked notes.
func (e *stakeEnv) scanStake() {
	ctx := e.ctx()
	size, _, err := e.app.ShieldedStakingKeeper.StakeTreeState(ctx)
	require.NoError(e.t, err)
	for ; e.sw.scanned < size; e.sw.scanned++ {
		bz, err := e.app.ShieldedStakingKeeper.StakeCommitment(ctx, e.sw.scanned)
		require.NoError(e.t, err)
		leaf, err := privacy.FieldFromBytes(bz)
		require.NoError(e.t, err)
		e.sw.leaves = append(e.sw.leaves, leaf)
	}
	for _, n := range e.sw.notes {
		if n.known {
			continue
		}
		cm := e.scm(n)
		for i, l := range e.sw.leaves {
			if l == cm {
				n.pos, n.known = uint64(i), true
				break
			}
		}
	}
}

func (e *stakeEnv) stakeTree(size uint64) *merkle.Tree {
	t := merkle.NewMem()
	for _, l := range e.sw.leaves[:size] {
		_, err := t.Append(l)
		require.NoError(e.t, err)
	}
	return t
}

// stakeBalance is the wallet's unspent stake of denom.
func (e *stakeEnv) stakeBalance(denom string) uint64 {
	var s uint64
	for _, n := range e.sw.notes {
		if n.known && !n.spent && n.denom == denom {
			s += n.amount
		}
	}
	return s
}

// unspentStake is a known unspent stake note of denom, nil if none.
func (e *stakeEnv) unspentStake(denom string) *snote {
	for _, n := range e.sw.notes {
		if n.known && !n.spent && n.denom == denom {
			return n
		}
	}
	return nil
}

// mintedStake finds the stake note the chain minted to n's stake pc in res
// (its amount read from the event) and tracks it.
func (e *stakeEnv) mintedStake(res *abci.ExecTxResult, n *snote) *snote {
	e.t.Helper()
	want := hex.EncodeToString(privacy.FieldBytes(e.spc(n)))
	for _, ev := range eventsOf(res.Events, sstypes.EventTypeStakeNote) {
		if ev["spc"] != want {
			continue
		}
		require.Equal(e.t, n.denom, ev["denom"])
		_, err := fmt.Sscan(ev["amount"], &n.amount)
		require.NoError(e.t, err)
		e.trackStake(n)
		e.scanStake()
		require.True(e.t, n.known, "minted stake note not found in the stake tree")
		return n
	}
	e.t.Fatalf("no stake note minted to the wallet: %s", res.Log)
	return nil
}

// stakePlan is a stake proof being built: up to two inputs (all of denom),
// up to two outputs of the same owner, v_out leaving, a stake pc the chain may
// mint to (mint, any denom), an owner tag salt, and the size of the stake
// tree the inputs are proven under (0: the current tree).
type stakePlan struct {
	denom  string
	ins    []*snote
	outs   []*snote
	vOut   uint64
	mint   *snote
	salt   fr.Element
	atSize uint64
	proof  sstypes.StakeProof
}

func pad2[T any](xs []T, zero T) [2]T {
	out := [2]T{zero, zero}
	copy(out[:], xs)
	return out
}

// stake lays out a stake proof's public values (everything but the proof).
func (e *stakeEnv) stake(sp *stakePlan) *stakePlan {
	e.t.Helper()
	require.LessOrEqual(e.t, len(sp.ins), 2)
	require.LessOrEqual(e.t, len(sp.outs), 2)
	size := uint64(len(e.sw.leaves))
	if sp.atSize > 0 {
		size = sp.atSize
	}
	sp.atSize = size
	// A msg that has the chain mint a stake note names it (mint) and carries
	// its blind stake ciphertext.
	mints := sp.mint != nil
	if sp.mint == nil {
		sp.mint = e.freshStake(sp.denom, 0)
	}
	var anchor fr.Element
	if size > 0 {
		r, err := e.stakeTree(size).Root()
		require.NoError(e.t, err)
		anchor = r
	}
	p := sstypes.StakeProof{Anchor: privacy.FieldBytes(anchor), SpcMint: privacy.FieldBytes(e.spc(sp.mint)),
		OwnerTag: privacy.FieldBytes(privacy.OwnerTag(privacy.OwnerPK(e.w.nk), sp.salt))}
	for i := range 2 {
		var nf, cm fr.Element
		var ct []byte
		if i < len(sp.ins) {
			require.True(e.t, sp.ins[i].known && sp.ins[i].pos < size, "stake input outside the anchor's tree")
			nf = e.snf(sp.ins[i])
		}
		if i < len(sp.outs) && sp.outs[i].amount > 0 {
			cm = e.scm(sp.outs[i])
			ct = []byte(fmt.Sprintf("stake-ct:%d", e.w.seq))
		}
		p.Nullifiers = append(p.Nullifiers, privacy.FieldBytes(nf))
		p.Commitments = append(p.Commitments, privacy.FieldBytes(cm))
		p.Ciphertexts = append(p.Ciphertexts, ct)
	}
	if mints {
		p.SpcCiphertext = shieldedtest.BlindCT(fmt.Sprintf("spc/%d/%d", e.w.seq, len(e.sw.leaves)))
	}
	sp.proof = p
	return sp
}

// stakeProver proves the stake circuit into this suite's proof cache.
func (e *stakeEnv) stakeProver() *shieldedtest.Prover {
	script := "scripts/staking-fixtures.sh"
	if e.proofDir == dexProofs {
		script = "scripts/dex-fixtures.sh"
	}
	pr := shieldedtest.ForDir(e.t, e.proofDir, script)
	pr.Circuit, pr.VK = "stake", "../x/shieldedstaking/testdata/stake.vk"
	return pr
}

// stakeWitness is sp's Prover.toml under sighash, and its public inputs.
func (e *stakeEnv) stakeWitness(msg sstypes.StakeMsg, sp *stakePlan, sighash fr.Element) (string, [][]byte) {
	p := msg.StakeProofOf()
	pub := p.PublicInputs(sstypes.StakeAsset(msg.StakeDenom()), msg.VOut(), sighash)
	q := func(x fr.Element) string { return fmt.Sprintf("\"0x%x\"", privacy.FieldBytes(x)) }
	qs := func(xs [2]fr.Element) string { return "[" + q(xs[0]) + ", " + q(xs[1]) + "]" }
	u := func(xs [2]uint64) string { return fmt.Sprintf("[\"%d\", \"%d\"]", xs[0], xs[1]) }
	none := &snote{rho: ssDet("stake-dummy", 1), rcm: ssDet("stake-dummy", 2)}
	ins, outs := pad2(sp.ins, none), pad2(sp.outs, &snote{rho: ssDet("stake-dummy", 3), rcm: ssDet("stake-dummy", 4)})
	var inAmt, inPos, outAmt [2]uint64
	var inRho, inRcm, outRho, outRcm [2]fr.Element
	paths := make([]string, 2)
	tree := e.stakeTree(sp.atSize)
	for i := range 2 {
		inAmt[i], inRho[i], inRcm[i] = ins[i].amount, ins[i].rho, ins[i].rcm
		var sib [merkle.Depth]fr.Element
		if i < len(sp.ins) {
			inPos[i] = ins[i].pos
			var err error
			sib, err = tree.Path(ins[i].pos)
			require.NoError(e.t, err)
		}
		parts := make([]string, merkle.Depth)
		for j := range parts {
			parts[j] = q(sib[j])
		}
		paths[i] = "[" + strings.Join(parts, ", ") + "]"
		outAmt[i], outRho[i], outRcm[i] = outs[i].amount, outs[i].rho, outs[i].rcm
	}
	var b strings.Builder
	fmt.Fprintf(&b, "nk = %s\nin_amount = %s\nin_rho = %s\nin_rcm = %s\nin_pos = %s\nin_path = [%s]\n",
		q(e.w.nk), u(inAmt), qs(inRho), qs(inRcm), u(inPos), strings.Join(paths, ", "))
	fmt.Fprintf(&b, "out_amount = %s\nout_rho = %s\nout_rcm = %s\nmint_rho = %s\nmint_rcm = %s\ntag_salt = %s\n",
		u(outAmt), qs(outRho), qs(outRcm), q(sp.mint.rho), q(sp.mint.rcm), q(sp.salt))
	names := []string{"anchor", "asset", "nf_0", "nf_1", "cm_out_0", "cm_out_1", "v_in", "v_out", "spc_mint", "otag", "sighash"}
	for i, n := range names {
		if n == "v_in" || n == "v_out" {
			var v fr.Element
			_ = v.SetBytes(pub[i])
			fmt.Fprintf(&b, "%s = \"%s\"\n", n, v.String())
			continue
		}
		fmt.Fprintf(&b, "%s = \"0x%x\"\n", n, pub[i])
	}
	return b.String(), pub
}

// proveStake proves msg's stake proof from sp, under msg's sighash (every
// other field of msg must be final).
func (e *stakeEnv) proveStake(msg sstypes.StakeMsg, sp *stakePlan) {
	e.t.Helper()
	proof, err := e.tryProveStake(msg, sp)
	require.NoError(e.t, err)
	msg.StakeProofOf().Proof = proof
}

// tryProveStake is proveStake returning the prover's error
// (shieldedtest.ErrWitnessRefused when the circuit refuses the witness).
func (e *stakeEnv) tryProveStake(msg sstypes.StakeMsg, sp *stakePlan) ([]byte, error) {
	sighash, err := shieldedtypes.Sighash(msg, ssChainID, ssTx, e.app.AuthKeeper.AddressCodec())
	require.NoError(e.t, err)
	toml, pub := e.stakeWitness(msg, sp, sighash)
	return e.stakeProver().TryProve(toml, pub)
}

// settleStake marks sp executed: inputs spent, outputs tracked.
func (e *stakeEnv) settleStake(sp *stakePlan) {
	for _, n := range sp.ins {
		n.spent = true
	}
	e.trackStake(sp.outs...)
	e.scanStake()
}

// The stake side of the private staking harness: the test wallet's
// owner-locked stake notes (x/shieldedstaking's stake note tree), synced from
// the chain like the shielded pool's notes, and stake circuit proofs built
// for them. Proofs are cached by public inputs beside the action proofs
// (stake-<hash>.proof), proven with nargo + bb when EARTH_CIRCUITS is set.

package app

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	"github.com/stretchr/testify/require"

	allocationtypes "github.com/earth-network/earth/x/allocation/types"
	shieldedtest "github.com/earth-network/earth/x/shielded/testutil"
	shieldedtypes "github.com/earth-network/earth/x/shielded/types"
	sstypes "github.com/earth-network/earth/x/shieldedstaking/types"
	"github.com/earth-network/earth/zk/debt"
	"github.com/earth-network/earth/zk/merkle"
	"github.com/earth-network/earth/zk/privacy"
)

// snote is a stake note the wallet owns (or will). A note holding a
// redelegation's exposure carries its slash label (moveKey, moveTime,
// exposed).
type snote struct {
	denom    string
	amount   uint64
	rho, rcm fr.Element
	pos      uint64
	known    bool
	spent    bool
	moveKey  fr.Element
	moveTime uint64
	exposed  uint64
}

func (n *snote) labelled() bool { return !n.moveKey.IsZero() }

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

func (e *stakeEnv) label(n *snote) fr.Element {
	if !n.labelled() {
		return fr.Element{}
	}
	return privacy.StakeLabel(n.moveKey, n.moveTime, n.exposed)
}

func (e *stakeEnv) scm(n *snote) fr.Element {
	return privacy.StakeCM(privacy.AssetID(n.denom), n.amount, e.spc(n), e.label(n))
}

// sgw is a stake note's Groundworks tag.
func (e *stakeEnv) sgw(n *snote) fr.Element { return privacy.StakeGW(e.w.nk, n.rho) }

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

// stakeBalance is the wallet's unspent stake of denom (nominal: labelled
// exposures at their credited amount).
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

// unlabelledStake is a known unspent unlabelled stake note of denom: the
// one a redelegation's credit lane may merge into.
func (e *stakeEnv) unlabelledStake(denom string) *snote {
	for _, n := range e.sw.notes {
		if n.known && !n.spent && n.denom == denom && !n.labelled() {
			return n
		}
	}
	return nil
}

// creditLane is a stake proof's lane B: dst derth credited into in (an
// unlabelled note; nil: a padding input), labelled with the move.
type creditLane struct {
	denom    string
	in       *snote
	vIn      uint64
	moveTime uint64
	// out is the merged note (amount in + vIn, labelled by the move),
	// filled by stake.
	out *snote
	pad *snote
}

// stakePlan is a stake proof being built. Lane A: up to two inputs of denom
// (none: a padding input), one output (nil: a zero padding note), v_in
// credited, v_out leaving; a labelled input's label is kept on the output,
// or cleared (clear) once its window closed. Lane B: credit (a
// redelegation). vote and creditVote: the outputs that vote in Groundworks
// with split. The size of the stake tree the inputs are proven under (0: the
// current tree).
type stakePlan struct {
	denom      string
	ins        []*snote
	out        *snote
	vIn        uint64
	vOut       uint64
	clear      bool
	credit     *creditLane
	vote       bool
	creditVote bool
	split      []allocationtypes.AllocationWeight
	atSize     uint64
	proof      sstypes.StakeProof
	// filled by stake: the padding inputs and zero output, the debt witness.
	pad     *snote
	pad1    *snote
	zeroOut *snote
	debtW   *debt.Witness
}

func pad2[T any](xs []T, zero T) [2]T {
	out := [2]T{zero, zero}
	copy(out[:], xs)
	return out
}

// labelledIn is sp's labelled lane A input, nil if none.
func (sp *stakePlan) labelledIn() *snote {
	for _, n := range sp.ins {
		if n.labelled() {
			return n
		}
	}
	return nil
}

// debtTree is the chain's slash debt tree as a wallet rebuilds it from the
// rows in insertion order.
func (e *stakeEnv) debtTree() *debt.Tree {
	rows, err := e.app.ShieldedStakingKeeper.DebtRows(e.ctx(), 0, ^uint64(0))
	require.NoError(e.t, err)
	var rs []debt.Row
	for _, r := range rows {
		k, err := privacy.FieldFromBytes(r.Key)
		require.NoError(e.t, err)
		rs = append(rs, debt.Row{Key: k, Retained: r.Retained})
	}
	t, err := debt.Rebuild(rs)
	require.NoError(e.t, err)
	root, _, err := e.app.ShieldedStakingKeeper.DebtRoot(e.ctx())
	require.NoError(e.t, err)
	got, err := t.Root()
	require.NoError(e.t, err)
	require.Equal(e.t, root, privacy.FieldBytes(got), "the wallet's debt tree is the chain's")
	return t
}

// stake lays out a stake proof's public values (everything but the proof).
func (e *stakeEnv) stake(sp *stakePlan) *stakePlan {
	e.t.Helper()
	require.LessOrEqual(e.t, len(sp.ins), 2)
	size := uint64(len(e.sw.leaves))
	if sp.atSize > 0 {
		size = sp.atSize
	}
	sp.atSize = size
	r, err := e.stakeTree(size).Root()
	require.NoError(e.t, err)
	z := privacy.FieldBytes(fr.Element{})
	p := sstypes.StakeProof{Anchor: privacy.FieldBytes(r), DebtRoot: z, CreditNullifier: z, CreditCommitment: z,
		Commitment: z, CreditGroundworksTag: z, VoteTag: z, CreditVoteTag: z, PendingKey: z}
	nfs, gws := [2]fr.Element{}, [2]fr.Element{}
	for i, n := range sp.ins {
		require.True(e.t, n.known && n.pos < size, "stake input outside the anchor's tree")
		nfs[i], gws[i] = e.snf(n), e.sgw(n)
	}
	if len(sp.ins) == 0 {
		// A padding input: the owner's own would-be nullifier and tag.
		sp.pad = e.freshStake(sp.denom, 0)
		nfs[0], gws[0] = privacy.StakeNF(e.w.nk, sp.pad.rho, 0), e.sgw(sp.pad)
	}
	if len(sp.ins) < 2 {
		// The second slot is always spent too (padding with a fresh rho), so
		// a merge of two notes looks like a spend of one.
		sp.pad1 = e.freshStake(sp.denom, 0)
		nfs[1], gws[1] = privacy.StakeNF(e.w.nk, sp.pad1.rho, 0), e.sgw(sp.pad1)
	}
	p.Nullifiers = [][]byte{privacy.FieldBytes(nfs[0]), privacy.FieldBytes(nfs[1])}
	p.GroundworksTags = [][]byte{privacy.FieldBytes(gws[0]), privacy.FieldBytes(gws[1])}
	// Lane A's output: a kept label goes with it.
	if li := sp.labelledIn(); li != nil && !sp.clear && sp.out != nil {
		sp.out.moveKey, sp.out.moveTime, sp.out.exposed = li.moveKey, li.moveTime, li.exposed
	}
	// Every proof names the label window's current clear_before and the
	// current debt root once the chain is a window old (audit 7, B L-1),
	// whether it clears a label or not.
	cb, err := e.app.ShieldedStakingKeeper.ClearBefore(e.ctx())
	require.NoError(e.t, err)
	if sp.clear {
		require.NotNil(e.t, sp.labelledIn(), "nothing to clear")
	}
	if cb > 0 || sp.clear {
		p.ClearBefore = cb
		t := e.debtTree()
		root, err := t.Root()
		require.NoError(e.t, err)
		p.DebtRoot = privacy.FieldBytes(root)
		if sp.clear {
			w, err := t.Lookup(sp.labelledIn().moveKey)
			require.NoError(e.t, err)
			sp.debtW = &w
		}
	}
	out := sp.out
	if out == nil {
		sp.zeroOut = e.freshStake(sp.denom, 0)
		out = sp.zeroOut
	}
	p.Commitment = privacy.FieldBytes(e.scm(out))
	p.Ciphertext = shieldedtest.StakeCT(fmt.Sprintf("%d/%d", e.w.seq, len(e.sw.leaves)))
	if sp.vote {
		require.NotNil(e.t, sp.out, "a padding output cannot vote")
		p.VoteTag = privacy.FieldBytes(e.sgw(sp.out))
		p.VoteWeight = sp.out.amount - sp.out.exposed
		// A kept label's exposure votes pending.
		if sp.out.exposed > 0 {
			p.PendingKey = privacy.FieldBytes(sp.out.moveKey)
			p.PendingTime, p.PendingExposed = sp.out.moveTime, sp.out.exposed
		}
	}
	if c := sp.credit; c != nil {
		var nf fr.Element
		in := c.in
		if in != nil {
			require.True(e.t, in.known && in.pos < size && !in.labelled(), "credit input: an unlabelled note in the anchor's tree")
			nf = e.snf(in)
		} else {
			c.pad = e.freshStake(c.denom, 0)
			in = c.pad
			nf = privacy.StakeNF(e.w.nk, c.pad.rho, 0)
		}
		c.out = e.freshStake(c.denom, in.amount+c.vIn)
		c.out.moveKey, c.out.moveTime, c.out.exposed = nf, c.moveTime, c.vIn
		p.CreditNullifier = privacy.FieldBytes(nf)
		p.CreditGroundworksTag = privacy.FieldBytes(e.sgw(in))
		p.CreditCommitment = privacy.FieldBytes(e.scm(c.out))
		p.CreditCiphertext = shieldedtest.StakeCT(fmt.Sprintf("cr/%d/%d", e.w.seq, len(e.sw.leaves)))
		if sp.creditVote {
			p.CreditVoteTag = privacy.FieldBytes(e.sgw(c.out))
			p.CreditVoteWeight = c.out.amount - c.out.exposed
		}
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

// tomlQ, tomlPath and friends write Prover.toml values.
func tomlQ(x fr.Element) string { return fmt.Sprintf("\"0x%x\"", privacy.FieldBytes(x)) }

func tomlU(v uint64) string { return fmt.Sprintf("\"%d\"", v) }

func tomlPath(sib [merkle.Depth]fr.Element) string {
	parts := make([]string, merkle.Depth)
	for j := range parts {
		parts[j] = tomlQ(sib[j])
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// stakeWitness is sp's Prover.toml under sighash, and its public inputs.
func (e *stakeEnv) stakeWitness(msg sstypes.StakeMsg, sp *stakePlan, lanes sstypes.StakeLanes, sighash fr.Element) (string, [][]byte) {
	p := msg.StakeProofOf()
	pub := p.PublicInputs(lanes, sighash)
	none := &snote{rho: ssDet("stake-dummy", 1), rcm: ssDet("stake-dummy", 2)}
	ins := pad2(sp.ins, none)
	if sp.pad != nil {
		ins[0] = sp.pad
	}
	if sp.pad1 != nil {
		ins[1] = sp.pad1
	}
	tree := e.stakeTree(sp.atSize)
	var b strings.Builder
	arr := func(name string, f func(i int) string) {
		fmt.Fprintf(&b, "%s = [%s, %s]\n", name, f(0), f(1))
	}
	path := func(n *snote) string {
		var sib [merkle.Depth]fr.Element
		if n.amount > 0 {
			var err error
			sib, err = tree.Path(n.pos)
			require.NoError(e.t, err)
		}
		return tomlPath(sib)
	}
	fmt.Fprintf(&b, "nk = %s\n", tomlQ(e.w.nk))
	arr("in_amount", func(i int) string { return tomlU(ins[i].amount) })
	arr("in_rho", func(i int) string { return tomlQ(ins[i].rho) })
	arr("in_rcm", func(i int) string { return tomlQ(ins[i].rcm) })
	arr("in_pos", func(i int) string {
		if ins[i].amount == 0 {
			return tomlU(0)
		}
		return tomlU(ins[i].pos)
	})
	arr("in_path", func(i int) string { return path(ins[i]) })
	arr("in_move_key", func(i int) string { return tomlQ(ins[i].moveKey) })
	arr("in_move_time", func(i int) string { return tomlU(ins[i].moveTime) })
	arr("in_exposed", func(i int) string { return tomlU(ins[i].exposed) })
	out := sp.out
	if out == nil {
		out = sp.zeroOut
	}
	fmt.Fprintf(&b, "out_amount = %s\nout_rho = %s\nout_rcm = %s\nclear = %t\n", tomlU(out.amount), tomlQ(out.rho), tomlQ(out.rcm), sp.clear)
	var w debt.Witness
	if sp.debtW != nil {
		w = *sp.debtW
	}
	fmt.Fprintf(&b, "debt_low_key = %s\ndebt_low_next_key = %s\ndebt_low_next_index = %s\ndebt_low_retained = %s\ndebt_low_index = %s\ndebt_low_path = %s\n",
		tomlQ(w.Low.Key), tomlQ(w.Low.NextKey), tomlU(w.Low.NextIndex), tomlU(w.Low.Retained), tomlU(w.Index), tomlPath(w.Path))
	crIn, crOut := &snote{rho: ssDet("stake-dummy", 5), rcm: ssDet("stake-dummy", 6)}, &snote{rho: ssDet("stake-dummy", 7), rcm: ssDet("stake-dummy", 8)}
	if c := sp.credit; c != nil {
		if c.in != nil {
			crIn = c.in
		} else {
			crIn = c.pad
		}
		crOut = c.out
	}
	crPos := uint64(0)
	if crIn.amount > 0 {
		crPos = crIn.pos
	}
	fmt.Fprintf(&b, "cr_in_amount = %s\ncr_in_rho = %s\ncr_in_rcm = %s\ncr_in_pos = %s\ncr_in_path = %s\ncr_out_rho = %s\ncr_out_rcm = %s\n",
		tomlU(crIn.amount), tomlQ(crIn.rho), tomlQ(crIn.rcm), tomlU(crPos), path(crIn), tomlQ(crOut.rho), tomlQ(crOut.rcm))
	names := []string{"anchor", "asset", "nf_0", "nf_1", "cm_out", "v_in", "v_out", "clear_before", "debt_root",
		"cr_asset", "cr_nf", "cr_cm", "cr_v_in", "cr_move_time",
		"gw_0", "gw_1", "cr_gw", "gw_out", "w_out", "cr_gw_out", "cr_w_out", "p_key", "p_time", "p_ex", "sighash"}
	ints := map[string]bool{"v_in": true, "v_out": true, "clear_before": true, "cr_v_in": true, "cr_move_time": true,
		"w_out": true, "cr_w_out": true, "p_time": true, "p_ex": true}
	for i, n := range names {
		if ints[n] {
			var v fr.Element
			_ = v.SetBytes(pub[i])
			fmt.Fprintf(&b, "%s = \"%s\"\n", n, v.String())
			continue
		}
		fmt.Fprintf(&b, "%s = \"0x%x\"\n", n, pub[i])
	}
	return b.String(), pub
}

// stakeLanes is what the chain supplies for msg.
func (e *stakeEnv) stakeLanes(msg sstypes.StakeMsg) sstypes.StakeLanes { return msg.StakeLanes() }

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
	toml, pub := e.stakeWitness(msg, sp, e.stakeLanes(msg), sighash)
	return e.stakeProver().TryProve(toml, pub)
}

// settleStake marks sp executed: inputs spent, outputs tracked.
func (e *stakeEnv) settleStake(sp *stakePlan) {
	for _, n := range sp.ins {
		n.spent = true
	}
	e.trackStake(sp.out)
	if sp.credit != nil {
		if sp.credit.in != nil {
			sp.credit.in.spent = true
		}
		e.trackStake(sp.credit.out)
	}
	e.scanStake()
}

// Audit 7, B L-1 / A7-L3: a stake proof whose clear_before was non-zero only
// when it cleared a label marked the tx as clearing one, which the public
// redelegations into that validator could link to its owner. Now
// every stake proof must name the label window's current clear_before (the
// block time less the window, within ClearBeforeSlackSeconds) and the
// current debt root, whether it clears anything or not.
func TestStakeNotesNameClearBefore(t *testing.T) {
	e := initStakeEnv(t)
	v, _ := e.createValidator(1000 * ssErth)
	e.next(5 * time.Second)
	e.shield(uint64(1_000 * ssErth))
	e.shield(uint64(1_000 * ssErth))
	k := e.app.ShieldedStakingKeeper
	cb, err := k.ClearBefore(e.ctx())
	require.NotZero(t, cb)
	in := e.w.unspent("uerth", uint64(200*ssErth))
	require.NotNil(t, in)
	m, p, sp := e.delegateMsg(v, in, uint64(200*ssErth))
	require.Equal(t, cb, m.Stake.ClearBefore, "a proof that clears nothing names it too")
	root, _, err := k.DebtRoot(e.ctx())
	require.NoError(t, err)
	require.Equal(t, root, m.Stake.DebtRoot)
	zero := make([]byte, 32)
	for _, bad := range []struct {
		cb   uint64
		root []byte
		why  string
	}{
		{0, zero, "clear_before"}, // the old encoding of "clears nothing"
		{cb + 1_000, root, "clear_before"},
		{cb - sstypes.ClearBeforeSlackSeconds - 1, root, "clear_before"},
		{cb, privacy.FieldBytes(ssDet("stale-debt-root", 0)), "debt root"},
	} {
		mb := *m
		mb.Stake.ClearBefore, mb.Stake.DebtRoot = bad.cb, bad.root
		res := e.run(e.privateTx(&mb))
		require.NotEqual(t, uint32(0), res.Code, "clear_before %d", bad.cb)
		require.Contains(t, res.Log, bad.why)
	}
	res := e.run(e.privateTx(m))
	require.Equal(t, uint32(0), res.Code, res.Log)
	e.settle(p)
	e.settleStake(sp)
	e.invariants()
}

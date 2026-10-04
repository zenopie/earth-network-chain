// The stake vote side of the private staking harness: a stake note's vote on
// a proposal without spending it (circuits/vote), proven against the
// proposal's snapshot roots. The wallet rebuilds the stake nullifier indexed
// tree from the chain's insertion-ordered stream (Query/StakeNullifierTree)
// up to the snapshot's nf_size, as a real wallet does, and takes its note's
// low-leaf witness there. Proofs are cached beside the stake proofs
// (vote-<hash>.proof).

package app

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"cosmossdk.io/collections"
	"cosmossdk.io/log"
	"cosmossdk.io/math"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	dbm "github.com/cosmos/cosmos-db"
	"github.com/cosmos/cosmos-sdk/baseapp"
	"github.com/cosmos/cosmos-sdk/client/flags"
	simtestutil "github.com/cosmos/cosmos-sdk/testutil/sims"
	v1 "github.com/cosmos/cosmos-sdk/x/gov/types/v1"
	"github.com/stretchr/testify/require"

	shieldedtest "github.com/earth-network/earth/x/shielded/testutil"
	shieldedtypes "github.com/earth-network/earth/x/shielded/types"
	sskeeper "github.com/earth-network/earth/x/shieldedstaking/keeper"
	sstypes "github.com/earth-network/earth/x/shieldedstaking/types"
	"github.com/earth-network/earth/zk/debt"
	"github.com/earth-network/earth/zk/indexed"
	"github.com/earth-network/earth/zk/merkle"
	"github.com/earth-network/earth/zk/privacy"
)

// nullifierStream is every stake nullifier in insertion order, paged from
// the chain (leaf 1, 2, ...).
func (e *stakeEnv) nullifierStream() [][]byte {
	e.t.Helper()
	q := sskeeper.NewQueryServerImpl(e.app.ShieldedStakingKeeper)
	var out [][]byte
	for {
		res, err := q.StakeNullifierTree(e.ctx(), &sstypes.QueryStakeNullifierTreeRequest{Start: uint64(len(out)), Limit: 7})
		require.NoError(e.t, err)
		if len(res.Values) == 0 {
			if res.Size_ > 0 {
				require.Equal(e.t, res.Size_, uint64(len(out))+1)
			}
			return out
		}
		out = append(out, res.Values...)
	}
}

// snapshotNfTree is the stake nullifier tree as the proposal's snapshot saw
// it: the first nf_size-1 nullifiers of the stream, re-inserted in order.
func (e *stakeEnv) snapshotNfTree(snap sstypes.ProposalSnapshot) *indexed.Tree {
	e.t.Helper()
	stream := e.nullifierStream()
	n := uint64(0)
	if snap.NfSize > 0 {
		n = snap.NfSize - 1
	}
	require.LessOrEqual(e.t, n, uint64(len(stream)))
	vals := make([]fr.Element, n)
	for i := range vals {
		v, err := privacy.FieldFromBytes(stream[i])
		require.NoError(e.t, err)
		vals[i] = v
	}
	t, err := indexed.Rebuild(vals)
	require.NoError(e.t, err)
	r, err := t.Root()
	require.NoError(e.t, err)
	require.Equal(e.t, snap.NfRoot, privacy.FieldBytes(r), "the rebuilt nullifier tree is the snapshot's")
	return t
}

func (e *stakeEnv) vnf(n *snote, proposalID uint64) []byte {
	return privacy.FieldBytes(privacy.VoteNF(e.w.nk, n.rho, uint32(n.pos), proposalID))
}

// votePlan is a vote proof's witness: per used slot, the note, its path
// under the snapshot's note root, its low leaf under the snapshot's
// nullifier root, and (a labelled note) its debt witness under the current
// debt root.
type votePlan struct {
	notes    []*snote
	paths    [][merkle.Depth]fr.Element
	lows     []indexed.Witness
	debts    []debt.Witness
	debtRoot []byte
	snap     sstypes.ProposalSnapshot
}

// votePlanFor lays out the vote witness of ns (1..MaxVoteNotes notes of one
// validator) on proposalID. A note the snapshot cannot prove (spent before
// it, or minted after) gets the best witness a cheat has: the stale low
// leaf, or a path in the current tree.
func (e *stakeEnv) votePlanFor(ns []*snote, proposalID uint64) *votePlan {
	e.t.Helper()
	require.NotEmpty(e.t, ns)
	require.LessOrEqual(e.t, len(ns), sstypes.MaxVoteNotes)
	e.scanStake()
	snap, err := e.app.ShieldedStakingKeeper.Snapshots.Get(e.ctx(), proposalID)
	require.NoError(e.t, err)
	vp := &votePlan{notes: ns, snap: snap}
	nft := e.snapshotNfTree(snap)
	dt := e.debtTree()
	dr, err := dt.Root()
	require.NoError(e.t, err)
	vp.debtRoot = privacy.FieldBytes(dr)
	for _, n := range ns {
		size := snap.TreeSize
		if n.pos >= size {
			size = uint64(len(e.sw.leaves)) // minted after the snapshot: today's tree
		}
		path, err := e.stakeTree(size).Path(n.pos)
		require.NoError(e.t, err)
		nf := e.snf(n)
		low, err := nft.NonMembership(nf)
		if err != nil {
			// Spent before the snapshot: take the low leaf as it was before nf
			// went in (its root is not the snapshot's).
			require.ErrorIs(e.t, err, indexed.ErrExists)
			stream := e.nullifierStream()
			var before []fr.Element
			for _, b := range stream {
				v, _ := privacy.FieldFromBytes(b)
				if v == nf {
					break
				}
				before = append(before, v)
			}
			old, err := indexed.Rebuild(before)
			require.NoError(e.t, err)
			low, err = old.NonMembership(nf)
			require.NoError(e.t, err)
		}
		vp.paths = append(vp.paths, path)
		vp.lows = append(vp.lows, low)
		var dw debt.Witness
		if n.labelled() {
			dw, err = dt.Lookup(n.moveKey)
			require.NoError(e.t, err)
		}
		vp.debts = append(vp.debts, dw)
	}
	return vp
}

// voteWitness is vp's Prover.toml for msg under sighash, and the public
// inputs. Unused slots: amount 0, everything else zero.
func (e *stakeEnv) voteWitness(m *sstypes.MsgStakeVote, vp *votePlan, sighash fr.Element) (string, [][]byte) {
	pub := m.VotePublicInputs(vp.snap.Root, vp.snap.NfRoot, sighash)
	const n = sstypes.MaxVoteNotes
	var zero [merkle.Depth]fr.Element
	var b strings.Builder
	arr := func(name string, f func(i int) string) {
		xs := make([]string, n)
		for i := range xs {
			xs[i] = f(i)
		}
		fmt.Fprintf(&b, "%s = [%s]\n", name, strings.Join(xs, ", "))
	}
	used := func(i int) bool { return i < len(vp.notes) }
	note := func(i int) *snote {
		if used(i) {
			return vp.notes[i]
		}
		return &snote{}
	}
	fmt.Fprintf(&b, "nk = %s\n", tomlQ(e.w.nk))
	arr("amount", func(i int) string { return tomlU(note(i).amount) })
	arr("rho", func(i int) string { return tomlQ(note(i).rho) })
	arr("rcm", func(i int) string { return tomlQ(note(i).rcm) })
	arr("pos", func(i int) string { return tomlU(note(i).pos) })
	arr("path", func(i int) string {
		if used(i) {
			return tomlPath(vp.paths[i])
		}
		return tomlPath(zero)
	})
	arr("move_key", func(i int) string { return tomlQ(note(i).moveKey) })
	arr("move_time", func(i int) string { return tomlU(note(i).moveTime) })
	arr("exposed", func(i int) string { return tomlU(note(i).exposed) })
	low := func(i int) indexed.Witness {
		if used(i) {
			return vp.lows[i]
		}
		return indexed.Witness{}
	}
	arr("low_value", func(i int) string { return tomlQ(low(i).Low.Value) })
	arr("low_next_value", func(i int) string { return tomlQ(low(i).Low.NextValue) })
	arr("low_next_index", func(i int) string { return tomlU(low(i).Low.NextIndex) })
	arr("low_index", func(i int) string { return tomlU(low(i).Index) })
	arr("low_path", func(i int) string { return tomlPath(low(i).Path) })
	dw := func(i int) debt.Witness {
		if used(i) {
			return vp.debts[i]
		}
		return debt.Witness{}
	}
	arr("debt_low_key", func(i int) string { return tomlQ(dw(i).Low.Key) })
	arr("debt_low_next_key", func(i int) string { return tomlQ(dw(i).Low.NextKey) })
	arr("debt_low_next_index", func(i int) string { return tomlU(dw(i).Low.NextIndex) })
	arr("debt_low_retained", func(i int) string { return tomlU(dw(i).Low.Retained) })
	arr("debt_low_index", func(i int) string { return tomlU(dw(i).Index) })
	arr("debt_low_path", func(i int) string { return tomlPath(dw(i).Path) })
	fmt.Fprintf(&b, "note_root = \"0x%x\"\nnf_root = \"0x%x\"\ndebt_root = \"0x%x\"\nasset = \"0x%x\"\nweight = \"%d\"\nproposal_id = \"%d\"\n",
		pub[0], pub[1], pub[2], pub[3], m.Weight, m.ProposalId)
	arr("vnf", func(i int) string { return fmt.Sprintf(`"0x%x"`, pub[6+i]) })
	fmt.Fprintf(&b, "sighash = \"0x%x\"\n", pub[6+n])
	return b.String(), pub
}

func (e *stakeEnv) voteProver() *shieldedtest.Prover {
	pr := shieldedtest.ForDir(e.t, e.proofDir, "scripts/staking-fixtures.sh")
	pr.Circuit, pr.VK = "vote", "../x/shieldedstaking/testdata/vote.vk"
	return pr
}

// tryProveVote proves m's vote proof from vp (every other field of m final):
// shieldedtest.ErrWitnessRefused when the circuit refuses the witness.
func (e *stakeEnv) tryProveVote(m *sstypes.MsgStakeVote, vp *votePlan) ([]byte, error) {
	sighash, err := shieldedtypes.Sighash(m, ssChainID, ssTx, e.app.AuthKeeper.AddressCodec())
	require.NoError(e.t, err)
	toml, pub := e.voteWitness(m, vp, sighash)
	return e.voteProver().TryProve(toml, pub)
}

// voteNullifiers is the MaxVoteNotes vote nullifier slots of ns on
// proposalID: theirs first, then zeros.
func (e *stakeEnv) voteNullifiers(ns []*snote, proposalID uint64) [][]byte {
	out := make([][]byte, sstypes.MaxVoteNotes)
	for i := range out {
		out[i] = make([]byte, 32)
		if i < len(ns) {
			out[i] = e.vnf(ns[i], proposalID)
		}
	}
	return out
}

// stakeVoteNotesMsg is the vote of ns (one validator's) on proposalID with
// weight (0: their sum, rounded down to three significant digits, as every
// wallet does), its fee bundle paid from any current ERTH note. With prove,
// its bundle and vote proof are proven (failing the test if the circuit
// refuses).
func (e *stakeEnv) stakeVoteNotesMsg(ns []*snote, proposalID uint64, opts []*v1.WeightedVoteOption, weight uint64, prove bool) (*sstypes.MsgStakeVote, *pendingBundle, *votePlan) {
	e.t.Helper()
	v, ok := sstypes.ParseDerthDenom(ns[0].denom)
	require.True(e.t, ok)
	if weight == 0 {
		var sum uint64
		for _, n := range ns {
			require.Equal(e.t, ns[0].denom, n.denom)
			sum += e.clearedValue(n) // a labelled note votes its value now
		}
		weight = sstypes.RoundVoteWeight(sum)
	}
	vp := e.votePlanFor(ns, proposalID)
	fee := e.feeOnly()
	m := &sstypes.MsgStakeVote{Bundle: fee.b, ProposalId: proposalID, Validator: v, Options: opts,
		Weight: weight, VoteNullifiers: e.voteNullifiers(ns, proposalID), DebtRoot: vp.debtRoot}
	if !prove {
		unproven(m)
		return m, fee, vp
	}
	e.prove(m, fee)
	proof, err := e.tryProveVote(m, vp)
	require.NoError(e.t, err)
	m.Proof = proof
	return m, fee, vp
}

// stakeVoteMsg is stakeVoteNotesMsg for the one note n.
func (e *stakeEnv) stakeVoteMsg(n *snote, proposalID uint64, opts []*v1.WeightedVoteOption, weight uint64, prove bool) (*sstypes.MsgStakeVote, *pendingBundle, *votePlan) {
	return e.stakeVoteNotesMsg([]*snote{n}, proposalID, opts, weight, prove)
}

// stakeVoteNotes votes all of ns on proposalID in one msg; the notes stay
// unspent. Returns the msg.
func (e *stakeEnv) stakeVoteNotes(ns []*snote, proposalID uint64, opt v1.VoteOption) *sstypes.MsgStakeVote {
	e.t.Helper()
	m, p, _ := e.stakeVoteNotesMsg(ns, proposalID, v1.NewNonSplitVoteOption(opt), 0, true)
	res := e.run(e.privateTx(m))
	require.Equal(e.t, uint32(0), res.Code, res.Log)
	e.settle(p)
	require.Empty(e.t, eventsOf(res.Events, sstypes.EventTypeStakeNullifier), "a vote spends no stake note")
	require.Empty(e.t, eventsOf(res.Events, sstypes.EventTypeStakeNote), "a vote mints no stake note")
	ev := eventsOf(res.Events, sstypes.EventTypeStakeVote)
	require.Len(e.t, ev, 1)
	hs := make([]string, len(ns))
	for i, b := range m.UsedVoteNullifiers() {
		hs[i] = fmt.Sprintf("%x", b)
	}
	require.Equal(e.t, strings.Join(hs, ","), ev[0][sstypes.AttributeKeyVoteNFs])
	require.Equal(e.t, fmt.Sprint(m.Weight), ev[0][sstypes.AttributeKeyDerth], "one weight for the vote")
	return m
}

// stakeVote votes all of n on proposalID; the note stays unspent.
func (e *stakeEnv) stakeVote(n *snote, proposalID uint64, opt v1.VoteOption) {
	e.t.Helper()
	e.stakeVoteNotes([]*snote{n}, proposalID, opt)
}

// requireRefused: the circuit refused a witness (or, without the circuits at
// hand, there is no cached proof for it: a refused witness leaves none).
func requireRefused(t *testing.T, err error) {
	t.Helper()
	if shieldedtest.Circuits() != "" {
		require.ErrorIs(t, err, shieldedtest.ErrWitnessRefused)
	} else {
		require.ErrorIs(t, err, shieldedtest.ErrNoCircuits)
	}
}

// restake merges ns (one or two notes of one validator) into one note,
// clearing a labelled one's label when clear.
func (e *stakeEnv) restake(ns []*snote, clear bool) *snote {
	e.t.Helper()
	v, ok := sstypes.ParseDerthDenom(ns[0].denom)
	require.True(e.t, ok)
	var amount uint64
	for _, n := range ns {
		if clear {
			amount += e.clearedValue(n)
		} else {
			amount += n.amount
		}
	}
	out := e.freshStake(ns[0].denom, amount)
	p := e.feeOnly()
	sp := e.stake(&stakePlan{denom: ns[0].denom, ins: ns, out: out, clear: clear})
	m := &sstypes.MsgRestake{Bundle: p.b, Validator: v, Stake: sp.proof}
	e.prove(m, p)
	e.proveStake(m, sp)
	res := e.run(e.privateTx(m))
	require.Equal(e.t, uint32(0), res.Code, res.Log)
	e.settle(p)
	e.settleStake(sp)
	require.True(e.t, out.known)
	return out
}

// One stake note votes on two concurrently open proposals (the decoy
// proposal attack on spend-to-vote: voting on the decoy no longer burns the
// note's vote on the real one). Its votes are unlinkable (two vote
// nullifiers, no spend nullifier), the note stays spendable, a second vote
// on one proposal is refused, a note spent before the snapshot cannot vote,
// one spent after it still can (its outputs cannot), and the tallies count
// each vote once. The vote nullifiers and the indexed nullifier tree cross a
// genesis.
func TestStakeVoteConcurrentProposals(t *testing.T) {
	e := initStakeEnv(t)
	vB, _ := e.createValidator(1000 * ssErth)
	e.next(5 * time.Second)
	e.shield(uint64(5_000 * ssErth))
	e.shield(uint64(200 * ssErth))
	e.shield(uint64(200 * ssErth))
	// Three notes at one validator (a wallet that did not merge: two
	// devices, or before the one-note rule).
	n := e.delegateWith(vB, uint64(1_000*ssErth), true)
	k := e.delegateWith(vB, uint64(600*ssErth), true)
	m := e.delegateWith(vB, uint64(300*ssErth), true)
	// m is spent before voting begins.
	m1 := e.restake([]*snote{m}, false)
	e.next(5 * time.Second)

	prop1 := e.submitProposal()
	prop2 := e.submitProposal()
	snaps := map[uint64]sstypes.ProposalSnapshot{}
	for _, p := range []uint64{prop1, prop2} {
		s, err := e.app.ShieldedStakingKeeper.Snapshots.Get(e.ctx(), p)
		require.NoError(t, err)
		require.NotEmpty(t, s.NfRoot)
		require.Equal(t, uint64(5), s.NfSize, "the sentinel, the three delegations' padding nullifiers and m's")
		snaps[p] = s
	}
	require.Equal(t, snaps[prop1].NfRoot, snaps[prop2].NfRoot)

	// n votes Yes on one and No on the other: both count.
	e.stakeVote(n, prop1, v1.OptionYes)
	e.stakeVote(n, prop2, v1.OptionNo)
	nf := privacy.FieldBytes(e.snf(n))
	require.NotEqual(t, e.vnf(n, prop1), e.vnf(n, prop2), "unlinkable: one vote nullifier per proposal")
	require.NotEqual(t, nf, e.vnf(n, prop1))
	require.NotEqual(t, nf, e.vnf(n, prop2))
	spent, err := e.app.ShieldedStakingKeeper.StakeNullifiers.Has(e.ctx(), nf)
	require.NoError(t, err)
	require.False(t, spent, "voting spends nothing")

	// Again on prop1: refused, whatever the option.
	again, _, _ := e.stakeVoteMsg(n, prop1, v1.NewNonSplitVoteOption(v1.OptionNo), 0, false)
	res := e.checkTx(e.privateTx(again))
	require.Equal(t, sstypes.ErrVoteNullifierUsed.ABCICode(), res.Code, res.Log)
	// A fresh (forged) vote nullifier for n on prop1: the proof does not
	// verify.
	forged, fp, _ := e.stakeVoteMsg(n, prop1, v1.NewNonSplitVoteOption(v1.OptionNo), 0, false)
	forged.VoteNullifiers[0] = privacy.FieldBytes(ssDet("forged-vnf", 0))
	forgedProof := *forged
	e.prove(&forgedProof, fp)
	forgedProof.Proof = make([]byte, shieldedtypes.ProofBytes)
	res = e.checkTx(e.privateTx(&forgedProof))
	require.Equal(t, sstypes.ErrInvalidStakeProof.ABCICode(), res.Code, res.Log)

	// m was spent before the snapshots: its nullifier is under nf_root, so
	// no low leaf proves it absent.
	sm, _, vpm := e.stakeVoteMsg(m, prop1, v1.NewNonSplitVoteOption(v1.OptionYes), 0, false)
	_, err = e.tryProveVote(sm, vpm)
	requireRefused(t, err)
	// m's outputs came before the snapshot too: they vote.
	e.stakeVote(m1, prop1, v1.OptionNo)

	// k is restaked after the snapshots: k still votes (its nullifier went
	// in after nf_root), its outputs cannot (not under the note root).
	k1 := e.restake([]*snote{k}, false)
	sk1, _, vpk1 := e.stakeVoteMsg(k1, prop1, v1.NewNonSplitVoteOption(v1.OptionYes), 0, false)
	_, err = e.tryProveVote(sk1, vpk1)
	requireRefused(t, err)
	e.stakeVote(k, prop1, v1.OptionAbstain)

	// n is still an ordinary note: it undelegates.
	e.undelegate(vB, n, uint64(10*ssErth))

	// The tallies: prop1 Yes n, No m1, Abstain k; prop2 No n.
	tally := func(p uint64) sstypes.VoteTally {
		tl, err := e.app.ShieldedStakingKeeper.Tallies.Get(e.ctx(), collections.Join(p, e.valoper(vB)))
		require.NoError(t, err)
		return tl
	}
	dec := func(x uint64) math.LegacyDec {
		return math.LegacyNewDecFromInt(math.NewIntFromUint64(sstypes.RoundVoteWeight(x)))
	}
	t1 := tally(prop1)
	require.Equal(t, dec(n.amount), t1.Yes)
	require.Equal(t, dec(m1.amount), t1.No)
	require.Equal(t, dec(k.amount), t1.Abstain)
	require.True(t, t1.NoWithVeto.IsZero())
	t2 := tally(prop2)
	require.Equal(t, dec(n.amount), t2.No)
	require.True(t, t2.Yes.Add(t2.Abstain).Add(t2.NoWithVeto).IsZero())
	require.Equal(t, 3, countVotes(t, e, prop1))
	require.Equal(t, 1, countVotes(t, e, prop2))
	e.invariants()

	// Genesis round trip mid-vote: the nullifier tree rebuilds to the same
	// roots (snapshot nf roots checked), and the vote nullifiers still bar a
	// second vote.
	exported, err := e.app.ExportAppStateAndValidators(false, nil, nil)
	require.NoError(t, err)
	var appState map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(exported.AppState, &appState))
	fresh := New(log.NewNopLogger(), dbm.NewMemDB(), nil, true, simtestutil.AppOptionsMap{flags.FlagHome: t.TempDir()},
		baseapp.SetChainID(ssChainID))
	fctx := fresh.NewUncachedContext(false, cmtproto.Header{ChainID: ssChainID, Height: e.height, Time: e.now})
	_, err = fresh.ModuleManager.InitGenesis(fctx, fresh.AppCodec(), appState)
	require.NoError(t, err)
	gs1, err := e.app.ShieldedStakingKeeper.ExportGenesis(e.ctx())
	require.NoError(t, err)
	gs2, err := fresh.ShieldedStakingKeeper.ExportGenesis(fctx)
	require.NoError(t, err)
	require.Equal(t, gs1, gs2)
	size1, root1, latest1, err := e.app.ShieldedStakingKeeper.StakeNullifierTree(e.ctx())
	require.NoError(t, err)
	size2, root2, latest2, err := fresh.ShieldedStakingKeeper.StakeNullifierTree(fctx)
	require.NoError(t, err)
	require.Equal(t, []any{size1, root1, latest1}, []any{size2, root2, latest2})
	used, err := fresh.ShieldedStakingKeeper.UsedVoteNullifiers.Has(fctx, collections.Join(prop1, e.vnf(n, prop1)))
	require.NoError(t, err)
	require.True(t, used)
	// A snapshot whose nf root the nullifiers do not rebuild is refused.
	bad := *gs1
	bad.Snapshots = append([]sstypes.ProposalSnapshot(nil), gs1.Snapshots...)
	bad.Snapshots[0].NfRoot = privacy.FieldBytes(ssDet("bad-nf-root", 0))
	bz, err := fresh.AppCodec().MarshalJSON(&bad)
	require.NoError(t, err)
	appState[sstypes.ModuleName] = bz
	fresh2 := New(log.NewNopLogger(), dbm.NewMemDB(), nil, true, simtestutil.AppOptionsMap{flags.FlagHome: t.TempDir()},
		baseapp.SetChainID(ssChainID))
	fctx2 := fresh2.NewUncachedContext(false, cmtproto.Header{ChainID: ssChainID, Height: e.height, Time: e.now})
	err = func() (err error) {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("%v", r)
			}
		}()
		_, err = fresh2.ModuleManager.InitGenesis(fctx2, fresh2.AppCodec(), appState)
		return err
	}()
	require.ErrorContains(t, err, "stake nullifier root")
}

// One vote per person: up to two of an owner's stake notes at a validator
// vote in one msg with ONE public weight (their sum rounded down to three
// significant digits), each note's vote nullifier recorded. With one note
// per validator a vote uses one slot; the second covers a note made beside
// a labelled one (or a wallet that did not merge). A note already in a vote
// is refused in another (even beside a fresh note); a third note votes in a
// second msg; the tally counts each weight once; the shape rules hold before
// any proof is read.
func TestStakeVoteManyNotesOneWeight(t *testing.T) {
	e := initStakeEnv(t)
	vB, _ := e.createValidator(1000 * ssErth)
	e.next(5 * time.Second)
	e.shield(uint64(5_000 * ssErth))
	e.shield(uint64(200 * ssErth))
	var ns []*snote
	for i, amt := range []int64{123, 456, 789} {
		ns = append(ns, e.delegateWith(vB, uint64(amt*ssErth+int64(i)*7_777), true))
	}
	e.next(5 * time.Second)
	prop := e.submitProposal()

	// The shape, before any proof: exactly two slots, used ones first,
	// distinct; three notes do not fit.
	m, _, _ := e.stakeVoteNotesMsg(ns[:2], prop, v1.NewNonSplitVoteOption(v1.OptionYes), 0, false)
	z := make([]byte, 32)
	for name, vnfs := range map[string][][]byte{
		"one slot":      m.VoteNullifiers[:1],
		"three slots":   append(append([][]byte{}, m.VoteNullifiers...), z),
		"gap":           {z, m.VoteNullifiers[1]},
		"none used":     {z, z},
		"repeated note": {m.VoteNullifiers[0], m.VoteNullifiers[0]},
	} {
		bad := *m
		bad.VoteNullifiers = vnfs
		require.Error(t, bad.ValidateBasic(), name)
	}
	require.NoError(t, m.ValidateBasic())
	noRoot := *m
	noRoot.DebtRoot = nil
	require.Error(t, noRoot.ValidateBasic(), "a vote names the debt root")

	// Two notes, one msg, one weight.
	sum2 := ns[0].amount + ns[1].amount
	v2 := e.stakeVoteNotes(ns[:2], prop, v1.OptionYes)
	require.Equal(t, sstypes.RoundVoteWeight(sum2), v2.Weight)
	require.Less(t, v2.Weight, sum2, "the exact sum is not published")
	for _, n := range ns[:2] {
		used, err := e.app.ShieldedStakingKeeper.UsedVoteNullifiers.Has(e.ctx(), collections.Join(prop, e.vnf(n, prop)))
		require.NoError(t, err)
		require.True(t, used)
	}
	require.Equal(t, 1, countVotes(t, e, prop))

	// A note that voted cannot vote again, not even beside a fresh one.
	again, _, _ := e.stakeVoteNotesMsg([]*snote{ns[2], ns[1]}, prop, v1.NewNonSplitVoteOption(v1.OptionNo), 0, false)
	res := e.checkTx(e.privateTx(again))
	require.Equal(t, sstypes.ErrVoteNullifierUsed.ABCICode(), res.Code, res.Log)
	// A stale debt root is refused before any proof is read.
	stale, _, _ := e.stakeVoteMsg(ns[2], prop, v1.NewNonSplitVoteOption(v1.OptionNo), 0, false)
	stale.DebtRoot = privacy.FieldBytes(ssDet("stale-debt-root", 0))
	res = e.checkTx(e.privateTx(stale))
	require.Equal(t, sstypes.ErrStakeTree.ABCICode(), res.Code, res.Log)
	require.Contains(t, res.Log, "debt root")

	// The third note votes in a second msg (its own weight).
	e.stakeVote(ns[2], prop, v1.OptionNo)
	require.Equal(t, 2, countVotes(t, e, prop))

	tl, err := e.app.ShieldedStakingKeeper.Tallies.Get(e.ctx(), collections.Join(prop, e.valoper(vB)))
	require.NoError(t, err)
	require.Equal(t, math.LegacyNewDecFromInt(math.NewIntFromUint64(v2.Weight)), tl.Yes)
	require.Equal(t, math.LegacyNewDecFromInt(math.NewIntFromUint64(sstypes.RoundVoteWeight(ns[2].amount))), tl.No)
	e.invariants()

	// Genesis carries each note vote with its vote nullifiers, keyed by the first.
	gs, err := e.app.ShieldedStakingKeeper.ExportGenesis(e.ctx())
	require.NoError(t, err)
	require.NoError(t, gs.Validate())
	noteVotes := 0
	for _, v := range gs.Votes {
		if !v.Position {
			noteVotes++
			require.Equal(t, v.Key[1:], v.VoteNullifiers[0])
		}
	}
	require.Equal(t, 2, noteVotes)
	// A genesis whose votes share a vote nullifier is refused.
	bad := *gs
	bad.Votes = append([]sstypes.StakeVote(nil), gs.Votes...)
	for i := range bad.Votes {
		if len(bad.Votes[i].VoteNullifiers) == 1 {
			v := bad.Votes[i]
			v.VoteNullifiers = [][]byte{v.VoteNullifiers[0], e.vnf(ns[0], prop)}
			bad.Votes[i] = v
		}
	}
	require.ErrorContains(t, bad.Validate(), "used twice")
}

// Audit 6 C-L4: when the last end-of-block recording of the stake roots
// failed, a proposal entering voting takes no roots (no note votes; a stale
// nf root would let a note spent into a position since vote twice), and the
// next successful recording clears the condition.
func TestAudit6SnapshotSkipsStaleRoots(t *testing.T) {
	e := initStakeEnv(t)
	k := e.app.ShieldedStakingKeeper

	fresh := e.submitProposal()
	snap, err := k.Snapshots.Get(e.ctx(), fresh)
	require.NoError(t, err)
	require.NotEmpty(t, snap.NfRoot, "a healthy snapshot has its roots")

	require.NoError(t, k.RootsStale.Set(e.ctx(), true)) // as a failed recording leaves it
	stale := e.submitProposal()
	snap, err = k.Snapshots.Get(e.ctx(), stale)
	require.NoError(t, err)
	require.Empty(t, snap.Root)
	require.Empty(t, snap.NfRoot)
	has, err := k.RootsStale.Has(e.ctx())
	require.NoError(t, err)
	require.False(t, has, "the block's own recording succeeded and cleared it")

	again := e.submitProposal()
	snap, err = k.Snapshots.Get(e.ctx(), again)
	require.NoError(t, err)
	require.NotEmpty(t, snap.NfRoot)
}

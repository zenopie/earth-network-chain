package app

// The stake vote side of the private staking harness: a stake note's vote on
// a proposal without spending it (circuits/vote), proven against the
// proposal's snapshot roots. The wallet rebuilds the stake nullifier indexed
// tree from the chain's insertion-ordered stream (Query/StakeNullifierTree)
// up to the snapshot's nf_size, as a real wallet does, and takes its note's
// low-leaf witness there. Proofs are cached beside the stake proofs
// (vote-<hash>.proof).

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
	dbm "github.com/cosmos/cosmos-db"
	"github.com/cosmos/cosmos-sdk/baseapp"
	"github.com/cosmos/cosmos-sdk/client/flags"
	simtestutil "github.com/cosmos/cosmos-sdk/testutil/sims"

	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	v1 "github.com/cosmos/cosmos-sdk/x/gov/types/v1"
	"github.com/stretchr/testify/require"

	shieldedtest "github.com/earth-network/earth/x/shielded/testutil"
	shieldedtypes "github.com/earth-network/earth/x/shielded/types"
	sskeeper "github.com/earth-network/earth/x/shieldedstaking/keeper"
	sstypes "github.com/earth-network/earth/x/shieldedstaking/types"
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

// votePlan is a vote proof's witness: the note, its path under the
// snapshot's note root, and its low leaf under the snapshot's nullifier root.
type votePlan struct {
	n    *snote
	path [merkle.Depth]fr.Element
	low  indexed.Witness
	snap sstypes.ProposalSnapshot
}

// votePlanFor lays out n's vote witness on proposalID. A note the snapshot
// cannot prove (spent before it, or minted after) gets the best witness a
// cheat has: the stale low leaf, or a path in the current tree.
func (e *stakeEnv) votePlanFor(n *snote, proposalID uint64) *votePlan {
	e.t.Helper()
	e.scanStake()
	snap, err := e.app.ShieldedStakingKeeper.Snapshots.Get(e.ctx(), proposalID)
	require.NoError(e.t, err)
	vp := &votePlan{n: n, snap: snap}
	size := snap.TreeSize
	if n.pos >= size {
		size = uint64(len(e.sw.leaves)) // minted after the snapshot: today's tree
	}
	vp.path, err = e.stakeTree(size).Path(n.pos)
	require.NoError(e.t, err)
	nft := e.snapshotNfTree(snap)
	nf := e.snf(n)
	vp.low, err = nft.NonMembership(nf)
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
		vp.low, err = old.NonMembership(nf)
		require.NoError(e.t, err)
	}
	return vp
}

// voteWitness is vp's Prover.toml for msg under sighash, and the public
// inputs.
func (e *stakeEnv) voteWitness(m *sstypes.MsgStakeVote, vp *votePlan, sighash fr.Element) (string, [][]byte) {
	pub := m.VotePublicInputs(vp.snap.Root, vp.snap.NfRoot, sighash)
	q := func(x fr.Element) string { return fmt.Sprintf("\"0x%x\"", privacy.FieldBytes(x)) }
	arr := func(xs [merkle.Depth]fr.Element) string {
		parts := make([]string, len(xs))
		for i, x := range xs {
			parts[i] = q(x)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "nk = %s\namount = \"%d\"\nrho = %s\nrcm = %s\npos = \"%d\"\npath = %s\n",
		q(e.w.nk), vp.n.amount, q(vp.n.rho), q(vp.n.rcm), vp.n.pos, arr(vp.path))
	fmt.Fprintf(&b, "low_value = %s\nlow_next_value = %s\nlow_next_index = \"%d\"\nlow_index = \"%d\"\nlow_path = %s\n",
		q(vp.low.Low.Value), q(vp.low.Low.NextValue), vp.low.Low.NextIndex, vp.low.Index, arr(vp.low.Path))
	fmt.Fprintf(&b, "note_root = \"0x%x\"\nnf_root = \"0x%x\"\nasset = \"0x%x\"\nweight = \"%d\"\nproposal_id = \"%d\"\nvnf = \"0x%x\"\nsighash = \"0x%x\"\n",
		pub[0], pub[1], pub[2], m.Weight, m.ProposalId, pub[5], pub[6])
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

// stakeVoteMsg is n's vote on proposalID with weight (0: all of n, rounded
// down to three significant digits), its fee
// bundle paid from any current ERTH note. With prove, its bundle and vote
// proof are proven (failing the test if the circuit refuses).
func (e *stakeEnv) stakeVoteMsg(n *snote, proposalID uint64, opts []*v1.WeightedVoteOption, weight uint64, prove bool) (*sstypes.MsgStakeVote, *pendingBundle, *votePlan) {
	e.t.Helper()
	v, ok := sstypes.ParseDerthDenom(n.denom)
	require.True(e.t, ok)
	if weight == 0 {
		weight = sstypes.RoundVoteWeight(n.amount) // as every wallet does
	}
	vp := e.votePlanFor(n, proposalID)
	fee := e.feeOnly()
	m := &sstypes.MsgStakeVote{Bundle: fee.b, ProposalId: proposalID, Validator: v, Options: opts,
		Weight: weight, VoteNullifier: e.vnf(n, proposalID)}
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

// stakeVote votes all of n on proposalID; the note stays unspent.
func (e *stakeEnv) stakeVote(n *snote, proposalID uint64, opt v1.VoteOption) {
	e.t.Helper()
	m, p, _ := e.stakeVoteMsg(n, proposalID, v1.NewNonSplitVoteOption(opt), 0, true)
	res := e.run(e.privateTx(m))
	require.Equal(e.t, uint32(0), res.Code, res.Log)
	e.settle(p)
	require.Empty(e.t, eventsOf(res.Events, sstypes.EventTypeStakeNullifier), "a vote spends no stake note")
	require.Empty(e.t, eventsOf(res.Events, sstypes.EventTypeStakeNote), "a vote mints no stake note")
	ev := eventsOf(res.Events, sstypes.EventTypeStakeVote)
	require.Len(e.t, ev, 1)
	require.Equal(e.t, fmt.Sprintf("%x", m.VoteNullifier), ev[0][sstypes.AttributeKeyVoteNF])
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

// restakeAll spends all of n into two new notes of the same owner (split).
func (e *stakeEnv) restakeAll(n *snote) (*snote, *snote) {
	e.t.Helper()
	v, ok := sstypes.ParseDerthDenom(n.denom)
	require.True(e.t, ok)
	a, b := e.freshStake(n.denom, n.amount/2), e.freshStake(n.denom, n.amount-n.amount/2)
	p := e.feeOnly()
	sp := e.stake(&stakePlan{denom: n.denom, ins: []*snote{n}, outs: []*snote{a, b}})
	m := &sstypes.MsgRestake{Bundle: p.b, Validator: v, Stake: sp.proof}
	e.prove(m, p)
	e.proveStake(m, sp)
	res := e.run(e.privateTx(m))
	require.Equal(e.t, uint32(0), res.Code, res.Log)
	e.settle(p)
	e.settleStake(sp)
	require.True(e.t, a.known && b.known)
	return a, b
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
	n := e.delegate(vB, uint64(1_000*ssErth))
	k := e.delegate(vB, uint64(600*ssErth))
	m := e.delegate(vB, uint64(300*ssErth))
	// m is spent before voting begins.
	m1, _ := e.restakeAll(m)
	e.next(5 * time.Second)

	prop1 := e.submitProposal()
	prop2 := e.submitProposal()
	snaps := map[uint64]sstypes.ProposalSnapshot{}
	for _, p := range []uint64{prop1, prop2} {
		s, err := e.app.ShieldedStakingKeeper.Snapshots.Get(e.ctx(), p)
		require.NoError(t, err)
		require.NotEmpty(t, s.NfRoot)
		require.Equal(t, uint64(2), s.NfSize, "the sentinel and m's nullifier")
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
	forged.VoteNullifier = privacy.FieldBytes(ssDet("forged-vnf", 0))
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
	k1, _ := e.restakeAll(k)
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
	dec := func(x uint64) math.LegacyDec { return math.LegacyNewDecFromInt(math.NewIntFromUint64(x)) }
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
	used, err := fresh.ShieldedStakingKeeper.Votes.Has(fctx, collections.Join(prop1, append([]byte{0}, e.vnf(n, prop1)...)))
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

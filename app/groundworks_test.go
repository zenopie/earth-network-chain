package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"cosmossdk.io/collections"
	"cosmossdk.io/log"
	"cosmossdk.io/math"
	storetypes "cosmossdk.io/store/types"
	abci "github.com/cometbft/cometbft/abci/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	dbm "github.com/cosmos/cosmos-db"
	"github.com/cosmos/cosmos-sdk/baseapp"
	"github.com/cosmos/cosmos-sdk/client/flags"
	simtestutil "github.com/cosmos/cosmos-sdk/testutil/sims"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	slashingtypes "github.com/cosmos/cosmos-sdk/x/slashing/types"
	stakingkeeper "github.com/cosmos/cosmos-sdk/x/staking/keeper"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	"github.com/stretchr/testify/require"

	allocationkeeper "github.com/earth-network/earth/x/allocation/keeper"
	allocationtypes "github.com/earth-network/earth/x/allocation/types"
	sskeeper "github.com/earth-network/earth/x/shieldedstaking/keeper"
	sstypes "github.com/earth-network/earth/x/shieldedstaking/types"
	"github.com/earth-network/earth/zk/privacy"
)

// Groundworks votes are weighed per validator: one weighted voter per
// validator holding trunc(rate x sum(derth x pct) / 100) per option. These tests drive the stake handlers directly (the ante,
// and so the proofs, faked: fakeAuthorized), casting and cancelling note
// votes with restakes, so they can make many votes cheaply.

type gwEnv struct {
	*stakeEnv
	v    sdk.ValAddress
	opts []uint64
	seq  uint64
}

const gwStream = allocationtypes.STREAM_ID_GROUNDWORKS

const gwE = uint64(ssErth)

func initGwEnv(t *testing.T) *gwEnv {
	e := initStakeEnv(t)
	e.fundPoolDirect(1_000_000 * ssErth)
	v, _ := e.createValidator(1000 * ssErth)
	e.next(5 * time.Second)
	gov := authtypes.NewModuleAddress("gov")
	require.NoError(t, e.app.BankKeeper.SendCoins(e.ctx(), e.userAddr(), gov, sdk.NewCoins(sdk.NewInt64Coin("uerth", 20*ssErth))))
	g := &gwEnv{stakeEnv: e, v: v}
	for _, d := range []string{"good a", "good b", "good c"} {
		res, err := allocationkeeper.NewMsgServerImpl(e.app.AllocationKeeper).AddAddressOption(e.ctx(), &allocationtypes.MsgAddAddressOption{
			Submitter: e.bech(gov), Stream: gwStream, Description: d, Recipient: e.bech(e.userAddr()),
		})
		require.NoError(t, err)
		g.opts = append(g.opts, res.Id)
	}
	// Return what the options did not cost: gov's account holds only deposits.
	left := e.app.BankKeeper.GetAllBalances(e.ctx(), gov)
	if !left.IsZero() {
		require.NoError(t, e.app.BankKeeper.SendCoins(e.ctx(), gov, e.userAddr(), left))
	}
	e.fakeDelegate(v, uint64(200_000*gwE), "gw")
	e.days(2) // joined, then rewards compounded: rate > 1
	require.True(t, e.state(v).EpochRate.GT(math.LegacyOneDec()))
	return g
}

func (g *gwEnv) split(pcts ...uint64) []allocationtypes.AllocationWeight {
	var out []allocationtypes.AllocationWeight
	for i, p := range pcts {
		if p > 0 {
			out = append(out, allocationtypes.AllocationWeight{OptionId: g.opts[i], Percent: p})
		}
	}
	return out
}

// gwVote is a test note's Groundworks vote: the voting note's tag, and the
// vote's id.
type gwVote struct {
	tag []byte
	id  uint64
}

// tryCastGw is a restake (ante faked) whose output votes derth at v with
// splits, spending prev's note (its tag cancels prev) when given.
func (g *gwEnv) tryCastGw(v sdk.ValAddress, derth uint64, splits []allocationtypes.AllocationWeight, prev *gwVote) (*gwVote, error) {
	g.seq++
	st := fakeStake(fmt.Sprintf("gw-vote-%d", g.seq), false)
	if prev != nil {
		st.GroundworksTags[0] = prev.tag
	}
	tag := privacy.FieldBytes(ssDet("gw-tag", g.seq))
	st.VoteTag, st.VoteWeight = tag, derth
	m := &sstypes.MsgRestake{Validator: g.valoper(v), Stake: st, GroundworksSplit: splits}
	if _, err := sskeeper.NewMsgServerImpl(g.app.ShieldedStakingKeeper).Restake(g.fakeAuthorized(m), m); err != nil {
		return nil, err
	}
	id, err := g.app.ShieldedStakingKeeper.GwVotesByTag.Get(g.ctx(), tag)
	require.NoError(g.t, err)
	return &gwVote{tag: tag, id: id}, nil
}

// castGw votes a note of derth at v with splits.
func (g *gwEnv) castGw(v sdk.ValAddress, derth uint64, splits []allocationtypes.AllocationWeight) *gwVote {
	g.t.Helper()
	gv, err := g.tryCastGw(v, derth, splits, nil)
	require.NoError(g.t, err)
	return gv
}

// recastGw spends prev's note into one voting splits (the same derth): a
// split changed, or renewed.
func (g *gwEnv) recastGw(prev *gwVote, splits []allocationtypes.AllocationWeight) *gwVote {
	g.t.Helper()
	old := g.vote(prev.id)
	val, err := sdk.ValAddressFromBech32(old.Validator)
	require.NoError(g.t, err)
	gv, err := g.tryCastGw(val, old.Derth.Uint64(), splits, prev)
	require.NoError(g.t, err)
	return gv
}

// tryCancelGw spends prev's note into one that does not vote.
func (g *gwEnv) tryCancelGw(prev *gwVote) error {
	g.seq++
	st := fakeStake(fmt.Sprintf("gw-cancel-%d", g.seq), false)
	st.GroundworksTags[0] = prev.tag
	m := &sstypes.MsgRestake{Validator: g.valoper(g.v), Stake: st}
	_, err := sskeeper.NewMsgServerImpl(g.app.ShieldedStakingKeeper).Restake(g.fakeAuthorized(m), m)
	return err
}

func (g *gwEnv) cancelGw(prev *gwVote) {
	g.t.Helper()
	require.NoError(g.t, g.tryCancelGw(prev))
}

// vote is the stored vote id with its live weight (queries' view).
func (g *gwEnv) vote(id uint64) sstypes.GroundworksVote {
	g.t.Helper()
	res, err := sskeeper.NewQueryServerImpl(g.app.ShieldedStakingKeeper).GroundworksVote(g.ctx(), &sstypes.QueryGroundworksVoteRequest{Id: id})
	require.NoError(g.t, err)
	return res.Vote
}

// hasVote reports whether vote id is stored.
func (g *gwEnv) hasVote(id uint64) bool {
	has, err := g.app.ShieldedStakingKeeper.GwVotes.Has(g.ctx(), id)
	require.NoError(g.t, err)
	return has
}

// voter is v's Groundworks voter's option weights (nil when it has none).
func (g *gwEnv) voter(v sdk.ValAddress) map[uint64]math.Int {
	vt, err := g.app.AllocationKeeper.Voters.Get(g.ctx(), collections.Join(uint32(gwStream), sstypes.ValidatorVoterKey(v)))
	if err != nil {
		require.ErrorIs(g.t, err, collections.ErrNotFound)
		return nil
	}
	require.Empty(g.t, vt.Percentages)
	out := map[uint64]math.Int{}
	sum := math.ZeroInt()
	for _, w := range vt.OptionWeights {
		out[w.OptionId] = w.Weight
		sum = sum.Add(w.Weight)
	}
	require.Equal(g.t, sum, vt.Weight)
	return out
}

func (g *gwEnv) allocated(opt uint64) math.Int {
	o, err := g.app.AllocationKeeper.Options.Get(g.ctx(), collections.Join(uint32(gwStream), opt))
	require.NoError(g.t, err)
	return o.AmountAllocated
}

func (g *gwEnv) totals(v sdk.ValAddress) map[uint64]math.Int {
	out := map[uint64]math.Int{}
	require.NoError(g.t, g.app.ShieldedStakingKeeper.GwTotals.Walk(g.ctx(), collections.NewPrefixedPairRange[string, uint64](g.valoper(v)),
		func(k collections.Pair[string, uint64], t math.Int) (bool, error) {
			out[k.K2()] = t
			return false, nil
		}))
	return out
}

// perVote is what voting one note at a time gave each option: sum over live
// votes of trunc(trunc(rate x derth) x pct / 100).
func (g *gwEnv) perVote(v sdk.ValAddress) map[uint64]math.Int {
	rate := g.state(v).EpochRate
	epoch, err := g.app.AllocationKeeper.StreamEpoch(g.ctx(), gwStream)
	require.NoError(g.t, err)
	out := map[uint64]math.Int{}
	require.NoError(g.t, g.app.ShieldedStakingKeeper.GwVotes.Walk(g.ctx(), nil, func(_ uint64, p sstypes.GroundworksVote) (bool, error) {
		if p.Validator != g.valoper(v) || p.SplitEpoch != epoch {
			return false, nil
		}
		w := rate.MulInt(p.Derth).TruncateInt()
		for _, s := range p.Splits {
			c := w.MulRaw(int64(s.Percent)).QuoRaw(100)
			if cur, ok := out[s.OptionId]; ok {
				c = c.Add(cur)
			}
			out[s.OptionId] = c
		}
		return false, nil
	}))
	return out
}

// requireEquivalent: the aggregate voter puts on each option what voting
// note by note did, up to rounding (the aggregate truncates once, never
// less, by at most 2 per vote).
func (g *gwEnv) requireEquivalent(v sdk.ValAddress, votes int) {
	g.t.Helper()
	agg, per := g.voter(v), g.perVote(v)
	require.Equal(g.t, len(per), len(agg))
	rate := g.state(v).EpochRate
	for opt, want := range per {
		got := agg[opt]
		d := got.Sub(want)
		require.False(g.t, d.IsNegative(), "option %d: aggregate %s < per-vote %s", opt, got, want)
		require.True(g.t, d.LTE(math.NewInt(int64(2*votes))), "option %d: aggregate %s, per-vote %s", opt, got, want)
		require.Equal(g.t, rate.MulInt(g.totals(v)[opt]).QuoInt64(100).TruncateInt(), got)
	}
	require.NoError(g.t, g.app.ShieldedStakingKeeper.AssertInvariants(g.ctx()))
	require.NoError(g.t, g.app.AllocationKeeper.AssertHotInvariants(g.ctx()))
}

func TestGroundworksAggregateEquivalence(t *testing.T) {
	g := initGwEnv(t)
	type vote struct {
		amt  uint64
		pcts []uint64
	}
	plan := []vote{
		{1_000*gwE + 7, []uint64{100}},
		{333*gwE + 1, []uint64{30, 70}},
		{777*gwE + 3, []uint64{0, 100}},
		{1 * gwE, []uint64{33, 33, 34}},
		{12_345*gwE + 11, []uint64{1, 0, 99}},
	}
	var vs []*gwVote
	for i, p := range plan {
		vs = append(vs, g.castGw(g.v, p.amt, g.split(p.pcts...)))
		g.requireEquivalent(g.v, i+1)
	}
	// Only one Groundworks voter for all of v's votes.
	n := 0
	require.NoError(t, g.app.AllocationKeeper.Voters.Walk(g.ctx(), collections.NewPrefixedPairRange[uint32, []byte](uint32(gwStream)),
		func(collections.Pair[uint32, []byte], allocationtypes.Voter) (bool, error) {
			n++
			return false, nil
		}))
	require.Equal(t, 1, n)
	// The options carry exactly the voter's weights (no other voter).
	for opt, w := range g.voter(g.v) {
		require.Equal(t, w, g.allocated(opt))
	}
	// Queries show each vote's live weight: derth x epoch rate.
	rate := g.state(g.v).EpochRate
	for i, gv := range vs {
		require.Equal(t, rate.MulInt(math.NewIntFromUint64(plan[i].amt)).TruncateInt(), g.vote(gv.id).Weight)
	}

	// An epoch later: re-weighed at the new rate, still equivalent.
	g.days(1)
	require.True(t, g.state(g.v).EpochRate.GT(rate))
	g.requireEquivalent(g.v, len(plan))
}

// Casts, re-casts and cancels move the totals by exactly what they added:
// after every vote is gone, nothing is left, on the totals or on the
// options. A vote under a tag already voting is refused unless the proof
// cancels it; a split with no vote, and a vote with no split, are refused.
func TestGroundworksTotalsExact(t *testing.T) {
	g := initGwEnv(t)
	a := g.castGw(g.v, 501*gwE+3, g.split(37, 63))
	b := g.castGw(g.v, 2*gwE+1, g.split(0, 51, 49))
	g.requireEquivalent(g.v, 2)

	g.seq++
	st := fakeStake(fmt.Sprintf("gw-twice-%d", g.seq), false)
	st.VoteTag, st.VoteWeight = a.tag, 5*gwE
	twice := &sstypes.MsgRestake{Validator: g.valoper(g.v), Stake: st, GroundworksSplit: g.split(100)}
	_, err := sskeeper.NewMsgServerImpl(g.app.ShieldedStakingKeeper).Restake(g.fakeAuthorized(twice), twice)
	require.ErrorIs(t, err, sstypes.ErrGroundworksVote, "one vote per tag")
	require.Error(t, (&sstypes.MsgRestake{Validator: g.valoper(g.v), Stake: st}).ValidateBasic(), "a vote names a split")

	a = g.recastGw(a, g.split(0, 0, 100))
	g.requireEquivalent(g.v, 2)
	b = g.recastGw(b, g.split(50, 50))
	g.requireEquivalent(g.v, 2)
	g.days(1)
	g.requireEquivalent(g.v, 2)

	g.cancelGw(a)
	g.cancelGw(b)
	require.False(t, g.hasVote(a.id))
	require.Empty(t, g.totals(g.v))
	require.Nil(t, g.voter(g.v))
	for _, o := range g.opts {
		require.True(t, g.allocated(o).IsZero(), "option %d", o)
	}
	tw, err := g.app.AllocationKeeper.TotalWeight.Get(g.ctx(), uint32(gwStream))
	require.NoError(t, err)
	require.True(t, tw.IsZero())
	require.NoError(t, g.app.ShieldedStakingKeeper.AssertInvariants(g.ctx()))
}

// There is no vote cap, and a vote weighs at least 1 ERTH (derth x rate).
func TestGroundworksManyVotesNoCap(t *testing.T) {
	g := initGwEnv(t)
	params, err := g.app.ShieldedStakingKeeper.Params.Get(g.ctx())
	require.NoError(t, err)
	require.Equal(t, sstypes.DefaultMinGroundworksVote, params.MinPosition)
	require.Equal(t, math.NewInt(1_000_000), params.MinPosition)
	rate := g.state(g.v).EpochRate
	under := math.LegacyNewDec(int64(gwE)).Quo(rate).TruncateInt().Uint64() - 1
	_, err = g.tryCastGw(g.v, under, g.split(100), nil)
	require.ErrorIs(t, err, sstypes.ErrGroundworksVote)
	_, err = g.tryCastGw(g.v, under+2, g.split(100), nil)
	require.NoError(t, err)

	const n = 300
	for i := 0; i < n; i++ {
		g.castGw(g.v, uint64(gwE)+uint64(i), g.split(uint64(1+i%99), uint64(99-i%99)))
	}
	g.requireEquivalent(g.v, n+1)
	g.days(1)
	g.requireEquivalent(g.v, n+1)
}

// The epoch's Groundworks work is one voter per validator: re-weighing costs
// the same gas with 1 vote as with 200 on the same validator and options.
func TestGroundworksEpochCostIndependentOfVotes(t *testing.T) {
	gas := func(votes int) uint64 {
		g := initGwEnv(t)
		for i := 0; i < votes; i++ {
			g.castGw(g.v, uint64(10*gwE), g.split(40, 60))
		}
		ctx, _ := g.ctx().WithGasMeter(storetypes.NewGasMeter(1 << 60)).CacheContext()
		g.app.ShieldedStakingKeeper.ReweighGroundworks(ctx)
		return ctx.GasMeter().GasConsumed()
	}
	one, many := gas(1), gas(200)
	t.Logf("re-weigh gas: 1 vote %d, 200 votes %d", one, many)
	require.Positive(t, one)
	// Only the stored integers grow (a few more digits in each value): no
	// per-vote read or write, each of which alone costs 1,000 gas flat.
	require.InDelta(t, float64(one), float64(many), 2_000)
}

// A slash re-weighs the slashed validator's voter at once, at its post-slash
// rate (see also TestGroundworksSelfBondWeight).
func TestGroundworksSlashReweigh(t *testing.T) {
	g := initGwEnv(t)
	vA := g.genesisValidator()
	g.fakeDelegate(vA, uint64(50_000*gwE), "gw-a")
	g.days(1)
	g.castGw(vA, 10_000*gwE, g.split(100))
	g.castGw(vA, 3_000*gwE, g.split(20, 80))
	g.castGw(g.v, 5_000*gwE, g.split(100))
	before, beforeV := g.voter(vA), g.voter(g.v)
	g.requireEquivalent(vA, 2)

	g.next(5 * time.Second)
	g.next(5 * time.Second)
	infraction := g.height
	val, err := g.app.StakingKeeper.GetValidator(g.ctx(), vA)
	require.NoError(t, err)
	power := val.ConsensusPower(g.app.StakingKeeper.PowerReduction(g.ctx()))
	consAddr, err := val.GetConsAddr()
	require.NoError(t, err)
	g.block(5*time.Second, []abci.Misbehavior{{
		Type: abci.MisbehaviorType_DUPLICATE_VOTE, Validator: abci.Validator{Address: consAddr, Power: power},
		Height: infraction, Time: g.times[infraction], TotalVotingPower: power + 1000,
	}})

	after := g.voter(vA)
	require.True(t, after[g.opts[0]].LT(before[g.opts[0]]), "%s -> %s", before[g.opts[0]], after[g.opts[0]])
	require.Equal(t, beforeV, g.voter(g.v), "the other validator's voter did not move")
	g.requireEquivalent(vA, 2)
}

// A governance reset of the Groundworks stream retires every vote, lazily:
// the validator voter goes, its totals count as zero (and are dropped at the
// next epoch end), a stale vote is cancelled without touching them, and a
// re-cast counts again.
func TestGroundworksResetIsLazy(t *testing.T) {
	g := initGwEnv(t)
	a := g.castGw(g.v, 1_000*gwE, g.split(100))
	b := g.castGw(g.v, 2_000*gwE, g.split(50, 50))
	require.NotNil(t, g.voter(g.v))

	gov := authtypes.NewModuleAddress("gov")
	_, err := allocationkeeper.NewMsgServerImpl(g.app.AllocationKeeper).ResetAllocations(g.ctx(),
		&allocationtypes.MsgResetAllocations{Authority: g.bech(gov), Stream: gwStream})
	require.NoError(t, err)
	require.True(t, g.vote(a.id).Weight.IsZero(), "a stale vote carries nothing")
	require.NoError(t, g.app.ShieldedStakingKeeper.AssertInvariants(g.ctx()))

	g.days(1) // the epoch end drops v's stale totals and its voter
	require.Empty(t, g.totals(g.v))
	require.Nil(t, g.voter(g.v))

	a = g.recastGw(a, g.split(0, 100)) // a votes again
	g.requireEquivalent(g.v, 1)
	require.Equal(t, map[uint64]math.Int{g.opts[1]: math.NewInt(100_000 * ssErth)}, g.totals(g.v))
	g.cancelGw(b) // stale: nothing to take off
	g.requireEquivalent(g.v, 1)
	g.cancelGw(a)
	require.Empty(t, g.totals(g.v))
	require.Nil(t, g.voter(g.v))
}

// Export and import: the totals are rebuilt from the votes, and the
// allocation module's validator voters come back as exported.
func TestGroundworksGenesisRoundTrip(t *testing.T) {
	g := initGwEnv(t)
	g.castGw(g.v, 1_000*gwE, g.split(10, 90))
	g.castGw(g.v, 4_321*gwE, g.split(0, 0, 100))
	g.castGw(g.v, 9*gwE, g.split(50, 50))
	g.days(1)
	want, wantV := g.totals(g.v), g.voter(g.v)

	exported, err := g.app.ExportAppStateAndValidators(false, nil, nil)
	require.NoError(t, err)
	var appState map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(exported.AppState, &appState))
	fresh := New(log.NewNopLogger(), dbm.NewMemDB(), nil, true, simtestutil.AppOptionsMap{flags.FlagHome: t.TempDir()},
		baseapp.SetChainID(ssChainID))
	fctx := fresh.NewUncachedContext(false, cmtproto.Header{ChainID: ssChainID, Height: g.height, Time: g.now})
	_, err = fresh.ModuleManager.InitGenesis(fctx, fresh.AppCodec(), appState)
	require.NoError(t, err)
	require.NoError(t, fresh.ShieldedStakingKeeper.AssertInvariants(fctx))

	got := map[uint64]math.Int{}
	require.NoError(t, fresh.ShieldedStakingKeeper.GwTotals.Walk(fctx, nil, func(k collections.Pair[string, uint64], v math.Int) (bool, error) {
		require.Equal(t, g.valoper(g.v), k.K1())
		got[k.K2()] = v
		return false, nil
	}))
	require.Equal(t, want, got)
	vt, err := fresh.AllocationKeeper.Voters.Get(fctx, collections.Join(uint32(gwStream), sstypes.ValidatorVoterKey(g.v)))
	require.NoError(t, err)
	gotV := map[uint64]math.Int{}
	for _, w := range vt.OptionWeights {
		gotV[w.OptionId] = w.Weight
	}
	require.Equal(t, wantV, gotV)
	// The fresh chain re-files the same voter from its rebuilt totals.
	fresh.ShieldedStakingKeeper.ReweighGroundworks(fctx)
	vt2, err := fresh.AllocationKeeper.Voters.Get(fctx, collections.Join(uint32(gwStream), sstypes.ValidatorVoterKey(g.v)))
	require.NoError(t, err)
	require.Equal(t, vt.OptionWeights, vt2.OptionWeights)
}

// Audit 7: an option pruned while votes still name it. The validator's voter
// is re-filed without it at once (it took nothing anyway); an export drops it
// from the votes' splits (as x/allocation's from its voters), and a vote left
// with nothing is not exported, so the imported chain rebuilds totals and a
// voter that never name it.
func TestGroundworksPrunedOptionLeavesVotes(t *testing.T) {
	g := initGwEnv(t)
	v1 := g.castGw(g.v, 1_000*gwE, g.split(10, 90))
	v2 := g.castGw(g.v, 500*gwE, g.split(100))
	g.days(1)
	pruned := g.opts[0]
	// The chamber strikes option 0; thirty idle days later it is pruned.
	require.NoError(t, g.app.AllocationKeeper.RemoveGroundworksOption(g.ctx(), authtypes.NewModuleAddress("assembly"), pruned))
	g.days(31)
	has, err := g.app.AllocationKeeper.Options.Has(g.ctx(), collections.Join(uint32(gwStream), pruned))
	require.NoError(t, err)
	require.False(t, has, "pruned")
	// Still named by the votes and their totals at runtime...
	require.Equal(t, g.opts[0], g.vote(v2.id).Splits[0].OptionId)
	_, named := g.totals(g.v)[pruned]
	require.True(t, named)
	// ...but the re-filed voter leaves it out.
	g.app.ShieldedStakingKeeper.ReweighGroundworks(g.ctx())
	_, inVoter := g.voter(g.v)[pruned]
	require.False(t, inVoter)
	require.NoError(t, g.app.ShieldedStakingKeeper.AssertInvariants(g.ctx()))

	exported, err := g.app.ExportAppStateAndValidators(false, nil, nil)
	require.NoError(t, err)
	var appState map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(exported.AppState, &appState))
	var gs sstypes.GenesisState
	require.NoError(t, g.app.AppCodec().UnmarshalJSON(appState[sstypes.ModuleName], &gs))
	ids := map[uint64]bool{}
	for _, v := range gs.Positions {
		ids[v.Id] = true
		for _, w := range v.Splits {
			require.NotEqual(t, pruned, w.OptionId, "vote %d", v.Id)
		}
		if v.Id == v1.id {
			require.Equal(t, g.split(0, 90), v.Splits)
		}
	}
	require.False(t, ids[v2.id], "its only option is gone: no vote")
	fresh := New(log.NewNopLogger(), dbm.NewMemDB(), nil, true, simtestutil.AppOptionsMap{flags.FlagHome: t.TempDir()},
		baseapp.SetChainID(ssChainID))
	fctx := fresh.NewUncachedContext(false, cmtproto.Header{ChainID: ssChainID, Height: g.height, Time: g.now})
	_, err = fresh.ModuleManager.InitGenesis(fctx, fresh.AppCodec(), appState)
	require.NoError(t, err)
	require.NoError(t, fresh.ShieldedStakingKeeper.AssertInvariants(fctx))
	require.NoError(t, fresh.ShieldedStakingKeeper.GwTotals.Walk(fctx, nil, func(k collections.Pair[string, uint64], _ math.Int) (bool, error) {
		require.NotEqual(t, pruned, k.K2())
		return false, nil
	}))
	fresh.ShieldedStakingKeeper.ReweighGroundworks(fctx)
	vt, err := fresh.AllocationKeeper.Voters.Get(fctx, collections.Join(uint32(gwStream), sstypes.ValidatorVoterKey(g.v)))
	require.NoError(t, err)
	for _, w := range vt.OptionWeights {
		require.NotEqual(t, pruned, w.OptionId)
	}
}

// Audit 6 D6-1: a delegation removed from a validator that is not Bonded
// (just created, jailed, unbonding, unbonded) used to keep its weight, with
// no stake behind it. Audit 7 D7-L2: an operator's Groundworks weight is its
// self-bond at a Bonded validator only; outside the active set it weighs
// nothing (its vote kept at weight zero while the bond remains).

type gwWeightEnv struct {
	*stakeEnv
	opt uint64
}

func initGwWeightEnv(t *testing.T) *gwWeightEnv {
	e := initStakeEnv(t)
	gov := authtypes.NewModuleAddress("gov")
	require.NoError(t, e.app.BankKeeper.SendCoins(e.ctx(), e.userAddr(), gov, sdk.NewCoins(sdk.NewInt64Coin("uerth", 10*ssErth))))
	res, err := allocationkeeper.NewMsgServerImpl(e.app.AllocationKeeper).AddAddressOption(e.ctx(), &allocationtypes.MsgAddAddressOption{
		Submitter: e.bech(gov), Stream: allocationtypes.STREAM_ID_GROUNDWORKS, Description: "a public good", Recipient: e.bech(e.userAddr()),
	})
	require.NoError(t, err)
	e.next(5 * time.Second)
	return &gwWeightEnv{stakeEnv: e, opt: res.Id}
}

func (g *gwWeightEnv) vote(op sdk.AccAddress) {
	_, err := allocationkeeper.NewMsgServerImpl(g.app.AllocationKeeper).SetAllocations(g.ctx(), &allocationtypes.MsgSetAllocations{
		Creator: g.bech(op), Stream: allocationtypes.STREAM_ID_GROUNDWORKS,
		Percentages: []allocationtypes.AllocationWeight{{OptionId: g.opt, Percent: 100}},
	})
	require.NoError(g.t, err)
}

// weight is the operator's stored Groundworks weight (0 once the record is gone).
func (g *gwWeightEnv) weight(op sdk.AccAddress) math.Int {
	v, err := g.app.AllocationKeeper.Voters.Get(g.ctx(), collections.Join(uint32(allocationtypes.STREAM_ID_GROUNDWORKS), []byte(op)))
	if errors.Is(err, collections.ErrNotFound) {
		return math.ZeroInt()
	}
	require.NoError(g.t, err)
	return v.Weight
}

// bonded is the operator's weight-bearing stake: its delegations at Bonded
// validators.
func (g *gwWeightEnv) bonded(op sdk.AccAddress) math.Int {
	b, err := g.app.AllocationKeeper.BondedWeight(g.ctx(), op)
	require.NoError(g.t, err)
	return b
}

func (g *gwWeightEnv) allocated() math.Int {
	o, err := g.app.AllocationKeeper.Options.Get(g.ctx(), collections.Join(uint32(allocationtypes.STREAM_ID_GROUNDWORKS), g.opt))
	require.NoError(g.t, err)
	return o.AmountAllocated
}

func (g *gwWeightEnv) undelegate(val sdk.ValAddress, amount math.Int) {
	_, err := stakingkeeper.NewMsgServerImpl(g.app.StakingKeeper).Undelegate(g.ctx(), stakingtypes.NewMsgUndelegate(
		g.bech(sdk.AccAddress(val)), g.valoper(val), sdk.NewCoin("uerth", amount)))
	require.NoError(g.t, err)
}

func (g *gwWeightEnv) status(val sdk.ValAddress) stakingtypes.BondStatus {
	v, err := g.app.StakingKeeper.GetValidator(g.ctx(), val)
	require.NoError(g.t, err)
	return v.GetStatus()
}

func (g *gwWeightEnv) selfBond(val sdk.ValAddress) math.Int {
	d, err := g.app.StakingKeeper.GetDelegation(g.ctx(), sdk.AccAddress(val), val)
	require.NoError(g.t, err)
	v, err := g.app.StakingKeeper.GetValidator(g.ctx(), val)
	require.NoError(g.t, err)
	return v.TokensFromSharesTruncated(d.Shares).TruncateInt()
}

func (g *gwWeightEnv) checkConsistent(op sdk.AccAddress) {
	g.t.Helper()
	require.Equal(g.t, g.bonded(op), g.weight(op), "weight follows the live bond")
	require.NoError(g.t, g.app.AllocationKeeper.AssertHotInvariants(g.ctx()))
}

// The audit's PoC: create, vote and undelegate the whole self-bond in one
// block, while the validator is still Unbonded. The vote finds no weight
// (D7-L2); once Bonded it weighs the bond, and the weight goes with the stake.
func TestGroundworksSameBlockCreateVoteUndelegate(t *testing.T) {
	g := initGwWeightEnv(t)
	base := g.allocated()
	val, _ := g.createValidator(500 * ssErth)
	op := sdk.AccAddress(val)
	require.Equal(t, stakingtypes.Unbonded, g.status(val))
	_, err := allocationkeeper.NewMsgServerImpl(g.app.AllocationKeeper).SetAllocations(g.ctx(), &allocationtypes.MsgSetAllocations{
		Creator: g.bech(op), Stream: allocationtypes.STREAM_ID_GROUNDWORKS,
		Percentages: []allocationtypes.AllocationWeight{{OptionId: g.opt, Percent: 100}},
	})
	require.ErrorIs(t, err, allocationtypes.ErrNoWeight, "an Unbonded validator's bond weighs nothing")
	require.Equal(t, base, g.allocated())
	g.next(5 * time.Second)
	require.Equal(t, stakingtypes.Bonded, g.status(val))
	g.vote(op)
	require.Equal(t, math.NewInt(500*ssErth), g.weight(op))
	require.Equal(t, base.AddRaw(500*ssErth), g.allocated())

	g.undelegate(val, math.NewInt(500*ssErth))
	require.True(t, g.weight(op).IsZero(), "phantom weight: %s", g.weight(op))
	require.Equal(t, base, g.allocated())
	g.checkConsistent(op)
	g.days(3)
	require.True(t, g.weight(op).IsZero())
	require.Equal(t, base, g.allocated())
	g.checkConsistent(op)
}

// A Bonded validator's operator votes, the validator is jailed (Unbonding),
// the operator withdraws part of the bond, then all of it after the
// validator is Unbonded. The weight tracks the bond at every step.
func TestGroundworksJailedThenFullUnbond(t *testing.T) {
	g := initGwWeightEnv(t)
	base := g.allocated()
	val, _ := g.createValidator(500 * ssErth)
	op := sdk.AccAddress(val)
	g.next(5 * time.Second)
	require.Equal(t, stakingtypes.Bonded, g.status(val))
	g.vote(op)
	g.checkConsistent(op)

	// Jailed: Unbonding, its tokens untouched, its weight gone (D7-L2) in
	// the block it left the active set; the vote stays.
	v, err := g.app.StakingKeeper.GetValidator(g.ctx(), val)
	require.NoError(t, err)
	cons, err := v.GetConsAddr()
	require.NoError(t, err)
	require.NoError(t, g.app.StakingKeeper.Jail(g.ctx(), cons))
	g.next(5 * time.Second)
	require.Equal(t, stakingtypes.Unbonding, g.status(val))
	g.checkConsistent(op)
	require.True(t, g.weight(op).IsZero())
	require.Equal(t, base, g.allocated())
	g.requireVoteKept(op)

	// A partial withdrawal from the Unbonding validator.
	g.undelegate(val, math.NewInt(200*ssErth))
	g.checkConsistent(op)
	require.True(t, g.weight(op).IsZero())
	g.requireVoteKept(op)

	// Past the unbonding time: Unbonded. The weight is unchanged.
	g.days(22)
	require.Equal(t, stakingtypes.Unbonded, g.status(val))
	g.checkConsistent(op)

	// The rest of the bond, from the Unbonded validator.
	g.undelegate(val, g.selfBond(val))
	require.True(t, g.weight(op).IsZero(), "phantom weight: %s", g.weight(op))
	require.Equal(t, base, g.allocated())
	g.checkConsistent(op)
}

// A Bonded validator's operator withdraws its whole self-bond: Bonded is the
// case that always worked, kept as a guard.
func TestGroundworksBondedFullUnbond(t *testing.T) {
	g := initGwWeightEnv(t)
	base := g.allocated()
	val, _ := g.createValidator(500 * ssErth)
	op := sdk.AccAddress(val)
	g.next(5 * time.Second)
	require.Equal(t, stakingtypes.Bonded, g.status(val))
	g.vote(op)
	g.undelegate(val, math.NewInt(100*ssErth))
	g.checkConsistent(op)
	g.undelegate(val, g.selfBond(val))
	require.True(t, g.weight(op).IsZero())
	require.Equal(t, base, g.allocated())
	g.checkConsistent(op)
}

// A redelegation of a self-bond is refused (the private staking module is the
// sole delegator besides an operator to its own validator), so the weight
// cannot move through one.
func TestGroundworksRedelegationRefused(t *testing.T) {
	g := initGwWeightEnv(t)
	val, _ := g.createValidator(500 * ssErth)
	op := sdk.AccAddress(val)
	g.next(5 * time.Second)
	g.vote(op)
	_, err := stakingkeeper.NewMsgServerImpl(g.app.StakingKeeper).BeginRedelegate(g.ctx(), stakingtypes.NewMsgBeginRedelegate(
		g.bech(op), g.valoper(val), g.valoper(g.genesisValidator()), sdk.NewInt64Coin("uerth", 100*ssErth)))
	require.Error(t, err)
	g.checkConsistent(op)
}

// requireVoteKept: the operator's vote is stored (at weight zero) while its
// bond remains outside the active set.
func (g *gwWeightEnv) requireVoteKept(op sdk.AccAddress) {
	g.t.Helper()
	v, err := g.app.AllocationKeeper.Voters.Get(g.ctx(), collections.Join(uint32(allocationtypes.STREAM_ID_GROUNDWORKS), []byte(op)))
	require.NoError(g.t, err)
	require.NotEmpty(g.t, v.Percentages)
}

// Audit 7 D7-L2: a jailed validator's operator loses its Groundworks weight
// in the block its validator leaves the active set, and gets it back, with
// no new vote, in the block it is Bonded again (unjailed).
func TestGroundworksBondedOnly(t *testing.T) {
	g := initGwWeightEnv(t)
	base := g.allocated()
	val, key := g.createValidator(500 * ssErth)
	op := sdk.AccAddress(val)
	g.next(5 * time.Second)
	g.vote(op)
	require.Equal(t, math.NewInt(500*ssErth), g.weight(op))

	v, err := g.app.StakingKeeper.GetValidator(g.ctx(), val)
	require.NoError(t, err)
	cons, err := v.GetConsAddr()
	require.NoError(t, err)
	require.NoError(t, g.app.SlashingKeeper.Jail(g.ctx(), cons))
	g.next(5 * time.Second)
	require.Equal(t, stakingtypes.Unbonding, g.status(val))
	require.True(t, g.weight(op).IsZero())
	require.Equal(t, base, g.allocated())
	g.requireVoteKept(op)
	g.checkConsistent(op)

	// Unjailed: Bonded again at the block's end, the weight back.
	res := g.run(g.signedTx(key, 300_000, 5_000, slashingtypes.NewMsgUnjail(g.valoper(val))))
	require.Equal(t, uint32(0), res.Code, res.Log)
	g.next(5 * time.Second)
	require.Equal(t, stakingtypes.Bonded, g.status(val))
	require.Equal(t, math.NewInt(500*ssErth), g.weight(op))
	require.Equal(t, base.AddRaw(500*ssErth), g.allocated())
	g.checkConsistent(op)
}

// Re-audit R5 (GwEpoch leak): a validator's Groundworks entry goes with its
// last vote, and the epoch end never walks the Groundworks index: its
// voters re-weigh with the bounded book sweep. Stale entries (here forced in
// state) cost an epoch end nothing.
func TestGroundworksIndexNoLeakAndEpochCostBounded(t *testing.T) {
	g := initGwEnv(t)
	a := g.castGw(g.v, 2*gwE, g.split(100))
	g.cancelGw(a)
	_, err := g.app.ShieldedStakingKeeper.GwEpoch.Get(g.ctx(), g.valoper(g.v))
	require.ErrorIs(t, err, collections.ErrNotFound, "GwEpoch removed with the last vote")

	epochEndGas := func(n int) uint64 {
		cc, _ := g.ctx().CacheContext()
		for i := 0; i < n; i++ {
			va := sdk.ValAddress([]byte(fmt.Sprintf("pocvalidator%08d", i)))
			require.NoError(t, g.app.ShieldedStakingKeeper.GwEpoch.Set(cc, g.valoper(va), 0))
		}
		ep, err := g.app.ShieldedStakingKeeper.Epoch.Get(cc)
		require.NoError(t, err)
		ctx := cc.WithBlockTime(time.Unix(ep.EndTime, 0)).WithGasMeter(storetypes.NewGasMeter(1 << 60))
		require.NoError(t, g.app.ShieldedStakingKeeper.EndBlocker(ctx))
		return ctx.GasMeter().GasConsumed()
	}
	base, many := epochEndGas(0), epochEndGas(10_000)
	t.Logf("epoch end gas: no stale entries %d, 10000 stale entries %d", base, many)
	require.InDelta(t, float64(base), float64(many), 5_000)
}

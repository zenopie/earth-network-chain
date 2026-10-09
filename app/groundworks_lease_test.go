package app

import (
	"encoding/json"
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
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	allocationtypes "github.com/earth-network/earth/x/allocation/types"
	sstypes "github.com/earth-network/earth/x/shieldedstaking/types"
)

// gwLease is the lease these tests set: the shortest allowed.
const gwLease = int64(allocationtypes.MinGroundworksLeaseSeconds)

func setGwLease(t *testing.T, e *stakeEnv) {
	t.Helper()
	p, err := e.app.AllocationKeeper.Params.Get(e.ctx())
	require.NoError(t, err)
	p.GroundworksLeaseSeconds = uint64(gwLease)
	require.NoError(t, e.app.AllocationKeeper.Params.Set(e.ctx(), p))
}

func (g *gwEnv) storedVote(id uint64) sstypes.GroundworksVote {
	g.t.Helper()
	p, err := g.app.ShieldedStakingKeeper.GwVotes.Get(g.ctx(), id)
	require.NoError(g.t, err)
	return p
}

// at runs one block at unix time t.
func (e *stakeEnv) atUnix(t int64) {
	e.t.Helper()
	require.Greater(e.t, t, e.now.Unix())
	e.next(time.Unix(t, 0).Sub(e.now))
}

func (e *stakeEnv) gwIndex() (math.Int, math.Int) {
	e.t.Helper()
	idx, err := e.app.AllocationKeeper.RewardIndex.Get(e.ctx(), uint32(allocationtypes.STREAM_ID_GROUNDWORKS))
	require.NoError(e.t, err)
	total, err := e.app.AllocationKeeper.TotalWeight.Get(e.ctx(), uint32(allocationtypes.STREAM_ID_GROUNDWORKS))
	require.NoError(e.t, err)
	return idx, total
}

// A Groundworks vote is leased: it counts until split_expires_at (cast +
// groundworks_lease_seconds), comes off its validator's totals at exactly
// that time (the stream settled to the expiry first, however late the
// block) and is deleted, and re-casting renews it. At the boundary: counting
// one second before, gone at the second itself.
func TestGroundworksVoteLease(t *testing.T) {
	g := initGwEnv(t)
	setGwLease(t, g.stakeEnv)
	p1 := g.castGw(g.v, 1_000*gwE, g.split(100))
	e1 := g.now.Unix() + gwLease
	require.Equal(t, e1, g.storedVote(p1.id).SplitExpiresAt)
	g.next(time.Hour)
	p2 := g.castGw(g.v, 2_000*gwE, g.split(0, 100))
	e2 := g.storedVote(p2.id).SplitExpiresAt
	require.Equal(t, g.now.Unix()+gwLease, e2)

	// Ten minutes before p1's lapse, then a block half an hour after it
	// (p2 lapses an hour after p1).
	g.atUnix(e1 - 600)
	require.Contains(t, g.totals(g.v), g.opts[0], "p1 still counts")
	w := g.voter(g.v)[g.opts[0]]
	require.True(t, w.IsPositive())
	idx0, total0 := g.gwIndex()
	g.atUnix(e1 + 1800)
	idx1, _ := g.gwIndex()
	require.NotContains(t, g.totals(g.v), g.opts[0], "p1's vote lapsed")
	require.False(t, g.hasVote(p1.id), "a lapsed vote is deleted")
	_, err := g.app.ShieldedStakingKeeper.GwVotesByTag.Get(g.ctx(), p1.tag)
	require.ErrorIs(t, err, collections.ErrNotFound)
	require.True(t, g.allocated(g.opts[0]).IsZero(), "nothing left on p1's option")
	// The half hour after the lapse was shared only by the weight left: the
	// index moved by 600 s over total0, then 1,800 s over total0 - w.
	prec := math.NewInt(1_000_000_000_000_000_000)
	eps := math.NewInt(allocationtypes.EmissionPerSecond)
	want := eps.MulRaw(600).Mul(prec).Quo(total0).Add(eps.MulRaw(1800).Mul(prec).Quo(total0.Sub(w)))
	require.Equal(t, want, idx1.Sub(idx0), "emission after the lapse is never shared with the lapsed weight")
	g.requireEquivalent(g.v, 2)

	// Renewal: p2 re-casts before its lease ends and keeps counting past it.
	p2 = g.recastGw(p2, g.split(0, 100))
	e2b := g.storedVote(p2.id).SplitExpiresAt
	require.Greater(t, e2b, e2)
	g.atUnix(e2 + 60)
	require.Contains(t, g.totals(g.v), g.opts[1], "renewed: still counts")

	// The boundary: one second before the lease end it counts, at it not.
	g.atUnix(e2b - 1)
	require.Contains(t, g.totals(g.v), g.opts[1])
	g.atUnix(e2b)
	require.NotContains(t, g.totals(g.v), g.opts[1])
	require.False(t, g.hasVote(p2.id))
	require.NoError(t, g.app.ShieldedStakingKeeper.AssertInvariants(g.ctx()))
}

// The lease travels through an export: votes carry split_expires_at, the
// imported chain queues them again, and they lapse there on time.
func TestGroundworksLeaseGenesisRoundTrip(t *testing.T) {
	g := initGwEnv(t)
	setGwLease(t, g.stakeEnv)
	gv := g.castGw(g.v, 1_000*gwE, g.split(100))
	p := gv.id
	exp := g.storedVote(p).SplitExpiresAt
	g.next(time.Hour)

	exported, err := g.app.ExportAppStateAndValidators(false, nil, nil)
	require.NoError(t, err)
	var appState map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(exported.AppState, &appState))
	fresh := New(log.NewNopLogger(), dbm.NewMemDB(), nil, true, simtestutil.AppOptionsMap{flags.FlagHome: t.TempDir()},
		baseapp.SetChainID(ssChainID))
	fctx := fresh.NewUncachedContext(false, cmtproto.Header{ChainID: ssChainID, Height: g.height, Time: g.now})
	_, err = fresh.ModuleManager.InitGenesis(fctx, fresh.AppCodec(), appState)
	require.NoError(t, err)
	got, err := fresh.ShieldedStakingKeeper.GwVotes.Get(fctx, p)
	require.NoError(t, err)
	require.Equal(t, exp, got.SplitExpiresAt)
	has, err := fresh.ShieldedStakingKeeper.GwLapses.Has(fctx, collections.Join(exp, p))
	require.NoError(t, err)
	require.True(t, has, "the imported lease is queued")
	gp, err := fresh.AllocationKeeper.Params.Get(fctx)
	require.NoError(t, err)
	require.Equal(t, uint64(gwLease), gp.GroundworksLeaseSeconds)

	// A block past the lease on the imported chain: its BeginBlock sweep
	// retires the vote.
	lctx := fctx.WithBlockTime(time.Unix(exp+10, 0))
	require.NoError(t, fresh.AllocationKeeper.BeginBlocker(lctx))
	has, err = fresh.ShieldedStakingKeeper.GwVotes.Has(lctx, p)
	require.NoError(t, err)
	require.False(t, has)
}

// An operator's Groundworks split (its self-bond's vote) is leased the same
// way: it lapses at cast + groundworks_lease_seconds, and casting again
// renews it.
func TestGroundworksOperatorLease(t *testing.T) {
	g := initGwWeightEnv(t)
	setGwLease(t, g.stakeEnv)
	base := g.allocated()
	val, _ := g.createValidator(500 * ssErth)
	op := sdk.AccAddress(val)
	g.next(5 * time.Second)
	g.vote(op)
	v, err := g.app.AllocationKeeper.Voters.Get(g.ctx(), collections.Join(uint32(allocationtypes.STREAM_ID_GROUNDWORKS), []byte(op)))
	require.NoError(t, err)
	exp := v.ExpiresAt
	require.Equal(t, g.now.Unix()+gwLease, exp)
	require.Equal(t, base.AddRaw(500*ssErth).String(), g.allocated().String())

	// Renewed halfway: the old lease end passes with the vote standing.
	g.next(time.Duration(gwLease/2) * time.Second)
	g.vote(op)
	g.atUnix(exp + 60)
	require.True(t, g.weight(op).IsPositive(), "renewed: still counts")
	v, err = g.app.AllocationKeeper.Voters.Get(g.ctx(), collections.Join(uint32(allocationtypes.STREAM_ID_GROUNDWORKS), []byte(op)))
	require.NoError(t, err)
	// Then it lapses at its new lease end.
	g.atUnix(v.ExpiresAt)
	require.True(t, g.weight(op).IsZero(), "lapsed")
	require.Equal(t, base, g.allocated())
}

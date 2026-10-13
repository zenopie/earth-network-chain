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

// A stake note's Groundworks vote carries no lease (v1.3.0): it lives with
// its note's stake vintage. Past what a lease would have been it still
// counts, and nothing is queued for it.
func TestGroundworksVoteNoLease(t *testing.T) {
	g := initGwEnv(t)
	setGwLease(t, g.stakeEnv)
	p1 := g.castGw(g.v, 1_000*gwE, g.split(100))
	e1 := g.now.Unix() + gwLease
	got := g.storedVote(p1.id)
	cur := sstypes.MonthOf(g.now)
	require.Equal(t, cur, got.Vintage, "the vote is in its note's vintage")
	g.atUnix(e1 + 1800)
	require.Contains(t, g.totals(g.v), g.opts[0], "still counts past a lease's length")
	require.True(t, g.hasVote(p1.id))
	require.True(t, g.allocated(g.opts[0]).IsPositive())
	g.requireEquivalent(g.v, 1)
	require.NoError(t, g.app.ShieldedStakingKeeper.AssertInvariants(g.ctx()))
}

// A vote travels through an export with its vintage and no lease; the
// imported chain rebuilds its vintage's totals and indexes it by vintage.
func TestGroundworksVoteGenesisRoundTrip(t *testing.T) {
	g := initGwEnv(t)
	setGwLease(t, g.stakeEnv)
	gv := g.castGw(g.v, 1_000*gwE, g.split(100))
	p := gv.id
	vin := g.storedVote(p).Vintage
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
	require.Equal(t, vin, got.Vintage)
	has, err := fresh.ShieldedStakingKeeper.GwVotesByVintage.Has(fctx, collections.Join3(vin, got.Validator, p))
	require.NoError(t, err)
	require.True(t, has, "indexed by vintage")
	t0, err := fresh.ShieldedStakingKeeper.GwTotals.Get(fctx, collections.Join3(got.Validator, vin, g.opts[0]))
	require.NoError(t, err)
	require.Equal(t, got.Derth.MulRaw(100), t0)
	require.NoError(t, fresh.ShieldedStakingKeeper.AssertInvariants(fctx))
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

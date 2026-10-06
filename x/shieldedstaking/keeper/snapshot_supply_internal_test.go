package keeper

import (
	"testing"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"
	storetypes "cosmossdk.io/store/types"
	addresscodec "github.com/cosmos/cosmos-sdk/codec/address"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/cosmos/cosmos-sdk/testutil"
	sdk "github.com/cosmos/cosmos-sdk/types"
	moduletestutil "github.com/cosmos/cosmos-sdk/types/module/testutil"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/stretchr/testify/require"

	allocationkeeper "github.com/earth-network/earth/x/allocation/keeper"
	shieldedkeeper "github.com/earth-network/earth/x/shielded/keeper"
	"github.com/earth-network/earth/x/shieldedstaking/types"
)

// Audit 4 I1: a snapshot taken in block H pairs the stake roots of the end
// of H-1, so the derth supply it sees is the supply at the start of H. A
// change earlier in H (before the snapshot) does not count; one after it in
// H, or later, is checkpointed so the snapshot keeps its start-of-block
// supply; a snapshot in a later block sees the changed supply.
func TestSnapshotSupplyIsStartOfBlock(t *testing.T) {
	key := storetypes.NewKVStoreKey(types.StoreKey)
	ctx := testutil.DefaultContextWithDB(t, key, storetypes.NewTransientStoreKey("tt")).Ctx
	enc := moduletestutil.MakeTestEncodingConfig()
	k := NewKeeper(runtime.NewKVStoreService(key), enc.Codec, addresscodec.NewBech32Codec("earth"),
		authtypes.NewModuleAddress("gov"), nil, nil, nil, nil, nil, nil, shieldedkeeper.Keeper{}, allocationkeeper.Keeper{})
	const val = "earthvaloper1test"
	at := func(h int64) sdk.Context { return ctx.WithBlockHeight(h) }
	change := func(c sdk.Context, d int64) {
		vs, err := k.ValidatorState(c, val)
		require.NoError(t, err)
		require.NoError(t, k.checkpointSupply(c, &vs))
		vs.DerthSupply = vs.DerthSupply.AddRaw(d)
		require.NoError(t, k.Validators.Set(c, val, vs))
	}
	snapshot := func(c sdk.Context, id uint64) types.ProposalSnapshot {
		seq, err := k.SnapshotSeq.Next(c)
		require.NoError(t, err)
		s := types.ProposalSnapshot{ProposalId: id, Height: c.BlockHeight(), Seq: seq + 1}
		require.NoError(t, k.Snapshots.Set(c, id, s))
		require.NoError(t, k.SnapshotsBySeq.Set(c, collections.Join(s.Seq, id)))
		return s
	}
	supply := func(c sdk.Context, s types.ProposalSnapshot) int64 {
		v, err := k.snapshotSupply(c, s, val)
		require.NoError(t, err)
		return v.Int64()
	}

	change(at(10), 1_000) // supply 1000 by the end of block 10

	// Block 20: +500 before the snapshot, the snapshot, +7 after it.
	change(at(20), 500)
	s1 := snapshot(at(20), 1)
	require.Equal(t, int64(1_000), supply(at(20), s1), "a change earlier in the block does not count")
	change(at(20), 7)
	require.Equal(t, int64(1_000), supply(at(20), s1), "nor one after it")

	// Block 25: a second snapshot, no change in its block: it sees 1507.
	s2 := snapshot(at(25), 2)
	change(at(30), -100)
	require.Equal(t, int64(1_000), supply(at(30), s1))
	require.Equal(t, int64(1_507), supply(at(30), s2))
	require.Equal(t, math.NewInt(1_407), k.Supply(at(30), val))
}

// Audit C-1: a snapshot taken without roots (RootsStale) refuses note votes,
// which prove against its roots, and still takes position votes.
func TestRootlessSnapshotTakesPositionVotes(t *testing.T) {
	key := storetypes.NewKVStoreKey(types.StoreKey)
	ctx := testutil.DefaultContextWithDB(t, key, storetypes.NewTransientStoreKey("tt")).Ctx
	enc := moduletestutil.MakeTestEncodingConfig()
	k := NewKeeper(runtime.NewKVStoreService(key), enc.Codec, addresscodec.NewBech32Codec("earth"),
		authtypes.NewModuleAddress("gov"), nil, nil, nil, nil, nil, nil, shieldedkeeper.Keeper{}, allocationkeeper.Keeper{})
	const val = "earthvaloper1test"
	c := ctx.WithBlockHeight(10)
	vs, err := k.ValidatorState(c, val)
	require.NoError(t, err)
	vs.DerthSupply = math.NewInt(1_000)
	require.NoError(t, k.Validators.Set(c, val, vs))
	c = ctx.WithBlockHeight(20)
	snap := types.ProposalSnapshot{ProposalId: 1, Height: 20, Seq: 1, VotingEnd: c.BlockTime().UnixNano() + 1e12}
	require.NoError(t, k.Snapshots.Set(c, 1, snap))
	require.NoError(t, k.SnapshotsBySeq.Set(c, collections.Join(snap.Seq, uint64(1))))

	_, _, err = k.openSnapshot(c, 1, val, true)
	require.ErrorIs(t, err, types.ErrNoVoting, "a note vote needs the snapshot's roots")
	_, supply, err := k.openSnapshot(c, 1, val, false)
	require.NoError(t, err, "a position vote does not")
	require.Equal(t, int64(1_000), supply.Int64())
}

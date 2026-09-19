package keeper

import (
	"testing"
	"time"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/stretchr/testify/require"

	"github.com/earth-network/earth/x/allocation/types"
)

// chamber is the address the assembly registers itself under in app wiring.
func chamberAddr() sdk.AccAddress { return sdk.AccAddress(authtypes.NewModuleAddress("assembly")) }

// addVotedOption puts a groundworks ADDRESS option in place with a staker's
// whole weight behind it, and lets it accrue.
func addVotedOption(t *testing.T, e *testEnv, bonded int64) (uint64, string) {
	t.Helper()
	ms := NewMsgServerImpl(e.k)
	authority, _ := e.k.addressCodec.BytesToString(e.k.GetAuthority())
	_, recipient := e.addr("recipient")
	_, voter := e.addr("voter")

	added, err := ms.AddAddressOption(e.ctx, &types.MsgAddAddressOption{
		Submitter: authority, Stream: types.STREAM_ID_GROUNDWORKS,
		Recipient: recipient, Description: "grant",
	})
	require.NoError(t, err)

	e.staking.bonded[voter] = math.NewInt(bonded)
	_, err = ms.SetAllocations(e.ctx, &types.MsgSetAllocations{
		Creator: voter, Stream: types.STREAM_ID_GROUNDWORKS,
		Percentages: []types.AllocationWeight{{OptionId: added.Id, Percent: 100}},
	})
	require.NoError(t, err)
	return added.Id, voter
}

// TestRemoveGroundworksOption is the whole of the chamber's one affirmative
// power, held against the two running figures that halt the chain when they stop
// agreeing.
//
// The option being removed has weight and a balance, which is exactly what the
// idle sweep in prune.go is careful never to touch. So this path has to take
// both down itself — SummedWeight through setOption, TotalWeight by hand — and
// burn the coins behind the balance, or the EndBlock assertion fires in the
// block the removal lands in.
func TestRemoveGroundworksOption(t *testing.T) {
	e := newTestEnv(t)
	require.NoError(t, e.k.InitGenesis(e.ctx, *types.DefaultGenesis()))
	id, _ := addVotedOption(t, e, 1_000_000)

	// Let it earn something, so the forfeiture below is not vacuous. Advanced
	// from the env's own clock rather than to a fixed instant: the test context
	// starts at wall-clock now, and an absolute timestamp in the past would move
	// the chain backwards and accrue nothing.
	e.ctx = e.ctx.WithBlockTime(e.ctx.BlockTime().Add(10 * time.Minute))
	require.NoError(t, e.k.AdvanceIndex(e.ctx, types.STREAM_ID_GROUNDWORKS))

	before, err := e.k.Options.Get(e.ctx, optionKey(types.STREAM_ID_GROUNDWORKS, id))
	require.NoError(t, err)
	require.True(t, before.AmountAllocated.IsPositive(), "the option should be carrying the voter's weight")

	burnedBefore := e.bank.burned.AmountOf("uerth")
	require.NoError(t, e.k.RemoveGroundworksOption(e.ctx, chamberAddr(), id))

	opt, err := e.k.Options.Get(e.ctx, optionKey(types.STREAM_ID_GROUNDWORKS, id))
	require.NoError(t, err)
	require.True(t, opt.Removed, "the record survives, marked removed")
	require.True(t, opt.AmountAllocated.IsZero(), "a removed option carries no weight")
	require.True(t, opt.Accumulated.IsZero(), "a removed option carries no balance")

	// What it had accrued is destroyed, not merely written off: those coins were
	// minted as they accrued and are sitting on the module account.
	require.True(t, e.bank.burned.AmountOf("uerth").GT(burnedBefore),
		"the forfeited balance must be burned, not just zeroed")

	// The two figures the EndBlocker compares, and the solvency check that has
	// halted this chain before when a removal path forgot one of them.
	require.NoError(t, e.k.AssertInvariants(e.ctx))
	require.NoError(t, e.k.AssertHotInvariants(e.ctx))

	report, err := e.k.CheckStreamWeight(e.ctx, types.STREAM_ID_GROUNDWORKS)
	require.NoError(t, err)
	require.True(t, report.Declared.Equal(report.Summed),
		"declared %s vs summed %s", report.Declared, report.Summed)
}

// A voter still pointing at a struck option must not be stranded by it.
//
// This is the reason removal marks the record instead of deleting it. The split
// is replayed by resyncVoter on every stake change, out of a staking hook, with
// no transaction behind it — an error there would leave the voter unable to bond
// or unbond at all, and a double subtraction would drive the stream's weight
// below what its options hold and halt the chain.
func TestVoterSurvivesTheRemovalOfAnOptionTheyVotedFor(t *testing.T) {
	e := newTestEnv(t)
	require.NoError(t, e.k.InitGenesis(e.ctx, *types.DefaultGenesis()))
	id, voter := addVotedOption(t, e, 1_000_000)
	require.NoError(t, e.k.RemoveGroundworksOption(e.ctx, chamberAddr(), id))

	// Driven through the real staking hook rather than an internal call, because
	// the hook is where this would actually bite: x/staking calls it, and an
	// error coming back out of it is the voter's delegation failing.
	addrBz, err := e.k.addressCodec.StringToBytes(voter)
	require.NoError(t, err)
	e.staking.bonded[voter] = math.NewInt(4_000_000)
	require.NoError(t, e.k.Hooks().AfterDelegationModified(e.ctx, sdk.AccAddress(addrBz), sdk.ValAddress{}))

	require.NoError(t, e.k.AssertInvariants(e.ctx))
	report, err := e.k.CheckStreamWeight(e.ctx, types.STREAM_ID_GROUNDWORKS)
	require.NoError(t, err)
	require.True(t, report.Declared.Equal(report.Summed),
		"declared %s vs summed %s after a resync over a removed option", report.Declared, report.Summed)
	require.False(t, report.Declared.IsNegative(), "stream weight went negative: %s", report.Declared)

	// And they can still vote again, for something that exists.
	ms := NewMsgServerImpl(e.k)
	_, err = ms.SetAllocations(e.ctx, &types.MsgSetAllocations{
		Creator: voter, Stream: types.STREAM_ID_GROUNDWORKS,
		Percentages: []types.AllocationWeight{{OptionId: 1, Percent: 100}},
	})
	require.NoError(t, err)
	require.NoError(t, e.k.AssertInvariants(e.ctx))
}

// Casting a fresh vote for a struck option is refused outright. The replay path
// has to skip silently; a voter who is present to be told is told.
func TestSetAllocationsRefusesARemovedOption(t *testing.T) {
	e := newTestEnv(t)
	require.NoError(t, e.k.InitGenesis(e.ctx, *types.DefaultGenesis()))
	id, _ := addVotedOption(t, e, 1_000_000)
	require.NoError(t, e.k.RemoveGroundworksOption(e.ctx, chamberAddr(), id))

	ms := NewMsgServerImpl(e.k)
	_, other := e.addr("other")
	e.staking.bonded[other] = math.NewInt(500_000)
	_, err := ms.SetAllocations(e.ctx, &types.MsgSetAllocations{
		Creator: other, Stream: types.STREAM_ID_GROUNDWORKS,
		Percentages: []types.AllocationWeight{{OptionId: id, Percent: 100}},
	})
	require.ErrorIs(t, err, types.ErrOptionRemoved)
}

// Only the registered chamber may strike, and striking twice is not an error.
//
// Idempotence is not politeness: a removal ballot can carry against an option an
// earlier ballot already struck, and that resolution happens in an EndBlocker,
// where returning an error halts the chain.
func TestRemovalIsChamberOnlyAndIdempotent(t *testing.T) {
	e := newTestEnv(t)
	require.NoError(t, e.k.InitGenesis(e.ctx, *types.DefaultGenesis()))
	id, _ := addVotedOption(t, e, 1_000_000)

	_, stranger := e.addr("stranger")
	strangerBz, err := e.k.addressCodec.StringToBytes(stranger)
	require.NoError(t, err)
	require.ErrorIs(t, e.k.RemoveGroundworksOption(e.ctx, strangerBz, id), types.ErrInvalidSigner)

	require.NoError(t, e.k.RemoveGroundworksOption(e.ctx, chamberAddr(), id))
	require.NoError(t, e.k.RemoveGroundworksOption(e.ctx, chamberAddr(), id), "striking twice is a race, not a fault")
	require.NoError(t, e.k.RemoveGroundworksOption(e.ctx, chamberAddr(), 9999), "an option that never existed is not a fault either")
	require.NoError(t, e.k.AssertInvariants(e.ctx))
}

// A struck option is collected by the idle sweep that already exists, whatever
// kind it was — including INTEGRATED, which the sweep otherwise leaves alone
// because an idle one is idle on purpose.
func TestRemovedOptionIsPrunable(t *testing.T) {
	require.True(t, prunable(types.AllocationOption{
		Kind: types.ALLOCATION_KIND_INTEGRATED, Removed: true,
		AmountAllocated: math.ZeroInt(),
	}), "a struck INTEGRATED option would otherwise sit in state for ever")
	require.False(t, prunable(types.AllocationOption{
		Kind: types.ALLOCATION_KIND_INTEGRATED, AmountAllocated: math.ZeroInt(),
	}), "an ordinary idle INTEGRATED option is idle on purpose")
}

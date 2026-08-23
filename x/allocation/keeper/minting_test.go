package keeper

import (
	"testing"
	"time"

	"cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"

	"github.com/earth-network/earth/x/allocation/types"
)

func mustCoin(denom string, amt math.Int) sdk.Coin { return sdk.NewCoin(denom, amt) }

// addCaretakerOption gives the human stream somewhere to point weight at. The
// test env does not run InitGenesis, so the stream has no option #1 of its own.
func addCaretakerOption(t *testing.T, e *testEnv) uint64 {
	t.Helper()
	_, recipient := e.addr("caretaker-recipient")
	id, err := e.k.appendOption(e.ctx, types.STREAM_ID_CARETAKER, types.AllocationOption{
		Description: "somewhere to point",
		Kind:        types.ALLOCATION_KIND_ADDRESS,
		Recipient:   recipient,
	})
	if err != nil {
		t.Fatalf("appendOption: %v", err)
	}
	return id
}

// Emission is minted when it accrues, not when it is claimed, and the amount is
// a function of the block clock alone. That is what makes the supply figure
// true and the emission table checkable: 1 ERTH/sec per stream, verifiable
// against the chain's own clock without knowing anything about options,
// handlers, or who has claimed what.
func TestAdvanceIndexMintsTheEmission(t *testing.T) {
	e := newTestEnv(t)
	voter, _ := e.addr("voter")
	e.humans.add(voter)

	start := time.Unix(1_700_000_000, 0).UTC()
	e.ctx = e.ctx.WithBlockTime(start)
	if err := e.k.AdvanceIndex(e.ctx, types.STREAM_ID_CARETAKER); err != nil {
		t.Fatalf("AdvanceIndex: %v", err)
	}
	// Nothing yet: the first call only anchors last_upkeep, and with no weight
	// there would be nothing to divide by either.
	if got := e.bank.minted.AmountOf("uerth"); !got.IsZero() {
		t.Fatalf("first advance minted %s, want 0", got)
	}

	// Give the stream weight, then let ten seconds pass.
	id := addCaretakerOption(t, e)
	if err := e.k.resyncVoter(e.ctx, types.STREAM_ID_CARETAKER, voter,
		[]types.AllocationWeight{{OptionId: id, Percent: 100}}, math.NewInt(types.HumanVoterWeight)); err != nil {
		t.Fatalf("resyncVoter: %v", err)
	}
	e.ctx = e.ctx.WithBlockTime(start.Add(10 * time.Second))
	if err := e.k.AdvanceIndex(e.ctx, types.STREAM_ID_CARETAKER); err != nil {
		t.Fatalf("AdvanceIndex: %v", err)
	}

	want := math.NewInt(types.EmissionPerSecond).MulRaw(10)
	if got := e.bank.minted.AmountOf("uerth"); !got.Equal(want) {
		t.Fatalf("minted %s over ten seconds, want %s", got, want)
	}
}

// A stream nobody has voted in mints nothing. Emission that no option is
// directing is never created rather than created and stranded — which is now
// visible in the supply instead of being an accounting convention.
func TestUnvotedStreamMintsNothing(t *testing.T) {
	e := newTestEnv(t)
	start := time.Unix(1_700_000_000, 0).UTC()
	e.ctx = e.ctx.WithBlockTime(start)
	if err := e.k.AdvanceIndex(e.ctx, types.STREAM_ID_CARETAKER); err != nil {
		t.Fatalf("AdvanceIndex: %v", err)
	}
	e.ctx = e.ctx.WithBlockTime(start.Add(time.Hour))
	if err := e.k.AdvanceIndex(e.ctx, types.STREAM_ID_CARETAKER); err != nil {
		t.Fatalf("AdvanceIndex: %v", err)
	}
	if got := e.bank.minted.AmountOf("uerth"); !got.IsZero() {
		t.Fatalf("an unvoted stream minted %s, want 0", got)
	}
}

// The module has to be able to pay what its options say they hold. Before
// emission was minted at accrual there was nothing to compare an accrued balance
// against; this is the check that became possible.
func TestSolvencyTracksWhatTheOptionsAreOwed(t *testing.T) {
	e := newTestEnv(t)
	voter, _ := e.addr("voter")
	e.humans.add(voter)
	id := addCaretakerOption(t, e)

	start := time.Unix(1_700_000_000, 0).UTC()
	e.ctx = e.ctx.WithBlockTime(start)
	if err := e.k.AdvanceIndex(e.ctx, types.STREAM_ID_CARETAKER); err != nil {
		t.Fatalf("AdvanceIndex: %v", err)
	}
	if err := e.k.resyncVoter(e.ctx, types.STREAM_ID_CARETAKER, voter,
		[]types.AllocationWeight{{OptionId: id, Percent: 100}}, math.NewInt(types.HumanVoterWeight)); err != nil {
		t.Fatalf("resyncVoter: %v", err)
	}
	e.ctx = e.ctx.WithBlockTime(start.Add(60 * time.Second))
	if err := e.k.AdvanceIndex(e.ctx, types.STREAM_ID_CARETAKER); err != nil {
		t.Fatalf("AdvanceIndex: %v", err)
	}
	// Settle the option so its accrual is recorded rather than merely implied.
	// Re-casting the same vote is the cheapest way in: resyncVoter settles every
	// option it touches before it adjusts the weight.
	if err := e.k.resyncVoter(e.ctx, types.STREAM_ID_CARETAKER, voter,
		[]types.AllocationWeight{{OptionId: id, Percent: 100}}, math.NewInt(types.HumanVoterWeight)); err != nil {
		t.Fatalf("resyncVoter: %v", err)
	}

	rep, err := e.k.CheckSolvency(e.ctx)
	if err != nil {
		t.Fatalf("CheckSolvency: %v", err)
	}
	if rep.Broken() {
		t.Fatalf("solvent module reported short by %s (accrued %s, residue %s, held %s)",
			rep.Short, rep.Accrued, rep.Residue, rep.Held)
	}
	if !rep.Accrued.IsPositive() {
		t.Fatalf("nothing accrued, so the check proves nothing")
	}

	// Take the coins away and the check must say so. This is the failure it
	// exists for: a payout path that spends what a mint never created.
	e.bank.debit(e.bank.modBal)
	rep, err = e.k.CheckSolvency(e.ctx)
	if err != nil {
		t.Fatalf("CheckSolvency: %v", err)
	}
	if !rep.Broken() {
		t.Fatalf("an emptied module account must report short, got %+v", rep)
	}
}

// The genesis seed puts real coins behind the registration-reward pool. A pool
// that says it holds a quarter of the pre-mine has to be able to pay it.
func TestGenesisSeedIsBackedByABalance(t *testing.T) {
	e := newTestEnv(t)
	seed := math.NewInt(630_720_000_000_000)

	gen := types.DefaultGenesis()
	gen.RegistrationRewardSeed = seed
	if err := gen.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	// The genesis file funds the module account alongside the seed; here that is
	// the bank stub standing in for it.
	e.bank.fundModule(mustCoin("uerth", seed))
	if err := e.k.InitGenesis(e.ctx, *gen); err != nil {
		t.Fatalf("InitGenesis: %v", err)
	}

	opt, err := e.k.Options.Get(e.ctx, optionKey(types.STREAM_ID_CARETAKER, types.RegistrationRewardOptionID))
	if err != nil {
		t.Fatalf("option #1: %v", err)
	}
	if !opt.Accumulated.Equal(seed) {
		t.Fatalf("option #1 accrued %s, want the seed %s", opt.Accumulated, seed)
	}

	rep, err := e.k.CheckSolvency(e.ctx)
	if err != nil {
		t.Fatalf("CheckSolvency: %v", err)
	}
	if rep.Broken() {
		t.Fatalf("the seed must be backed by the balance genesis funded: short %s", rep.Short)
	}

	// A seed on top of an import would credit the pool twice.
	gen.Streams = []types.StreamState{{Stream: types.STREAM_ID_CARETAKER, RewardIndex: math.ZeroInt(), TotalWeight: math.ZeroInt()}}
	if err := gen.Validate(); err == nil {
		t.Fatal("a seed alongside imported streams must be rejected")
	}
}

// The truncation dust has to reach the community pool without depending on
// anybody having voted for the emergency fund.
//
// This is the bug the separate sink exists for: an INTEGRATED handler is only
// invoked when its option has accrued something, so routing the residue through
// the emergency fund left it stranded on the module account for as long as that
// fund had no votes — which at launch is from genesis onward.
func TestResidueReachesTheCommunityPoolWithoutVotes(t *testing.T) {
	e := newTestEnv(t)
	e.k.RegisterResidueSink(ResidueSink(e.k, e.pool))

	voter, _ := e.addr("voter")
	e.humans.add(voter)
	id := addCaretakerOption(t, e)

	// A weight that does not divide the emission evenly, so the index truncates
	// and leaves something behind.
	start := time.Unix(1_700_000_000, 0).UTC()
	e.ctx = e.ctx.WithBlockTime(start)
	if err := e.k.AdvanceIndex(e.ctx, types.STREAM_ID_CARETAKER); err != nil {
		t.Fatalf("AdvanceIndex: %v", err)
	}
	if err := e.k.resyncVoter(e.ctx, types.STREAM_ID_CARETAKER, voter,
		[]types.AllocationWeight{{OptionId: id, Percent: 100}}, math.NewInt(3)); err != nil {
		t.Fatalf("resyncVoter: %v", err)
	}
	e.ctx = e.ctx.WithBlockTime(start.Add(time.Second))
	if err := e.k.AdvanceIndex(e.ctx, types.STREAM_ID_CARETAKER); err != nil {
		t.Fatalf("AdvanceIndex: %v", err)
	}

	residue, err := e.k.GetResidue(e.ctx)
	if err != nil {
		t.Fatalf("GetResidue: %v", err)
	}
	if !residue.IsPositive() {
		t.Skip("no truncation at this weight; nothing to sweep")
	}

	// Nobody has voted for the emergency fund, so its handler never runs.
	if err := e.k.SweepResidue(e.ctx); err != nil {
		t.Fatalf("SweepResidue: %v", err)
	}
	if got := e.pool.funded.AmountOf("uerth"); !got.Equal(residue) {
		t.Fatalf("community pool got %s, want the residue %s", got, residue)
	}
	after, err := e.k.GetResidue(e.ctx)
	if err != nil {
		t.Fatalf("GetResidue: %v", err)
	}
	if !after.IsZero() {
		t.Fatalf("residue still %s after the sweep", after)
	}
	if e.pool.sender.String() != authtypes.NewModuleAddress(types.ModuleName).String() {
		t.Fatalf("funded by %s, want the allocation module account", e.pool.sender)
	}
}

// No sink registered is not a broken chain: the dust stays put rather than the
// EndBlocker failing and halting.
func TestSweepResidueWithoutASinkIsHarmless(t *testing.T) {
	e := newTestEnv(t)
	if err := e.k.SweepResidue(e.ctx); err != nil {
		t.Fatalf("SweepResidue with no sink: %v", err)
	}
}

// Both allocation pillars feed the same residue counter, and one sweep drains
// them together. Residue is global rather than per stream because it is dust
// either way and nothing about it is worth knowing per stream — but that only
// holds if both streams actually reach it.
func TestResidueCollectsFromBothStreams(t *testing.T) {
	e := newTestEnv(t)
	e.k.RegisterResidueSink(ResidueSink(e.k, e.pool))

	human, _ := e.addr("human")
	staker, _ := e.addr("staker")
	e.humans.add(human)

	caretakerOpt := addCaretakerOption(t, e)
	groundworksOpt := addDeadOption(t, e, "somewhere for stake to point")

	start := time.Unix(1_700_000_000, 0).UTC()
	e.ctx = e.ctx.WithBlockTime(start)
	for _, st := range types.Streams {
		if err := e.k.AdvanceIndex(e.ctx, st); err != nil {
			t.Fatalf("AdvanceIndex %s: %v", st, err)
		}
	}

	// Weights that do not divide the emission evenly, so both indexes truncate.
	if err := e.k.resyncVoter(e.ctx, types.STREAM_ID_CARETAKER, human,
		[]types.AllocationWeight{{OptionId: caretakerOpt, Percent: 100}}, math.NewInt(3)); err != nil {
		t.Fatalf("resyncVoter caretaker: %v", err)
	}
	if err := e.k.resyncVoter(e.ctx, types.STREAM_ID_GROUNDWORKS, staker,
		[]types.AllocationWeight{{OptionId: groundworksOpt, Percent: 100}}, math.NewInt(7)); err != nil {
		t.Fatalf("resyncVoter groundworks: %v", err)
	}

	// Advance one stream at a time, checking the counter moves for each.
	e.ctx = e.ctx.WithBlockTime(start.Add(time.Second))
	if err := e.k.AdvanceIndex(e.ctx, types.STREAM_ID_CARETAKER); err != nil {
		t.Fatalf("AdvanceIndex caretaker: %v", err)
	}
	afterCaretaker, err := e.k.GetResidue(e.ctx)
	if err != nil {
		t.Fatalf("GetResidue: %v", err)
	}
	if !afterCaretaker.IsPositive() {
		t.Fatal("the caretaker stream contributed no residue; the test proves nothing")
	}

	if err := e.k.AdvanceIndex(e.ctx, types.STREAM_ID_GROUNDWORKS); err != nil {
		t.Fatalf("AdvanceIndex groundworks: %v", err)
	}
	afterBoth, err := e.k.GetResidue(e.ctx)
	if err != nil {
		t.Fatalf("GetResidue: %v", err)
	}
	if !afterBoth.GT(afterCaretaker) {
		t.Fatalf("the groundworks stream added nothing: %s then %s", afterCaretaker, afterBoth)
	}

	// One sweep takes both.
	if err := e.k.SweepResidue(e.ctx); err != nil {
		t.Fatalf("SweepResidue: %v", err)
	}
	if got := e.pool.funded.AmountOf("uerth"); !got.Equal(afterBoth) {
		t.Fatalf("community pool got %s, want both streams' residue %s", got, afterBoth)
	}
	left, err := e.k.GetResidue(e.ctx)
	if err != nil {
		t.Fatalf("GetResidue: %v", err)
	}
	if !left.IsZero() {
		t.Fatalf("residue still %s after the sweep", left)
	}
}

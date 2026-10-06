package app_test

import (
	"testing"

	allocationtypes "github.com/earth-network/earth/x/allocation/types"
	dextypes "github.com/earth-network/earth/x/dex/types"
	personhoodtypes "github.com/earth-network/earth/x/personhood/types"
	shieldedtypes "github.com/earth-network/earth/x/shielded/types"
)

// The chain's automatic per-block work budget, in one place.
//
// Every module's BeginBlocker and EndBlocker runs on an infinite gas meter and
// consumes no block gas. Block gas bounds transactions; it does not bound this.
// So the only ceiling on automatic per-block work is the set of caps below, and
// the number that actually decides how long a block takes is their SUM — which
// is not written in any module, because no module can see the others.
//
// That is what this file is. It is not testing behaviour; it is the place the
// sum is chosen, so that raising any one cap is a deliberate act with a failing
// test in front of it rather than a local decision with a global effect.
//
// Adding a new per-block loop means adding its cap here. A loop that cannot be
// given a cap does not belong in a BeginBlocker.

// maxRetirementsPerBlock is the ceiling on record-retiring work per block:
//
//	registrations   x/personhood  BeginBlock  shared by the expiry sweep and the
//	                              revoked-signer purge (one budget, spent
//	                              purge-first)
//	unbondings      x/dex         EndBlock    matured liquidity withdrawals
//	dead options    x/allocation  BeginBlock  permissionlessly-added options that
//	                              have carried no weight and owed nothing for the
//	                              whole grace period
//	expired roots   x/shielded    EndBlock    note-tree anchors past the window
//	                              (one is recorded per block at most, so the cap
//	                              only matters draining a backlog)
//
// Each unit is roughly a dozen store operations plus a settle, so this is a
// small fraction of a block at the numbers below. It is stated as a sum because
// the two land in the same block and neither module knows about the other.
const maxRetirementsPerBlock = personhoodtypes.DefaultRegistrationSweepLimit +
	dextypes.LpUnbondSweepLimit +
	allocationtypes.OptionPruneSweepLimit +
	shieldedtypes.RootPruneLimit

func TestPerBlockWorkBudget(t *testing.T) {
	// The individual caps. Changing one of these is fine; changing it without
	// noticing what it does to the total is what this guards against.
	if got := personhoodtypes.DefaultRegistrationSweepLimit; got != 100 {
		t.Errorf("registration sweep budget is %d, expected 100 — update the total below", got)
	}
	if got := dextypes.LpUnbondSweepLimit; got != 50 {
		t.Errorf("lp unbonding sweep cap is %d, expected 50 — update the total below", got)
	}
	if got := allocationtypes.OptionPruneSweepLimit; got != 20 {
		t.Errorf("option prune sweep cap is %d, expected 20 — update the total below", got)
	}
	if got := shieldedtypes.RootPruneLimit; got != 20 {
		t.Errorf("shielded root prune cap is %d, expected 20 — update the total below", got)
	}

	// The sum, which is the number that matters and which nothing else states.
	//
	// 190 retirements is on the order of a couple of thousand store operations,
	// comfortably inside a block. The point of the bound is not that 150 is
	// special — it is that the figure exists at all, and that a future sweep
	// cannot quietly push it up by adding a cap of its own.
	const budgetCeiling = 200
	if maxRetirementsPerBlock > budgetCeiling {
		t.Fatalf("per-block retirement budget is now %d, above the %d this chain has chosen. "+
			"BeginBlock and EndBlock consume no block gas, so nothing else bounds this: "+
			"either lower a cap, or raise the ceiling deliberately and say why",
			maxRetirementsPerBlock, budgetCeiling)
	}
}

// TestEveryPerBlockLoopHasACap is a checklist rather than an assertion, kept
// next to the budget so the two are read together.
//
// Bounded, and by what:
//
//	x/personhood  BeginBlock  expiry sweep + revoked-signer purge
//	                          -> registration_sweep_limit (shared)
//	              BeginBlock  ANML buyback  -> O(1)
//	x/dex         EndBlock    matured unbondings -> LpUnbondSweepLimit
//	              EndBlock    due auction settle -> one-shot, then never again
//	              EndBlock    POL burn -> O(schedules), and schedules are created
//	                          only at genesis and at auction settlement, never by
//	                          a user
//	x/allocation  BeginBlock  stream upkeep -> O(streams x integrated options),
//	                          both governance-controlled
//	              EndBlock    weight invariant -> O(1) per stream. It compares
//	                          two maintained aggregates rather than walking the
//	                          options, which matters because adding an option is
//	                          permissionless. The walk lives in AssertInvariants,
//	                          off the block path. See x/allocation/keeper/
//	                          invariants.go and TestInvariantCostIsFlatInOptionCount.
//	x/earth       EndBlock    fee split -> O(fee denoms in one block)
//	x/mint        BeginBlock  emission -> O(1)
//	x/shielded    EndBlock    anchor record -> O(1); root prune ->
//	                          RootPruneLimit; turnstile check -> the denoms
//	                          moved this block, each move already paid for by
//	                          the tx or capped by the module that made it
//
//	x/dex         EndBlock    solvency -> O(1) for ERTH, plus the pools this
//	                          block wrote and a fixed rotation of a few others.
//	                          See x/dex/keeper/solvency.go and solvency_test.go.
func TestEveryPerBlockLoopHasACap(t *testing.T) {
	t.Log("see the comment above: this is a checklist, reviewed when a per-block loop is added")
}

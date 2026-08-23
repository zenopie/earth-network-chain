package app_test

import (
	"testing"

	dextypes "github.com/earth-network/earth/x/dex/types"
	personhoodtypes "github.com/earth-network/earth/x/personhood/types"
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
//
// Each unit is roughly a dozen store operations plus a settle, so this is a
// small fraction of a block at the numbers below. It is stated as a sum because
// the two land in the same block and neither module knows about the other.
const maxRetirementsPerBlock = personhoodtypes.DefaultRegistrationSweepLimit +
	dextypes.LpUnbondSweepLimit

func TestPerBlockWorkBudget(t *testing.T) {
	// The individual caps. Changing one of these is fine; changing it without
	// noticing what it does to the total is what this guards against.
	if got := personhoodtypes.DefaultRegistrationSweepLimit; got != 100 {
		t.Errorf("registration sweep budget is %d, expected 100 — update the total below", got)
	}
	if got := dextypes.LpUnbondSweepLimit; got != 50 {
		t.Errorf("lp unbonding sweep cap is %d, expected 50 — update the total below", got)
	}

	// The sum, which is the number that matters and which nothing else states.
	//
	// 150 retirements is on the order of a couple of thousand store operations,
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
//
//	x/dex         EndBlock    solvency -> O(1) for ERTH, plus the pools this
//	                          block wrote and a fixed rotation of a few others.
//	                          See TestPoolSetIsNoLongerUnbounded.
func TestEveryPerBlockLoopHasACap(t *testing.T) {
	t.Log("see the comment above: this is a checklist, reviewed when a per-block loop is added")
}

// TestPoolSetIsNoLongerUnbounded records why the pool set stopped mattering.
//
// x/dex asserts solvency every block, and that check used to walk every pool —
// twice, since comparing against GetAllBalances is O(denoms held) as well. With
// CreatePool permissionless, one pool per denom, no minimum liquidity and no
// scarcity of denoms behind IBC, a pool was a one-time gas-metered cost that
// bought permanent unmetered work on every future block, including after its
// creator withdrew and left.
//
// It now decomposes by asset instead: ERTH is commingled so it carries a running
// total compared against one bank balance, and every other denom belongs to
// exactly one pool, so a pool's own reserve is the whole obligation for it. Only
// the pools a block actually wrote are checked, which is sound because the dex
// module account is blocked from receiving outside transfers — nothing but the
// module can move its coins, so an untouched pool cannot have drifted. A fixed
// rotation re-checks a few others per block as a backstop.
//
// The measurement lives in x/dex/keeper/solvency_test.go, which pins the cost as
// flat in the pool count rather than merely small today.
func TestPoolSetIsNoLongerUnbounded(t *testing.T) {
	t.Log("solvency is O(1) plus pools-written-this-block; see x/dex/keeper/solvency.go")
}

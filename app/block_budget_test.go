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
//	x/earth       EndBlock    fee split -> O(fee denoms in one block)
//	x/mint        BeginBlock  emission -> O(1)
//
// NOT bounded, and known:
//
//	x/dex         EndBlock    AssertHotInvariants -> O(pools), and pools are
//	                          permissionless. See TestPoolSetIsTheOneUnboundedLoop.
func TestEveryPerBlockLoopHasACap(t *testing.T) {
	t.Log("see the comment above: this is a checklist, reviewed when a per-block loop is added")
}

// TestPoolSetIsTheOneUnboundedLoop records the single per-block loop on this
// chain whose size a user can grow.
//
// x/dex's EndBlocker asserts its solvency and volume invariants every block, and
// both walk the whole pool set. That is deliberate and the reasoning in
// invariants.go is sound — the check is what stands between a mispriced pool and
// a silent drain, and it is correctly kept to the two O(pools) checks with the
// unbonding walk left off the hot path.
//
// What is not bounded is the pool set. CreatePool is permissionless once the
// auction has settled, one pool per token denom, with no minimum liquidity — so
// a pool costs two units of dust plus gas. Denoms are not scarce either: IBC
// vouchers mint a fresh denom per base denom, and a counterparty chain the
// attacker controls can supply as many as they like.
//
// The asymmetry is the problem. Creating a pool is a one-time, gas-metered cost.
// The work it adds to the EndBlocker is permanent, unmetered, and paid by every
// validator on every block thereafter, including after the creator withdraws
// their liquidity and walks away.
//
// This test does not fail. It exists so the assumption is written down next to
// the budget rather than living in one person's head.
func TestPoolSetIsTheOneUnboundedLoop(t *testing.T) {
	t.Log("pool count is bounded only by denom availability and dust; see comment above")
}

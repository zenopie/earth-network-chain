package keeper

import (
	"context"
	"errors"
	"sort"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/x/earth/types"
)

// Counting what the chain destroys.
//
// Ethereum does not do this, and it is worth being clear about why the same
// answer does not work here. There the burn is one mechanism — the EIP-1559
// base fee — and its size is a product of two fields in the block header, so
// anyone can total it by walking headers and nothing has to be stored. This
// chain burns in five places for five different reasons, and three of them run
// in EndBlock as a function of the clock and of pool reserves. Nothing in a
// header or a transaction says how much went. x/bank knows only what supply
// remains.
//
// So the counters are kept as the burns happen. They cost one write per burn
// and they are exact, which is the whole of their justification: a figure that
// says "this much has been destroyed" is worth nothing if it is an estimate.
//
// Attribution is kept alongside the total because the mechanisms mean different
// things. Gas burned is congestion, swap fees are volume, a pol retirement is a
// schedule running whether anyone trades or not — collapsing them into one
// number would hide every one of those signals.
//
// SCOPE: these are tokenomics burns, not every way ERTH can leave the supply.
//
// What is counted is supply this chain destroys on purpose, as part of how the
// token is designed to work — fees returned to holders by removing them from
// circulation, liquidity retired on schedule, an emission spent buying its own
// token back. That is the question the counters answer: where is value being
// destroyed, and by which mechanism.
//
// Deliberately NOT counted, because they are not tokenomics:
//
//   - Staking slashing (x/staking). A penalty for downtime or equivocation. It
//     burns real ERTH and fires in ordinary operation, but it prices validator
//     misbehaviour, not the token. It belongs to the security model.
//   - Vetoed proposal deposits (x/gov, burn_vote_veto is true in genesis). A
//     forfeiture in a governance process, for the same reason.
//   - wasmd's vesting-account prune, which burns the balance of a vesting
//     account that a contract is instantiated over. Not a mechanism at all —
//     it needs an account deliberately parked at a contract's future address.
//
// SourceWasm sits on the near side of that line but is worth reading carefully:
// a contract burning its own coins is real, economically-driven destruction and
// belongs in the total, but it is third-party behaviour, not a lever this
// chain's tokenomics controls. The by_source split keeps it distinguishable.
//
// The consequence to hold onto: this total is NOT all supply destruction, so
// current_supply cannot be reconstructed from genesis supply, issuance and
// these figures. x/bank is the authority on how much ERTH exists. These
// counters answer why the tokenomics moved it, not what the number is.

// RecordBurn adds coins to the running total for one source.
//
// Call it immediately after the BurnCoins it describes, in the same context, so
// that a burn which is later discarded takes its counter with it. The buyback
// in x/personhood burns inside a cache context it throws away on any failure;
// recording against the parent context there would count trades that never
// happened.
//
// Non-positive amounts are dropped rather than rejected: several callers hand
// over a coin set that is legitimately empty on the block in question, and an
// error would make every one of them write a branch to avoid it.
func (k Keeper) RecordBurn(ctx context.Context, source string, coins sdk.Coins) error {
	for _, c := range coins {
		if !c.Amount.IsPositive() {
			continue
		}
		key := collections.Join(source, c.Denom)
		total, err := k.Burned.Get(ctx, key)
		if err != nil {
			if !errors.Is(err, collections.ErrNotFound) {
				return err
			}
			total = math.ZeroInt()
		}
		if err := k.Burned.Set(ctx, key, total.Add(c.Amount)); err != nil {
			return err
		}
	}
	return nil
}

// BurnedBySource returns every source that has ever burned, sorted by source
// name, with its per-denom totals.
//
// A source with no burns is absent rather than zero. The distinction matters on
// a young chain: "the auction has not settled yet" and "the auction settled and
// retired nothing" are different facts, and only the first should read as empty.
func (k Keeper) BurnedBySource(ctx context.Context) ([]types.BurnTotal, error) {
	bySource := map[string]sdk.Coins{}
	if err := k.Burned.Walk(ctx, nil, func(key collections.Pair[string, string], amount math.Int) (bool, error) {
		source, denom := key.K1(), key.K2()
		bySource[source] = bySource[source].Add(sdk.NewCoin(denom, amount))
		return false, nil
	}); err != nil {
		return nil, err
	}

	// The walk yields keys in the store's byte order, which sorts by source and
	// then denom already. Sorting again is cheap insurance against that becoming
	// untrue, and against callers depending on an order the store never promised.
	sources := make([]string, 0, len(bySource))
	for source := range bySource {
		sources = append(sources, source)
	}
	sort.Strings(sources)

	out := make([]types.BurnTotal, 0, len(sources))
	for _, source := range sources {
		out = append(out, types.BurnTotal{Source: source, Amount: bySource[source]})
	}
	return out, nil
}

// TotalBurned is every source summed, one entry per denom.
func (k Keeper) TotalBurned(ctx context.Context) (sdk.Coins, error) {
	total := sdk.NewCoins()
	if err := k.Burned.Walk(ctx, nil, func(key collections.Pair[string, string], amount math.Int) (bool, error) {
		total = total.Add(sdk.NewCoin(key.K2(), amount))
		return false, nil
	}); err != nil {
		return nil, err
	}
	return total, nil
}

// setBurned replaces one source's counters wholesale. Used by InitGenesis; the
// running chain only ever adds.
func (k Keeper) setBurned(ctx context.Context, source string, coins sdk.Coins) error {
	for _, c := range coins {
		if !c.Amount.IsPositive() {
			continue
		}
		if err := k.Burned.Set(ctx, collections.Join(source, c.Denom), c.Amount); err != nil {
			return err
		}
	}
	return nil
}

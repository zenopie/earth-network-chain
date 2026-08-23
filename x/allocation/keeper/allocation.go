package keeper

import (
	"context"
	"errors"
	"time"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/x/allocation/types"
)

// indexPrecision scales a stream's reward index to preserve fractional
// per-weight rewards (mirrors the reference contract's INDEX_PRECISION).
var indexPrecision = math.NewInt(1_000_000_000_000_000_000) // 1e18

// key is the store key for a stream. Collections keys are plain integers; the
// enum only exists at the message boundary.
func key(stream types.StreamId) uint32 { return uint32(stream) }

// optionKey is the store key for one option within one stream.
func optionKey(stream types.StreamId, id uint64) collections.Pair[uint32, uint64] {
	return collections.Join(key(stream), id)
}

// voterKey is the store key for one voter within one stream.
func voterKey(stream types.StreamId, addr []byte) collections.Pair[uint32, []byte] {
	return collections.Join(key(stream), addr)
}

// ValidateStream rejects a message that names no stream or an unknown one.
func ValidateStream(stream types.StreamId) error { return types.ValidateStreamId(stream) }

// --- per-stream getters with zero defaults ---

func (k Keeper) getRewardIndex(ctx context.Context, stream types.StreamId) (math.Int, error) {
	v, err := k.RewardIndex.Get(ctx, key(stream))
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return math.ZeroInt(), nil
		}
		return math.Int{}, err
	}
	return v, nil
}

func (k Keeper) getTotalWeight(ctx context.Context, stream types.StreamId) (math.Int, error) {
	v, err := k.TotalWeight.Get(ctx, key(stream))
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return math.ZeroInt(), nil
		}
		return math.Int{}, err
	}
	return v, nil
}

func (k Keeper) getSummedWeight(ctx context.Context, stream types.StreamId) (math.Int, error) {
	v, err := k.SummedWeight.Get(ctx, key(stream))
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return math.ZeroInt(), nil
		}
		return math.Int{}, err
	}
	return v, nil
}

// setOption is the only writer of an option record.
//
// Every write goes through here so the stream's running weight sum moves with
// it, which is what lets the EndBlocker check the stream in O(1) instead of
// walking every option — see invariants.go. Calling k.Options.Set directly is
// therefore a bug: the sum would stop meaning what it says, and the next block
// would report the stream as drifted. CheckStreamWeight is the exhaustive walk
// that catches one, and the tests run it after every operation.
//
// The sum is never clamped. TotalWeight is, in resyncVoter, and that clamp is
// precisely the thing worth catching: it turns a negative total into zero and
// erases the evidence. A sum that is allowed to go negative keeps it.
func (k Keeper) setOption(ctx context.Context, stream types.StreamId, opt types.AllocationOption) error {
	prev := math.ZeroInt()
	prevAcc := math.ZeroInt()
	if p, err := k.Options.Get(ctx, optionKey(stream, opt.Id)); err == nil {
		if !p.AmountAllocated.IsNil() {
			prev = p.AmountAllocated
		}
		prevAcc = accruedOf(p)
	} else if !errors.Is(err, collections.ErrNotFound) {
		return err
	}

	cur := math.ZeroInt()
	if !opt.AmountAllocated.IsNil() {
		cur = opt.AmountAllocated
	}

	if delta := cur.Sub(prev); !delta.IsZero() {
		sum, err := k.getSummedWeight(ctx, stream)
		if err != nil {
			return err
		}
		if err := k.SummedWeight.Set(ctx, key(stream), sum.Add(delta)); err != nil {
			return err
		}
	}

	// The accrued balances are summed here for the same reason and by the same
	// method, but they answer a different question: emission is minted as it
	// accrues, so what the options say they hold is a claim on coins this module
	// is actually carrying. See CheckSolvency.
	if delta := accruedOf(opt).Sub(prevAcc); !delta.IsZero() {
		acc, err := k.GetSummedAccrued(ctx)
		if err != nil {
			return err
		}
		if err := k.SummedAccrued.Set(ctx, acc.Add(delta)); err != nil {
			return err
		}
	}

	if err := k.Options.Set(ctx, optionKey(stream, opt.Id), opt); err != nil {
		return err
	}
	// The removal schedule is maintained here rather than anywhere else for the
	// same reason the sum is: one writer, so the schedule cannot describe a
	// record that has since changed underneath it. See prune.go.
	return k.refreshPruneSchedule(ctx, stream, opt)
}

// accruedOf reads an option's accrued balance, treating an unset one as zero.
func accruedOf(opt types.AllocationOption) math.Int {
	if opt.Accumulated.IsNil() {
		return math.ZeroInt()
	}
	return opt.Accumulated
}

// GetSummedAccrued reports the running sum of every option's accrued balance,
// across both streams. Zero before anything has accrued.
func (k Keeper) GetSummedAccrued(ctx context.Context) (math.Int, error) {
	v, err := k.SummedAccrued.Get(ctx)
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return math.ZeroInt(), nil
		}
		return math.Int{}, err
	}
	if v.IsNil() {
		return math.ZeroInt(), nil
	}
	return v, nil
}

func (k Keeper) getLastUpkeep(ctx context.Context, stream types.StreamId) (int64, error) {
	v, err := k.LastUpkeep.Get(ctx, key(stream))
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return 0, nil
		}
		return 0, err
	}
	return v, nil
}

// getEpoch returns the stream's current allocation epoch (0 before any reset).
func (k Keeper) getEpoch(ctx context.Context, stream types.StreamId) (uint64, error) {
	v, err := k.Epoch.Get(ctx, key(stream))
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return 0, nil
		}
		return 0, err
	}
	return v, nil
}

// AdvanceIndex advances a stream's reward index by the emission (1 ERTH/sec)
// accrued since its last upkeep, split across the stream's total voting weight,
// and mints that emission.
//
// Minting here — at the moment the emission is earned rather than the moment
// somebody collects it — is what makes this module the sole issuer of allocation
// ERTH and the reported supply a true one.
//
// The amount is a function of the block clock and nothing else:
// EmissionPerSecond times the elapsed interval. It does not depend on how many
// options there are, whether their handlers resolve, or whether anyone ever
// claims. So the emission table can be checked against the chain's own clock,
// which was not true when four different modules minted at four different
// moments for four different reasons.
//
// A stream with no weight mints nothing. That is deliberate and it is visible
// now: emission nobody has voted for is never created, rather than created and
// stranded. Before the first human registers, the caretaker stream issues zero.
//
// The index is truncated to indexPrecision, so the options between them can only
// collect delta*total, a hair under the reward. That hair is minted too — the
// emission is 1 ERTH/sec, not 1 ERTH/sec less rounding — and set aside as
// residue for the community pool, the same place x/distribution puts the dust
// from its own per-validator split.
//
// Exported because x/personhood has to settle the human stream before it retires
// a lapsed registration: the vote weight being unwound has to be credited
// against a current index or the option loses the emission it earned this block.
func (k Keeper) AdvanceIndex(ctx context.Context, stream types.StreamId) error {
	now := sdk.UnwrapSDKContext(ctx).BlockTime().UnixNano()
	last, err := k.getLastUpkeep(ctx, stream)
	if err != nil {
		return err
	}
	if last != 0 && now > last {
		total, err := k.getTotalWeight(ctx, stream)
		if err != nil {
			return err
		}
		if total.IsPositive() {
			reward := math.NewInt(types.EmissionPerSecond).MulRaw(now - last).QuoRaw(int64(time.Second))
			if reward.IsPositive() {
				idx, err := k.getRewardIndex(ctx, stream)
				if err != nil {
					return err
				}
				delta := reward.Mul(indexPrecision).Quo(total)
				if err := k.RewardIndex.Set(ctx, key(stream), idx.Add(delta)); err != nil {
					return err
				}
				if err := k.mintEmission(ctx, reward, delta.Mul(total).Quo(indexPrecision)); err != nil {
					return err
				}
			}
		}
	}
	return k.LastUpkeep.Set(ctx, key(stream), now)
}

// mintEmission issues one interval's emission into the module account and books
// the part of it the options cannot reach.
func (k Keeper) mintEmission(ctx context.Context, reward, collectable math.Int) error {
	denom, err := k.HubDenom(ctx)
	if err != nil {
		return err
	}
	if err := k.bankKeeper.MintCoins(ctx, types.ModuleName,
		sdk.NewCoins(sdk.NewCoin(denom, reward))); err != nil {
		return err
	}
	dust := reward.Sub(collectable)
	if !dust.IsPositive() {
		return nil
	}
	held, err := k.GetResidue(ctx)
	if err != nil {
		return err
	}
	return k.Residue.Set(ctx, held.Add(dust))
}

// GetResidue reports the minted emission no option can collect, awaiting a sweep
// to the community pool. Zero before anything has accrued.
func (k Keeper) GetResidue(ctx context.Context) (math.Int, error) {
	v, err := k.Residue.Get(ctx)
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return math.ZeroInt(), nil
		}
		return math.Int{}, err
	}
	if v.IsNil() {
		return math.ZeroInt(), nil
	}
	return v, nil
}

// nextOptionID hands out the next option id within a stream. Ids restart per
// stream, so the human stream's option #1 and the capital stream's option #1 are
// two different options.
func (k Keeper) nextOptionID(ctx context.Context, stream types.StreamId) (uint64, error) {
	cur, err := k.OptionSeq.Get(ctx, key(stream))
	if err != nil {
		if !errors.Is(err, collections.ErrNotFound) {
			return 0, err
		}
		cur = 0
	}
	next := cur + 1
	return next, k.OptionSeq.Set(ctx, key(stream), next)
}

// appendOption stores a new allocation option in a stream, assigning it the next
// id and initializing its accounting fields to the stream's current reward index.
func (k Keeper) appendOption(ctx context.Context, stream types.StreamId, opt types.AllocationOption) (uint64, error) {
	id, err := k.nextOptionID(ctx, stream)
	if err != nil {
		return 0, err
	}
	rewardIndex, err := k.getRewardIndex(ctx, stream)
	if err != nil {
		return 0, err
	}
	opt.Id = id
	opt.Stream = stream
	opt.AmountAllocated = math.ZeroInt()
	opt.Accumulated = math.ZeroInt()
	opt.LastRewardIndex = rewardIndex
	if err := k.setOption(ctx, stream, opt); err != nil {
		return 0, err
	}
	if opt.Kind == types.ALLOCATION_KIND_INTEGRATED {
		if err := k.IntegratedOptions.Set(ctx, optionKey(stream, id)); err != nil {
			return 0, err
		}
	}
	return id, nil
}

// settleOption accrues an option's pending ERTH up to rewardIndex. Port of the
// reference `settle_allocation`.
func settleOption(opt *types.AllocationOption, rewardIndex math.Int) {
	if opt.AmountAllocated.IsPositive() {
		delta := rewardIndex.Sub(opt.LastRewardIndex)
		opt.Accumulated = opt.Accumulated.Add(opt.AmountAllocated.Mul(delta).Quo(indexPrecision))
	}
	opt.LastRewardIndex = rewardIndex
}

// resyncVoter re-applies a voter's split within one stream at the given weight:
// it removes the voter's previous contribution from each option and from the
// stream total, then adds the new contribution. The stream's reward index must
// already be current (call AdvanceIndex first). Ports `subtract_old_allocations`
// + `add_new_allocations`.
//
// weight arrives already resolved — the human stream's fixed per-human weight,
// or a staker's normalized bonded stake. Nothing here knows which stream it is
// working on, which is the point.
func (k Keeper) resyncVoter(ctx context.Context, stream types.StreamId, addrBz []byte, percentages []types.AllocationWeight, weight math.Int) error {
	rewardIndex, err := k.getRewardIndex(ctx, stream)
	if err != nil {
		return err
	}
	total, err := k.getTotalWeight(ctx, stream)
	if err != nil {
		return err
	}
	epoch, err := k.getEpoch(ctx, stream)
	if err != nil {
		return err
	}

	// Remove the voter's previous contribution, but only if it belongs to the
	// current epoch. A reset already zeroed the aggregates wholesale, so
	// subtracting a stale record's weight again would drive options negative.
	if old, err := k.Voters.Get(ctx, voterKey(stream, addrBz)); err == nil && old.Epoch == epoch {
		for _, w := range old.Percentages {
			opt, err := k.Options.Get(ctx, optionKey(stream, w.OptionId))
			if err != nil {
				continue
			}
			settleOption(&opt, rewardIndex)
			amt := old.Weight.MulRaw(int64(w.Percent)).QuoRaw(100)
			opt.AmountAllocated = opt.AmountAllocated.Sub(amt)
			total = total.Sub(amt)
			if err := k.setOption(ctx, stream, opt); err != nil {
				return err
			}
		}
	} else if err != nil && !errors.Is(err, collections.ErrNotFound) {
		return err
	}

	// Add the new contribution.
	for _, w := range percentages {
		opt, err := k.Options.Get(ctx, optionKey(stream, w.OptionId))
		if err != nil {
			return types.ErrOptionNotFound.Wrapf("option %d", w.OptionId)
		}
		settleOption(&opt, rewardIndex)
		amt := weight.MulRaw(int64(w.Percent)).QuoRaw(100)
		opt.AmountAllocated = opt.AmountAllocated.Add(amt)
		total = total.Add(amt)
		if err := k.setOption(ctx, stream, opt); err != nil {
			return err
		}
	}

	if total.IsNegative() {
		total = math.ZeroInt()
	}
	if err := k.TotalWeight.Set(ctx, key(stream), total); err != nil {
		return err
	}

	if len(percentages) == 0 || !weight.IsPositive() {
		return k.Voters.Remove(ctx, voterKey(stream, addrBz))
	}
	return k.Voters.Set(ctx, voterKey(stream, addrBz), types.Voter{Percentages: percentages, Weight: weight, Epoch: epoch})
}

// ClearVoter retires an address's vote in one stream, returning the weight it
// was carrying to the stream. Exported for x/personhood, which has to do this
// when a registration lapses: left in place, a lapsed human's split keeps
// diluting every voter who is still verified, while the options it named keep
// accruing ERTH nobody living directs.
//
// The caller must have advanced the stream's index for this block.
func (k Keeper) ClearVoter(ctx context.Context, stream types.StreamId, addrBz []byte) error {
	return k.resyncVoter(ctx, stream, addrBz, nil, math.ZeroInt())
}

// DrawFromOption settles an option and withdraws `ppm` parts-per-million of its
// accrued ERTH, returning the amount withdrawn for the caller to pay out. The
// option keeps the remainder.
//
// Parts per million rather than basis points because the caller has to be able
// to halve the rate exactly. x/personhood draws half as much when a registration
// names no referrer, and halving a rate expressed in whole basis points is
// integer division on a number small enough to reach zero: at 1 bp the
// unreferred branch drew nothing at all and paid the registrant nothing, with no
// error. A hundredfold finer unit puts that failure a hundred times further away
// and lets the rate be tuned without a migration. It is the same unit Uniswap v3
// uses for its fee tiers, for much the same reason.
//
// This is how an INTEGRATED option whose handler resolves nothing per block gets
// drained on an external event instead: x/personhood calls it when a human
// registers. A missing option is not an error — it just means that pool was
// never configured — and yields zero.
func (k Keeper) DrawFromOption(ctx context.Context, stream types.StreamId, optionID uint64, ppm int64) (math.Int, error) {
	opt, err := k.Options.Get(ctx, optionKey(stream, optionID))
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return math.ZeroInt(), nil
		}
		return math.ZeroInt(), err
	}
	rewardIndex, err := k.getRewardIndex(ctx, stream)
	if err != nil {
		return math.ZeroInt(), err
	}
	settleOption(&opt, rewardIndex)

	drawn := opt.Accumulated.MulRaw(ppm).QuoRaw(types.PpmDenominator)
	if drawn.IsPositive() {
		opt.Accumulated = opt.Accumulated.Sub(drawn)
	} else {
		drawn = math.ZeroInt()
	}
	if err := k.setOption(ctx, stream, opt); err != nil {
		return math.ZeroInt(), err
	}
	return drawn, nil
}

// resetAllocations retires every vote in one stream at once: it settles each of
// the stream's options up to now, zeroes the weights, and bumps the stream's
// epoch so existing voter records stop counting.
//
// This is O(options), not O(voters) — the voter rows are left where they are and
// simply age out of relevance. Accrued balances are deliberately preserved: a
// reset redirects the future stream, it does not confiscate ERTH an option has
// already earned. The other stream is not touched.
func (k Keeper) resetAllocations(ctx context.Context, stream types.StreamId) (uint64, error) {
	if err := k.AdvanceIndex(ctx, stream); err != nil {
		return 0, err
	}
	rewardIndex, err := k.getRewardIndex(ctx, stream)
	if err != nil {
		return 0, err
	}

	ids, err := k.streamOptionIDs(ctx, stream)
	if err != nil {
		return 0, err
	}
	for _, id := range ids {
		opt, err := k.Options.Get(ctx, optionKey(stream, id))
		if err != nil {
			return 0, err
		}
		// Settle before zeroing so everything earned up to this block is kept.
		settleOption(&opt, rewardIndex)
		opt.AmountAllocated = math.ZeroInt()
		if err := k.setOption(ctx, stream, opt); err != nil {
			return 0, err
		}
	}
	if err := k.TotalWeight.Set(ctx, key(stream), math.ZeroInt()); err != nil {
		return 0, err
	}

	epoch, err := k.getEpoch(ctx, stream)
	if err != nil {
		return 0, err
	}
	epoch++
	return epoch, k.Epoch.Set(ctx, key(stream), epoch)
}

// streamOptionIDs collects one stream's option ids. Collected up front because
// every caller writes back to the map it is walking.
func (k Keeper) streamOptionIDs(ctx context.Context, stream types.StreamId) ([]uint64, error) {
	var ids []uint64
	rng := collections.NewPrefixedPairRange[uint32, uint64](key(stream))
	if err := k.Options.Walk(ctx, rng, func(k collections.Pair[uint32, uint64], _ types.AllocationOption) (bool, error) {
		ids = append(ids, k.K2())
		return false, nil
	}); err != nil {
		return nil, err
	}
	return ids, nil
}

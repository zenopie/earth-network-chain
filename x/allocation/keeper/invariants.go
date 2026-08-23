package keeper

import (
	"context"
	"fmt"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/x/allocation/types"
)

// The stream accounting has to add up, and nothing was checking that it did.
//
// AdvanceIndex divides a stream's emission by TotalWeight, and each option later
// collects against its own AmountAllocated:
//
//	idx += reward * precision / TotalWeight        // the whole
//	owed = AmountAllocated * (idx - lastIdx)       // the parts
//
// So the parts must sum to the whole. If they do not, the stream pays out
// something other than the 1 ERTH/sec it was given — proportionally over or
// under, on every option, forever. Both errors are invisible from outside: there
// is no balance to compare against, because an option's rewards are minted when
// they are claimed rather than escrowed.
//
// TotalWeight is maintained by adding and subtracting in resyncVoter, which is
// exactly the shape that drifts. resyncVoter also clamps a negative total to
// zero, so a drift large enough to go negative is absorbed rather than surfaced
// — the symptom is erased and the arithmetic keeps running.
//
// This is the same check x/dex runs on LpTotalVolume against the sum of the
// pools' stored volume, which is unsurprising: lp_rewards.go describes itself as
// using "the same index pattern the allocation streams use". The pattern was
// copied and the check was not.
//
// What it costs per block, and why that changed.
//
// Summing the parts means walking every option in the stream, and adding an
// ADDRESS option is permissionless. That made the per-block cost something an
// outsider could raise for a one-time fee: the option is paid for once and
// decoded by every node in every block after — the same trade x/dex had to undo
// for pools (see solvency.go and TestPoolSetIsNoLongerUnbounded).
//
// So the sum is maintained instead of recomputed. setOption moves SummedWeight
// by the amount each option actually took, and the EndBlocker compares that
// against TotalWeight: two numbers per stream, whatever the option count.
//
// It is worth stating why that is a real check rather than a number agreeing
// with itself, which is the reason x/dex took its volume check *off* the block
// path instead. The two aggregates are maintained at different sites and from
// different quantities. resyncVoter moves TotalWeight by the voter's whole
// weight; setOption moves the sum by what each option's balance actually
// changed by. The clamp is the case that matters: resyncVoter floors a negative
// total at zero while the options it just wrote keep their negative balances, so
// the two part ways and the comparison says so. A single aggregate incremented
// alongside itself would not have caught that.
//
// What the O(1) check cannot see is an option written around setOption
// entirely, with TotalWeight left alone: both aggregates miss it, so they still
// agree. That is a bug in this module rather than something a user can cause, so
// it is covered where the walk now lives — CheckStreamWeight, called by
// AssertInvariants, which the tests run after every operation, and by genesis
// validation on every import.

// WeightReport is one stream's declared weight against the sum of its options.
type WeightReport struct {
	Stream   types.StreamId
	Declared math.Int
	Summed   math.Int
}

func (r WeightReport) Broken() bool { return !r.Declared.Equal(r.Summed) }

// CheckStreamWeight sums a stream's options and compares them to the declared
// total. O(options in the stream).
func (k Keeper) CheckStreamWeight(ctx context.Context, stream types.StreamId) (WeightReport, error) {
	declared, err := k.getTotalWeight(ctx, stream)
	if err != nil {
		return WeightReport{}, err
	}

	summed := math.ZeroInt()
	rng := collections.NewPrefixedPairRange[uint32, uint64](key(stream))
	if err := k.Options.Walk(ctx, rng,
		func(_ collections.Pair[uint32, uint64], opt types.AllocationOption) (bool, error) {
			if !opt.AmountAllocated.IsNil() {
				summed = summed.Add(opt.AmountAllocated)
			}
			return false, nil
		}); err != nil {
		return WeightReport{}, err
	}

	return WeightReport{Stream: stream, Declared: declared, Summed: summed}, nil
}

// CheckStreamWeightBounded is the same comparison against the running sum
// rather than a walk. O(1) per stream, which is what makes it safe to run in a
// block whose option count an outsider can raise.
func (k Keeper) CheckStreamWeightBounded(ctx context.Context, stream types.StreamId) (WeightReport, error) {
	declared, err := k.getTotalWeight(ctx, stream)
	if err != nil {
		return WeightReport{}, err
	}
	summed, err := k.getSummedWeight(ctx, stream)
	if err != nil {
		return WeightReport{}, err
	}
	return WeightReport{Stream: stream, Declared: declared, Summed: summed}, nil
}

// AssertHotInvariants is the EndBlocker's check: both streams, O(1) each.
//
// Returning an error from an EndBlocker halts the chain, which is the intended
// outcome: from the moment the two disagree, every block pays the wrong amount,
// and the error compounds silently into balances that were minted and spent. A
// halt is recoverable by upgrade; an emission that has been quietly wrong for a
// month is not.
func (k Keeper) AssertHotInvariants(ctx context.Context) error {
	for _, stream := range types.Streams {
		rep, err := k.CheckStreamWeightBounded(ctx, stream)
		if err != nil {
			return err
		}
		if rep.Broken() {
			err := types.ErrInvariantBroken.Wrap(fmt.Sprintf(
				"stream %s: total_weight is %s but its options allocate %s",
				rep.Stream, rep.Declared, rep.Summed))
			sdk.UnwrapSDKContext(ctx).Logger().Error(
				"allocation invariant broken — halting", "err", err,
				"height", sdk.UnwrapSDKContext(ctx).BlockHeight())
			return err
		}
	}
	return nil
}

// AssertInvariants is the exhaustive version: it walks the options instead of
// trusting either aggregate.
//
// Two jobs. It catches what the bounded pair is blind to — an option written
// around setOption with TotalWeight left alone, which leaves both aggregates
// ignorant of it and so agreeing with each other. And when the bounded check
// does fire, this is what says which of the two numbers is wrong: the bounded
// one can only report that they disagree, while the walk knows what the options
// actually hold.
//
// O(options), and deliberately not on the block path. Tests call it after every
// operation; operators can run it against a halted node.
func (k Keeper) AssertInvariants(ctx context.Context) error {
	for _, stream := range types.Streams {
		rep, err := k.CheckStreamWeight(ctx, stream)
		if err != nil {
			return err
		}
		if rep.Broken() {
			return types.ErrInvariantBroken.Wrapf(
				"stream %s: total_weight is %s but its options allocate %s",
				rep.Stream, rep.Declared, rep.Summed)
		}
		running, err := k.getSummedWeight(ctx, stream)
		if err != nil {
			return err
		}
		if !running.Equal(rep.Summed) {
			return types.ErrInvariantBroken.Wrapf(
				"stream %s: summed_weight is %s but the options sum to %s — "+
					"something wrote an option without going through setOption",
				stream, running, rep.Summed)
		}
	}
	return nil
}

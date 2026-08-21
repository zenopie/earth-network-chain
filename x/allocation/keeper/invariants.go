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

// AssertInvariants checks every stream. Returning an error from the EndBlocker
// halts the chain, which is the intended outcome: from the moment the two
// disagree, every block pays the wrong amount, and the error compounds silently
// into balances that were minted and spent. A halt is recoverable by upgrade;
// an emission that has been quietly wrong for a month is not.
func (k Keeper) AssertInvariants(ctx context.Context) error {
	for _, stream := range types.Streams {
		rep, err := k.CheckStreamWeight(ctx, stream)
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

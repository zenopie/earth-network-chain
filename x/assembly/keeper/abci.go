package keeper

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	v1 "github.com/cosmos/cosmos-sdk/x/gov/types/v1"

	"github.com/earth-network/earth/x/assembly/types"
)

// EndBlocker resolves both of the chamber's clocks: the proposals whose voting
// period closed this block, and the removal ballots that did.
//
// ORDERING IS LOAD-BEARING. This module's EndBlocker must run immediately before
// x/gov's — see app/app_config.go. x/gov's EndBlocker tallies a due proposal and,
// if it passed, executes its messages in the same pass; there is no later point
// at which a decision can be taken back. So the chamber has to take its own
// decision first, and a proposal the humans refused has to be gone from
// ActiveProposalsQueue before x/gov looks at that queue.
func (k Keeper) EndBlocker(ctx context.Context) error {
	if err := k.resolveDueProposals(ctx); err != nil {
		return err
	}
	if err := k.resolveDueRemovals(ctx); err != nil {
		return err
	}
	// Last, and capped: clearing the votes of ballots that have closed. Closing
	// a ballot does not touch its votes, so no block's work grows with turnout.
	return k.purgeClosedBallots(ctx, types.ClosedVotePurgeLimit)
}

// resolveDueProposals applies the human result to every proposal whose voting
// period has closed.
//
// Approved proposals are left completely untouched. That is not tidiness: x/gov
// is about to tally them, and tallying is what deletes the votes it counts — so
// anything done here that consumed a proposal's votes would leave x/gov counting
// an empty set and failing a proposal both houses had passed.
func (k Keeper) resolveDueProposals(ctx context.Context) error {
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	// The same range x/gov's EndBlocker uses a moment later, so the two agree on
	// exactly which proposals are due in this block.
	rng := collections.NewPrefixUntilPairRange[time.Time, uint64](sdkCtx.BlockTime())
	iter, err := k.gov.ActiveProposalsQueue.Iterate(ctx, rng)
	if err != nil {
		return err
	}
	due, err := iter.KeyValues()
	if err != nil {
		return err
	}

	for _, entry := range due {
		id := entry.Key.K2()

		proposal, err := k.gov.Proposals.Get(ctx, id)
		if err != nil {
			// A proposal x/gov itself cannot decode is x/gov's to fail, and it
			// has a path for exactly that. Leaving it alone is what keeps this
			// module from having to reimplement that handling.
			if errors.Is(err, collections.ErrEncoding) {
				if err := k.endProposalRound(ctx, id); err != nil {
					return err
				}
				continue
			}
			return err
		}

		// The running tally is kept true as registrations retire — see
		// OnRegistrationRetired — so it is decided on as it stands.
		tally, err := k.proposalTally(ctx, id)
		if err != nil {
			return err
		}

		// The expedited track buys a one-day voting period instead of seven and
		// pays for it in agreement: three quarters rather than two thirds, the
		// same trade the stake house makes on the same proposal.
		approved := types.Approves(tally.Yes, tally.No)
		if proposal.Expedited {
			approved = types.ApprovesExpedited(tally.Yes, tally.No)
		}

		// This round of voting is over in all three outcomes, so its ballot
		// closes in all three: O(1), its votes cleared later. For a demotion
		// that is not tidying. The regular round is a longer deliberation under
		// a different bar, and it opens a ballot of its own rather than inherit
		// a one-day tally.
		if err := k.endProposalRound(ctx, id); err != nil {
			return err
		}

		if approved {
			continue
		}

		// An expedited proposal the chamber declines is demoted, not killed —
		// the same thing x/gov does when its own expedited tally falls short
		// (x/gov/abci.go, the `case proposal.Expedited` branch). Declining it
		// here can as easily mean "not on the fast track" as "never", and the
		// regular round costs those who refused it nothing, because silence
		// fails that round too.
		if proposal.Expedited {
			demoted, err := k.demoteExpedited(ctx, proposal, tally)
			if err != nil {
				return err
			}
			if demoted {
				continue
			}
			// Could not be demoted — see demoteExpedited. Falls through to an
			// outright refusal rather than being left in the queue for x/gov to
			// pass unratified.
		}

		if err := k.failProposal(ctx, proposal, tally, barFor(proposal)); err != nil {
			return err
		}
	}
	return nil
}

// demoteExpedited converts an expedited proposal the chamber declined into an
// ordinary one, with a full voting period ahead of it, and reports whether it
// did.
//
// Deposits are deliberately untouched: they follow the proposal into its second
// round, which is also why x/gov skips its own deposit handling on this branch.
//
// It reports false — and the caller then refuses the proposal outright — when
// the recomputed end time is not actually in the future. That can only happen if
// the regular voting period is shorter than the elapsed expedited one, which is
// a misconfiguration rather than a state anyone should reach. It matters anyway:
// x/gov's EndBlocker runs a moment after this one over the same due range, so a
// proposal re-queued into the past would be tallied by the stake house in this
// very block, with the chamber's refusal already spent and no second round in
// which to express it.
func (k Keeper) demoteExpedited(ctx context.Context, proposal v1.Proposal, tally types.Tally) (bool, error) {
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	params, err := k.gov.Params.Get(ctx)
	if err != nil {
		return false, err
	}
	if params.VotingPeriod == nil || proposal.VotingStartTime == nil || proposal.VotingEndTime == nil {
		return false, nil
	}
	endTime := proposal.VotingStartTime.Add(*params.VotingPeriod)
	if !endTime.After(sdkCtx.BlockTime()) {
		return false, nil
	}
	previousEnd := *proposal.VotingEndTime

	proposal.Expedited = false
	proposal.VotingEndTime = &endTime
	// The status stays StatusVotingPeriod, so SetProposal keeps it in
	// VotingPeriodProposals and humans can vote on it again in the longer round.
	if err := k.gov.SetProposal(ctx, proposal); err != nil {
		return false, err
	}
	if err := k.gov.ActiveProposalsQueue.Remove(ctx, collections.Join(previousEnd, proposal.Id)); err != nil {
		return false, err
	}
	if err := k.gov.ActiveProposalsQueue.Set(ctx, collections.Join(endTime, proposal.Id), proposal.Id); err != nil {
		return false, err
	}

	sdkCtx.EventManager().EmitEvent(sdk.NewEvent(
		"assembly_demoted_expedited_proposal",
		sdk.NewAttribute("proposal_id", strconv.FormatUint(proposal.Id, 10)),
		sdk.NewAttribute("yes", strconv.FormatUint(tally.Yes, 10)),
		sdk.NewAttribute("no", strconv.FormatUint(tally.No, 10)),
		sdk.NewAttribute("voting_end_time", endTime.String()),
	))
	return true, nil
}

// failProposal ends a proposal the chamber refused.
//
// x/gov's own tally is run first and recorded, even though the outcome is
// already settled. A proposal that simply vanished with an empty tally would
// leave no way to tell whether stake had been for it or against it, and the
// whole claim of this design is that both houses are visible. Running Tally here
// is safe precisely because this proposal is being taken out of the queue in the
// same breath: x/gov will not tally it again.
func (k Keeper) failProposal(ctx context.Context, proposal v1.Proposal, tally types.Tally, bar string) error {
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	_, _, stakeResult, err := k.gov.Tally(ctx, proposal)
	if err != nil {
		return err
	}

	proposal.FinalTallyResult = &stakeResult
	proposal.Status = v1.StatusFailed
	proposal.FailedReason = fmt.Sprintf(
		"refused by the assembly: %d of %d human votes in favour, short of the %s required",
		tally.Yes, tally.Yes+tally.No, bar,
	)

	// SetProposal drops it from VotingPeriodProposals on its own, because the
	// status is no longer a voting one.
	if err := k.gov.SetProposal(ctx, proposal); err != nil {
		return err
	}
	if err := k.gov.ActiveProposalsQueue.Remove(ctx, collections.Join(*proposal.VotingEndTime, proposal.Id)); err != nil {
		return err
	}

	// Refunded, not burned. x/gov burns a deposit to punish a proposal that
	// wasted the chain's time — one that never reached quorum, or that stake
	// itself vetoed. A proposal stake passed and humans declined is not that: the
	// proposer used the process correctly and lost, which is what losing is
	// supposed to look like.
	if err := k.gov.RefundAndDeleteDeposits(ctx, proposal.Id); err != nil {
		return err
	}

	sdkCtx.EventManager().EmitEvent(sdk.NewEvent(
		"assembly_refused_proposal",
		sdk.NewAttribute("proposal_id", strconv.FormatUint(proposal.Id, 10)),
		sdk.NewAttribute("yes", strconv.FormatUint(tally.Yes, 10)),
		sdk.NewAttribute("no", strconv.FormatUint(tally.No, 10)),
	))
	return nil
}

// resolveDueRemovals closes every removal ballot whose time is up, and strikes
// the option behind any that carried.
func (k Keeper) resolveDueRemovals(ctx context.Context) error {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	now := sdkCtx.BlockTime().Unix()

	// Ordered by closing time, so this stops at the first ballot that is not due
	// rather than walking every open one.
	var closed []collections.Pair[int64, uint64]
	rng := new(collections.Range[collections.Pair[int64, uint64]]).
		EndInclusive(collections.Join(now, ^uint64(0)))
	if err := k.RemovalQueue.Walk(ctx, rng, func(key collections.Pair[int64, uint64]) (bool, error) {
		if key.K1() > now {
			return true, nil
		}
		closed = append(closed, key)
		return false, nil
	}); err != nil {
		return err
	}

	for _, key := range closed {
		optionID := key.K2()
		ballot, err := k.RemovalBallots.Get(ctx, optionID)
		if err != nil {
			return err
		}
		if ballot.Tally, err = k.removalTally(ctx, optionID); err != nil {
			return err
		}

		carried := types.Approves(ballot.Tally.Yes, ballot.Tally.No)
		if carried {
			// An option struck between the ballot opening and now — by an
			// earlier ballot that carried, or by the idle sweep collecting it —
			// is already gone. Removing it twice is not an error worth halting
			// the chain over, so the allocation keeper reports it and the ballot
			// simply closes.
			if err := k.allocation.RemoveGroundworksOption(ctx, k.chamberAddr, optionID); err != nil {
				return err
			}
		}

		if err := k.closeRemovalBallot(ctx, key, optionID); err != nil {
			return err
		}

		sdkCtx.EventManager().EmitEvent(sdk.NewEvent(
			"assembly_removal_closed",
			sdk.NewAttribute("option_id", strconv.FormatUint(optionID, 10)),
			sdk.NewAttribute("carried", strconv.FormatBool(carried)),
			sdk.NewAttribute("yes", strconv.FormatUint(ballot.Tally.Yes, 10)),
			sdk.NewAttribute("no", strconv.FormatUint(ballot.Tally.No, 10)),
		))
	}
	return nil
}

// barFor names the share of the human vote a proposal needed, for the record it
// leaves behind.
func barFor(proposal v1.Proposal) string {
	if proposal.Expedited {
		return "three quarters"
	}
	return "two thirds"
}

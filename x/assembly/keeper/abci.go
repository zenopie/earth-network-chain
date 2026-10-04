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

	"github.com/earth-network/earth/internal/safeexec"
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
	if err := k.closeOrphanedBallots(ctx, types.OrphanBallotCheckLimit); err != nil {
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
		// Per proposal, on its own branch, recovering panics. A proposal the
		// chamber cannot resolve must not slip through to x/gov unratified,
		// and must not halt the chain either: it is refused outright
		// (failUnresolved). Only if that too fails is the error returned.
		if err := safeexec.Cached(sdkCtx, func(c sdk.Context) error {
			return k.resolveDueProposal(c, id)
		}); err != nil {
			sdkCtx.Logger().Error("assembly: resolving a due proposal failed; refusing it", "proposal_id", id, "err", err)
			if ferr := safeexec.Cached(sdkCtx, func(c sdk.Context) error {
				return k.failUnresolved(c, id, err)
			}); ferr != nil {
				return errors.Join(err, ferr)
			}
		}
	}
	return nil
}

// resolveDueProposal applies the human result to one due proposal.
func (k Keeper) resolveDueProposal(ctx context.Context, id uint64) error {

	proposal, err := k.gov.Proposals.Get(ctx, id)
	if err != nil {
		// A proposal x/gov itself cannot decode is x/gov's to fail, and it
		// has a path for exactly that. Leaving it alone is what keeps this
		// module from having to reimplement that handling.
		if errors.Is(err, collections.ErrEncoding) {
			if err := k.endProposalRound(ctx, id); err != nil {
				return err
			}
			if err := k.ProposalRound.Remove(ctx, id); err != nil {
				return err
			}
			if err := k.forgetSubjects(ctx, id); err != nil {
				return err
			}
			return nil
		}
		return err
	}

	// Decided on the running tally as it stands.
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
		if proposal.Expedited {
			// x/gov tallies it next and, if stake does not pass it on the
			// expedited track, demotes it to a regular round, still voting.
			// That round is a new ballot scope (audit 5 L-AS1): left at round
			// 0, each voter's nullifier would repeat across the two rounds and
			// link an anonymous voter's two votes. It opens now, as a
			// chamber demotion's does. If x/gov ends the proposal instead,
			// AfterProposalVotingPeriodEnded drops the round.
			round, _, err := k.proposalRound(ctx, proposal)
			if err != nil {
				return err
			}
			if err := k.ProposalRound.Set(ctx, id, types.ProposalRound{
				Round: round + 1, OpenedAt: sdk.UnwrapSDKContext(ctx).BlockTime().Unix(),
			}); err != nil {
				return err
			}
		} else if err := k.ProposalRound.Remove(ctx, id); err != nil {
			return err
		}
		// Subjects are kept: x/gov tallies the proposal next, and if it
		// is expedited and stake does not pass it, x/gov demotes it to a
		// regular round, still voting. AfterProposalVotingPeriodEnded
		// forgets them once x/gov has really ended its voting.
		return nil
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
			return nil
		}
		// Could not be demoted — see demoteExpedited. Falls through to an
		// outright refusal rather than being left in the queue for x/gov to
		// pass unratified.
	}

	if err := k.ProposalRound.Remove(ctx, id); err != nil {
		return err
	}
	if err := k.forgetSubjects(ctx, id); err != nil {
		return err
	}
	if err := k.failProposal(ctx, proposal, tally, barFor(proposal)); err != nil {
		return err
	}
	return nil
}

// failUnresolved refuses a due proposal whose resolution failed: its round
// ends, its subjects are forgotten, and it is failed with an empty human
// tally, so x/gov never tallies it as if the chamber had ratified it.
func (k Keeper) failUnresolved(ctx context.Context, id uint64, cause error) error {
	if err := k.endProposalRound(ctx, id); err != nil {
		return err
	}
	if err := k.ProposalRound.Remove(ctx, id); err != nil {
		return err
	}
	if err := k.forgetSubjects(ctx, id); err != nil {
		return err
	}
	proposal, err := k.gov.Proposals.Get(ctx, id)
	if err != nil {
		return err
	}
	sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(sdk.NewEvent(
		"assembly_resolve_failed",
		sdk.NewAttribute("proposal_id", strconv.FormatUint(id, 10)),
		sdk.NewAttribute("error", cause.Error()),
	))
	return k.failProposal(ctx, proposal, types.Tally{}, barFor(proposal))
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
	round, _, err := k.proposalRound(ctx, proposal)
	if err != nil {
		return false, err
	}

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
	// The regular round is a new ballot scope, opened now: a voter proves an
	// identity activated before it opened, and votes in it afresh.
	if err := k.ProposalRound.Set(ctx, proposal.Id, types.ProposalRound{
		Round: round + 1, OpenedAt: sdkCtx.BlockTime().Unix(),
	}); err != nil {
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

	// Stake's tally runs the chain's custom tally function (x/shieldedstaking
	// StakeTally) over validator state: on its own branch, recovering panics.
	// If it cannot be computed the proposal is still refused (the chamber
	// said no), with an empty stake result and its deposits refunded: there
	// is no stake judgement to burn them on.
	var (
		burnDeposits bool
		stakeResult  v1.TallyResult
	)
	if terr := safeexec.Cached(sdkCtx, func(c sdk.Context) error {
		var err error
		_, burnDeposits, stakeResult, err = k.gov.Tally(c, proposal)
		return err
	}); terr != nil {
		sdkCtx.Logger().Error("assembly: stake tally of a refused proposal failed", "proposal_id", proposal.Id, "err", terr)
		burnDeposits, stakeResult = false, v1.EmptyTallyResult()
	}
	var err error

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

	// Whatever stake's own tally says. x/gov burns a deposit to punish a
	// proposal that wasted the chain's time — one that missed quorum, or that
	// stake vetoed — and that judgement is stake's to make whichever house
	// ended the proposal. This used to refund unconditionally, so a proposal
	// stake had vetoed as spam got its deposit back whenever the humans also
	// said no, which took away the only cost of submitting spam. A proposal
	// stake would have passed or merely rejected is still refunded: the
	// proposer used the process correctly and lost.
	if burnDeposits {
		err = k.gov.DeleteAndBurnDeposits(ctx, proposal.Id)
	} else {
		err = k.gov.RefundAndDeleteDeposits(ctx, proposal.Id)
	}
	if err != nil {
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

// closeOrphanedBallots closes the ballot of any proposal x/gov no longer has,
// or no longer has in its voting period.
//
// A proposal cancelled in its voting period is deleted by x/gov outright: it
// leaves the active queue, so resolveDueProposals never reaches it, and x/gov
// has no hook for cancellation. Its ballot stayed open for good, and every
// vote on it with it — including the index entries each retirement walks. A
// ballot of a proposal that ended without this module closing it (state no
// block writes: an imported genesis) is closed too (audit 5 L-AS4).
//
// The walk resumes where the last one stopped (OrphanSweepCursor) and wraps,
// so every ballot is looked at in turn, however many live ones sort first.
func (k Keeper) closeOrphanedBallots(ctx context.Context, limit int) error {
	var orphans []uint64
	checked := 0
	cursor, err := k.OrphanSweepCursor.Get(ctx)
	if err != nil && !errors.Is(err, collections.ErrNotFound) {
		return err
	}
	var rng collections.Ranger[uint64]
	if cursor > 0 {
		rng = new(collections.Range[uint64]).StartExclusive(cursor)
	}
	last := uint64(0)
	err = k.ProposalBallot.Walk(ctx, rng, func(proposalID, _ uint64) (bool, error) {
		if checked >= limit {
			return true, nil
		}
		checked++
		last = proposalID
		p, err := k.gov.Proposals.Get(ctx, proposalID)
		switch {
		case errors.Is(err, collections.ErrNotFound):
			orphans = append(orphans, proposalID)
		case errors.Is(err, collections.ErrEncoding):
			// x/gov's to fail; resolveDueProposal closes it then.
		case err != nil:
			return true, err
		case p.Status != v1.StatusVotingPeriod:
			orphans = append(orphans, proposalID)
		}
		return false, nil
	})
	if err != nil {
		return err
	}
	next := last
	if checked < limit {
		next = 0 // reached the end: start over next block
	}
	if err := k.OrphanSweepCursor.Set(ctx, next); err != nil {
		return err
	}
	// A cancelled proposal nobody voted on has no ballot, only its subjects.
	// Subjects of a proposal x/gov ended are normally forgotten by
	// AfterProposalVotingPeriodEnded; x/gov swallows a hook error, so one
	// left behind is dropped here.
	//
	// This walk resumes and wraps too (audit 6 D-L-AS1): from the start each
	// block, it only ever looked at the first limit entries, so subjects
	// sorting after limit live ones were never dropped.
	checked = 0
	var ended []uint64
	subjCursor, err := k.SubjectSweepCursor.Get(ctx)
	if err != nil && !errors.Is(err, collections.ErrNotFound) {
		return err
	}
	var srng collections.Ranger[uint64]
	if subjCursor > 0 {
		srng = new(collections.Range[uint64]).StartExclusive(subjCursor)
	}
	lastSubject := uint64(0)
	err = k.Subjects.Walk(ctx, srng, func(proposalID uint64, _ types.ProposalSubjects) (bool, error) {
		if checked >= limit {
			return true, nil
		}
		checked++
		lastSubject = proposalID
		p, err := k.gov.Proposals.Get(ctx, proposalID)
		switch {
		case errors.Is(err, collections.ErrNotFound):
			orphans = append(orphans, proposalID)
		case errors.Is(err, collections.ErrEncoding):
		case err != nil:
			return true, err
		case p.Status != v1.StatusVotingPeriod:
			ended = append(ended, proposalID)
		}
		return false, nil
	})
	if err != nil {
		return err
	}
	nextSubject := lastSubject
	if checked < limit {
		nextSubject = 0 // reached the end: start over next block
	}
	if err := k.SubjectSweepCursor.Set(ctx, nextSubject); err != nil {
		return err
	}
	for _, id := range ended {
		if err := k.forgetSubjects(ctx, id); err != nil {
			return err
		}
	}
	for _, id := range orphans {
		if err := k.forgetSubjects(ctx, id); err != nil {
			return err
		}
		if _, ok, err := k.proposalBallot(ctx, id); err != nil {
			return err
		} else if !ok {
			continue
		}
		if err := k.endProposalRound(ctx, id); err != nil {
			return err
		}
		if err := k.ProposalRound.Remove(ctx, id); err != nil {
			return err
		}
		sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(sdk.NewEvent(
			"assembly_closed_orphaned_ballot",
			sdk.NewAttribute("proposal_id", strconv.FormatUint(id, 10)),
		))
	}
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
			//
			// Isolated in a cache context. This runs in EndBlock, where an
			// error halts the chain on every validator at once, and it calls
			// into another module whose failure modes this one cannot see. A
			// strike that errors is rolled back whole, reported, and the ballot
			// closes as if it had fallen short; governance can strike the
			// option by proposal. Halting is not a proportionate response to
			// one option failing to come off the slate.
			if err := safeexec.Cached(sdkCtx, func(cacheCtx sdk.Context) error {
				return k.allocation.RemoveGroundworksOption(cacheCtx, k.chamberAddr, optionID)
			}); err != nil {
				sdkCtx.EventManager().EmitEvent(sdk.NewEvent(
					"assembly_removal_failed",
					sdk.NewAttribute("option_id", strconv.FormatUint(optionID, 10)),
					sdk.NewAttribute("error", err.Error()),
				))
				carried = false
			}
		}

		if err := k.closeRemovalBallot(ctx, key, optionID); err != nil {
			return err
		}
		// The cooldown lets a declined removal stand. Only a declined one:
		// a strike that carried has removed the option (nothing left to
		// protect, so no entry is left behind), and one that carried but
		// failed to apply must not shield the option from the next ballot for
		// thirty days. A declined ballot opened by the option's own
		// beneficiaries does buy it a cooldown, but only by surviving a
		// public seven-day vote anyone can join: the ballot itself is the
		// defence, and an opener cannot keep it from being carried.
		if approved := types.Approves(ballot.Tally.Yes, ballot.Tally.No); approved {
			if err := k.RemovalCooldown.Remove(ctx, optionID); err != nil {
				return err
			}
		} else if err := k.RemovalCooldown.Set(ctx, optionID, now+types.RemovalCooldown); err != nil {
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

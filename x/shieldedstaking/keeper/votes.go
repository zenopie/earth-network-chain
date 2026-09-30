package keeper

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	govkeeper "github.com/cosmos/cosmos-sdk/x/gov/keeper"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"
	v1 "github.com/cosmos/cosmos-sdk/x/gov/types/v1"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	"github.com/earth-network/earth/x/shieldedstaking/types"
)

// Stake votes.
//
// When a proposal enters voting this module snapshots the note-tree anchor and,
// per validator, the derth supply and rate. A derth/v note that was in the tree
// then (and is unspent when it votes) votes with a note_vote proof against the
// snapshot root; a position created before the snapshot votes with its key.
// The weight is public; the voter is not.
//
// The tally (StakeTally, x/gov's custom tally function) turns each validator's
// privately voted derth into a fraction of the module's CURRENT shares at v:
// shares_voted = module_shares_v x derth_voted / supply_v(snapshot), capped at
// module_shares_v. In current shares, so a slash or undelegation during the
// vote shrinks private votes with the stake behind them and the total never
// exceeds bonded stake. Those shares are deducted from v, which (as in the
// SDK's default tally) then votes whatever was not deducted: its self-bond,
// the module's un-voted derth, and any delegator that did not vote.

// snapshotProposal records proposalID's snapshot if it has entered voting.
func (k Keeper) snapshotProposal(ctx context.Context, proposalID uint64) error {
	if k.gov.k == nil {
		return nil
	}
	if ok, err := k.Snapshots.Has(ctx, proposalID); err != nil || ok {
		return err
	}
	prop, err := k.gov.k.Proposals.Get(ctx, proposalID)
	if err != nil {
		return err
	}
	if prop.Status != v1.StatusVotingPeriod || prop.VotingEndTime == nil {
		return nil
	}
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	snap := types.ProposalSnapshot{ProposalId: proposalID, Height: sdkCtx.BlockHeight(), VotingEnd: prop.VotingEndTime.UnixNano()}
	if root, err := k.shielded.LatestRoot.Get(ctx); err == nil {
		rec, err := k.shielded.Roots.Get(ctx, root)
		if err != nil {
			return err
		}
		snap.Root, snap.TreeSize = root, rec.TreeSize
	} else if !errors.Is(err, collections.ErrNotFound) {
		return err
	}
	err = k.Validators.Walk(ctx, nil, func(v string, _ types.ValidatorState) (bool, error) {
		b, s, err := k.Backing(ctx, v)
		if err != nil {
			return true, err
		}
		if s.IsPositive() {
			snap.Validators = append(snap.Validators, types.ValidatorSnapshot{Validator: v, Supply: s, Rate: rateOf(b, s)})
		}
		return false, nil
	})
	if err != nil {
		return err
	}
	if err := k.Snapshots.Set(ctx, proposalID, snap); err != nil {
		return err
	}
	if err := k.SnapshotExpiry.Set(ctx, collections.Join(snap.VotingEnd, proposalID)); err != nil {
		return err
	}
	sdkCtx.EventManager().EmitEvent(sdk.NewEvent(types.EventTypeSnapshot,
		sdk.NewAttribute(types.AttributeKeyProposal, strconv.FormatUint(proposalID, 10)),
		sdk.NewAttribute(types.AttributeKeyRoot, fmt.Sprintf("%x", snap.Root)),
	))
	return nil
}

// openSnapshot returns proposalID's snapshot and valoper's entry in it, if
// the proposal is still open to stake votes.
func (k Keeper) openSnapshot(ctx context.Context, proposalID uint64, valoper string) (types.ProposalSnapshot, types.ValidatorSnapshot, error) {
	snap, err := k.Snapshots.Get(ctx, proposalID)
	if errors.Is(err, collections.ErrNotFound) {
		return snap, types.ValidatorSnapshot{}, types.ErrNoVoting.Wrapf("proposal %d has no stake-vote snapshot", proposalID)
	} else if err != nil {
		return snap, types.ValidatorSnapshot{}, err
	}
	if sdk.UnwrapSDKContext(ctx).BlockTime().UnixNano() >= snap.VotingEnd {
		return snap, types.ValidatorSnapshot{}, types.ErrNoVoting.Wrapf("voting on proposal %d has ended", proposalID)
	}
	if len(snap.Root) == 0 {
		return snap, types.ValidatorSnapshot{}, types.ErrNoVoting.Wrap("the note tree was empty when voting began")
	}
	for _, vs := range snap.Validators {
		if vs.Validator == valoper {
			return snap, vs, nil
		}
	}
	return snap, types.ValidatorSnapshot{}, types.ErrNoVoting.Wrapf("%s had no derth when voting began", valoper)
}

func zeroTally() types.VoteTally {
	z := math.LegacyZeroDec()
	return types.VoteTally{Yes: z, Abstain: z, No: z, NoWithVeto: z}
}

// addToTally adds (sign=+1) or removes (sign=-1) a vote's derth.
func addToTally(t *types.VoteTally, v types.StakeVote, sign int64) {
	d := math.LegacyNewDecFromInt(v.Derth).MulInt64(sign)
	for _, o := range v.Options {
		w, _ := math.LegacyNewDecFromStr(o.Weight)
		x := d.Mul(w)
		switch o.Option {
		case v1.OptionYes:
			t.Yes = t.Yes.Add(x)
		case v1.OptionAbstain:
			t.Abstain = t.Abstain.Add(x)
		case v1.OptionNo:
			t.No = t.No.Add(x)
		case v1.OptionNoWithVeto:
			t.NoWithVeto = t.NoWithVeto.Add(x)
		}
	}
}

func (k Keeper) tally(ctx context.Context, proposalID uint64, valoper string) (types.VoteTally, error) {
	t, err := k.Tallies.Get(ctx, collections.Join(proposalID, valoper))
	if errors.Is(err, collections.ErrNotFound) {
		return zeroTally(), nil
	}
	return t, err
}

// putVote records v, replacing an earlier vote under the same key.
func (k Keeper) putVote(ctx context.Context, v types.StakeVote) error {
	key := collections.Join(v.ProposalId, v.Key)
	if old, err := k.Votes.Get(ctx, key); err == nil {
		t, err := k.tally(ctx, old.ProposalId, old.Validator)
		if err != nil {
			return err
		}
		addToTally(&t, old, -1)
		if err := k.Tallies.Set(ctx, collections.Join(old.ProposalId, old.Validator), t); err != nil {
			return err
		}
	} else if !errors.Is(err, collections.ErrNotFound) {
		return err
	}
	t, err := k.tally(ctx, v.ProposalId, v.Validator)
	if err != nil {
		return err
	}
	addToTally(&t, v, 1)
	if err := k.Tallies.Set(ctx, collections.Join(v.ProposalId, v.Validator), t); err != nil {
		return err
	}
	if err := k.Votes.Set(ctx, key, v); err != nil {
		return err
	}
	sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(sdk.NewEvent(types.EventTypeStakeVote,
		sdk.NewAttribute(types.AttributeKeyProposal, strconv.FormatUint(v.ProposalId, 10)),
		sdk.NewAttribute(types.AttributeKeyValidator, v.Validator),
		sdk.NewAttribute(types.AttributeKeyDerth, v.Derth.String()),
		sdk.NewAttribute(types.AttributeKeyOptions, v1.WeightedVoteOptions(v.Options).String()),
	))
	return nil
}

// sweepSnapshots forgets proposals whose voting has ended: x/gov (whose
// EndBlocker runs before this one) has tallied them. Bounded per block; a
// long vote list is cleared over several blocks.
func (k Keeper) sweepSnapshots(ctx context.Context) {
	now := sdk.UnwrapSDKContext(ctx).BlockTime().UnixNano()
	var due []collections.Pair[int64, uint64]
	_ = k.SnapshotExpiry.Walk(ctx, nil, func(key collections.Pair[int64, uint64]) (bool, error) {
		if key.K1() > now || len(due) >= types.SnapshotSweepLimit {
			return true, nil
		}
		due = append(due, key)
		return false, nil
	})
	budget := 2_000
	for _, key := range due {
		id := key.K2()
		err := k.guarded(ctx, func(cc context.Context) error {
			var votes []collections.Pair[uint64, []byte]
			err := k.Votes.Walk(cc, collections.NewPrefixedPairRange[uint64, []byte](id),
				func(vk collections.Pair[uint64, []byte], _ types.StakeVote) (bool, error) {
					votes = append(votes, vk)
					return len(votes) >= budget, nil
				})
			if err != nil {
				return err
			}
			for _, vk := range votes {
				if err := k.Votes.Remove(cc, vk); err != nil {
					return err
				}
			}
			budget -= len(votes)
			if budget <= 0 {
				return nil // more next block
			}
			if err := k.Tallies.Clear(cc, collections.NewPrefixedPairRange[uint64, string](id)); err != nil {
				return err
			}
			if err := k.Snapshots.Remove(cc, id); err != nil {
				return err
			}
			return k.SnapshotExpiry.Remove(cc, key)
		})
		if err != nil {
			k.failure(ctx, "sweep_snapshot", "", err)
		}
		if budget <= 0 {
			return
		}
	}
}

// StakeTally is x/gov's CalculateVoteResultsAndVotingPowerFn for this chain:
// the SDK default's transparent tally plus private stake votes. It writes
// nothing of its own (x/gov's TallyResult query calls it); like the default
// it removes the proposal's transparent votes, which x/gov relies on.
func (k Keeper) StakeTally() govkeeper.CalculateVoteResultsAndVotingPowerFn {
	return func(ctx context.Context, gk govkeeper.Keeper, proposal v1.Proposal, validators map[string]v1.ValidatorGovInfo,
	) (math.LegacyDec, map[v1.VoteOption]math.LegacyDec, error) {
		total := math.LegacyZeroDec()
		results := map[v1.VoteOption]math.LegacyDec{
			v1.OptionYes: math.LegacyZeroDec(), v1.OptionAbstain: math.LegacyZeroDec(),
			v1.OptionNo: math.LegacyZeroDec(), v1.OptionNoWithVeto: math.LegacyZeroDec(),
		}
		// add counts power once toward the total and split across options,
		// exactly as the default does.
		add := func(power math.LegacyDec, opts []weighted) {
			for _, o := range opts {
				results[o.opt] = results[o.opt].Add(power.Mul(o.w))
			}
			total = total.Add(power)
		}

		// 1. Transparent votes: validators' own and any other delegator's.
		if err := k.transparentTally(ctx, gk, proposal, validators, add); err != nil {
			return math.LegacyDec{}, nil, err
		}

		// 2. Private votes, per validator.
		if err := k.privateTally(ctx, proposal.Id, validators, add); err != nil {
			return math.LegacyDec{}, nil, err
		}

		// 3. Validators vote what was not deducted (inheritance).
		for _, val := range validators {
			if len(val.Vote) == 0 || val.DelegatorShares.IsZero() {
				continue
			}
			power := val.DelegatorShares.Sub(val.DelegatorDeductions).MulInt(val.BondedTokens).Quo(val.DelegatorShares)
			add(power, weightsOf(val.Vote))
		}
		return total, results, nil
	}
}

func (k Keeper) privateTally(ctx context.Context, proposalID uint64, validators map[string]v1.ValidatorGovInfo,
	add func(math.LegacyDec, []weighted),
) error {
	snap, err := k.Snapshots.Get(ctx, proposalID)
	if errors.Is(err, collections.ErrNotFound) {
		return nil
	} else if err != nil {
		return err
	}
	supply := map[string]math.Int{}
	for _, vs := range snap.Validators {
		supply[vs.Validator] = vs.Supply
	}
	return k.Tallies.Walk(ctx, collections.NewPrefixedPairRange[uint64, string](proposalID),
		func(key collections.Pair[uint64, string], t types.VoteTally) (bool, error) {
			valoper := key.K2()
			val, ok := validators[valoper]
			s := supply[valoper]
			if !ok || s.IsNil() || !s.IsPositive() || val.DelegatorShares.IsZero() {
				return false, nil // not bonded, or no snapshot: no power, as in the default
			}
			valAddr, err := k.valAddr(valoper)
			if err != nil {
				return true, err
			}
			del, err := k.staking.GetDelegation(ctx, k.modAddr, valAddr)
			if errors.Is(err, stakingtypes.ErrNoDelegation) {
				return false, nil
			} else if err != nil {
				return true, err
			}
			voted := t.Yes.Add(t.Abstain).Add(t.No).Add(t.NoWithVeto)
			if !voted.IsPositive() {
				return false, nil
			}
			// Deducted shares: module shares x voted / supply(start), capped at
			// the module's shares and at what v has left to deduct.
			sDec := math.LegacyNewDecFromInt(s)
			ded := del.Shares.Mul(voted).Quo(sDec)
			if ded.GT(del.Shares) {
				ded = del.Shares
			}
			if room := val.DelegatorShares.Sub(val.DelegatorDeductions); ded.GT(room) {
				ded = room
			}
			if !ded.IsPositive() {
				return false, nil
			}
			val.DelegatorDeductions = val.DelegatorDeductions.Add(ded)
			validators[valoper] = val
			power := ded.MulInt(val.BondedTokens).Quo(val.DelegatorShares)
			var split []weighted
			for _, o := range []weighted{
				{v1.OptionYes, t.Yes}, {v1.OptionAbstain, t.Abstain}, {v1.OptionNo, t.No}, {v1.OptionNoWithVeto, t.NoWithVeto},
			} {
				if o.w.IsPositive() {
					split = append(split, weighted{o.opt, o.w.Quo(voted)})
				}
			}
			add(power, split)
			return false, nil
		})
}

// transparentTally is the first pass of x/gov's default tally
// (x/gov/keeper/tally.go, SDK v0.53.6), unchanged in behaviour: a validator's
// own vote is recorded for inheritance, and every voter's delegations are
// deducted from their validators and counted at the voter's option.
func (k Keeper) transparentTally(ctx context.Context, gk govkeeper.Keeper, proposal v1.Proposal,
	validators map[string]v1.ValidatorGovInfo, add func(math.LegacyDec, []weighted),
) error {
	rng := collections.NewPrefixedPairRange[uint64, sdk.AccAddress](proposal.Id)
	var remove []collections.Pair[uint64, sdk.AccAddress]
	err := gk.Votes.Walk(ctx, rng, func(key collections.Pair[uint64, sdk.AccAddress], vote v1.Vote) (bool, error) {
		voter, err := k.addressCodec.StringToBytes(vote.Voter)
		if err != nil {
			return false, err
		}
		valAddrStr, err := k.staking.ValidatorAddressCodec().BytesToString(voter)
		if err != nil {
			return false, err
		}
		if val, ok := validators[valAddrStr]; ok {
			val.Vote = vote.Options
			validators[valAddrStr] = val
		}
		err = k.staking.IterateDelegations(ctx, voter, func(_ int64, d stakingtypes.DelegationI) bool {
			val, ok := validators[d.GetValidatorAddr()]
			if !ok {
				return false
			}
			val.DelegatorDeductions = val.DelegatorDeductions.Add(d.GetShares())
			validators[d.GetValidatorAddr()] = val
			add(d.GetShares().MulInt(val.BondedTokens).Quo(val.DelegatorShares), weightsOf(vote.Options))
			return false
		})
		if err != nil {
			return false, err
		}
		remove = append(remove, key)
		return false, nil
	})
	if err != nil {
		return fmt.Errorf("error while iterating delegations: %w", err)
	}
	for _, key := range remove {
		if err := gk.Votes.Remove(ctx, key); err != nil {
			return err
		}
	}
	return nil
}

type weighted struct {
	opt v1.VoteOption
	w   math.LegacyDec
}

func weightsOf(opts []*v1.WeightedVoteOption) []weighted {
	out := make([]weighted, 0, len(opts))
	for _, o := range opts {
		w, _ := math.LegacyNewDecFromStr(o.Weight)
		out = append(out, weighted{o.Option, w})
	}
	return out
}

// ---- gov hooks --------------------------------------------------------------

// GovHooks snapshots a proposal as it enters voting. x/gov activates voting
// inside AddDeposit and calls AfterProposalDeposit right after, for the
// initial deposit and every later one.
type GovHooks struct{ k Keeper }

var _ govtypes.GovHooks = GovHooks{}

// GovHooks returns the hooks to register with x/gov.
func (k Keeper) GovHooks() GovHooks { return GovHooks{k: k} }

func (h GovHooks) AfterProposalDeposit(ctx context.Context, proposalID uint64, _ sdk.AccAddress) error {
	// Never fail a deposit over the snapshot: a proposal without one simply
	// takes no private votes.
	if err := h.k.guarded(ctx, func(cc context.Context) error { return h.k.snapshotProposal(cc, proposalID) }); err != nil {
		h.k.failure(ctx, "snapshot", "", err)
	}
	return nil
}

func (GovHooks) AfterProposalSubmission(context.Context, uint64) error { return nil }
func (GovHooks) AfterProposalVote(context.Context, uint64, sdk.AccAddress) error {
	return nil
}
func (GovHooks) AfterProposalFailedMinDeposit(context.Context, uint64) error  { return nil }
func (GovHooks) AfterProposalVotingPeriodEnded(context.Context, uint64) error { return nil }

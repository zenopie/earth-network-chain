package keeper

import (
	"context"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"

	allocationkeeper "github.com/earth-network/earth/x/allocation/keeper"
	allocationtypes "github.com/earth-network/earth/x/allocation/types"
	"github.com/earth-network/earth/x/shieldedstaking/types"
)

// Groundworks votes. A stake note votes in place: a stake proof that creates
// the note may publish its Groundworks tag (circuits/stake gw = H(TAG_GW, nk,
// rho)) and its unexposed amount, and the msg names a split. The chain
// stores a GroundworksVote under the tag; the owner stays unknown. Every
// stake proof publishes its inputs' tags, and the vote stored under each is
// cancelled (applyGroundworks), so a note that is spent stops voting in the
// same block, and a stake msg naming a split re-casts the vote on the note it
// creates. Every unit of derth is in one unspent note, so the live weight
// never exceeds the unspent voted derth. A vote weighs derth x rate_v in the
// Groundworks stream, at the rate of the last epoch end, so every vote of a
// validator moves together once an epoch and not with every block's rewards.
//
// Votes are weighed per validator, not one by one. For each validator v and
// option o the module keeps T[v][o] = sum over v's live votes of derth x
// percent (exact integers: nothing is divided until the weight is taken),
// adjusted by each cast and cancel in O(options). All of v's votes are one
// Groundworks voter (types.ValidatorVoterKey) with an absolute weight on each
// option, trunc(rate_v x T[v][o] / 100) (allocation.SetWeightedVoter). An
// epoch end or a slash re-files one voter per validator: the work grows with
// validators, never with votes, so votes need no cap.
//
// A note's exposure (derth a move brought in, labelled: a slash of the
// move's source may still cut it until the move's window closes) votes too,
// pending: stored beside the vote with the move's key and time and not
// counted, then added to the vote's derth once the window closes, at what
// the slash debt tree says it is worth (matureVotes, BeginBlock). Lane A's
// is published by the proof (pending_key/time/exposed, the kept label's), the
// credit lane's is the credit itself (the msg's move). No re-vote is needed
// for moved stake to count.
//
// A vote is leased (x/allocation groundworks_lease_seconds, a year by
// default, as a caretaker split): it counts until split_expires_at, and
// every stake msg that re-casts it starts a new lease. At the lapse the vote
// comes off its validator's totals at that exact time and is deleted:
// x/allocation settles the stream to it first (voteLapser, a types.Lapser).
// A vote at a jailed or unbonded validator keeps its weight until then
// (audit C-8, decided).
//
// A governance reset of the stream (ResetAllocations) bumps its epoch. A
// vote counts only in the epoch it was cast in (split_epoch), and a
// validator's totals only in the epoch recorded for them (GwEpoch): after a
// reset both are stale, treated as zero, and the owner votes again (a
// restake of the note onto itself with its split), as any voter does.

// setVote stores v, keeping its lease in the lapse queue, its pending
// exposure in the maturity queue and its tag indexed.
func (k Keeper) setVote(ctx context.Context, v types.GroundworksVote) error {
	if err := k.unqueueLapse(ctx, v.Id); err != nil {
		return err
	}
	if v.SplitExpiresAt > 0 {
		if err := k.GwLapses.Set(ctx, collections.Join(v.SplitExpiresAt, v.Id)); err != nil {
			return err
		}
	}
	if hasPending(v) {
		if err := k.GwMatures.Set(ctx, collections.Join(v.MaturesAt, v.Id)); err != nil {
			return err
		}
	}
	if err := k.GwVotesByTag.Set(ctx, v.Tag, v.Id); err != nil {
		return err
	}
	v.Weight = math.ZeroInt() // not stored: withLiveWeight
	return k.GwVotes.Set(ctx, v.Id, v)
}

// removeVote deletes vote v, its lease and its tag.
func (k Keeper) removeVote(ctx context.Context, v types.GroundworksVote) error {
	if err := k.unqueueLapse(ctx, v.Id); err != nil {
		return err
	}
	if err := k.GwVotesByTag.Remove(ctx, v.Tag); err != nil {
		return err
	}
	return k.GwVotes.Remove(ctx, v.Id)
}

// unqueueLapse drops the stored vote id's lease from the lapse queue and
// its pending exposure from the maturity queue.
func (k Keeper) unqueueLapse(ctx context.Context, id uint64) error {
	old, err := k.GwVotes.Get(ctx, id)
	if errors.Is(err, collections.ErrNotFound) {
		return nil
	} else if err != nil {
		return err
	}
	if old.SplitExpiresAt > 0 {
		if err := k.GwLapses.Remove(ctx, collections.Join(old.SplitExpiresAt, id)); err != nil {
			return err
		}
	}
	if hasPending(old) {
		return k.GwMatures.Remove(ctx, collections.Join(old.MaturesAt, id))
	}
	return nil
}

func hasPending(v types.GroundworksVote) bool { return !v.Pending.IsNil() && v.Pending.IsPositive() }

// pendingPart is an output's exposure, voting pending: its move's key and
// time and the exposed derth (none: exposed 0).
type pendingPart struct {
	key     []byte
	time    uint64
	exposed uint64
}

// gwOutput is one output's vote: its tag, its weight now, its pending
// exposure, and the validator it votes at.
type gwOutput struct {
	tag     []byte
	w       uint64
	pending pendingPart
	val     string
}

func (o gwOutput) votes() bool { return o.w > 0 || o.pending.exposed > 0 }

// gwOutputs is m's two outputs' votes: lane A's (its pending exposure the
// proof's), the credit lane's (a move's: its pending exposure the credit,
// keyed by the credit lane's nullifier, at the msg's move_time).
func gwOutputs(m types.StakeMsg) []gwOutput {
	p := m.StakeProofOf()
	laneA, credit := m.Validators()
	a := gwOutput{tag: p.VoteTag, w: p.VoteWeight, val: laneA,
		pending: pendingPart{key: p.PendingKey, time: p.PendingTime, exposed: p.PendingExposed}}
	b := gwOutput{tag: p.CreditVoteTag, w: p.CreditVoteWeight, val: credit}
	if r, ok := m.(*types.MsgRedelegate); ok && !isZeroBytes(p.CreditVoteTag) {
		b.pending = pendingPart{key: p.CreditNullifier, time: r.MoveTime, exposed: r.DstDerth}
	}
	return []gwOutput{a, b}
}

func isZeroBytes(b []byte) bool {
	for _, x := range b {
		if x != 0 {
			return false
		}
	}
	return true
}

// maturesAt is when an exposure moved under key at moveTime may count: the
// first second its label could clear (moveTime < ClearBefore, under the
// label window as it stands now), and not before its move's x/staking entry
// completes: an entry merged into a later one (mergeOldEntries) stays
// slashable past the label window, and a debt row it takes then must still
// cut the exposure before it counts.
func (k Keeper) maturesAt(ctx context.Context, key []byte, moveTime uint64) (int64, error) {
	w, err := k.labelWindow(ctx)
	if err != nil {
		return 0, err
	}
	t := moveTime + w + 1
	if t < moveTime || t > uint64(1<<62) {
		return 1 << 62, nil
	}
	at := int64(t)
	mv, err := k.Moves.Get(ctx, key)
	if err != nil && !errors.Is(err, collections.ErrNotFound) {
		return 0, err
	}
	if err == nil && mv.Completion > 0 {
		if c := mv.Completion/1_000_000_000 + 1; c > at {
			at = c
		}
	}
	return at, nil
}

// exposureWorth is what exposed derth moved under key is worth now: the
// slash debt row's retained derth if the move was slashed, else all of it.
func (k Keeper) exposureWorth(ctx context.Context, key []byte, exposed math.Int) (math.Int, error) {
	r, err := k.DebtRetained.Get(ctx, key)
	if errors.Is(err, collections.ErrNotFound) {
		return exposed, nil
	} else if err != nil {
		return math.Int{}, err
	}
	return math.MinInt(exposed, math.NewIntFromUint64(r)), nil
}

// voteByTag returns the vote stored under tag, if any.
func (k Keeper) voteByTag(ctx context.Context, tag []byte) (types.GroundworksVote, bool, error) {
	id, err := k.GwVotesByTag.Get(ctx, tag)
	if errors.Is(err, collections.ErrNotFound) {
		return types.GroundworksVote{}, false, nil
	} else if err != nil {
		return types.GroundworksVote{}, false, err
	}
	v, err := k.GwVotes.Get(ctx, id)
	if err != nil {
		return v, false, err
	}
	return v, true, nil
}

// gwEpoch is the Groundworks stream's allocation epoch.
func (k Keeper) gwEpoch(ctx context.Context) (uint64, error) {
	return k.allocation.StreamEpoch(ctx, allocationtypes.STREAM_ID_GROUNDWORKS)
}

// voteLive reports whether v counts in Groundworks epoch epoch.
func voteLive(v types.GroundworksVote, epoch uint64) bool {
	return len(v.Splits) > 0 && v.SplitEpoch == epoch
}

// withLiveWeight is v with Weight filled in: derth x its validator's epoch
// rate while it counts, else zero (what queries and events show).
func (k Keeper) withLiveWeight(ctx context.Context, v types.GroundworksVote) types.GroundworksVote {
	v.Weight = math.ZeroInt()
	if epoch, err := k.gwEpoch(ctx); err == nil && voteLive(v, epoch) {
		v.Weight = k.voteWeight(ctx, v.Validator, v.Derth)
	}
	return v
}

// freshTotals makes v's totals belong to epoch: totals from an older epoch
// (before a reset) are deleted first.
func (k Keeper) freshTotals(ctx context.Context, v string, epoch uint64) error {
	e, err := k.GwEpoch.Get(ctx, v)
	if err == nil && e == epoch {
		return nil
	}
	if err != nil && !errors.Is(err, collections.ErrNotFound) {
		return err
	}
	if err := k.clearTotals(ctx, v); err != nil {
		return err
	}
	return k.GwEpoch.Set(ctx, v, epoch)
}

// clearTotals deletes v's totals.
func (k Keeper) clearTotals(ctx context.Context, v string) error {
	return k.GwTotals.Clear(ctx, collections.NewPrefixedPairRange[string, uint64](v))
}

// addTotal adds delta to m[key], deleting it at zero; below zero is an
// invariant failure.
func addTotal(ctx context.Context, m collections.Map[collections.Pair[string, uint64], math.Int], key collections.Pair[string, uint64], delta math.Int) error {
	cur, err := m.Get(ctx, key)
	if errors.Is(err, collections.ErrNotFound) {
		cur = math.ZeroInt()
	} else if err != nil {
		return err
	}
	next := cur.Add(delta)
	switch {
	case next.IsNegative():
		return types.ErrInvariant.Wrapf("groundworks total %s/%d below zero", key.K1(), key.K2())
	case next.IsZero():
		return m.Remove(ctx, key)
	default:
		return m.Set(ctx, key, next)
	}
}

// addVoteTotals adds (sign 1) or removes (sign -1) a live vote's
// contribution, derth x percent per option, to its validator's totals.
// Removing exactly what adding put there is the point: the same vote always
// contributes the same integers.
func (k Keeper) addVoteTotals(ctx context.Context, p types.GroundworksVote, sign int64, epoch uint64) error {
	if err := k.freshTotals(ctx, p.Validator, epoch); err != nil {
		return err
	}
	for _, w := range p.Splits {
		if err := addTotal(ctx, k.GwTotals, collections.Join(p.Validator, w.OptionId), p.Derth.MulRaw(int64(w.Percent)).MulRaw(sign)); err != nil {
			return err
		}
	}
	return nil
}

// validatorOptionWeights is v's Groundworks voter: trunc(rate_v x T[v][o] /
// 100) per option, nothing when its totals are stale.
//
// Deliberately not gated on v's status (audit C-8, decided: keep). Votes are
// private stake whose holders chose where Groundworks money goes; they
// keep that weight while their validator is jailed, unbonding or unbonded,
// as they keep their derth (and earn nothing meanwhile). Only an operator's
// self-bond is gated, counting at a Bonded validator alone (D7-L2): that is
// the operator's own power, which a validator out of the set does not hold.
func (k Keeper) validatorOptionWeights(ctx context.Context, v string, epoch uint64) ([]allocationtypes.OptionWeight, error) {
	e, err := k.GwEpoch.Get(ctx, v)
	if errors.Is(err, collections.ErrNotFound) || (err == nil && e != epoch) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	rate := k.epochRate(ctx, v)
	var ws []allocationtypes.OptionWeight
	err = k.GwTotals.Walk(ctx, collections.NewPrefixedPairRange[string, uint64](v),
		func(key collections.Pair[string, uint64], t math.Int) (bool, error) {
			// An option pruned since a vote named it takes nothing
			// (x/allocation skips it); the voter leaves it out (audit 7).
			if ok, err := k.gwOptionExists(ctx, key.K2()); err != nil || !ok {
				return err != nil, err
			}
			w := rate.MulInt(t).QuoInt64(100).TruncateInt()
			if w.IsPositive() {
				ws = append(ws, allocationtypes.OptionWeight{OptionId: key.K2(), Weight: w})
			}
			return false, nil
		})
	return ws, err
}

// gwOptionExists reports whether Groundworks option id still exists (not
// pruned).
func (k Keeper) gwOptionExists(ctx context.Context, id uint64) (bool, error) {
	return k.allocation.Options.Has(ctx, collections.Join(uint32(allocationtypes.STREAM_ID_GROUNDWORKS), id))
}

// existingSplits is splits without the options pruned since (what an export
// writes: x/allocation's export drops them from its voters alike, audit 6
// D-L-A1; a vote naming one would rebuild totals naming it).
func (k Keeper) existingSplits(ctx context.Context, splits []allocationtypes.AllocationWeight) ([]allocationtypes.AllocationWeight, error) {
	var out []allocationtypes.AllocationWeight
	for _, w := range splits {
		ok, err := k.gwOptionExists(ctx, w.OptionId)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, w)
		}
	}
	return out, nil
}

// syncValidatorVoter re-files v's Groundworks voter from its totals at its
// epoch rate; stale totals (a reset since) are deleted and the voter cleared.
func (k Keeper) syncValidatorVoter(ctx context.Context, v string) error {
	return k.syncValidatorVoterWith(ctx, v, false)
}

// syncValidatorVoterWith is syncValidatorVoter; settled: the stream is
// already settled to the moment of the change (a lapse), so the voter is
// written without settling to the block time. Otherwise the stream is
// settled first, before the totals are read: nothing between the read and
// the write may move them (audit round 2, CD-1).
func (k Keeper) syncValidatorVoterWith(ctx context.Context, v string, settled bool) error {
	if !settled {
		if err := k.allocation.AdvanceIndex(ctx, allocationtypes.STREAM_ID_GROUNDWORKS); err != nil {
			return err
		}
	}
	valBz, err := k.valAddr(v)
	if err != nil {
		return err
	}
	epoch, err := k.gwEpoch(ctx)
	if err != nil {
		return err
	}
	if e, err := k.GwEpoch.Get(ctx, v); err == nil && e != epoch {
		if err := k.clearTotals(ctx, v); err != nil {
			return err
		}
		if err := k.GwEpoch.Remove(ctx, v); err != nil {
			return err
		}
	}
	// No live totals left (the last vote cancelled or lapsed): the
	// validator leaves the Groundworks index, so nothing walks or re-files
	// it until a note votes there again.
	empty := true
	if err := k.GwTotals.Walk(ctx, collections.NewPrefixedPairRange[string, uint64](v),
		func(collections.Pair[string, uint64], math.Int) (bool, error) { empty = false; return true, nil }); err != nil {
		return err
	}
	if empty {
		if err := k.GwEpoch.Remove(ctx, v); err != nil {
			return err
		}
	}
	ws, err := k.validatorOptionWeights(ctx, v, epoch)
	if err != nil {
		return err
	}
	return k.allocation.SetWeightedVoterSettled(ctx, allocationtypes.STREAM_ID_GROUNDWORKS, types.ValidatorVoterKey(valBz), ws)
}

// resyncValidatorVoter is syncValidatorVoter in its own cache, for EndBlock:
// on failure v keeps its old weights until the next try.
func (k Keeper) resyncValidatorVoter(ctx context.Context, v string) {
	if err := k.guarded(ctx, func(cc context.Context) error { return k.syncValidatorVoter(cc, v) }); err != nil {
		k.failure(ctx, "reweigh_groundworks", v, err)
	}
}

// ReweighGroundworks re-files every validator's Groundworks voter at its
// epoch rate: one voter per validator with live votes, whatever the number
// of votes. Unbounded in validators, so the epoch end does not
// run it (the bounded book sweep re-files each voter, resyncBooks); for
// tests and tools. Never fails.
func (k Keeper) ReweighGroundworks(ctx context.Context) {
	var vals []string
	_ = k.GwEpoch.Walk(ctx, nil, func(v string, _ uint64) (bool, error) {
		vals = append(vals, v)
		return false, nil
	})
	for _, v := range vals {
		k.resyncValidatorVoter(ctx, v)
	}
}

// castVote stores a vote of derth at validator under tag, split by splits,
// leased from now: its contribution goes on the validator's totals in the
// current epoch and the validator's voter is re-filed. The stream is settled
// first (syncValidatorVoter reads the totals after).
func (k Keeper) castVote(ctx context.Context, tag []byte, validator string, derth math.Int, pending pendingPart, splits []allocationtypes.AllocationWeight) error {
	if err := k.allocation.AdvanceIndex(ctx, allocationtypes.STREAM_ID_GROUNDWORKS); err != nil {
		return err
	}
	epoch, err := k.gwEpoch(ctx)
	if err != nil {
		return err
	}
	expires, err := k.allocation.GroundworksLeaseEnd(ctx)
	if err != nil {
		return err
	}
	id, err := k.GwVoteSeq.Next(ctx)
	if err != nil {
		return err
	}
	v := types.GroundworksVote{
		Id: id, Validator: validator, Derth: derth, Splits: splits, Tag: tag,
		CreatedHeight: sdk.UnwrapSDKContext(ctx).BlockHeight(), Weight: math.ZeroInt(),
		SplitEpoch: epoch, SplitExpiresAt: expires, Pending: math.ZeroInt(),
	}
	if pending.exposed > 0 {
		at, err := k.maturesAt(ctx, pending.key, pending.time)
		if err != nil {
			return err
		}
		ex := math.NewIntFromUint64(pending.exposed)
		if sdk.UnwrapSDKContext(ctx).BlockTime().Unix() >= at {
			// Its window closed already (a label not cleared yet): it counts now.
			worth, err := k.exposureWorth(ctx, pending.key, ex)
			if err != nil {
				return err
			}
			v.Derth = v.Derth.Add(worth)
		} else {
			v.Pending, v.PendingKey, v.PendingMoveTime, v.MaturesAt = ex, pending.key, pending.time, at
		}
	}
	// Nothing to weigh (a fully exposed note whose move a slash cut to
	// nothing): no vote.
	if v.Derth.IsZero() && !hasPending(v) {
		return nil
	}
	if err := k.addVoteTotals(ctx, v, 1, epoch); err != nil {
		return err
	}
	if err := k.setVote(ctx, v); err != nil {
		return err
	}
	if err := k.syncValidatorVoter(ctx, validator); err != nil {
		return err
	}
	k.voteEvent(ctx, "cast", v)
	return nil
}

// cancelVote deletes the vote stored under tag, if any: its live
// contribution comes off its validator's totals and the voter is re-filed.
// A tag no vote is stored under (a note that did not vote, a padding input)
// is nothing to cancel.
func (k Keeper) cancelVote(ctx context.Context, tag []byte) error {
	v, ok, err := k.voteByTag(ctx, tag)
	if err != nil || !ok {
		return err
	}
	// Settled before the totals change; a settle never retires a lease
	// (only x/allocation's BeginBlock sweep does), so v is still what is
	// stored when its contribution comes off (audit round 2, CD-1).
	if err := k.allocation.AdvanceIndex(ctx, allocationtypes.STREAM_ID_GROUNDWORKS); err != nil {
		return err
	}
	epoch, err := k.gwEpoch(ctx)
	if err != nil {
		return err
	}
	live := voteLive(v, epoch)
	if live {
		if err := k.addVoteTotals(ctx, v, -1, epoch); err != nil {
			return err
		}
	}
	if err := k.removeVote(ctx, v); err != nil {
		return err
	}
	if live {
		if err := k.syncValidatorVoter(ctx, v.Validator); err != nil {
			return err
		}
	}
	k.voteEvent(ctx, "cancelled", v)
	return nil
}

// applyGroundworks is a stake proof's Groundworks effect, after the proof's
// spends: every input tag cancels the vote stored under it, then each output
// that votes casts one with the msg's split (lane A's at its validator, the
// credit lane's at the credited validator). checkGroundworks refused, in the
// ante, everything this would.
func (k Keeper) applyGroundworks(ctx context.Context, m types.StakeMsg) error {
	p := m.StakeProofOf()
	for _, tag := range p.InputTags() {
		if err := k.cancelVote(ctx, tag); err != nil {
			return err
		}
	}
	split := m.GroundworksSplitOf()
	for _, o := range gwOutputs(m) {
		if !o.votes() {
			continue
		}
		if err := k.castVote(ctx, o.tag, o.val, math.NewIntFromUint64(o.w), o.pending, split); err != nil {
			return err
		}
	}
	return nil
}

// checkGroundworks refuses, in the ante, a msg whose Groundworks effect
// applyGroundworks would refuse: a split the stream does not take, a vote
// below min_groundworks_vote ((derth + pending) x epoch rate), beyond a note's value or
// weighing nothing at its validator, or under a tag a vote is already stored under that this
// proof does not cancel.
func (k Keeper) checkGroundworks(ctx context.Context, m types.StakeMsg) error {
	p := m.StakeProofOf()
	split := m.GroundworksSplitOf()
	if len(split) == 0 {
		return nil
	}
	if err := k.allocation.ValidateSplit(ctx, allocationtypes.STREAM_ID_GROUNDWORKS, split); err != nil {
		return err
	}
	params, err := k.Params.Get(ctx)
	if err != nil {
		return err
	}
	cancelled := map[string]bool{}
	for _, t := range p.InputTags() {
		cancelled[string(t)] = true
	}
	for _, o := range gwOutputs(m) {
		if !o.votes() {
			continue
		}
		// Weighed with its pending exposure: what it counts once matured.
		d := math.NewIntFromUint64(o.w).Add(math.NewIntFromUint64(o.pending.exposed))
		if err := fitsNote(d); err != nil {
			return err
		}
		weight := k.voteWeight(ctx, o.val, d)
		if !weight.IsPositive() {
			return allocationtypes.ErrNoWeight
		}
		if weight.LT(params.MinPosition) {
			return types.ErrGroundworksVote.Wrapf("a vote weighs at least %s", params.MinPosition)
		}
		if _, ok, err := k.voteByTag(ctx, o.tag); err != nil {
			return err
		} else if ok && !cancelled[string(o.tag)] {
			return types.ErrGroundworksVote.Wrap("a vote is already stored under this tag")
		}
	}
	return nil
}

// epochRate is v's rate at the last epoch end, 1 before its first.
func (k Keeper) epochRate(ctx context.Context, valoper string) math.LegacyDec {
	vs, err := k.ValidatorState(ctx, valoper)
	if err != nil || vs.EpochRate.IsNil() || !vs.EpochRate.IsPositive() {
		return math.LegacyOneDec()
	}
	return vs.EpochRate
}

// voteWeight is derth x epoch rate, truncated.
func (k Keeper) voteWeight(ctx context.Context, valoper string, derth math.Int) math.Int {
	return k.epochRate(ctx, valoper).MulInt(derth).TruncateInt()
}

// GroundworksWeightSource is the Groundworks stream's weight source:
//
//   - a validator's votes voter (types.ValidatorVoterKey): zero here;
//     this module files its option weights itself (SetWeightedVoter);
//   - this module's own account: zero. Its delegations are the private stake,
//     already counted through the votes; counting them again as an
//     account's bond would weigh that stake twice;
//   - any other account: its stake at Bonded validators. Transparent
//     delegation is refused except a validator operator's self-bond, so that
//     is all it can be: a validator's self-bond votes like any stake, while
//     its validator is in the active set.
type GroundworksWeightSource struct{ k Keeper }

// NewGroundworksWeightSource returns the source to register with x/allocation.
func NewGroundworksWeightSource(k Keeper) GroundworksWeightSource {
	return GroundworksWeightSource{k: k}
}

var (
	_ allocationtypes.WeightSource  = GroundworksWeightSource{}
	_ allocationtypes.BondedTracker = GroundworksWeightSource{}
)

func (s GroundworksWeightSource) Weight(ctx context.Context, key []byte) (math.Int, error) {
	if !s.TracksBonded(key) {
		return math.ZeroInt(), nil
	}
	// Bonded validators only (audit 7, D7-L2), as x/allocation resyncs it.
	return s.k.allocation.BondedWeight(ctx, sdk.AccAddress(key))
}

// TracksBonded: x/allocation's staking hooks resync an account voter's weight
// from its bonded stake when a delegation changes. Not for a validator's
// votes voter (this module files its option weights itself,
// SetWeightedVoter) and never for this module's account, whose delegations
// change every epoch and carry no weight.
func (s GroundworksWeightSource) TracksBonded(key []byte) bool {
	if types.IsValidatorVoterKey(key) {
		return false
	}
	return !sdk.AccAddress(key).Equals(s.k.modAddr)
}

// reweighSlashed: a slash lowers a validator's rate at once (its delegation
// lost tokens), so its votes must not keep voting at the pre-slash epoch
// rate until the next epoch. For each validator recorded this block (slashed,
// by the slash hook, which runs before the tokens move; the source and every
// destination of a slashed redelegation; both sides of a private
// redelegation, audit 7): the live rate becomes its epoch rate if lower,
// and only its votes re-weigh at it, each in its own cache. Rewards still
// reach votes at the daily epoch. Never fails: it runs in
// EndBlock.
func (k Keeper) reweighSlashed(ctx context.Context) {
	var vals []string
	_ = k.SlashedValidators.Walk(ctx, nil, func(v string) (bool, error) {
		vals = append(vals, v)
		return false, nil
	})
	for _, v := range vals {
		err := k.guarded(ctx, func(cc context.Context) error {
			// A validator with no book has no votes to re-weigh; writing
			// one here would only create an empty book (audit 6 C-L1).
			if has, err := k.Validators.Has(cc, v); err != nil || !has {
				return err
			}
			rate, err := k.Rate(cc, v)
			if err != nil {
				return err
			}
			vs, err := k.ValidatorState(cc, v)
			if err != nil {
				return err
			}
			// The live rate also counts the rewards accrued since the epoch
			// end (and the queue), which votes only take at the next
			// epoch: a slash may only lower the epoch rate, never raise it.
			if !vs.EpochRate.IsNil() && vs.EpochRate.IsPositive() && rate.GT(vs.EpochRate) {
				rate = vs.EpochRate
			}
			vs.EpochRate = rate
			return k.Validators.Set(cc, v, vs)
		})
		if err != nil {
			k.failure(ctx, "slash_rate", v, err)
		} else if _, err := k.GwEpoch.Get(ctx, v); err == nil {
			k.resyncValidatorVoter(ctx, v)
		}
		_ = k.SlashedValidators.Remove(ctx, v)
	}
}

// gwKey is a (validator, option) totals key as a comparable map key.
type gwKey struct {
	val    string
	option uint64
}

// gwTotalsOf computes every validator's totals from the votes: the sum of
// derth x percent over its votes that count in epoch. O(votes): for
// InitGenesis and the invariant only.
func (k Keeper) gwTotalsOf(ctx context.Context, epoch uint64) (map[gwKey]math.Int, error) {
	out := map[gwKey]math.Int{}
	err := k.GwVotes.Walk(ctx, nil, func(_ uint64, p types.GroundworksVote) (bool, error) {
		if !voteLive(p, epoch) {
			return false, nil
		}
		for _, w := range p.Splits {
			key := gwKey{p.Validator, w.OptionId}
			c := p.Derth.MulRaw(int64(w.Percent))
			if cur, ok := out[key]; ok {
				c = c.Add(cur)
			}
			out[key] = c
		}
		return false, nil
	})
	// Stored totals are never zero (addTotal deletes them): a vote all of
	// whose weight is still pending adds none.
	for key, c := range out {
		if c.IsZero() {
			delete(out, key)
		}
	}
	return out, err
}

// rebuildGwTotals writes the totals from the imported votes (InitGenesis;
// x/allocation, initialized first, already holds the voters).
func (k Keeper) rebuildGwTotals(ctx context.Context) error {
	epoch, err := k.gwEpoch(ctx)
	if err != nil {
		return err
	}
	totals, err := k.gwTotalsOf(ctx, epoch)
	if err != nil {
		return err
	}
	for key, t := range totals {
		if err := k.GwTotals.Set(ctx, collections.Join(key.val, key.option), t); err != nil {
			return err
		}
		if err := k.GwEpoch.Set(ctx, key.val, epoch); err != nil {
			return err
		}
	}
	return nil
}

// assertGwTotals: every validator's stored totals (in the current epoch)
// equal the sum of its live votes' contributions.
func (k Keeper) assertGwTotals(ctx context.Context) error {
	epoch, err := k.gwEpoch(ctx)
	if err != nil {
		return err
	}
	want, err := k.gwTotalsOf(ctx, epoch)
	if err != nil {
		return err
	}
	seen := 0
	err = k.GwTotals.Walk(ctx, nil, func(key collections.Pair[string, uint64], t math.Int) (bool, error) {
		e, err := k.GwEpoch.Get(ctx, key.K1())
		if err != nil || e != epoch {
			return false, nil // stale: counts as zero, deleted on next touch
		}
		w, ok := want[gwKey{key.K1(), key.K2()}]
		if !ok || !w.Equal(t) {
			return true, types.ErrInvariant.Wrapf("groundworks total %s/%d is %s, votes give %s", key.K1(), key.K2(), t, w)
		}
		seen++
		return false, nil
	})
	if err != nil {
		return err
	}
	if seen != len(want) {
		return types.ErrInvariant.Wrapf("%d groundworks totals stored, votes give %d", seen, len(want))
	}
	return nil
}

// voteLapser retires Groundworks votes at their lease end (x/allocation
// settles the stream to that time first; types.Lapser).
type voteLapser struct{ k Keeper }

// NewGroundworksLapser returns the lapser to register with x/allocation for
// the Groundworks stream.
func NewGroundworksLapser(k Keeper) allocationtypes.Lapser { return voteLapser{k: k} }

func (l voteLapser) NextLapse(ctx context.Context, t int64) (int64, bool, error) {
	it, err := l.k.GwLapses.Iterate(ctx, new(collections.Range[collections.Pair[int64, uint64]]).
		EndExclusive(collections.Join(t+1, uint64(0))))
	if err != nil {
		return 0, false, err
	}
	defer it.Close()
	if !it.Valid() {
		return 0, false, nil
	}
	key, err := it.Key()
	if err != nil {
		return 0, false, err
	}
	return key.K1(), true, nil
}

// Lapse deletes every vote whose lease ends at t: off its validator's
// totals, the validator's voter re-filed (settled: the stream is at t). Per
// validator in its own cache; a validator whose lapse fails keeps those
// votes counting a day longer (re-queued) instead of stalling.
func (l voteLapser) Lapse(ctx context.Context, t int64) error {
	k := l.k
	byVal := map[string][]uint64{}
	var vals []string
	if err := k.GwLapses.Walk(ctx, collections.NewPrefixedPairRange[int64, uint64](t),
		func(key collections.Pair[int64, uint64]) (bool, error) {
			p, err := k.GwVotes.Get(ctx, key.K2())
			v := ""
			if err == nil {
				v = p.Validator
			} else if !errors.Is(err, collections.ErrNotFound) {
				return true, err
			}
			if _, ok := byVal[v]; !ok {
				vals = append(vals, v)
			}
			byVal[v] = append(byVal[v], key.K2())
			return false, nil
		}); err != nil {
		return err
	}
	for _, v := range vals {
		ids := byVal[v]
		err := k.guarded(ctx, func(cc context.Context) error {
			epoch, err := k.gwEpoch(cc)
			if err != nil {
				return err
			}
			touched := false
			for _, id := range ids {
				if err := k.GwLapses.Remove(cc, collections.Join(t, id)); err != nil {
					return err
				}
				p, err := k.GwVotes.Get(cc, id)
				if errors.Is(err, collections.ErrNotFound) {
					continue
				} else if err != nil {
					return err
				}
				// Re-cast since (its new lease is queued under a new id).
				if p.SplitExpiresAt > t {
					continue
				}
				if voteLive(p, epoch) {
					if err := k.addVoteTotals(cc, p, -1, epoch); err != nil {
						return err
					}
					touched = true
				}
				if err := k.GwVotesByTag.Remove(cc, p.Tag); err != nil {
					return err
				}
				if err := k.GwVotes.Remove(cc, id); err != nil {
					return err
				}
				k.voteEvent(cc, "lapsed", p)
			}
			if touched {
				return k.syncValidatorVoterWith(cc, v, true)
			}
			return nil
		})
		if err != nil {
			k.failure(ctx, "lapse_groundworks", v, err)
			retry := allocationkeeper.LapseRetryAt(ctx, t)
			allocationkeeper.EmitLeaseRetireFailed(ctx, allocationtypes.STREAM_ID_GROUNDWORKS, "groundworks_votes", v, t, retry, err)
			for _, id := range ids {
				if err := k.GwLapses.Remove(ctx, collections.Join(t, id)); err != nil {
					return err
				}
				if err := k.GwLapses.Set(ctx, collections.Join(retry, id)); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// splitsAttr is a split as an event attribute: option:percent, comma-separated.
func splitsAttr(splits []allocationtypes.AllocationWeight) string {
	parts := make([]string, len(splits))
	for i, w := range splits {
		parts[i] = strconv.FormatUint(w.OptionId, 10) + ":" + strconv.FormatUint(w.Percent, 10)
	}
	return strings.Join(parts, ",")
}

// voteEvent emits a Groundworks vote's change: cast, cancelled or lapsed.
func (k Keeper) voteEvent(ctx context.Context, action string, v types.GroundworksVote) {
	v = k.withLiveWeight(ctx, v)
	sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(sdk.NewEvent(types.EventTypeGroundworksVote,
		sdk.NewAttribute(types.AttributeKeyAction, action),
		sdk.NewAttribute(types.AttributeKeyVoteID, strconv.FormatUint(v.Id, 10)),
		sdk.NewAttribute(types.AttributeKeyTag, hex.EncodeToString(v.Tag)),
		sdk.NewAttribute(types.AttributeKeyValidator, v.Validator),
		sdk.NewAttribute(types.AttributeKeyDerth, v.Derth.String()),
		sdk.NewAttribute(types.AttributeKeyWeight, v.Weight.String()),
		sdk.NewAttribute(types.AttributeKeyOptions, splitsAttr(v.Splits)),
		sdk.NewAttribute(types.AttributeKeySplitExpiresAt, strconv.FormatInt(v.SplitExpiresAt, 10)),
		sdk.NewAttribute(types.AttributeKeyPending, pendingAttr(v)),
		sdk.NewAttribute(types.AttributeKeyMaturesAt, strconv.FormatInt(v.MaturesAt, 10)),
	))
}

func pendingAttr(v types.GroundworksVote) string {
	if v.Pending.IsNil() {
		return "0"
	}
	return v.Pending.String()
}

// maturesPerBlock bounds matureVotes' work in one block; the rest waits a
// block (each is a few writes and a voter re-filed).
const maturesPerBlock = 200

// matureVotes adds every due pending exposure to its vote (BeginBlock,
// after the slash watch settles, so a debt row written this block is seen):
// at what the debt tree says it is worth, the vote's validator's totals and
// voter updated. A window grown since the vote was cast (unbonding_time
// raised) is waited out (re-queued). Each in its own cache: one that fails
// is retried a day later and never halts the block.
func (k Keeper) matureVotes(ctx context.Context) {
	now := sdk.UnwrapSDKContext(ctx).BlockTime().Unix()
	var due []collections.Pair[int64, uint64]
	it, err := k.GwMatures.Iterate(ctx, new(collections.Range[collections.Pair[int64, uint64]]).
		EndExclusive(collections.Join(now+1, uint64(0))))
	if err != nil {
		k.failure(ctx, "mature_groundworks", "", err)
		return
	}
	for ; it.Valid() && len(due) < maturesPerBlock; it.Next() {
		key, err := it.Key()
		if err != nil {
			break
		}
		due = append(due, key)
	}
	it.Close()
	for _, key := range due {
		err := k.guarded(ctx, func(cc context.Context) error { return k.matureVote(cc, key) })
		if err != nil {
			k.failure(ctx, "mature_groundworks", "", err)
			_ = k.guarded(ctx, func(cc context.Context) error {
				v, err := k.GwVotes.Get(cc, key.K2())
				if err != nil {
					return k.GwMatures.Remove(cc, key)
				}
				if err := k.GwMatures.Remove(cc, key); err != nil {
					return err
				}
				v.MaturesAt = key.K1() + 86400
				return k.setVote(cc, v)
			})
		}
	}
}

// matureVote counts vote key.K2()'s pending exposure (due at key.K1()).
func (k Keeper) matureVote(ctx context.Context, key collections.Pair[int64, uint64]) error {
	v, err := k.GwVotes.Get(ctx, key.K2())
	if errors.Is(err, collections.ErrNotFound) {
		return k.GwMatures.Remove(ctx, key)
	} else if err != nil {
		return err
	}
	if !hasPending(v) || v.MaturesAt != key.K1() {
		return k.GwMatures.Remove(ctx, key)
	}
	at, err := k.maturesAt(ctx, v.PendingKey, v.PendingMoveTime)
	if err != nil {
		return err
	}
	if sdk.UnwrapSDKContext(ctx).BlockTime().Unix() < at {
		// The window grew, or the move's entry completes later (merged): wait it out.
		if err := k.GwMatures.Remove(ctx, key); err != nil {
			return err
		}
		v.MaturesAt = at
		return k.setVote(ctx, v)
	}
	worth, err := k.exposureWorth(ctx, v.PendingKey, v.Pending)
	if err != nil {
		return err
	}
	if err := k.allocation.AdvanceIndex(ctx, allocationtypes.STREAM_ID_GROUNDWORKS); err != nil {
		return err
	}
	epoch, err := k.gwEpoch(ctx)
	if err != nil {
		return err
	}
	live := voteLive(v, epoch)
	if live {
		if err := k.addVoteTotals(ctx, v, -1, epoch); err != nil {
			return err
		}
	}
	if err := k.GwMatures.Remove(ctx, key); err != nil {
		return err
	}
	v.Derth = v.Derth.Add(worth)
	v.Pending, v.PendingKey, v.PendingMoveTime, v.MaturesAt = math.ZeroInt(), nil, 0, 0
	if v.Derth.IsZero() {
		// A slash cut it to nothing: the vote weighs nothing, and goes.
		if err := k.removeVote(ctx, v); err != nil {
			return err
		}
		if live {
			if err := k.syncValidatorVoter(ctx, v.Validator); err != nil {
				return err
			}
		}
		k.voteEvent(ctx, "cancelled", v)
		return nil
	}
	if live {
		if err := k.addVoteTotals(ctx, v, 1, epoch); err != nil {
			return err
		}
	}
	if err := k.setVote(ctx, v); err != nil {
		return err
	}
	if live {
		if err := k.syncValidatorVoter(ctx, v.Validator); err != nil {
			return err
		}
	}
	k.voteEvent(ctx, "matured", v)
	return nil
}

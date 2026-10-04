package keeper

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"

	"cosmossdk.io/collections"
	errorsmod "cosmossdk.io/errors"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	"github.com/earth-network/earth/x/shieldedstaking/types"
	"github.com/earth-network/earth/zk/privacy"
)

// InitGenesis loads the books. It runs after bank, staking and shielded, and
// checks the books against them.
func (k Keeper) InitGenesis(ctx context.Context, gs types.GenesisState) error {
	// Validate first: what follows reads the amounts (a nil Requested would
	// panic InitChain rather than refuse the file; audit 5 L-ST2).
	if err := gs.Validate(); err != nil {
		return err
	}
	if err := k.checkGenesisValidators(ctx, gs); err != nil {
		return err
	}
	if err := k.checkUnbondingEntries(ctx, gs.Params); err != nil {
		return err
	}
	// The moves before x/staking's redelegations are checked against them.
	if err := k.initMoves(ctx, gs); err != nil {
		return err
	}
	if err := k.checkGenesisDelegations(ctx, gs); err != nil {
		return err
	}
	if err := k.checkModuleWithdrawAddr(ctx); err != nil {
		return err
	}
	if err := k.Params.Set(ctx, gs.Params); err != nil {
		return err
	}
	epoch := gs.Epoch
	if epoch == nil {
		now := sdk.UnwrapSDKContext(ctx).BlockTime().Unix()
		epoch = &types.Epoch{Number: 1, StartTime: now, EndTime: now + int64(gs.Params.EpochSeconds)}
	}
	if err := k.Epoch.Set(ctx, *epoch); err != nil {
		return err
	}
	for _, v := range gs.Validators {
		if err := k.Validators.Set(ctx, v.Validator, v); err != nil {
			return err
		}
	}
	for _, r := range gs.UnbondRecords {
		key := collections.Join(r.Validator, r.Epoch)
		if err := k.UnbondRecords.Set(ctx, key, r); err != nil {
			return err
		}
		if r.Requested.IsZero() {
			if err := k.OrphanRecords.Set(ctx, key); err != nil {
				return err
			}
		}
		switch r.Status {
		case types.UNBOND_STATUS_PENDING:
			if err := k.PendingRecords.Set(ctx, key); err != nil {
				return err
			}
		case types.UNBOND_STATUS_UNBONDING:
			if err := k.MaturityQueue.Set(ctx, collections.Join3(r.CompletionTime, r.Validator, r.Epoch)); err != nil {
				return err
			}
		}
	}
	// Payouts: untried ones under their record (a MATURED record with any is
	// marked for the sweep), failed ones under their retry time.
	for _, p := range gs.UnbondPayouts {
		if err := k.UnbondPayouts.Set(ctx, p.Id, p); err != nil {
			return err
		}
		if p.RetryAt > 0 {
			if err := k.PayoutRetries.Set(ctx, collections.Join(p.RetryAt, p.Id)); err != nil {
				return err
			}
			continue
		}
		if err := k.PayoutsByRecord.Set(ctx, collections.Join3(p.Validator, p.Epoch, p.Id)); err != nil {
			return err
		}
		r, err := k.UnbondRecords.Get(ctx, collections.Join(p.Validator, p.Epoch))
		if err != nil {
			return err
		}
		if r.Status == types.UNBOND_STATUS_MATURED {
			if err := k.MaturedRecords.Set(ctx, collections.Join(p.Validator, p.Epoch)); err != nil {
				return err
			}
		}
	}
	if err := k.UnbondPayoutSeq.Set(ctx, gs.NextUnbondPayoutId); err != nil {
		return err
	}
	for _, p := range gs.Positions {
		if err := k.setPosition(ctx, p); err != nil {
			return err
		}
		if err := k.PositionsByVal.Set(ctx, collections.Join(p.Validator, p.Id)); err != nil {
			return err
		}
	}
	if err := k.PositionSeq.Set(ctx, gs.NextPositionId); err != nil {
		return err
	}
	if err := k.rebuildGwTotals(ctx); err != nil {
		return err
	}
	if err := k.SnapshotSeq.Set(ctx, gs.SnapshotSeq); err != nil {
		return err
	}
	for _, c := range gs.SupplyCheckpoints {
		if err := k.SupplyCheckpoints.Set(ctx, collections.Join(c.Validator, c.Seq), c.Supply); err != nil {
			return err
		}
		if err := k.CheckpointsBySeq.Set(ctx, collections.Join(c.Seq, c.Validator)); err != nil {
			return err
		}
	}
	if gs.EpochSweep != nil {
		if err := k.EpochSweep.Set(ctx, *gs.EpochSweep); err != nil {
			return err
		}
	}
	// An open snapshot's height is compared with the new chain's heights (a
	// position created at or after it may not vote), so it must be below the
	// chain's first height: a zero-height export shifts it below 1
	// (ResetHeightsForZeroHeight), and an export relaunched at
	// initial_height = export height + 1 has it below already. A snapshot at
	// or above the initial height would let positions created on the new
	// chain vote on it (audit 4, I2).
	// (InitChain's context carries the initial height when it is above 1,
	// and 0 for a chain starting at 1.)
	initialHeight := sdk.UnwrapSDKContext(ctx).BlockHeight()
	if initialHeight < 1 {
		initialHeight = 1
	}
	for _, s := range gs.Snapshots {
		if s.Height >= initialHeight {
			return fmt.Errorf("snapshot %d: height %d is not below the initial height %d (export for zero height, or relaunch past it)",
				s.ProposalId, s.Height, initialHeight)
		}
	}
	for _, s := range gs.Snapshots {
		if err := k.Snapshots.Set(ctx, s.ProposalId, s); err != nil {
			return err
		}
		if err := k.SnapshotExpiry.Set(ctx, collections.Join(s.VotingEnd, s.ProposalId)); err != nil {
			return err
		}
		if s.Seq > 0 {
			if err := k.SnapshotsBySeq.Set(ctx, collections.Join(s.Seq, s.ProposalId)); err != nil {
				return err
			}
		}
	}
	for _, v := range gs.Votes {
		if err := k.putVote(ctx, v); err != nil {
			return err
		}
	}
	if err := k.initStakeTree(ctx, gs); err != nil {
		return err
	}
	retiring := map[string]bool{}
	for _, r := range gs.RetiringEscrows {
		if err := k.RetiringEscrows.Set(ctx, collections.Join(r.ReleaseAt, r.Validator)); err != nil {
			return err
		}
		retiring[string(r.Validator)] = true
	}
	if err := k.initGenesisEscrows(ctx, retiring); err != nil {
		return err
	}
	// Removed validators whose escrow release failed: x/staking no longer
	// lists them, so their escrows are recorded again here, for the epoch
	// end's retry (retryEscrowReleases) to find.
	for _, v := range gs.PendingReleases {
		if err := k.RewardEscrows.Set(ctx, types.RewardEscrowAddress(v), v); err != nil {
			return err
		}
		if err := k.PendingReleases.Set(ctx, v); err != nil {
			return err
		}
	}
	return k.AssertInvariants(ctx)
}

// checkUnbondingEntries refuses params under which an epoch's undelegation
// could hit x/staking's max_entries for the module's delegation to one
// validator (audit F8): one entry per epoch over the unbonding time, plus
// one for a sweep under way.
func (k Keeper) checkUnbondingEntries(ctx context.Context, p types.Params) error {
	ut, err := k.staking.UnbondingTime(ctx)
	if err != nil {
		return err
	}
	maxEntries, err := k.staking.MaxEntries(ctx)
	if err != nil {
		return err
	}
	if need := p.MaxEpochUnbondings(uint64(ut / time.Second)); need > uint64(maxEntries) {
		return types.ErrInvalidMsg.Wrapf("epoch_seconds %d with x/staking's unbonding_time %s needs max_entries >= %d, have %d",
			p.EpochSeconds, ut, need, maxEntries)
	}
	return nil
}

// checkGenesisValidators refuses a genesis whose books, records, positions,
// snapshots or votes name a validator by any string but its canonical
// encoding under this chain's validator codec (keeper.valAddr).
func (k Keeper) checkGenesisValidators(_ context.Context, gs types.GenesisState) error {
	check := func(what, v string) error {
		if _, err := k.valAddr(v); err != nil {
			return fmt.Errorf("genesis %s: %w", what, err)
		}
		return nil
	}
	for _, v := range gs.Validators {
		if err := check("book", v.Validator); err != nil {
			return err
		}
	}
	for _, r := range gs.UnbondRecords {
		if err := check("unbond record", r.Validator); err != nil {
			return err
		}
	}
	for _, p := range gs.Positions {
		if err := check("position", p.Validator); err != nil {
			return err
		}
	}
	for _, s := range gs.Snapshots {
		for _, vs := range s.Validators {
			if err := check("snapshot", vs.Validator); err != nil {
				return err
			}
		}
	}
	for _, v := range gs.Votes {
		if err := check("vote", v.Validator); err != nil {
			return err
		}
	}
	for _, c := range gs.SupplyCheckpoints {
		if err := check("supply checkpoint", c.Validator); err != nil {
			return err
		}
	}
	if gs.EpochSweep != nil && gs.EpochSweep.Cursor != "" {
		if err := check("epoch sweep cursor", gs.EpochSweep.Cursor); err != nil {
			return err
		}
	}
	return nil
}

// initStakeTree rebuilds the stake note tree from its leaves, its nullifier
// set and its roots. Every root record, and every snapshot's note root, must
// be the rebuilt tree's root at its tree_size, and no record may be dated
// after genesis (audit 4, G1 / L-B: only the last was checked, so any other
// record or a snapshot could carry a forged anchor). The last record is the
// latest and must be the rebuilt tree's root.
func (k Keeper) initStakeTree(ctx context.Context, gs types.GenesisState) error {
	genesisTime := sdk.UnwrapSDKContext(ctx).BlockTime().Unix()
	n := uint64(len(gs.StakeCommitments))
	want := map[uint64][][]byte{} // tree_size -> roots claimed there
	for i, r := range gs.StakeRoots {
		if r.Time > genesisTime {
			return fmt.Errorf("stake root %d: time %d is after genesis time %d", i, r.Time, genesisTime)
		}
		if r.TreeSize > n {
			return fmt.Errorf("stake root %d: tree_size %d is past the tree's %d", i, r.TreeSize, n)
		}
		want[r.TreeSize] = append(want[r.TreeSize], r.Root)
	}
	for _, s := range gs.Snapshots {
		if len(s.Root) == 0 {
			continue
		}
		if s.TreeSize > n {
			return fmt.Errorf("snapshot %d: tree_size %d is past the stake tree's %d", s.ProposalId, s.TreeSize, n)
		}
		want[s.TreeSize] = append(want[s.TreeSize], s.Root)
	}
	t, err := k.stakeTree(ctx)
	if err != nil {
		return err
	}
	if t.Size() != 0 {
		return fmt.Errorf("stake tree is not empty at genesis")
	}
	check := func() error {
		roots, ok := want[t.Size()]
		if !ok {
			return nil
		}
		root, err := t.Root()
		if err != nil {
			return err
		}
		for _, r := range roots {
			if !bytes.Equal(r, privacy.FieldBytes(root)) {
				return fmt.Errorf("stake root %X is not the root of the first %d stake commitments", r, t.Size())
			}
		}
		return nil
	}
	if err := check(); err != nil {
		return err
	}
	for i, cm := range gs.StakeCommitments {
		leaf, err := privacy.FieldFromBytes(cm)
		if err != nil {
			return fmt.Errorf("stake commitment %d: %w", i, err)
		}
		if _, err := t.Append(leaf); err != nil {
			return err
		}
		if err := check(); err != nil {
			return err
		}
	}
	if err := k.StakeTreeSize.Set(ctx, t.Size()); err != nil {
		return err
	}
	if err := k.initNfTree(ctx, gs); err != nil {
		return err
	}
	for i, r := range gs.StakeRoots {
		if err := k.putStakeRoot(ctx, r, i == len(gs.StakeRoots)-1); err != nil {
			return err
		}
	}
	if t.Size() == 0 {
		return nil
	}
	root, err := t.Root()
	if err != nil {
		return err
	}
	latest, err := k.StakeLatestRoot.Get(ctx)
	if err != nil {
		return err
	}
	if !bytes.Equal(latest, privacy.FieldBytes(root)) {
		return fmt.Errorf("the stake tree's root %X is not its latest recorded root %X", privacy.FieldBytes(root), latest)
	}
	return nil
}

// initNfTree rebuilds the stake nullifier tree by inserting the nullifiers in
// their exported (insertion) order, and checks every snapshot's nf_root
// against the tree as it stood at the snapshot's nf_size. The rebuilt root is
// the latest recorded one (genesis is the end of a block).
func (k Keeper) initNfTree(ctx context.Context, gs types.GenesisState) error {
	want := map[uint64][][]byte{} // nf_size -> the nf_roots snapshots took there
	for _, s := range gs.Snapshots {
		if len(s.NfRoot) > 0 {
			want[s.NfSize] = append(want[s.NfSize], s.NfRoot)
		}
	}
	t, err := k.nfTree(ctx)
	if err != nil {
		return err
	}
	check := func() error {
		roots, ok := want[t.Size()]
		if !ok {
			return nil
		}
		root, err := t.Root()
		if err != nil {
			return err
		}
		for _, r := range roots {
			if !bytes.Equal(r, privacy.FieldBytes(root)) {
				return fmt.Errorf("a snapshot's stake nullifier root %X is not the tree's at size %d (%X)", r, t.Size(), privacy.FieldBytes(root))
			}
		}
		delete(want, t.Size())
		return nil
	}
	if err := check(); err != nil { // size 0: the empty root
		return err
	}
	for i, nf := range gs.StakeNullifiers {
		v, err := privacy.FieldFromBytes(nf)
		if err != nil {
			return fmt.Errorf("stake nullifier %d: %w", i, err)
		}
		if _, err := t.Insert(v); err != nil {
			return fmt.Errorf("stake nullifier %d: %w", i, err)
		}
		if err := check(); err != nil {
			return err
		}
	}
	if len(want) > 0 {
		return fmt.Errorf("%d snapshot stake nullifier root(s) at sizes the tree never had", len(want))
	}
	if err := k.StakeNfSize.Set(ctx, t.Size()); err != nil {
		return err
	}
	if t.Size() == 0 {
		return nil
	}
	root, err := t.Root()
	if err != nil {
		return err
	}
	if err := k.StakeNfLatestRoot.Set(ctx, privacy.FieldBytes(root)); err != nil {
		return err
	}
	return k.StakeNfLatestSize.Set(ctx, t.Size())
}

// ExportGenesis exports the books.
func (k Keeper) ExportGenesis(ctx context.Context) (*types.GenesisState, error) {
	params, err := k.Params.Get(ctx)
	if err != nil {
		return nil, err
	}
	gs := &types.GenesisState{Params: params}
	if e, err := k.Epoch.Get(ctx); err == nil {
		gs.Epoch = &e
	} else if !errors.Is(err, collections.ErrNotFound) {
		return nil, err
	}
	if err := k.Validators.Walk(ctx, nil, func(_ string, v types.ValidatorState) (bool, error) {
		gs.Validators = append(gs.Validators, v)
		return false, nil
	}); err != nil {
		return nil, err
	}
	if err := k.UnbondRecords.Walk(ctx, nil, func(_ collections.Pair[string, uint64], r types.UnbondRecord) (bool, error) {
		gs.UnbondRecords = append(gs.UnbondRecords, r)
		return false, nil
	}); err != nil {
		return nil, err
	}
	if err := k.UnbondPayouts.Walk(ctx, nil, func(_ uint64, p types.UnbondPayout) (bool, error) {
		gs.UnbondPayouts = append(gs.UnbondPayouts, p)
		return false, nil
	}); err != nil {
		return nil, err
	}
	if gs.NextUnbondPayoutId, err = k.UnbondPayoutSeq.Peek(ctx); err != nil {
		return nil, err
	}
	if err := k.Positions.Walk(ctx, nil, func(_ uint64, p types.Position) (bool, error) {
		// A split naming an option pruned since loses it (audit 7, as
		// x/allocation's export drops it from its voters); a split left with
		// nothing is no split.
		if len(p.Splits) > 0 {
			kept, err := k.existingSplits(ctx, p.Splits)
			if err != nil {
				return true, err
			}
			if len(kept) == 0 {
				p.Splits, p.SplitEpoch = nil, 0
			} else {
				p.Splits = kept
			}
		}
		gs.Positions = append(gs.Positions, p)
		return false, nil
	}); err != nil {
		return nil, err
	}
	if gs.NextPositionId, err = k.PositionSeq.Peek(ctx); err != nil {
		return nil, err
	}
	if gs.SnapshotSeq, err = k.SnapshotSeq.Peek(ctx); err != nil {
		return nil, err
	}
	if err := k.SupplyCheckpoints.Walk(ctx, nil, func(key collections.Pair[string, uint64], supply math.Int) (bool, error) {
		gs.SupplyCheckpoints = append(gs.SupplyCheckpoints, types.SupplyCheckpoint{Validator: key.K1(), Seq: key.K2(), Supply: supply})
		return false, nil
	}); err != nil {
		return nil, err
	}
	if sweep, err := k.EpochSweep.Get(ctx); err == nil {
		if sweep.Active {
			gs.EpochSweep = &sweep
		}
	} else if !errors.Is(err, collections.ErrNotFound) {
		return nil, err
	}
	if err := k.Snapshots.Walk(ctx, nil, func(_ uint64, s types.ProposalSnapshot) (bool, error) {
		gs.Snapshots = append(gs.Snapshots, s)
		return false, nil
	}); err != nil {
		return nil, err
	}
	if err := k.Votes.Walk(ctx, nil, func(_ collections.Pair[uint64, []byte], v types.StakeVote) (bool, error) {
		gs.Votes = append(gs.Votes, v)
		return false, nil
	}); err != nil {
		return nil, err
	}
	t, err := k.stakeTree(ctx)
	if err != nil {
		return nil, err
	}
	for i := uint64(0); i < t.Size(); i++ {
		l, err := t.Leaf(i)
		if err != nil {
			return nil, err
		}
		gs.StakeCommitments = append(gs.StakeCommitments, privacy.FieldBytes(l))
	}
	// Nullifiers in insertion order (leaf 1, 2, ...): InitGenesis re-inserts
	// them so, rebuilding the same tree.
	if err := k.StakeNfValues.Walk(ctx, nil, func(_ uint64, nf []byte) (bool, error) {
		gs.StakeNullifiers = append(gs.StakeNullifiers, nf)
		return false, nil
	}); err != nil {
		return nil, err
	}
	// Roots oldest first, the latest last (InitGenesis makes the last one
	// the latest).
	latest, err := k.StakeLatestRoot.Get(ctx)
	if err != nil && !errors.Is(err, collections.ErrNotFound) {
		return nil, err
	}
	var last *types.StakeRoot
	if err := k.StakeRootsByTime.Walk(ctx, nil, func(key collections.Pair[int64, []byte]) (bool, error) {
		r, err := k.StakeRoots.Get(ctx, key.K2())
		if err != nil {
			return true, err
		}
		if bytes.Equal(r.Root, latest) {
			last = &r
			return false, nil
		}
		gs.StakeRoots = append(gs.StakeRoots, r)
		return false, nil
	}); err != nil {
		return nil, err
	}
	if last != nil {
		gs.StakeRoots = append(gs.StakeRoots, *last)
	}
	if err := k.PendingReleases.Walk(ctx, nil, func(v []byte) (bool, error) {
		gs.PendingReleases = append(gs.PendingReleases, v)
		return false, nil
	}); err != nil {
		return nil, err
	}
	if err := k.RetiringEscrows.Walk(ctx, nil, func(key collections.Pair[int64, []byte]) (bool, error) {
		gs.RetiringEscrows = append(gs.RetiringEscrows, types.RetiringEscrow{ReleaseAt: key.K1(), Validator: key.K2()})
		return false, nil
	}); err != nil {
		return nil, err
	}
	// Moves still open to slashing, the debt rows in insertion order (each
	// with its latest retained), and the longest unbonding_time seen.
	if err := k.Moves.Walk(ctx, nil, func(_ []byte, mv types.Move) (bool, error) {
		gs.Moves = append(gs.Moves, mv)
		return false, nil
	}); err != nil {
		return nil, err
	}
	if gs.DebtRows, err = k.DebtRows(ctx, 0, ^uint64(0)); err != nil {
		return nil, err
	}
	if m, err := k.MaxUnbonding.Get(ctx); err == nil {
		gs.MaxUnbondingSeconds = m
	} else if !errors.Is(err, collections.ErrNotFound) {
		return nil, err
	}
	return gs, nil
}

// checkGenesisDelegations enforces the delegation rule on what x/staking
// loaded (it runs no hooks for an exported genesis): every delegation and
// unbonding delegation is this module's or an operator's on its own
// validator, and every redelegation is this module's (private
// redelegations in flight; a self-bond moved to another validator would stop
// being one, so an operator never redelegates).
//
// A delegation of this module's needs its validator's book (audit 6 C-I2):
// without one, delegating there is refused as settling, and the sweep, which
// walks books, never settles it.
func (k Keeper) checkGenesisDelegations(ctx context.Context, gs types.GenesisState) error {
	dels, err := k.staking.GetAllDelegations(ctx)
	if err != nil {
		return err
	}
	books := make(map[string]bool, len(gs.Validators))
	for _, v := range gs.Validators {
		books[v.Validator] = true
	}
	for _, d := range dels {
		del, val, err := k.delegationAddrs(d.DelegatorAddress, d.ValidatorAddress)
		if err != nil {
			return err
		}
		if !k.AllowedDelegator(del, val) {
			return errorsmod.Wrapf(types.ErrTransparentStaking, "genesis delegation %s -> %s", d.DelegatorAddress, d.ValidatorAddress)
		}
		if del.Equals(k.modAddr) && !books[d.ValidatorAddress] {
			return errorsmod.Wrapf(types.ErrValidator, "genesis: the module's delegation to %s has no book", d.ValidatorAddress)
		}
	}
	var bad error
	if err := k.staking.IterateUnbondingDelegations(ctx, func(_ int64, u stakingtypes.UnbondingDelegation) bool {
		del, val, err := k.delegationAddrs(u.DelegatorAddress, u.ValidatorAddress)
		if err != nil {
			bad = err
		} else if !k.AllowedDelegator(del, val) {
			bad = errorsmod.Wrapf(types.ErrTransparentStaking, "genesis unbonding delegation %s -> %s", u.DelegatorAddress, u.ValidatorAddress)
		}
		return bad != nil
	}); err != nil {
		return err
	}
	if bad != nil {
		return bad
	}
	held := 0
	if err := k.staking.IterateRedelegations(ctx, func(_ int64, r stakingtypes.Redelegation) bool {
		n, err := k.checkRedelegationRecord(ctx, r)
		if err != nil {
			bad = fmt.Errorf("genesis %w", err)
		}
		held += n
		return bad != nil
	}); err != nil {
		return err
	}
	if bad != nil {
		return bad
	}
	if err := k.checkOpenMoves(ctx, held); err != nil {
		return fmt.Errorf("genesis: %w", err)
	}
	return nil
}

// checkRedelegationRecord is the rule for an x/staking redelegation, at
// genesis and in invariant 9: only this module redelegates (a private
// redelegation; an operator's self-bond cannot move), between two different
// canonical validators, with at least one entry and at most
// MaxEntryHeightsPerPair of positive height, in creation-height order (a
// slash's replay relies on it: moves.go slashedEntries). An entry is known
// by its creation height and completion (two can share a height: a move in
// the block where the source began unbonding, and one after, at the
// source's unbonding height). Every unmatured entry of positive height is
// the only one with its height and completion, and its moves (those at its
// height with its completion) hold exactly its shares, so a slash of it is
// owed by them. An entry at height 0 or below
// is a zero-height export's (x/staking reset its height; the export dropped
// its moves): no slash on this chain reaches it, so no shares are checked;
// a move there (a source unbonding since the export) must still match one
// such entry's completion. Returns how many unmatured moves the record's
// entries hold: every unmatured move must be one (invariant 10, genesis).
func (k Keeper) checkRedelegationRecord(ctx context.Context, r stakingtypes.Redelegation) (int, error) {
	del, err := k.addressCodec.StringToBytes(r.DelegatorAddress)
	if err != nil {
		return 0, fmt.Errorf("redelegation delegator %q: %w", r.DelegatorAddress, err)
	}
	if !sdk.AccAddress(del).Equals(k.modAddr) {
		return 0, errorsmod.Wrapf(types.ErrTransparentStaking, "redelegation %s: %s -> %s (only private staking redelegates)",
			r.DelegatorAddress, r.ValidatorSrcAddress, r.ValidatorDstAddress)
	}
	src, err := k.valAddr(r.ValidatorSrcAddress)
	if err != nil {
		return 0, err
	}
	dst, err := k.valAddr(r.ValidatorDstAddress)
	if err != nil {
		return 0, err
	}
	if src.Equals(dst) {
		return 0, errorsmod.Wrapf(types.ErrRedelegation, "redelegation %s -> itself", r.ValidatorSrcAddress)
	}
	id := entryID(r.ValidatorSrcAddress, r.ValidatorDstAddress)
	if n := countedEntries(r.Entries); len(r.Entries) == 0 || n > types.MaxEntryHeightsPerPair {
		return 0, errorsmod.Wrapf(types.ErrRedelegation, "redelegation %s has %d entries, %d of positive height (1.., at most %d)",
			id, len(r.Entries), n, types.MaxEntryHeightsPerPair)
	}
	now := sdk.UnwrapSDKContext(ctx).BlockTime()
	// The unmatured entries' completions, by height.
	open := map[int64]map[int64]math.LegacyDec{}
	var heights []int64
	for i, e := range r.Entries {
		if i > 0 && e.CreationHeight < r.Entries[i-1].CreationHeight {
			return 0, errorsmod.Wrapf(types.ErrRedelegation, "redelegation %s: entries out of creation-height order", id)
		}
		if e.IsMature(now) {
			continue
		}
		h, c := e.CreationHeight, e.CompletionTime.UnixNano()
		if open[h] == nil {
			open[h] = map[int64]math.LegacyDec{}
			heights = append(heights, h)
		}
		if _, dup := open[h][c]; dup && h > 0 {
			return 0, errorsmod.Wrapf(types.ErrRedelegation, "redelegation %s: two entries at height %d completing at %d", id, h, c)
		}
		open[h][c] = e.SharesDst
	}
	moves := 0
	for _, h := range heights {
		sum := map[int64]math.LegacyDec{}
		rng := collections.NewSuperPrefixedTripleRange[string, int64, []byte](id, h)
		if err := k.MovesByEntry.Walk(ctx, rng, func(key collections.Triple[string, int64, []byte]) (bool, error) {
			mv, err := k.Moves.Get(ctx, key.K3())
			if err != nil {
				return true, err
			}
			if mv.SrcValidator != r.ValidatorSrcAddress || mv.DstValidator != r.ValidatorDstAddress || mv.EntryHeight != h {
				return true, errorsmod.Wrapf(types.ErrRedelegation, "move %X is indexed under %s at height %d", mv.Key, id, h)
			}
			if _, ok := open[h][mv.Completion]; !ok {
				return true, errorsmod.Wrapf(types.ErrRedelegation, "move %X: completion %d, no entry of %s at height %d has it", mv.Key, mv.Completion, id, h)
			}
			moves++
			if s, ok := sum[mv.Completion]; ok {
				sum[mv.Completion] = s.Add(mv.Shares)
			} else {
				sum[mv.Completion] = mv.Shares
			}
			return false, nil
		}); err != nil {
			return 0, err
		}
		if h <= 0 {
			continue
		}
		for c, shares := range open[h] {
			got, ok := sum[c]
			if !ok {
				got = math.LegacyZeroDec()
			}
			if !got.Equal(shares) {
				return 0, errorsmod.Wrapf(types.ErrRedelegation, "redelegation %s at height %d: its moves hold %s shares, the entry %s",
					id, h, got, shares)
			}
		}
	}
	return moves, nil
}

// checkOpenMoves: every unmatured move is one of the held moves the module's
// unmatured redelegation entries hold (checkRedelegationRecord counts them).
func (k Keeper) checkOpenMoves(ctx context.Context, held int) error {
	now := sdk.UnwrapSDKContext(ctx).BlockTime().UnixNano()
	open := 0
	if err := k.Moves.Walk(ctx, nil, func(_ []byte, mv types.Move) (bool, error) {
		if mv.Completion > now {
			open++
		}
		return false, nil
	}); err != nil {
		return err
	}
	if open != held {
		return errorsmod.Wrapf(types.ErrRedelegation, "%d unmatured moves, %d of them in an unmatured entry of theirs", open, held)
	}
	return nil
}

// initMoves loads the moves, the slash debt tree (rows re-set in insertion
// order: the same tree) and the longest unbonding_time seen. Every move with
// a cut exposure has its row; a move's indexes are rebuilt.
func (k Keeper) initMoves(ctx context.Context, gs types.GenesisState) error {
	t, err := k.debtTree(ctx)
	if err != nil {
		return err
	}
	if t.Size() != 0 {
		return fmt.Errorf("slash debt tree is not empty at genesis")
	}
	for i, r := range gs.DebtRows {
		key, err := privacy.FieldFromBytes(r.Key)
		if err != nil {
			return fmt.Errorf("debt row %d: %w", i, err)
		}
		if _, err := t.Set(key, r.Retained); err != nil {
			return fmt.Errorf("debt row %d: %w", i, err)
		}
	}
	if err := k.DebtSize.Set(ctx, t.Size()); err != nil {
		return err
	}
	for _, mv := range gs.Moves {
		if _, err := k.valAddr(mv.SrcValidator); err != nil {
			return fmt.Errorf("genesis move: %w", err)
		}
		if _, err := k.valAddr(mv.DstValidator); err != nil {
			return fmt.Errorf("genesis move: %w", err)
		}
		r, err := k.DebtRetained.Get(ctx, mv.Key)
		slashed := err == nil
		if err != nil && !errors.Is(err, collections.ErrNotFound) {
			return err
		}
		if slashed && !mv.Retained.Equal(math.NewIntFromUint64(r)) || !slashed && !mv.Retained.Equal(mv.Credited) {
			return fmt.Errorf("genesis move %X: retained %s does not match its debt row", mv.Key, mv.Retained)
		}
		if err := k.putMove(ctx, mv); err != nil {
			return err
		}
	}
	if gs.MaxUnbondingSeconds > 0 {
		if err := k.MaxUnbonding.Set(ctx, gs.MaxUnbondingSeconds); err != nil {
			return err
		}
	}
	return k.noteMaxUnbonding(ctx)
}

func (k Keeper) delegationAddrs(del, val string) (sdk.AccAddress, sdk.ValAddress, error) {
	d, err := k.addressCodec.StringToBytes(del)
	if err != nil {
		return nil, nil, fmt.Errorf("genesis delegator %q: %w", del, err)
	}
	v, err := k.valAddr(val)
	if err != nil {
		return nil, nil, fmt.Errorf("genesis validator %q: %w", val, err)
	}
	return d, v, nil
}

// checkModuleWithdrawAddr refuses a genesis in which x/distribution pays
// this module's delegation rewards anywhere but the module account (audit
// 4, G2): the epoch end withdraws them by balance delta, so rewards paid
// elsewhere would simply never be booked, every private staker's yield
// going to whoever the withdraw address names, with no invariant noticing.
// Nothing on chain can set it (the module has no key, and MsgSetWithdrawAddress
// needs the delegator's signature), so genesis is the one way in.
func (k Keeper) checkModuleWithdrawAddr(ctx context.Context) error {
	wa, err := k.distr.GetDelegatorWithdrawAddr(ctx, k.modAddr)
	if err != nil {
		return err
	}
	if !wa.Equals(k.modAddr) {
		return fmt.Errorf("genesis: the %s module account's withdraw address is %s, not itself", types.ModuleName, wa)
	}
	return nil
}

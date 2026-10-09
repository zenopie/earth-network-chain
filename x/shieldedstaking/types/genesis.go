package types

import (
	"bytes"
	"fmt"

	"cosmossdk.io/math"

	shieldedtypes "github.com/earth-network/earth/x/shielded/types"
	"github.com/earth-network/earth/zk/privacy"
)

// DefaultGenesis is the launch state: default params, no epoch (the first
// starts at InitGenesis) and nothing staked.
func DefaultGenesis() *GenesisState {
	return &GenesisState{Params: DefaultParams(), NextPositionId: 0}
}

func nonNeg(what string, x math.Int) error {
	if x.IsNil() || x.IsNegative() {
		return fmt.Errorf("%s must be non-negative", what)
	}
	return nil
}

// Validate checks the genesis state's internal consistency. The books are
// checked against the bank and x/staking at InitGenesis (AssertInvariants).
func (gs GenesisState) Validate() error {
	if err := gs.Params.Validate(); err != nil {
		return err
	}
	vals := map[string]bool{}
	for _, v := range gs.Validators {
		if vals[v.Validator] {
			return fmt.Errorf("duplicate validator %s", v.Validator)
		}
		vals[v.Validator] = true
		if err := CanonicalValoper(v.Validator); err != nil {
			return fmt.Errorf("book: %w", err)
		}
		if err := nonNeg("pending_delegation", v.PendingDelegation); err != nil {
			return err
		}
		if err := nonNeg("pending_undelegation", v.PendingUndelegation); err != nil {
			return err
		}
		if err := nonNeg("derth_supply", v.DerthSupply); err != nil {
			return err
		}
		if v.EpochRate.IsNil() || v.EpochRate.IsNegative() {
			return fmt.Errorf("%s: invalid epoch rate", v.Validator)
		}
		if err := nonNeg("slash_debt", v.SlashDebt); err != nil {
			return err
		}
	}
	if err := gs.validateMoves(); err != nil {
		return err
	}
	recs := map[string]bool{}
	for _, r := range gs.UnbondRecords {
		key := fmt.Sprintf("%s/%d", r.Validator, r.Epoch)
		if recs[key] {
			return fmt.Errorf("duplicate unbond record %s", key)
		}
		recs[key] = true
		if err := CanonicalValoper(r.Validator); err != nil {
			return fmt.Errorf("unbond record: %w", err)
		}
		for what, x := range map[string]math.Int{
			"requested": r.Requested, "target": r.Target, "undelegated": r.Undelegated,
			"payout": r.Payout, "outstanding": r.Outstanding, "paid": r.Paid,
		} {
			if err := nonNeg(key+" "+what, x); err != nil {
				return err
			}
		}
		if r.Outstanding.GT(r.Requested) || (r.Requested.IsPositive() && r.Paid.GT(r.Payout)) {
			return fmt.Errorf("unbond record %s is inconsistent", key)
		}
	}
	if err := gs.validatePayouts(); err != nil {
		return err
	}
	ids := map[uint64]bool{}
	tags := map[string]bool{}
	for _, v := range gs.Positions {
		if ids[v.Id] || v.Id >= gs.NextPositionId {
			return fmt.Errorf("groundworks vote %d duplicated or not below next_groundworks_vote_id", v.Id)
		}
		ids[v.Id] = true
		if err := CanonicalValoper(v.Validator); err != nil {
			return fmt.Errorf("groundworks vote %d: %w", v.Id, err)
		}
		// A vote's derth is a stake note's unexposed amount: at most 2^63-1.
		if v.Derth.IsNil() || !shieldedtypes.FitsNote(v.Derth) {
			return fmt.Errorf("groundworks vote %d is invalid: derth must be 1..2^63-1", v.Id)
		}
		if t, err := privacy.FieldFromBytes(v.Tag); err != nil || t.IsZero() {
			return fmt.Errorf("groundworks vote %d: malformed tag", v.Id)
		}
		if tags[string(v.Tag)] {
			return fmt.Errorf("groundworks vote %d: tag repeated", v.Id)
		}
		tags[string(v.Tag)] = true
		// A vote is cast with a split and leased.
		if len(v.Splits) == 0 || v.SplitExpiresAt <= 0 {
			return fmt.Errorf("groundworks vote %d: a vote has a split and a lease", v.Id)
		}
	}
	snaps := map[uint64]bool{}
	seqs := map[uint64]bool{}
	for _, s := range gs.Snapshots {
		if s.Seq > 0 {
			if seqs[s.Seq] || s.Seq > gs.SnapshotSeq {
				return fmt.Errorf("snapshot %d: seq %d repeated or above snapshot_seq %d", s.ProposalId, s.Seq, gs.SnapshotSeq)
			}
			seqs[s.Seq] = true
		}
		if snaps[s.ProposalId] {
			return fmt.Errorf("duplicate snapshot %d", s.ProposalId)
		}
		snaps[s.ProposalId] = true
		if len(s.NfRoot) > 0 {
			if _, err := privacy.FieldFromBytes(s.NfRoot); err != nil {
				return fmt.Errorf("snapshot %d nf_root: %w", s.ProposalId, err)
			}
			if s.NfSize == 1 || s.NfSize > uint64(len(gs.StakeNullifiers))+1 {
				return fmt.Errorf("snapshot %d: nf_size %d, the nullifier tree has %d leaves", s.ProposalId, s.NfSize, len(gs.StakeNullifiers)+1)
			}
		}
		for _, vs := range s.Validators {
			if err := CanonicalValoper(vs.Validator); err != nil {
				return fmt.Errorf("snapshot %d: %w", s.ProposalId, err)
			}
		}
	}
	voteKeys := map[string]bool{}
	usedVnfs := map[string]bool{}
	for _, v := range gs.Votes {
		if k := fmt.Sprintf("%d/%x", v.ProposalId, v.Key); voteKeys[k] {
			return fmt.Errorf("vote %s repeated", k)
		} else {
			voteKeys[k] = true
		}
		if len(v.Key) != 33 || v.Key[0] != 0 {
			return fmt.Errorf("vote on proposal %d: malformed key %x", v.ProposalId, v.Key)
		}
		if !snaps[v.ProposalId] {
			return fmt.Errorf("vote on proposal %d without a snapshot", v.ProposalId)
		}
		if err := checkVoteNullifiers(v, usedVnfs); err != nil {
			return err
		}
		if err := CanonicalValoper(v.Validator); err != nil {
			return fmt.Errorf("vote on proposal %d: %w", v.ProposalId, err)
		}
		if err := ValidateOptions(v.Options); err != nil {
			return err
		}
	}
	cps := map[string]bool{}
	for _, c := range gs.SupplyCheckpoints {
		key := fmt.Sprintf("%s/%d", c.Validator, c.Seq)
		if cps[key] || c.Seq == 0 || c.Seq > gs.SnapshotSeq {
			return fmt.Errorf("supply checkpoint %s repeated or out of range", key)
		}
		cps[key] = true
		if err := CanonicalValoper(c.Validator); err != nil {
			return fmt.Errorf("supply checkpoint: %w", err)
		}
		if err := nonNeg("supply checkpoint "+key, c.Supply); err != nil {
			return err
		}
	}
	for _, v := range gs.Validators {
		if v.CheckpointSeq > gs.SnapshotSeq {
			return fmt.Errorf("%s: checkpoint_seq %d above snapshot_seq %d", v.Validator, v.CheckpointSeq, gs.SnapshotSeq)
		}
	}
	if gs.EpochSweep != nil && gs.EpochSweep.Cursor != "" {
		if err := CanonicalValoper(gs.EpochSweep.Cursor); err != nil {
			return fmt.Errorf("epoch sweep cursor: %w", err)
		}
	}
	for i, cm := range gs.StakeCommitments {
		if _, err := privacy.FieldFromBytes(cm); err != nil {
			return fmt.Errorf("stake commitment %d: %w", i, err)
		}
	}
	nfs := map[string]bool{}
	for i, nf := range gs.StakeNullifiers {
		if v, err := privacy.FieldFromBytes(nf); err != nil || v.IsZero() || nfs[string(nf)] {
			return fmt.Errorf("stake nullifier %d is malformed or repeated", i)
		}
		nfs[string(nf)] = true
	}
	roots := map[string]bool{}
	for _, r := range gs.StakeRoots {
		if _, err := privacy.FieldFromBytes(r.Root); err != nil || roots[string(r.Root)] {
			return fmt.Errorf("stake root %x is malformed or repeated", r.Root)
		}
		if r.TreeSize > uint64(len(gs.StakeCommitments)) {
			return fmt.Errorf("stake root %x claims %d leaves, the tree has %d", r.Root, r.TreeSize, len(gs.StakeCommitments))
		}
		roots[string(r.Root)] = true
	}
	if len(gs.StakeCommitments) > 0 && len(gs.StakeRoots) == 0 {
		return fmt.Errorf("a stake tree with no recorded root")
	}
	pending := map[string]bool{}
	for _, v := range gs.PendingReleases {
		if len(v) == 0 || pending[string(v)] {
			return fmt.Errorf("pending release %x is empty or repeated", v)
		}
		pending[string(v)] = true
	}
	retiring := map[string]bool{}
	for _, r := range gs.RetiringEscrows {
		if len(r.Validator) == 0 || r.ReleaseAt <= 0 || retiring[string(r.Validator)] {
			return fmt.Errorf("retiring escrow %x is malformed or repeated", r.Validator)
		}
		retiring[string(r.Validator)] = true
	}
	return nil
}

// validatePayouts checks the queued undelegation payouts: unique ids below
// next_unbond_payout_id, each against an existing record that has
// undelegations (not an orphan), a value of 1..2^63-1 (what an undelegation
// books), a pc and a blind ciphertext, retry_at set exactly when it failed
// before; and per record, the unpaid payouts sum to its outstanding.
func (gs GenesisState) validatePayouts() error {
	records := map[string]UnbondRecord{}
	for _, r := range gs.UnbondRecords {
		records[fmt.Sprintf("%s/%d", r.Validator, r.Epoch)] = r
	}
	ids := map[uint64]bool{}
	owed := map[string]math.Int{}
	for _, p := range gs.UnbondPayouts {
		if ids[p.Id] || p.Id >= gs.NextUnbondPayoutId {
			return fmt.Errorf("unbond payout %d duplicated or not below next_unbond_payout_id", p.Id)
		}
		ids[p.Id] = true
		key := fmt.Sprintf("%s/%d", p.Validator, p.Epoch)
		r, ok := records[key]
		if !ok || !r.Requested.IsPositive() {
			return fmt.Errorf("unbond payout %d: no unbond record %s with undelegations", p.Id, key)
		}
		if p.Value.IsNil() || !shieldedtypes.FitsNote(p.Value) {
			return fmt.Errorf("unbond payout %d: value must be 1..2^63-1", p.Id)
		}
		if _, err := privacy.FieldFromBytes(p.Pc); err != nil {
			return fmt.Errorf("unbond payout %d pc: %w", p.Id, err)
		}
		if err := shieldedtypes.CheckBlindCiphertext("ciphertext", p.Ciphertext); err != nil {
			return fmt.Errorf("unbond payout %d: %w", p.Id, err)
		}
		if p.RetryAt < 0 || (p.RetryAt > 0) != (p.PayoutAttempts > 0) {
			return fmt.Errorf("unbond payout %d: retry_at %d with %d attempts", p.Id, p.RetryAt, p.PayoutAttempts)
		}
		if cur, ok := owed[key]; ok {
			owed[key] = cur.Add(p.Value)
		} else {
			owed[key] = p.Value
		}
	}
	for key, r := range records {
		if !r.Requested.IsPositive() {
			continue
		}
		o, ok := owed[key]
		if !ok {
			o = math.ZeroInt()
		}
		if !o.Equal(r.Outstanding) {
			return fmt.Errorf("unbond record %s: outstanding %s, its payouts sum to %s", key, r.Outstanding, o)
		}
	}
	return nil
}

// checkVoteNullifiers: a note vote names exactly MaxVoteNotes vote
// nullifiers (padding included), non-zero canonical field elements, its
// key's first, none used by another
// vote on the proposal (used records proposal/vnf seen so far).
func checkVoteNullifiers(v StakeVote, used map[string]bool) error {
	if len(v.VoteNullifiers) != MaxVoteNotes {
		return fmt.Errorf("note vote on proposal %d: %d vote nullifiers, want %d", v.ProposalId, len(v.VoteNullifiers), MaxVoteNotes)
	}
	if !bytes.Equal(v.VoteNullifiers[0], v.Key[1:]) {
		return fmt.Errorf("note vote on proposal %d: key %x is not its first vote nullifier", v.ProposalId, v.Key)
	}
	for _, vnf := range v.VoteNullifiers {
		f, err := privacy.FieldFromBytes(vnf)
		if err != nil || f.IsZero() {
			return fmt.Errorf("note vote on proposal %d: malformed vote nullifier %x", v.ProposalId, vnf)
		}
		k := fmt.Sprintf("%d/%x", v.ProposalId, vnf)
		if used[k] {
			return fmt.Errorf("vote nullifier %s used twice", k)
		}
		used[k] = true
	}
	return nil
}

// validateMoves: the debt rows' keys are canonical, nonzero and distinct;
// every move names two different canonical validators, a canonical nonzero
// key of its own, a positive credit that fits a note, retained in
// 0..credited, positive shares and an entry (height >= 0, completion), and a
// cut exposure has its row. Every debt row's key and every move's key is a
// spent stake nullifier (a move key is its redelegation's credit nullifier,
// inserted with it): a row keyed elsewhere could stand for a label no
// redelegation made.
func (gs GenesisState) validateMoves() error {
	spent := make(map[string]bool, len(gs.StakeNullifiers))
	for _, nf := range gs.StakeNullifiers {
		spent[string(nf)] = true
	}
	rows := map[string]uint64{}
	for i, r := range gs.DebtRows {
		k, err := privacy.FieldFromBytes(r.Key)
		if err != nil || k.IsZero() {
			return fmt.Errorf("debt row %d: key is not a canonical nonzero field element", i)
		}
		if !spent[string(r.Key)] {
			return fmt.Errorf("debt row %d: key %X is not a spent stake nullifier", i, r.Key)
		}
		if _, dup := rows[string(r.Key)]; dup {
			return fmt.Errorf("debt row %d: repeated key", i)
		}
		rows[string(r.Key)] = r.Retained
	}
	keys := map[string]bool{}
	for _, mv := range gs.Moves {
		k, err := privacy.FieldFromBytes(mv.Key)
		if err != nil || k.IsZero() || keys[string(mv.Key)] {
			return fmt.Errorf("move %X: key is malformed or repeated", mv.Key)
		}
		keys[string(mv.Key)] = true
		if !spent[string(mv.Key)] {
			return fmt.Errorf("move %X: key is not a spent stake nullifier", mv.Key)
		}
		if err := CanonicalValoper(mv.SrcValidator); err != nil {
			return fmt.Errorf("move %X: %w", mv.Key, err)
		}
		if err := CanonicalValoper(mv.DstValidator); err != nil {
			return fmt.Errorf("move %X: %w", mv.Key, err)
		}
		if mv.SrcValidator == mv.DstValidator {
			return fmt.Errorf("move %X: to its own source", mv.Key)
		}
		if mv.Credited.IsNil() || !shieldedtypes.FitsNote(mv.Credited) {
			return fmt.Errorf("move %X: credited must be 1..2^63-1", mv.Key)
		}
		if mv.Retained.IsNil() || mv.Retained.IsNegative() || mv.Retained.GT(mv.Credited) {
			return fmt.Errorf("move %X: retained must be 0..credited", mv.Key)
		}
		if mv.Shares.IsNil() || !mv.Shares.IsPositive() || mv.EntryHeight < 0 || mv.Completion <= 0 || mv.MoveTime == 0 {
			return fmt.Errorf("move %X: needs positive shares, an entry height, a completion and a move time", mv.Key)
		}
		if r, ok := rows[string(mv.Key)]; ok && !math.NewIntFromUint64(r).Equal(mv.Retained) {
			return fmt.Errorf("move %X: retained %s, its debt row %d", mv.Key, mv.Retained, r)
		} else if !ok && !mv.Retained.Equal(mv.Credited) {
			return fmt.Errorf("move %X: a cut exposure without a debt row", mv.Key)
		}
	}
	return nil
}

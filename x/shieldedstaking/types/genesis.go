package types

import (
	"fmt"

	"cosmossdk.io/math"

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
	ids := map[uint64]bool{}
	for _, p := range gs.Positions {
		if ids[p.Id] || p.Id >= gs.NextPositionId {
			return fmt.Errorf("position %d duplicated or not below next_position_id", p.Id)
		}
		ids[p.Id] = true
		if err := CanonicalValoper(p.Validator); err != nil {
			return fmt.Errorf("position %d: %w", p.Id, err)
		}
		if p.Derth.IsNil() || !p.Derth.IsPositive() {
			return fmt.Errorf("position %d is invalid", p.Id)
		}
		if _, err := privacy.FieldFromBytes(p.OwnerTag); err != nil {
			return fmt.Errorf("position %d owner_tag: %w", p.Id, err)
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
		for _, vs := range s.Validators {
			if err := CanonicalValoper(vs.Validator); err != nil {
				return fmt.Errorf("snapshot %d: %w", s.ProposalId, err)
			}
		}
	}
	for _, v := range gs.Votes {
		if !snaps[v.ProposalId] {
			return fmt.Errorf("vote on proposal %d without a snapshot", v.ProposalId)
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
		if _, err := privacy.FieldFromBytes(nf); err != nil || nfs[string(nf)] {
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
	return nil
}

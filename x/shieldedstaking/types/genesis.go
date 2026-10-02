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
		if p.Derth.IsNil() || !p.Derth.IsPositive() {
			return fmt.Errorf("position %d is invalid", p.Id)
		}
		if _, err := privacy.FieldFromBytes(p.OwnerTag); err != nil {
			return fmt.Errorf("position %d owner_tag: %w", p.Id, err)
		}
	}
	snaps := map[uint64]bool{}
	for _, s := range gs.Snapshots {
		if snaps[s.ProposalId] {
			return fmt.Errorf("duplicate snapshot %d", s.ProposalId)
		}
		snaps[s.ProposalId] = true
	}
	for _, v := range gs.Votes {
		if !snaps[v.ProposalId] {
			return fmt.Errorf("vote on proposal %d without a snapshot", v.ProposalId)
		}
		if err := ValidateOptions(v.Options); err != nil {
			return err
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

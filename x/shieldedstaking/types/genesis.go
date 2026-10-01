package types

import (
	"fmt"

	"cosmossdk.io/math"
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
		if p.Derth.IsNil() || !p.Derth.IsPositive() || len(p.Pubkey) != 33 {
			return fmt.Errorf("position %d is invalid", p.Id)
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
	return nil
}

package types

import (
	"encoding/hex"
	"fmt"

	"github.com/earth-network/earth/zk/privacy"
)

// DefaultGenesis returns the default genesis state
func DefaultGenesis() *GenesisState {
	return &GenesisState{
		Params: DefaultParams(),
	}
}

// Validate performs basic genesis state validation returning an error upon any
// failure.
//
// The nullifier checks are the point. A nullifier is what makes a passport
// unable to register twice, so a genesis carrying two registrations under one
// nullifier — or a registration with none at all — has already lost the property
// the module exists to provide, and it must be refused at import rather than
// discovered later. So is a leaf index shared by two registrations, or one
// outside the tree: InitGenesis writes each registration's leaf at its index.
func (gs GenesisState) Validate() error {
	seenNullifier := make(map[string]struct{}, len(gs.Registrations))
	seenLeaf := make(map[uint64]struct{}, len(gs.Registrations))

	for _, reg := range gs.Registrations {
		// 1..32 bytes, as passports_seen (which InitGenesis fills from
		// these) requires: otherwise an import exports a file that fails
		// this check (audit 6 B6-7).
		if len(reg.Nullifier) == 0 || len(reg.Nullifier) > 32 {
			return fmt.Errorf("registration at leaf %d: a nullifier is 1..32 bytes", reg.LeafIndex)
		}
		n := hex.EncodeToString(reg.Nullifier)
		if _, dup := seenNullifier[n]; dup {
			return fmt.Errorf("nullifier %s is registered twice — one passport, two humans", n)
		}
		seenNullifier[n] = struct{}{}

		if reg.PredecessorAt < 0 {
			return fmt.Errorf("registration %s: negative predecessor_at", n)
		}
		if reg.LeafIndex >= gs.IdentityTreeSize {
			return fmt.Errorf("registration %s: leaf %d is outside the identity tree (size %d)", n, reg.LeafIndex, gs.IdentityTreeSize)
		}
		if _, dup := seenLeaf[reg.LeafIndex]; dup {
			return fmt.Errorf("leaf %d holds two registrations", reg.LeafIndex)
		}
		seenLeaf[reg.LeafIndex] = struct{}{}

		if _, err := privacy.FieldFromBytes(reg.Idc); err != nil {
			return fmt.Errorf("registration %s: idc: %w", n, err)
		}
		if len(reg.DscKey) > 0 {
			if _, err := privacy.FieldFromBytes(reg.DscKey); err != nil {
				return fmt.Errorf("registration %s: dsc_key: %w", n, err)
			}
		}
		if reg.RegisteredAt <= 0 {
			return fmt.Errorf("registration %s has no registration time; the expiry sweep "+
				"orders by it and would retire this one immediately", n)
		}
		if reg.ActivatedAt <= 0 {
			return fmt.Errorf("registration %s has no activation time", n)
		}
	}

	for i, r := range gs.IdentityRoots {
		if _, err := privacy.FieldFromBytes(r.Root); err != nil {
			return fmt.Errorf("identity root %d: %w", i, err)
		}
		// InitGenesis checks each record against the rebuilt tree (and the
		// genesis time); a size past the tree's is refused here already.
		if r.TreeSize > gs.IdentityTreeSize {
			return fmt.Errorf("identity root %d: tree_size %d is past the tree's %d", i, r.TreeSize, gs.IdentityTreeSize)
		}
	}
	seenClaim := map[string]struct{}{}
	for _, c := range gs.ClaimNullifiers {
		if _, err := privacy.FieldFromBytes(c.Nullifier); err != nil {
			return fmt.Errorf("claim nullifier: %w", err)
		}
		k := fmt.Sprintf("%d/%x", c.Day, c.Nullifier)
		if _, dup := seenClaim[k]; dup {
			return fmt.Errorf("claim nullifier %s listed twice", k)
		}
		seenClaim[k] = struct{}{}
	}
	seenVote := map[string]struct{}{}
	for _, v := range gs.CaretakerVotes {
		if _, err := privacy.FieldFromBytes(v.Nullifier); err != nil {
			return fmt.Errorf("caretaker vote: %w", err)
		}
		if _, dup := seenVote[string(v.Nullifier)]; dup {
			return fmt.Errorf("caretaker vote %x listed twice", v.Nullifier)
		}
		seenVote[string(v.Nullifier)] = struct{}{}
		if v.ExpiresAt <= 0 {
			return fmt.Errorf("caretaker vote %x has no expiry", v.Nullifier)
		}
	}

	if err := validateHandles(gs.Handles); err != nil {
		return err
	}
	for what, list := range map[string][][]byte{"passports_seen": gs.PassportsSeen, "handle_moved_out": gs.HandleMovedOut, "caretaker_moved_out": gs.CaretakerMovedOut} {
		seen := map[string]bool{}
		for _, nf := range list {
			if what == "passports_seen" {
				// Passport nullifiers, as registrations carry them.
				if len(nf) == 0 || len(nf) > 32 {
					return fmt.Errorf("%s: a nullifier is 1..32 bytes", what)
				}
			} else if _, err := privacy.FieldFromBytes(nf); err != nil {
				return fmt.Errorf("%s: %w", what, err)
			}
			if seen[string(nf)] {
				return fmt.Errorf("%s: %x listed twice", what, nf)
			}
			seen[string(nf)] = true
		}
	}
	if gs.HandleLeaseMax < 0 {
		return fmt.Errorf("handle_lease_max is negative")
	}

	seenUsed := map[string]struct{}{}
	for _, u := range gs.UsedBindings {
		if _, err := privacy.FieldFromBytes(u.Binding); err != nil {
			return fmt.Errorf("used binding: %w", err)
		}
		if _, dup := seenUsed[string(u.Binding)]; dup {
			return fmt.Errorf("used binding %x listed twice", u.Binding)
		}
		seenUsed[string(u.Binding)] = struct{}{}
		if u.ExpiresAt <= 0 {
			return fmt.Errorf("used binding %x has no expiry", u.Binding)
		}
	}

	seenPurge := map[string]struct{}{}
	for _, dsc := range gs.PendingDscPurges {
		if _, err := privacy.FieldFromBytes(dsc); err != nil {
			return fmt.Errorf("pending dsc purge: %w", err)
		}
		if _, dup := seenPurge[string(dsc)]; dup {
			return fmt.Errorf("pending dsc purge %x listed twice", dsc)
		}
		seenPurge[string(dsc)] = struct{}{}
	}
	seenDscRate := map[string]struct{}{}
	for _, r := range gs.DscRates {
		if len(r.DscKey) == 0 {
			return fmt.Errorf("dsc rate counter without a dsc key")
		}
		if _, dup := seenDscRate[string(r.DscKey)]; dup {
			return fmt.Errorf("dsc rate counter %x listed twice", r.DscKey)
		}
		seenDscRate[string(r.DscKey)] = struct{}{}
	}
	seenCountryRate := map[string]struct{}{}
	for _, r := range gs.CountryRates {
		if r.Country == "" {
			return fmt.Errorf("country rate counter without a country")
		}
		if _, dup := seenCountryRate[r.Country]; dup {
			return fmt.Errorf("country rate counter %s listed twice", r.Country)
		}
		seenCountryRate[r.Country] = struct{}{}
	}
	if h := gs.LeaseHold; h.Seconds < 0 || h.Until < 0 || h.Seconds > 365*SecondsPerDay || (h.Seconds == 0) != (h.Until == 0) {
		return fmt.Errorf("lease_hold %+v: seconds and until must both be set (seconds at most a year) or both zero", h)
	}

	if gs.LastBuyback < 0 {
		return fmt.Errorf("last_buyback must not be negative")
	}

	return gs.Params.Validate()
}

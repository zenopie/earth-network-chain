package types

import (
	"encoding/hex"
	"fmt"
)

// DefaultGenesis returns the default genesis state: an empty chamber.
//
// There is nothing to seed. The electoral roll lives in x/personhood and the
// proposals live in x/gov; this module holds only votes in flight, and a fresh
// chain has none.
func DefaultGenesis() *GenesisState {
	return &GenesisState{}
}

// Validate checks the genesis state is self-consistent.
func (gs GenesisState) Validate() error {
	seenProposalVote := map[string]bool{}
	for _, v := range gs.ProposalVotes {
		if err := validateOption(v.Option); err != nil {
			return fmt.Errorf("proposal %d vote: %w", v.ProposalId, err)
		}
		if len(v.Nullifier) == 0 {
			return fmt.Errorf("proposal %d: vote with an empty nullifier", v.ProposalId)
		}
		// One registration, one vote. An import carrying the same nullifier
		// twice on one proposal would be a person voting twice, and rebuilding
		// the tally from it would count them twice.
		key := fmt.Sprintf("%d/%s", v.ProposalId, hex.EncodeToString(v.Nullifier))
		if seenProposalVote[key] {
			return fmt.Errorf("proposal %d: duplicate vote from nullifier %s",
				v.ProposalId, hex.EncodeToString(v.Nullifier))
		}
		seenProposalVote[key] = true
	}

	seenBallot := map[uint64]bool{}
	seenID := map[uint64]bool{}
	for _, entry := range gs.RemovalBallots {
		if seenBallot[entry.Ballot.OptionId] {
			return fmt.Errorf("option %d: more than one open removal ballot", entry.Ballot.OptionId)
		}
		seenBallot[entry.Ballot.OptionId] = true

		if entry.Ballot.BallotId == 0 || entry.Ballot.BallotId > gs.BallotSeq {
			return fmt.Errorf("option %d: removal ballot id %d is not one ballot_seq %d handed out",
				entry.Ballot.OptionId, entry.Ballot.BallotId, gs.BallotSeq)
		}
		if seenID[entry.Ballot.BallotId] {
			return fmt.Errorf("ballot id %d used twice", entry.Ballot.BallotId)
		}
		seenID[entry.Ballot.BallotId] = true
		if entry.Ballot.ClosesAt <= 0 {
			return fmt.Errorf("option %d: removal ballot has no closing time", entry.Ballot.OptionId)
		}
		seenVote := map[string]bool{}
		for _, v := range entry.Votes {
			if err := validateOption(v.Option); err != nil {
				return fmt.Errorf("option %d removal vote: %w", entry.Ballot.OptionId, err)
			}
			if len(v.Nullifier) == 0 {
				return fmt.Errorf("option %d: removal vote with an empty nullifier", entry.Ballot.OptionId)
			}
			if seenVote[hex.EncodeToString(v.Nullifier)] {
				return fmt.Errorf("option %d: duplicate removal vote from nullifier %s",
					entry.Ballot.OptionId, hex.EncodeToString(v.Nullifier))
			}
			seenVote[hex.EncodeToString(v.Nullifier)] = true
		}
	}
	seenCooldown := map[uint64]bool{}
	for _, c := range gs.RemovalCooldowns {
		if seenCooldown[c.OptionId] {
			return fmt.Errorf("option %d: two removal cooldowns", c.OptionId)
		}
		seenCooldown[c.OptionId] = true
		if c.Until <= 0 {
			return fmt.Errorf("option %d: removal cooldown has no end", c.OptionId)
		}
	}
	seenRound := map[uint64]bool{}
	for _, r := range gs.ProposalRounds {
		if seenRound[r.ProposalId] {
			return fmt.Errorf("proposal %d: two rounds", r.ProposalId)
		}
		seenRound[r.ProposalId] = true
	}
	return nil
}

func validateOption(o VoteOption) error {
	if o != VOTE_OPTION_YES && o != VOTE_OPTION_NO {
		return fmt.Errorf("vote option %q is neither yes nor no", o)
	}
	return nil
}

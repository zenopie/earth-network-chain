package types

import (
	"fmt"

	"cosmossdk.io/math"
)

// DefaultGenesis returns the default genesis state.
//
// Streams is empty, which is what tells InitGenesis this is a fresh chain rather
// than an import: it then seeds both streams at zero and gives each its option
// #1. See keeper/genesis.go.
func DefaultGenesis() *GenesisState {
	return &GenesisState{
		Params:                 DefaultParams(),
		RegistrationRewardSeed: math.ZeroInt(),
	}
}

// Validate performs basic genesis state validation returning an error upon any
// failure.
//
// The arithmetic checks here are the same ones keeper/invariants.go enforces
// every block. Doing them at import means a malformed genesis is refused by
// `validate-genesis`, rather than starting a chain that halts on its own
// invariant at height 1 — which looks like a consensus failure and is much
// harder to read.
func (gs GenesisState) Validate() error {
	// An absent seed is no pre-funding, not an error. Every other Int in this
	// module reads nil as zero, and a genesis file written by hand should not
	// have to name a field it does not want.
	if !gs.RegistrationRewardSeed.IsNil() && gs.RegistrationRewardSeed.IsNegative() {
		return fmt.Errorf("registration_reward_seed must not be negative, got %s", gs.RegistrationRewardSeed)
	}
	// A seed only means anything on a fresh chain. On an import the option's
	// balance comes back with its stream, and honouring both would credit the
	// pool twice — once from the export and once from the seed.
	if len(gs.Streams) > 0 && !gs.RegistrationRewardSeed.IsNil() && gs.RegistrationRewardSeed.IsPositive() {
		return fmt.Errorf("registration_reward_seed is for a fresh chain; an import carries option balances in its streams")
	}

	seenStream := make(map[StreamId]struct{}, len(gs.Streams))

	for _, st := range gs.Streams {
		if err := ValidateStreamId(st.Stream); err != nil {
			return err
		}
		if _, dup := seenStream[st.Stream]; dup {
			return fmt.Errorf("stream %s appears twice", st.Stream)
		}
		seenStream[st.Stream] = struct{}{}

		if st.RewardIndex.IsNil() || st.RewardIndex.IsNegative() {
			return fmt.Errorf("stream %s: reward_index must not be negative", st.Stream)
		}
		if st.TotalWeight.IsNil() || st.TotalWeight.IsNegative() {
			return fmt.Errorf("stream %s: total_weight must not be negative", st.Stream)
		}
		if st.LastUpkeep < 0 {
			return fmt.Errorf("stream %s: last_upkeep must not be negative", st.Stream)
		}

		seenOption := make(map[uint64]struct{}, len(st.Options))
		summed := math.ZeroInt()
		for _, opt := range st.Options {
			if opt.Id == 0 {
				return fmt.Errorf("stream %s: option id 0 is not issued", st.Stream)
			}
			if opt.Id > st.OptionSeq {
				return fmt.Errorf("stream %s: option %d is beyond the id sequence %d",
					st.Stream, opt.Id, st.OptionSeq)
			}
			if _, dup := seenOption[opt.Id]; dup {
				return fmt.Errorf("stream %s: duplicated option id %d", st.Stream, opt.Id)
			}
			seenOption[opt.Id] = struct{}{}

			if err := ValidateDescription(opt.Description); err != nil {
				return fmt.Errorf("stream %s: option %d: %w", st.Stream, opt.Id, err)
			}
			if opt.Stream != st.Stream {
				return fmt.Errorf("stream %s: option %d claims stream %s",
					st.Stream, opt.Id, opt.Stream)
			}
			if opt.AmountAllocated.IsNil() || opt.AmountAllocated.IsNegative() {
				return fmt.Errorf("stream %s: option %d has a negative allocation",
					st.Stream, opt.Id)
			}
			if opt.Accumulated.IsNil() || opt.Accumulated.IsNegative() {
				return fmt.Errorf("stream %s: option %d has negative accrued rewards",
					st.Stream, opt.Id)
			}
			if opt.LastRewardIndex.IsNil() || opt.LastRewardIndex.GT(st.RewardIndex) {
				return fmt.Errorf("stream %s: option %d was last settled at index %s, "+
					"ahead of the stream's own %s — it would collect a negative reward",
					st.Stream, opt.Id, opt.LastRewardIndex, st.RewardIndex)
			}
			summed = summed.Add(opt.AmountAllocated)
		}

		// The load-bearing one. AdvanceIndex divides the emission by
		// total_weight and each option collects against its own
		// amount_allocated, so if the parts do not sum to the whole the stream
		// pays out something other than the 1 ERTH/sec it was given.
		if !summed.Equal(st.TotalWeight) {
			return fmt.Errorf("stream %s: total_weight is %s but the options allocate %s",
				st.Stream, st.TotalWeight, summed)
		}

		seenVoter := make(map[string]struct{}, len(st.Voters))
		for _, v := range st.Voters {
			if _, dup := seenVoter[v.Address]; dup {
				return fmt.Errorf("stream %s: duplicated voter %s", st.Stream, v.Address)
			}
			seenVoter[v.Address] = struct{}{}

			if v.Voter.Weight.IsNil() || v.Voter.Weight.IsNegative() {
				return fmt.Errorf("stream %s: voter %s has a negative weight", st.Stream, v.Address)
			}
			var pct uint64
			for _, w := range v.Voter.Percentages {
				if _, ok := seenOption[w.OptionId]; !ok {
					return fmt.Errorf("stream %s: voter %s allocates to option %d, which does not exist",
						st.Stream, v.Address, w.OptionId)
				}
				// Bound each share before it enters the uint64 sum: two shares near
				// 2^63 sum to exactly 100 modulo 2^64, slipping past the pct > 100
				// check below and later sign-flipping to a negative weight. Same
				// guard SetAllocations applies to a live vote.
				if w.Percent > 100 {
					return fmt.Errorf("stream %s: voter %s allocates %d%% to option %d, over 100",
						st.Stream, v.Address, w.Percent, w.OptionId)
				}
				pct += w.Percent
			}
			if len(v.Voter.Percentages) > 0 && pct > 100 {
				return fmt.Errorf("stream %s: voter %s allocates %d%%", st.Stream, v.Address, pct)
			}
		}
	}

	return gs.Params.Validate()
}

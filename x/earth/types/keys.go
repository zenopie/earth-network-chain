package types

import "cosmossdk.io/collections"

const (
	// ModuleName defines the module name
	ModuleName = "earth"

	// StoreKey defines the primary module store key
	StoreKey = ModuleName

	// GovModuleName duplicates the gov module's name to avoid a dependency with x/gov.
	// It should be synced with the gov module's name if it is ever changed.
	// See: https://github.com/cosmos/cosmos-sdk/blob/v0.52.0-beta.2/x/gov/types/keys.go#L9
	GovModuleName = "gov"
)

// The four pillars, and the whole of ERTH issuance.
//
// Each pillar emits a fixed 1 ERTH/sec, so the chain issues exactly 4 ERTH/sec
// forever. Because the rate is constant while the supply it adds to grows, the
// inflation *rate* decays on its own — roughly 5% in year one against the 2.52B
// genesis pool, falling toward 2.5% by year twenty. There is no schedule to
// maintain and no halving to get wrong.
//
// Two pillars are weighted by personhood and two by capital; two pay individuals
// and two pay collectively-chosen options:
//
//	individual         (human,   individual)  x/personhood  ANML buyback-and-burn
//	collective human   (human,   collective)  x/allocation  human stream, one-human-one-vote
//	investor           (capital, individual)  x/earth       staking rewards via x/distribution
//	collective capital (capital, collective)  x/allocation  capital stream, stake-weighted
//
// The rates live here rather than in each pillar so this file is the single
// answer to "how much ERTH exists and where does it go". The pillar modules
// import these; nothing here imports them.
const (
	// EmissionPerSecondPerPillar is one pillar's rate in uerth (1 ERTH/sec).
	EmissionPerSecondPerPillar = 1_000_000

	// Pillars is how many streams emit at that rate.
	Pillars = 4

	// TotalEmissionPerSecond is the chain's entire issuance rate, in uerth.
	TotalEmissionPerSecond = EmissionPerSecondPerPillar * Pillars
)

// ParamsKey is the prefix to retrieve all Params
var ParamsKey = collections.NewPrefix("p_earth")

// Tokenomics state. This module owns the creation and destruction of ERTH: the
// investor pillar's emission into the fee collector, and the burning of gas
// fees. What happens to the emission after that — the power-weighted split,
// commission, the community tax, unclaimed reward accounting — is x/distribution
// working exactly as it does on any other chain, and needs no state here.
var (
	// LastMintTimeKey is the block time (unix nanoseconds) of the previous
	// emission, used to prorate the fixed per-second rate across variable block
	// times.
	LastMintTimeKey = collections.NewPrefix("last_mint_time")
)

// BurnedKey prefixes the cumulative burn counters, keyed by (source, denom).
//
// The chain destroys supply in five places across four modules, and three of
// them run in EndBlock where nothing indexes them. Nobody can reconstruct the
// figure after the fact — x/bank tracks what supply remains, never what left —
// so it is counted as it happens or not at all. See keeper/burns.go.
var BurnedKey = collections.NewPrefix("burned")

// The mechanisms that destroy supply. These are the `source` keys under
// BurnedKey and the labels the explorer groups by, so renaming one silently
// starts a fresh counter and orphans the old total: treat them as state.
const (
	// SourceGasFees is the burned half of each block's gas, split in
	// keeper/fees.go. Any denom that can pay for gas can appear here.
	SourceGasFees = "gas_fees"

	// SourceSwapFee is the ERTH half of every dex swap fee, burned in
	// x/dex/keeper/msg_server_swap.go.
	SourceSwapFee = "swap_fee"

	// SourcePolRetire is protocol-owned liquidity being retired on its
	// straight-line schedule in x/dex/keeper/pol_burn.go. Burns the pool's
	// spoke token as well as ERTH, except where the spoke side is a bridged
	// asset the chain cannot recreate.
	SourcePolRetire = "pol_retire"

	// SourceAnmlBuyback is ANML bought with the individual pillar's emission
	// and destroyed, in x/personhood/keeper/abci.go.
	SourceAnmlBuyback = "anml_buyback"

	// SourceAllocation is ERTH an allocation option earned but nobody claimed,
	// plus the fee for opening one. Two call sites in x/allocation, counted
	// together because the distinction is internal bookkeeping rather than
	// something a reader of the total would ask about.
	SourceAllocation = "allocation"
)

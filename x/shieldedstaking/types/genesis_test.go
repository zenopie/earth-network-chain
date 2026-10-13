package types

import (
	"testing"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/bech32"
	"github.com/stretchr/testify/require"

	allocationtypes "github.com/earth-network/earth/x/allocation/types"
	"github.com/earth-network/earth/zk/privacy"
)

// Audit 5 L-ST2: a hand-built genesis with a nil amount is refused by
// Validate (InitGenesis validates first, so it cannot panic InitChain).
func TestGenesisValidation(t *testing.T) {
	val, err := bech32.ConvertAndEncode(sdk.GetConfig().GetBech32ValidatorAddrPrefix(), make([]byte, 20))
	require.NoError(t, err)

	gs := DefaultGenesis()
	gs.UnbondRecords = []UnbondRecord{{Validator: val, Epoch: 1}}
	require.Error(t, gs.Validate(), "nil amounts")

	// A Groundworks vote's derth fits a stake note (1..2^63-1); it has a
	// canonical tag (unique), a split, a vintage and no lease (v1.3.0).
	vote := func() GroundworksVote {
		return GroundworksVote{Id: 0, Validator: val, Derth: math.NewInt(5),
			Tag: privacy.FieldBytes(privacy.U64(1)), Weight: math.ZeroInt(),
			Splits: []allocationtypes.AllocationWeight{{OptionId: 1, Percent: 100}}, Vintage: 202610}
	}
	withVotes := func(vs ...GroundworksVote) *GenesisState {
		g := DefaultGenesis()
		g.NextPositionId = uint64(len(vs))
		g.Positions = vs
		return g
	}
	require.NoError(t, withVotes(vote()).Validate())
	for name, mutate := range map[string]func(v *GroundworksVote){
		"derth past a u64":     func(v *GroundworksVote) { v.Derth = math.NewIntFromUint64(^uint64(0)).AddRaw(1) },
		"derth past 2^63-1":    func(v *GroundworksVote) { v.Derth = math.NewIntFromUint64(1 << 63) },
		"nothing weighed":      func(v *GroundworksVote) { v.Derth = math.ZeroInt() },
		"zero tag":             func(v *GroundworksVote) { v.Tag = make([]byte, 32) },
		"short tag":            func(v *GroundworksVote) { v.Tag = v.Tag[1:] },
		"no split":             func(v *GroundworksVote) { v.Splits = nil },
		"a lease":              func(v *GroundworksVote) { v.SplitExpiresAt = 99 },
		"no vintage":           func(v *GroundworksVote) { v.Vintage = 0 },
		"not a vintage":        func(v *GroundworksVote) { v.Vintage = 202613 },
		"id past next_vote_id": func(v *GroundworksVote) { v.Id = 1 },
	} {
		v := vote()
		mutate(&v)
		require.Error(t, withVotes(v).Validate(), name)
	}
	edge := vote()
	edge.Derth = math.NewIntFromUint64(1<<63 - 1)
	require.NoError(t, withVotes(edge).Validate())
	twin := vote()
	twin.Id = 1
	require.ErrorContains(t, withVotes(vote(), twin).Validate(), "tag repeated")
}

// Stake vintages in genesis: a book's vintage supplies sum to its supply; a
// retired vintage holds a held payout (no pc) of exactly its remaining
// value; a payout with no pc is a retired vintage's.
func TestGenesisVintages(t *testing.T) {
	val, err := bech32.ConvertAndEncode(sdk.GetConfig().GetBech32ValidatorAddrPrefix(), make([]byte, 20))
	require.NoError(t, err)
	book := func(supply int64) ValidatorState {
		return ValidatorState{Validator: val, PendingDelegation: math.ZeroInt(), PendingUndelegation: math.ZeroInt(),
			EpochRate: math.LegacyOneDec(), DerthSupply: math.NewInt(supply), SupplyAtBlockStart: math.ZeroInt(), SlashDebt: math.ZeroInt()}
	}
	base := func() *GenesisState {
		g := DefaultGenesis()
		g.BaseVintage = 202610
		g.Validators = []ValidatorState{book(30)}
		g.VintageSupplies = []VintageSupply{{Validator: val, Vintage: 202610, Supply: math.NewInt(10)},
			{Validator: val, Vintage: 202611, Supply: math.NewInt(20)}}
		g.UnbondRecords = []UnbondRecord{{Validator: val, Epoch: 3, Requested: math.NewInt(7), Target: math.NewInt(7),
			Undelegated: math.ZeroInt(), Payout: math.ZeroInt(), Outstanding: math.NewInt(7), Paid: math.ZeroInt()}}
		g.UnbondPayouts = []UnbondPayout{{Id: 1, Validator: val, Epoch: 3, Value: math.NewInt(7)}}
		g.NextUnbondPayoutId = 2
		g.RetiredVintages = []RetiredVintage{{Validator: val, Vintage: 202509, Supply: math.NewInt(9), Value: math.NewInt(8),
			PayoutId: 1, Epoch: 3, RemainingSupply: math.NewInt(6), RemainingValue: math.NewInt(7)}}
		return g
	}
	require.NoError(t, base().Validate())
	for name, mutate := range map[string]func(g *GenesisState){
		"sum short":           func(g *GenesisState) { g.VintageSupplies[1].Supply = math.NewInt(19) },
		"repeated":            func(g *GenesisState) { g.VintageSupplies[1].Vintage = 202610 },
		"zero supply":         func(g *GenesisState) { g.VintageSupplies[0].Supply = math.ZeroInt() },
		"bad vintage":         func(g *GenesisState) { g.VintageSupplies[0].Vintage = 202600 },
		"bad base":            func(g *GenesisState) { g.BaseVintage = 13 },
		"no book":             func(g *GenesisState) { g.Validators = nil },
		"held value differs":  func(g *GenesisState) { g.RetiredVintages[0].RemainingValue = math.NewInt(6) },
		"no held payout":      func(g *GenesisState) { g.RetiredVintages[0].PayoutId = 5 },
		"nothing remaining":   func(g *GenesisState) { g.RetiredVintages[0].RemainingSupply = math.ZeroInt() },
		"remaining past":      func(g *GenesisState) { g.RetiredVintages[0].RemainingSupply = math.NewInt(10) },
		"unheld payout no pc": func(g *GenesisState) { g.RetiredVintages = nil },
		"held with pc":        func(g *GenesisState) { g.UnbondPayouts[0].Pc = privacy.FieldBytes(privacy.U64(1)) },
		"bad move vintage":    func(g *GenesisState) { g.Moves = []Move{{Vintage: 7}} },
	} {
		g := base()
		mutate(g)
		require.Error(t, g.Validate(), name)
	}
}

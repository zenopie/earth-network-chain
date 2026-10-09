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
	// canonical tag (unique), a split and a lease.
	vote := func() GroundworksVote {
		return GroundworksVote{Id: 0, Validator: val, Derth: math.NewInt(5),
			Tag: privacy.FieldBytes(privacy.U64(1)), Weight: math.ZeroInt(),
			Splits: []allocationtypes.AllocationWeight{{OptionId: 1, Percent: 100}}, SplitExpiresAt: 99}
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
		"no lease":             func(v *GroundworksVote) { v.SplitExpiresAt = 0 },
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

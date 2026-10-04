package types

import (
	"testing"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/bech32"
	"github.com/stretchr/testify/require"

	"github.com/earth-network/earth/zk/privacy"
)

// Audit 5 L-ST2: a hand-built genesis with a nil amount is refused by
// Validate (InitGenesis validates first, so it cannot panic InitChain), and a
// position's derth must fit a stake note (1..2^63-1).
func TestGenesisValidation(t *testing.T) {
	val, err := bech32.ConvertAndEncode(sdk.GetConfig().GetBech32ValidatorAddrPrefix(), make([]byte, 20))
	require.NoError(t, err)

	gs := DefaultGenesis()
	gs.UnbondRecords = []UnbondRecord{{Validator: val, Epoch: 1}}
	require.Error(t, gs.Validate(), "nil amounts")

	gs = DefaultGenesis()
	gs.NextPositionId = 1
	pos := Position{Id: 0, Validator: val, Derth: math.NewIntFromUint64(^uint64(0)).AddRaw(1),
		OwnerTag: privacy.FieldBytes(privacy.U64(1)), Weight: math.ZeroInt()}
	gs.Positions = []Position{pos}
	require.ErrorContains(t, gs.Validate(), "2^63-1")
	// Above 2^63-1 though within a u64: unlocking it could not mint the note.
	gs.Positions[0].Derth = math.NewIntFromUint64(1 << 63)
	require.ErrorContains(t, gs.Validate(), "2^63-1")
	gs.Positions[0].Derth = math.NewIntFromUint64(1<<63 - 1)
	require.NoError(t, gs.Validate())
	gs.Positions[0].Derth = math.NewInt(5)
	require.NoError(t, gs.Validate())
}

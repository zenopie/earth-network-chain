package app

import (
	"testing"

	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	"github.com/stretchr/testify/require"
)

// A simulated signed tx runs under DefaultSimulationGasLimit, not the 100M
// block limit wasmd falls back to (round-5 R5-E-2): a simulate needs no fee
// and no valid signature, and a contract can burn all the gas it is given.
func TestSimulateSignedTxCapped(t *testing.T) {
	e := initShieldedEnv(t)
	other := sdk.AccAddress(make([]byte, 20))
	// The simulate path takes the gas the client asks for as irrelevant: the
	// cap replaces the meter whatever the tx says.
	tx := e.signedTx(90_000_000, e.fee(1_000), banktypes.NewMsgSend(e.userAddr(), other, sdk.NewCoins(sdk.NewInt64Coin("uerth", 1))))
	gi, _, err := e.app.Simulate(tx)
	require.NoError(t, err)
	require.Equal(t, DefaultSimulationGasLimit, gi.GasWanted, "signed simulate runs under the default cap")
	require.Less(t, gi.GasUsed, DefaultSimulationGasLimit)
}

func TestSimulationGasLimitSetting(t *testing.T) {
	require.Equal(t, DefaultSimulationGasLimit, *SimulationGasLimit(wasmtypes.NodeConfig{}))
	three := uint64(3_000_000)
	require.Equal(t, three, *SimulationGasLimit(wasmtypes.NodeConfig{SimulationGasLimit: &three}))
	zero := uint64(0)
	require.Equal(t, DefaultSimulationGasLimit, *SimulationGasLimit(wasmtypes.NodeConfig{SimulationGasLimit: &zero}))
}

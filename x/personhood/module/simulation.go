package personhood

import (
	"math/rand"

	"github.com/cosmos/cosmos-sdk/types/module"
	simtypes "github.com/cosmos/cosmos-sdk/types/simulation"
	"github.com/cosmos/cosmos-sdk/x/simulation"

	personhoodsimulation "github.com/earth-network/earth/x/personhood/simulation"
	"github.com/earth-network/earth/x/personhood/types"
)

// GenerateGenesisState creates a randomized GenState of the module.
func (AppModule) GenerateGenesisState(simState *module.SimulationState) {
	accs := make([]string, len(simState.Accounts))
	for i, acc := range simState.Accounts {
		accs[i] = acc.Address.String()
	}
	personhoodGenesis := types.GenesisState{
		Params: types.DefaultParams(),
	}
	simState.GenState[types.ModuleName] = simState.Cdc.MustMarshalJSON(&personhoodGenesis)
}

// RegisterStoreDecoder registers a decoder.
func (am AppModule) RegisterStoreDecoder(_ simtypes.StoreDecoderRegistry) {}

// WeightedOperations returns the all the gov module operations with their respective weights.
func (am AppModule) WeightedOperations(simState module.SimulationState) []simtypes.WeightedOperation {
	operations := make([]simtypes.WeightedOperation, 0)
	const (
		opWeightMsgRegister          = "op_weight_msg_personhood"
		defaultWeightMsgRegister int = 100
	)

	var weightMsgRegister int
	simState.AppParams.GetOrGenerate(opWeightMsgRegister, &weightMsgRegister, nil,
		func(_ *rand.Rand) {
			weightMsgRegister = defaultWeightMsgRegister
		},
	)
	operations = append(operations, simulation.NewWeightedOperation(
		weightMsgRegister,
		personhoodsimulation.SimulateMsgRegister(am.authKeeper, am.bankKeeper, am.keeper, simState.TxConfig),
	))
	const (
		opWeightMsgClaimAnml          = "op_weight_msg_personhood"
		defaultWeightMsgClaimAnml int = 100
	)

	var weightMsgClaimAnml int
	simState.AppParams.GetOrGenerate(opWeightMsgClaimAnml, &weightMsgClaimAnml, nil,
		func(_ *rand.Rand) {
			weightMsgClaimAnml = defaultWeightMsgClaimAnml
		},
	)
	operations = append(operations, simulation.NewWeightedOperation(
		weightMsgClaimAnml,
		personhoodsimulation.SimulateMsgClaimAnml(am.authKeeper, am.bankKeeper, am.keeper, simState.TxConfig),
	))
	return operations
}

// ProposalMsgs returns msgs used for governance proposals for simulations.
func (am AppModule) ProposalMsgs(simState module.SimulationState) []simtypes.WeightedProposalMsg {
	return []simtypes.WeightedProposalMsg{}
}

package caretaker

import (
	autocliv1 "cosmossdk.io/api/cosmos/autocli/v1"

	"github.com/earth-network/earth/x/personhood/types"
)

// AutoCLIOptions implements the autocli.HasAutoCLIConfig interface.
func (am AppModule) AutoCLIOptions() *autocliv1.ModuleOptions {
	return &autocliv1.ModuleOptions{
		Query: &autocliv1.ServiceCommandDescriptor{
			Service: types.Query_serviceDesc.ServiceName,
			RpcCommandOptions: []*autocliv1.RpcCommandOptions{
				{
					RpcMethod: "Params",
					Use:       "params",
					Short:     "Shows the parameters of the module",
				},
				{
					RpcMethod: "DemocraticOptions",
					Use:       "democratic-options",
					Short:     "List democratic allocation options, reward index, total weight, registrations",
				},
				{
					RpcMethod:      "Registration",
					Use:            "registration [address]",
					Short:          "Show a wallet's registration status",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "address"}},
				},
				{
					RpcMethod:      "DemocraticVoter",
					Use:            "democratic-voter [address]",
					Short:          "Show a registered human's democratic split",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "address"}},
				},
				// this line is used by ignite scaffolding # autocli/query
			},
		},
		Tx: &autocliv1.ServiceCommandDescriptor{
			Service:              types.Msg_serviceDesc.ServiceName,
			EnhanceCustomCommand: true, // only required if you want to use the custom command
			RpcCommandOptions: []*autocliv1.RpcCommandOptions{
				{
					RpcMethod: "UpdateParams",
					Skip:      true, // skipped because authority gated
				},
				{
					RpcMethod: "Register",
					Use:       "register",
					Short:     "Register as a unique human (--proof snarkjs proof.json, --public-signals, --signature-algorithm; optional --affiliate)",
				},
				{
					RpcMethod: "ClaimAnml",
					Use:       "claim-anml",
					Short:     "Claim 1 ANML (once per day, registered humans only)",
				},
				{
					RpcMethod: "SetDemocraticAllocations",
					Use:       "set-democratic-allocations",
					Short:     "Set your one-human-one-vote split (--percentages JSON, must sum to 100)",
				},
				{
					RpcMethod:      "ClaimDemocraticAllocation",
					Use:            "claim-democratic-allocation [option-id]",
					Short:          "Claim an ADDRESS option's accrued ERTH to its recipient",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "option_id"}},
				},
				{
					RpcMethod: "AddIntegratedOption",
					Skip:      true, // authority (governance) gated
				},
				{
					RpcMethod:      "AddAddressOption",
					Use:            "add-address-option [recipient] [description]",
					Short:          "Add a claim-based ADDRESS democratic option (permissionless; burns a fee)",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "recipient"}, {ProtoField: "description"}},
					FlagOptions: map[string]*autocliv1.FlagOptions{
						"claimer": {Usage: "address allowed to trigger the claim (default: anyone; payout always goes to recipient)"},
					},
				},
				{
					RpcMethod: "ResetDemocraticAllocations",
					Skip:      true, // authority (governance) gated
				},
				// this line is used by ignite scaffolding # autocli/tx
			},
		},
	}
}

package deflation

import (
	autocliv1 "cosmossdk.io/api/cosmos/autocli/v1"

	"github.com/earth-network/earth/x/deflation/types"
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
					RpcMethod: "AllocationOptions",
					Use:       "allocation-options",
					Short:     "List all allocation options with the reward index and total weight",
				},
				{
					RpcMethod:      "AllocationOption",
					Use:            "allocation-option [id]",
					Short:          "Show a single allocation option",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "id"}},
				},
				{
					RpcMethod:      "Voter",
					Use:            "voter [address]",
					Short:          "Show a voter's allocation split",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "address"}},
				},
			},
		},
		Tx: &autocliv1.ServiceCommandDescriptor{
			Service:              types.Msg_serviceDesc.ServiceName,
			EnhanceCustomCommand: true,
			RpcCommandOptions: []*autocliv1.RpcCommandOptions{
				{
					RpcMethod: "UpdateParams",
					Skip:      true, // skipped because authority gated
				},
				{
					RpcMethod: "SetAllocations",
					Use:       "set-allocations",
					Short:     "Set your stake-weighted allocation split (--percentages JSON, must sum to 100)",
				},
				{
					RpcMethod:      "ClaimAllocation",
					Use:            "claim-allocation [option-id]",
					Short:          "Claim an ADDRESS allocation option's accrued ERTH to its recipient",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "option_id"}},
				},
				{
					RpcMethod: "AddIntegratedOption",
					Skip:      true, // authority (governance) gated
				},
				{
					RpcMethod:      "AddAddressOption",
					Use:            "add-address-option [recipient] [description]",
					Short:          "Add a claim-based ADDRESS allocation option (permissionless; burns a fee)",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "recipient"}, {ProtoField: "description"}},
					FlagOptions: map[string]*autocliv1.FlagOptions{
						"claimer": {Usage: "address allowed to trigger the claim (default: anyone; payout always goes to recipient)"},
					},
				},
			},
		},
	}
}

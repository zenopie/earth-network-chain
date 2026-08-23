package allocation

import (
	autocliv1 "cosmossdk.io/api/cosmos/autocli/v1"

	"github.com/earth-network/earth/x/allocation/types"
)

// streamUsage documents the stream argument once; every command takes it.
const streamUsage = "stream is `caretaker` (one-human-one-vote) or `groundworks` (stake-weighted)."

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
					RpcMethod: "Options",
					Use:       "options [stream]",
					Short:     "List a page of a stream's allocation options, with its reward index, total weight and epoch",
					Long: "List a stream's allocation options, a page at a time.\n\n" +
						"Anyone may add an option for a fee, so the list is paged: one request returns at most 100. " +
						"Use --page-key from the previous response to continue.\n\n" + streamUsage,
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "stream"}},
				},
				{
					RpcMethod:      "Option",
					Use:            "option [stream] [id]",
					Short:          "Show a single allocation option",
					Long:           "Show a single allocation option.\n\n" + streamUsage,
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "stream"}, {ProtoField: "id"}},
				},
				{
					RpcMethod:      "Voter",
					Use:            "voter [stream] [address]",
					Short:          "Show a voter's split within a stream",
					Long:           "Show a voter's split within a stream.\n\n" + streamUsage,
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "stream"}, {ProtoField: "address"}},
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
					RpcMethod:      "SetAllocations",
					Use:            "set-allocations [stream]",
					Short:          "Set your allocation split in a stream (--percentages JSON, must sum to 100)",
					Long:           "Set your allocation split in a stream.\n\n" + streamUsage,
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "stream"}},
				},
				{
					RpcMethod:      "ClaimAllocation",
					Use:            "claim-allocation [stream] [option-id]",
					Short:          "Claim an ADDRESS option's accrued ERTH to its recipient",
					Long:           "Claim an ADDRESS option's accrued ERTH to its recipient.\n\n" + streamUsage,
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "stream"}, {ProtoField: "option_id"}},
				},
				{
					RpcMethod: "AddIntegratedOption",
					Skip:      true, // authority (governance) gated
				},
				{
					RpcMethod:      "AddAddressOption",
					Use:            "add-address-option [stream] [recipient] [description]",
					Short:          "Add a claim-based ADDRESS option (permissionless; burns a fee)",
					Long:           "Add a claim-based ADDRESS option.\n\n" + streamUsage,
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "stream"}, {ProtoField: "recipient"}, {ProtoField: "description"}},
					FlagOptions: map[string]*autocliv1.FlagOptions{
						"claimer": {Usage: "address allowed to trigger the claim (default: anyone; payout always goes to recipient)"},
					},
				},
				{
					RpcMethod: "ResetAllocations",
					Skip:      true, // authority (governance) gated
				},
			},
		},
	}
}

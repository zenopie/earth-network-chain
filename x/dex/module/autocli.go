package dex

import (
	autocliv1 "cosmossdk.io/api/cosmos/autocli/v1"

	"github.com/earth-network/earth/x/dex/types"
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
					RpcMethod: "ListPool",
					Use:       "list-pool",
					Short:     "List all pool",
				},
				{
					RpcMethod:      "GetPool",
					Use:            "get-pool [id]",
					Short:          "Gets a pool",
					Alias:          []string{"show-pool"},
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "pool_id"}},
				},
				{
					RpcMethod: "PolBurns",
					Use:       "pol-burns",
					Short:     "Show how much protocol-owned liquidity is left to retire",
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
					RpcMethod:      "CreatePool",
					Use:            "create-pool [erth-amount] [token-amount]",
					Short:          "Create an ERTH<->token pool (one side must be ERTH)",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "amount_a"}, {ProtoField: "amount_b"}},
				},
				{
					RpcMethod:      "AddLiquidity",
					Use:            "add-liquidity [pool-id] [amount-a] [amount-b]",
					Short:          "Send a add-liquidity tx",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "pool_id"}, {ProtoField: "amount_a"}, {ProtoField: "amount_b"}},
				},
				{
					RpcMethod:      "RemoveLiquidity",
					Use:            "remove-liquidity [pool-id] [shares]",
					Short:          "Send a remove-liquidity tx",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "pool_id"}, {ProtoField: "shares"}},
				},
				{
					RpcMethod:      "Swap",
					Use:            "swap [token-in] [denom-out] [min-amount-out]",
					Short:          "Swap token_in for denom_out, routed through the ERTH hub",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "token_in"}, {ProtoField: "denom_out"}, {ProtoField: "min_amount_out"}},
				},
				// this line is used by ignite scaffolding # autocli/tx
			},
		},
	}
}

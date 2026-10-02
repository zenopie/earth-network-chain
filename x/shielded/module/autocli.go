package shielded

import (
	autocliv1 "cosmossdk.io/api/cosmos/autocli/v1"

	"github.com/earth-network/earth/x/shielded/types"
)

// AutoCLIOptions implements the autocli.HasAutoCLIConfig interface.
//
// Only queries and MsgShield have commands. MsgSend is unsigned and
// carries proofs: wallets build its raw tx bytes and broadcast them, which
// the CLI's sign-and-send flow cannot do.
func (am AppModule) AutoCLIOptions() *autocliv1.ModuleOptions {
	return &autocliv1.ModuleOptions{
		Query: &autocliv1.ServiceCommandDescriptor{
			Service: types.Query_serviceDesc.ServiceName,
			RpcCommandOptions: []*autocliv1.RpcCommandOptions{
				{RpcMethod: "Params", Use: "params", Short: "Show the shielded pool's parameters"},
				{RpcMethod: "Tree", Use: "tree", Short: "Show the note tree's size, current root and latest anchor"},
				{RpcMethod: "Roots", Use: "roots", Short: "List the anchors still in the window, newest first"},
				{
					RpcMethod:      "Root",
					Use:            "root [root-hex]",
					Short:          "Show whether a root is a valid anchor",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "root"}},
				},
				{
					RpcMethod:      "Nullifier",
					Use:            "nullifier [nullifier-hex]",
					Short:          "Show whether a nullifier is spent",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "nullifier"}},
				},
				{RpcMethod: "Assets", Use: "assets", Short: "List the denoms admitted to the pool"},
				{RpcMethod: "Turnstiles", Use: "turnstiles", Short: "Show what has entered and left the pool per denom"},
			},
		},
		Tx: &autocliv1.ServiceCommandDescriptor{
			Service: types.Msg_serviceDesc.ServiceName,
			RpcCommandOptions: []*autocliv1.RpcCommandOptions{
				{RpcMethod: "UpdateParams", Skip: true},
				{RpcMethod: "RegisterAsset", Skip: true},
				{RpcMethod: "Send", Skip: true},
				{
					RpcMethod: "Shield",
					Use:       "shield [amount] [pc-base64]",
					Short:     "Move coins into the shielded pool as a note to pc",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{
						{ProtoField: "amount"}, {ProtoField: "pc"},
					},
				},
			},
		},
	}
}

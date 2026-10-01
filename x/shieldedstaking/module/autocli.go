package shieldedstaking

import (
	autocliv1 "cosmossdk.io/api/cosmos/autocli/v1"

	"github.com/earth-network/earth/x/shieldedstaking/types"
)

// AutoCLIOptions implements the autocli.HasAutoCLIConfig interface. Queries
// only: every msg but UpdateParams is an unsigned private msg that wallets
// build as raw tx bytes.
func (am AppModule) AutoCLIOptions() *autocliv1.ModuleOptions {
	return &autocliv1.ModuleOptions{
		Query: &autocliv1.ServiceCommandDescriptor{
			Service: types.Query_serviceDesc.ServiceName,
			RpcCommandOptions: []*autocliv1.RpcCommandOptions{
				{RpcMethod: "Params", Use: "params", Short: "Show private staking's parameters"},
				{RpcMethod: "Epoch", Use: "epoch", Short: "Show the epoch in progress"},
				{
					RpcMethod: "Validator", Use: "validator [valoper]", Short: "Show a validator's rate, derth supply and queues",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "validator"}},
				},
				{
					RpcMethod: "UnbondRecord", Use: "unbond-record [valoper] [epoch]", Short: "Show the record behind unbond/<valoper>/<epoch>",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "validator"}, {ProtoField: "epoch"}},
				},
				{
					RpcMethod: "Position", Use: "position [id]", Short: "Show a Groundworks position",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "id"}},
				},
				{RpcMethod: "Positions", Use: "positions", Short: "List Groundworks positions"},
				{
					RpcMethod: "Snapshot", Use: "snapshot [proposal-id]", Short: "Show a proposal's stake-vote snapshot",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "proposal_id"}},
				},
			},
		},
		Tx: &autocliv1.ServiceCommandDescriptor{
			Service: types.Msg_serviceDesc.ServiceName,
			RpcCommandOptions: []*autocliv1.RpcCommandOptions{
				{RpcMethod: "UpdateParams", Skip: true},
				{RpcMethod: "Delegate", Skip: true},
				{RpcMethod: "Undelegate", Skip: true},
				{RpcMethod: "ClaimUnbonding", Skip: true},
				{RpcMethod: "StakeVote", Skip: true},
				{RpcMethod: "LockPosition", Skip: true},
				{RpcMethod: "UpdatePosition", Skip: true},
				{RpcMethod: "UnlockPosition", Skip: true},
				{RpcMethod: "PositionVote", Skip: true},
			},
		},
	}
}

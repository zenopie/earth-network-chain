package assembly

import (
	autocliv1 "cosmossdk.io/api/cosmos/autocli/v1"

	"github.com/earth-network/earth/x/assembly/types"
)

// AutoCLIOptions implements the autocli.HasAutoCLIConfig interface.
func (am AppModule) AutoCLIOptions() *autocliv1.ModuleOptions {
	return &autocliv1.ModuleOptions{
		Query: &autocliv1.ServiceCommandDescriptor{
			Service: types.Query_serviceDesc.ServiceName,
			RpcCommandOptions: []*autocliv1.RpcCommandOptions{
				{
					RpcMethod:      "ProposalTally",
					Use:            "proposal-tally [proposal-id]",
					Short:          "Show the human vote on a governance proposal",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "proposal_id"}},
				},
				{
					RpcMethod: "RemovalBallots",
					Use:       "removal-ballots",
					Short:     "List open ballots to remove a groundworks option",
				},
			},
		},
		Tx: &autocliv1.ServiceCommandDescriptor{
			Service: types.Msg_serviceDesc.ServiceName,
			RpcCommandOptions: []*autocliv1.RpcCommandOptions{
				// Unsigned private msgs carrying a membership proof and a fee
				// bundle; the CLI cannot build them. Wallets build the raw tx.
				{RpcMethod: "VoteProposal", Skip: true},
				{RpcMethod: "ProposeRemoval", Skip: true},
				{RpcMethod: "VoteRemoval", Skip: true},
			},
		},
	}
}

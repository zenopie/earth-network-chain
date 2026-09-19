package assembly

import (
	autocliv1 "cosmossdk.io/api/cosmos/autocli/v1"

	"github.com/earth-network/earth/x/assembly/types"
)

// AutoCLIOptions implements the autocli.HasAutoCLIConfig interface.
func (am AppModule) AutoCLIOptions() *autocliv1.ModuleOptions {
	const voteUsage = "Vote is `yes` or `no`. There is no abstain: approval is two " +
		"thirds of the votes cast, so abstaining and not voting are the same thing.\n\n" +
		"Voting requires a live proof-of-personhood registration. Weight is the " +
		"person, not the wallet — one registration is one vote however much or " +
		"little it holds."

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
				{
					RpcMethod:      "VoteProposal",
					Use:            "vote-proposal [proposal-id] [yes|no]",
					Short:          "Vote as a human on a governance proposal",
					Long:           "Vote as a human on a governance proposal.\n\n" + voteUsage,
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "proposal_id"}, {ProtoField: "option"}},
				},
				{
					RpcMethod: "ProposeRemoval",
					Use:       "propose-removal [option-id]",
					Short:     "Open a ballot to remove a groundworks allocation option",
					Long: "Open a ballot to remove a groundworks allocation option.\n\n" +
						"No deposit, and stake gets no say. One ballot per option at a time.",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "option_id"}},
				},
				{
					RpcMethod:      "VoteRemoval",
					Use:            "vote-removal [option-id] [yes|no]",
					Short:          "Vote on an open removal ballot",
					Long:           "Vote on an open removal ballot.\n\n" + voteUsage,
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "option_id"}, {ProtoField: "option"}},
				},
			},
		},
	}
}

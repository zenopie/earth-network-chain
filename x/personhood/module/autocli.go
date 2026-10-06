package personhood

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
					RpcMethod: "RegistrationCount",
					Use:       "registration-count",
					Short:     "Show how many humans are currently registered",
				},
				{
					RpcMethod:      "Registration",
					Use:            "registration [passport-nullifier-hex]",
					Short:          "Show the registration filed under a passport nullifier",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "nullifier"}},
				},
				{
					RpcMethod: "IdentityTree",
					Use:       "identity-tree",
					Short:     "Show the identity tree's size and latest root",
				},
				{
					RpcMethod: "IdentityLeaves",
					Use:       "identity-leaves",
					Short:     "List identity leaves (--start, --limit)",
				},
				{
					RpcMethod: "CaretakerVoterCount",
					Use:       "caretaker-voter-count",
					Short:     "Show how many caretaker splits currently count",
				},
				{
					RpcMethod: "LeaseBounds",
					Use:       "lease-bounds",
					Short:     "Effective lease lengths and predecessor bounds (handle claim, caretaker cast) at this block",
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
				// Every other msg is an unsigned private msg carrying proofs; the
				// CLI cannot build them. Wallets build the raw tx and broadcast it.
				{RpcMethod: "Register", Skip: true},
				{RpcMethod: "ClaimAnml", Skip: true},
				{RpcMethod: "SetCaretaker", Skip: true},
				{RpcMethod: "BindHandle", Skip: true},
				// this line is used by ignite scaffolding # autocli/tx
			},
		},
	}
}

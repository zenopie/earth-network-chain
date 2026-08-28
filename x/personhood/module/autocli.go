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
					Use:            "registration [address]",
					Short:          "Show a wallet's registration status",
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
					RpcMethod: "Unregister",
					Use:       "unregister",
					Short:     "Retire your own proof-of-personhood registration, freeing its nullifier",
				},
				// this line is used by ignite scaffolding # autocli/tx
			},
		},
	}
}

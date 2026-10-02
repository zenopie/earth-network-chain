package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/server"
	genutiltypes "github.com/cosmos/cosmos-sdk/x/genutil/types"

	"github.com/earth-network/earth/app"
)

// withOperatorWithdrawCheck adds the cross-module operator withdraw-address
// check (app.ValidateOperatorWithdrawAddrs) to `genesis validate`, after the
// SDK's per-module validation passes.
func withOperatorWithdrawCheck(genesisCmd *cobra.Command) *cobra.Command {
	for _, c := range genesisCmd.Commands() {
		if c.Name() != "validate" || c.RunE == nil {
			continue
		}
		inner := c.RunE
		c.RunE = func(cmd *cobra.Command, args []string) error {
			if err := inner(cmd, args); err != nil {
				return err
			}
			file := server.GetServerContextFromCmd(cmd).Config.GenesisFile()
			if len(args) > 0 {
				file = args[0]
			}
			ag, err := genutiltypes.AppGenesisFromFile(file)
			if err != nil {
				return err
			}
			var state map[string]json.RawMessage
			if err := json.Unmarshal(ag.AppState, &state); err != nil {
				return err
			}
			clientCtx := client.GetClientContextFromCmd(cmd)
			if err := app.ValidateOperatorWithdrawAddrs(clientCtx.Codec, clientCtx.TxConfig.TxJSONDecoder(), state); err != nil {
				return fmt.Errorf("error validating genesis file %s: %w", file, err)
			}
			return nil
		}
	}
	return genesisCmd
}

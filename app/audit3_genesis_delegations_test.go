package app

import (
	"cosmossdk.io/math"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	"github.com/stretchr/testify/require"

	sstypes "github.com/earth-network/earth/x/shieldedstaking/types"
)

// AUDIT3 info: x/staking loads an exported genesis's delegations without
// running the hooks that enforce the delegation rule. x/shieldedstaking's
// InitGenesis now checks it: only the module and each operator's self-bond
// delegate (or unbond), and nobody redelegates.
func TestAudit3GenesisDelegationRule(t *testing.T) {
	foreign := sdk.AccAddress(secp256k1.GenPrivKeyFromSecret([]byte("audit3/foreign")).PubKey().Address())
	withStaking := func(edit func(st map[string]any, op sdk.AccAddress), notBonded int64) (err error) {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("%v", r)
			}
		}()
		_, err = initStakeEnvWith(t, func(appState map[string]json.RawMessage, op sdk.AccAddress) {
			var st map[string]any
			require.NoError(t, json.Unmarshal(appState["staking"], &st))
			st["exported"] = true // as an export: x/staking runs no hooks
			edit(st, op)
			bz, err := json.Marshal(st)
			require.NoError(t, err)
			appState["staking"] = bz
			if notBonded > 0 { // the unbonding entry's coins, in the not-bonded pool
				var bank map[string]any
				require.NoError(t, json.Unmarshal(appState["bank"], &bank))
				bank["balances"] = append(bank["balances"].([]any), map[string]any{
					"address": authtypes.NewModuleAddress(stakingtypes.NotBondedPoolName).String(),
					"coins":   []any{map[string]any{"denom": "uerth", "amount": fmt.Sprint(notBonded)}}})
				for _, c := range bank["supply"].([]any) {
					c := c.(map[string]any)
					if c["denom"] == "uerth" {
						cur, _ := math.NewIntFromString(c["amount"].(string))
						c["amount"] = cur.AddRaw(notBonded).String()
					}
				}
				appState["bank"], err = json.Marshal(bank)
				require.NoError(t, err)
			}
		})
		return err
	}
	err := withStaking(func(st map[string]any, op sdk.AccAddress) {
		st["delegations"] = []any{map[string]any{
			"delegator_address": foreign.String(), "validator_address": sdk.ValAddress(op).String(), "shares": "1.000000000000000000",
		}}
	}, 0)
	require.ErrorContains(t, err, "genesis delegation")

	err = withStaking(func(st map[string]any, op sdk.AccAddress) {
		st["unbonding_delegations"] = []any{map[string]any{
			"delegator_address": foreign.String(), "validator_address": sdk.ValAddress(op).String(),
			"entries": []any{map[string]any{"creation_height": "1", "completion_time": "2030-01-01T00:00:00Z",
				"initial_balance": "1", "balance": "1", "unbonding_id": "1"}},
		}}
	}, 1)
	require.ErrorContains(t, err, "genesis unbonding delegation")

	err = withStaking(func(st map[string]any, op sdk.AccAddress) {
		st["redelegations"] = []any{map[string]any{
			"delegator_address": op.String(), "validator_src_address": sdk.ValAddress(op).String(),
			"validator_dst_address": sdk.ValAddress(foreign).String(),
			"entries": []any{map[string]any{"creation_height": "1", "completion_time": "2030-01-01T00:00:00Z",
				"initial_balance": "1", "shares_dst": "1.000000000000000000", "unbonding_id": "2"}},
		}}
	}, 0)
	require.ErrorContains(t, err, "genesis redelegation")
	require.ErrorContains(t, err, sstypes.ErrTransparentStaking.Error()[:20])
}

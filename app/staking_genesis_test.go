package app

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"cosmossdk.io/log"
	"cosmossdk.io/math"
	dbm "github.com/cosmos/cosmos-db"
	"github.com/cosmos/cosmos-sdk/client/flags"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	simtestutil "github.com/cosmos/cosmos-sdk/testutil/sims"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	"github.com/stretchr/testify/require"

	sstypes "github.com/earth-network/earth/x/shieldedstaking/types"
	"github.com/earth-network/earth/zk/indexed"
	"github.com/earth-network/earth/zk/merkle"
	"github.com/earth-network/earth/zk/privacy"
)

// AUDIT3 info: x/staking loads an exported genesis's delegations without
// running the hooks that enforce the delegation rule. x/shieldedstaking's
// InitGenesis now checks it: only the module and each operator's self-bond
// delegate (or unbond), and nobody redelegates.
func TestGenesisDelegationRule(t *testing.T) {
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

	// Audit 6 C-I2: the module's delegation needs its validator's book.
	mod := authtypes.NewModuleAddress(sstypes.ModuleName)
	err = withStaking(func(st map[string]any, op sdk.AccAddress) {
		st["delegations"] = []any{map[string]any{
			"delegator_address": mod.String(), "validator_address": sdk.ValAddress(op).String(), "shares": "1.000000000000000000",
		}}
	}, 0)
	require.ErrorContains(t, err, "has no book")

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

// Audit 4 genesis PoCs (x/shieldedstaking), ported: each now asserts the
// forged state is refused.

// stakeGenesisWith sets x/shieldedstaking's stake tree (one commitment),
// stake roots and one snapshot (proposal 77) in its genesis.
func stakeGenesisWith(t *testing.T, roots [][]byte, snapRoot []byte, snapHeight string) func(map[string]json.RawMessage, sdk.AccAddress) {
	cm := privacy.FieldBytes(ssDet("a4/cm", 0))
	ts := fmt.Sprint(ssGenesisTime.Unix())
	b64 := base64.StdEncoding.EncodeToString
	return func(appState map[string]json.RawMessage, _ sdk.AccAddress) {
		var gs map[string]any
		require.NoError(t, json.Unmarshal(appState[sstypes.ModuleName], &gs))
		gs["stake_commitments"] = []any{b64(cm)}
		var rs []any
		for i, r := range roots {
			rec := map[string]any{"root": b64(r), "height": "1", "time": ts, "tree_size": "1"}
			if i < len(roots)-1 {
				rec["superseded_at"] = ts // every record but the latest was superseded
			}
			rs = append(rs, rec)
		}
		gs["stake_roots"] = rs
		gs["snapshot_seq"] = "1"
		gs["snapshots"] = []any{map[string]any{
			"proposal_id": "77", "root": b64(snapRoot), "tree_size": "1", "height": snapHeight,
			"voting_end": fmt.Sprint(ssGenesisTime.Add(30 * 24 * time.Hour).UnixNano()), "seq": "1",
			"nf_root": b64(privacy.FieldBytes(indexed.EmptyRoot)), "nf_size": "0",
		}}
		bz, err := json.Marshal(gs)
		require.NoError(t, err)
		appState[sstypes.ModuleName] = bz
	}
}

// A4-G1 / L-B: every stake_roots record and every snapshot's note root is
// checked against the rebuilt stake tree; I2: an open snapshot must be
// below the initial height.
func TestGenesisForgedStakeAnchorRefused(t *testing.T) {
	leaf, _ := privacy.FieldFromBytes(privacy.FieldBytes(ssDet("a4/cm", 0)))
	m := merkle.NewMem()
	_, err := m.Append(leaf)
	require.NoError(t, err)
	r, err := m.Root()
	require.NoError(t, err)
	real := privacy.FieldBytes(r)
	forged := privacy.FieldBytes(ssDet("a4/forged-root", 0))

	_, err = initStakeEnvRecover(t, stakeGenesisWith(t, [][]byte{forged, real}, real, "0"))
	require.ErrorContains(t, err, "is not the root of the first 1 stake commitments", "a forged earlier stake root")

	_, err = initStakeEnvRecover(t, stakeGenesisWith(t, [][]byte{real}, forged, "0"))
	require.ErrorContains(t, err, "is not the root of the first 1 stake commitments", "a forged snapshot root")

	_, err = initStakeEnvRecover(t, stakeGenesisWith(t, [][]byte{real}, real, "1"))
	require.ErrorContains(t, err, "is not below the initial height", "a snapshot at the initial height")

	e, err := initStakeEnvWith(t, stakeGenesisWith(t, [][]byte{real}, real, "0"))
	require.NoError(t, err, "the real roots, a snapshot below the initial height")
	snap, err := e.app.ShieldedStakingKeeper.Snapshots.Get(e.ctx(), 77)
	require.NoError(t, err)
	require.Equal(t, real, snap.Root)
}

// A4-G2: genesis may not point x/shieldedstaking's own module account's
// withdraw address anywhere else: InitGenesis refuses it, and so does
// `genesis validate` (ValidateOperatorWithdrawAddrs) for any module account.
func TestGenesisModuleWithdrawAddrRefused(t *testing.T) {
	thief := sdk.AccAddress(secp256k1.GenPrivKeyFromSecret([]byte("a4/thief")).PubKey().Address())
	mod := authtypes.NewModuleAddress(sstypes.ModuleName)
	var state map[string]json.RawMessage
	err := func() (err error) {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("%v", r)
			}
		}()
		_, err = initStakeEnvWith(t, func(appState map[string]json.RawMessage, _ sdk.AccAddress) {
			var d map[string]any
			require.NoError(t, json.Unmarshal(appState["distribution"], &d))
			d["delegator_withdraw_infos"] = []any{map[string]any{
				"delegator_address": mod.String(), "withdraw_address": thief.String(),
			}}
			bz, err := json.Marshal(d)
			require.NoError(t, err)
			appState["distribution"] = bz
			state = appState
		})
		return err
	}()
	require.ErrorContains(t, err, "module account's withdraw address")

	a := New(log.NewNopLogger(), dbm.NewMemDB(), nil, true, simtestutil.AppOptionsMap{flags.FlagHome: t.TempDir()})
	require.ErrorContains(t, ValidateOperatorWithdrawAddrs(a.AppCodec(), a.TxConfig().TxJSONDecoder(), state),
		"module account has withdraw address")
}

// initStakeEnvRecover is initStakeEnvWith with InitGenesis's panic (a module
// refusing its genesis) returned as an error.
func initStakeEnvRecover(t *testing.T, mutate func(map[string]json.RawMessage, sdk.AccAddress)) (e *stakeEnv, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%v", r)
		}
	}()
	return initStakeEnvWith(t, mutate)
}

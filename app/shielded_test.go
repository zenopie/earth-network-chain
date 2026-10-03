package app

// x/shielded on the real app: the pinned launch genesis, the real TxConfig,
// encoder, ante router, CheckTx and FinalizeBlock, and real UltraHonk action
// proofs (x/shielded/testdata/proofs, made by scripts/shielded-fixtures.sh
// for x/shielded/testutil's scenario of Orchard-style bundles).

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"cosmossdk.io/log"
	"cosmossdk.io/math"
	abci "github.com/cometbft/cometbft/abci/types"
	cmtjson "github.com/cometbft/cometbft/libs/json"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	cmttypes "github.com/cometbft/cometbft/types"
	dbm "github.com/cosmos/cosmos-db"
	"github.com/cosmos/cosmos-sdk/baseapp"
	"github.com/cosmos/cosmos-sdk/client/flags"
	clienttx "github.com/cosmos/cosmos-sdk/client/tx"
	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	simtestutil "github.com/cosmos/cosmos-sdk/testutil/sims"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	signingtypes "github.com/cosmos/cosmos-sdk/types/tx/signing"
	authsigning "github.com/cosmos/cosmos-sdk/x/auth/signing"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/cosmos/cosmos-sdk/x/authz"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	"github.com/stretchr/testify/require"

	shieldedkeeper "github.com/earth-network/earth/x/shielded/keeper"
	shieldedtest "github.com/earth-network/earth/x/shielded/testutil"
	shieldedtypes "github.com/earth-network/earth/x/shielded/types"
	"github.com/earth-network/earth/zk/privacy"
)

const (
	shValErth  = 200_000_000
	shUserErth = 1_000_000_000
	shUserAnml = 5_000_000
)

// shGas is the gas limit a scenario send declares: its fixed private charge
// (bundle_gas + 2.3M per action) plus room for the tx's size. The scenario's
// fees cover it at the node's 0.005uerth min gas price.
func shGas(actions int) uint64 {
	return shieldedtypes.DefaultBundleGas + uint64(actions)*(shieldedtypes.DefaultProofVerificationGas+2*shieldedtypes.DefaultNoteGas) + 200_000*uint64(actions)
}

type shieldedEnv struct {
	t      *testing.T
	app    *App
	height int64
	now    time.Time
	user   *secp256k1.PrivKey
	prover *shieldedtest.Prover
}

// initShieldedEnv boots the launch genesis re-keyed to the scenario's chain
// id, with the action verifying key set, a user funded in uerth and
// (straight into genesis balances) uanml, and a node min-gas-price of
// 0.005uerth (the SDL's MIN_GAS_PRICES).
func initShieldedEnv(t *testing.T) *shieldedEnv {
	t.Helper()
	return initShieldedEnvWith(t, shieldedEnvOpts{})
}

// shieldedEnvOpts varies the environment for other suites: a fixed genesis
// time and deterministic keys (so every tree and time is reproducible), and a
// hook to change the app state before InitChain.
type shieldedEnvOpts struct {
	genesisTime time.Time
	keySeed     string
	maxPrivate  uint32
	tweak       func(t *testing.T, app *App, appState map[string]json.RawMessage)
}

func initShieldedEnvWith(t *testing.T, opts shieldedEnvOpts) *shieldedEnv {
	t.Helper()
	raw, err := os.ReadFile("../networks/genesis.json")
	require.NoError(t, err)
	var doc struct {
		AppState  map[string]json.RawMessage `json:"app_state"`
		Consensus struct {
			Params json.RawMessage `json:"params"`
		} `json:"consensus"`
	}
	require.NoError(t, json.Unmarshal(raw, &doc))

	user, val := secp256k1.GenPrivKey(), secp256k1.GenPrivKey()
	if opts.keySeed != "" {
		user = secp256k1.GenPrivKeyFromSecret([]byte(opts.keySeed + "/user"))
		val = secp256k1.GenPrivKeyFromSecret([]byte(opts.keySeed + "/validator"))
	}
	userAddr := sdk.AccAddress(user.PubKey().Address()).String()
	app0 := New(log.NewNopLogger(), dbm.NewMemDB(), nil, true, simtestutil.AppOptionsMap{flags.FlagHome: t.TempDir()})
	// The launch gentx is signed for earth-1; this chain gets its own.
	valAddr := sdk.AccAddress(val.PubKey().Address()).String()
	doc.AppState["genutil"] = shieldedGentx(t, app0, val)

	var bank map[string]any
	require.NoError(t, json.Unmarshal(doc.AppState["bank"], &bank))
	bank["balances"] = append(bank["balances"].([]any), map[string]any{"address": userAddr, "coins": []any{
		map[string]any{"denom": "uanml", "amount": fmt.Sprint(shUserAnml)},
		map[string]any{"denom": "uerth", "amount": fmt.Sprint(shUserErth)},
	}}, map[string]any{"address": valAddr, "coins": []any{
		map[string]any{"denom": "uerth", "amount": fmt.Sprint(shValErth)},
	}})
	bumped := map[string]bool{}
	for _, c := range bank["supply"].([]any) {
		c := c.(map[string]any)
		cur, _ := math.NewIntFromString(c["amount"].(string))
		switch c["denom"] {
		case "uerth":
			c["amount"], bumped["uerth"] = cur.AddRaw(shUserErth+shValErth).String(), true
		case "uanml":
			c["amount"], bumped["uanml"] = cur.AddRaw(shUserAnml).String(), true
		}
	}
	if !bumped["uanml"] {
		bank["supply"] = append(bank["supply"].([]any), map[string]any{"denom": "uanml", "amount": fmt.Sprint(shUserAnml)})
	}
	require.True(t, bumped["uerth"])
	doc.AppState["bank"], err = json.Marshal(bank)
	require.NoError(t, err)

	var auth map[string]any
	require.NoError(t, json.Unmarshal(doc.AppState["auth"], &auth))
	accs := auth["accounts"].([]any)
	auth["accounts"] = append(accs,
		map[string]any{"@type": "/cosmos.auth.v1beta1.BaseAccount", "address": userAddr, "account_number": fmt.Sprint(len(accs) + 100), "sequence": "0"},
		map[string]any{"@type": "/cosmos.auth.v1beta1.BaseAccount", "address": valAddr, "account_number": fmt.Sprint(len(accs) + 101), "sequence": "0"})
	doc.AppState["auth"], err = json.Marshal(auth)
	require.NoError(t, err)

	vk, err := os.ReadFile("../x/shielded/testdata/action.vk")
	require.NoError(t, err)
	gs := shieldedtypes.DefaultGenesis()
	gs.Params.VerifyingKeys = map[string][]byte{shieldedtypes.CircuitAction: vk}
	if opts.maxPrivate > 0 {
		gs.Params.MaxPrivateActionsPerBlock = opts.maxPrivate
	}
	doc.AppState[shieldedtypes.ModuleName], err = app0.AppCodec().MarshalJSON(gs)
	require.NoError(t, err)
	if opts.tweak != nil {
		opts.tweak(t, app0, doc.AppState)
	}

	appState, err := json.Marshal(doc.AppState)
	require.NoError(t, err)
	app := New(log.NewNopLogger(), dbm.NewMemDB(), nil, true, simtestutil.AppOptionsMap{flags.FlagHome: t.TempDir()},
		baseapp.SetChainID(shieldedtest.ChainID), baseapp.SetMinGasPrices("0.005uerth"))
	var cpJSON cmttypes.ConsensusParams
	require.NoError(t, cmtjson.Unmarshal(doc.Consensus.Params, &cpJSON))
	cp := cpJSON.ToProto()
	now := time.Now().UTC().Truncate(time.Second)
	if !opts.genesisTime.IsZero() {
		now = opts.genesisTime
	}
	_, err = app.InitChain(&abci.RequestInitChain{
		ChainId: shieldedtest.ChainID, Time: now, InitialHeight: 1, ConsensusParams: &cp, AppStateBytes: appState,
	})
	require.NoError(t, err)
	e := &shieldedEnv{t: t, app: app, now: now, user: user, prover: shieldedtest.DefaultProver(t)}
	e.finalize()
	return e
}

// shieldedGentx is a MsgCreateValidator for val, signed for the scenario's
// chain id (genesis txs verify with account number 0).
func shieldedGentx(t *testing.T, app *App, val *secp256k1.PrivKey) json.RawMessage {
	t.Helper()
	valoper, err := app.StakingKeeper.ValidatorAddressCodec().BytesToString(val.PubKey().Address())
	require.NoError(t, err)
	msg, err := stakingtypes.NewMsgCreateValidator(valoper, ed25519.GenPrivKey().PubKey(),
		sdk.NewInt64Coin("uerth", 100_000_000), stakingtypes.NewDescription("shielded-test", "", "", "", ""),
		stakingtypes.NewCommissionRates(math.LegacyNewDecWithPrec(1, 1), math.LegacyNewDecWithPrec(2, 1), math.LegacyNewDecWithPrec(1, 2)),
		math.OneInt())
	require.NoError(t, err)
	cfg := app.TxConfig()
	b := cfg.NewTxBuilder()
	require.NoError(t, b.SetMsgs(msg))
	b.SetGasLimit(200_000)
	mode := signingtypes.SignMode_SIGN_MODE_DIRECT
	require.NoError(t, b.SetSignatures(signingtypes.SignatureV2{PubKey: val.PubKey(), Data: &signingtypes.SingleSignatureData{SignMode: mode}}))
	valAddr, err := app.AuthKeeper.AddressCodec().BytesToString(val.PubKey().Address())
	require.NoError(t, err)
	sig, err := clienttx.SignWithPrivKey(context.Background(), mode, authsigning.SignerData{
		Address: valAddr, ChainID: shieldedtest.ChainID, PubKey: val.PubKey(),
	}, b, val, cfg, 0)
	require.NoError(t, err)
	require.NoError(t, b.SetSignatures(sig))
	bz, err := cfg.TxJSONEncoder()(b.GetTx())
	require.NoError(t, err)
	out, err := json.Marshal(map[string]any{"gen_txs": []json.RawMessage{bz}})
	require.NoError(t, err)
	return out
}

func (e *shieldedEnv) finalize(txs ...[]byte) *abci.ResponseFinalizeBlock {
	return e.finalizeAfter(5*time.Second, txs...)
}

func (e *shieldedEnv) finalizeAfter(dt time.Duration, txs ...[]byte) *abci.ResponseFinalizeBlock {
	e.t.Helper()
	e.height++
	e.now = e.now.Add(dt)
	res, err := e.app.FinalizeBlock(&abci.RequestFinalizeBlock{Height: e.height, Time: e.now, Txs: txs})
	require.NoError(e.t, err)
	_, err = e.app.Commit()
	require.NoError(e.t, err)
	return res
}

func (e *shieldedEnv) ctx() sdk.Context {
	return e.app.BaseApp.NewUncachedContext(false, cmtproto.Header{ChainID: shieldedtest.ChainID, Height: e.height, Time: e.now})
}

func (e *shieldedEnv) checkTx(bz []byte) *abci.ResponseCheckTx {
	e.t.Helper()
	res, err := e.app.CheckTx(&abci.RequestCheckTx{Tx: bz, Type: abci.CheckTxType_New})
	require.NoError(e.t, err)
	return res
}

func (e *shieldedEnv) userAddr() sdk.AccAddress { return sdk.AccAddress(e.user.PubKey().Address()) }

func (e *shieldedEnv) bech(a sdk.AccAddress) string {
	s, err := e.app.AuthKeeper.AddressCodec().BytesToString(a)
	require.NoError(e.t, err)
	return s
}

// privateTx encodes an unsigned tx. The fee is the msg's unless fee is set.
func (e *shieldedEnv) privateTx(gas uint64, fee *sdk.Coins, msgs ...sdk.Msg) []byte {
	e.t.Helper()
	b := e.app.TxConfig().NewTxBuilder()
	require.NoError(e.t, b.SetMsgs(msgs...))
	b.SetGasLimit(gas)
	if fee != nil {
		b.SetFeeAmount(*fee)
	} else {
		pm := msgs[0].(shieldedtypes.PrivateMsg)
		b.SetFeeAmount(sdk.NewCoins(sdk.NewCoin("uerth", shieldedtypes.TotalFee(pm))))
	}
	bz, err := e.app.TxConfig().TxEncoder()(b.GetTx())
	require.NoError(e.t, err)
	return bz
}

// signedTx signs with the user key in SIGN_MODE_DIRECT.
func (e *shieldedEnv) signedTx(gas uint64, fee sdk.Coins, msgs ...sdk.Msg) []byte {
	e.t.Helper()
	cfg := e.app.TxConfig()
	b := cfg.NewTxBuilder()
	require.NoError(e.t, b.SetMsgs(msgs...))
	b.SetGasLimit(gas)
	b.SetFeeAmount(fee)
	acc := e.app.AuthKeeper.GetAccount(e.ctx(), e.userAddr())
	require.NotNil(e.t, acc)
	mode := signingtypes.SignMode_SIGN_MODE_DIRECT
	require.NoError(e.t, b.SetSignatures(signingtypes.SignatureV2{
		PubKey: e.user.PubKey(), Data: &signingtypes.SingleSignatureData{SignMode: mode}, Sequence: acc.GetSequence(),
	}))
	sig, err := clienttx.SignWithPrivKey(e.ctx(), mode, authsigning.SignerData{
		Address: e.bech(e.userAddr()), ChainID: shieldedtest.ChainID, AccountNumber: acc.GetAccountNumber(),
		Sequence: acc.GetSequence(), PubKey: e.user.PubKey(),
	}, b, e.user, cfg, acc.GetSequence())
	require.NoError(e.t, err)
	require.NoError(e.t, b.SetSignatures(sig))
	bz, err := cfg.TxEncoder()(b.GetTx())
	require.NoError(e.t, err)
	return bz
}

func (e *shieldedEnv) fee(amt int64) sdk.Coins { return sdk.NewCoins(sdk.NewInt64Coin("uerth", amt)) }

// sendMsg is the scenario's send i, proven (from the proof cache).
func (e *shieldedEnv) sendMsg(s shieldedtest.Scenario, i int) *shieldedtypes.MsgSend {
	e.t.Helper()
	m, err := s.Msg(i, e.bech(shieldedtest.Receiver), shieldedtypes.TxFields{GasLimit: shGas(len(s.Sends[i].Actions))}, func(toml string, pub [][]byte) []byte {
		return e.prover.Prove(e.t, toml, pub)
	})
	require.NoError(e.t, err)
	return m
}

// sendTx is send i as an unsigned tx with its declared gas.
func (e *shieldedEnv) sendTx(s shieldedtest.Scenario, i int) []byte {
	return e.privateTx(shGas(len(s.Sends[i].Actions)), nil, e.sendMsg(s, i))
}

func eventsOf(evs []abci.Event, typ string) []map[string]string {
	var out []map[string]string
	for _, ev := range evs {
		if ev.Type != typ {
			continue
		}
		m := map[string]string{}
		for _, a := range ev.Attributes {
			m[a.Key] = a.Value
		}
		out = append(out, m)
	}
	return out
}

func requireOK(t *testing.T, r *abci.ExecTxResult) {
	t.Helper()
	require.Equal(t, uint32(0), r.Code, r.Log)
}

// shieldAll shields the scenario's notes in one signed tx (positions 0..10).
func (e *shieldedEnv) shieldAll(s shieldedtest.Scenario) *abci.ResponseFinalizeBlock {
	e.t.Helper()
	var shields []sdk.Msg
	for _, n := range s.Shields {
		shields = append(shields, &shieldedtypes.MsgShield{
			Sender: e.bech(e.userAddr()), Amount: sdk.NewCoin(n.Denom, math.NewIntFromUint64(n.Value)), Pc: privacy.FieldBytes(n.PC()),
			Ciphertext: shieldedtest.BlindCT(fmt.Sprintf("shield/%d", len(shields))),
		})
	}
	fb := e.finalize(e.signedTx(4_000_000, e.fee(20_000), shields...))
	requireOK(e.t, fb.TxResults[0])
	return fb
}

// fixedGas is a private msg's fixed charge for bundles of these sizes.
func fixedGas(actions ...int) int64 {
	var g int64
	for _, n := range actions {
		g += int64(shieldedtypes.DefaultBundleGas) + int64(n)*int64(shieldedtypes.DefaultProofVerificationGas+2*shieldedtypes.DefaultNoteGas)
	}
	return g
}

func TestShieldedPoolEndToEnd(t *testing.T) {
	e := initShieldedEnv(t)
	k := e.app.ShieldedKeeper
	s := shieldedtest.Default()
	pool := authtypes.NewModuleAddress(shieldedtypes.ModuleName)
	feeCollector := authtypes.NewModuleAddress(authtypes.FeeCollectorName)
	erth := func(a sdk.AccAddress) int64 { return e.app.BankKeeper.GetBalance(e.ctx(), a, "uerth").Amount.Int64() }

	// The pool account is a module account and deliberately not blocked.
	require.False(t, e.app.BankKeeper.BlockedAddr(pool))

	// --- shield: the scenario's eleven notes (ERTH and ANML), one signed tx.
	fb := e.shieldAll(s)
	notes := eventsOf(fb.TxResults[0].Events, shieldedtypes.EventTypeNote)
	require.Len(t, notes, 11)
	require.Equal(t, "10", notes[10]["position"])
	require.Len(t, eventsOf(fb.Events, shieldedtypes.EventTypeRoot), 1, "EndBlock recorded the anchor")
	require.Equal(t, int64(1_116_000), erth(pool))

	// --- send2: a two-action bundle. CheckTx, then a block.
	good := e.sendTx(s, shieldedtest.Send2)
	res := e.checkTx(good)
	require.Equal(t, uint32(0), res.Code, res.Log)
	// Gas is the fixed private charge plus the tx's size, and little else.
	require.GreaterOrEqual(t, res.GasUsed, fixedGas(2)+sizeGas(e, good))
	require.Less(t, res.GasUsed, fixedGas(2)+sizeGas(e, good)+20_000, "pool writes run unmetered")
	// Same nullifiers again: CheckTx has already spent them in check state.
	again := e.privateTx(shGas(2)+1, nil, e.sendMsg(s, shieldedtest.Send2))
	res = e.checkTx(again)
	require.Equal(t, shieldedtypes.ErrNullifierSpent.ABCICode(), res.Code, res.Log)

	supplyBefore := e.app.BankKeeper.GetSupply(e.ctx(), "uerth").Amount
	fb = e.finalize(good, again)
	requireOK(t, fb.TxResults[0])
	require.Equal(t, shieldedtypes.ErrNullifierSpent.ABCICode(), fb.TxResults[1].Code, fb.TxResults[1].Log)
	r0 := fb.TxResults[0]
	notes = eventsOf(r0.Events, shieldedtypes.EventTypeNote)
	require.Len(t, notes, 2)
	for i, n := range notes {
		require.Equal(t, fmt.Sprint(11+i), n["position"])
		require.NotEmpty(t, n["ciphertext"])
	}
	require.Len(t, eventsOf(r0.Events, shieldedtypes.EventTypeNullifier), 2)
	// The fee left the pool into fee_collector, and x/earth burned half.
	require.Equal(t, int64(1_116_000-30_000), erth(pool))
	require.Equal(t, "15000uerth", burned(fb))
	require.True(t, supplyBefore.Sub(e.app.BankKeeper.GetSupply(e.ctx(), "uerth").Amount).GTE(math.NewInt(15_000)))
	require.Equal(t, int64(15_000), erth(feeCollector), "the other half waits for distribution")

	// CometBFT's recheck after the commit drops the included tx.
	rc, err := e.app.CheckTx(&abci.RequestCheckTx{Tx: good, Type: abci.CheckTxType_Recheck})
	require.NoError(t, err)
	require.Equal(t, shieldedtypes.ErrNullifierSpent.ABCICode(), rc.Code, rc.Log)

	// --- double spend in a later block: refused, and nothing moves.
	fb = e.finalize(e.privateTx(shGas(2)+2, nil, e.sendMsg(s, shieldedtest.Send2)))
	require.Equal(t, shieldedtypes.ErrNullifierSpent.ABCICode(), fb.TxResults[0].Code, fb.TxResults[0].Log)
	require.Equal(t, int64(1_116_000-30_000), erth(pool))

	// --- multi3: an ANML send paying its fee in ERTH, one bundle of three
	// actions, one of them mixed (spends ERTH, outputs ANML).
	tx := e.sendTx(s, shieldedtest.Multi3)
	res = e.checkTx(tx)
	require.Equal(t, uint32(0), res.Code, res.Log)
	require.GreaterOrEqual(t, res.GasUsed, fixedGas(3)+sizeGas(e, tx))
	require.Less(t, res.GasUsed, fixedGas(3)+sizeGas(e, tx)+20_000)
	fb = e.finalize(tx)
	requireOK(t, fb.TxResults[0])
	require.Equal(t, int64(1_116_000-70_000), erth(pool))
	require.Equal(t, int64(5_000_000), e.app.BankKeeper.GetBalance(e.ctx(), pool, "uanml").Amount.Int64(), "no ANML left the pool")

	// --- simulate unshield2 with placeholder proofs of the real length and
	// no binding signature check: same gas as the real tx.
	u := e.sendMsg(s, shieldedtest.Unshield2)
	sim := e.sendMsg(s, shieldedtest.Unshield2)
	for i := range sim.Bundle.Actions {
		sim.Bundle.Actions[i].Proof = make([]byte, len(u.Bundle.Actions[i].Proof))
	}
	gi, _, err := e.app.Simulate(e.privateTx(shGas(2), nil, sim))
	require.NoError(t, err)

	// --- unshield2: 500,000 to the receiver, the fee paid out of the same
	// uerth balance (no fee note).
	good = e.privateTx(shGas(2), nil, u)
	res = e.checkTx(good)
	require.Equal(t, uint32(0), res.Code, res.Log)
	fb = e.finalize(good)
	requireOK(t, fb.TxResults[0])
	require.Equal(t, gi.GasUsed, uint64(fb.TxResults[0].GasUsed), "simulate gas == DeliverTx gas")
	require.Equal(t, int64(500_000), erth(shieldedtest.Receiver))
	require.Len(t, eventsOf(fb.TxResults[0].Events, shieldedtypes.EventTypeUnshield), 1)
	require.Equal(t, int64(1_116_000-600_000), erth(pool))

	// --- consolidate: ten actions in one bundle.
	tx = e.sendTx(s, shieldedtest.Consolidate)
	res = e.checkTx(tx)
	require.Equal(t, uint32(0), res.Code, res.Log)
	require.GreaterOrEqual(t, res.GasUsed, fixedGas(10)+sizeGas(e, tx))
	require.Less(t, res.GasUsed, fixedGas(10)+sizeGas(e, tx)+20_000)
	fb = e.finalize(tx)
	requireOK(t, fb.TxResults[0])
	require.Len(t, eventsOf(fb.TxResults[0].Events, shieldedtypes.EventTypeNullifier), 10)

	ts, err := k.Turnstile(e.ctx(), "uerth")
	require.NoError(t, err)
	require.Equal(t, int64(1_116_000), ts.In.Int64())
	require.Equal(t, int64(730_000), ts.Out.Int64())
	require.Equal(t, int64(1_116_000-730_000), erth(pool))
	require.NoError(t, k.AssertInvariants(e.ctx()))

	// --- a fresh bundle spending send2's note again, and a one-action bundle.
	fb = e.finalize(e.sendTx(s, shieldedtest.DoubleSpend))
	require.Equal(t, shieldedtypes.ErrNullifierSpent.ABCICode(), fb.TxResults[0].Code, fb.TxResults[0].Log)
	fb = e.finalize(e.sendTx(s, shieldedtest.SingleAction))
	require.Equal(t, shieldedtypes.ErrInvalidBundle.ABCICode(), fb.TxResults[0].Code, fb.TxResults[0].Log)
	require.Contains(t, fb.TxResults[0].Log, "pad with dummies")

	// The chain's tree is the scenario's.
	want, err := s.TreeBefore(shieldedtest.DoubleSpend)
	require.NoError(t, err)
	wantRoot, err := want.Root()
	require.NoError(t, err)
	got, err := k.CurrentRoot(e.ctx())
	require.NoError(t, err)
	require.Equal(t, privacy.FieldBytes(wantRoot), got)

	// --- stale anchors: a newer anchor exists and two weeks pass; send2's
	// anchors are no longer accepted (checked before its spent nullifiers).
	pc := privacy.FieldBytes(shieldedtest.Det("late", 0))
	fb = e.finalize(e.signedTx(600_000, e.fee(3_000), &shieldedtypes.MsgShield{
		Sender: e.bech(e.userAddr()), Amount: sdk.NewInt64Coin("uerth", 7), Pc: pc, Ciphertext: shieldedtest.BlindCT("late")}))
	requireOK(t, fb.TxResults[0])
	fb = e.finalizeAfter(15*24*time.Hour, e.privateTx(shGas(2)+3, nil, e.sendMsg(s, shieldedtest.Send2)))
	require.Equal(t, shieldedtypes.ErrUnknownRoot.ABCICode(), fb.TxResults[0].Code, fb.TxResults[0].Log)

	// --- genesis round trip through a full app export.
	exported, err := e.app.ExportAppStateAndValidators(false, nil, nil)
	require.NoError(t, err)
	var appState map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(exported.AppState, &appState))
	var gs shieldedtypes.GenesisState
	require.NoError(t, e.app.AppCodec().UnmarshalJSON(appState[shieldedtypes.ModuleName], &gs))
	require.NoError(t, gs.Validate())
	require.Len(t, gs.Commitments, 29)
	require.Len(t, gs.Nullifiers, 17)
	fresh := New(log.NewNopLogger(), dbm.NewMemDB(), nil, true, simtestutil.AppOptionsMap{flags.FlagHome: t.TempDir()},
		baseapp.SetChainID(shieldedtest.ChainID))
	fctx := fresh.NewUncachedContext(false, cmtproto.Header{ChainID: shieldedtest.ChainID, Height: e.height, Time: e.now})
	_, err = fresh.ModuleManager.InitGenesis(fctx, fresh.AppCodec(), appState)
	require.NoError(t, err)
	gs2, err := fresh.ShieldedKeeper.ExportGenesis(fctx)
	require.NoError(t, err)
	require.Equal(t, gs, *gs2)
	r1, _ := k.CurrentRoot(e.ctx())
	r2, _ := fresh.ShieldedKeeper.CurrentRoot(fctx)
	require.Equal(t, r1, r2)
	require.NoError(t, fresh.ShieldedKeeper.AssertInvariants(fctx))
	// The spent set came across.
	for _, nf := range e.sendMsg(s, shieldedtest.Consolidate).Bundle.Nullifiers() {
		spent, err := fresh.ShieldedKeeper.Nullifiers.Has(fctx, nf)
		require.NoError(t, err)
		require.True(t, spent)
	}
}

// sizeGas is what ConsumeGasForTxSize charged for bz.
func sizeGas(e *shieldedEnv, bz []byte) int64 {
	params := e.app.AuthKeeper.GetParams(e.ctx())
	return int64(params.TxSizeCostPerByte) * int64(len(bz))
}

func burned(res *abci.ResponseFinalizeBlock) string {
	for _, m := range eventsOf(res.Events, "gas_fees_split") {
		return m["burned"]
	}
	return ""
}

// The block cap counts actions: a tx whose actions would take the block over
// max_private_actions_per_block is refused before anything else is checked,
// and a block's actions under it are admitted; max_actions_per_bundle
// refuses a bundle above it outright.
func TestShieldedBlockCapCountsActions(t *testing.T) {
	e := initShieldedEnv(t)
	s := shieldedtest.Default()
	e.shieldAll(s)
	setParams := func(f func(p *shieldedtypes.Params)) {
		p, err := e.app.ShieldedKeeper.Params.Get(e.ctx())
		require.NoError(t, err)
		f(&p)
		require.NoError(t, e.app.ShieldedKeeper.Params.Set(e.ctx(), p))
	}
	send2 := e.sendTx(s, shieldedtest.Send2)
	dup := e.privateTx(shGas(2)+1, nil, e.sendMsg(s, shieldedtest.Send2))

	// Cap 3: the second two-action tx would make 4. (Below governance's own
	// floor of 2 x max_actions_per_bundle; set directly to isolate the cap.)
	setParams(func(p *shieldedtypes.Params) { p.MaxPrivateActionsPerBlock = 3 })
	fb := e.finalize(send2, dup)
	requireOK(t, fb.TxResults[0])
	require.Equal(t, shieldedtypes.ErrBlockCap.ABCICode(), fb.TxResults[1].Code, fb.TxResults[1].Log)

	// Cap 5: the same pair fits the cap; the duplicate then fails on its
	// spent nullifiers, not the cap.
	setParams(func(p *shieldedtypes.Params) { p.MaxPrivateActionsPerBlock = 5 })
	fb = e.finalize(e.sendTx(s, shieldedtest.Multi3), dup)
	requireOK(t, fb.TxResults[0])
	require.Equal(t, shieldedtypes.ErrNullifierSpent.ABCICode(), fb.TxResults[1].Code, fb.TxResults[1].Log)

	// max_actions_per_bundle 2: the ten-action consolidation is refused in
	// CheckTx and in a block.
	setParams(func(p *shieldedtypes.Params) { p.MaxActionsPerBundle = 2; p.MaxPrivateActionsPerBlock = 4 })
	requireOK(t, e.finalize(e.sendTx(s, shieldedtest.Unshield2)).TxResults[0])
	tx := e.sendTx(s, shieldedtest.Consolidate)
	res := e.checkTx(tx)
	require.Equal(t, shieldedtypes.ErrInvalidBundle.ABCICode(), res.Code, res.Log)
	require.Contains(t, res.Log, "max_actions_per_bundle")
	fb = e.finalize(tx)
	require.Equal(t, shieldedtypes.ErrInvalidBundle.ABCICode(), fb.TxResults[0].Code)
}

func TestShieldedPrivateTxShape(t *testing.T) {
	e := initShieldedEnv(t)
	s := shieldedtest.Default()
	e.shieldAll(s)
	m := e.sendMsg(s, shieldedtest.Send2)
	gas := shGas(2)

	// Declared fee differs from the msg's.
	lie := e.fee(25_000)
	res := e.checkTx(e.privateTx(gas, &lie, m))
	require.Contains(t, res.Log, "must equal the msg's fee")

	// Below the node's min gas price: refused in CheckTx (gas 10M x 0.005 =
	// 50,000 > 30,000). The node price is local; only min_fee is consensus.
	res = e.checkTx(e.privateTx(10_000_000, nil, m))
	require.Equal(t, sdkerrors.ErrInsufficientFee.ABCICode(), res.Code, res.Log)

	// Too little gas for the fixed private charge: out of gas in the ante,
	// before anything is spent.
	fb := e.finalize(e.privateTx(3_000_000, nil, m))
	require.Equal(t, sdkerrors.ErrOutOfGas.ABCICode(), fb.TxResults[0].Code, fb.TxResults[0].Log)
	spent, err := e.app.ShieldedKeeper.Nullifiers.Has(e.ctx(), m.Bundle.Actions[0].Nullifier)
	require.NoError(t, err)
	require.False(t, spent)

	// Two private msgs, or a private msg with a normal one: refused.
	send := banktypes.NewMsgSend(e.userAddr(), e.userAddr(), e.fee(1))
	res = e.checkTx(e.privateTx(gas, nil, m, e.sendMsg(s, shieldedtest.Multi3)))
	require.Contains(t, res.Log, "exactly one private msg")
	res = e.checkTx(e.privateTx(gas, nil, m, send))
	require.Contains(t, res.Log, "exactly one private msg")
	mixed := e.signedTx(gas, e.fee(40_000), m, send)
	require.Contains(t, e.checkTx(mixed).Log, "exactly one private msg")
	require.Contains(t, e.finalize(mixed).TxResults[0].Log, "exactly one private msg")

	// A private msg carrying a signature.
	res = e.checkTx(e.signedTx(gas, e.fee(30_000), m))
	require.Contains(t, res.Log, "must be unsigned")

	// Unordered private txs.
	ub := e.app.TxConfig().NewTxBuilder()
	require.NoError(t, ub.SetMsgs(m))
	ub.SetGasLimit(gas)
	ub.SetFeeAmount(e.fee(30_000))
	ub.SetUnordered(true)
	ub.SetTimeoutTimestamp(e.now.Add(time.Minute))
	ubz, err := e.app.TxConfig().TxEncoder()(ub.GetTx())
	require.NoError(t, err)
	require.Contains(t, e.checkTx(ubz).Log, "unordered")

	// A relay swapping a ciphertext: the binding signature fails in CheckTx
	// and in a block.
	bad := e.sendMsg(s, shieldedtest.Send2)
	bad.Bundle.Actions[0].Ciphertext = shieldedtest.NoteCT("swapped by a relay")
	res = e.checkTx(e.privateTx(gas, nil, bad))
	require.Equal(t, shieldedtypes.ErrInvalidBindingSig.ABCICode(), res.Code, res.Log)
	fb = e.finalize(e.privateTx(gas, nil, bad))
	require.Equal(t, shieldedtypes.ErrInvalidBindingSig.ABCICode(), fb.TxResults[0].Code)

	// A tampered proof: the action proof fails.
	tampered := e.sendMsg(s, shieldedtest.Send2)
	tampered.Bundle.Actions[1].Proof = append([]byte(nil), tampered.Bundle.Actions[1].Proof...)
	tampered.Bundle.Actions[1].Proof[100] ^= 1
	res = e.checkTx(e.privateTx(gas, nil, tampered))
	require.Equal(t, shieldedtypes.ErrInvalidProof.ABCICode(), res.Code, res.Log)

	// Padding: a one-action bundle is refused before anything else.
	pad := e.sendMsg(s, shieldedtest.Send2)
	pad.Bundle.Actions = pad.Bundle.Actions[:1]
	res = e.checkTx(e.privateTx(gas, nil, pad))
	require.Equal(t, shieldedtypes.ErrInvalidBundle.ABCICode(), res.Code, res.Log)

	// And the real one still goes through after all that.
	requireOK(t, e.finalize(e.privateTx(gas, nil, m)).TxResults[0])
}

// A zero-signer msg reaching its handler by any route other than the private
// ante — a contract's CosmosMsg::Any, an ICA host tx, authz — is refused.
func TestShieldedHandlerBypassRefused(t *testing.T) {
	e := initShieldedEnv(t)
	s := shieldedtest.Default()
	m := e.sendMsg(s, shieldedtest.Send2)

	// The router directly, as wasm's message dispatcher and the ICA host call it.
	h := e.app.MsgServiceRouter().Handler(m)
	require.NotNil(t, h)
	_, err := h(e.ctx(), m)
	require.ErrorIs(t, err, shieldedtypes.ErrUnauthorized)
	require.False(t, shieldedkeeper.AuthorizedNullifiers(e.ctx(), m.Bundle.Nullifiers()...))

	// authz: MsgExec wrapping a private msg is refused (zero signers).
	exec := authz.NewMsgExec(e.userAddr(), []sdk.Msg{m})
	fb := e.finalize(e.signedTx(400_000, e.fee(2_000), &exec))
	require.Contains(t, fb.TxResults[0].Log, "only one signer")
	spent, err := e.app.ShieldedKeeper.Nullifiers.Has(e.ctx(), m.Bundle.Actions[0].Nullifier)
	require.NoError(t, err)
	require.False(t, spent)
}

func TestShieldedSendRestriction(t *testing.T) {
	e := initShieldedEnv(t)
	other := sdk.AccAddress([]byte("some-other-account!!"))
	pool := authtypes.NewModuleAddress(shieldedtypes.ModuleName)

	// ANML cannot move between accounts...
	fb := e.finalize(e.signedTx(200_000, e.fee(1_000), banktypes.NewMsgSend(e.userAddr(), other, sdk.NewCoins(sdk.NewInt64Coin("uanml", 1)))))
	require.NotEqual(t, uint32(0), fb.TxResults[0].Code)
	require.Contains(t, fb.TxResults[0].Log, "exists only in the shielded pool")
	// ...nor into gov (a deposit) or through MultiSend.
	fb = e.finalize(e.signedTx(200_000, e.fee(1_000), banktypes.NewMsgMultiSend(
		banktypes.NewInput(e.userAddr(), sdk.NewCoins(sdk.NewInt64Coin("uanml", 2))),
		[]banktypes.Output{banktypes.NewOutput(other, sdk.NewCoins(sdk.NewInt64Coin("uanml", 2)))})))
	require.Contains(t, fb.TxResults[0].Log, "exists only in the shielded pool")

	// ...nor pay a fee.
	res := e.checkTx(e.signedTx(200_000, sdk.NewCoins(sdk.NewInt64Coin("uanml", 1_000)), banktypes.NewMsgSend(e.userAddr(), other, e.fee(1))))
	require.Contains(t, res.Log, "cannot pay fees")

	// Nothing reaches the pool by MsgSend.
	fb = e.finalize(e.signedTx(200_000, e.fee(1_000), banktypes.NewMsgSend(e.userAddr(), pool, e.fee(5))))
	require.NotEqual(t, uint32(0), fb.TxResults[0].Code)
	require.Contains(t, fb.TxResults[0].Log, "accepts coins only through MsgShield")

	// ANML shields, and is counted.
	pc := privacy.FieldBytes(shieldedtest.Det("anml", 0))
	fb = e.finalize(e.signedTx(600_000, e.fee(3_000), &shieldedtypes.MsgShield{
		Sender: e.bech(e.userAddr()), Amount: sdk.NewInt64Coin("uanml", 1_000_000), Pc: pc, Ciphertext: shieldedtest.BlindCT("anml")}))
	requireOK(t, fb.TxResults[0])
	ts, err := e.app.ShieldedKeeper.Turnstile(e.ctx(), "uanml")
	require.NoError(t, err)
	require.Equal(t, int64(1_000_000), ts.In.Int64())
	require.Equal(t, int64(1_000_000), e.app.BankKeeper.GetBalance(e.ctx(), pool, "uanml").Amount.Int64())
	require.NoError(t, e.app.ShieldedKeeper.AssertInvariants(e.ctx()))

	// Uerth still moves freely between accounts.
	requireOK(t, e.finalize(e.signedTx(200_000, e.fee(1_000), banktypes.NewMsgSend(e.userAddr(), other, e.fee(3)))).TxResults[0])
	require.True(t, strings.HasPrefix(e.bech(other), "earth1"))
}

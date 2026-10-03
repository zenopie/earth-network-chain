package app

// Test harness for x/shieldedstaking on the real app: the launch genesis
// re-keyed to a deterministic chain, real FinalizeBlock/Commit with every
// validator signing (so x/earth's emission reaches distribution and
// validators earn), ABCI misbehavior for slashing, and real UltraHonk proofs.
//
// Proofs. A private staking test's public inputs depend on what the chain
// computed before it (a derth note's amount depends on the rate, which depends
// on rewards), so they cannot be written down ahead of time the way
// x/shielded/testutil's scenario is. Instead the chain run is deterministic —
// fixed keys, genesis time and block times — and every proof (fee bundles'
// action proofs, stake proofs: stake_notes_test.go) is cached under
// x/shieldedstaking/testdata/proofs, keyed by circuit and public inputs.
// A test that needs a proof it has no file for fails, naming the fix:
//
//	scripts/staking-fixtures.sh [path-to-earth-network-mobile/circuits]
//
// which re-runs these tests with EARTH_CIRCUITS set: missing proofs are then
// proven with nargo + bb (against the committed verifying keys) and written.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"cosmossdk.io/log"
	"cosmossdk.io/math"
	abci "github.com/cometbft/cometbft/abci/types"
	cmtjson "github.com/cometbft/cometbft/libs/json"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	cmttypes "github.com/cometbft/cometbft/types"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	dbm "github.com/cosmos/cosmos-db"
	"github.com/cosmos/cosmos-sdk/baseapp"
	"github.com/cosmos/cosmos-sdk/client/flags"
	clienttx "github.com/cosmos/cosmos-sdk/client/tx"
	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	simtestutil "github.com/cosmos/cosmos-sdk/testutil/sims"
	sdk "github.com/cosmos/cosmos-sdk/types"
	signingtypes "github.com/cosmos/cosmos-sdk/types/tx/signing"
	authsigning "github.com/cosmos/cosmos-sdk/x/auth/signing"
	stakingkeeper "github.com/cosmos/cosmos-sdk/x/staking/keeper"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	"github.com/stretchr/testify/require"

	earthtypes "github.com/earth-network/earth/x/earth/types"
	shieldedtest "github.com/earth-network/earth/x/shielded/testutil"
	shieldedtypes "github.com/earth-network/earth/x/shielded/types"
	sstypes "github.com/earth-network/earth/x/shieldedstaking/types"
	"github.com/earth-network/earth/zk/merkle"
	"github.com/earth-network/earth/zk/orchard"
	"github.com/earth-network/earth/zk/privacy"
)

const (
	ssChainID = "earth-staking-test"
	ssErth    = int64(1_000_000)
	ssGas     = uint64(16_000_000)
	ssFee     = uint64(5_000)
)

var ssGenesisTime = time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)

func ssDet(label string, i uint64) fr.Element {
	return privacy.H(privacy.AssetID("staking-fixture/"+label), privacy.U64(i))
}

type stakeEnv struct {
	t      *testing.T
	app    *App
	height int64
	now    time.Time
	times  map[int64]time.Time
	user   *secp256k1.PrivKey
	val    *secp256k1.PrivKey
	w      *wallet
	sw     *stakeWallet
	extra  int
	// reserved are notes build must not pick as a fee note (one already
	// committed to another bundle of the msg being built).
	reserved []*wnote
	// proofDir is where this suite's proofs are cached.
	proofDir string
}

// initStakeEnv boots the launch genesis for ssChainID: one genesis validator
// (100 ERTH self-bond), a transparent user with 1,000,000 ERTH, the action
// verifying key, and genesis time fixed.
func initStakeEnv(t *testing.T) *stakeEnv {
	t.Helper()
	e, err := initStakeEnvWith(t, nil)
	require.NoError(t, err)
	return e
}

// initStakeEnvWith is initStakeEnv with mutate applied to the app state
// (given the genesis validator's operator account) before InitChain, whose
// error it returns.
func initStakeEnvWith(t *testing.T, mutate func(appState map[string]json.RawMessage, op sdk.AccAddress)) (*stakeEnv, error) {
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

	user := secp256k1.GenPrivKeyFromSecret([]byte("staking-test/user"))
	val := secp256k1.GenPrivKeyFromSecret([]byte("staking-test/validator"))
	const userErth, valErth = 1_000_000 * ssErth, 200 * ssErth
	app0 := New(log.NewNopLogger(), dbm.NewMemDB(), nil, true, simtestutil.AppOptionsMap{flags.FlagHome: t.TempDir()})
	doc.AppState["genutil"] = gentxFor(t, app0, val, ssChainID, "staking-test")

	var bank map[string]any
	require.NoError(t, json.Unmarshal(doc.AppState["bank"], &bank))
	userAddr := sdk.AccAddress(user.PubKey().Address()).String()
	valAddr := sdk.AccAddress(val.PubKey().Address()).String()
	bank["balances"] = append(bank["balances"].([]any),
		map[string]any{"address": userAddr, "coins": []any{map[string]any{"denom": "uerth", "amount": fmt.Sprint(userErth)}}},
		map[string]any{"address": valAddr, "coins": []any{map[string]any{"denom": "uerth", "amount": fmt.Sprint(valErth)}}})
	for _, c := range bank["supply"].([]any) {
		c := c.(map[string]any)
		if c["denom"] == "uerth" {
			cur, _ := math.NewIntFromString(c["amount"].(string))
			c["amount"] = cur.AddRaw(userErth + valErth).String()
		}
	}
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

	gs := shieldedtypes.DefaultGenesis()
	gs.Params.VerifyingKeys = map[string][]byte{
		shieldedtypes.CircuitAction: mustRead(t, "../x/shielded/testdata/action.vk"),
		shieldedtypes.CircuitStake:  mustRead(t, "../x/shieldedstaking/testdata/stake.vk"),
	}
	doc.AppState[shieldedtypes.ModuleName], err = app0.AppCodec().MarshalJSON(gs)
	require.NoError(t, err)

	if mutate != nil {
		mutate(doc.AppState, sdk.AccAddress(val.PubKey().Address()))
	}
	appState, err := json.Marshal(doc.AppState)
	require.NoError(t, err)
	app := New(log.NewNopLogger(), dbm.NewMemDB(), nil, true, simtestutil.AppOptionsMap{flags.FlagHome: t.TempDir()},
		baseapp.SetChainID(ssChainID))
	var cpJSON cmttypes.ConsensusParams
	require.NoError(t, cmtjson.Unmarshal(doc.Consensus.Params, &cpJSON))
	cp := cpJSON.ToProto()
	_, err = app.InitChain(&abci.RequestInitChain{
		ChainId: ssChainID, Time: ssGenesisTime, InitialHeight: 1, ConsensusParams: &cp, AppStateBytes: appState,
	})
	if err != nil {
		return nil, err
	}
	e := &stakeEnv{t: t, app: app, now: ssGenesisTime, times: map[int64]time.Time{}, user: user, val: val,
		w: &wallet{nk: ssDet("nk", 0)}, sw: &stakeWallet{}, proofDir: stakingProofs}
	e.next(5 * time.Second)
	return e, nil
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	bz, err := os.ReadFile(path)
	require.NoError(t, err)
	return bz
}

// gentxFor is a MsgCreateValidator (100 ERTH self-bond) signed for chainID.
func gentxFor(t *testing.T, app *App, val *secp256k1.PrivKey, chainID, moniker string) json.RawMessage {
	t.Helper()
	valoper, err := app.StakingKeeper.ValidatorAddressCodec().BytesToString(val.PubKey().Address())
	require.NoError(t, err)
	msg, err := stakingtypes.NewMsgCreateValidator(valoper, ed25519.GenPrivKeyFromSecret([]byte(moniker+"/cons")).PubKey(),
		sdk.NewInt64Coin("uerth", 100*ssErth), stakingtypes.NewDescription(moniker, "", "", "", ""),
		stakingtypes.NewCommissionRates(math.LegacyNewDecWithPrec(1, 1), math.LegacyNewDecWithPrec(2, 1), math.LegacyNewDecWithPrec(1, 2)),
		math.OneInt())
	require.NoError(t, err)
	cfg := app.TxConfig()
	b := cfg.NewTxBuilder()
	require.NoError(t, b.SetMsgs(msg))
	b.SetGasLimit(200_000)
	mode := signingtypes.SignMode_SIGN_MODE_DIRECT
	require.NoError(t, b.SetSignatures(signingtypes.SignatureV2{PubKey: val.PubKey(), Data: &signingtypes.SingleSignatureData{SignMode: mode}}))
	addr, err := app.AuthKeeper.AddressCodec().BytesToString(val.PubKey().Address())
	require.NoError(t, err)
	sig, err := clienttx.SignWithPrivKey(context.Background(), mode, authsigning.SignerData{
		Address: addr, ChainID: chainID, PubKey: val.PubKey(),
	}, b, val, cfg, 0)
	require.NoError(t, err)
	require.NoError(t, b.SetSignatures(sig))
	bz, err := cfg.TxJSONEncoder()(b.GetTx())
	require.NoError(t, err)
	out, err := json.Marshal(map[string]any{"gen_txs": []json.RawMessage{bz}})
	require.NoError(t, err)
	return out
}

// ctx reads (and, for setup, writes) the committed state as of the next block.
func (e *stakeEnv) ctx() sdk.Context {
	return e.app.BaseApp.NewUncachedContext(false, cmtproto.Header{ChainID: ssChainID, Height: e.height + 1, Time: e.now})
}

func (e *stakeEnv) lastCommit() abci.CommitInfo {
	ctx := e.ctx()
	vals, err := e.app.StakingKeeper.GetBondedValidatorsByPower(ctx)
	require.NoError(e.t, err)
	pr := e.app.StakingKeeper.PowerReduction(ctx)
	var votes []abci.VoteInfo
	for _, v := range vals {
		ca, err := v.GetConsAddr()
		require.NoError(e.t, err)
		votes = append(votes, abci.VoteInfo{
			Validator: abci.Validator{Address: ca, Power: v.ConsensusPower(pr)}, BlockIdFlag: cmtproto.BlockIDFlagCommit,
		})
	}
	return abci.CommitInfo{Votes: votes}
}

// block finalizes and commits a block dt after the last, every bonded
// validator having signed the previous one.
func (e *stakeEnv) block(dt time.Duration, mis []abci.Misbehavior, txs ...[]byte) *abci.ResponseFinalizeBlock {
	e.t.Helper()
	ci := e.lastCommit()
	e.height++
	e.now = e.now.Add(dt)
	e.times[e.height] = e.now
	res, err := e.app.FinalizeBlock(&abci.RequestFinalizeBlock{
		Height: e.height, Time: e.now, DecidedLastCommit: ci, Misbehavior: mis, Txs: txs,
	})
	require.NoError(e.t, err)
	_, err = e.app.Commit()
	require.NoError(e.t, err)
	e.w.scan(e)
	e.scanStake()
	return res
}

func (e *stakeEnv) next(dt time.Duration, txs ...[]byte) *abci.ResponseFinalizeBlock {
	return e.block(dt, nil, txs...)
}

// days runs n blocks a day apart: n epoch ends.
func (e *stakeEnv) days(n int) {
	for i := 0; i < n; i++ {
		e.next(24 * time.Hour)
	}
}

func (e *stakeEnv) bech(a sdk.AccAddress) string {
	s, err := e.app.AuthKeeper.AddressCodec().BytesToString(a)
	require.NoError(e.t, err)
	return s
}

func (e *stakeEnv) valoper(v sdk.ValAddress) string {
	s, err := e.app.StakingKeeper.ValidatorAddressCodec().BytesToString(v)
	require.NoError(e.t, err)
	return s
}

func (e *stakeEnv) userAddr() sdk.AccAddress { return sdk.AccAddress(e.user.PubKey().Address()) }

func (e *stakeEnv) genesisValidator() sdk.ValAddress { return sdk.ValAddress(e.val.PubKey().Address()) }

// createValidator bonds a new validator with self (uerth) through the staking
// msg server, as a signed MsgCreateValidator would.
func (e *stakeEnv) createValidator(self int64) (sdk.ValAddress, *secp256k1.PrivKey) {
	e.extra++
	key := secp256k1.GenPrivKeyFromSecret([]byte(fmt.Sprintf("staking-test/validator-%d", e.extra)))
	op := sdk.AccAddress(key.PubKey().Address())
	ctx := e.ctx()
	coins := sdk.NewCoins(sdk.NewInt64Coin("uerth", self+ssErth))
	require.NoError(e.t, e.app.BankKeeper.MintCoins(ctx, earthtypes.ModuleName, coins))
	require.NoError(e.t, e.app.BankKeeper.SendCoinsFromModuleToAccount(ctx, earthtypes.ModuleName, op, coins))
	val := sdk.ValAddress(op)
	msg, err := stakingtypes.NewMsgCreateValidator(e.valoper(val),
		ed25519.GenPrivKeyFromSecret([]byte(fmt.Sprintf("staking-test/cons-%d", e.extra))).PubKey(),
		sdk.NewInt64Coin("uerth", self), stakingtypes.Description{Moniker: fmt.Sprintf("v%d", e.extra)},
		stakingtypes.NewCommissionRates(math.LegacyNewDecWithPrec(1, 1), math.LegacyNewDecWithPrec(2, 1), math.LegacyNewDecWithPrec(1, 2)),
		math.OneInt())
	require.NoError(e.t, err)
	_, err = stakingkeeper.NewMsgServerImpl(e.app.StakingKeeper).CreateValidator(ctx, msg)
	require.NoError(e.t, err)
	return val, key
}

// signedTx signs msgs with key in SIGN_MODE_DIRECT.
func (e *stakeEnv) signedTx(key *secp256k1.PrivKey, gas uint64, fee int64, msgs ...sdk.Msg) []byte {
	e.t.Helper()
	cfg := e.app.TxConfig()
	b := cfg.NewTxBuilder()
	require.NoError(e.t, b.SetMsgs(msgs...))
	b.SetGasLimit(gas)
	b.SetFeeAmount(sdk.NewCoins(sdk.NewInt64Coin("uerth", fee)))
	addr := sdk.AccAddress(key.PubKey().Address())
	acc := e.app.AuthKeeper.GetAccount(e.ctx(), addr)
	require.NotNil(e.t, acc)
	mode := signingtypes.SignMode_SIGN_MODE_DIRECT
	require.NoError(e.t, b.SetSignatures(signingtypes.SignatureV2{
		PubKey: key.PubKey(), Data: &signingtypes.SingleSignatureData{SignMode: mode}, Sequence: acc.GetSequence(),
	}))
	sig, err := clienttx.SignWithPrivKey(e.ctx(), mode, authsigning.SignerData{
		Address: e.bech(addr), ChainID: ssChainID, AccountNumber: acc.GetAccountNumber(),
		Sequence: acc.GetSequence(), PubKey: key.PubKey(),
	}, b, key, cfg, acc.GetSequence())
	require.NoError(e.t, err)
	require.NoError(e.t, b.SetSignatures(sig))
	bz, err := cfg.TxEncoder()(b.GetTx())
	require.NoError(e.t, err)
	return bz
}

// ssTx is the tx every private msg here is proven for (privateTx's).
var ssTx = shieldedtypes.TxFields{GasLimit: ssGas}

// privateTx encodes an unsigned private tx whose declared fee is its msg's.
func (e *stakeEnv) privateTx(msg shieldedtypes.PrivateMsg) []byte {
	e.t.Helper()
	b := e.app.TxConfig().NewTxBuilder()
	require.NoError(e.t, b.SetMsgs(msg))
	b.SetGasLimit(ssGas)
	b.SetFeeAmount(sdk.NewCoins(sdk.NewCoin("uerth", shieldedtypes.TotalFee(msg))))
	bz, err := e.app.TxConfig().TxEncoder()(b.GetTx())
	require.NoError(e.t, err)
	return bz
}

func (e *stakeEnv) checkTx(bz []byte) *abci.ResponseCheckTx {
	e.t.Helper()
	res, err := e.app.CheckTx(&abci.RequestCheckTx{Tx: bz, Type: abci.CheckTxType_New})
	require.NoError(e.t, err)
	return res
}

// run delivers one tx in its own block (5 s after the last) and returns its
// result.
func (e *stakeEnv) run(bz []byte) *abci.ExecTxResult {
	e.t.Helper()
	return e.next(5*time.Second, bz).TxResults[0]
}

func (e *stakeEnv) invariants() {
	e.t.Helper()
	require.NoError(e.t, e.app.ShieldedStakingKeeper.AssertInvariants(e.ctx()))
	require.NoError(e.t, e.app.ShieldedKeeper.AssertInvariants(e.ctx()))
}

// ---- wallet ---------------------------------------------------------------

type wnote struct {
	denom    string
	value    uint64
	rho, rcm fr.Element
	pos      uint64
	known    bool // position found in the tree
	spent    bool
}

type wallet struct {
	nk      fr.Element
	seq     uint64
	notes   []*wnote
	scanned uint64
	leaves  []fr.Element
}

func (w *wallet) fresh(denom string, value uint64) *wnote {
	w.seq++
	return &wnote{denom: denom, value: value, rho: ssDet("rho", w.seq), rcm: ssDet("rcm", w.seq)}
}

func (w *wallet) pc(n *wnote) fr.Element { return privacy.PC(privacy.OwnerPK(w.nk), n.rho, n.rcm) }

func (w *wallet) cm(n *wnote) fr.Element {
	return privacy.CM(privacy.AssetID(n.denom), n.value, w.pc(n))
}

func (w *wallet) nf(n *wnote) fr.Element { return privacy.NF(w.nk, n.rho, uint32(n.pos)) }

// track adds notes the wallet expects to find in the tree.
func (w *wallet) track(ns ...*wnote) { w.notes = append(w.notes, ns...) }

// scan reads the leaves appended since the last scan and places tracked
// notes.
func (w *wallet) scan(e *stakeEnv) {
	ctx := e.ctx()
	size, err := e.app.ShieldedKeeper.Size(ctx)
	require.NoError(e.t, err)
	for ; w.scanned < size; w.scanned++ {
		bz, err := e.app.ShieldedKeeper.Commitment(ctx, w.scanned)
		require.NoError(e.t, err)
		leaf, err := privacy.FieldFromBytes(bz)
		require.NoError(e.t, err)
		w.leaves = append(w.leaves, leaf)
	}
	for _, n := range w.notes {
		if n.known || n.value == 0 {
			continue
		}
		cm := w.cm(n)
		for i, l := range w.leaves {
			if l == cm {
				n.pos, n.known = uint64(i), true
				break
			}
		}
	}
}

// tree is the note tree of the first size leaves.
func (w *wallet) tree(t *testing.T, size uint64) *merkle.Tree {
	tr := merkle.NewMem()
	for _, l := range w.leaves[:size] {
		_, err := tr.Append(l)
		require.NoError(t, err)
	}
	return tr
}

// unspent returns a known unspent note of denom with value >= min, not in
// avoid.
func (w *wallet) unspent(denom string, min uint64, avoid ...*wnote) *wnote {
	return w.unspentBefore(denom, min, ^uint64(0), avoid...)
}

// unspentBefore is unspent among the notes at positions below size.
func (w *wallet) unspentBefore(denom string, min, size uint64, avoid ...*wnote) *wnote {
next:
	for _, n := range w.notes {
		if !n.known || n.spent || n.denom != denom || n.value < min || n.value == 0 || n.pos >= size {
			continue
		}
		for _, a := range avoid {
			if a == n {
				continue next
			}
		}
		return n
	}
	return nil
}

func (w *wallet) balance(denom string) uint64 {
	var s uint64
	for _, n := range w.notes {
		if n.known && !n.spent && n.denom == denom {
			s += n.value
		}
	}
	return s
}

// shield moves amount of the user's uerth into one note (a signed tx).
func (e *stakeEnv) shield(amount uint64) *wnote {
	e.t.Helper()
	n := e.w.fresh("uerth", amount)
	res := e.run(e.signedTx(e.user, 600_000, 5_000, &shieldedtypes.MsgShield{
		Sender: e.bech(e.userAddr()), Amount: sdk.NewCoin("uerth", math.NewIntFromUint64(amount)), Pc: privacy.FieldBytes(e.w.pc(n)),
		Ciphertext: shieldedtest.BlindCT(fmt.Sprintf("shield/%d", len(e.w.leaves))),
	}))
	require.Equal(e.t, uint32(0), res.Code, res.Log)
	e.w.track(n)
	e.w.scan(e)
	require.True(e.t, n.known)
	return n
}

// spend describes one bundle: inputs (0..n) of asset denom, the value
// leaving the pool as denom (valueOut), the change back to the wallet, and
// an ERTH fee note paying fee (unless feeless).
type spend struct {
	denom    string
	inputs   []*wnote
	valueOut uint64
	fee      uint64
	// atSize proves every action (dummies too) against the root of the first
	// atSize leaves (a stake vote's snapshot root) instead of the current
	// one; every input must be among them.
	atSize uint64
	// feeless pays no fee: no fee action (a stake vote's vote bundle, whose
	// fee a second bundle pays; a msg paying its fee from its output).
	feeless bool
}

// pendingBundle is a bundle built but not yet proven.
type pendingBundle struct {
	plan *shieldedtest.Plan
	// b is the unproven bundle, for the msg to carry.
	b   shieldedtypes.Bundle
	fee uint64
	in  []*wnote
	out []*wnote
	// also are bundles spent in the same msg, settled with this one.
	also []*pendingBundle
}

// build lays out a bundle against the current tree (or the first atSize
// leaves): one action per input (the first returns the change), one
// spending an ERTH fee note and returning its change, padded with a dummy
// action to the two-action minimum.
func (e *stakeEnv) build(s spend) *pendingBundle {
	e.t.Helper()
	w := e.w
	if s.feeless {
		s.fee = 0
	} else if s.fee == 0 {
		s.fee = ssFee
	}
	size := uint64(len(w.leaves))
	if s.atSize > 0 {
		size = s.atSize
	}
	w.seq++
	p := &pendingBundle{fee: s.fee, plan: &shieldedtest.Plan{
		Seed: fmt.Sprintf("staking/%d", w.seq), Tree: w.tree(e.t, size), DummyNK: w.nk,
	}}
	ps := func(n *wnote) *shieldedtest.PlanSpend {
		return &shieldedtest.PlanSpend{NK: w.nk, Denom: n.denom, Value: n.value, Rho: n.rho, Rcm: n.rcm, Position: n.pos}
	}
	output := func(n *wnote) shieldedtest.PlanOutput {
		p.out = append(p.out, n)
		return shieldedtest.PlanOutput{Denom: n.denom, Value: n.value, PC: w.pc(n),
			Ciphertext: shieldedtest.NoteCT(fmt.Sprintf("ct:%s:%d", p.plan.Seed, len(p.out)))}
	}
	var inA uint64
	for _, n := range s.inputs {
		require.Less(e.t, n.pos, size, "input outside the anchor's tree")
		inA += n.value
	}
	require.GreaterOrEqual(e.t, inA, s.valueOut)
	for i, n := range s.inputs {
		v := uint64(0)
		if i == 0 {
			v = inA - s.valueOut
		}
		p.in = append(p.in, n)
		p.plan.Actions = append(p.plan.Actions, shieldedtest.PlanAction{Spend: ps(n), Out: output(w.fresh(s.denom, v))})
	}
	if !s.feeless {
		feeNote := w.unspentBefore("uerth", s.fee, size, append(append([]*wnote{}, s.inputs...), e.reserved...)...)
		require.NotNil(e.t, feeNote, "no ERTH note to pay the fee")
		p.in = append(p.in, feeNote)
		p.plan.Actions = append(p.plan.Actions, shieldedtest.PlanAction{Spend: ps(feeNote), Out: output(w.fresh("uerth", feeNote.value-s.fee))})
	}
	for len(p.plan.Actions) < shieldedtypes.MinActionsPerBundle {
		p.plan.Actions = append(p.plan.Actions, shieldedtest.PlanAction{Out: output(w.fresh(s.denom, 0))})
	}
	b, err := p.plan.Unproven()
	require.NoError(e.t, err)
	p.b = b
	return p
}

// leg is one note a multi-asset bundle spends, releasing valueOut of it and
// returning the change.
type leg struct {
	n        *wnote
	valueOut uint64
}

// buildLegs lays out one bundle spending each leg's note (one action each,
// the change back to the wallet), paying fee out of the first uerth leg's
// change, or out of an ERTH fee note when no leg is uerth.
func (e *stakeEnv) buildLegs(fee uint64, legs ...leg) *pendingBundle {
	e.t.Helper()
	w := e.w
	w.seq++
	p := &pendingBundle{fee: fee, plan: &shieldedtest.Plan{
		Seed: fmt.Sprintf("staking/%d", w.seq), Tree: w.tree(e.t, uint64(len(w.leaves))), DummyNK: w.nk,
	}}
	paid := false
	for _, l := range legs {
		require.GreaterOrEqual(e.t, l.n.value, l.valueOut)
		change := l.n.value - l.valueOut
		if !paid && l.n.denom == "uerth" {
			require.GreaterOrEqual(e.t, change, fee, "the uerth leg cannot pay the fee")
			change -= fee
			paid = true
		}
		out := w.fresh(l.n.denom, change)
		p.in, p.out = append(p.in, l.n), append(p.out, out)
		p.plan.Actions = append(p.plan.Actions, shieldedtest.PlanAction{
			Spend: &shieldedtest.PlanSpend{NK: w.nk, Denom: l.n.denom, Value: l.n.value, Rho: l.n.rho, Rcm: l.n.rcm, Position: l.n.pos},
			Out: shieldedtest.PlanOutput{Denom: out.denom, Value: out.value, PC: w.pc(out),
				Ciphertext: shieldedtest.NoteCT(fmt.Sprintf("ct:%s:%d", p.plan.Seed, len(p.out)))},
		})
	}
	if !paid {
		avoid := append([]*wnote{}, e.reserved...)
		for _, l := range legs {
			avoid = append(avoid, l.n)
		}
		feeNote := w.unspent("uerth", fee, avoid...)
		require.NotNil(e.t, feeNote, "no ERTH note to pay the fee")
		out := w.fresh("uerth", feeNote.value-fee)
		p.in, p.out = append(p.in, feeNote), append(p.out, out)
		p.plan.Actions = append(p.plan.Actions, shieldedtest.PlanAction{
			Spend: &shieldedtest.PlanSpend{NK: w.nk, Denom: "uerth", Value: feeNote.value, Rho: feeNote.rho, Rcm: feeNote.rcm, Position: feeNote.pos},
			Out:   shieldedtest.PlanOutput{Denom: "uerth", Value: out.value, PC: w.pc(out)},
		})
	}
	for len(p.plan.Actions) < shieldedtypes.MinActionsPerBundle {
		out := w.fresh("uerth", 0)
		p.out = append(p.out, out)
		p.plan.Actions = append(p.plan.Actions, shieldedtest.PlanAction{Out: shieldedtest.PlanOutput{Denom: "uerth", PC: w.pc(out)}})
	}
	b, err := p.plan.Unproven()
	require.NoError(e.t, err)
	p.b = b
	return p
}

// stubBundle is a well-formed, unproven two-action bundle with balances,
// its nullifiers derived from label.
func stubBundle(label string, balances ...shieldedtypes.ValueBalance) shieldedtypes.Bundle {
	b := stubFeeBundle(1)
	b.Balances = balances
	for i := range b.Actions {
		b.Actions[i].Nullifier = privacy.FieldBytes(ssDet("stub-nf/"+label, uint64(i)))
	}
	return b
}

// prove fills in the proofs and binding signatures of msg's bundles,
// ps[i] for msg.PrivateBundles()[i], under msg's sighash (computed here, so
// every other field of msg must be final).
func (e *stakeEnv) prove(msg shieldedtypes.PrivateMsg, ps ...*pendingBundle) {
	e.t.Helper()
	plans := make([]*shieldedtest.Plan, len(ps))
	for i, p := range ps {
		plans[i] = p.plan
	}
	script := "scripts/staking-fixtures.sh"
	if e.proofDir == dexProofs {
		script = "scripts/dex-fixtures.sh"
	}
	pr := shieldedtest.ForDir(e.t, e.proofDir, script)
	require.NoError(e.t, shieldedtest.ProveMsg(msg, ssChainID, ssTx, e.app.AuthKeeper.AddressCodec(), plans, pr.TryProve))
}

// unproven fills msg's bundles (and stake proof) with placeholder proofs and
// binding signatures: well formed, for a msg the chain must refuse before
// verifying anything.
func unproven(msg shieldedtypes.PrivateMsg) {
	for _, b := range msg.PrivateBundles() {
		for i := range b.Actions {
			b.Actions[i].Proof = make([]byte, 14656)
		}
		b.BindingSig = make([]byte, orchard.BindingSigSize)
	}
	if sm, ok := msg.(sstypes.StakeMsg); ok {
		sm.StakeProofOf().Proof = make([]byte, 14656)
	}
}

// settle marks a bundle executed: inputs spent, outputs tracked.
func (e *stakeEnv) settle(p *pendingBundle) {
	for _, q := range append([]*pendingBundle{p}, p.also...) {
		for _, n := range q.in {
			n.spent = true
		}
		e.w.track(q.out...)
	}
	e.w.scan(e)
}

// minted finds the note the chain minted to n's pc in res (value read from
// the shielded_mint event whose denom is n's) and tracks it.
func (e *stakeEnv) minted(res *abci.ExecTxResult, n *wnote) *wnote {
	e.t.Helper()
	for _, m := range eventsOf(res.Events, shieldedtypes.EventTypeMint) {
		c, err := sdk.ParseCoinNormalized(m["amount"])
		require.NoError(e.t, err)
		if c.Denom != n.denom {
			continue
		}
		n.value = c.Amount.Uint64()
		e.w.track(n)
		e.w.scan(e)
		require.True(e.t, n.known, "minted note not found in the tree")
		return n
	}
	e.t.Fatalf("no %s note minted: %s", n.denom, res.Log)
	return nil
}

// Proof caches, one per suite, each regenerated by its own script.
var (
	stakingProofs = filepath.Join("..", "x", "shieldedstaking", "testdata", "proofs") // scripts/staking-fixtures.sh
	dexProofs     = filepath.Join("..", "x", "dex", "testdata", "proofs")             // scripts/dex-fixtures.sh
)

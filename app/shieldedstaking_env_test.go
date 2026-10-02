package app

// Test harness for x/shieldedstaking on the real app: the launch genesis
// re-keyed to a deterministic chain, real FinalizeBlock/Commit with every
// validator signing (so x/earth's emission reaches distribution and
// validators earn), ABCI misbehavior for slashing, and real UltraHonk proofs.
//
// Proofs. A private staking test's public inputs depend on what the chain
// computed before it (a derth note's value depends on the rate, which depends
// on rewards), so they cannot be written down ahead of time the way
// x/shielded/testutil's scenario is. Instead the chain run is deterministic —
// fixed keys, genesis time and block times — and every proof is cached under
// x/shieldedstaking/testdata/proofs, keyed by circuit and public inputs.
// A test that needs a proof it has no file for fails, naming the fix:
//
//	scripts/staking-fixtures.sh [path-to-earth-network-mobile/circuits]
//
// which re-runs these tests with EARTH_CIRCUITS set: missing proofs are then
// proven with nargo + bb (against the committed verifying keys) and written.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
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
	shieldedtypes "github.com/earth-network/earth/x/shielded/types"
	"github.com/earth-network/earth/zk/merkle"
	"github.com/earth-network/earth/zk/privacy"
)

const (
	ssChainID = "earth-staking-test"
	ssErth    = int64(1_000_000)
	ssGas     = uint64(8_000_000)
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
	pr     *prover
	extra  int
	// reserved are notes build must not pick as a fee note (one already
	// committed to another transfer of the msg being built).
	reserved []*wnote
	// proofDir is where this suite's proofs are cached.
	proofDir string
}

// initStakeEnv boots the launch genesis for ssChainID: one genesis validator
// (100 ERTH self-bond), a transparent user with 1,000,000 ERTH, the transfer
// verifying key, and genesis time fixed.
func initStakeEnv(t *testing.T) *stakeEnv {
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
		// TODO(orchard-phase2): this suite's private msgs still carry legacy
		// transfers (refused); the action key keeps genesis valid.
		shieldedtypes.CircuitAction: mustRead(t, "../x/shielded/testdata/action.vk"),
	}
	doc.AppState[shieldedtypes.ModuleName], err = app0.AppCodec().MarshalJSON(gs)
	require.NoError(t, err)

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
	require.NoError(t, err)
	e := &stakeEnv{t: t, app: app, now: ssGenesisTime, times: map[int64]time.Time{}, user: user, val: val,
		w: &wallet{nk: ssDet("nk", 0)}, pr: sharedProver(t), proofDir: stakingProofs}
	e.next(5 * time.Second)
	return e
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

// privateTx encodes an unsigned private tx whose declared fee is its proof's.
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
	}))
	require.Equal(e.t, uint32(0), res.Code, res.Log)
	e.w.track(n)
	e.w.scan(e)
	require.True(e.t, n.known)
	return n
}

// spend describes one transfer: inputs (0-2) of asset denom, the value
// leaving the pool as denom (valueOut), and the change back to the wallet.
type spend struct {
	denom    string
	inputs   []*wnote
	valueOut uint64
	fee      uint64
	// atSize proves against the root of the first atSize leaves (a stake
	// vote's snapshot root) instead of the current one; every input must be
	// among them.
	atSize uint64
	// feeless pays no fee: slot 2 is a dummy (a stake vote's transfer, whose
	// fee a second transfer pays; a msg paying its fee from its output).
	feeless bool
}

// pendingTransfer is a transfer built but not yet proven.
type pendingTransfer struct {
	tr     shieldedtypes.Transfer
	in     [3]*wnote
	out    [3]*wnote
	root   fr.Element
	size   uint64
	denomA string
	// also are transfers spent in the same msg, settled with this one.
	also []*pendingTransfer
}

// build lays out a transfer against the current tree: the inputs, dummies,
// an ERTH fee note in slot 2 and change outputs.
func (e *stakeEnv) build(s spend) *pendingTransfer {
	e.t.Helper()
	w := e.w
	if s.feeless {
		s.fee = 0
	} else if s.fee == 0 {
		s.fee = ssFee
	}
	p := &pendingTransfer{denomA: s.denom, size: uint64(len(w.leaves))}
	if s.atSize > 0 {
		p.size = s.atSize
	}
	var inA uint64
	for i := 0; i < 2; i++ {
		if i < len(s.inputs) {
			p.in[i] = s.inputs[i]
			inA += s.inputs[i].value
		} else {
			p.in[i] = w.fresh(s.denom, 0) // dummy: position 0, fresh nullifier
		}
	}
	if s.feeless {
		p.in[2] = w.fresh("uerth", 0)
	} else {
		feeNote := w.unspentBefore("uerth", s.fee, p.size, append(append([]*wnote{}, s.inputs...), e.reserved...)...)
		require.NotNil(e.t, feeNote, "no ERTH note to pay the fee")
		p.in[2] = feeNote
	}
	require.GreaterOrEqual(e.t, inA, s.valueOut)
	p.out[0] = w.fresh(s.denom, inA-s.valueOut)
	p.out[1] = w.fresh(s.denom, 0)
	p.out[2] = w.fresh("uerth", p.in[2].value-s.fee)
	root, err := w.tree(e.t, p.size).Root()
	require.NoError(e.t, err)
	p.root = root
	p.tr = shieldedtypes.Transfer{Root: privacy.FieldBytes(root), Fee: s.fee, ValueOut: s.valueOut}
	if s.valueOut > 0 {
		p.tr.DenomOut = s.denom
	}
	for i := 0; i < 3; i++ {
		p.tr.Nullifiers = append(p.tr.Nullifiers, privacy.FieldBytes(w.nf(p.in[i])))
		p.tr.Commitments = append(p.tr.Commitments, privacy.FieldBytes(w.cm(p.out[i])))
		ct := []byte(fmt.Sprintf("ct:%d:%d", w.seq, i))
		p.tr.Ciphertexts = append(p.tr.Ciphertexts, ct)
	}
	return p
}

// prove fills in the transfer's proof for msg (whose Signal is computed
// here, so every other field of msg must be final).
func (e *stakeEnv) prove(p *pendingTransfer, msg shieldedtypes.PrivateMsg) {
	e.t.Helper()
	e.proveInto(p, msg, msg.(shieldedtypes.TransferMsg).PrivateTransfer()) // TODO(orchard-phase2): bundles
}

// proveInto is prove for one of msg's transfers, target (a msg spending
// several: every proof binds the one signal).
func (e *stakeEnv) proveInto(p *pendingTransfer, msg shieldedtypes.PrivateMsg, target *shieldedtypes.Transfer) {
	e.t.Helper()
	signal, err := msg.(shieldedtypes.TransferMsg).Signal(ssChainID, e.app.AuthKeeper.AddressCodec())
	require.NoError(e.t, err)
	var assetPub fr.Element
	if p.tr.ValueOut > 0 {
		assetPub = privacy.AssetID(p.denomA)
	}
	w := e.w
	tr := w.tree(e.t, p.size)
	q := func(x fr.Element) string { b := x.Bytes(); return fmt.Sprintf("\"0x%x\"", b[:]) }
	arr := func(xs []fr.Element) string {
		parts := make([]string, len(xs))
		for i, x := range xs {
			parts[i] = q(x)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	}
	var vals, pos, outVals, paths [3]string
	var rho, rcm, outPC, nf, cm [3]fr.Element
	for i, n := range p.in {
		vals[i] = fmt.Sprintf("\"%d\"", n.value)
		var sib [merkle.Depth]fr.Element
		if n.value > 0 {
			sib, err = tr.Path(n.pos)
			require.NoError(e.t, err)
			pos[i] = fmt.Sprintf("\"%d\"", n.pos)
		} else {
			pos[i] = "\"0\""
		}
		paths[i] = arr(sib[:])
		rho[i], rcm[i], nf[i] = n.rho, n.rcm, w.nf(n)
		outVals[i] = fmt.Sprintf("\"%d\"", p.out[i].value)
		outPC[i], cm[i] = w.pc(p.out[i]), w.cm(p.out[i])
	}
	var b strings.Builder
	fmt.Fprintf(&b, "asset = %s\nnk = %s\n", q(privacy.AssetID(p.denomA)), q(w.nk))
	fmt.Fprintf(&b, "in_value = [%s]\nin_rho = %s\nin_rcm = %s\nin_pos = [%s]\n", strings.Join(vals[:], ", "), arr(rho[:]), arr(rcm[:]), strings.Join(pos[:], ", "))
	fmt.Fprintf(&b, "in_path = [%s]\n", strings.Join(paths[:], ", "))
	fmt.Fprintf(&b, "out_value = [%s]\nout_pc = %s\n", strings.Join(outVals[:], ", "), arr(outPC[:]))
	fmt.Fprintf(&b, "root = %s\nnf = %s\ncm_out = %s\nfee = \"%d\"\nv_pub_out = \"%d\"\nasset_pub = %s\nsignal = %s\n",
		q(p.root), arr(nf[:]), arr(cm[:]), p.tr.Fee, p.tr.ValueOut, q(assetPub), q(signal))
	target.Proof = e.pr.prove(e.t, e.proofDir, shieldedtypes.CircuitTransfer, b.String(), p.tr.PublicInputs(assetPub, signal))
}

// settle marks a transfer executed: inputs spent, outputs tracked.
func (e *stakeEnv) settle(p *pendingTransfer) {
	for _, n := range p.in {
		n.spent = true
	}
	e.w.track(p.out[:]...)
	for _, q := range p.also {
		for _, n := range q.in {
			n.spent = true
		}
		e.w.track(q.out[:]...)
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

// ---- prover ---------------------------------------------------------------

type prover struct {
	mu  sync.Mutex
	dir string // compiled copy of the circuits, when proving
}

var (
	proverOnce sync.Once
	theProver  = &prover{}
)

func sharedProver(*testing.T) *prover { return theProver }

// Proof caches, one per suite, each regenerated by its own script.
var (
	stakingProofs = filepath.Join("..", "x", "shieldedstaking", "testdata", "proofs") // scripts/staking-fixtures.sh
	dexProofs     = filepath.Join("..", "x", "dex", "testdata", "proofs")             // scripts/dex-fixtures.sh
)

func proofFile(dir, circuit string, pub [][]byte) string {
	h := sha256.New()
	h.Write([]byte(circuit))
	for _, x := range pub {
		h.Write(x)
	}
	return filepath.Join(dir, fmt.Sprintf("%s-%x.proof", circuit, h.Sum(nil)[:10]))
}

var vkFiles = map[string]string{
	shieldedtypes.CircuitTransfer: "../x/shielded/testdata/transfer.vk",
}

// prove returns the cached proof for (circuit, pub), proving and caching it
// when EARTH_CIRCUITS points at the circuits.
func (p *prover) prove(t *testing.T, dir, circuit, toml string, pub [][]byte) []byte {
	t.Helper()
	file := proofFile(dir, circuit, pub)
	if bz, err := os.ReadFile(file); err == nil {
		return bz
	}
	src := os.Getenv("EARTH_CIRCUITS")
	if src == "" {
		script := "scripts/staking-fixtures.sh"
		if dir == dexProofs {
			script = "scripts/dex-fixtures.sh"
		}
		t.Fatalf("no proof fixture %s for this test's public inputs: run %s", file, script)
	}
	require.NoError(t, os.MkdirAll(dir, 0o755))
	p.mu.Lock()
	defer p.mu.Unlock()
	run := func(dir string, name string, args ...string) {
		cmd := exec.Command(name, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "PATH="+os.Getenv("HOME")+"/.nargo/bin:"+os.Getenv("HOME")+"/.bb:"+os.Getenv("PATH"))
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s %v: %s", name, args, out)
	}
	proverOnce.Do(func() {
		dir, err := os.MkdirTemp("", "staking-circuits")
		require.NoError(t, err)
		run(".", "cp", "-R", src, filepath.Join(dir, "circuits"))
		p.dir = filepath.Join(dir, "circuits")
		_ = os.RemoveAll(filepath.Join(p.dir, "target"))
		run(p.dir, "nargo", "compile", "--package", shieldedtypes.CircuitTransfer)
	})
	require.NotEmpty(t, p.dir, "circuit compile failed earlier")
	require.NoError(t, os.WriteFile(filepath.Join(p.dir, circuit, "Prover.toml"), []byte(toml), 0o644))
	run(p.dir, "nargo", "execute", "--package", circuit)
	vk, err := filepath.Abs(vkFiles[circuit])
	require.NoError(t, err)
	out, err := os.MkdirTemp("", "proof")
	require.NoError(t, err)
	defer os.RemoveAll(out)
	run(p.dir, "bb", "prove", "-b", "target/"+circuit+".json", "-w", "target/"+circuit+".gz", "-k", vk, "-o", out, "-t", "noir-recursive")
	gotPub, err := os.ReadFile(filepath.Join(out, "public_inputs"))
	require.NoError(t, err)
	require.True(t, bytes.Equal(gotPub, bytes.Join(pub, nil)), "%s: bb's public inputs differ from the chain's", circuit)
	proof, err := os.ReadFile(filepath.Join(out, "proof"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(file, proof, 0o644))
	return proof
}

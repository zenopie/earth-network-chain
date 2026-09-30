package app

// SPIKE (Phase 0, privacy redesign): unsigned private txs paid from a pool.
//
// Everything here goes through the real TxConfig, encoder, CheckTx and
// FinalizeBlock on the pinned earth-1 genesis, with the pool pre-funded.

import (
	"crypto/sha256"
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
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	simtestutil "github.com/cosmos/cosmos-sdk/testutil/sims"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	signingtypes "github.com/cosmos/cosmos-sdk/types/tx/signing"
	authsigning "github.com/cosmos/cosmos-sdk/x/auth/signing"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	"github.com/stretchr/testify/require"

	shieldedkeeper "github.com/earth-network/earth/x/shieldedspike/keeper"
	shieldedtypes "github.com/earth-network/earth/x/shieldedspike/types"
)

const (
	spikePool    = 1_000_000_000 // uerth pre-funded into the pool
	spikeUserBal = 1_000_000_000
)

type spikeEnv struct {
	t       *testing.T
	app     *App
	chainID string
	height  int64
	now     time.Time
	user    *secp256k1.PrivKey
}

// initSpikeGenesis boots the pinned genesis with the pool and one user funded,
// and a node min-gas-price of 0.005uerth (the SDL's MIN_GAS_PRICES).
func initSpikeGenesis(t *testing.T) *spikeEnv {
	t.Helper()
	raw, err := os.ReadFile("../networks/genesis.json")
	require.NoError(t, err)
	var doc struct {
		ChainID   string                     `json:"chain_id"`
		AppState  map[string]json.RawMessage `json:"app_state"`
		Consensus struct {
			Params json.RawMessage `json:"params"`
		} `json:"consensus"`
	}
	require.NoError(t, json.Unmarshal(raw, &doc))

	user := secp256k1.GenPrivKey()
	userAddr := sdk.AccAddress(user.PubKey().Address()).String()
	poolAddr := authtypes.NewModuleAddress(shieldedtypes.ModuleName).String()

	// bank: fund pool + user, bump supply.
	var bank map[string]any
	require.NoError(t, json.Unmarshal(doc.AppState["bank"], &bank))
	bals := bank["balances"].([]any)
	for _, a := range []struct {
		addr string
		amt  int64
	}{{poolAddr, spikePool}, {userAddr, spikeUserBal}} {
		bals = append(bals, map[string]any{"address": a.addr, "coins": []any{map[string]any{"denom": "uerth", "amount": fmt.Sprint(a.amt)}}})
	}
	bank["balances"] = bals
	for _, c := range bank["supply"].([]any) {
		c := c.(map[string]any)
		if c["denom"] == "uerth" {
			cur, _ := math.NewIntFromString(c["amount"].(string))
			c["amount"] = cur.AddRaw(spikePool + spikeUserBal).String()
		}
	}
	doc.AppState["bank"], err = json.Marshal(bank)
	require.NoError(t, err)

	// auth: the user's base account.
	var auth map[string]any
	require.NoError(t, json.Unmarshal(doc.AppState["auth"], &auth))
	accs := auth["accounts"].([]any)
	accs = append(accs, map[string]any{"@type": "/cosmos.auth.v1beta1.BaseAccount", "address": userAddr, "account_number": fmt.Sprint(len(accs)), "sequence": "0"})
	auth["accounts"] = accs
	doc.AppState["auth"], err = json.Marshal(auth)
	require.NoError(t, err)

	appState, err := json.Marshal(doc.AppState)
	require.NoError(t, err)

	opts := simtestutil.AppOptionsMap{flags.FlagHome: t.TempDir()}
	app := New(log.NewNopLogger(), dbm.NewMemDB(), nil, true, opts,
		baseapp.SetChainID(doc.ChainID), baseapp.SetMinGasPrices("0.005uerth"))

	var cpJSON cmttypes.ConsensusParams
	require.NoError(t, cmtjson.Unmarshal(doc.Consensus.Params, &cpJSON))
	cp := cpJSON.ToProto()
	now := time.Now().UTC()
	_, err = app.InitChain(&abci.RequestInitChain{
		ChainId: doc.ChainID, Time: now, InitialHeight: 1, ConsensusParams: &cp, AppStateBytes: appState,
	})
	require.NoError(t, err)
	env := &spikeEnv{t: t, app: app, chainID: doc.ChainID, height: 0, now: now, user: user}
	env.finalize() // height 1
	return env
}

func (e *spikeEnv) finalize(txs ...[]byte) *abci.ResponseFinalizeBlock {
	e.t.Helper()
	e.height++
	e.now = e.now.Add(5 * time.Second)
	res, err := e.app.FinalizeBlock(&abci.RequestFinalizeBlock{Height: e.height, Time: e.now, Txs: txs})
	require.NoError(e.t, err)
	_, err = e.app.Commit()
	require.NoError(e.t, err)
	return res
}

func (e *spikeEnv) ctx() sdk.Context {
	return e.app.BaseApp.NewUncachedContext(false, cmtproto.Header{ChainID: e.chainID, Height: e.height, Time: e.now})
}

func (e *spikeEnv) balance(addr sdk.AccAddress) math.Int {
	return e.app.BankKeeper.GetBalance(e.ctx(), addr, "uerth").Amount
}

func (e *spikeEnv) checkTx(bz []byte) *abci.ResponseCheckTx {
	e.t.Helper()
	res, err := e.app.CheckTx(&abci.RequestCheckTx{Tx: bz, Type: abci.CheckTxType_New})
	require.NoError(e.t, err)
	return res
}

func nullifier(s string) []byte { h := sha256.Sum256([]byte(s)); return h[:] }

func privMsg(nfSeed string, fee int64) *shieldedtypes.MsgPrivateNoop {
	nf := nullifier(nfSeed)
	f := fmt.Sprint(fee)
	return &shieldedtypes.MsgPrivateNoop{Proof: shieldedtypes.StubProof(nf, f), Fee: f, Nullifier: nf}
}

// buildTx builds with the app's real TxConfig. The tx fee is the sum of the
// private msgs' fees unless feeOverride is set.
func (e *spikeEnv) buildTx(gas uint64, feeOverride *sdk.Coins, msgs ...sdk.Msg) []byte {
	e.t.Helper()
	b := e.app.TxConfig().NewTxBuilder()
	require.NoError(e.t, b.SetMsgs(msgs...))
	b.SetGasLimit(gas)
	sum := math.ZeroInt()
	for _, m := range msgs {
		if pm, ok := m.(shieldedtypes.PrivateMsg); ok {
			f, _ := pm.PrivateFee()
			sum = sum.Add(f)
		}
	}
	if feeOverride != nil {
		b.SetFeeAmount(*feeOverride)
	} else {
		b.SetFeeAmount(sdk.NewCoins(sdk.NewCoin("uerth", sum)))
	}
	bz, err := e.app.TxConfig().TxEncoder()(b.GetTx())
	require.NoError(e.t, err)
	return bz
}

// signedTx signs with the user key in SIGN_MODE_DIRECT.
func (e *spikeEnv) signedTx(gas uint64, fee sdk.Coins, msgs ...sdk.Msg) []byte {
	e.t.Helper()
	cfg := e.app.TxConfig()
	b := cfg.NewTxBuilder()
	require.NoError(e.t, b.SetMsgs(msgs...))
	b.SetGasLimit(gas)
	b.SetFeeAmount(fee)
	addr := sdk.AccAddress(e.user.PubKey().Address())
	acc := e.app.AuthKeeper.GetAccount(e.ctx(), addr)
	require.NotNil(e.t, acc)
	mode := signingtypes.SignMode_SIGN_MODE_DIRECT
	require.NoError(e.t, b.SetSignatures(signingtypes.SignatureV2{
		PubKey: e.user.PubKey(), Data: &signingtypes.SingleSignatureData{SignMode: mode}, Sequence: acc.GetSequence(),
	}))
	sig, err := clienttx.SignWithPrivKey(e.ctx(), mode, authsigning.SignerData{
		Address: addr.String(), ChainID: e.chainID, AccountNumber: acc.GetAccountNumber(),
		Sequence: acc.GetSequence(), PubKey: e.user.PubKey(),
	}, b, e.user, cfg, acc.GetSequence())
	require.NoError(e.t, err)
	require.NoError(e.t, b.SetSignatures(sig))
	bz, err := cfg.TxEncoder()(b.GetTx())
	require.NoError(e.t, err)
	return bz
}

func burnedIn(res *abci.ResponseFinalizeBlock) string {
	for _, ev := range res.Events {
		if ev.Type == "gas_fees_split" {
			for _, a := range ev.Attributes {
				if a.Key == "burned" {
					return a.Value
				}
			}
		}
	}
	return ""
}

func TestPrivateSpike(t *testing.T) {
	e := initSpikeGenesis(t)
	pool := authtypes.NewModuleAddress(shieldedtypes.ModuleName)
	feeCollector := authtypes.NewModuleAddress(authtypes.FeeCollectorName)

	const gas = 400_000
	const fee = 2_001 // >= 0.005*400000 = 2000; odd so the burn rounds up

	// --- happy path: CheckTx, then FinalizeBlock, fee moved and half burned.
	good := e.buildTx(gas, nil, privMsg("note-1", fee))
	res := e.checkTx(good)
	require.Equal(t, uint32(0), res.Code, res.Log)
	t.Logf("CheckTx ok: gas_wanted=%d gas_used=%d", res.GasWanted, res.GasUsed)

	// Same bytes again in CheckTx: CometBFT's cache normally stops this first,
	// but the app rejects it too because the ante wrote the nullifier into
	// checkState.
	res = e.checkTx(good)
	require.NotEqual(t, uint32(0), res.Code)
	require.Contains(t, res.Log, "already spent")

	// Different bytes, same nullifier (gas bumped): still rejected in CheckTx.
	m := privMsg("note-1", fee)
	res = e.checkTx(e.buildTx(gas+1, nil, m))
	require.NotEqual(t, uint32(0), res.Code)
	require.Contains(t, res.Log, "already spent")

	poolBefore, supplyBefore := e.balance(pool), e.app.BankKeeper.GetSupply(e.ctx(), "uerth").Amount
	fb := e.finalize(good)
	require.Equal(t, uint32(0), fb.TxResults[0].Code, fb.TxResults[0].Log)
	t.Logf("FinalizeBlock ok: gas_used=%d burned=%s", fb.TxResults[0].GasUsed, burnedIn(fb))
	require.Equal(t, poolBefore.SubRaw(fee), e.balance(pool), "fee left the pool")
	require.Equal(t, "1001uerth", burnedIn(fb), "half (rounded up) burned by SplitCollectedFees")
	require.Equal(t, math.NewInt(1000), e.balance(feeCollector), "other half waits for distribution")
	require.True(t, supplyBefore.Sub(e.app.BankKeeper.GetSupply(e.ctx(), "uerth").Amount).GTE(math.NewInt(1001)))
	spent, err := e.app.ShieldedSpikeKeeper.HasNullifier(e.ctx(), nullifier("note-1"))
	require.NoError(t, err)
	require.True(t, spent)

	// CometBFT recheck after commit: the included tx is now invalid.
	rc, err := e.app.CheckTx(&abci.RequestCheckTx{Tx: good, Type: abci.CheckTxType_Recheck})
	require.NoError(t, err)
	require.NotEqual(t, uint32(0), rc.Code)

	// --- replay in a later block (identical bytes): rejected in DeliverTx.
	fb = e.finalize(good)
	require.NotEqual(t, uint32(0), fb.TxResults[0].Code)
	require.Contains(t, fb.TxResults[0].Log, "already spent")

	// Two txs, same nullifier, same block: the second fails.
	a := e.buildTx(gas, nil, privMsg("note-2", fee))
	b := e.buildTx(gas+7, nil, privMsg("note-2", fee))
	fb = e.finalize(a, b)
	require.Equal(t, uint32(0), fb.TxResults[0].Code, fb.TxResults[0].Log)
	require.NotEqual(t, uint32(0), fb.TxResults[1].Code)

	// Same nullifier twice in one tx.
	res = e.checkTx(e.buildTx(gas, nil, privMsg("note-3", fee), privMsg("note-3", fee)))
	require.Contains(t, res.Log, "repeated")

	// --- zero fee: rejected in CheckTx and in FinalizeBlock.
	zero := e.buildTx(gas, nil, privMsg("note-zero", 0))
	res = e.checkTx(zero)
	require.Equal(t, sdkerrors.ErrInsufficientFee.ABCICode(), res.Code, res.Log)
	fb = e.finalize(zero)
	require.Equal(t, sdkerrors.ErrInsufficientFee.ABCICode(), fb.TxResults[0].Code, fb.TxResults[0].Log)

	// Zero fee with no fee coins at all (AuthInfo.Fee.Amount empty).
	empty := sdk.NewCoins()
	res = e.checkTx(e.buildTx(gas, &empty, privMsg("note-zero2", 0)))
	require.NotEqual(t, uint32(0), res.Code, res.Log)

	// Below the node's min gas price: CheckTx rejects, but it clears the
	// consensus floor (1000uerth) so a proposer may still include it.
	cheap := e.buildTx(gas, nil, privMsg("note-cheap", 1000))
	res = e.checkTx(cheap)
	require.Equal(t, sdkerrors.ErrInsufficientFee.ABCICode(), res.Code, res.Log)
	fb = e.finalize(cheap)
	require.Equal(t, uint32(0), fb.TxResults[0].Code, fb.TxResults[0].Log)

	// Below the consensus floor: rejected in DeliverTx too.
	fb = e.finalize(e.buildTx(gas, nil, privMsg("note-999", 999)))
	require.Equal(t, sdkerrors.ErrInsufficientFee.ABCICode(), fb.TxResults[0].Code, fb.TxResults[0].Log)

	// Declared tx fee differs from the proof's fee.
	lie := sdk.NewCoins(sdk.NewInt64Coin("uerth", 5000))
	res = e.checkTx(e.buildTx(gas, &lie, privMsg("note-lie", fee)))
	require.Contains(t, res.Log, "must equal the proofs' fee")

	// Bad proof.
	bad := privMsg("note-bad", fee)
	bad.Fee = "3000" // proof bound fee=2001
	res = e.checkTx(e.buildTx(gas, nil, bad))
	require.Contains(t, res.Log, "invalid proof")

	// --- mixed tx: private + normal msg.
	userAddr := sdk.AccAddress(e.user.PubKey().Address())
	send := banktypes.NewMsgSend(userAddr, userAddr, sdk.NewCoins(sdk.NewInt64Coin("uerth", 1)))
	res = e.checkTx(e.buildTx(gas, nil, privMsg("note-mix", fee), send))
	require.Contains(t, res.Log, "cannot be mixed")
	// Mixed and properly signed by the normal msg's signer: still rejected.
	mixedSigned := e.signedTx(gas, sdk.NewCoins(sdk.NewInt64Coin("uerth", 5000)), privMsg("note-mix2", fee), send)
	res = e.checkTx(mixedSigned)
	require.Contains(t, res.Log, "cannot be mixed")
	fb = e.finalize(mixedSigned)
	require.Contains(t, fb.TxResults[0].Log, "cannot be mixed")

	// --- private msg carrying a signature: rejected.
	signedPriv := e.signedTx(gas, sdk.NewCoins(sdk.NewInt64Coin("uerth", fee)), privMsg("note-sig", fee))
	res = e.checkTx(signedPriv)
	require.Contains(t, res.Log, "must be unsigned")

	// --- normal txs unchanged: a signed MsgSend passes; an unsigned one gets
	// the SDK's ErrNoSignatures from the normal chain.
	normal := e.signedTx(200_000, sdk.NewCoins(sdk.NewInt64Coin("uerth", 1000)), send)
	res = e.checkTx(normal)
	require.Equal(t, uint32(0), res.Code, res.Log)
	fb = e.finalize(normal)
	require.Equal(t, uint32(0), fb.TxResults[0].Code, fb.TxResults[0].Log)
	res = e.checkTx(e.buildTx(200_000, &sdk.Coins{sdk.NewInt64Coin("uerth", 1000)}, send))
	require.Equal(t, sdkerrors.ErrNoSignatures.ABCICode(), res.Code, res.Log)

	// --- simulate: gas estimation works on an unproven tx (bogus proof), and
	// charges exactly what the real one costs.
	sim := privMsg("note-sim", fee)
	sim.Proof = []byte("not yet proven, same length as the real proof....")[:32]
	gi, _, err := e.app.Simulate(e.buildTx(gas, nil, sim))
	require.NoError(t, err)
	real := e.buildTx(gas, nil, privMsg("note-sim", fee))
	res = e.checkTx(real)
	require.Equal(t, uint32(0), res.Code, res.Log)
	// CheckTx runs only the ante, so compare against DeliverTx.
	fb = e.finalize(real)
	require.Equal(t, uint32(0), fb.TxResults[0].Code, fb.TxResults[0].Log)
	// Within a few gas: balance values read/written differ in length between
	// checkState (where simulate runs) and deliver state, as for any SDK tx.
	require.InDelta(t, gi.GasUsed, uint64(fb.TxResults[0].GasUsed), 100, "simulate gas ~= DeliverTx gas")
	t.Logf("Simulate gas_used=%d", gi.GasUsed)

	// --- a private msg reaching its handler without the private ante (as a
	// contract's CosmosMsg::Any or an ICA host tx would, since zero signers
	// pass their "every signer == caller" loop) is refused.
	h := e.app.MsgServiceRouter().Handler(privMsg("note-bypass", fee))
	require.NotNil(t, h)
	_, err = h(e.ctx(), privMsg("note-bypass", fee))
	require.ErrorContains(t, err, "did not pass the private ante chain")
	require.False(t, shieldedkeeper.IsAuthorized(e.ctx(), nullifier("note-bypass")))

	// --- SDK facts the design has to route around.
	dec, err := e.app.TxConfig().TxDecoder()(good)
	require.NoError(t, err, "decoder accepts a tx with no signer infos/signatures")
	signers, err := dec.(authsigning.SigVerifiableTx).GetSigners()
	require.NoError(t, err)
	require.Empty(t, signers)
	require.ErrorIs(t, dec.(sdk.HasValidateBasic).ValidateBasic(), sdkerrors.ErrNoSignatures)
	require.Panics(t, func() { dec.(sdk.FeeTx).FeePayer() }, "FeePayer indexes signers[0]")
	// Unordered private txs are refused (the unordered nonce path is in SigVerification).
	ub := e.app.TxConfig().NewTxBuilder()
	require.NoError(t, ub.SetMsgs(privMsg("note-unord", fee)))
	ub.SetGasLimit(gas)
	ub.SetFeeAmount(sdk.NewCoins(sdk.NewInt64Coin("uerth", fee)))
	ub.SetUnordered(true)
	ub.SetTimeoutTimestamp(e.now.Add(time.Minute))
	ubz, err := e.app.TxConfig().TxEncoder()(ub.GetTx())
	require.NoError(t, err)
	res = e.checkTx(ubz)
	require.True(t, strings.Contains(res.Log, "unordered"), res.Log)
}

package app

import (
	"crypto/rand"
	"strconv"
	"testing"
	"time"

	abci "github.com/cometbft/cometbft/abci/types"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	gfr "github.com/consensys/gnark-crypto/ecc/grumpkin/fr"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	txtypes "github.com/cosmos/cosmos-sdk/types/tx"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/stretchr/testify/require"

	shieldedtest "github.com/earth-network/earth/x/shielded/testutil"
	shieldedtypes "github.com/earth-network/earth/x/shielded/types"
	sstypes "github.com/earth-network/earth/x/shieldedstaking/types"
	"github.com/earth-network/earth/zk/orchard"
	"github.com/earth-network/earth/zk/privacy"
)

// Regression tests for the x/shielded audit (2026-10-02). Each began as the
// auditor's PoC and now asserts the fixed behaviour.

// sendMsgFor is send i proven for a tx with fields tx.
func (e *shieldedEnv) sendMsgFor(s shieldedtest.Scenario, i int, tx shieldedtypes.TxFields) *shieldedtypes.MsgSend {
	e.t.Helper()
	m, err := s.Msg(i, e.bech(shieldedtest.Receiver), tx, func(toml string, pub [][]byte) []byte {
		return e.prover.Prove(e.t, toml, pub)
	})
	require.NoError(e.t, err)
	return m
}

// privateTxFields encodes msg as an unsigned tx carrying tx's fields.
func (e *shieldedEnv) privateTxFields(tx shieldedtypes.TxFields, msg shieldedtypes.PrivateMsg) []byte {
	e.t.Helper()
	b := e.app.TxConfig().NewTxBuilder()
	require.NoError(e.t, b.SetMsgs(msg))
	b.SetGasLimit(tx.GasLimit)
	b.SetMemo(tx.Memo)
	b.SetTimeoutHeight(tx.TimeoutHeight)
	b.SetFeeAmount(sdk.NewCoins(sdk.NewCoin("uerth", shieldedtypes.TotalFee(msg))))
	bz, err := e.app.TxConfig().TxEncoder()(b.GetTx())
	require.NoError(e.t, err)
	return bz
}

// M1: a private tx is unsigned, so its memo, timeout height and gas limit
// were anyone's to rewrite: a relayer could swap an exchange deposit memo,
// strip the wallet's expiry, or lower the gas so the handler ran out after
// the ante had spent the notes. All three are now bound by the sighash; a
// tx rewritten in any of them fails its binding signature, and the tx as
// proven executes with its memo.
func TestShieldedPrivateTxFieldsBound(t *testing.T) {
	e := initShieldedEnv(t)
	s := shieldedtest.Default()
	e.shieldAll(s)
	tx := shieldedtypes.TxFields{Memo: "exchange-deposit-4242", TimeoutHeight: 1_000, GasLimit: shGas(2)}
	m := e.sendMsgFor(s, shieldedtest.Send2, tx)

	for name, other := range map[string]shieldedtypes.TxFields{
		"memo rewritten":   {Memo: "attacker-deposit-999", TimeoutHeight: tx.TimeoutHeight, GasLimit: tx.GasLimit},
		"memo stripped":    {TimeoutHeight: tx.TimeoutHeight, GasLimit: tx.GasLimit},
		"timeout stripped": {Memo: tx.Memo, GasLimit: tx.GasLimit},
		"timeout moved":    {Memo: tx.Memo, TimeoutHeight: 2_000_000, GasLimit: tx.GasLimit},
		"gas raised":       {Memo: tx.Memo, TimeoutHeight: tx.TimeoutHeight, GasLimit: tx.GasLimit + 12_345},
		"gas lowered":      {Memo: tx.Memo, TimeoutHeight: tx.TimeoutHeight, GasLimit: tx.GasLimit - 1},
	} {
		bz := e.privateTxFields(other, m)
		res := e.checkTx(bz)
		require.Equal(t, shieldedtypes.ErrInvalidBindingSig.ABCICode(), res.Code, "%s: %s", name, res.Log)
	}
	// And in a block (a proposer is no better placed than a relayer).
	fb := e.finalize(e.privateTxFields(shieldedtypes.TxFields{Memo: "attacker", TimeoutHeight: tx.TimeoutHeight, GasLimit: tx.GasLimit}, m))
	require.Equal(t, shieldedtypes.ErrInvalidBindingSig.ABCICode(), fb.TxResults[0].Code, fb.TxResults[0].Log)

	// timeout_timestamp is not bound: a private tx may not carry one.
	b := e.app.TxConfig().NewTxBuilder()
	require.NoError(t, b.SetMsgs(m))
	b.SetGasLimit(tx.GasLimit)
	b.SetMemo(tx.Memo)
	b.SetTimeoutHeight(tx.TimeoutHeight)
	b.SetTimeoutTimestamp(e.now.Add(time.Hour))
	b.SetFeeAmount(sdk.NewCoins(sdk.NewCoin("uerth", shieldedtypes.TotalFee(m))))
	bz, err := e.app.TxConfig().TxEncoder()(b.GetTx())
	require.NoError(t, err)
	require.Contains(t, e.checkTx(bz).Log, "timeout_timestamp")

	// The tx as proven: accepted, memo and all.
	good := e.privateTxFields(tx, m)
	require.Equal(t, uint32(0), e.checkTx(good).Code)
	fb = e.finalize(good)
	requireOK(t, fb.TxResults[0])
	dec, err := e.app.TxConfig().TxDecoder()(good)
	require.NoError(t, err)
	require.Equal(t, tx.Memo, dec.(sdk.TxWithMemo).GetMemo())
}

// M1: bb ignored bytes after a proof's 458 field elements, so a proof with a
// byte appended verified: the same tx under another hash. Every proof is now
// exactly 14,656 bytes, refused statelessly otherwise.
func TestShieldedProofBytesNotMalleable(t *testing.T) {
	e := initShieldedEnv(t)
	s := shieldedtest.Default()
	e.shieldAll(s)
	m := e.sendMsg(s, shieldedtest.Send2)
	require.Len(t, m.Bundle.Actions[0].Proof, shieldedtypes.ProofBytes)
	for _, extra := range []int{1, 31, 32} {
		padded := *m
		padded.Bundle.Actions = append([]shieldedtypes.Action(nil), m.Bundle.Actions...)
		padded.Bundle.Actions[0].Proof = append(append([]byte(nil), m.Bundle.Actions[0].Proof...), make([]byte, extra)...)
		res := e.checkTx(e.privateTx(shGas(2), nil, &padded))
		require.Equal(t, shieldedtypes.ErrInvalidBundle.ABCICode(), res.Code, res.Log)
	}
	requireOK(t, e.finalize(e.privateTx(shGas(2), nil, m)).TxResults[0])
}

// forgedPrivateTx is the auditor's free-verification tx: n actions with
// fresh nullifiers, a valid anchor and a recycled proof, and a valid binding
// signature over a forged, unbacked uerth balance.
func (e *shieldedEnv) forgedPrivateTx(n int, proof []byte) []byte {
	e.t.Helper()
	anchor, err := e.app.ShieldedKeeper.LatestRoot.Get(e.ctx())
	require.NoError(e.t, err)
	const fee = 1_000_000
	erthG := orchard.ValueBase(privacy.AssetID("uerth"))
	var bsk gfr.Element
	b := shieldedtypes.Bundle{Balances: []shieldedtypes.ValueBalance{{Denom: "uerth", Amount: fee}}}
	for i := 0; i < n; i++ {
		var r gfr.Element
		_, _ = r.SetRandom()
		bsk.Add(&bsk, &r)
		cv := orchard.Mul(orchard.R, r)
		if i == 0 {
			cv = orchard.Add(cv, orchard.Mul(erthG, orchard.ScalarU64(fee)))
		}
		var nf, cm fr.Element
		_, _ = nf.SetRandom()
		_, _ = cm.SetRandom()
		b.Actions = append(b.Actions, shieldedtypes.Action{Anchor: anchor, Nullifier: privacy.FieldBytes(nf),
			Commitment: privacy.FieldBytes(cm), Cv: orchard.PointBytes(cv), Proof: proof,
			Ciphertext: shieldedtest.NoteCT("forged/" + strconv.Itoa(i))})
	}
	b.BindingSig = make([]byte, orchard.BindingSigSize)
	m := &shieldedtypes.MsgSend{Bundle: b, Fee: fee}
	tx := shieldedtypes.TxFields{GasLimit: shGas(n)}
	sh, err := shieldedtypes.Sighash(m, shieldedtest.ChainID, tx, e.app.AuthKeeper.AddressCodec())
	require.NoError(e.t, err)
	m.Bundle.BindingSig, err = orchard.SignBinding(bsk, sh, rand.Reader)
	require.NoError(e.t, err)
	ob, err := m.Bundle.ToOrchard()
	require.NoError(e.t, err)
	require.NoError(e.t, ob.CheckBalance(sh, orchard.CanonicalBase), "the forged binding signature verifies")
	return e.privateTxFields(tx, m)
}

// M2: such a tx reached proof verification in CheckTx with nothing at stake
// and made the node verify every proof. It is still refused at the proofs
// (x/shielded/keeper TestCheckPrivateMsgOneJunkProof shows CheckTx now
// stops at the first), and pays nothing.
func TestShieldedCheckTxForgedBundle(t *testing.T) {
	e := initShieldedEnv(t)
	s := shieldedtest.Default()
	e.shieldAll(s)
	e.finalize()
	proof := e.sendMsg(s, shieldedtest.Send2).Bundle.Actions[0].Proof
	res := e.checkTx(e.forgedPrivateTx(16, proof))
	require.Equal(t, shieldedtypes.ErrInvalidProof.ABCICode(), res.Code, res.Log)
	require.Contains(t, res.Log, "bundle 0 action 0")
}

// L1: max_private_actions_per_block counts only txs that pass their ante. A
// block of failing private txs is bounded by block gas instead: each tx's
// fixed private gas (every proof's verification gas) is consumed before any
// proof is verified and counts toward max_gas although the ante fails, so
// once the block's gas is gone the rest are refused without verifying
// anything.
func TestShieldedBlockGasBoundsFailedVerification(t *testing.T) {
	e := initShieldedEnv(t)
	s := shieldedtest.Default()
	e.shieldAll(s)
	e.finalize()
	proof := e.sendMsg(s, shieldedtest.Send2).Bundle.Actions[0].Proof
	const n, txs = 16, 6
	var block [][]byte
	for i := 0; i < txs; i++ {
		block = append(block, e.forgedPrivateTx(n, proof))
	}
	fb := e.finalize(block...)
	verified, refused := 0, 0
	var gas int64
	for _, r := range fb.TxResults {
		gas += r.GasUsed
		switch r.Code {
		case shieldedtypes.ErrInvalidProof.ABCICode():
			verified++
		case sdkerrors.ErrOutOfGas.ABCICode():
			refused++
		default:
			t.Fatalf("unexpected result %d: %s", r.Code, r.Log)
		}
	}
	params, err := e.app.ShieldedKeeper.Params.Get(e.ctx())
	require.NoError(t, err)
	perTx := params.PrivateMsgGas([]*shieldedtypes.Bundle{{Actions: make([]shieldedtypes.Action, n)}})
	maxGas := int64(100_000_000) // networks/genesis.json consensus max_gas
	t.Logf("%d txs of %d actions: %d reached the proofs, %d refused for block gas; %d gas used, %d per tx charged up front",
		txs, n, verified, refused, gas, perTx)
	require.Positive(t, refused, "block gas ran out")
	require.LessOrEqual(t, int64(verified-1)*int64(perTx), maxGas,
		"proof work is bounded by max_gas / per-tx private gas (+ the tx that crossed the limit)")
}

// L3: the pool's module account can neither mint nor burn.
func TestShieldedPoolAccountHasNoMintBurn(t *testing.T) {
	e := initShieldedEnv(t)
	acc := e.app.AuthKeeper.GetModuleAccount(e.ctx(), shieldedtypes.ModuleName)
	require.NotNil(t, acc)
	require.False(t, acc.HasPermission(authtypes.Minter))
	require.False(t, acc.HasPermission(authtypes.Burner))
	require.Empty(t, GetMaccPerms()[shieldedtypes.ModuleName])
	require.Panics(t, func() {
		_ = e.app.BankKeeper.MintCoins(e.ctx(), shieldedtypes.ModuleName, sdk.NewCoins(sdk.NewInt64Coin("uerth", 1)))
	})
}

// L5: a msg with an action handler had its remainders unchecked: value its
// handler never takes would stay in the pool with no note for it. Each
// handler now declares what it releases, and the pool refuses any other
// remainder before spending anything (behind each msg's ValidateBasic, which
// refuses the same shapes first).
func TestDexReleaseMapMatchesHandler(t *testing.T) {
	e := initDexEnv(t)
	e.shield(uint64(10_000 * ssErth))
	e.shield(uint64(100 * ssErth))
	erth := e.w.unspent("uerth", uint64(1_000*ssErth))
	anml, _ := e.noteSwap(erth, uint64(1_000*ssErth), "uanml", 1, 0)
	v := e.valoper(e.genesisValidator())
	// A restake (which releases nothing) whose bundle also releases ANML.
	p := e.buildLegs(ssFee, leg{anml, 5})
	pc := privacy.FieldBytes(ssDet("l5", 0))
	st := fakeStake("l5", false)
	st.Proof, st.OwnerTag = make([]byte, shieldedtypes.ProofBytes), pc
	m := &sstypes.MsgRestake{Bundle: p.b, Validator: v, Stake: st}
	require.Error(t, m.ValidateBasic(), "the first line")
	ctx := shieldedtypes.WithTxFields(e.ctx(), ssTx)
	_, err := e.app.ShieldedKeeper.CheckPrivateMsg(ctx, m)
	require.ErrorIs(t, err, shieldedtypes.ErrReleaseMap)
	require.Contains(t, err.Error(), "uanml")
	// A delegation releasing its uerth: exactly what its handler takes.
	in := e.w.unspent("uerth", uint64(ssErth))
	dm, _, _ := e.delegateMsg(e.genesisValidator(), in, uint64(ssErth))
	_, err = e.app.ShieldedKeeper.CheckPrivateMsg(ctx, dm)
	require.NoError(t, err)
}

// AUDIT3-B (F2): a private MsgSend unshield could name x/shieldedstaking's
// (unblocked) module account as its receiver, putting unbooked uerth there:
// invariant 1 broken for good and every later export refused at import.
// checkUnshield now refuses any module account receiver.
func TestUnshieldIntoStakingModuleRefused(t *testing.T) {
	e := initShieldedEnv(t)
	s := shieldedtest.Default()
	e.shieldAll(s)
	requireOK(t, e.finalize(e.sendTx(s, shieldedtest.Send2)).TxResults[0])
	requireOK(t, e.finalize(e.sendTx(s, shieldedtest.Multi3)).TxResults[0])

	ctx := e.ctx()
	require.NoError(t, e.app.ShieldedStakingKeeper.AssertInvariants(ctx))
	staking := authtypes.NewModuleAddress(sstypes.ModuleName)
	before := e.app.BankKeeper.GetBalance(ctx, staking, "uerth").Amount
	s.Sends[shieldedtest.Unshield2].Receiver = staking
	m, err := s.Msg(shieldedtest.Unshield2, e.bech(staking),
		shieldedtypes.TxFields{GasLimit: shGas(2)}, func(toml string, pub [][]byte) []byte {
			return e.prover.Prove(t, toml, pub)
		})
	require.NoError(t, err)
	tx := e.privateTx(shGas(2), nil, m)
	ct := e.checkTx(tx)
	require.NotZero(t, ct.Code)
	require.Contains(t, ct.Log, "module account")
	fb := e.finalize(tx)
	require.NotZero(t, fb.TxResults[0].Code)
	ctx = e.ctx()
	require.True(t, before.Equal(e.app.BankKeeper.GetBalance(ctx, staking, "uerth").Amount))
	require.NoError(t, e.app.ShieldedStakingKeeper.AssertInvariants(ctx))
}

// AUDIT3-B: private staking's send restriction accepted any transfer from
// the shielded pool. It now accepts pool -> module only inside x/shielded's
// ReleaseToModule (marked context), the path its own private msgs pay by.
func TestPoolToStakingModuleNeedsRelease(t *testing.T) {
	e := initStakeEnv(t)
	ctx := e.ctx()
	pool := authtypes.NewModuleAddress(shieldedtypes.ModuleName)
	mod := authtypes.NewModuleAddress(sstypes.ModuleName)
	require.NoError(t, e.app.ShieldedStakingKeeper.AssertInvariants(ctx))
	one := sdk.NewCoins(sdk.NewInt64Coin("uerth", 1))

	_, err := e.app.ShieldedStakingKeeper.SendRestriction(ctx, pool, mod, one)
	require.ErrorIs(t, err, sstypes.ErrSendRestricted)
	_, err = e.app.ShieldedStakingKeeper.SendRestriction(shieldedtypes.WithModuleRelease(ctx, "dex"), pool, mod, one)
	require.ErrorIs(t, err, sstypes.ErrSendRestricted, "a release to another module does not count")
	_, err = e.app.ShieldedStakingKeeper.SendRestriction(shieldedtypes.WithModuleRelease(ctx, sstypes.ModuleName), pool, mod, one)
	require.NoError(t, err)

	e.fundPoolDirect(10)
	ctx = e.ctx()
	require.ErrorIs(t, e.app.BankKeeper.SendCoins(ctx, pool, mod, one), sstypes.ErrSendRestricted)
	require.NoError(t, e.app.ShieldedStakingKeeper.AssertInvariants(ctx))
}

// Audit 4 L1: a private tx whose timeout_height is at or below the last
// committed height can only fail in the next block. CheckTx refuses it (the
// SDK's rule admitted timeout == height), and PrepareProposal leaves out a
// tx whose timeout is below the proposal's height before counting it toward
// the private action cap.
func TestExpiredTimeoutHeight(t *testing.T) {
	e := initShieldedEnv(t)
	s := shieldedtest.Default()
	requireOK(t, e.shieldAll(s).TxResults[0])
	m := e.sendMsg(s, shieldedtest.Send2)
	withTimeout := func(h uint64) []byte {
		return e.privateTxFields(shieldedtypes.TxFields{TimeoutHeight: h, GasLimit: shGas(2)}, m)
	}

	expired := withTimeout(uint64(e.height))
	res := e.checkTx(expired)
	require.Equal(t, sdkerrors.ErrTxTimeoutHeight.ABCICode(), res.Code, res.Log)
	// One past the committed height is not refused for its timeout (it fails
	// later: the proof was made for other tx fields).
	res = e.checkTx(withTimeout(uint64(e.height) + 1))
	require.NotEqual(t, sdkerrors.ErrTxTimeoutHeight.ABCICode(), res.Code, res.Log)

	live := e.sendTx(s, shieldedtest.Send2)
	resp, err := e.app.PrepareProposal(&abci.RequestPrepareProposal{
		Txs: [][]byte{expired, live, withTimeout(uint64(e.height) + 1)}, MaxTxBytes: 1 << 30, Height: e.height + 1, Time: e.now,
	})
	require.NoError(t, err)
	require.Len(t, resp.Txs, 2, "the expired tx is left out")
	require.Equal(t, live, resp.Txs[0])
}

// Audit 6 A-L1: a private tx's gas_limit is the block space it claims, and
// its gas use is known by the end of the private ante; a limit past
// PrivateGasCeilingFactor x the use is refused, before anything else is
// checked, in CheckTx and in a block. Within it, the tx goes on to its
// checks (here its binding signature, which binds the limit it was proven
// for).
func TestPrivateGasLimitBounded(t *testing.T) {
	e := initShieldedEnv(t)
	s := shieldedtest.Default()
	e.shieldAll(s)
	tx := shieldedtypes.TxFields{GasLimit: shGas(2)}
	m := e.sendMsgFor(s, shieldedtest.Send2, tx)

	huge := e.privateTxFields(shieldedtypes.TxFields{GasLimit: 100_000_000}, m)
	res := e.checkTx(huge)
	require.Equal(t, sdkerrors.ErrInvalidRequest.ABCICode(), res.Code, res.Log)
	require.Contains(t, res.Log, "exceeds what this private tx uses")
	fb := e.finalize(huge)
	require.Contains(t, fb.TxResults[0].Log, "exceeds what this private tx uses")

	within := e.privateTxFields(shieldedtypes.TxFields{GasLimit: tx.GasLimit + 1_000}, m)
	res = e.checkTx(within)
	require.Equal(t, shieldedtypes.ErrInvalidBindingSig.ABCICode(), res.Code, res.Log)

	good := e.privateTxFields(tx, m)
	require.Equal(t, uint32(0), e.checkTx(good).Code)
	requireOK(t, e.finalize(good).TxResults[0])
}

// Re-audit R7 ("one tx encoding"): a relayer re-spelling an unsigned private
// tx without touching anything the sighash binds (a deprecated AuthInfo.tip,
// a non-critical unknown field appended to the body, one inside the msg) is
// refused, in CheckTx and in a block. Only the canonical encoding lands.
func TestPrivateTxRespellingsRefused(t *testing.T) {
	e := initDexEnv(t)
	e.shield(uint64(10_000 * ssErth))
	e.shield(uint64(100 * ssErth))
	erth := e.w.unspent("uerth", uint64(1_000*ssErth))
	anml, _ := e.noteSwap(erth, uint64(1_000*ssErth), "uanml", 1, 0)
	_ = e.buildLegs(ssFee, leg{anml, 5})
	in := e.w.unspent("uerth", uint64(ssErth))
	dm, _, _ := e.delegateMsg(e.genesisValidator(), in, uint64(ssErth))
	orig := e.privateTx(dm)

	var raw txtypes.TxRaw
	require.NoError(t, raw.Unmarshal(orig))
	var ai txtypes.AuthInfo
	require.NoError(t, ai.Unmarshal(raw.AuthInfoBytes))
	ai.Tip = &txtypes.Tip{Tipper: "x"} //nolint:staticcheck
	tipped := raw
	tipped.AuthInfoBytes, _ = ai.Marshal()
	tbz, _ := tipped.Marshal()

	// field 1025, varint: tag (1025<<3)|0 = 0x88 0x40, value 1
	padded := raw
	padded.BodyBytes = append(append([]byte(nil), raw.BodyBytes...), 0x88, 0x40, 0x01)
	pbz, _ := padded.Marshal()

	// the same unknown field inside the msg's Any value
	var body txtypes.TxBody
	require.NoError(t, body.Unmarshal(raw.BodyBytes))
	body.Messages[0].Value = append(append([]byte(nil), body.Messages[0].Value...), 0x88, 0x40, 0x01)
	inner := raw
	inner.BodyBytes, _ = body.Marshal()
	ibz, _ := inner.Marshal()

	for name, bz := range map[string][]byte{"tip": tbz, "body-noncritical": pbz, "msg-noncritical": ibz} {
		res := e.checkTx(bz)
		require.NotZero(t, res.Code, "%s: %s", name, res.Log)
		require.Contains(t, []uint32{sdkerrors.ErrInvalidRequest.ABCICode(), sdkerrors.ErrTxDecode.ABCICode()}, res.Code, "%s: %s", name, res.Log)
		r := e.run(bz)
		require.NotZero(t, r.Code, "%s delivered: %s", name, r.Log)
	}
	res := e.checkTx(orig)
	require.Zero(t, res.Code, res.Log)
	r := e.run(orig)
	require.Zero(t, r.Code, r.Log)
}

// Re-audit R2 (CheckTx bypass): a valid fee bundle (reusable: the tx never
// lands, so its nullifiers stay unspent) next to a junk stake proof. The
// msg's own proof is verified first, and proofs CheckTx saw verify are
// remembered: each junk attempt costs one verification, however many actions
// the bundles hold. The mirror image (a valid stake proof, a junk bundle
// proof) costs at most one new verification per attempt too.
func TestCheckTxValidBundleJunkActionProofCostsOne(t *testing.T) {
	e := initDexEnv(t)
	e.shield(uint64(10_000 * ssErth))
	e.shield(uint64(100 * ssErth))
	erth := e.w.unspent("uerth", uint64(1_000*ssErth))
	anml, _ := e.noteSwap(erth, uint64(1_000*ssErth), "uanml", 1, 0)
	_ = e.buildLegs(ssFee, leg{anml, 5})
	in := e.w.unspent("uerth", uint64(ssErth))
	dm, _, _ := e.delegateMsg(e.genesisValidator(), in, uint64(ssErth))
	require.Greater(t, len(dm.Bundle.Actions), 1)
	sk := e.app.ShieldedKeeper
	for i := 0; i < 5; i++ {
		m := *dm
		m.Stake.Proof = append([]byte(nil), dm.Stake.Proof...)
		m.Stake.Proof[100+i] ^= 0x01
		before := sk.CheckTxVerifications()
		res := e.checkTx(e.privateTx(&m))
		require.Equal(t, sstypes.ErrInvalidStakeProof.ABCICode(), res.Code, res.Log)
		require.Equal(t, uint64(1), sk.CheckTxVerifications()-before, "junk stake proof #%d", i)
	}
	for i := 0; i < 5; i++ {
		m := *dm
		m.Bundle.Actions = append([]shieldedtypes.Action(nil), dm.Bundle.Actions...)
		last := len(m.Bundle.Actions) - 1
		m.Bundle.Actions[last].Proof = append([]byte(nil), dm.Bundle.Actions[last].Proof...)
		m.Bundle.Actions[last].Proof[100+i] ^= 0x01
		before := sk.CheckTxVerifications()
		res := e.checkTx(e.privateTx(&m))
		require.NotZero(t, res.Code, res.Log)
		// the stake proof (first time only) and the earlier bundle proofs
		// (first time only), then the junk one
		if i > 0 {
			require.Equal(t, uint64(1), sk.CheckTxVerifications()-before, "junk bundle proof #%d", i)
		}
	}
	// The honest tx still passes, and lands.
	res := e.checkTx(e.privateTx(dm))
	require.Zero(t, res.Code, res.Log)
}

package app

import (
	"crypto/rand"
	"testing"
	"time"

	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	gfr "github.com/consensys/gnark-crypto/ecc/grumpkin/fr"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
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
func TestShieldedAuditPrivateTxFieldsBound(t *testing.T) {
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
func TestShieldedAuditProofBytesNotMalleable(t *testing.T) {
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
			Commitment: privacy.FieldBytes(cm), Cv: orchard.PointBytes(cv), Proof: proof})
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
// (x/shielded/keeper TestCheckPrivateMsgAuditOneJunkProof shows CheckTx now
// stops at the first), and pays nothing.
func TestShieldedAuditCheckTxForgedBundle(t *testing.T) {
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
func TestShieldedAuditBlockGasBoundsFailedVerification(t *testing.T) {
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
func TestShieldedAuditPoolAccountHasNoMintBurn(t *testing.T) {
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
func TestDexAuditReleaseMapMatchesHandler(t *testing.T) {
	e := initDexEnv(t)
	e.shield(uint64(10_000 * ssErth))
	e.shield(uint64(100 * ssErth))
	erth := e.w.unspent("uerth", uint64(1_000*ssErth))
	anml, _ := e.noteSwap(erth, uint64(1_000*ssErth), "uanml", 1, 0)
	v := e.valoper(e.genesisValidator())
	// A restake (which releases nothing) whose bundle also releases ANML.
	p := e.buildLegs(ssFee, leg{anml, 5})
	z := make([]byte, 32)
	pc := privacy.FieldBytes(ssDet("l5", 0))
	m := &sstypes.MsgRestake{Bundle: p.b, Validator: v, Stake: sstypes.StakeProof{Proof: make([]byte, shieldedtypes.ProofBytes),
		Anchor: z, Nullifiers: [][]byte{pc, z}, Commitments: [][]byte{pc, z}, SpcMint: pc, OwnerTag: pc}}
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


package app

// x/dex's note paths on the real app, with real proofs: swaps between notes
// through the launch genesis's ANML/ERTH pool, fees paid from swap and
// unshield outputs, ANML bought with transparent ERTH, pool-1 liquidity
// added from notes and withdrawn as a note, and the refusals of every
// transparent ANML leg and of every route around the private ante.
//
// Same harness as x/shieldedstaking's (shieldedstaking_env_test.go): a
// deterministic chain, every proof cached by its public inputs, here under
// x/dex/testdata/proofs (scripts/dex-fixtures.sh regenerates them).

import (
	"fmt"
	"testing"
	"time"

	"cosmossdk.io/math"
	abci "github.com/cometbft/cometbft/abci/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/cosmos/cosmos-sdk/x/authz"
	"github.com/stretchr/testify/require"

	dexkeeper "github.com/earth-network/earth/x/dex/keeper"
	dextypes "github.com/earth-network/earth/x/dex/types"
	shieldedtypes "github.com/earth-network/earth/x/shielded/types"
	"github.com/earth-network/earth/zk/privacy"
)

const anmlPool = uint64(1)

func initDexEnv(t *testing.T) *stakeEnv {
	e := initStakeEnv(t)
	e.proofDir = dexProofs
	return e
}

func (e *stakeEnv) dexInvariants() {
	e.t.Helper()
	e.invariants()
	require.NoError(e.t, e.app.DexKeeper.AssertInvariants(e.ctx()))
}

func (e *stakeEnv) pool(id uint64) dextypes.Pool {
	p, err := e.app.DexKeeper.Pool.Get(e.ctx(), id)
	require.NoError(e.t, err)
	return p
}

// noteSwapMsg spends amount of in (a fee note pays the fee, unless
// feeFromOutput is set) and swaps it for denomOut, minted to a fresh note.
func (e *stakeEnv) noteSwapMsg(in *wnote, amount uint64, denomOut string, minOut, feeFromOutput uint64, prove bool) (*dextypes.MsgNoteSwap, *pendingBundle, *wnote) {
	e.t.Helper()
	p := e.build(spend{denom: in.denom, inputs: []*wnote{in}, valueOut: amount, feeless: feeFromOutput > 0})
	out := e.w.fresh(denomOut, 0)
	m := &dextypes.MsgNoteSwap{
		Bundle: p.b, Fee: p.fee, DenomOut: denomOut, MinAmountOut: minOut,
		Pc: privacy.FieldBytes(e.w.pc(out)), Ciphertext: []byte("swap out"), FeeFromOutput: feeFromOutput,
	}
	if !prove {
		unproven(m)
		return m, p, out
	}
	e.prove(m, p)
	return m, p, out
}

// noteSwap runs a note swap and returns the output note.
func (e *stakeEnv) noteSwap(in *wnote, amount uint64, denomOut string, minOut, feeFromOutput uint64) (*wnote, *abci.ExecTxResult) {
	e.t.Helper()
	m, p, out := e.noteSwapMsg(in, amount, denomOut, minOut, feeFromOutput, true)
	res := e.run(e.privateTx(m))
	require.Equal(e.t, uint32(0), res.Code, res.Log)
	e.settle(p)
	return e.minted(res, out), res
}

// quote is what a swap of amountIn would pay out right now.
func (e *stakeEnv) quote(denomIn string, amountIn uint64, denomOut string) uint64 {
	e.t.Helper()
	out, err := e.app.DexKeeper.SimulateSwapExactIn(e.ctx(), sdk.NewCoin(denomIn, math.NewIntFromUint64(amountIn)), denomOut)
	require.NoError(e.t, err)
	return out.Amount.Uint64()
}

func feeEvents(t *testing.T, res *abci.ExecTxResult) []map[string]string {
	t.Helper()
	return eventsOf(res.Events, shieldedtypes.EventTypeFee)
}

// Swaps between notes in both directions through the ANML/ERTH pool, one
// paying its fee from its ERTH output; an unshield paying its fee from what
// it unshields; and a swap whose price moved past its bound, which fails
// whole: nothing spent, no fee.
func TestDexNoteSwaps(t *testing.T) {
	e := initDexEnv(t)
	e.shield(uint64(100_000 * ssErth))
	e.shield(uint64(100 * ssErth)) // fees
	pool0 := e.pool(anmlPool)

	// --- ERTH note -> ANML note.
	erth := e.w.unspent("uerth", uint64(10_000*ssErth))
	want := e.quote("uerth", uint64(10_000*ssErth), "uanml")
	anml, res := e.noteSwap(erth, uint64(10_000*ssErth), "uanml", want, 0)
	require.Equal(t, want, anml.value)
	require.Len(t, feeEvents(t, res), 1)
	// The ANML left the pool's reserve (POL retirement also shrinks it a
	// little every block, so: at least that much).
	p1 := e.pool(anmlPool)
	require.True(t, p1.ReserveToken.Amount.LTE(pool0.ReserveToken.Amount.SubRaw(int64(want))))
	swaps := eventsOf(res.Events, "swap")
	require.Len(t, swaps, 1)
	require.Equal(t, fmt.Sprintf("%duanml", want), swaps[0]["token_out"])
	require.Equal(t, dextypes.ModuleName, swaps[0]["trader"])
	e.dexInvariants()

	// --- ANML note -> ERTH note, fee from a fee note.
	half := anml.value / 2
	wantErth := e.quote("uanml", half, "uerth")
	before := e.w.balance("uerth")
	erthOut, _ := e.noteSwap(anml, half, "uerth", wantErth, 0)
	require.Equal(t, wantErth, erthOut.value)
	require.Equal(t, before-ssFee+wantErth, e.w.balance("uerth"))
	e.dexInvariants()

	// --- ANML note -> ERTH note paying its fee from the output: a holder of
	// nothing but ANML can sell it. No ERTH note is spent; the note holds the
	// output less the fee, and fee_collector got the fee from the dex.
	rest := e.w.unspent("uanml", 1)
	require.NotNil(t, rest)
	wantErth = e.quote("uanml", rest.value, "uerth")
	// The fee must clear the floor, and the bound must leave something.
	tiny, _, _ := e.noteSwapMsg(rest, rest.value, "uerth", wantErth, 1, false)
	ct := e.checkTx(e.privateTx(tiny))
	require.Equal(t, sdkerrors.ErrInsufficientFee.ABCICode(), ct.Code, ct.Log)
	before = e.w.balance("uerth")
	sold, res := e.noteSwap(rest, rest.value, "uerth", wantErth, ssFee)
	require.Equal(t, wantErth-ssFee, sold.value)
	require.Equal(t, before+sold.value, e.w.balance("uerth"), "no fee note spent")
	fees := feeEvents(t, res)
	require.Len(t, fees, 1)
	require.Equal(t, fmt.Sprintf("%duerth", ssFee), fees[0]["amount"])
	require.Equal(t, dextypes.ModuleName, fees[0]["module"])
	require.Equal(t, uint64(0), e.w.balance("uanml"))
	e.dexInvariants()

	// --- a price that moved past min_amount_out: the swap runs in the ante,
	// atomically with the spend, so the tx fails whole. Its notes stay
	// unspent and it pays nothing.
	erth = e.w.unspent("uerth", uint64(1_000*ssErth))
	want = e.quote("uerth", uint64(1_000*ssErth), "uanml")
	sm, sp, _ := e.noteSwapMsg(erth, uint64(1_000*ssErth), "uanml", want+1, 0, true)
	res = e.run(e.privateTx(sm))
	require.Equal(t, dextypes.ErrSlippage.ABCICode(), res.Code, res.Log)
	for _, n := range sp.in {
		spent, err := e.app.ShieldedKeeper.Nullifiers.Has(e.ctx(), privacy.FieldBytes(e.w.nf(n)))
		require.NoError(t, err)
		require.False(t, spent)
	}
	require.Empty(t, feeEvents(t, res))
	e.dexInvariants()

	// (The unshield paying its fee from what it unshields is MsgSend's: see
	// x/shielded's keeper and app tests.)
}

// A transparent ERTH holder buys ANML as a note; every transparent ANML leg
// is refused; a private dex msg reached any way but the private ante refuses.
func TestDexAnmlTransparentLegs(t *testing.T) {
	e := initDexEnv(t)
	user := e.bech(e.userAddr())

	// --- buy: ERTH from the account, ANML minted to a note.
	want := e.quote("uerth", uint64(1_000*ssErth), "uanml")
	n := e.w.fresh("uanml", 0)
	res := e.run(e.signedTx(e.user, 600_000, 5_000, &dextypes.MsgBuyAnml{
		Creator: user, TokenIn: sdk.NewInt64Coin("uerth", 1_000*ssErth), MinAmountOut: fmt.Sprint(want),
		Pc: privacy.FieldBytes(e.w.pc(n)), Ciphertext: []byte("bought"),
	}))
	require.Equal(t, uint32(0), res.Code, res.Log)
	n = e.minted(res, n)
	require.Equal(t, want, n.value)
	require.True(t, e.app.BankKeeper.GetBalance(e.ctx(), e.userAddr(), "uanml").IsZero())
	e.dexInvariants()

	// --- refusals of every transparent ANML leg.
	refused := func(msg sdk.Msg) {
		t.Helper()
		r := e.run(e.signedTx(e.user, 600_000, 5_000, msg))
		require.Equal(t, dextypes.ErrShieldedOnly.ABCICode(), r.Code, "%T: %s", msg, r.Log)
	}
	refused(&dextypes.MsgSwap{Creator: user, TokenIn: sdk.NewInt64Coin("uerth", ssErth), DenomOut: "uanml", MinAmountOut: "0"})
	refused(&dextypes.MsgSwap{Creator: user, TokenIn: sdk.NewInt64Coin("uanml", 1), DenomOut: "uerth", MinAmountOut: "0"})
	refused(&dextypes.MsgAddLiquidity{Creator: user, PoolId: anmlPool,
		AmountA: sdk.NewInt64Coin("uerth", ssErth), AmountB: sdk.NewInt64Coin("uanml", 1)})
	refused(&dextypes.MsgCreatePool{Creator: user, AmountA: sdk.NewInt64Coin("uerth", ssErth), AmountB: sdk.NewInt64Coin("uanml", 1)})
	// A withdrawal from the ANML pool must name a pc for its ANML leg.
	r := e.run(e.signedTx(e.user, 600_000, 5_000, &dextypes.MsgRemoveLiquidity{
		Creator: user, PoolId: anmlPool, Shares: sdk.NewInt64Coin(dextypes.LPShareDenom(anmlPool), 1),
	}))
	require.NotEqual(t, uint32(0), r.Code)
	require.Contains(t, r.Log, "pays uanml as a note")
	// Two-hop (token -> ERTH -> ANML and back): refused on the account's
	// leg before any route is looked up.
	refused(&dextypes.MsgSwap{Creator: user, TokenIn: sdk.NewInt64Coin("ibc/TOKEN", ssErth), DenomOut: "uanml", MinAmountOut: "0"})
	refused(&dextypes.MsgSwap{Creator: user, TokenIn: sdk.NewInt64Coin("uanml", 1), DenomOut: "ibc/TOKEN", MinAmountOut: "0"})
	e.dexInvariants()

	// --- bypass: the private msgs' handlers, reached by the router as a
	// contract or an ICA host would reach them, refuse.
	bal := func(denom string, v uint64) shieldedtypes.ValueBalance { return shieldedtypes.ValueBalance{Denom: denom, Amount: v} }
	pc := privacy.FieldBytes(ssDet("bypass-pc", 0))
	for _, m := range []sdk.Msg{
		&dextypes.MsgNoteSwap{Bundle: stubBundle("a", bal("uanml", 1), bal("uerth", ssFee)), Fee: ssFee, DenomOut: "uerth", MinAmountOut: 1, Pc: pc},
		&dextypes.MsgAddLiquidityShielded{Bundle: stubBundle("b", bal("uanml", 1), bal("uerth", ssFee+1)), Fee: ssFee,
			PoolId: anmlPool, Provider: user, RefundPc: pc},
	} {
		h := e.app.MsgServiceRouter().Handler(m)
		require.NotNil(t, h, "%T", m)
		_, err := h(e.ctx(), m)
		require.ErrorIs(t, err, shieldedtypes.ErrUnauthorized, "%T", m)
	}
	// ...and authz, which refuses a msg with no signer.
	exec := authz.NewMsgExec(e.userAddr(), []sdk.Msg{&dextypes.MsgNoteSwap{DenomOut: "uerth"}})
	fb := e.run(e.signedTx(e.user, 400_000, 5_000, &exec))
	require.NotEqual(t, uint32(0), fb.Code)
	// A private msg in a signed tx goes nowhere either.
	sw := &dextypes.MsgNoteSwap{Bundle: stubBundle("c", bal("uanml", 1), bal("uerth", ssFee)), Fee: ssFee, DenomOut: "uerth", MinAmountOut: 1, Pc: pc}
	ct := e.checkTx(e.signedTx(e.user, 400_000, 5_000, sw))
	require.NotEqual(t, uint32(0), ct.Code)
}

// Pool-1 liquidity from notes: ANML and ERTH released by one bundle, shares
// to a transparent provider, the leftover of the ratio back as notes;
// then a withdrawal that pays the ERTH leg to the account and mints the ANML
// leg as a note.
func TestDexAnmlPoolLiquidity(t *testing.T) {
	e := initDexEnv(t)
	user := e.bech(e.userAddr())
	e.shield(uint64(200_000 * ssErth))
	e.shield(uint64(100 * ssErth)) // fees
	erth := e.w.unspent("uerth", uint64(50_000*ssErth))
	anml, _ := e.noteSwap(erth, uint64(50_000*ssErth), "uanml", 1, 0)
	e.dexInvariants()

	// Deposit all the ANML and more ERTH than the ratio takes.
	pool := e.pool(anmlPool)
	lp := dextypes.LPShareDenom(anmlPool)
	total := e.app.BankKeeper.GetSupply(e.ctx(), lp).Amount
	erthIn := uint64(100_000 * ssErth)
	erthNote := e.w.unspent("uerth", erthIn+ssFee)
	pa := e.buildLegs(ssFee, leg{anml, anml.value}, leg{erthNote, erthIn})
	refE := e.w.fresh("uerth", 0)
	refT := &wnote{denom: "uanml", rho: refE.rho, rcm: refE.rcm} // the same pc
	wantShares := math.NewIntFromUint64(anml.value).Mul(total).Quo(pool.ReserveToken.Amount)
	m := &dextypes.MsgAddLiquidityShielded{
		Bundle: pa.b, Fee: pa.fee, PoolId: anmlPool, Provider: user, MinShares: wantShares.String(),
		RefundPc: privacy.FieldBytes(e.w.pc(refE)), RefundCiphertext: []byte("refund"),
	}
	e.prove(m, pa)
	// The sighash binds the provider: it cannot be changed.
	other := *m
	other.Provider = e.bech(e.genesisValidator().Bytes())
	ct := e.checkTx(e.privateTx(&other))
	require.Equal(t, shieldedtypes.ErrInvalidBindingSig.ABCICode(), ct.Code, ct.Log)

	res := e.run(e.privateTx(m))
	require.Equal(t, uint32(0), res.Code, res.Log)
	e.settle(pa)
	shares := e.app.BankKeeper.GetBalance(e.ctx(), e.userAddr(), lp).Amount
	require.True(t, shares.Equal(wantShares), "shares %s want %s", shares, wantShares)
	// The deposit is taken in the pool ratio (POL retirement burns shares and
	// reserves together, so the ratio read above still holds): all the
	// ANML but rounding, and the ERTH that matches it. What the ratio did not
	// take came back as notes to the refund pc.
	adds := eventsOf(res.Events, "add_liquidity")
	require.Len(t, adds, 1)
	require.Equal(t, sdk.NewCoin(lp, wantShares).String(), adds[0]["shares"])
	refE = e.minted(res, refE)
	depErth := erthIn - refE.value
	wantDep := wantShares.Mul(pool.ReserveErth.Amount).Quo(total).Uint64()
	require.InDelta(t, float64(wantDep), float64(depErth), float64(wantDep)/1e6+2)
	depTok := wantShares.Mul(pool.ReserveToken.Amount).Quo(total).Uint64()
	if anml.value > depTok {
		refT = e.minted(res, refT)
		require.Equal(t, anml.value-depTok, refT.value)
	}
	e.dexInvariants()

	// --- withdraw half to a pc: ERTH to the account, ANML as a note.
	half := shares.QuoRaw(2)
	back := e.w.fresh("uanml", 0)
	rm := &dextypes.MsgRemoveLiquidity{Creator: user, PoolId: anmlPool, Shares: sdk.NewCoin(lp, half),
		Pc: privacy.FieldBytes(e.w.pc(back)), Ciphertext: []byte("lp anml")}
	r := e.run(e.signedTx(e.user, 600_000, 5_000, rm))
	require.Equal(t, uint32(0), r.Code, r.Log)
	e.dexInvariants()
	erthBefore := e.app.BankKeeper.GetBalance(e.ctx(), e.userAddr(), "uerth").Amount
	var paid *abci.ResponseFinalizeBlock
	for i := 0; i < 9 && paid == nil; i++ {
		fb := e.next(24 * time.Hour)
		if len(eventsOf(fb.Events, "complete_unbond_liquidity")) > 0 {
			paid = fb
		}
	}
	require.NotNil(t, paid, "withdrawal never matured")
	done := eventsOf(paid.Events, "complete_unbond_liquidity")[0]
	outErth, err := sdk.ParseCoinNormalized(done["amount_a"])
	require.NoError(t, err)
	outTok, err := sdk.ParseCoinNormalized(done["amount_b"])
	require.NoError(t, err)
	require.True(t, outTok.IsPositive())
	gotErth := e.app.BankKeeper.GetBalance(e.ctx(), e.userAddr(), "uerth").Amount.Sub(erthBefore)
	require.True(t, gotErth.Equal(outErth.Amount), "account got %s, payout %s", gotErth, outErth)
	require.True(t, e.app.BankKeeper.GetBalance(e.ctx(), e.userAddr(), "uanml").IsZero())
	back.value = outTok.Amount.Uint64()
	e.w.track(back)
	e.w.scan(e)
	require.True(t, back.known, "the ANML leg was minted as a note to the withdrawal's pc")
	e.dexInvariants()
}

// The SimulateSwapExactIn query on the real app, over ABCI as a wallet's
// node serves it: it returns what the swap then pays, and its burn and
// pool writes stay in its discarded cache (supply, the dex's balance and
// the pool are unchanged, even from an uncached context).
func TestDexSimulateSwapQuery(t *testing.T) {
	e := initDexEnv(t)
	req := &dextypes.QuerySimulateSwapExactInRequest{OfferDenom: "uerth", OfferAmount: math.NewInt(1_000 * ssErth), AskDenom: "uanml"}

	bz, err := req.Marshal()
	require.NoError(t, err)
	qres, err := e.app.Query(t.Context(), &abci.RequestQuery{Path: "/earth.dex.v1.Query/SimulateSwapExactIn", Data: bz})
	require.NoError(t, err)
	require.Equal(t, uint32(0), qres.Code, qres.Log)
	var viaAbci dextypes.QuerySimulateSwapExactInResponse
	require.NoError(t, viaAbci.Unmarshal(qres.Value))
	require.Equal(t, "uanml", viaAbci.TokenOut.Denom)
	require.Equal(t, sdk.NewCoin("uerth", math.NewInt(1_000*ssErth).MulRaw(3).QuoRaw(1000)), viaAbci.Fee)

	ctx := e.ctx()
	supply := e.app.BankKeeper.GetSupply(ctx, "uerth")
	dexBal := e.app.BankKeeper.GetAllBalances(ctx, authtypes.NewModuleAddress(dextypes.ModuleName))
	pool := e.pool(anmlPool)
	res, err := dexkeeper.NewQueryServerImpl(e.app.DexKeeper).SimulateSwapExactIn(ctx, req)
	require.NoError(t, err)
	require.True(t, res.ErthBurned.IsPositive())
	require.Equal(t, supply, e.app.BankKeeper.GetSupply(ctx, "uerth"))
	require.Equal(t, dexBal, e.app.BankKeeper.GetAllBalances(ctx, authtypes.NewModuleAddress(dextypes.ModuleName)))
	require.Equal(t, pool, e.pool(anmlPool))
	require.Equal(t, e.quote("uerth", uint64(1_000*ssErth), "uanml"), res.TokenOut.Amount.Uint64())
}

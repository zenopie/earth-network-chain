// Package ante runs unsigned private txs through their own decorator chain.
//
// A private tx has no account behind it. It carries exactly one PrivateMsg,
// no signatures and no signer infos; its authorization is the msg's transfer
// proof, its replay protection is that transfer's nullifiers, and its fee is
// paid out of the pool by the transfer's ERTH fee slot. None of the SDK's
// account decorators apply (DeductFee calls FeePayer, which indexes
// signers[0] and panics with none), and the SDK's ValidateBasic refuses any
// unsigned tx, so the private chain replaces them:
//
//	SetUpContext, LimitSimulationGas, CircuitBreaker  (as the normal chain)
//	ValidateTx        tx shape; fee == the transfer's fee, in uerth
//	TxTimeoutHeight, ValidateMemo, ConsumeGasForTxSize  (as the normal chain)
//	PrivateMsg        fixed gas; block cap; state checks; proof;
//	                  spend + append; fee floor; fee to fee_collector
//
// Everything a private msg writes to the pool happens here, in the ante,
// after every check has passed. The ante's writes persist even if the msg
// then fails, so a handler failure can never leave inputs spent without their
// outputs, or a fee paid without its note spent (which would let the same
// notes pay fees forever).
//
// Gas is fixed per msg (params.PrivateMsgGas) and charged before any work;
// the pool's writes then run on an infinite gas meter. A private tx's gas is
// therefore a function of its bytes alone, identical in CheckTx, DeliverTx
// and simulate, and whoever relays an unsigned tx cannot lower its gas limit
// to make it fail halfway.
package ante

import (
	errorsmod "cosmossdk.io/errors"
	"cosmossdk.io/math"
	storetypes "cosmossdk.io/store/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	txtypes "github.com/cosmos/cosmos-sdk/types/tx"

	"github.com/earth-network/earth/x/shielded/keeper"
	"github.com/earth-network/earth/x/shielded/types"
)

// protoTxGetter is implemented by x/auth/tx's wrapper, the only sdk.Tx the
// default TxDecoder produces.
type protoTxGetter interface{ GetProtoTx() *txtypes.Tx }

func countPrivate(tx sdk.Tx) int {
	n := 0
	for _, m := range tx.GetMsgs() {
		if _, ok := m.(types.PrivateMsg); ok {
			n++
		}
	}
	return n
}

// NewRouter returns the app's ante handler. A tx without private msgs goes to
// normal, unchanged. A tx with one must hold exactly that one msg and carry
// no signatures or signer infos, and goes to private. Anything else is
// refused: a signed tx cannot smuggle a private msg past the private chain,
// and a private msg cannot ride along with msgs the normal chain would
// authorize.
func NewRouter(normal, private sdk.AnteHandler) sdk.AnteHandler {
	return func(ctx sdk.Context, tx sdk.Tx, simulate bool) (sdk.Context, error) {
		n := countPrivate(tx)
		if n == 0 {
			return normal(ctx, tx, simulate)
		}
		if n != 1 || len(tx.GetMsgs()) != 1 {
			return ctx, errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "a private tx carries exactly one private msg and nothing else")
		}
		ptx, ok := tx.(protoTxGetter)
		if !ok {
			return ctx, errorsmod.Wrapf(sdkerrors.ErrTxDecode, "unexpected tx type %T", tx)
		}
		p := ptx.GetProtoTx()
		if p.AuthInfo == nil || len(p.Signatures) != 0 || len(p.AuthInfo.SignerInfos) != 0 {
			return ctx, errorsmod.Wrap(sdkerrors.ErrUnauthorized, "private txs must be unsigned")
		}
		return private(ctx, tx, simulate)
	}
}

// ValidateTxDecorator replaces ante.NewValidateBasicDecorator for private txs
// (the SDK's returns ErrNoSignatures for any unsigned tx). It repeats what
// else that check covers and adds the private-tx shape.
type ValidateTxDecorator struct{}

func (ValidateTxDecorator) AnteHandle(ctx sdk.Context, tx sdk.Tx, simulate bool, next sdk.AnteHandler) (sdk.Context, error) {
	if ctx.IsReCheckTx() {
		return next(ctx, tx, simulate)
	}
	p := tx.(protoTxGetter).GetProtoTx()
	if p.Body == nil || p.AuthInfo == nil || p.AuthInfo.Fee == nil {
		return ctx, errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "missing body, auth info or fee")
	}
	fee := p.AuthInfo.Fee
	if fee.GasLimit > txtypes.MaxGasWanted {
		return ctx, errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "invalid gas supplied; %d > %d", fee.GasLimit, txtypes.MaxGasWanted)
	}
	if fee.Payer != "" || fee.Granter != "" {
		// A payer is appended to the tx's signers and would demand a
		// signature; a granter means feegrant. Neither applies.
		return ctx, errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "private txs take no fee payer or granter")
	}
	if len(p.Body.ExtensionOptions) != 0 || len(p.Body.NonCriticalExtensionOptions) != 0 {
		return ctx, errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "private txs take no extension options")
	}
	if p.Body.Unordered {
		// Replay protection is the nullifiers; the unordered-nonce path lives
		// in SigVerificationDecorator, which private txs never reach.
		return ctx, errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "private txs cannot be unordered")
	}
	// The declared fee must be exactly what the proof releases, in uerth, so
	// explorers, CometBFT and anything reading AuthInfo.Fee see the real fee.
	declared := sdk.Coins(fee.Amount)
	if !declared.IsValid() && !declared.Empty() {
		return ctx, errorsmod.Wrapf(sdkerrors.ErrInsufficientFee, "invalid fee %s", declared)
	}
	t := tx.GetMsgs()[0].(types.PrivateMsg).PrivateTransfer()
	want := sdk.NewCoins(sdk.NewCoin(types.FeeDenom, t.FeeInt()))
	if !declared.Equal(want) {
		return ctx, errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "tx fee %s must equal the proof's fee %s", declared, want)
	}
	return next(ctx, tx, simulate)
}

// PrivateMsgDecorator does the private msg's work, in order:
//
//  1. charge params.PrivateMsgGas (before anything, whatever follows);
//  2. in FinalizeBlock, admit the tx under max_private_txs_per_block;
//  3. the fee floor: fee >= params.min_fee always, and >= the node's
//     min-gas-price x gas in CheckTx;
//  4. the stateful checks (anchor, nullifiers, asset, receiver, room);
//  5. verify the proof (skipped on recheck, where neither the proof nor its
//     public inputs can have changed; charged but not required in simulate,
//     so a wallet can estimate a tx before proving over its final fee);
//  6. spend the nullifiers, append the outputs, pay the fee to
//     fee_collector (where x/earth burns half), and authorize the msg.
type PrivateMsgDecorator struct {
	K keeper.Keeper
}

func (d PrivateMsgDecorator) AnteHandle(ctx sdk.Context, tx sdk.Tx, simulate bool, next sdk.AnteHandler) (sdk.Context, error) {
	msg := tx.GetMsgs()[0].(types.PrivateMsg)
	params, err := d.K.Params.Get(ctx)
	if err != nil {
		return ctx, err
	}
	ctx.GasMeter().ConsumeGas(params.PrivateMsgGas(), "shielded: private msg (proof, nullifiers, notes)")

	// Past this point every read and write is prepaid.
	pool := ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())

	if ctx.ExecMode() == sdk.ExecModeFinalize {
		if err := d.K.CountPrivateTx(pool); err != nil {
			return ctx, err
		}
	}

	feeTx, ok := tx.(sdk.FeeTx)
	if !ok {
		return ctx, errorsmod.Wrap(sdkerrors.ErrTxDecode, "tx is not a FeeTx")
	}
	amt := msg.PrivateTransfer().FeeInt()
	minFee, err := d.K.MinFee(pool)
	if err != nil {
		return ctx, err
	}
	if amt.LT(minFee.Amount) {
		return ctx, errorsmod.Wrapf(sdkerrors.ErrInsufficientFee, "private fee %s%s below consensus minimum %s", amt, types.FeeDenom, minFee)
	}
	gas := feeTx.GetGas()
	if ctx.IsCheckTx() && !simulate {
		price := ctx.MinGasPrices().AmountOf(types.FeeDenom)
		if price.IsPositive() {
			req := price.MulInt64(int64(gas)).Ceil().RoundInt()
			if amt.LT(req) {
				return ctx, errorsmod.Wrapf(sdkerrors.ErrInsufficientFee, "insufficient fee; got %s%s required %s%s", amt, types.FeeDenom, req, types.FeeDenom)
			}
		}
	}

	prepared, err := d.K.CheckPrivateMsg(pool, msg)
	if err != nil {
		return ctx, err
	}

	if !ctx.IsReCheckTx() && !simulate {
		if err := d.K.VerifyPrivateMsg(pool, prepared); err != nil {
			return ctx, err
		}
	}

	pool, err = d.K.ExecutePrivateMsg(pool, msg)
	if err != nil {
		return ctx, err
	}
	// Carry the authorization, not the infinite meter, into the msg.
	ctx = keeper.CarryAuthorization(ctx, pool)
	ctx.EventManager().EmitEvent(sdk.NewEvent(sdk.EventTypeTx, sdk.NewAttribute(sdk.AttributeKeyFee, feeTx.GetFee().String())))
	if gas > 0 {
		ctx = ctx.WithPriority(amt.Quo(math.NewIntFromUint64(gas)).Int64())
	}
	return next(ctx, tx, simulate)
}

// RejectFeeDenomsDecorator refuses a transparent tx paying its fee in a
// shielded-only denom. The bank's send restriction would refuse the transfer
// to fee_collector anyway; this says why.
type RejectFeeDenomsDecorator struct {
	Denoms []string
}

func (d RejectFeeDenomsDecorator) AnteHandle(ctx sdk.Context, tx sdk.Tx, simulate bool, next sdk.AnteHandler) (sdk.Context, error) {
	if feeTx, ok := tx.(sdk.FeeTx); ok {
		for _, c := range feeTx.GetFee() {
			for _, d := range d.Denoms {
				if c.Denom == d {
					return ctx, errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "%s cannot pay fees: it exists only in the shielded pool", d)
				}
			}
		}
	}
	return next(ctx, tx, simulate)
}

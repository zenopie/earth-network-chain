// Package ante runs unsigned private txs through their own decorator chain.
//
// A private tx has no account behind it. It carries exactly one PrivateMsg,
// no signatures and no signer infos; its authorization is the msg's bundles
// (an action proof per action and a binding signature per bundle, all over
// the msg's sighash), its replay protection is their nullifiers, and its fee
// is paid out of the pool by the bundles' uerth balance. None of the SDK's
// account decorators apply (DeductFee calls FeePayer, which indexes
// signers[0] and panics with none), and the SDK's ValidateBasic refuses any
// unsigned tx, so the private chain replaces them:
//
//	SetUpContext, LimitSimulationGas, CircuitBreaker  (as the normal chain)
//	ValidateTx        tx shape; bundle shapes; fee == the msg's fee, in uerth
//	TxTimeoutHeight, ValidateMemo, ConsumeGasForTxSize  (as the normal chain)
//	PrivateMsg        fixed gas; block cap; state checks; binding signatures
//	                  and proofs; spend + append; fee floor; fee to
//	                  fee_collector; unshield; an action that must be atomic
//	                  with the spend, and a fee paid from that action's output
//
// A msg may spend more than one bundle (a stake vote and the bundle paying
// its fee); each is checked, proven and executed as a single one is, under
// the msg's one sighash. A msg may instead pay its fee out of the uerth its
// action produces (types.FeeFromOutputMsg); that fee is held to the same
// floor, and the ante refuses the tx unless it was paid in full before the
// ante returns.
//
// A msg of another module may carry an action beyond its bundles (see
// types.PrivateActionHandler); its checks and proofs run in the same pass,
// before anything is written.
//
// Everything a private msg writes to the pool happens here, in the ante,
// after every check has passed. The ante's writes persist even if the msg
// then fails, so a handler failure can never leave inputs spent without their
// outputs, or a fee paid without its note spent (which would let the same
// notes pay fees forever).
//
// Gas is fixed per bundle and per action (types.Params.PrivateMsgGas) and
// charged before any work; the pool's writes then run on an infinite gas
// meter. A private tx's gas is therefore a function of its shape alone,
// identical in CheckTx, DeliverTx and simulate, and whoever relays an
// unsigned tx cannot lower its gas limit to make it fail halfway.
package ante

import (
	stdmath "math"

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
	if p.Body.TimeoutTimestamp != nil {
		// Not bound by the sighash (TxFields): a relayer could add, move or
		// strip it. A private tx expires by timeout_height, which is bound.
		return ctx, errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "private txs take no timeout_timestamp; use timeout_height")
	}
	// The declared fee must be exactly what the bundles pay, in uerth, so
	// explorers, CometBFT and anything reading AuthInfo.Fee see the real fee.
	declared := sdk.Coins(fee.Amount)
	if !declared.IsValid() && !declared.Empty() {
		return ctx, errorsmod.Wrapf(sdkerrors.ErrInsufficientFee, "invalid fee %s", declared)
	}
	msg := tx.GetMsgs()[0].(types.PrivateMsg)
	if err := types.ValidateBundles(msg); err != nil {
		return ctx, err
	}
	want := sdk.NewCoins(sdk.NewCoin(types.FeeDenom, types.TotalFee(msg)))
	if !declared.Equal(want) {
		return ctx, errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "tx fee %s must equal the msg's fee %s", declared, want)
	}
	return next(ctx, tx, simulate)
}

// PrivateMsgDecorator does the private msg's work, in order:
//
//  1. refuse a bundle over max_actions_per_bundle, then charge
//     params.PrivateMsgGas (bundle_gas per bundle, proof and two notes per
//     action) plus the action's fixed gas (before anything, whatever follows);
//  2. in FinalizeBlock, admit the tx's actions under
//     max_private_actions_per_block;
//  3. the fee floor: fee >= params.min_fee always, and >= the node's
//     min-gas-price x gas in CheckTx;
//  4. the stateful checks (anchors, nullifiers, assets, room, the release
//     map), then the action's;
//  5. verify every binding signature, then every action proof (in parallel),
//     then the action's proofs (skipped on recheck, where neither the proofs
//     nor their public inputs can have changed; charged but not required in
//     simulate, so a wallet can estimate a tx before proving over its final
//     fee);
//  6. spend the nullifiers, append the outputs, pay the fee to fee_collector
//     (where x/earth burns half), pay an unshield's receiver, and authorize
//     the msg and its action;
//  7. run the action here if its handler must be atomic with the spend
//     (types.PrivateActionExecutor), and require any fee from output paid.
type PrivateMsgDecorator struct {
	K keeper.Keeper
}

func (d PrivateMsgDecorator) AnteHandle(ctx sdk.Context, tx sdk.Tx, simulate bool, next sdk.AnteHandler) (sdk.Context, error) {
	msg := tx.GetMsgs()[0].(types.PrivateMsg)
	// The tx fields every sighash below binds (types.SighashOf): an unsigned
	// tx's relayer cannot rewrite its memo, timeout height or gas limit.
	p := tx.(protoTxGetter).GetProtoTx()
	if p.Body == nil || p.AuthInfo == nil || p.AuthInfo.Fee == nil {
		return ctx, errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "missing body, auth info or fee")
	}
	ctx = types.WithTxFields(ctx, types.TxFields{
		Memo: p.Body.Memo, TimeoutHeight: p.Body.TimeoutHeight, GasLimit: p.AuthInfo.Fee.GasLimit,
	})
	params, err := d.K.Params.Get(ctx)
	if err != nil {
		return ctx, err
	}
	bundles := msg.PrivateBundles()
	for i, b := range bundles {
		if len(b.Actions) > int(params.MaxActionsPerBundle) {
			return ctx, errorsmod.Wrapf(types.ErrInvalidBundle, "bundle %d: %d actions, max_actions_per_bundle is %d",
				i, len(b.Actions), params.MaxActionsPerBundle)
		}
	}
	ctx.GasMeter().ConsumeGas(params.PrivateMsgGas(bundles), "shielded: private msg (bundles: proofs, nullifiers, notes)")
	action, hasAction := d.K.PrivateAction(msg)
	if hasAction {
		g, err := action.PrivateActionGas(ctx, msg)
		if err != nil {
			return ctx, err
		}
		ctx.GasMeter().ConsumeGas(g, "shielded: private action")
	}

	// Past this point every read and write is prepaid.
	pool := ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())

	if ctx.ExecMode() == sdk.ExecModeFinalize {
		if err := d.K.CountPrivateActions(pool, uint64(types.ActionCount(msg))); err != nil {
			return ctx, err
		}
	}

	feeTx, ok := tx.(sdk.FeeTx)
	if !ok {
		return ctx, errorsmod.Wrap(sdkerrors.ErrTxDecode, "tx is not a FeeTx")
	}
	// The whole fee: the bundles', plus what the msg pays from its output.
	// Held to the same floor and price whichever pays it.
	amt := types.TotalFee(msg)
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
	var actionPrepared any
	if hasAction {
		if actionPrepared, err = action.CheckPrivateAction(pool, msg); err != nil {
			return ctx, err
		}
	}

	if !ctx.IsReCheckTx() && !simulate {
		if err := d.K.VerifyPrivateMsg(pool, prepared); err != nil {
			return ctx, err
		}
		if hasAction {
			if err := action.VerifyPrivateAction(pool, msg, actionPrepared); err != nil {
				return ctx, err
			}
		}
	}

	pool, err = d.K.ExecutePrivateMsg(pool, msg)
	if err != nil {
		return ctx, err
	}
	if hasAction {
		pool = keeper.WithAuthorizedAction(pool, actionPrepared)
	}
	// An action that must be atomic with the spend runs here, and a fee from
	// output must now be paid in full; either failing fails the whole ante,
	// so nothing above is written.
	if err := d.K.ExecutePrivateAction(pool, msg, actionPrepared); err != nil {
		return ctx, err
	}
	// Carry the authorization, not the infinite meter, into the msg.
	ctx = keeper.CarryAuthorization(ctx, pool)
	ctx.EventManager().EmitEvent(sdk.NewEvent(sdk.EventTypeTx, sdk.NewAttribute(sdk.AttributeKeyFee, feeTx.GetFee().String())))
	if gas > 0 {
		// fee/gas, capped: a fee near 2^65 at gas 1 does not fit an int64
		// (Int64 would panic).
		prio := amt.Quo(math.NewIntFromUint64(gas))
		if !prio.IsInt64() {
			prio = math.NewInt(stdmath.MaxInt64)
		}
		ctx = ctx.WithPriority(prio.Int64())
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

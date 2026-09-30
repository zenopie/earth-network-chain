// Package ante routes unsigned private txs to their own decorator chain.
package ante

import (
	errorsmod "cosmossdk.io/errors"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	txtypes "github.com/cosmos/cosmos-sdk/types/tx"

	"bytes"

	"github.com/earth-network/earth/x/shieldedspike/keeper"
	"github.com/earth-network/earth/x/shieldedspike/types"
)

// protoTxGetter is implemented by x/auth/tx's wrapper, the only sdk.Tx the
// default TxDecoder produces.
type protoTxGetter interface{ GetProtoTx() *txtypes.Tx }

// countPrivate returns how many of the tx's msgs are private.
func countPrivate(tx sdk.Tx) int {
	n := 0
	for _, m := range tx.GetMsgs() {
		if _, ok := m.(types.PrivateMsg); ok {
			n++
		}
	}
	return n
}

// NewRouter returns the app's ante handler: txs with no private msgs go to
// normal, unchanged. A tx with any private msg must be entirely private and
// carry no signatures or signer infos, and then goes to private. Everything
// else is rejected.
func NewRouter(normal, private sdk.AnteHandler) sdk.AnteHandler {
	return func(ctx sdk.Context, tx sdk.Tx, simulate bool) (sdk.Context, error) {
		n := countPrivate(tx)
		if n == 0 {
			return normal(ctx, tx, simulate)
		}
		if n != len(tx.GetMsgs()) {
			return ctx, errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "private msgs cannot be mixed with other msgs")
		}
		ptx, ok := tx.(protoTxGetter)
		if !ok {
			return ctx, errorsmod.Wrapf(sdkerrors.ErrTxDecode, "unexpected tx type %T", tx)
		}
		p := ptx.GetProtoTx()
		if len(p.Signatures) != 0 || len(p.AuthInfo.SignerInfos) != 0 {
			return ctx, errorsmod.Wrap(sdkerrors.ErrUnauthorized, "private txs must be unsigned")
		}
		return private(ctx, tx, simulate)
	}
}

// ValidateBasicDecorator replaces ante.NewValidateBasicDecorator for private
// txs. The SDK's (x/auth/tx wrapper.ValidateBasic -> types/tx Tx.ValidateBasic)
// returns ErrNoSignatures for any unsigned tx; everything else it checks is
// repeated here, plus the private-tx shape.
type ValidateBasicDecorator struct{}

func (ValidateBasicDecorator) AnteHandle(ctx sdk.Context, tx sdk.Tx, simulate bool, next sdk.AnteHandler) (sdk.Context, error) {
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
		// A payer would be appended to GetSigners (Tx.GetSigners), making the tx
		// require a signature; a granter means feegrant. Neither applies.
		return ctx, errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "private txs take no fee payer or granter")
	}
	if len(p.Body.ExtensionOptions) != 0 || len(p.Body.NonCriticalExtensionOptions) != 0 {
		return ctx, errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "private txs take no extension options")
	}
	if p.Body.Unordered {
		// Replay protection is the nullifier; the unordered-nonce path lives in
		// SigVerificationDecorator, which private txs never reach.
		return ctx, errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "private txs cannot be unordered")
	}
	// The declared fee must equal what the proofs release, in uerth only, so
	// explorers, CometBFT and anything reading AuthInfo.Fee see the real fee.
	sum := math.ZeroInt()
	for _, m := range tx.GetMsgs() {
		f, err := m.(types.PrivateMsg).PrivateFee()
		if err != nil {
			return ctx, errorsmod.Wrap(sdkerrors.ErrInvalidRequest, err.Error())
		}
		sum = sum.Add(f)
	}
	declared := sdk.Coins(fee.Amount)
	if !declared.IsValid() && !declared.Empty() {
		return ctx, errorsmod.Wrapf(sdkerrors.ErrInsufficientFee, "invalid fee %s", declared)
	}
	want := sdk.NewCoins(sdk.NewCoin(types.FeeDenom, sum))
	if !declared.Equal(want) {
		return ctx, errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "tx fee %s must equal the proofs' fee %s", declared, want)
	}
	return next(ctx, tx, simulate)
}

// ProofDecorator charges fixed gas per private msg and verifies its proof.
// Gas is charged before verifying and regardless of the outcome.
type ProofDecorator struct{}

func (ProofDecorator) AnteHandle(ctx sdk.Context, tx sdk.Tx, simulate bool, next sdk.AnteHandler) (sdk.Context, error) {
	for _, m := range tx.GetMsgs() {
		pm := m.(types.PrivateMsg)
		ctx.GasMeter().ConsumeGas(types.ProofVerifyGas, "private proof verification")
		f, _ := pm.PrivateFee()
		ok := bytes.Equal(pm.PrivateProof(), types.StubProof(pm.PrivateNullifier(), f.String()))
		// Simulate charges the same gas but does not demand a valid proof, so a
		// wallet can estimate gas before it proves over the final fee.
		if !ok && !simulate {
			return ctx, errorsmod.Wrap(sdkerrors.ErrUnauthorized, "invalid proof")
		}
	}
	return next(ctx, tx, simulate)
}

// NullifierDecorator marks every nullifier spent, rejecting one already spent
// (in committed state, in this node's CheckTx state, or twice in this tx).
// This is the private tx's only replay protection: it has no sequence.
type NullifierDecorator struct{ K keeper.Keeper }

func (d NullifierDecorator) AnteHandle(ctx sdk.Context, tx sdk.Tx, simulate bool, next sdk.AnteHandler) (sdk.Context, error) {
	seen := map[string]bool{}
	for _, m := range tx.GetMsgs() {
		nf := m.(types.PrivateMsg).PrivateNullifier()
		if seen[string(nf)] {
			return ctx, errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "nullifier repeated in tx")
		}
		seen[string(nf)] = true
		spent, err := d.K.HasNullifier(ctx, nf)
		if err != nil {
			return ctx, err
		}
		if spent {
			return ctx, errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "nullifier %X already spent", nf)
		}
		if err := d.K.SetNullifier(ctx, nf); err != nil {
			return ctx, err
		}
	}
	return next(keeper.WithAuthorized(ctx, seen), tx, simulate)
}

// FeeDecorator enforces the fee floor and pays the fee from the pool.
//   - always (every mode, consensus): fee >= MinFee >= 1, so no zero-fee tx;
//   - CheckTx only: fee >= node min-gas-price * gas, as DeductFeeDecorator does.
type FeeDecorator struct{ K keeper.Keeper }

func (d FeeDecorator) AnteHandle(ctx sdk.Context, tx sdk.Tx, simulate bool, next sdk.AnteHandler) (sdk.Context, error) {
	feeTx := tx.(sdk.FeeTx)
	fee := feeTx.GetFee() // equals the proofs' fee sum; ValidateBasicDecorator checked
	amt := fee.AmountOf(types.FeeDenom)
	if min := d.K.MinFee(ctx); amt.LT(min) {
		return ctx, errorsmod.Wrapf(sdkerrors.ErrInsufficientFee, "private fee %s%s below consensus minimum %s%s", amt, types.FeeDenom, min, types.FeeDenom)
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
	if err := d.K.PayFee(ctx, fee); err != nil {
		return ctx, errorsmod.Wrap(sdkerrors.ErrInsufficientFunds, err.Error())
	}
	ctx.EventManager().EmitEvent(sdk.NewEvent(sdk.EventTypeTx, sdk.NewAttribute(sdk.AttributeKeyFee, fee.String())))
	if gas > 0 {
		ctx = ctx.WithPriority(amt.Quo(math.NewIntFromUint64(gas)).Int64())
	}
	return next(ctx, tx, simulate)
}


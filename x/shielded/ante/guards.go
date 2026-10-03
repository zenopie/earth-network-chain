package ante

import (
	errorsmod "cosmossdk.io/errors"
	storetypes "cosmossdk.io/store/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
)

// RecoverDecorator turns a panic in the rest of the private ante chain into
// an error returned with the context it was handed (audit 4, I3).
//
// baseapp recovers a panic out of the ante handler, but then reports the
// gas of the context it had before the ante ran: the gas the private ante
// had already charged (fixed per-proof gas, charged before any proof is
// verified) is reported as zero and never reaches the block gas meter, so a
// tx that panics the ante costs its block nothing for the work it caused.
// Returned as an error with this context, whose gas meter the later
// decorators charged, the gas is reported and consumed like any failed
// ante's, deterministically. Out-of-gas is re-panicked: SetUpContextDecorator
// (which must run before this one) turns it into ErrOutOfGas.
type RecoverDecorator struct{}

func (RecoverDecorator) AnteHandle(ctx sdk.Context, tx sdk.Tx, simulate bool, next sdk.AnteHandler) (newCtx sdk.Context, err error) {
	defer func() {
		if r := recover(); r != nil {
			if _, oog := r.(storetypes.ErrorOutOfGas); oog {
				panic(r)
			}
			newCtx, err = ctx, errorsmod.Wrapf(sdkerrors.ErrPanic, "private ante: %v", r)
		}
	}()
	return next(ctx, tx, simulate)
}

// ExpiredTimeoutDecorator refuses, in CheckTx and ReCheckTx, a private tx
// whose timeout_height is at or below the last committed height (audit 4,
// L1). Such a tx can only fail in the next block (TxTimeoutHeightDecorator
// refuses height > timeout_height there), so admitting or keeping it in the
// mempool only lets it take a proposer's private-action budget
// (max_private_actions_per_block, counted by PrepareProposal) from txs that
// would land. FinalizeBlock keeps the SDK's rule.
type ExpiredTimeoutDecorator struct{}

type timeoutTx interface {
	GetTimeoutHeight() uint64
}

func (ExpiredTimeoutDecorator) AnteHandle(ctx sdk.Context, tx sdk.Tx, simulate bool, next sdk.AnteHandler) (sdk.Context, error) {
	if ctx.IsCheckTx() || ctx.IsReCheckTx() {
		if t, ok := tx.(timeoutTx); ok {
			if th := t.GetTimeoutHeight(); th != 0 && ctx.BlockHeight() >= 0 && th <= uint64(ctx.BlockHeight()) {
				return ctx, errorsmod.Wrapf(sdkerrors.ErrTxTimeoutHeight,
					"timeout_height %d is not after the last committed height %d", th, ctx.BlockHeight())
			}
		}
	}
	return next(ctx, tx, simulate)
}

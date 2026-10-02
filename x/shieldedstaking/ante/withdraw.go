package ante

import (
	"context"

	"cosmossdk.io/core/address"
	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/authz"
	distrtypes "github.com/cosmos/cosmos-sdk/x/distribution/types"

	"github.com/earth-network/earth/x/shieldedstaking/types"
)

// WithdrawChecker is x/shieldedstaking's keeper: it refuses a validator
// operator's withdraw address pointing at another account.
type WithdrawChecker interface {
	CheckWithdrawAddr(ctx context.Context, del, withdraw sdk.AccAddress) error
}

// WithdrawAddrFilterDecorator refuses MsgSetWithdrawAddress — top level or
// inside an authz MsgExec — from a validator operator to another account: an
// operator's self-bond compounds, so its rewards must land in its own
// account. Genesis also disables withdraw addresses in x/distribution's
// params, which refuses every route; this is the operator-scoped line that
// holds if governance re-enables them, and the compounding resets a foreign
// address any other route sets (x/shieldedstaking/keeper/withdraw_addr.go).
type WithdrawAddrFilterDecorator struct {
	AddressCodec address.Codec
	K            WithdrawChecker
}

func (d WithdrawAddrFilterDecorator) AnteHandle(ctx sdk.Context, tx sdk.Tx, simulate bool, next sdk.AnteHandler) (sdk.Context, error) {
	if err := d.check(ctx, tx.GetMsgs(), 0); err != nil {
		return ctx, err
	}
	return next(ctx, tx, simulate)
}

func (d WithdrawAddrFilterDecorator) check(ctx sdk.Context, msgs []sdk.Msg, depth int) error {
	if depth > 4 {
		return errorsmod.Wrap(types.ErrOperatorWithdraw, "nested too deep")
	}
	for _, m := range msgs {
		switch m := m.(type) {
		case *distrtypes.MsgSetWithdrawAddress:
			del, err := d.AddressCodec.StringToBytes(m.DelegatorAddress)
			if err != nil {
				return err
			}
			wa, err := d.AddressCodec.StringToBytes(m.WithdrawAddress)
			if err != nil {
				return err
			}
			if err := d.K.CheckWithdrawAddr(ctx, del, wa); err != nil {
				return err
			}
		case *authz.MsgExec:
			inner, err := m.GetMessages()
			if err != nil {
				return err
			}
			if err := d.check(ctx, inner, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

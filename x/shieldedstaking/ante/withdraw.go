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

// WithdrawChecker is x/shieldedstaking's keeper: it refuses what would take a
// validator operator's self-bond rewards out of the epoch's compounding.
type WithdrawChecker interface {
	// CheckWithdrawAddr refuses an operator's withdraw address elsewhere.
	CheckWithdrawAddr(ctx context.Context, del, withdraw sdk.AccAddress) error
	// CheckRewardWithdraw refuses an operator's mid-epoch reward claim.
	CheckRewardWithdraw(ctx context.Context, del sdk.AccAddress) error
}

// CheckOperatorRewardsMsg refuses MsgSetWithdrawAddress from anyone while
// x/distribution's withdraw addresses are disabled (genesis) and, for a
// validator operator, to another account always; an operator's
// MsgWithdrawDelegatorReward; and every MsgWithdrawValidatorCommission: a
// validator's self-bond
// rewards and commission compound into its self-bond at the epoch end, so
// they stay with distribution until then and land in the operator account.
// An operator's only exit for either is unbonding its self-bond. Other msgs
// pass. It does not look inside authz MsgExec: the ante decorator recurses,
// and the app's message router (app/operator_router.go) sees each inner msg
// as authz dispatches it.
func CheckOperatorRewardsMsg(ctx context.Context, ac address.Codec, k WithdrawChecker, m sdk.Msg) error {
	switch m := m.(type) {
	case *distrtypes.MsgSetWithdrawAddress:
		del, err := ac.StringToBytes(m.DelegatorAddress)
		if err != nil {
			return err
		}
		wa, err := ac.StringToBytes(m.WithdrawAddress)
		if err != nil {
			return err
		}
		return k.CheckWithdrawAddr(ctx, del, wa)
	case *distrtypes.MsgWithdrawDelegatorReward:
		del, err := ac.StringToBytes(m.DelegatorAddress)
		if err != nil {
			return err
		}
		return k.CheckRewardWithdraw(ctx, del)
	case *distrtypes.MsgWithdrawValidatorCommission:
		// Only a validator has commission: always refused.
		return errorsmod.Wrapf(types.ErrOperatorRewardClaim, "commission of %s", m.ValidatorAddress)
	}
	return nil
}

// WithdrawAddrFilterDecorator refuses, top level or inside an authz MsgExec,
// what CheckOperatorRewardsMsg refuses: a validator operator's
// MsgSetWithdrawAddress to another account, its MsgWithdrawDelegatorReward,
// and MsgWithdrawValidatorCommission. Every other route to the msg router (authz
// dispatch, gov, group, ICA host, contracts) goes through app's filtering
// router, which applies the same check; this refuses a plain tx before it
// pays a fee. Genesis also disables withdraw addresses in x/distribution's
// params, and the compounding resets a foreign withdraw address it finds
// (x/shieldedstaking/keeper/withdraw_addr.go).
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
		if exec, ok := m.(*authz.MsgExec); ok {
			inner, err := exec.GetMessages()
			if err != nil {
				return err
			}
			if err := d.check(ctx, inner, depth+1); err != nil {
				return err
			}
			continue
		}
		if err := CheckOperatorRewardsMsg(ctx, d.AddressCodec, d.K, m); err != nil {
			return err
		}
	}
	return nil
}

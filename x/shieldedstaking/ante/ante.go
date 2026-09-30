// Package ante refuses transparent delegation in the normal ante chain.
package ante

import (
	"cosmossdk.io/core/address"
	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/authz"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	"github.com/earth-network/earth/x/shieldedstaking/types"
)

// StakingMsgFilterDecorator refuses MsgDelegate, MsgUndelegate,
// MsgBeginRedelegate and MsgCancelUnbondingDelegation — top level or inside
// an authz MsgExec — unless the delegator is the validator's own operator
// account (a public self-bond). MsgCreateValidator passes.
//
// This is the early, readable refusal. The enforcement nothing can route
// around (group and gov proposals, ICA host txs, contracts) is the staking
// hook in x/shieldedstaking/keeper/hooks.go, which refuses the same delegation
// inside x/staking itself.
type StakingMsgFilterDecorator struct {
	AddressCodec   address.Codec
	ValidatorCodec address.Codec
}

func (d StakingMsgFilterDecorator) AnteHandle(ctx sdk.Context, tx sdk.Tx, simulate bool, next sdk.AnteHandler) (sdk.Context, error) {
	if err := d.check(tx.GetMsgs(), 0); err != nil {
		return ctx, err
	}
	return next(ctx, tx, simulate)
}

func (d StakingMsgFilterDecorator) selfBond(delegator, validator string) bool {
	del, err := d.AddressCodec.StringToBytes(delegator)
	if err != nil {
		return false
	}
	val, err := d.ValidatorCodec.StringToBytes(validator)
	if err != nil {
		return false
	}
	return sdk.AccAddress(del).Equals(sdk.AccAddress(val))
}

func (d StakingMsgFilterDecorator) check(msgs []sdk.Msg, depth int) error {
	if depth > 4 {
		return errorsmod.Wrap(types.ErrTransparentStaking, "nested too deep")
	}
	for _, m := range msgs {
		ok := true
		switch m := m.(type) {
		case *stakingtypes.MsgDelegate:
			ok = d.selfBond(m.DelegatorAddress, m.ValidatorAddress)
		case *stakingtypes.MsgUndelegate:
			ok = d.selfBond(m.DelegatorAddress, m.ValidatorAddress)
		case *stakingtypes.MsgCancelUnbondingDelegation:
			ok = d.selfBond(m.DelegatorAddress, m.ValidatorAddress)
		case *stakingtypes.MsgBeginRedelegate:
			// A self-bond moving to another validator stops being one.
			ok = false
		case *authz.MsgExec:
			inner, err := m.GetMessages()
			if err != nil {
				return err
			}
			if err := d.check(inner, depth+1); err != nil {
				return err
			}
		}
		if !ok {
			return errorsmod.Wrapf(types.ErrTransparentStaking, "%s", sdk.MsgTypeURL(m))
		}
	}
	return nil
}

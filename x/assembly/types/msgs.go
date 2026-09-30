package types

import (
	"cosmossdk.io/core/address"
	errorsmod "cosmossdk.io/errors"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	sdk "github.com/cosmos/cosmos-sdk/types"

	shieldedtypes "github.com/earth-network/earth/x/shielded/types"
	"github.com/earth-network/earth/zk/privacy"
)

var (
	_ shieldedtypes.PrivateMsg = (*MsgVoteProposal)(nil)
	_ shieldedtypes.PrivateMsg = (*MsgProposeRemoval)(nil)
	_ shieldedtypes.PrivateMsg = (*MsgVoteRemoval)(nil)

	_ sdk.HasValidateBasic = (*MsgVoteProposal)(nil)
	_ sdk.HasValidateBasic = (*MsgProposeRemoval)(nil)
	_ sdk.HasValidateBasic = (*MsgVoteRemoval)(nil)
)

func validateFee(t *shieldedtypes.Transfer) error {
	if err := t.ValidateBasic(); err != nil {
		return err
	}
	if t.ValueOut != 0 {
		return errorsmod.Wrap(shieldedtypes.ErrInvalidTransfer, "an assembly msg's fee transfer releases nothing")
	}
	return nil
}

// PrivateTransfer implements PrivateMsg.
func (m *MsgVoteProposal) PrivateTransfer() *shieldedtypes.Transfer { return &m.Fee }

// Signal implements PrivateMsg. Fields: proposal_id, option.
func (m *MsgVoteProposal) Signal(chainID string, _ address.Codec) (fr.Element, error) {
	return m.Fee.ActionSignal(sdk.MsgTypeURL(m), chainID, privacy.U64(m.ProposalId), privacy.U64(uint64(m.Option)))
}

// ValidateBasic checks everything that needs no state.
func (m *MsgVoteProposal) ValidateBasic() error {
	if m.Option != VOTE_OPTION_YES && m.Option != VOTE_OPTION_NO {
		return ErrBadVoteOption
	}
	if err := validateFee(&m.Fee); err != nil {
		return err
	}
	return m.Membership.ValidateBasic()
}

// PrivateTransfer implements PrivateMsg.
func (m *MsgProposeRemoval) PrivateTransfer() *shieldedtypes.Transfer { return &m.Fee }

// Signal implements PrivateMsg. Fields: option_id.
func (m *MsgProposeRemoval) Signal(chainID string, _ address.Codec) (fr.Element, error) {
	return m.Fee.ActionSignal(sdk.MsgTypeURL(m), chainID, privacy.U64(m.OptionId))
}

// ValidateBasic checks everything that needs no state.
func (m *MsgProposeRemoval) ValidateBasic() error {
	if err := validateFee(&m.Fee); err != nil {
		return err
	}
	return m.Membership.ValidateBasic()
}

// PrivateTransfer implements PrivateMsg.
func (m *MsgVoteRemoval) PrivateTransfer() *shieldedtypes.Transfer { return &m.Fee }

// Signal implements PrivateMsg. Fields: option_id, option.
func (m *MsgVoteRemoval) Signal(chainID string, _ address.Codec) (fr.Element, error) {
	return m.Fee.ActionSignal(sdk.MsgTypeURL(m), chainID, privacy.U64(m.OptionId), privacy.U64(uint64(m.Option)))
}

// ValidateBasic checks everything that needs no state.
func (m *MsgVoteRemoval) ValidateBasic() error {
	if m.Option != VOTE_OPTION_YES && m.Option != VOTE_OPTION_NO {
		return ErrBadVoteOption
	}
	if err := validateFee(&m.Fee); err != nil {
		return err
	}
	return m.Membership.ValidateBasic()
}

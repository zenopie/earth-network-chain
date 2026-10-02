package types

import (
	"cosmossdk.io/core/address"
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

// PrivateBundles implements PrivateMsg: the fee bundle.
func (m *MsgVoteProposal) PrivateBundles() []*shieldedtypes.Bundle {
	return []*shieldedtypes.Bundle{&m.Fee}
}

// PrivateFee implements PrivateMsg: the fee bundle's uerth balance.
func (m *MsgVoteProposal) PrivateFee() uint64 { return shieldedtypes.FeeBundleFee(&m.Fee) }

// SighashFields implements PrivateMsg: proposal_id, option.
func (m *MsgVoteProposal) SighashFields(address.Codec) ([]fr.Element, error) {
	return []fr.Element{privacy.U64(m.ProposalId), privacy.U64(uint64(m.Option))}, nil
}

// ValidateBasic checks everything that needs no state.
func (m *MsgVoteProposal) ValidateBasic() error {
	if m.Option != VOTE_OPTION_YES && m.Option != VOTE_OPTION_NO {
		return ErrBadVoteOption
	}
	if err := shieldedtypes.ValidateFeeOnly(m); err != nil {
		return err
	}
	return m.Membership.ValidateBasic()
}

// PrivateBundles implements PrivateMsg: the fee bundle.
func (m *MsgProposeRemoval) PrivateBundles() []*shieldedtypes.Bundle {
	return []*shieldedtypes.Bundle{&m.Fee}
}

// PrivateFee implements PrivateMsg: the fee bundle's uerth balance.
func (m *MsgProposeRemoval) PrivateFee() uint64 { return shieldedtypes.FeeBundleFee(&m.Fee) }

// SighashFields implements PrivateMsg: option_id.
func (m *MsgProposeRemoval) SighashFields(address.Codec) ([]fr.Element, error) {
	return []fr.Element{privacy.U64(m.OptionId)}, nil
}

// ValidateBasic checks everything that needs no state.
func (m *MsgProposeRemoval) ValidateBasic() error {
	if err := shieldedtypes.ValidateFeeOnly(m); err != nil {
		return err
	}
	return m.Membership.ValidateBasic()
}

// PrivateBundles implements PrivateMsg: the fee bundle.
func (m *MsgVoteRemoval) PrivateBundles() []*shieldedtypes.Bundle {
	return []*shieldedtypes.Bundle{&m.Fee}
}

// PrivateFee implements PrivateMsg: the fee bundle's uerth balance.
func (m *MsgVoteRemoval) PrivateFee() uint64 { return shieldedtypes.FeeBundleFee(&m.Fee) }

// SighashFields implements PrivateMsg: option_id, option.
func (m *MsgVoteRemoval) SighashFields(address.Codec) ([]fr.Element, error) {
	return []fr.Element{privacy.U64(m.OptionId), privacy.U64(uint64(m.Option))}, nil
}

// ValidateBasic checks everything that needs no state.
func (m *MsgVoteRemoval) ValidateBasic() error {
	if m.Option != VOTE_OPTION_YES && m.Option != VOTE_OPTION_NO {
		return ErrBadVoteOption
	}
	if err := shieldedtypes.ValidateFeeOnly(m); err != nil {
		return err
	}
	return m.Membership.ValidateBasic()
}

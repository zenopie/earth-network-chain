package types

// TODO(orchard-phase2): these msgs still carry a retired 3-in/3-out Transfer.
// They satisfy shieldedtypes.PrivateMsg with no bundle, so the private ante
// refuses every one of them (types.ValidateBundles: a private msg spends
// 1..2 bundles) and none executes until it carries a Bundle.

import (
	"cosmossdk.io/core/address"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"

	shieldedtypes "github.com/earth-network/earth/x/shielded/types"
)

// PrivateBundles implements shieldedtypes.PrivateMsg: none yet.
func (m *MsgVoteProposal) PrivateBundles() []*shieldedtypes.Bundle { return nil }

// PrivateFee implements shieldedtypes.PrivateMsg: none yet.
func (m *MsgVoteProposal) PrivateFee() uint64 { return 0 }

// SighashFields implements shieldedtypes.PrivateMsg: refused until Phase 2.
func (m *MsgVoteProposal) SighashFields(address.Codec) ([]fr.Element, error) {
	return nil, shieldedtypes.ErrPhase2
}

// PrivateBundles implements shieldedtypes.PrivateMsg: none yet.
func (m *MsgProposeRemoval) PrivateBundles() []*shieldedtypes.Bundle { return nil }

// PrivateFee implements shieldedtypes.PrivateMsg: none yet.
func (m *MsgProposeRemoval) PrivateFee() uint64 { return 0 }

// SighashFields implements shieldedtypes.PrivateMsg: refused until Phase 2.
func (m *MsgProposeRemoval) SighashFields(address.Codec) ([]fr.Element, error) {
	return nil, shieldedtypes.ErrPhase2
}

// PrivateBundles implements shieldedtypes.PrivateMsg: none yet.
func (m *MsgVoteRemoval) PrivateBundles() []*shieldedtypes.Bundle { return nil }

// PrivateFee implements shieldedtypes.PrivateMsg: none yet.
func (m *MsgVoteRemoval) PrivateFee() uint64 { return 0 }

// SighashFields implements shieldedtypes.PrivateMsg: refused until Phase 2.
func (m *MsgVoteRemoval) SighashFields(address.Codec) ([]fr.Element, error) {
	return nil, shieldedtypes.ErrPhase2
}

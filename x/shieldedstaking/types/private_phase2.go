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
func (m *MsgDelegate) PrivateBundles() []*shieldedtypes.Bundle { return nil }

// PrivateFee implements shieldedtypes.PrivateMsg: none yet.
func (m *MsgDelegate) PrivateFee() uint64 { return 0 }

// SighashFields implements shieldedtypes.PrivateMsg: refused until Phase 2.
func (m *MsgDelegate) SighashFields(address.Codec) ([]fr.Element, error) {
	return nil, shieldedtypes.ErrPhase2
}

// PrivateBundles implements shieldedtypes.PrivateMsg: none yet.
func (m *MsgUndelegate) PrivateBundles() []*shieldedtypes.Bundle { return nil }

// PrivateFee implements shieldedtypes.PrivateMsg: none yet.
func (m *MsgUndelegate) PrivateFee() uint64 { return 0 }

// SighashFields implements shieldedtypes.PrivateMsg: refused until Phase 2.
func (m *MsgUndelegate) SighashFields(address.Codec) ([]fr.Element, error) {
	return nil, shieldedtypes.ErrPhase2
}

// PrivateBundles implements shieldedtypes.PrivateMsg: none yet.
func (m *MsgClaimUnbonding) PrivateBundles() []*shieldedtypes.Bundle { return nil }

// PrivateFee implements shieldedtypes.PrivateMsg: none yet.
func (m *MsgClaimUnbonding) PrivateFee() uint64 { return 0 }

// SighashFields implements shieldedtypes.PrivateMsg: refused until Phase 2.
func (m *MsgClaimUnbonding) SighashFields(address.Codec) ([]fr.Element, error) {
	return nil, shieldedtypes.ErrPhase2
}

// PrivateBundles implements shieldedtypes.PrivateMsg: none yet.
func (m *MsgStakeVote) PrivateBundles() []*shieldedtypes.Bundle { return nil }

// PrivateFee implements shieldedtypes.PrivateMsg: none yet.
func (m *MsgStakeVote) PrivateFee() uint64 { return 0 }

// SighashFields implements shieldedtypes.PrivateMsg: refused until Phase 2.
func (m *MsgStakeVote) SighashFields(address.Codec) ([]fr.Element, error) {
	return nil, shieldedtypes.ErrPhase2
}

// PrivateBundles implements shieldedtypes.PrivateMsg: none yet.
func (m *MsgLockPosition) PrivateBundles() []*shieldedtypes.Bundle { return nil }

// PrivateFee implements shieldedtypes.PrivateMsg: none yet.
func (m *MsgLockPosition) PrivateFee() uint64 { return 0 }

// SighashFields implements shieldedtypes.PrivateMsg: refused until Phase 2.
func (m *MsgLockPosition) SighashFields(address.Codec) ([]fr.Element, error) {
	return nil, shieldedtypes.ErrPhase2
}

// PrivateBundles implements shieldedtypes.PrivateMsg: none yet.
func (m *MsgUpdatePosition) PrivateBundles() []*shieldedtypes.Bundle { return nil }

// PrivateFee implements shieldedtypes.PrivateMsg: none yet.
func (m *MsgUpdatePosition) PrivateFee() uint64 { return 0 }

// SighashFields implements shieldedtypes.PrivateMsg: refused until Phase 2.
func (m *MsgUpdatePosition) SighashFields(address.Codec) ([]fr.Element, error) {
	return nil, shieldedtypes.ErrPhase2
}

// PrivateBundles implements shieldedtypes.PrivateMsg: none yet.
func (m *MsgUnlockPosition) PrivateBundles() []*shieldedtypes.Bundle { return nil }

// PrivateFee implements shieldedtypes.PrivateMsg: none yet.
func (m *MsgUnlockPosition) PrivateFee() uint64 { return 0 }

// SighashFields implements shieldedtypes.PrivateMsg: refused until Phase 2.
func (m *MsgUnlockPosition) SighashFields(address.Codec) ([]fr.Element, error) {
	return nil, shieldedtypes.ErrPhase2
}

// PrivateBundles implements shieldedtypes.PrivateMsg: none yet.
func (m *MsgPositionVote) PrivateBundles() []*shieldedtypes.Bundle { return nil }

// PrivateFee implements shieldedtypes.PrivateMsg: none yet.
func (m *MsgPositionVote) PrivateFee() uint64 { return 0 }

// SighashFields implements shieldedtypes.PrivateMsg: refused until Phase 2.
func (m *MsgPositionVote) SighashFields(address.Codec) ([]fr.Element, error) {
	return nil, shieldedtypes.ErrPhase2
}

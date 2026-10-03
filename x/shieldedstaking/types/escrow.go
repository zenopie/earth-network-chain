package types

import (
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/address"
)

// rewardEscrowKey is the derivation key of the validators' reward escrows.
var rewardEscrowKey = []byte("reward_escrow")

// RewardEscrowAddress is validator val's reward escrow: the account
// x/distribution pays the operator's self-bond rewards and the validator's
// commission to (its withdraw address, set by the chain). A module-derived
// address (ADR-028, 32 bytes): nobody holds a key for it, so only this
// module moves coins out of it, and it is deterministic from val, so it is
// never exported.
func RewardEscrowAddress(val sdk.ValAddress) sdk.AccAddress {
	return sdk.AccAddress(address.Module(ModuleName, rewardEscrowKey, val))
}

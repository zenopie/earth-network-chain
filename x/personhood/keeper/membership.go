package keeper

import (
	"context"

	errorsmod "cosmossdk.io/errors"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/x/personhood/types"
	shieldedtypes "github.com/earth-network/earth/x/shielded/types"
)

// MembershipStatement is types.MembershipStatement.
type MembershipStatement = types.MembershipStatement

// CheckMembership runs the state check on a membership proof: its root must be
// a current identity anchor.
func (k Keeper) CheckMembership(ctx context.Context, m types.Membership) error {
	return k.CheckIdentityAnchor(ctx, m.Root)
}

// VerifyMembership verifies m against st with the pool's membership key.
func (k Keeper) VerifyMembership(ctx context.Context, m types.Membership, st MembershipStatement) error {
	maxAct := uint64(0)
	if st.MaxActivation > 0 {
		maxAct = uint64(st.MaxActivation)
	}
	pub := types.MembershipPublicInputs(m, st.Scope, st.Signal, st.ExcludedDsc, maxAct)
	if err := k.shieldedKeeper.VerifyCircuit(ctx, shieldedtypes.CircuitMembership, m.Proof, pub); err != nil {
		return errorsmod.Wrap(types.ErrInvalidMembership, err.Error())
	}
	return nil
}

// MembershipActionGas is the fixed gas of a private action carrying one
// membership proof and making `writes` note-sized writes.
func (k Keeper) MembershipActionGas(ctx context.Context, writes uint64) (uint64, error) {
	proof, note, err := k.shieldedKeeper.PrivateGasPrices(ctx)
	if err != nil {
		return 0, err
	}
	return proof + writes*note, nil
}

// SignalOf is a private msg's signal on this chain.
func (k Keeper) SignalOf(ctx context.Context, msg shieldedtypes.PrivateMsg) (fr.Element, error) {
	return msg.Signal(sdk.UnwrapSDKContext(ctx).ChainID(), k.addressCodec)
}

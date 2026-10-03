package keeper

import (
	"context"

	errorsmod "cosmossdk.io/errors"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"

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
	pub := types.MembershipPublicInputs(m, st.Scope, st.Signal, st.ExcludedDsc, st.ExcludedCountry,
		types.BoundInput(st.MaxActivation), types.BoundInput(st.MaxPredecessor))
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

// SignalOf is the signal a private msg's membership proof binds: the msg's
// sighash on this chain in its tx (shieldedtypes.SighashOf), the same value
// every action proof of its fee bundle binds and its binding signature signs.
func (k Keeper) SignalOf(ctx context.Context, msg shieldedtypes.PrivateMsg) (fr.Element, error) {
	return shieldedtypes.SighashOf(ctx, msg, k.addressCodec)
}

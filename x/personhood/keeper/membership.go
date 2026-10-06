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

// MoveStatement is what a move proof is verified against: the scope its msg
// fixes and the msg's sighash.
type MoveStatement struct {
	Scope  fr.Element
	Signal fr.Element
}

// CheckMove checks a move proof's anchor (a recent identity root).
func (k Keeper) CheckMove(ctx context.Context, m types.MoveProof) error {
	return k.CheckIdentityAnchor(ctx, m.Root)
}

// VerifyMove verifies m against st with the pool's move key
// (circuits/move): the prover knows the secrets of the identity behind
// old_nullifier and of its successor under the same passport, which is
// live, behind new_nullifier.
func (k Keeper) VerifyMove(ctx context.Context, m types.MoveProof, st MoveStatement) error {
	pub := types.MovePublicInputs(m, st.Scope, st.Signal)
	if err := k.shieldedKeeper.VerifyCircuit(ctx, shieldedtypes.CircuitMove, m.Proof, pub); err != nil {
		return errorsmod.Wrap(types.ErrInvalidMove, err.Error())
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

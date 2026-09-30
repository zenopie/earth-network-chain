package types

import (
	"fmt"

	"cosmossdk.io/core/address"
	errorsmod "cosmossdk.io/errors"
	"cosmossdk.io/math"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/zk/privacy"
)

// PrivateMsg is a msg with no signers whose authorization is a transfer proof
// and whose replay protection is that transfer's nullifiers. The ante router
// sends any tx carrying one down the private chain (x/shielded/ante), which
// verifies the proof, spends the nullifiers, appends the outputs and pays the
// fee before the msg's handler runs.
//
// A msg type implementing this must also be given empty signers with a
// signing.CustomGetSigner, and its handler must refuse unless
// keeper.AuthorizedNullifiers holds for its transfer: a zero-signer msg passes
// every "each signer equals the caller" check vacuously, so a contract's
// CosmosMsg::Any or an ICA host tx would otherwise reach the handler without
// any proof being checked.
type PrivateMsg interface {
	sdk.Msg
	// PrivateTransfer is the transfer the msg spends.
	PrivateTransfer() *Transfer
	// Signal is the value the transfer proof's public signal must equal for
	// this msg on chainID (zk/privacy.SpendSignal with the msg's own fields).
	Signal(chainID string, ac address.Codec) (fr.Element, error)
}

var (
	_ PrivateMsg = (*MsgTransfer)(nil)

	_ sdk.HasValidateBasic = (*MsgTransfer)(nil)
	_ sdk.HasValidateBasic = (*MsgShield)(nil)
	_ sdk.HasValidateBasic = (*MsgRegisterAsset)(nil)
)

func checkField(what string, b []byte) error {
	if _, err := privacy.FieldFromBytes(b); err != nil {
		return errorsmod.Wrapf(ErrInvalidTransfer, "%s: %v", what, err)
	}
	return nil
}

// ValidateBasic checks everything about a transfer that needs no state.
func (t *Transfer) ValidateBasic() error {
	if len(t.Proof) == 0 || len(t.Proof) > MaxProofBytes {
		return errorsmod.Wrapf(ErrInvalidTransfer, "proof must be 1..%d bytes", MaxProofBytes)
	}
	if err := checkField("root", t.Root); err != nil {
		return err
	}
	if len(t.Nullifiers) != TransferArity || len(t.Commitments) != TransferArity || len(t.Ciphertexts) != TransferArity {
		return errorsmod.Wrapf(ErrInvalidTransfer, "need exactly %d nullifiers, commitments and ciphertexts", TransferArity)
	}
	for i := range TransferArity {
		if err := checkField(fmt.Sprintf("nullifier %d", i), t.Nullifiers[i]); err != nil {
			return err
		}
		if err := checkField(fmt.Sprintf("commitment %d", i), t.Commitments[i]); err != nil {
			return err
		}
		if len(t.Ciphertexts[i]) > MaxCiphertextBytes {
			return errorsmod.Wrapf(ErrInvalidTransfer, "ciphertext %d exceeds %d bytes", i, MaxCiphertextBytes)
		}
		for j := range i {
			if string(t.Nullifiers[i]) == string(t.Nullifiers[j]) {
				return errorsmod.Wrap(ErrInvalidTransfer, "duplicate nullifier")
			}
		}
	}
	// asset_pub is only constrained by the circuit when v_pub_out > 0, so the
	// chain pins the other case: nothing leaves, no asset is named, and
	// asset_pub is 0.
	if (t.ValueOut == 0) != (t.DenomOut == "") {
		return errorsmod.Wrap(ErrInvalidTransfer, "denom_out must be set exactly when value_out > 0")
	}
	if t.DenomOut != "" {
		if err := sdk.ValidateDenom(t.DenomOut); err != nil {
			return errorsmod.Wrap(ErrInvalidTransfer, err.Error())
		}
	}
	return nil
}

// FeeInt is the transfer's fee as a math.Int.
func (t *Transfer) FeeInt() math.Int { return math.NewIntFromUint64(t.Fee) }

// Ciphertexts3 is the ciphertexts as the fixed-size array the signal takes.
// Call after ValidateBasic.
func (t *Transfer) Ciphertexts3() [TransferArity][]byte {
	return [TransferArity][]byte{t.Ciphertexts[0], t.Ciphertexts[1], t.Ciphertexts[2]}
}

// PublicInputs lays out the transfer circuit's public inputs:
// root, nf[3], cm_out[3], fee, v_pub_out, asset_pub, signal. assetPub is the
// registry id of denom_out, or 0 when nothing leaves the pool.
func (t *Transfer) PublicInputs(assetPub, signal fr.Element) [][]byte {
	in := make([][]byte, 0, TransferPublicInputs)
	in = append(in, t.Root)
	in = append(in, t.Nullifiers...)
	in = append(in, t.Commitments...)
	in = append(in,
		privacy.FieldBytes(privacy.U64(t.Fee)),
		privacy.FieldBytes(privacy.U64(t.ValueOut)),
		privacy.FieldBytes(assetPub),
		privacy.FieldBytes(signal),
	)
	return in
}

// PrivateTransfer implements PrivateMsg.
func (m *MsgTransfer) PrivateTransfer() *Transfer { return &m.Transfer }

// ReceiverBytes is the unshield receiver, nil when nothing is unshielded.
func (m *MsgTransfer) ReceiverBytes(ac address.Codec) ([]byte, error) {
	if m.Receiver == "" {
		return nil, nil
	}
	bz, err := ac.StringToBytes(m.Receiver)
	if err != nil {
		return nil, errorsmod.Wrapf(ErrInvalidTransfer, "receiver: %v", err)
	}
	return bz, nil
}

// Signal implements PrivateMsg: zk/privacy.TransferSignal over the receiver's
// raw address bytes (empty when not unshielding) and the three ciphertexts.
func (m *MsgTransfer) Signal(chainID string, ac address.Codec) (fr.Element, error) {
	recv, err := m.ReceiverBytes(ac)
	if err != nil {
		return fr.Element{}, err
	}
	return privacy.TransferSignal(chainID, recv, m.Transfer.Ciphertexts3()), nil
}

// ValidateBasic runs in baseapp before the ante.
func (m *MsgTransfer) ValidateBasic() error {
	if err := m.Transfer.ValidateBasic(); err != nil {
		return err
	}
	if (m.Transfer.ValueOut == 0) != (m.Receiver == "") {
		return errorsmod.Wrap(ErrInvalidTransfer, "receiver must be set exactly when value_out > 0")
	}
	return nil
}

// ValidateBasic checks a shield needs no state to refuse.
func (m *MsgShield) ValidateBasic() error {
	if !m.Amount.IsValid() || !m.Amount.IsPositive() {
		return errorsmod.Wrapf(ErrInvalidNote, "amount %s must be a positive valid coin", m.Amount)
	}
	if !m.Amount.Amount.IsUint64() {
		return errorsmod.Wrap(ErrInvalidNote, "a note holds at most 2^64-1")
	}
	if _, err := privacy.FieldFromBytes(m.Pc); err != nil {
		return errorsmod.Wrapf(ErrInvalidNote, "pc: %v", err)
	}
	if len(m.Ciphertext) > MaxCiphertextBytes {
		return errorsmod.Wrapf(ErrInvalidNote, "ciphertext exceeds %d bytes", MaxCiphertextBytes)
	}
	return nil
}

// ValidateBasic checks the denom is well formed.
func (m *MsgRegisterAsset) ValidateBasic() error {
	return sdk.ValidateDenom(m.Denom)
}

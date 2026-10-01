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

// MultiTransferMsg is a PrivateMsg that spends more than one transfer in one
// tx: a stake vote proven against an old root plus a transfer paying its fee
// from a current one, or a pool deposit of two assets. PrivateTransfers lists
// every transfer, PrivateTransfer first. The private ante treats each as it
// treats a single transfer (anchor, nullifiers, proof, spend, append, fee),
// and every proof binds the msg's one Signal, which must bind every
// transfer's nullifiers (MultiSignal) so no transfer can be lifted out of the
// msg and paired with another.
type MultiTransferMsg interface {
	PrivateMsg
	PrivateTransfers() []*Transfer
}

// FeeFromOutputMsg is a PrivateMsg that may pay its fee out of the uerth its
// action produces (an unshield of uerth, an unbonding claim, a swap into
// uerth) instead of from a fee note. OutputFee is that fee, the msg's
// fee_from_output field, bound by the signal; when it is positive every
// transfer's own fee is 0. 0 means the fee is paid by the transfers as usual.
//
// The private ante charges it exactly like a transfer fee (the same floor,
// the same min gas price) and requires it paid, in full, before it writes
// anything: by the pool for an unshield, or by the action, which the ante
// then runs itself (PrivateActionExecutor) and which pays it with
// keeper.PayFeeFromModule.
type FeeFromOutputMsg interface {
	PrivateMsg
	OutputFee() uint64
}

// TransfersOf is every transfer msg spends, its PrivateTransfer first.
func TransfersOf(msg PrivateMsg) []*Transfer {
	if m, ok := msg.(MultiTransferMsg); ok {
		return m.PrivateTransfers()
	}
	return []*Transfer{msg.PrivateTransfer()}
}

// FeeFromOutputOf is msg's fee paid from its output, 0 for a msg that cannot
// pay that way.
func FeeFromOutputOf(msg PrivateMsg) uint64 {
	if m, ok := msg.(FeeFromOutputMsg); ok {
		return m.OutputFee()
	}
	return 0
}

// TotalFee is the whole fee msg's tx pays: every transfer's fee plus the fee
// from output. It is what AuthInfo.Fee must declare and what the ante holds
// to the fee floor and the min gas price.
func TotalFee(msg PrivateMsg) math.Int {
	total := math.NewIntFromUint64(FeeFromOutputOf(msg))
	for _, t := range TransfersOf(msg) {
		total = total.Add(t.FeeInt())
	}
	return total
}

// ValidateTransfers checks a private msg's transfers together: each one's
// ValidateBasic, nullifiers distinct across all of them, and, when the msg
// pays its fee from its output, no transfer paying a fee too.
func ValidateTransfers(msg PrivateMsg) error {
	ts := TransfersOf(msg)
	if len(ts) == 0 || len(ts) > MaxTransfersPerMsg {
		return errorsmod.Wrapf(ErrInvalidTransfer, "a private msg spends 1..%d transfers", MaxTransfersPerMsg)
	}
	seen := map[string]bool{}
	for i, t := range ts {
		if t == nil {
			return errorsmod.Wrapf(ErrInvalidTransfer, "transfer %d missing", i)
		}
		if err := t.ValidateBasic(); err != nil {
			return errorsmod.Wrapf(err, "transfer %d", i)
		}
		for _, nf := range t.Nullifiers {
			if seen[string(nf)] {
				return errorsmod.Wrap(ErrInvalidTransfer, "duplicate nullifier across transfers")
			}
			seen[string(nf)] = true
		}
	}
	if FeeFromOutputOf(msg) > 0 {
		for i, t := range ts {
			if t.Fee != 0 {
				return errorsmod.Wrapf(ErrInvalidTransfer, "transfer %d pays a fee; a msg paying its fee from its output pays no other", i)
			}
		}
	}
	return nil
}

// NullifierFields is t's nullifiers as field elements. Call after
// ValidateBasic.
func (t *Transfer) NullifierFields() ([TransferArity]fr.Element, error) {
	var nfs [TransferArity]fr.Element
	for i := range TransferArity {
		nf, err := privacy.FieldFromBytes(t.Nullifiers[i])
		if err != nil {
			return nfs, errorsmod.Wrapf(ErrInvalidTransfer, "nullifier %d: %v", i, err)
		}
		nfs[i] = nf
	}
	return nfs, nil
}

// MultiSignal is the signal every proof of a MultiTransferMsg binds:
// zk/privacy.MultiSpendSignal over each transfer's ciphertexts and
// nullifiers, in order, then the msg's own fields. Call after ValidateBasic.
func MultiSignal(msgType, chainID string, ts []*Transfer, extra ...fr.Element) (fr.Element, error) {
	cts := make([][TransferArity][]byte, len(ts))
	nfs := make([][TransferArity]fr.Element, len(ts))
	for i, t := range ts {
		if len(t.Ciphertexts) != TransferArity || len(t.Nullifiers) != TransferArity {
			return fr.Element{}, errorsmod.Wrapf(ErrInvalidTransfer, "transfer %d is malformed", i)
		}
		cts[i] = t.Ciphertexts3()
		nf, err := t.NullifierFields()
		if err != nil {
			return fr.Element{}, err
		}
		nfs[i] = nf
	}
	return privacy.MultiSpendSignal(msgType, chainID, cts, nfs, extra...), nil
}

var (
	_ PrivateMsg       = (*MsgTransfer)(nil)
	_ FeeFromOutputMsg = (*MsgTransfer)(nil)

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
// raw address bytes (empty when not unshielding), the three ciphertexts and
// fee_from_output.
func (m *MsgTransfer) Signal(chainID string, ac address.Codec) (fr.Element, error) {
	recv, err := m.ReceiverBytes(ac)
	if err != nil {
		return fr.Element{}, err
	}
	return privacy.TransferSignal(chainID, recv, m.Transfer.Ciphertexts3(), m.FeeFromOutput), nil
}

// ValidateBasic runs in baseapp before the ante.
func (m *MsgTransfer) ValidateBasic() error {
	if err := ValidateTransfers(m); err != nil {
		return err
	}
	if (m.Transfer.ValueOut == 0) != (m.Receiver == "") {
		return errorsmod.Wrap(ErrInvalidTransfer, "receiver must be set exactly when value_out > 0")
	}
	if m.FeeFromOutput > 0 {
		// The fee comes out of the uerth leaving the pool, and something must
		// be left for the receiver.
		if m.Transfer.DenomOut != FeeDenom || m.Transfer.ValueOut <= m.FeeFromOutput {
			return errorsmod.Wrapf(ErrInvalidTransfer, "fee_from_output needs an unshield of more than %d%s", m.FeeFromOutput, FeeDenom)
		}
	}
	return nil
}

// OutputFee implements FeeFromOutputMsg.
func (m *MsgTransfer) OutputFee() uint64 { return m.FeeFromOutput }

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

// ActionSignal is zk/privacy.ActionSignal over this transfer: the signal of a
// private msg of another module that pays its fee with t and acts with a
// second proof. Call after ValidateBasic.
func (t *Transfer) ActionSignal(msgType, chainID string, extra ...fr.Element) (fr.Element, error) {
	var nfs [TransferArity]fr.Element
	for i := range TransferArity {
		nf, err := privacy.FieldFromBytes(t.Nullifiers[i])
		if err != nil {
			return fr.Element{}, errorsmod.Wrapf(ErrInvalidTransfer, "nullifier %d: %v", i, err)
		}
		nfs[i] = nf
	}
	return privacy.ActionSignal(msgType, chainID, t.Ciphertexts3(), nfs, extra...), nil
}

package types

// TODO(orchard-phase2): everything in this file is the retired 3-in/3-out
// transfer, kept only so the msgs of x/personhood, x/assembly,
// x/shieldedstaking and x/dex (which still embed a Transfer) compile until
// they carry bundles. None of it is reachable on chain: the private ante
// accepts only bundles, those msgs report none (PrivateBundles() == nil),
// so ValidateBundles refuses every one of them, and the transfer circuit and
// its verifying key are gone. Delete this file with Phase 2.

import (
	"fmt"

	"cosmossdk.io/core/address"
	errorsmod "cosmossdk.io/errors"
	"cosmossdk.io/math"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/zk/privacy"
)

const (
	// CircuitTransfer names the retired transfer circuit. It is not a
	// verifying key Params accepts.
	CircuitTransfer = "transfer"

	// TransferArity is the retired transfer circuit's input and output count.
	TransferArity = 3

	// MaxTransfersPerMsg bounds the transfers of a legacy msg.
	MaxTransfersPerMsg = 2

	// TransferPublicInputs is the retired transfer circuit's public input
	// count: root, nf[3], cm_out[3], fee, v_pub_out, asset_pub, signal.
	TransferPublicInputs = 11
)

// TransferMsg is a legacy msg carrying a transfer.
type TransferMsg interface {
	sdk.Msg
	PrivateTransfer() *Transfer
	Signal(chainID string, ac address.Codec) (fr.Element, error)
}

// MultiTransferMsg is a legacy msg spending more than one transfer.
type MultiTransferMsg interface {
	TransferMsg
	PrivateTransfers() []*Transfer
}

// TransfersOf is every transfer msg spends, its PrivateTransfer first.
func TransfersOf(msg TransferMsg) []*Transfer {
	if m, ok := msg.(MultiTransferMsg); ok {
		return m.PrivateTransfers()
	}
	return []*Transfer{msg.PrivateTransfer()}
}

// ValidateTransfers checks a legacy msg's transfers together.
func ValidateTransfers(msg TransferMsg) error {
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
	if m, ok := msg.(interface{ OutputFee() uint64 }); ok && m.OutputFee() > 0 {
		for i, t := range ts {
			if t.Fee != 0 {
				return errorsmod.Wrapf(ErrInvalidTransfer, "transfer %d pays a fee; a msg paying its fee from its output pays no other", i)
			}
		}
	}
	return nil
}

// NullifierFields is t's nullifiers as field elements.
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

// MultiSignal is the legacy signal of a MultiTransferMsg.
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

func checkTransferField(what string, b []byte) error {
	if _, err := privacy.FieldFromBytes(b); err != nil {
		return errorsmod.Wrapf(ErrInvalidTransfer, "%s: %v", what, err)
	}
	return nil
}

// ValidateBasic checks everything about a legacy transfer that needs no
// state.
func (t *Transfer) ValidateBasic() error {
	if len(t.Proof) == 0 || len(t.Proof) > MaxProofBytes {
		return errorsmod.Wrapf(ErrInvalidTransfer, "proof must be 1..%d bytes", MaxProofBytes)
	}
	if err := checkTransferField("root", t.Root); err != nil {
		return err
	}
	if len(t.Nullifiers) != TransferArity || len(t.Commitments) != TransferArity || len(t.Ciphertexts) != TransferArity {
		return errorsmod.Wrapf(ErrInvalidTransfer, "need exactly %d nullifiers, commitments and ciphertexts", TransferArity)
	}
	for i := range TransferArity {
		if err := checkTransferField(fmt.Sprintf("nullifier %d", i), t.Nullifiers[i]); err != nil {
			return err
		}
		if err := checkTransferField(fmt.Sprintf("commitment %d", i), t.Commitments[i]); err != nil {
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
func (t *Transfer) Ciphertexts3() [TransferArity][]byte {
	return [TransferArity][]byte{t.Ciphertexts[0], t.Ciphertexts[1], t.Ciphertexts[2]}
}

// PublicInputs lays out the retired transfer circuit's public inputs.
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

// ActionSignal is zk/privacy.ActionSignal over this transfer.
func (t *Transfer) ActionSignal(msgType, chainID string, extra ...fr.Element) (fr.Element, error) {
	nfs, err := t.NullifierFields()
	if err != nil {
		return fr.Element{}, err
	}
	return privacy.ActionSignal(msgType, chainID, t.Ciphertexts3(), nfs, extra...), nil
}

// ErrPhase2 is what a legacy msg's PrivateMsg stubs return.
var ErrPhase2 = errorsmod.Wrap(ErrInvalidBundle, "TODO(orchard-phase2): this msg does not carry a bundle yet")

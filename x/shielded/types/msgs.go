package types

import (
	"math/bits"
	"sort"

	"cosmossdk.io/core/address"
	errorsmod "cosmossdk.io/errors"
	"cosmossdk.io/math"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/zk/orchard"
	"github.com/earth-network/earth/zk/privacy"
)

// PrivateMsg is a msg with no signers whose authorization is its bundles
// (every action proof and each binding signature, over the msg's sighash) and
// whose replay protection is their nullifiers. The ante router sends any tx
// carrying one down the private chain (x/shielded/ante), which verifies the
// bundles, spends the nullifiers, appends the outputs and pays the fee before
// the msg's handler runs.
//
// A msg type implementing this must also be given empty signers with a
// signing.CustomGetSigner, and its handler must refuse unless
// keeper.AuthorizedAction (or AuthorizedPositions) holds for it: a
// zero-signer msg passes every "each signer equals the caller" check
// vacuously, so a contract's CosmosMsg::Any or an ICA host tx would
// otherwise reach the handler without any proof being checked.
type PrivateMsg interface {
	sdk.Msg
	// PrivateBundles is every bundle the msg spends, in sighash order
	// (1..MaxBundlesPerMsg).
	PrivateBundles() []*Bundle
	// PrivateFee is the uerth the bundles pay to fee_collector, out of their
	// uerth balance.
	PrivateFee() uint64
	// SighashFields is the msg's own fields, bound after the bundle digests
	// (Sighash). Everything the msg's effect depends on that is not in a
	// bundle must be here.
	SighashFields(ac address.Codec) ([]fr.Element, error)
}

// UnshieldMsg is a PrivateMsg whose remainders (Remainders) all go to one
// transparent account, paid by the ante right after the fee. MsgSend.
type UnshieldMsg interface {
	PrivateMsg
	// UnshieldReceiver is the receiver's raw address, nil when the msg
	// unshields nothing.
	UnshieldReceiver(ac address.Codec) ([]byte, error)
}

// Sighash is msg's sighash on chainID in a tx with fields tx
// (zk/orchard.Sighash): the msg type URL, the chain id, every bundle's digest
// in order, the tx's memo, timeout height and gas limit, then the msg's own
// fields. Every action proof binds it, every binding signature signs it, and
// any other proof the msg carries (membership, passport) binds it as its
// signal. Call after ValidateBundles.
func Sighash(msg PrivateMsg, chainID string, tx TxFields, ac address.Codec) (fr.Element, error) {
	bs := msg.PrivateBundles()
	ob := make([]*orchard.Bundle, len(bs))
	for i, b := range bs {
		o, err := b.ToOrchard()
		if err != nil {
			return fr.Element{}, err
		}
		ob[i] = o
	}
	fields, err := msg.SighashFields(ac)
	if err != nil {
		return fr.Element{}, err
	}
	return orchard.Sighash(sdk.MsgTypeURL(msg), chainID, tx, ob, fields...), nil
}

// ValidateBundles checks a private msg's bundles together: 1..MaxBundlesPerMsg
// of them, each one's ValidateBasic, nullifiers distinct across all of them,
// and the release map (Remainders) well formed.
func ValidateBundles(msg PrivateMsg) error {
	bs := msg.PrivateBundles()
	if len(bs) < 1 || len(bs) > MaxBundlesPerMsg {
		return errorsmod.Wrapf(ErrInvalidBundle, "a private msg spends %d..%d bundles", 1, MaxBundlesPerMsg)
	}
	seen := map[string]bool{}
	for i, b := range bs {
		if b == nil {
			return errorsmod.Wrapf(ErrInvalidBundle, "bundle %d missing", i)
		}
		if err := b.ValidateBasic(); err != nil {
			return errorsmod.Wrapf(err, "bundle %d", i)
		}
		for _, a := range b.Actions {
			if seen[string(a.Nullifier)] {
				return errorsmod.Wrap(ErrInvalidBundle, "duplicate nullifier across bundles")
			}
			seen[string(a.Nullifier)] = true
		}
	}
	_, err := Remainders(msg)
	return err
}

// ActionCount is how many actions (proofs) msg's bundles carry.
func ActionCount(msg PrivateMsg) int {
	n := 0
	for _, b := range msg.PrivateBundles() {
		n += len(b.Actions)
	}
	return n
}

// Remainder is what is left of one denom's balance once the fee is paid: the
// value the msg releases (to its receiver, or to a module through its
// action).
type Remainder struct {
	Denom  string
	Amount uint64
}

// Remainders is msg's release map after the fee: per denom, the sum of every
// bundle's balance, less PrivateFee for uerth. Only positive remainders, in
// denom order. ErrReleaseMap when the uerth balance cannot cover the fee (a
// fee with nothing to pay it) or a sum overflows.
func Remainders(msg PrivateMsg) ([]Remainder, error) {
	sum := map[string]uint64{}
	for _, b := range msg.PrivateBundles() {
		for _, bal := range b.Balances {
			s, carry := bits.Add64(sum[bal.Denom], bal.Amount, 0)
			if carry != 0 {
				return nil, errorsmod.Wrapf(ErrReleaseMap, "%s balances overflow", bal.Denom)
			}
			sum[bal.Denom] = s
		}
	}
	fee := msg.PrivateFee()
	if sum[FeeDenom] < fee {
		return nil, errorsmod.Wrapf(ErrReleaseMap, "fee %d%s exceeds the bundles' %s balance %d", fee, FeeDenom, FeeDenom, sum[FeeDenom])
	}
	sum[FeeDenom] -= fee
	out := make([]Remainder, 0, len(sum))
	for d, v := range sum {
		if v > 0 {
			out = append(out, Remainder{Denom: d, Amount: v})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Denom < out[j].Denom })
	return out, nil
}

// TotalFee is the whole fee msg's tx pays, PrivateFee as an Int. It is what
// AuthInfo.Fee must declare and what the ante holds to the fee floor and the
// min gas price.
func TotalFee(msg PrivateMsg) math.Int {
	return math.NewIntFromUint64(msg.PrivateFee())
}

var (
	_ PrivateMsg  = (*MsgSend)(nil)
	_ UnshieldMsg = (*MsgSend)(nil)

	_ sdk.HasValidateBasic = (*MsgSend)(nil)
	_ sdk.HasValidateBasic = (*MsgShield)(nil)
	_ sdk.HasValidateBasic = (*MsgRegisterAsset)(nil)
)

// PrivateBundles implements PrivateMsg.
func (m *MsgSend) PrivateBundles() []*Bundle { return []*Bundle{&m.Bundle} }

// PrivateFee implements PrivateMsg.
func (m *MsgSend) PrivateFee() uint64 { return m.Fee }

// UnshieldReceiver implements UnshieldMsg.
func (m *MsgSend) UnshieldReceiver(ac address.Codec) ([]byte, error) {
	if m.Receiver == "" {
		return nil, nil
	}
	bz, err := ac.StringToBytes(m.Receiver)
	if err != nil {
		return nil, errorsmod.Wrapf(ErrInvalidBundle, "receiver: %v", err)
	}
	// The proof binds the bytes, not the string: refuse a re-spelling
	// (uppercase bech32) that would pass as the same tx under another hash.
	if s, err := ac.BytesToString(bz); err != nil || s != m.Receiver {
		return nil, errorsmod.Wrapf(ErrInvalidBundle, "receiver %q is not the canonical encoding of its address", m.Receiver)
	}
	return bz, nil
}

// SighashFields implements PrivateMsg: Bytes(receiver's raw address bytes,
// empty when nothing is unshielded), fee.
func (m *MsgSend) SighashFields(ac address.Codec) ([]fr.Element, error) {
	recv, err := m.UnshieldReceiver(ac)
	if err != nil {
		return nil, err
	}
	return []fr.Element{privacy.Bytes(recv), privacy.U64(m.Fee)}, nil
}

// ValidateBasic runs in baseapp before the ante. The release map: the fee is
// positive and covered by the uerth balance, and a receiver is named exactly
// when something is left for it.
func (m *MsgSend) ValidateBasic() error {
	if err := ValidateBundles(m); err != nil {
		return err
	}
	if m.Fee == 0 {
		return errorsmod.Wrap(ErrReleaseMap, "a private send pays a positive fee")
	}
	rem, err := Remainders(m)
	if err != nil {
		return err
	}
	if (len(rem) == 0) != (m.Receiver == "") {
		return errorsmod.Wrap(ErrReleaseMap, "receiver must be set exactly when the balances exceed the fee")
	}
	return nil
}

// ValidateBasic checks a shield needs no state to refuse.
func (m *MsgShield) ValidateBasic() error {
	if !m.Amount.IsValid() || !m.Amount.IsPositive() {
		return errorsmod.Wrapf(ErrInvalidNote, "amount %s must be a positive valid coin", m.Amount)
	}
	if !FitsNote(m.Amount.Amount) {
		return errorsmod.Wrap(ErrInvalidNote, "a note holds at most 2^63-1")
	}
	if _, err := privacy.FieldFromBytes(m.Pc); err != nil {
		return errorsmod.Wrapf(ErrInvalidNote, "pc: %v", err)
	}
	if err := CheckBlindCiphertext("ciphertext", m.Ciphertext); err != nil {
		return errorsmod.Wrap(ErrInvalidNote, err.Error())
	}
	return nil
}

// ValidateBasic checks the denom is well formed.
func (m *MsgRegisterAsset) ValidateBasic() error {
	return sdk.ValidateDenom(m.Denom)
}

// ValidateFeeOnly checks a private msg whose bundles pay its fee and nothing
// else (personhood and assembly msgs): ValidateBundles, a positive fee, and no
// value released beyond it.
func ValidateFeeOnly(msg PrivateMsg) error {
	if err := ValidateBundles(msg); err != nil {
		return err
	}
	if msg.PrivateFee() == 0 {
		return errorsmod.Wrap(ErrReleaseMap, "the fee bundle pays a positive uerth fee")
	}
	rem, err := Remainders(msg)
	if err != nil {
		return err
	}
	if len(rem) != 0 {
		return errorsmod.Wrap(ErrReleaseMap, "a fee bundle releases nothing beyond its uerth fee")
	}
	return nil
}

// FeeBundleFee is the fee a fee bundle pays: its uerth balance.
func FeeBundleFee(b *Bundle) uint64 { return b.Balance(FeeDenom) }

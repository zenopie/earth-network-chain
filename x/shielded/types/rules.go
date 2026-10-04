package types

import (
	"fmt"
	"math/bits"

	sdkmath "cosmossdk.io/math"

	"github.com/earth-network/earth/zk/privacy"
)

// One note-discovery rule: every note the chain mints to a hidden owner (a
// shield, a module's MintNote: registration rewards, ANML claims, swap
// outputs, LP shares, refunds and payouts, unbonding payouts) carries an
// amount-blind v2 ciphertext (zk/privacy.EncryptBlindNote) supplied by the
// msg that asks for it, and every stake note x/shieldedstaking mints carries
// a blind stake ciphertext (zk/privacy.EncryptBlindStakeNote). The wallet
// finds every note it owns by trial-decrypting ciphertexts and checking the
// cm against the published amount; it keeps no self-mint counter. One
// exception, an open note (Keeper.MintOpenNote: the referral note the chain
// mints to a referrer handle's address): no ciphertext; its shielded_mint
// event carries owner_pk, rho and rcm, and the wallet takes the notes whose
// owner_pk is its own (pc and cm recomputed and checked as usual).
const BlindCiphertextBytes = privacy.BlindNoteCiphertextBytes

// NoteCiphertextBytes is every bundle action's output ciphertext length: a
// v1 note ciphertext (zk/privacy.EncryptNote), dummy outputs included (a
// dummy is encrypted to a throwaway key). One length, so an output's
// ciphertext says nothing about what kind of output it is, and a relayer or
// wallet bug cannot pad or shorten it.
const NoteCiphertextBytes = privacy.NoteCiphertextBytes

// CheckBlindCiphertext refuses a minted note's ciphertext that is not
// exactly BlindCiphertextBytes long. Callers wrap the error in their own.
func CheckBlindCiphertext(what string, ct []byte) error {
	if len(ct) != BlindCiphertextBytes {
		return fmt.Errorf("%s: an amount-blind ciphertext of exactly %d bytes is required, got %d", what, BlindCiphertextBytes, len(ct))
	}
	return nil
}

// MaxNoteValue is the largest note value the chain mints: 2^63-1. The action
// circuit's range is u64, but every wallet (Android Long, iOS Int64, web)
// holds note and stake note values only up to 2^63-1 and ignores a note
// above it (PRIVACY_FORMATS section 3, Amounts), so a chain-minted note
// above it would be invisible to its owner. Every chain mint (MintNote,
// MintOpenNote, a shield, a stake note) refuses a value above it.
//
// MintNoteSplit pays a value above it as ceil(v / MaxNoteValue) notes to one
// pc, each a full MaxNoteValue but the last, at most MaxSplitNotes of them:
// 128 x (2^63-1), about 2^70, the same capacity the 64 x (2^64-1) split had.
// Each note is its own position, so its nullifier H(nk, rho, position)
// differs from its siblings' even when their commitments are equal; the
// owner finds each by its ciphertext (the same one) and the amount published
// at its position.
const (
	MaxNoteValue  = uint64(1)<<63 - 1
	MaxSplitNotes = 128
)

// FitsNote reports whether v is a value the chain mints as one note:
// 1..MaxNoteValue.
func FitsNote(v sdkmath.Int) bool {
	return !v.IsNil() && v.IsPositive() && v.IsUint64() && v.Uint64() <= MaxNoteValue
}

// SplitNoteValues splits v into note values: ceil(v / MaxNoteValue) of
// them, every one MaxNoteValue but the last. It refuses a non-positive v or
// one needing more than MaxSplitNotes notes.
func SplitNoteValues(v sdkmath.Int) ([]uint64, error) {
	if v.IsNil() || !v.IsPositive() {
		return nil, fmt.Errorf("note value %s must be positive", v)
	}
	max := sdkmath.NewIntFromUint64(MaxNoteValue)
	n := v.Add(max).SubRaw(1).Quo(max)
	if !n.IsInt64() || n.Int64() > MaxSplitNotes {
		return nil, fmt.Errorf("value %s needs more than %d notes of at most %d", v, MaxSplitNotes, MaxNoteValue)
	}
	out := make([]uint64, 0, n.Int64())
	for rest := v; rest.IsPositive(); {
		if rest.GT(max) {
			out = append(out, MaxNoteValue)
			rest = rest.Sub(max)
			continue
		}
		out = append(out, rest.Uint64())
		break
	}
	return out, nil
}

// One fee rule: every private msg pays its fee out of its bundles' uerth
// balance. The fee is that balance less the uerth the msg itself moves
// (FeeAfter): a delegation's amount, a swap's amount in when it swaps uerth,
// an LP deposit's ERTH leg, and nothing for every other msg. One exception
// names its fee explicitly: MsgSend, whose uerth beyond the fee is unshielded
// to its receiver.

// UerthBalance is the sum of msg's bundles' uerth balances (saturating; an
// overflow is refused by Remainders).
func UerthBalance(msg PrivateMsg) uint64 {
	var sum uint64
	for _, b := range msg.PrivateBundles() {
		s, carry := bits.Add64(sum, b.Balance(FeeDenom), 0)
		if carry != 0 {
			return ^uint64(0)
		}
		sum = s
	}
	return sum
}

// FeeAfter is msg's fee under the fee rule: its bundles' uerth balance less
// moved, the uerth the msg moves itself (0 when moved exceeds the balance,
// which the release-map checks then refuse).
func FeeAfter(msg PrivateMsg, moved uint64) uint64 {
	b := UerthBalance(msg)
	if moved > b {
		return 0
	}
	return b - moved
}

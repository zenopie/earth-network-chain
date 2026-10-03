package types

import (
	"fmt"
	"math/bits"

	"github.com/earth-network/earth/zk/privacy"
)

// One note-discovery rule: every note the chain mints to a hidden owner (a
// shield, a module's MintNote: registration rewards, ANML claims, swap
// outputs, LP shares, refunds and payouts, unbonding payouts) carries an
// amount-blind v2 ciphertext (zk/privacy.EncryptBlindNote) supplied by the
// msg that asks for it, and every stake note x/shieldedstaking mints carries
// a blind stake ciphertext (zk/privacy.EncryptBlindStakeNote). The wallet
// finds every note it owns by trial-decrypting ciphertexts and checking the
// cm against the published amount; it keeps no self-mint counter.
const BlindCiphertextBytes = privacy.BlindNoteCiphertextBytes

// CheckBlindCiphertext refuses a minted note's ciphertext that is not
// exactly BlindCiphertextBytes long. Callers wrap the error in their own.
func CheckBlindCiphertext(what string, ct []byte) error {
	if len(ct) != BlindCiphertextBytes {
		return fmt.Errorf("%s: an amount-blind ciphertext of exactly %d bytes is required, got %d", what, BlindCiphertextBytes, len(ct))
	}
	return nil
}

// One fee rule: every private msg pays its fee out of its bundles' uerth
// balance. The fee is that balance less the uerth the msg itself moves
// (FeeAfter): a delegation's amount, a swap's amount in when it swaps uerth,
// an LP deposit's ERTH leg, and nothing for every other msg. Two exceptions
// name their fee explicitly: MsgSend (whose uerth beyond the fee is
// unshielded to its receiver) and, paying from its output instead,
// MsgClaimUnbonding (FeeFromOutputMsg).

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

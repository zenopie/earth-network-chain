package types

import (
	"fmt"

	errorsmod "cosmossdk.io/errors"
)

// Referral code format: lowercase [a-z0-9-], ReferralCodeMinLen to
// ReferralCodeMaxLen characters, no leading or trailing dash. One spelling
// per code (no case folding), so the chain and every wallet agree on its
// bytes.
const (
	ReferralCodeMinLen = 3
	ReferralCodeMaxLen = 32

	// ReferralCodeGraceSeconds is how long a code stays reserved to its last
	// holder after its binding lapses, is cleared, or moves to another code.
	ReferralCodeGraceSeconds = 30 * 24 * 60 * 60

	// ReferralCodeSweepLimit bounds the released codes one block deletes.
	ReferralCodeSweepLimit = 200
)

// ValidateReferralCode checks code's format.
func ValidateReferralCode(code string) error {
	if len(code) < ReferralCodeMinLen || len(code) > ReferralCodeMaxLen {
		return errorsmod.Wrapf(ErrInvalidMsg, "referral code must be %d..%d characters", ReferralCodeMinLen, ReferralCodeMaxLen)
	}
	for i := 0; i < len(code); i++ {
		c := code[i]
		ok := (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-'
		if !ok {
			return errorsmod.Wrapf(ErrInvalidMsg, "referral code %q: only a-z, 0-9 and -", code)
		}
	}
	if code[0] == '-' || code[len(code)-1] == '-' {
		return errorsmod.Wrapf(ErrInvalidMsg, "referral code %q: no leading or trailing dash", code)
	}
	return nil
}

// validateReferralCodes checks genesis referral codes: well formed, unique,
// a field-sized nullifier, a release time, and one record per nullifier
// with the latest release (its active code: see the keeper).
func validateReferralCodes(codes []ReferralCode) error {
	seen := map[string]bool{}
	latest := map[string]int64{}
	for _, c := range codes {
		if err := ValidateReferralCode(c.Code); err != nil {
			return err
		}
		if seen[c.Code] {
			return fmt.Errorf("referral code %q listed twice", c.Code)
		}
		seen[c.Code] = true
		if len(c.Nullifier) != 32 {
			return fmt.Errorf("referral code %q: nullifier must be 32 bytes", c.Code)
		}
		if c.ReleasesAt <= 0 {
			return fmt.Errorf("referral code %q: no release time", c.Code)
		}
		if at, ok := latest[string(c.Nullifier)]; ok && at == c.ReleasesAt {
			return fmt.Errorf("referral code %q: two codes of one nullifier release at the same time", c.Code)
		}
		if c.ReleasesAt > latest[string(c.Nullifier)] {
			latest[string(c.Nullifier)] = c.ReleasesAt
		}
	}
	return nil
}

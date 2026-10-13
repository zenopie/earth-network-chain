package types

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	errorsmod "cosmossdk.io/errors"
)

// Stake vintages (v1.3.0, ORCHARD_DESIGN.md 8.9).
//
// A stake note's asset names its validator and its vintage: the UTC month
// (YYYYMM) its stake was last checked in (delegated, credited by a
// redelegation, or moved by MsgCheckIn). The denom is
//
//	derth/<valoper>/<YYYYMM>
//
// except for the base vintage (the v1.3.0 upgrade month on earth-1; a new
// chain's genesis month), whose denom is the unsuffixed derth/<valoper> that
// every note made before v1.3.0 carries in its commitment. Exactly one denom
// per (validator, vintage): VintageDenom is the only spelling the chain
// supplies to a proof.
//
// A vintage is live in its own month and the 12 after it, and retires at
// 00:00 UTC on the 1st of its month + 13 (VintageRetiresAt).

// LiveVintageMonths is how many vintages are live at once: the current
// month and the 12 before it.
const LiveVintageMonths = 13

// MinVintageYear and MaxVintageYear bound a vintage's year.
const (
	MinVintageYear = 2000
	MaxVintageYear = 9999
)

// MaxUnbondingSeconds bounds x/staking's unbonding_time while vintages
// retire (enforced on x/staking's MsgUpdateParams and at genesis): a
// redelegation's label window (unbonding_time + MoveTimeSlackSeconds) then
// closes before the credit's vintage can retire (at least 365 days after the
// credit), so no slash debt can reach a retired vintage through an open
// label, and every retired note can clear its label to withdraw.
const MaxUnbondingSeconds = 364 * 24 * 3600

// ValidateVintage refuses anything but a YYYYMM month in range.
func ValidateVintage(m uint32) error {
	y, mo := m/100, m%100
	if y < MinVintageYear || y > MaxVintageYear || mo < 1 || mo > 12 {
		return errorsmod.Wrapf(ErrVintage, "%d is not a vintage (YYYYMM)", m)
	}
	return nil
}

// VintageIndex is m as months since year 0 (comparable, subtractable).
func VintageIndex(m uint32) int64 { return int64(m/100)*12 + int64(m%100) - 1 }

// VintageFromIndex is VintageIndex's inverse.
func VintageFromIndex(i int64) uint32 {
	if i < 0 {
		return 0
	}
	return uint32(i/12)*100 + uint32(i%12) + 1
}

// AddMonths is m + n months (n may be negative).
func AddMonths(m uint32, n int64) uint32 { return VintageFromIndex(VintageIndex(m) + n) }

// MonthOf is t's UTC month as a vintage.
func MonthOf(t time.Time) uint32 {
	t = t.UTC()
	return uint32(t.Year())*100 + uint32(t.Month())
}

// VintageStart is 00:00 UTC on the 1st of m.
func VintageStart(m uint32) time.Time {
	return time.Date(int(m/100), time.Month(m%100), 1, 0, 0, 0, 0, time.UTC)
}

// VintageRetiresAt is when m retires: 00:00 UTC on the 1st of m + 13.
func VintageRetiresAt(m uint32) time.Time {
	return VintageStart(AddMonths(m, LiveVintageMonths))
}

// OldestLiveVintage is the oldest vintage live in month cur: cur - 12.
func OldestLiveVintage(cur uint32) uint32 { return AddMonths(cur, -(LiveVintageMonths - 1)) }

// VintageLive reports whether m is live in month cur: cur - 12 <= m <= cur.
func VintageLive(m, cur uint32) bool {
	i, c := VintageIndex(m), VintageIndex(cur)
	return i <= c && i > c-LiveVintageMonths
}

// VintageRetired reports whether m has retired by month cur.
func VintageRetired(m, cur uint32) bool {
	return VintageIndex(m) <= VintageIndex(cur)-LiveVintageMonths
}

// VintageDenom is the stake denom of validator's vintage m: the unsuffixed
// derth/<valoper> for the base vintage, derth/<valoper>/<YYYYMM> for every
// other.
func VintageDenom(valoper string, m, base uint32) string {
	if m == base {
		return DerthPrefix + valoper
	}
	return fmt.Sprintf("%s%s/%06d", DerthPrefix, valoper, m)
}

// ParseDerthDenom returns a derth denom's validator and vintage: month is
// 0 for the unsuffixed form (the base vintage's). Both forms parse; anything
// else (an empty validator, a suffix that is not a vintage) does not.
func ParseDerthDenom(denom string) (valoper string, month uint32, ok bool) {
	rest, ok := strings.CutPrefix(denom, DerthPrefix)
	if !ok || rest == "" {
		return "", 0, false
	}
	v, suffix, suffixed := strings.Cut(rest, "/")
	if v == "" {
		return "", 0, false
	}
	if !suffixed {
		return v, 0, true
	}
	if len(suffix) != 6 || strings.Contains(suffix, "/") {
		return "", 0, false
	}
	n, err := strconv.ParseUint(suffix, 10, 32)
	if err != nil || ValidateVintage(uint32(n)) != nil {
		return "", 0, false
	}
	return v, uint32(n), true
}

// VintageCtx is what resolves a msg's vintages to assets: the base vintage
// (the unsuffixed denom's) and the current month (the vintage a delegation,
// a redelegation's credit or a check-in lands in).
type VintageCtx struct {
	Base, Current uint32
	// Grace is the previous month during the first VintageGraceSeconds of
	// the current one (0 otherwise): a credit naming it is still accepted.
	Grace uint32
}

// VintageGraceSeconds is how long into a month a credit (a delegation, a
// redelegation's credit lane, a check-in) may still name the previous
// month: a proof built against a block before 00:00 UTC on the 1st and
// included after it. It equals ClearBeforeSlackSeconds, the longest a
// stake proof stays valid after the block it was built against (its
// clear_before must be within that of the current one), so every proof
// still valid otherwise is valid for its vintage too. The credit lands in
// the previous month's vintage, which is live for 12 more months.
const VintageGraceSeconds = ClearBeforeSlackSeconds

// NewVintageCtx is the vintage context at block time t.
func NewVintageCtx(base uint32, t time.Time) VintageCtx {
	vc := VintageCtx{Base: base, Current: MonthOf(t)}
	if t.Unix()-VintageStart(vc.Current).Unix() < VintageGraceSeconds {
		vc.Grace = AddMonths(vc.Current, -1)
	}
	return vc
}

// Credit resolves a credit_vintage field: 0 names the current month.
func (vc VintageCtx) Credit(m uint32) uint32 {
	if m == 0 {
		return vc.Current
	}
	return m
}

// CheckCredit refuses a credit vintage other than the current month, or the
// previous one during the grace (ErrVintage).
func (vc VintageCtx) CheckCredit(m uint32) error {
	if m == vc.Current || (vc.Grace != 0 && m == vc.Grace) {
		return nil
	}
	if vc.Grace != 0 {
		return ErrVintage.Wrapf("a credit lands in the current vintage %d (or %d until %ds into the month), not %d", vc.Current, vc.Grace, VintageGraceSeconds, m)
	}
	return ErrVintage.Wrapf("a credit lands in the current vintage %d, not %d", vc.Current, m)
}

// Resolve is a msg's vintage field: 0 names the base vintage.
func (vc VintageCtx) Resolve(m uint32) uint32 {
	if m == 0 {
		return vc.Base
	}
	return m
}

// Denom is valoper's denom for the msg vintage m (0: the base vintage).
func (vc VintageCtx) Denom(valoper string, m uint32) string {
	return VintageDenom(valoper, vc.Resolve(m), vc.Base)
}

// checkMsgVintage: a msg's vintage field is 0 (the base vintage) or a
// vintage.
func checkMsgVintage(m uint32) error {
	if m == 0 {
		return nil
	}
	return ValidateVintage(m)
}

package keeper

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Moving wallets used to cost you that day's ANML.
//
// A switch rebuilt the registration from scratch, which set LastAnmlClaim to
// today's midnight — the value a brand new registration gets so that its first
// claim opens tomorrow. Applied to someone who had been registered for months
// and had not yet claimed today, it read as "already claimed today" and took
// the day. The fix is to carry the clock across, and the thing worth pinning is
// that carrying it cannot be turned into a second claim.
func TestAnmlClockFor(t *testing.T) {
	const day = int64(86400)
	now := 20000*day + 15*3600 // day 20000, 15:00 UTC

	cases := []struct {
		name    string
		carried int64
		want    int64
		// claimableToday describes what ClaimAnml will decide with `want`:
		// it refuses when the stored day equals the current day.
		claimableToday bool
	}{
		{
			name:           "a new registration starts at today's midnight",
			carried:        0,
			want:           20000 * day,
			claimableToday: false,
		},
		{
			name:           "a switch by someone who claimed yesterday keeps yesterday",
			carried:        19999*day + 9*3600,
			want:           19999*day + 9*3600,
			claimableToday: true,
		},
		{
			name:           "a switch by someone who already claimed today keeps today",
			carried:        20000*day + 8*3600,
			want:           20000*day + 8*3600,
			claimableToday: false,
		},
		{
			name:           "a switch by someone who has never claimed keeps their start day",
			carried:        19990 * day,
			want:           19990 * day,
			claimableToday: true,
		},
		{
			name:           "a registration imported without the field falls back",
			carried:        0,
			want:           20000 * day,
			claimableToday: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := anmlClockFor(now, tc.carried)
			require.Equal(t, tc.want, got)
			require.Equal(t, tc.claimableToday, now/day != got/day,
				"claim eligibility today")
		})
	}
}

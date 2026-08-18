package keeper_test

import (
	"testing"
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"github.com/earth-network/earth/x/personhood/keeper"
	"github.com/earth-network/earth/x/personhood/types"
)

// The claim window is a UTC calendar day, not a rolling 24 hours.
//
// Worth a test of its own because the two are indistinguishable in most cases
// and differ exactly where it matters: a claim late in the day. Under a rolling
// window that claim sets the next one 24 hours out, so a day claimed late drags
// every following day later and the daily claim becomes something you have to
// be punctual about. This pins the boundary behaviour so that cannot come back.
func TestClaimAnmlResetsAtUtcMidnight(t *testing.T) {
	const day = int64(86400)

	// Day 20000 at 23:00 UTC — an hour before the boundary.
	claimedAt := 20000*day + 23*3600

	cases := []struct {
		name      string
		at        int64
		claimable bool
	}{
		{"ten minutes later, same day", claimedAt + 600, false},
		{"one minute before midnight", 20001*day - 60, false},
		{"midnight exactly", 20001 * day, true},
		{"one minute after midnight", 20001*day + 60, true},
		// The rolling rule would have refused this: it is only 1h01m after the
		// claim, but it is a different UTC day.
		{"an hour after the claim, next day", claimedAt + 3660, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := initFixture(t)
			sdkCtx := sdk.UnwrapSDKContext(f.ctx)
			ms := keeper.NewMsgServerImpl(f.keeper)

			addr := sdk.AccAddress([]byte("claimant___________1"))
			addrStr, err := f.addressCodec.BytesToString(addr)
			require.NoError(t, err)
			nullifier := []byte("nullifier-claim-test")

			require.NoError(t, f.keeper.Registrations.Set(sdkCtx, nullifier, types.Registration{
				Nullifier:     nullifier,
				Address:       addrStr,
				RegisteredAt:  claimedAt - day,
				DscKey:        []byte("dsc"),
				Country:       "NZ",
				LastAnmlClaim: claimedAt,
			}))
			require.NoError(t, f.keeper.RegByAddr.Set(sdkCtx, addr.Bytes(), nullifier))

			ctx := sdkCtx.WithBlockTime(time.Unix(tc.at, 0).UTC())
			_, err = ms.ClaimAnml(ctx, &types.MsgClaimAnml{Creator: addrStr})

			if tc.claimable {
				require.NoError(t, err, "should be claimable")
			} else {
				require.ErrorIs(t, err, types.ErrClaimTooSoon)
			}
		})
	}
}

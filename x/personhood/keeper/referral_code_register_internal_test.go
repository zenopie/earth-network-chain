package keeper

import (
	"testing"
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	personhoodtest "github.com/earth-network/earth/x/personhood/testutil"
	"github.com/earth-network/earth/x/personhood/types"
	"github.com/earth-network/earth/zk/privacy"
)

// D1 names its referrer by referral code. Refused while no live binding
// holds the code; accepted once one does, resolving to that binding's
// address (the payout's); swapping the code, or naming the address
// instead, breaks the passport binding.
func TestRegisterByReferralCode(t *testing.T) {
	k, ctx := regKeeper(t, stubPki{pubkey: dscKeyOf(t, "D1")})
	ctx = ctx.WithBlockTime(time.Date(2025, 1, 5, 12, 0, 0, 0, time.UTC))
	m := passportMsg(t, "D1")
	m.AffiliateCode, m.Affiliate = personhoodtest.Registrations["D1"].ReferrerCode, ""
	_, err := checkAndVerify(k, ctx, m)
	require.ErrorIs(t, err, types.ErrUnknownReferralCode)

	aAddr := personhoodtest.ReferralAddress("A")
	nf := privacy.FieldBytes(personhoodtest.Det("referrer-nf", 0))
	exp := ctx.BlockTime().Unix() + 60
	require.NoError(t, k.putReferrerBinding(ctx, types.ReferrerBinding{Nullifier: nf, Address: bech(t, aAddr), ExpiresAt: exp}, aAddr))
	_, err = k.bindCode(ctx, nf, "alice", exp)
	require.NoError(t, err)
	p, err := checkAndVerify(k, ctx, m)
	require.NoError(t, err)
	require.Equal(t, aAddr, []byte(p.affiliate), "paid to the code's bound address")

	swapped := *m
	swapped.AffiliateCode = "alice2"
	other := personhoodtest.ReferralAddress("other")
	otherNf := privacy.FieldBytes(personhoodtest.Det("other-nf", 0))
	require.NoError(t, k.putReferrerBinding(ctx, types.ReferrerBinding{Nullifier: otherNf, Address: bech(t, other), ExpiresAt: exp}, other))
	_, err = k.bindCode(ctx, otherNf, "alice2", exp)
	require.NoError(t, err)
	_, err = checkAndVerify(k, ctx, &swapped)
	require.ErrorIs(t, err, types.ErrBadPublicInputs, "another code")
	byAddr := *m
	byAddr.AffiliateCode, byAddr.Affiliate = "", bech(t, aAddr)
	_, err = checkAndVerify(k, ctx, &byAddr)
	require.ErrorIs(t, err, types.ErrBadPublicInputs, "the address form of the same referrer")

	// Lapsed: refused.
	_, err = checkAndVerify(k, ctx.WithBlockTime(time.Unix(exp+1, 0)), m)
	require.ErrorIs(t, err, types.ErrUnknownReferralCode)
}

func bech(t *testing.T, b []byte) string {
	t.Helper()
	return sdk.AccAddress(b).String()
}

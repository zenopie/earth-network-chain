package types

import (
	"strings"
	"testing"

	addresscodec "github.com/cosmos/cosmos-sdk/codec/address"
	"github.com/stretchr/testify/require"
)

// The registration's affiliate and a referrer binding's address are bound
// by their bytes: an uppercase re-spelling is refused.
func TestAddressFieldsCanonical(t *testing.T) {
	ac := addresscodec.NewBech32Codec("earth")
	addr, err := ac.BytesToString(make([]byte, 20))
	require.NoError(t, err)
	_, err = AffiliateField(ac, addr, "")
	require.NoError(t, err)
	_, err = AffiliateField(ac, strings.ToUpper(addr), "")
	require.ErrorContains(t, err, "canonical")
	_, err = (&MsgBindReferrer{Address: strings.ToUpper(addr)}).SighashFields(ac)
	require.ErrorContains(t, err, "canonical")
	_, err = (&MsgBindReferrer{Address: addr}).SighashFields(ac)
	require.NoError(t, err)
}

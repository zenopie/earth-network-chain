package types

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/earth-network/earth/zk/privacy"
)

// A handle's shielded address is bound by its bytes, and its text must be
// canonical: an uppercase re-spelling is refused. A bind names a handle and
// an address, a release neither.
func TestHandleAddressCanonical(t *testing.T) {
	addr := privacy.ShieldedAddress{OwnerPK: privacy.U64(7)}.Encode()
	_, err := (&MsgBindHandle{Handle: "alice", Address: strings.ToUpper(addr)}).SighashFields(nil)
	require.ErrorContains(t, err, "canonical")
	f, err := (&MsgBindHandle{Handle: "alice", Address: addr}).SighashFields(nil)
	require.NoError(t, err)
	require.Len(t, f, 3)
	require.ErrorContains(t, (&MsgBindHandle{Handle: "alice"}).ValidateBasic(), "a bind names a handle and an address")
	require.ErrorContains(t, (&MsgBindHandle{Address: addr}).ValidateBasic(), "a bind names a handle and an address")
	require.ErrorContains(t, (&MsgBindHandle{Handle: "Alice", Address: addr}).ValidateBasic(), "only a-z")
}

// A registration's affiliate: none, or all three of handle, pc and
// ciphertext; the field differs from the none case and binds each.
func TestRegisterAffiliateField(t *testing.T) {
	none, err := (&MsgRegister{}).AffiliateField()
	require.NoError(t, err)
	require.True(t, none.IsZero())
	ct := make([]byte, 177)
	ct[0] = 1
	pc := privacy.FieldBytes(privacy.U64(5))
	full := &MsgRegister{AffiliateHandle: "alice", AffiliatePc: pc, AffiliateCiphertext: ct}
	f, err := full.AffiliateField()
	require.NoError(t, err)
	require.False(t, f.IsZero())
	for _, m := range []*MsgRegister{
		{AffiliateHandle: "alice"},
		{AffiliatePc: pc, AffiliateCiphertext: ct},
		{AffiliateHandle: "alice", AffiliatePc: pc},
	} {
		_, err := m.AffiliateField()
		require.Error(t, err)
	}
	other := *full
	other.AffiliateHandle = "bob"
	g, err := other.AffiliateField()
	require.NoError(t, err)
	require.NotEqual(t, f, g)
}

func TestHandleFormat(t *testing.T) {
	for _, ok := range []string{"abc", "alice-2", "a1b2c3", "x-y-z", "0123456789abcdefghijklmnopqrstuv"} {
		require.NoError(t, ValidateHandle(ok), ok)
	}
	for _, bad := range []string{"", "ab", "Alice", "-abc", "abc-", "a_b", "a b", "ålice", "0123456789abcdefghijklmnopqrstuvw"} {
		require.Error(t, ValidateHandle(bad), bad)
	}
}

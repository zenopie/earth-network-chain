package types

import (
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"
)

func TestTypeURLs(t *testing.T) {
	for want, m := range map[string]sdk.Msg{
		TypeMsgDelegate: &MsgDelegate{}, TypeMsgUndelegate: &MsgUndelegate{}, TypeMsgClaimUnbonding: &MsgClaimUnbonding{},
		TypeMsgStakeVote: &MsgStakeVote{}, TypeMsgLockPosition: &MsgLockPosition{}, TypeMsgUpdatePosition: &MsgUpdatePosition{},
		TypeMsgUnlockPosition: &MsgUnlockPosition{}, TypeMsgPositionVote: &MsgPositionVote{},
	} {
		require.Equal(t, want, sdk.MsgTypeURL(m))
	}
}

func TestDenoms(t *testing.T) {
	v, e, ok := ParseUnbondDenom(UnbondDenom("earthvaloper1abc", 42))
	require.True(t, ok)
	require.Equal(t, "earthvaloper1abc", v)
	require.EqualValues(t, 42, e)
	v, ok = ParseDerthDenom(DerthDenom("earthvaloper1abc"))
	require.True(t, ok)
	require.Equal(t, "earthvaloper1abc", v)
	_, ok = ParseDerthDenom("derth/a/b")
	require.False(t, ok)
	id, ok := ParsePositionVoterKey(PositionVoterKey(1<<40 + 7))
	require.True(t, ok)
	require.EqualValues(t, 1<<40+7, id)
	require.Len(t, PositionVoterKey(1), 17)
	require.NoError(t, sdk.ValidateDenom(UnbondDenom("earthvaloper1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq", ^uint64(0))))
}

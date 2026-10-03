package types

import (
	"github.com/cosmos/cosmos-sdk/types/bech32"
	"strings"
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
	vk := ValidatorVoterKey(make([]byte, 20))
	require.Len(t, vk, 26)
	require.True(t, IsValidatorVoterKey(vk))
	require.True(t, IsValidatorVoterKey(ValidatorVoterKey(make([]byte, 32))))
	require.False(t, IsValidatorVoterKey(make([]byte, 20)))
	require.False(t, IsValidatorVoterKey(make([]byte, 32)))
	require.NoError(t, sdk.ValidateDenom(UnbondDenom("earthvaloper1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq", ^uint64(0))))
}

func TestCanonicalValoper(t *testing.T) {
	hrp := sdk.GetConfig().GetBech32ValidatorAddrPrefix()
	canon, err := bech32.ConvertAndEncode(hrp, make([]byte, 20))
	require.NoError(t, err)
	require.NoError(t, CanonicalValoper(canon))
	require.Error(t, CanonicalValoper(strings.ToUpper(canon)))
	require.Error(t, CanonicalValoper(""))
	other, _ := bech32.ConvertAndEncode("other", make([]byte, 20))
	require.Error(t, CanonicalValoper(other))
	short, _ := bech32.ConvertAndEncode(hrp, make([]byte, 5))
	require.Error(t, CanonicalValoper(short))
	require.Error(t, (&MsgDelegate{Validator: strings.ToUpper(canon)}).ValidateBasic())
}

package types

import (
	"github.com/cosmos/cosmos-sdk/types/bech32"
	"strings"
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"github.com/earth-network/earth/zk/privacy"
)

func TestTypeURLs(t *testing.T) {
	for want, m := range map[string]sdk.Msg{
		TypeMsgDelegate: &MsgDelegate{}, TypeMsgUndelegate: &MsgUndelegate{},
		TypeMsgStakeVote: &MsgStakeVote{}, TypeMsgLockPosition: &MsgLockPosition{}, TypeMsgUpdatePosition: &MsgUpdatePosition{},
		TypeMsgUnlockPosition: &MsgUnlockPosition{}, TypeMsgPositionVote: &MsgPositionVote{},
		TypeMsgRedelegate: &MsgRedelegate{},
	} {
		require.Equal(t, want, sdk.MsgTypeURL(m))
	}
}

func TestDenoms(t *testing.T) {
	v, ok := ParseDerthDenom(DerthDenom("earthvaloper1abc"))
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

// A stake proof carries exactly two ciphertext slots: empty for a zero
// commitment, exactly a wallet stake ciphertext (153 bytes) for a created
// note.
func TestStakeProofCiphertextShape(t *testing.T) {
	z := make([]byte, 32)
	cm := make([]byte, 32)
	cm[31] = 7
	ct := make([]byte, privacy.WalletStakeCiphertextBytes)
	base := func() StakeProof {
		return StakeProof{Proof: make([]byte, 14_656), Anchor: z, SpcMint: z, OwnerTag: z,
			Nullifiers: [][]byte{z, z}, Commitments: [][]byte{cm, z}, Ciphertexts: [][]byte{ct, nil}}
	}
	ok := base()
	require.NoError(t, ok.ValidateBasic())
	for name, mutate := range map[string]func(p *StakeProof){
		"one slot":           func(p *StakeProof) { p.Ciphertexts = p.Ciphertexts[:1] },
		"three slots":        func(p *StakeProof) { p.Ciphertexts = append(p.Ciphertexts, nil) },
		"missing ct":         func(p *StakeProof) { p.Ciphertexts[0] = nil },
		"ct for zero cm":     func(p *StakeProof) { p.Ciphertexts[1] = ct },
		"ct a byte short":    func(p *StakeProof) { p.Ciphertexts[0] = ct[:len(ct)-1] },
		"ct a byte long":     func(p *StakeProof) { p.Ciphertexts[0] = append(append([]byte(nil), ct...), 0) },
		"blind (177) length": func(p *StakeProof) { p.Ciphertexts[0] = make([]byte, privacy.BlindStakeCiphertextBytes) },
	} {
		p := base()
		mutate(&p)
		require.Error(t, p.ValidateBasic(), name)
	}
}

// MsgRedelegate's sighash binds the stake fields, then Bytes(src),
// Bytes(dst), amount; its stake denom is the source's derth and v_out the
// amount. Two of the same validator, or nothing to move, are refused before
// the bundle is looked at.
func TestMsgRedelegateFields(t *testing.T) {
	hrp := sdk.GetConfig().GetBech32ValidatorAddrPrefix()
	a, _ := bech32.ConvertAndEncode(hrp, make([]byte, 20))
	b, _ := bech32.ConvertAndEncode(hrp, append(make([]byte, 19), 1))
	z := make([]byte, 32)
	m := &MsgRedelegate{SrcValidator: a, DstValidator: b, Amount: 7,
		Stake: StakeProof{Anchor: z, SpcMint: z, OwnerTag: z, Nullifiers: [][]byte{z, z}, Commitments: [][]byte{z, z}}}
	fs, err := m.SighashFields(nil)
	require.NoError(t, err)
	stake := m.Stake.StakeFields()
	require.Len(t, fs, len(stake)+3)
	require.Equal(t, stake, fs[:len(stake)])
	require.Equal(t, privacy.Bytes([]byte(a)), fs[len(stake)])
	require.Equal(t, privacy.Bytes([]byte(b)), fs[len(stake)+1])
	require.Equal(t, privacy.U64(7), fs[len(stake)+2])
	require.Equal(t, DerthDenom(a), m.StakeDenom())
	require.Equal(t, uint64(7), m.VOut())

	same := *m
	same.DstValidator = a
	require.ErrorIs(t, same.ValidateBasic(), ErrRedelegation)
	zero := *m
	zero.Amount = 0
	require.ErrorContains(t, zero.ValidateBasic(), "amount must be positive")
	upper := *m
	upper.DstValidator = strings.ToUpper(b)
	require.Error(t, upper.ValidateBasic())
}

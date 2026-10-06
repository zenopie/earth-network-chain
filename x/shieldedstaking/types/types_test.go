package types

import (
	"strings"
	"testing"

	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	"github.com/cosmos/cosmos-sdk/types/bech32"

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

func el32(b byte) []byte {
	out := make([]byte, 32)
	out[31] = b
	return out
}

// A stake proof: one encoding per msg. A ciphertext exactly for a created
// note (201 bytes, label fields inside), none for a zero commitment; the
// debt root zero unless the proof may clear a label; its nullifiers
// distinct across both lanes.
func TestStakeProofForm(t *testing.T) {
	z := make([]byte, 32)
	ct := make([]byte, privacy.WalletStakeCiphertextBytes)
	require.Equal(t, 201, privacy.WalletStakeCiphertextBytes)
	base := func() StakeProof {
		return StakeProof{Proof: make([]byte, 14_656), Anchor: z, OwnerTag: z, DebtRoot: z,
			Nullifiers: [][]byte{el32(1), z}, Commitment: el32(7), Ciphertext: ct,
			CreditNullifier: z, CreditCommitment: z}
	}
	ok := base()
	require.NoError(t, ok.ValidateBasic())
	clearing := base()
	clearing.ClearBefore, clearing.DebtRoot = 5, el32(9)
	require.NoError(t, clearing.ValidateBasic())
	for name, mutate := range map[string]func(p *StakeProof){
		"one nullifier":          func(p *StakeProof) { p.Nullifiers = p.Nullifiers[:1] },
		"missing ct":             func(p *StakeProof) { p.Ciphertext = nil },
		"ct for zero cm":         func(p *StakeProof) { p.Commitment = z },
		"ct a byte short":        func(p *StakeProof) { p.Ciphertext = ct[:len(ct)-1] },
		"ct a byte long":         func(p *StakeProof) { p.Ciphertext = append(append([]byte(nil), ct...), 0) },
		"old wallet ct (153)":    func(p *StakeProof) { p.Ciphertext = make([]byte, 153) },
		"credit cm without ct":   func(p *StakeProof) { p.CreditCommitment = el32(8) },
		"debt root, no clearing": func(p *StakeProof) { p.DebtRoot = el32(9) },
		"debt root missing":      func(p *StakeProof) { p.DebtRoot = nil },
		"repeated nullifier":     func(p *StakeProof) { p.Nullifiers[1] = el32(1) },
		"credit repeats lane A":  func(p *StakeProof) { p.CreditNullifier = el32(1) },
	} {
		p := base()
		mutate(&p)
		require.Error(t, p.ValidateBasic(), name)
	}
}

// Every msg of a kind has one shape: lane A spends in both slots (notes or
// padding) and
// creates (the merged note, the change or a zero note); the credit lane is
// used exactly by a redelegation; position updates and votes use nothing.
func TestStakeProofShape(t *testing.T) {
	z := make([]byte, 32)
	ct := make([]byte, privacy.WalletStakeCiphertextBytes)
	notes := StakeProof{Proof: make([]byte, 14_656), Anchor: z, OwnerTag: z, DebtRoot: z,
		Nullifiers: [][]byte{el32(1), el32(3)}, Commitment: el32(7), Ciphertext: ct, CreditNullifier: z, CreditCommitment: z}
	require.NoError(t, notes.shape(true, false))
	oneSlot := notes
	oneSlot.Nullifiers = [][]byte{el32(1), z}
	require.Error(t, oneSlot.shape(true, false), "the second slot is always spent or padded (audit C-2)")
	require.Error(t, notes.shape(false, false), "a position msg spends nothing")
	require.Error(t, notes.shape(true, true), "the credit lane is required")
	noSpend := notes
	noSpend.Nullifiers = [][]byte{z, z}
	require.Error(t, noSpend.shape(true, false), "a first delegation pads its input")
	noOut := notes
	noOut.Commitment, noOut.Ciphertext = z, nil
	require.Error(t, noOut.shape(true, false), "a full exit pads its output")
	credit := notes
	credit.CreditNullifier, credit.CreditCommitment, credit.CreditCiphertext = el32(2), el32(8), ct
	require.NoError(t, credit.shape(true, true))
	require.Error(t, credit.shape(true, false), "only a redelegation uses the credit lane")
	pos := StakeProof{Proof: make([]byte, 14_656), Anchor: z, OwnerTag: z, DebtRoot: z,
		Nullifiers: [][]byte{z, z}, Commitment: z, CreditNullifier: z, CreditCommitment: z}
	require.NoError(t, pos.shape(false, false))
}

// The stake circuit's public inputs, in order: anchor, asset, nf_0, nf_1,
// cm_out, v_in, v_out, clear_before, debt_root, cr_asset, cr_nf, cr_cm,
// cr_v_in, cr_move_time, owner_tag, sighash.
func TestStakePublicInputs(t *testing.T) {
	p := StakeProof{Anchor: el32(1), Nullifiers: [][]byte{el32(2), el32(3)}, Commitment: el32(4), ClearBefore: 5,
		DebtRoot: el32(6), CreditNullifier: el32(7), CreditCommitment: el32(8), OwnerTag: el32(9)}
	l := StakeLanes{Denom: "derth/a", VIn: 10, VOut: 11, CreditDenom: "derth/b", CreditIn: 12, CreditMoveTime: 13}
	var sig fr.Element
	sig.SetUint64(14)
	pub := p.PublicInputs(l, sig)
	require.Len(t, pub, StakeProofInputs)
	u := func(v uint64) []byte { return privacy.FieldBytes(privacy.U64(v)) }
	require.Equal(t, [][]byte{
		el32(1), privacy.FieldBytes(privacy.AssetID("derth/a")), el32(2), el32(3), el32(4), u(10), u(11), u(5), el32(6),
		privacy.FieldBytes(privacy.AssetID("derth/b")), el32(7), el32(8), u(12), u(13), el32(9), u(14),
	}, pub)
}

// MsgRedelegate's sighash binds the stake fields, then Bytes(src),
// Bytes(dst), amount, dst_derth, move_time; its lanes are the source's derth
// (amount leaving) and the destination's (dst_derth credited, labelled at
// move_time); its move key is the credit nullifier.
func TestMsgRedelegateFields(t *testing.T) {
	hrp := sdk.GetConfig().GetBech32ValidatorAddrPrefix()
	a, _ := bech32.ConvertAndEncode(hrp, make([]byte, 20))
	b, _ := bech32.ConvertAndEncode(hrp, append(make([]byte, 19), 1))
	z := make([]byte, 32)
	m := &MsgRedelegate{SrcValidator: a, DstValidator: b, Amount: 7, DstDerth: 6, MoveTime: 1_700_000_000,
		Stake: StakeProof{Anchor: z, OwnerTag: z, DebtRoot: z, Nullifiers: [][]byte{z, z}, Commitment: z,
			CreditNullifier: el32(3), CreditCommitment: z}}
	fs, err := m.SighashFields(nil)
	require.NoError(t, err)
	stake := m.Stake.StakeFields()
	require.Len(t, fs, len(stake)+5)
	require.Equal(t, stake, fs[:len(stake)])
	require.Equal(t, privacy.Bytes([]byte(a)), fs[len(stake)])
	require.Equal(t, privacy.Bytes([]byte(b)), fs[len(stake)+1])
	require.Equal(t, privacy.U64(7), fs[len(stake)+2])
	require.Equal(t, privacy.U64(6), fs[len(stake)+3])
	require.Equal(t, privacy.U64(1_700_000_000), fs[len(stake)+4])
	require.Equal(t, StakeLanes{Denom: DerthDenom(a), VOut: 7, CreditDenom: DerthDenom(b), CreditIn: 6, CreditMoveTime: 1_700_000_000},
		m.StakeLanes())
	require.Equal(t, el32(3), m.MoveKey())

	same := *m
	same.DstValidator = a
	require.ErrorIs(t, same.ValidateBasic(), ErrRedelegation)
	zero := *m
	zero.Amount = 0
	require.ErrorContains(t, zero.ValidateBasic(), "amount must be positive")
	noDerth := *m
	noDerth.DstDerth = 0
	require.ErrorContains(t, noDerth.ValidateBasic(), "dst_derth must be positive")
	noTime := *m
	noTime.MoveTime = 0
	require.ErrorContains(t, noTime.ValidateBasic(), "move_time must be positive")
	upper := *m
	upper.DstValidator = strings.ToUpper(b)
	require.Error(t, upper.ValidateBasic())
}

// MsgDelegate credits the derth it names (v_in) to derth/<validator>; the
// sighash binds amount and derth.
func TestMsgDelegateFields(t *testing.T) {
	hrp := sdk.GetConfig().GetBech32ValidatorAddrPrefix()
	a, _ := bech32.ConvertAndEncode(hrp, make([]byte, 20))
	z := make([]byte, 32)
	m := &MsgDelegate{Validator: a, Amount: 9, Derth: 8,
		Stake: StakeProof{Anchor: z, OwnerTag: z, DebtRoot: z, Nullifiers: [][]byte{z, z}, Commitment: z,
			CreditNullifier: z, CreditCommitment: z}}
	fs, err := m.SighashFields(nil)
	require.NoError(t, err)
	stake := m.Stake.StakeFields()
	require.Equal(t, []fr.Element{privacy.Bytes([]byte(a)), privacy.U64(9), privacy.U64(8)}, fs[len(stake):])
	require.Equal(t, StakeLanes{Denom: DerthDenom(a), VIn: 8}, m.StakeLanes())
	noDerth := *m
	noDerth.Derth = 0
	require.ErrorContains(t, noDerth.ValidateBasic(), "derth must be positive")
}

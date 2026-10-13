package types

import (
	"testing"
	"time"

	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/bech32"
	"github.com/stretchr/testify/require"

	allocationtypes "github.com/earth-network/earth/x/allocation/types"
	"github.com/earth-network/earth/zk/privacy"
)

func TestVintageMonths(t *testing.T) {
	require.Equal(t, uint32(202610), MonthOf(time.Date(2026, 10, 31, 23, 59, 59, 0, time.UTC)))
	require.Equal(t, uint32(202611), MonthOf(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)))
	// UTC, whatever the time's zone.
	require.Equal(t, uint32(202611), MonthOf(time.Date(2026, 10, 31, 23, 0, 0, 0, time.FixedZone("x", -3600))))
	require.Equal(t, uint32(202701), AddMonths(202612, 1))
	require.Equal(t, uint32(202512), AddMonths(202612, -12))
	require.Equal(t, uint32(202711), AddMonths(202610, 13))
	require.Equal(t, int64(1), VintageIndex(202701)-VintageIndex(202612))
	require.Equal(t, time.Date(2027, 11, 1, 0, 0, 0, 0, time.UTC), VintageRetiresAt(202610))
	require.Equal(t, uint32(202511), OldestLiveVintage(202611))

	// Live: the current month and the 12 before it.
	cur := uint32(202711)
	require.True(t, VintageLive(202711, cur))
	require.True(t, VintageLive(202611, cur))
	require.False(t, VintageLive(202610, cur))
	require.True(t, VintageRetired(202610, cur))
	require.False(t, VintageRetired(202611, cur))
	require.False(t, VintageLive(202712, cur), "a future month is not live")
	require.False(t, VintageRetired(202712, cur))
	// Retirement at the first second of month + 13, never before.
	m := uint32(202610)
	at := VintageRetiresAt(m)
	require.True(t, VintageLive(m, MonthOf(at.Add(-time.Second))))
	require.True(t, VintageRetired(m, MonthOf(at)))

	for _, bad := range []uint32{0, 1, 202600, 202613, 199912, 1000001} {
		require.Error(t, ValidateVintage(bad), "%d", bad)
	}
	require.NoError(t, ValidateVintage(202612))
}

// Both denom forms parse; the base vintage is spelled without a suffix, every
// other vintage with exactly one.
func TestVintageDenoms(t *testing.T) {
	const v = "earthvaloper1abc"
	require.Equal(t, "derth/"+v, VintageDenom(v, 202610, 202610))
	require.Equal(t, "derth/"+v+"/202611", VintageDenom(v, 202611, 202610))
	require.Equal(t, DerthDenom(v), VintageDenom(v, 202610, 202610))

	val, m, ok := ParseDerthDenom("derth/" + v)
	require.True(t, ok)
	require.Equal(t, v, val)
	require.Zero(t, m, "the legacy form names the base vintage")
	val, m, ok = ParseDerthDenom("derth/" + v + "/202611")
	require.True(t, ok)
	require.Equal(t, v, val)
	require.Equal(t, uint32(202611), m)
	for _, bad := range []string{"derth/", "derth//202611", "derth/a/b", "derth/a/20261", "derth/a/202613",
		"derth/a/202611/1", "uerth", "derth/a/+20261", "derth/a/0202611"} {
		_, _, ok := ParseDerthDenom(bad)
		require.False(t, ok, bad)
	}
	vc := VintageCtx{Base: 202610, Current: 202612}
	require.Equal(t, uint32(202610), vc.Resolve(0))
	require.Equal(t, "derth/"+v, vc.Denom(v, 0))
	require.Equal(t, "derth/"+v, vc.Denom(v, 202610))
	require.Equal(t, "derth/"+v+"/202611", vc.Denom(v, 202611))
	// Distinct assets per vintage.
	require.NotEqual(t, privacy.AssetID(vc.Denom(v, 202611)), privacy.AssetID(vc.Denom(v, 202612)))
}

// MsgCheckIn: lane A the old vintage (amount leaving), lane B the current
// one (amount credited, unlabelled); the sighash binds the stake fields,
// then Bytes(validator), from_vintage, amount and the split.
func TestMsgCheckInFields(t *testing.T) {
	hrp := sdk.GetConfig().GetBech32ValidatorAddrPrefix()
	a, _ := bech32.ConvertAndEncode(hrp, make([]byte, 20))
	z := make([]byte, 32)
	m := &MsgCheckIn{Validator: a, FromVintage: 202610, Amount: 5,
		Stake: StakeProof{Anchor: z, DebtRoot: z, Nullifiers: [][]byte{z, z}, Commitment: z,
			CreditNullifier: z, CreditCommitment: z},
		GroundworksSplit: []allocationtypes.AllocationWeight{{OptionId: 3, Percent: 100}}}
	fs, err := m.SighashFields(nil)
	require.NoError(t, err)
	stake := m.Stake.StakeFields()
	require.Equal(t, []fr.Element{privacy.Bytes([]byte(a)), privacy.U64(202610), privacy.U64(5),
		privacy.Bytes(SplitsBytes(m.GroundworksSplit))}, fs[len(stake):])
	vc := VintageCtx{Base: 202610, Current: 202702}
	require.Equal(t, StakeLanes{Denom: DerthDenom(a), VOut: 5, CreditDenom: DerthPrefix + a + "/202702", CreditIn: 5},
		m.StakeLanes(vc))
	va, vb := m.LaneVintages(vc)
	require.Equal(t, uint32(202610), va)
	require.Equal(t, uint32(202702), vb)
	require.True(t, m.HasCreditLane())

	zero := *m
	zero.FromVintage = 0
	require.ErrorIs(t, zero.ValidateBasic(), ErrVintage)
	none := *m
	none.Amount = 0
	require.ErrorContains(t, none.ValidateBasic(), "amount must be positive")
}

// A pre-v1.3.0 msg (vintage 0) hashes exactly as before; naming a vintage
// appends it, so the two encodings of the base vintage never share a
// sighash.
func TestVintageSighashCompat(t *testing.T) {
	hrp := sdk.GetConfig().GetBech32ValidatorAddrPrefix()
	a, _ := bech32.ConvertAndEncode(hrp, make([]byte, 20))
	z := make([]byte, 32)
	st := StakeProof{Anchor: z, DebtRoot: z, Nullifiers: [][]byte{z, z}, Commitment: z, CreditNullifier: z, CreditCommitment: z}
	r := &MsgRestake{Validator: a, Stake: st}
	f0, err := r.SighashFields(nil)
	require.NoError(t, err)
	require.Len(t, f0, len(st.StakeFields())+2)
	r.Vintage = 202610
	f1, err := r.SighashFields(nil)
	require.NoError(t, err)
	require.Equal(t, append(append([]fr.Element{}, f0...), privacy.U64(202610)), f1)

	u := &MsgUndelegate{Validator: a, Stake: st, Amount: 1, Pc: z}
	g0, err := u.SighashFields(nil)
	require.NoError(t, err)
	u.Vintage = 202701
	g1, err := u.SighashFields(nil)
	require.NoError(t, err)
	require.Equal(t, len(g0)+1, len(g1))

	v := &MsgStakeVote{Validator: a, VoteNullifiers: [][]byte{el32(1), el32(2)}, DebtRoot: z}
	h0, err := v.SighashFields(nil)
	require.NoError(t, err)
	v.Vintage = 202701
	h1, err := v.SighashFields(nil)
	require.NoError(t, err)
	require.Equal(t, privacy.U64(202701), h1[len(h1)-1])
	require.Equal(t, h0, h1[:len(h0)])
	vc := VintageCtx{Base: 202610, Current: 202702}
	pub := v.VotePublicInputs(vc, z, z, fr.Element{})
	require.Equal(t, privacy.FieldBytes(privacy.AssetID(DerthPrefix+a+"/202701")), pub[3])
	v.Vintage = 0
	pub = v.VotePublicInputs(vc, z, z, fr.Element{})
	require.Equal(t, privacy.FieldBytes(privacy.AssetID(DerthDenom(a))), pub[3])

	bad := &MsgRestake{Validator: a, Stake: st, Vintage: 202613}
	require.ErrorIs(t, bad.ValidateBasic(), ErrVintage)
}

package cmd

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"

	personhoodtypes "github.com/earth-network/earth/x/personhood/types"
	shieldedtypes "github.com/earth-network/earth/x/shielded/types"
)

// The registration check reads the MsgRegister a wallet builds, fee bundle
// included, as proto JSON: it decodes field for field.
func TestGasCheckDecodesMsgRegisterWithBundle(t *testing.T) {
	cdc := gasCheckCodec()
	b32 := func(v byte) []byte { b := make([]byte, 32); b[31] = v; return b }
	msg := personhoodtypes.MsgRegister{
		Fee: shieldedtypes.Bundle{
			Actions: []shieldedtypes.Action{
				{Anchor: b32(1), Nullifier: b32(2), Commitment: b32(3), Cv: make([]byte, 64), Ciphertext: []byte("ct0"), Proof: []byte{9}},
				{Anchor: b32(1), Nullifier: b32(4), Commitment: b32(5), Cv: make([]byte, 64), Ciphertext: []byte("ct1"), Proof: []byte{8}},
			},
			Balances:   []shieldedtypes.ValueBalance{{Denom: "uerth", Amount: 50_000}},
			BindingSig: make([]byte, 96),
		},
		Proof: []byte{1, 2}, PublicSignals: []string{"1", "2"}, SignatureAlgorithm: "lean_poa", DscDer: []byte{3},
		Idc: b32(6), PcAnml: b32(7), PcErth: b32(8), CiphertextAnml: []byte("a"), CiphertextErth: []byte("e"),
	}
	raw, err := cdc.MarshalJSON(&msg)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"binding_sig"`)
	var got personhoodtypes.MsgRegister
	require.NoError(t, cdc.UnmarshalJSON(raw, &got))
	require.Equal(t, msg, got)
	require.Equal(t, uint64(50_000), got.PrivateFee())
}

func encodePairs(pairs ...pair) []byte {
	var out []byte
	for _, p := range pairs {
		var msg []byte
		msg = protowire.AppendTag(msg, 1, protowire.BytesType)
		msg = protowire.AppendBytes(msg, p.Key)
		msg = protowire.AppendTag(msg, 2, protowire.BytesType)
		msg = protowire.AppendBytes(msg, p.Value)
		out = protowire.AppendTag(out, 1, protowire.BytesType)
		out = protowire.AppendBytes(out, msg)
	}
	return out
}

func TestDecodePairs(t *testing.T) {
	want := []pair{{[]byte("a"), []byte("1")}, {[]byte("b"), nil}}
	got, err := decodePairs(encodePairs(want...))
	if err != nil || len(got) != 2 || !bytes.Equal(got[0].Key, []byte("a")) || !bytes.Equal(got[0].Value, []byte("1")) || !bytes.Equal(got[1].Key, []byte("b")) {
		t.Fatalf("decodePairs = %v, %v", got, err)
	}
	if _, err := decodePairs([]byte{0x08, 0x01}); err == nil {
		t.Fatal("decodePairs accepted a varint where kv.Pairs has bytes")
	}
}

func TestCommonPrefix(t *testing.T) {
	cases := []struct{ a, b, want []byte }{
		{[]byte{5, 'a'}, []byte{5, 'b'}, []byte{5}},
		{[]byte{5, 1, 2}, []byte{5, 1, 3}, []byte{5, 1}},
		{[]byte{5}, nil, []byte{5}},
		{[]byte{5}, []byte{6}, []byte{}},
	}
	for _, c := range cases {
		if got := commonPrefix(c.a, c.b); !bytes.Equal(got, c.want) {
			t.Errorf("commonPrefix(%v, %v) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

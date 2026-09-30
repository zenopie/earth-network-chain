package cmd

import (
	"bytes"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
)

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

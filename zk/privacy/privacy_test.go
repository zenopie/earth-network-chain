package privacy

import (
	"testing"

	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
)

// Pinned in privacy_core as ASSET_ERTH.
func TestAssetERTHMatchesNoir(t *testing.T) {
	const want = "ad44a14c7205c61e39db2c64b79005ffb14a2a927977e8cda2208be2e3fe54c"
	if got := AssetID("uerth"); got.Text(16) != want {
		t.Fatalf("AssetID(uerth) = %s, want %s", got.Text(16), want)
	}
}

func TestTagsMatchNoir(t *testing.T) {
	for name, c := range map[string]struct {
		got  string
		want string
	}{
		"id":     {TagID.Text(16), "65617274682e6964"},
		"owner":  {TagOwner.Text(16), "65617274682e6f776e6572"},
		"leaf":   {TagLeaf.Text(16), "65617274682e6c656166"},
		"sn":     {TagSN.Text(16), "65617274682e736e"},
		"pc":     {TagPC.Text(16), "65617274682e7063"},
		"cm":     {TagCM.Text(16), "65617274682e636d"},
		"nf":     {TagNF.Text(16), "65617274682e6e66"},
		"vote":   {TagVote.Text(16), "65617274682e766f7465"},
		"reg":    {TagReg.Text(16), "65617274682e726567"},
		"asset":  {TagAsset.Text(16), "65617274682e6173736574"},
		"signal": {TagSignal.Text(16), "65617274682e7369676e616c"},
		"bytes":  {TagBytes.Text(16), "65617274682e6279746573"},
		"scope":  {TagScope.Text(16), "65617274682e73636f7065"},
	} {
		if c.got != c.want {
			t.Errorf("tag %s = %s, want %s", name, c.got, c.want)
		}
	}
}

func TestAssetIDDistinct(t *testing.T) {
	seen := map[string]string{}
	for _, d := range []string{"uerth", "uanml", "derth/earthvaloper1abc", "unbond/earthvaloper1abc/1",
		"ibc/27394FB092D2ECCD56123C74F36E4C1F926001CEADA9CA97EA622B25F41E5EB2", "", "u", "uert"} {
		a := AssetID(d)
		k := a.String()
		if o, ok := seen[k]; ok {
			t.Fatalf("AssetID collision %q vs %q", d, o)
		}
		seen[k] = d
	}
}

func TestTagsDistinct(t *testing.T) {
	seen := map[string]bool{}
	for _, tg := range []fr.Element{TagID, TagOwner, TagLeaf, TagSN, TagPC, TagCM, TagNF, TagVote, TagReg, TagAsset, TagSignal, TagBytes, TagScope} {
		k := tg.Text(16)
		if seen[k] {
			t.Fatalf("duplicate tag %s", k)
		}
		seen[k] = true
	}
}

func TestBytesDistinguishesLengthAndChunks(t *testing.T) {
	cases := [][]byte{nil, {0}, {0, 0}, {1}, make([]byte, 31), make([]byte, 32), append(make([]byte, 31), 1)}
	seen := map[string]int{}
	for i, c := range cases {
		b := Bytes(c)
		k := b.Text(16)
		if j, ok := seen[k]; ok {
			t.Fatalf("Bytes collision between case %d and %d", i, j)
		}
		seen[k] = i
	}
}

func TestTransferSignalBindsEveryField(t *testing.T) {
	cts := [3][]byte{[]byte("a"), []byte("b"), []byte("c")}
	recv := []byte("receiver-address-20b")
	base := TransferSignal("earth-1", recv, cts, 0)
	if base != SpendSignal(MsgTransferType, "earth-1", cts, Bytes(recv), U64(0)) {
		t.Fatal("TransferSignal != SpendSignal(MsgTransfer, ..., Bytes(receiver), fee_from_output)")
	}
	alt := []fr.Element{
		TransferSignal("earth-2", recv, cts, 0),
		TransferSignal("earth-1", nil, cts, 0),
		TransferSignal("earth-1", recv, [3][]byte{[]byte("b"), []byte("a"), []byte("c")}, 0),
		TransferSignal("earth-1", recv, [3][]byte{[]byte("a"), []byte("b"), []byte("d")}, 0),
		TransferSignal("earth-1", recv, cts, 1),
		SpendSignal("/earth.shielded.v1.MsgOther", "earth-1", cts, Bytes(recv), U64(0)),
	}
	for i, a := range alt {
		if a == base {
			t.Fatalf("variant %d has the same signal", i)
		}
	}
}

func TestMultiSpendSignalBindsEveryTransfer(t *testing.T) {
	cts := [][3][]byte{{[]byte("a"), []byte("b"), []byte("c")}, {[]byte("d"), []byte("e"), []byte("f")}}
	nfs := [][3]fr.Element{{U64(1), U64(2), U64(3)}, {U64(4), U64(5), U64(6)}}
	base := MultiSpendSignal("/m", "earth-1", cts, nfs, U64(7))
	swappedNf := [][3]fr.Element{nfs[0], {U64(4), U64(5), U64(9)}}
	swappedCt := [][3][]byte{cts[0], {[]byte("d"), []byte("e"), []byte("g")}}
	reordered := [][3][]byte{cts[1], cts[0]}
	reorderedNf := [][3]fr.Element{nfs[1], nfs[0]}
	alt := []fr.Element{
		MultiSpendSignal("/m", "earth-1", cts, swappedNf, U64(7)),
		MultiSpendSignal("/m", "earth-1", swappedCt, nfs, U64(7)),
		MultiSpendSignal("/m", "earth-1", reordered, reorderedNf, U64(7)),
		MultiSpendSignal("/m", "earth-1", cts, nfs, U64(8)),
		MultiSpendSignal("/n", "earth-1", cts, nfs, U64(7)),
		MultiSpendSignal("/m", "earth-1", cts[:1], nfs[:1], U64(7)),
	}
	for i, a := range alt {
		if a == base {
			t.Fatalf("variant %d has the same signal", i)
		}
	}
}

func TestFieldFromBytesCanonical(t *testing.T) {
	e := AssetID("uerth")
	got, err := FieldFromBytes(FieldBytes(e))
	if err != nil || got != e {
		t.Fatalf("round trip: %v", err)
	}
	mod := fr.Modulus().FillBytes(make([]byte, 32))
	if _, err := FieldFromBytes(mod); err == nil {
		t.Fatal("modulus accepted")
	}
	if _, err := FieldFromBytes(make([]byte, 31)); err == nil {
		t.Fatal("short input accepted")
	}
}

func TestScopesDistinct(t *testing.T) {
	seen := map[string]string{}
	for name, s := range map[string]fr.Element{
		"claim0": ClaimScope(0), "claim1": ClaimScope(1), "caretaker": CaretakerScope(),
		"proposal1/0": ProposalScope(1, 0), "proposal1/1": ProposalScope(1, 1), "proposal0/1": ProposalScope(0, 1),
		"removal1": RemovalScope(1), "propose1/0": ProposeRemovalScope(1, 0),
	} {
		k := s.String()
		if o, ok := seen[k]; ok {
			t.Fatalf("scope collision %s vs %s", name, o)
		}
		seen[k] = name
	}
}

// Pinned in privacy_core as country_code.
func TestCountryField(t *testing.T) {
	if got := CountryField("DE"); got.Text(16) != "4445" {
		t.Fatalf("CountryField(DE) = %s", got.Text(16))
	}
	for _, cc := range []string{"", "D", "DEU", "de", "D1", "??"} {
		if f := CountryField(cc); !f.IsZero() {
			t.Errorf("CountryField(%q) is not 0 (unknown)", cc)
		}
	}
	a, b := IdentityLeaf(U64(1), U64(2), CountryField("DE"), 3), IdentityLeaf(U64(1), U64(2), CountryField("FR"), 3)
	if a.Equal(&b) {
		t.Fatal("the leaf does not commit to the country")
	}
}

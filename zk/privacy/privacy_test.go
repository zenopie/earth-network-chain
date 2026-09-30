package privacy

import "testing"

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
		"id":    {TagID.Text(16), "65617274682e6964"},
		"owner": {TagOwner.Text(16), "65617274682e6f776e6572"},
		"leaf":  {TagLeaf.Text(16), "65617274682e6c656166"},
		"sn":    {TagSN.Text(16), "65617274682e736e"},
		"pc":    {TagPC.Text(16), "65617274682e7063"},
		"cm":    {TagCM.Text(16), "65617274682e636d"},
		"nf":    {TagNF.Text(16), "65617274682e6e66"},
		"vote":  {TagVote.Text(16), "65617274682e766f7465"},
		"reg":   {TagReg.Text(16), "65617274682e726567"},
		"asset": {TagAsset.Text(16), "65617274682e6173736574"},
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

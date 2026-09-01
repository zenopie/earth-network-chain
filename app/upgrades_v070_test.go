package app

import (
	"encoding/base64"
	"encoding/hex"
	"path"
	"strings"
	"testing"
)

// The upgrades this binary can perform are v0.6.0, v0.7.0 and v0.8.0, and
// v0.6.1 is not among them.
//
// v0.6.1 was tagged and built but never proposed, so no chain ever halted on
// that name and nothing has to replay it. Keeping a handler for it would imply
// otherwise. The inverse property is the one that would matter if it were ever
// scheduled — every name a chain has actually halted on must keep its entry
// for ever, or a node syncing from genesis stops at that height with no handler.
func TestUpgradeSetIsTheMergedRelease(t *testing.T) {
	names := make([]string, 0, len(Upgrades))
	for _, u := range Upgrades {
		names = append(names, u.Name)
		if u.CreateHandler == nil {
			t.Fatalf("upgrade %q has no handler", u.Name)
		}
	}
	want := []string{"v0.6.0", "v0.7.0", "v0.8.0"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("Upgrades = %v, want %v", names, want)
	}
}

// The module set does not change, so nothing may declare a store upgrade. A
// stray one is not cosmetic: UpgradeStoreLoader would try to mount, rename or
// delete a store that does not need it, and the chain fails to start.
func TestV070AddsNoStores(t *testing.T) {
	for _, u := range Upgrades {
		s := u.StoreUpgrades
		if len(s.Added)+len(s.Renamed)+len(s.Deleted) != 0 {
			t.Fatalf("upgrade %q declares store upgrades: %+v", u.Name, s)
		}
	}
}

// Exactly the seven register circuits, each a decodable non-empty key.
//
// The handler refuses at runtime if this set and the chain's params describe
// different circuits, but that check fires at the upgrade height in front of
// every validator. This one fires in CI.
func TestV070EmbedsEveryVerifyingKey(t *testing.T) {
	want := map[string]bool{
		"lean_poa":              false,
		"lean_poa_p384":         false,
		"lean_poa_brainpool256": false,
		"lean_poa_brainpool384": false,
		"lean_poa_brainpool512": false,
		"lean_poa_rsa2048":      false,
		"lean_poa_rsa4096":      false,
	}

	entries, err := v070Assets.ReadDir(v070VerifyingKeyDir)
	if err != nil {
		t.Fatalf("read embedded verifying keys: %v", err)
	}
	for _, e := range entries {
		algo := strings.TrimSuffix(e.Name(), ".vk.b64")
		if _, ok := want[algo]; !ok {
			t.Fatalf("embedded an unexpected verifying key %q", algo)
		}
		want[algo] = true

		raw, err := v070Assets.ReadFile(path.Join(v070VerifyingKeyDir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		vk, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
		if err != nil {
			t.Fatalf("%s is not valid base64: %v", e.Name(), err)
		}
		if len(vk) == 0 {
			t.Fatalf("%s decodes to nothing", e.Name())
		}
	}
	for algo, seen := range want {
		if !seen {
			t.Fatalf("no embedded verifying key for %q", algo)
		}
	}
}

// The remap must actually reproduce the commitment earth-1 has in state.
//
// This is the load-bearing assertion of the whole DSC item. The old value is
// hardcoded because it is a fact about the live chain, not about this code: it
// is public signal [3] of the MsgRegister in block 4833, the transaction that
// created the one registration on earth-1. If the embedded certificate is ever
// swapped for the wrong one, the migration would rewrite a key nobody holds and
// silently strand the registration it was written to save — so the fixture is
// pinned rather than recomputed from whatever is embedded.
func TestV070RemapReproducesTheLiveRegistrationsKey(t *testing.T) {
	// Public signal [3] of earth-1 block 4833's MsgRegister.
	const liveDscKey = "05f76d3e04d7548b76de536de3eb01cbb7ddfb933c6b135dfd7b7c47f5d66ff0"

	remap, err := v070CommitmentRemap()
	if err != nil {
		t.Fatalf("v070CommitmentRemap: %v", err)
	}
	if len(remap) == 0 {
		t.Fatal("the remap is empty: no DSC certificates are embedded")
	}

	old, err := hex.DecodeString(liveDscKey)
	if err != nil {
		t.Fatal(err)
	}
	next, ok := remap[string(old)]
	if !ok {
		have := make([]string, 0, len(remap))
		for k := range remap {
			have = append(have, hex.EncodeToString([]byte(k)))
		}
		t.Fatalf("no remap for the live registration's dsc_key %s; embedded certificates produce %v",
			liveDscKey, have)
	}
	if hex.EncodeToString(next) == liveDscKey {
		t.Fatal("the tagged commitment equals the untagged one, so the tag is not being absorbed")
	}
	if len(next) != len(old) {
		t.Fatalf("new commitment is %d bytes, want %d", len(next), len(old))
	}
}

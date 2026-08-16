package keeper

import (
	"testing"
)

// TestRegistrationTallies checks the per-DSC and per-country counters the
// explorer reads: they must increment independently and survive repeats.
func TestRegistrationTallies(t *testing.T) {
	k, ctx := newKeeperForTest(t)

	dscA := []byte("dsc-a")
	dscB := []byte("dsc-b")
	for _, c := range []struct {
		dsc     []byte
		country string
	}{{dscA, "LV"}, {dscA, "LV"}, {dscA, "DE"}, {dscB, "DE"}, {dscB, ""}} {
		if err := bumpCount(ctx, k.RegCountByDsc, c.dsc); err != nil {
			t.Fatal(err)
		}
		if err := bumpCount(ctx, k.RegCountByCountry, c.country); err != nil {
			t.Fatal(err)
		}
	}

	if n, _ := k.RegCountByDsc.Get(ctx, dscA); n != 3 {
		t.Fatalf("dscA count = %d, want 3", n)
	}
	if n, _ := k.RegCountByDsc.Get(ctx, dscB); n != 2 {
		t.Fatalf("dscB count = %d, want 2", n)
	}
	if n, _ := k.RegCountByCountry.Get(ctx, "LV"); n != 2 {
		t.Fatalf("LV count = %d, want 2", n)
	}
	if n, _ := k.RegCountByCountry.Get(ctx, "DE"); n != 2 {
		t.Fatalf("DE count = %d, want 2", n)
	}
	// A certificate with no country still counts, under the empty key.
	if n, _ := k.RegCountByCountry.Get(ctx, ""); n != 1 {
		t.Fatalf("unknown-country count = %d, want 1", n)
	}
}

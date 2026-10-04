package types

import "testing"

// Audit 6 D-L-D2: volume_depth_cap_per_day is bounded.
func TestVolumeDepthCapBounded(t *testing.T) {
	p := DefaultParams()
	p.VolumeDepthCapPerDay = MaxVolumeDepthCapPerDay
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	p.VolumeDepthCapPerDay = MaxVolumeDepthCapPerDay + 1
	if p.Validate() == nil {
		t.Fatal("a multiple above the maximum validated")
	}
	p.VolumeDepthCapPerDay = 1 << 63
	if p.Validate() == nil {
		t.Fatal("a multiple past int64 validated")
	}
}

package keeper

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/earth-network/earth/x/pki/certs"
	"github.com/earth-network/earth/x/pki/types"
)

// The two ceilings that bound what one VerifyDsc call can cost are only safe if
// they sit above what genuine certificates need. Both were set from this
// measurement rather than from the specification, and both were wrong on the
// first guess: Doc 9303 implies keys top out around 512 bytes and the real
// store holds a 768-byte one, and a per-DN group of 16 looked generous until
// the largest real group turned out to be 13.
//
// So the numbers are pinned here against the master list the image ships. A
// trust-store update that outgrows either cap fails this test, which is a much
// better way to find out than a country's passports quietly failing to
// register.
func TestTrustStoreFitsTheCaps(t *testing.T) {
	path := filepath.Join("..", "..", "..", "csca", "masterlist", "allowlist.ml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("no bundled master list at %s", path)
	}
	ders, err := certs.ParseMasterList(raw)
	if err != nil {
		t.Fatalf("ParseMasterList: %v", err)
	}
	if len(ders) == 0 {
		t.Fatal("master list parsed to nothing")
	}

	perDN := map[[32]byte]int{}
	largestKey, largestKeyAt := 0, 0
	parsed := 0

	for i, der := range ders {
		c, err := certs.ParseCert(der)
		if err != nil {
			// A certificate the parser rejects is one the chain would never
			// verify against anyway, and the store carries a few. Not this
			// test's business — but a key rejected for *size* is, because that
			// is this cap doing the rejecting.
			if err == certs.ErrPublicKeyTooLarge {
				t.Errorf("certificate %d exceeds MaxPublicKeyBytes (%d); raise the cap",
					i, certs.MaxPublicKeyBytes)
			}
			continue
		}
		parsed++
		perDN[sha256.Sum256(c.SubjectRaw)]++
		if n := len(c.PublicKey.CanonicalBytes()); n > largestKey {
			largestKey, largestKeyAt = n, i
		}
	}

	groups := make([]int, 0, len(perDN))
	for _, n := range perDN {
		groups = append(groups, n)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(groups)))
	largestGroup := groups[0]

	t.Logf("%d certificates, %d parsed, %d distinct subject DNs", len(ders), parsed, len(perDN))
	t.Logf("largest per-DN group: %d (cap %d)", largestGroup, types.MaxIssuerCandidates)
	t.Logf("largest public key:   %d bytes at index %d (cap %d)",
		largestKey, largestKeyAt, certs.MaxPublicKeyBytes)

	// Headroom, not just fit. Countries renew their CSCAs, so a cap the store
	// has already grown into is one that will start rejecting registrations
	// before anyone next looks at this file.
	if largestGroup*2 > types.MaxIssuerCandidates {
		t.Errorf("largest per-DN group is %d against a cap of %d — less than 2x headroom; raise MaxIssuerCandidates",
			largestGroup, types.MaxIssuerCandidates)
	}
	if largestKey*2 > certs.MaxPublicKeyBytes {
		t.Errorf("largest public key is %d bytes against a cap of %d — less than 2x headroom; raise MaxPublicKeyBytes",
			largestKey, certs.MaxPublicKeyBytes)
	}
}

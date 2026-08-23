// Command pki-genesis collects CSCA certificates and prints them as the x/pki
// genesis `cscas` array (JSON), for seeding genesis. Only certificate_der is
// needed — the module re-derives the rest on init.
//
// Inputs may be ICAO CSCA Master Lists (.ml) and/or individual DER/CER
// certificate files. Master lists cover what ICAO distributes; individual files
// cover countries whose CSCAs are absent from the master list or are newer than
// the last distribution.
//
//	go run ./tools/pki-genesis path/to/allowlist.ml extra/*.cer > cscas.json
//
// Paste the array as app_state.pki.cscas in genesis (or config.yml).
package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/earth-network/earth/x/pki/certs"
)

type csca struct {
	CertificateDer string `json:"certificate_der"`
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: pki-genesis <allowlist.ml | cert.der> ...")
		os.Exit(2)
	}

	var (
		out     []csca
		skipped int
		// Deduped on exact DER bytes only — enough to tolerate the same file
		// being passed twice, without discarding anything meaningful.
		//
		// Do NOT dedup on the keeper's CSCA id (SKI). Master lists carry several
		// certificates per signing identity — renewals and link certificates that
		// share a key — and at least one SKI in the ICAO list appears under two
		// different subject DNs. The keeper overwrites the CSCA record by id but
		// writes a CscaByDN index entry for every certificate it ingests, and
		// issuerCandidates resolves issuers by subject DN as well as by SKI.
		// Collapsing on SKI here would silently drop DN index entries and shrink
		// the set of DSCs the chain can verify.
		seen = map[string]bool{}
	)

	for _, path := range os.Args[1:] {
		raw, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintln(os.Stderr, "read:", err)
			os.Exit(1)
		}

		var ders [][]byte
		if strings.EqualFold(filepath.Ext(path), ".ml") {
			ders, err = certs.ParseMasterList(raw)
			if err != nil {
				fmt.Fprintf(os.Stderr, "parse master list %s: %v\n", path, err)
				os.Exit(1)
			}
		} else {
			ders = [][]byte{raw}
		}

		added := 0
		for _, d := range ders {
			if _, err := certs.ParseCert(d); err != nil {
				skipped++ // skip certs even our lenient parser cannot read
				continue
			}
			h := sha256.Sum256(d)
			if seen[string(h[:])] {
				continue
			}
			seen[string(h[:])] = true
			out = append(out, csca{CertificateDer: base64.StdEncoding.EncodeToString(d)})
			added++
		}
		fmt.Fprintf(os.Stderr, "%s: +%d\n", filepath.Base(path), added)
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		fmt.Fprintln(os.Stderr, "encode:", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "total %d CSCAs (%d unparseable, skipped)\n", len(out), skipped)
}

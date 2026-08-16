package app_test

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestGenesisSeedsRegistrationTrustAnchors guards the two lists that decide
// whether proof-of-personhood works at all on a fresh chain.
//
// With either empty, MsgRegister always fails: no verifying key means no proof
// can be checked, and no CSCA means no Document Signer can be trusted. That
// would leave ANML claims and the entire democratic pillar inert from block 1
// until a governance proposal landed — a week, given the voting period. Both are
// easy to drop while editing config.yml, and nothing else would notice.
func TestGenesisSeedsRegistrationTrustAnchors(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "config.yml"))
	if err != nil {
		t.Fatalf("read config.yml: %v", err)
	}

	var cfg struct {
		Genesis struct {
			AppState struct {
				Personhood struct {
					Params struct {
						VerifyingKeys map[string]string `yaml:"verifying_keys"`
					} `yaml:"params"`
				} `yaml:"personhood"`
				Pki struct {
					Cscas []struct {
						CertificateDer string `yaml:"certificate_der"`
					} `yaml:"cscas"`
				} `yaml:"pki"`
			} `yaml:"app_state"`
		} `yaml:"genesis"`
	}
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("parse config.yml: %v", err)
	}

	// Every circuit the mobile client can select must have a key, or passports
	// with that Document Signer key type silently cannot register.
	wantAlgorithms := []string{
		"lean_poa",
		"lean_poa_p384",
		"lean_poa_rsa2048",
		"lean_poa_rsa4096",
		"lean_poa_brainpool256",
		"lean_poa_brainpool384",
		"lean_poa_brainpool512",
	}
	keys := cfg.Genesis.AppState.Personhood.Params.VerifyingKeys
	for _, algo := range wantAlgorithms {
		vk, ok := keys[algo]
		if !ok {
			t.Errorf("no verifying key seeded for %q", algo)
			continue
		}
		decoded, err := base64.StdEncoding.DecodeString(vk)
		if err != nil {
			t.Errorf("verifying key for %q is not valid base64: %v", algo, err)
			continue
		}
		if len(decoded) == 0 {
			t.Errorf("verifying key for %q is empty", algo)
		}
	}

	// The CSCA master list is the root of trust; a handful would mean a partial
	// import, so require something in the order of the real ICAO list.
	if n := len(cfg.Genesis.AppState.Pki.Cscas); n < 400 {
		t.Errorf("only %d CSCAs seeded; expected the full ICAO master list (~539)", n)
	}
}

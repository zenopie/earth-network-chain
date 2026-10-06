package app_test

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// minRegisterVariants is the number of passport register circuits
// (PASSPORT_COVERAGE.md in the mobile repo): a smaller set means a scheme
// real passports use was dropped.
const minRegisterVariants = 33

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
	// with that signature scheme silently cannot register. The circuits are
	// listed by networks/genesis/verifying-keys (one file per variant, written
	// by scripts/privacy-vks.sh from the mobile repo's circuits/variants.json);
	// the dev chain must carry exactly that set, with the same keys.
	files, err := filepath.Glob(filepath.Join("..", "networks", "genesis", "verifying-keys", "*.vk.b64"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) < minRegisterVariants {
		t.Fatalf("networks/genesis/verifying-keys holds %d register circuits, want at least %d", len(files), minRegisterVariants)
	}
	var wantAlgorithms []string
	genesisKeys := map[string]string{}
	for _, f := range files {
		algo := strings.TrimSuffix(filepath.Base(f), ".vk.b64")
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		wantAlgorithms = append(wantAlgorithms, algo)
		genesisKeys[algo] = strings.TrimSpace(string(b))
	}
	if n := len(cfg.Genesis.AppState.Personhood.Params.VerifyingKeys); n != len(files) {
		t.Errorf("config.yml seeds %d register keys, networks/genesis has %d", n, len(files))
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
		if vk != genesisKeys[algo] {
			t.Errorf("config.yml's key for %q differs from networks/genesis/verifying-keys", algo)
		}
	}

	// The CSCA master list is the root of trust; a handful would mean a partial
	// import, so require something in the order of the real ICAO list.
	if n := len(cfg.Genesis.AppState.Pki.Cscas); n < 400 {
		t.Errorf("only %d CSCAs seeded; expected the full ICAO master list (~539)", n)
	}
}

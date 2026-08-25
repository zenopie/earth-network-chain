package networks

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"cosmossdk.io/math"
	"gopkg.in/yaml.v3"
)

// Fields config.yml deliberately does not state, because ignite fills them from
// its own `default_denom` and config.yml says so in as many words: "one source
// of truth beats two". They are checked against that value instead of being
// reported as gaps.
var delegatedToDefaultDenom = map[string]bool{
	".staking.params.bond_denom": true,
	".mint.params.mint_denom":    true,
}

// sameDecimal reports whether two strings are the same LegacyDec. config.yml
// writes "0" where the genesis file writes "0.000000000000000000"; they are one
// value and only one of them is a disagreement worth failing on.
func sameDecimal(a, b string) bool {
	da, err := math.LegacyNewDecFromStr(a)
	if err != nil {
		return false
	}
	db, err := math.LegacyNewDecFromStr(b)
	if err != nil {
		return false
	}
	return da.Equal(db)
}

// config.yml is ignite's, and it drives `ignite chain serve` for local
// development only. networks/genesis/ is the source of truth for the chain that
// actually launches.
//
// Keeping two of anything is how they diverge, and they already did: commit
// 6dd49f3 split the pre-mine into a third for the ANML/ERTH pool and two thirds
// for the liquidity auction, changed networks/genesis.json, and left config.yml
// seeding the whole 2,522,880,000 ERTH into pool 1 with no auction at all. The
// dev chain and the launch chain disagreed about the token supply's shape for
// two days and nothing noticed.
//
// So: the parameters both files state must agree. This does not require
// config.yml to carry everything — it is free to add dev accounts, a faucet and
// a validator, none of which belong in a launch genesis — only that where the
// two overlap they say the same thing.
func TestConfigYmlAgreesWithGenesisSources(t *testing.T) {
	raw, err := os.ReadFile("../config.yml")
	if err != nil {
		t.Fatalf("read config.yml: %v", err)
	}
	var cfg struct {
		Genesis struct {
			AppState map[string]any `yaml:"app_state"`
		} `yaml:"genesis"`
	}
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("parse config.yml: %v", err)
	}
	if cfg.Genesis.AppState == nil {
		t.Fatal("config.yml has no genesis.app_state")
	}
	var denomCfg struct {
		DefaultDenom string `yaml:"default_denom"`
	}
	if err := yaml.Unmarshal(raw, &denomCfg); err != nil {
		t.Fatalf("parse config.yml: %v", err)
	}

	srcRaw, err := os.ReadFile("genesis/app_state.json")
	if err != nil {
		t.Fatalf("read app_state.json: %v", err)
	}
	var src map[string]any
	if err := json.Unmarshal(srcRaw, &src); err != nil {
		t.Fatalf("parse app_state.json: %v", err)
	}

	// Round-trip the YAML through JSON so numbers, which YAML types as int and
	// JSON as float64, compare as themselves rather than by Go kind.
	norm := func(v any) any {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("normalise: %v", err)
		}
		var out any
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatalf("normalise: %v", err)
		}
		return out
	}

	// Walk only the leaves the launch sources actually state. A key config.yml
	// omits entirely is a gap worth reporting; a key it adds is its own business.
	var walk func(want, got any, path string)
	walk = func(want, got any, path string) {
		wm, wIsMap := want.(map[string]any)
		gm, gIsMap := got.(map[string]any)
		if wIsMap && gIsMap {
			for k, wv := range wm {
				sub := path + "." + k
				if delegatedToDefaultDenom[sub] {
					if s, ok := wv.(string); ok && s != denomCfg.DefaultDenom {
						t.Errorf("genesis.app_state%s is %q, but config.yml's default_denom "+
							"is %q — ignite would seed the dev chain with the wrong denom",
							sub, s, denomCfg.DefaultDenom)
					}
					continue
				}
				gv, ok := gm[k]
				if !ok {
					// Recurse into a missing branch rather than reporting it: the
					// leaves under it may all be delegated to default_denom, and a
					// gap should be named at the leaf that is actually missing.
					if _, isMap := wv.(map[string]any); isMap {
						walk(wv, map[string]any{}, sub)
						continue
					}
					t.Errorf("config.yml is missing genesis.app_state%s — the launch "+
						"genesis sets it, so the dev chain is running different rules", sub)
					continue
				}
				walk(wv, gv, sub)
			}
			return
		}
		if ws, ok := want.(string); ok {
			if gs, ok := got.(string); ok && strings.ContainsAny(ws+gs, ".0123456789") && sameDecimal(ws, gs) {
				return
			}
		}
		if !reflect.DeepEqual(norm(want), norm(got)) {
			t.Errorf("genesis.app_state%s disagrees:\n  networks/genesis/app_state.json: %v\n  config.yml:                   %v",
				path, want, got)
		}
	}

	for section, want := range src {
		got, ok := cfg.Genesis.AppState[section]
		if !ok {
			// A whole section may be delegated to default_denom (staking is
			// nothing but bond_denom today); walk it so those are resolved
			// rather than reported as a missing section.
			got = map[string]any{}
		}
		walk(want, got, "."+section)
	}
}

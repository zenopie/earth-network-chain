package app_test

import (
	"os"
	"path/filepath"
	"testing"

	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	distrtypes "github.com/cosmos/cosmos-sdk/x/distribution/types"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"
	minttypes "github.com/cosmos/cosmos-sdk/x/mint/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	"gopkg.in/yaml.v3"

	dextypes "github.com/earth-network/earth/x/dex/types"
)

// TestGenesisFundsOnlyTheDexModuleAccount guards against a genesis that hands
// coins to a module account which does not expect them.
//
// Several modules reconcile their module account against their own genesis
// state in InitGenesis and panic on a mismatch. x/gov is the strictest: it
// compares the gov account's balance with the sum of proposal deposits, so a
// single stray coin aborts chain start with
//
//	panic: expected module account was 4291506029356dexlp/1 but we got
//
// which names the coin but not the address, and happens before any block, so
// there is nothing to query for a diagnosis. That exact panic was hit while
// seeding the ANML/ERTH pool: the pool's LP shares were parked on the gov
// account because governance "owns" the pool conceptually. Ownership is
// recorded in the dex pool state; the coins belong to the dex module account.
//
// The check is structural rather than a test of the gov account alone, because
// the next module account funded by mistake would reintroduce it somewhere new.
func TestGenesisFundsOnlyTheDexModuleAccount(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "config.yml"))
	if err != nil {
		t.Fatalf("read config.yml: %v", err)
	}

	var cfg struct {
		Genesis struct {
			AppState struct {
				Bank struct {
					Balances []struct {
						Address string `yaml:"address"`
					} `yaml:"balances"`
				} `yaml:"bank"`
			} `yaml:"app_state"`
		} `yaml:"genesis"`
	}
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("parse config.yml: %v", err)
	}

	balances := cfg.Genesis.AppState.Bank.Balances
	if len(balances) == 0 {
		t.Fatal("config.yml declares no genesis balances; the dex pool seed is missing")
	}

	// The dex module account is the one legitimate exception: it escrows the
	// pool reserves and holds the pool's LP shares.
	dexAddr := authtypes.NewModuleAddress(dextypes.ModuleName).String()

	// Every module account the app wires up that reconciles or would be
	// corrupted by unexpected coins at genesis.
	guarded := map[string]string{
		govtypes.ModuleName:            authtypes.NewModuleAddress(govtypes.ModuleName).String(),
		minttypes.ModuleName:           authtypes.NewModuleAddress(minttypes.ModuleName).String(),
		distrtypes.ModuleName:          authtypes.NewModuleAddress(distrtypes.ModuleName).String(),
		authtypes.FeeCollectorName:     authtypes.NewModuleAddress(authtypes.FeeCollectorName).String(),
		stakingtypes.BondedPoolName:    authtypes.NewModuleAddress(stakingtypes.BondedPoolName).String(),
		stakingtypes.NotBondedPoolName: authtypes.NewModuleAddress(stakingtypes.NotBondedPoolName).String(),
	}

	sawDex := false
	for _, b := range balances {
		if b.Address == dexAddr {
			sawDex = true
			continue
		}
		for name, addr := range guarded {
			if b.Address == addr {
				t.Errorf("genesis funds the %q module account (%s); move the coins to a "+
					"regular account or to the dex module account (%s)", name, addr, dexAddr)
			}
		}
	}

	// A typo in the dex address would not panic — the coins would simply sit on
	// an unowned account while the pool reported reserves it does not hold.
	if !sawDex {
		t.Errorf("no genesis balance is assigned to the dex module account (%s); "+
			"the seeded pool's reserves and LP shares have nowhere to live", dexAddr)
	}
}

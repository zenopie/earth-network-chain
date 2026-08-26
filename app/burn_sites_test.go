package app

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Every place the chain destroys supply, and what it is counted as.
//
// The burn counters in x/earth are a parallel record: x/bank moves the supply,
// and each call site is separately responsible for saying so. Nothing at runtime
// notices when the second half is forgotten — the burn still happens, the total
// is just quietly short, and no later observation can recover it.
//
// So the source is pinned instead. Adding a BurnCoins anywhere fails this test
// until it is listed here, which forces the decision at the moment it is made
// rather than leaving it to be discovered in a figure that looks plausible.
var burnSites = map[string]string{
	"x/earth/keeper/fees.go":                       "gas_fees",
	"x/dex/keeper/msg_server_swap.go":              "swap_fee",
	"x/dex/keeper/pol_burn.go":                     "pol_retire",
	"x/personhood/keeper/abci.go":                  "anml_buyback",
	"x/allocation/keeper/prune.go":                 "allocation",
	"x/allocation/keeper/msg_server_add_option.go": "allocation",

	// Deliberately uncounted. Withdrawing liquidity burns the shares that
	// represented the claim; the assets behind them go back to their owner and
	// no supply leaves. Counting these would report an ordinary withdrawal as
	// the largest burn the chain had ever done.
	"x/dex/keeper/shares.go": "NOT COUNTED: LP shares are a claim on a pool, not supply",
}

// TestEveryBurnSiteIsAccountedFor walks the modules for calls to BurnCoins and
// holds them against the list above.
func TestEveryBurnSiteIsAccountedFor(t *testing.T) {
	root := ".."

	var found []string
	err := filepath.Walk(filepath.Join(root, "x"), func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		// Generated code and tests do not burn anything a node will execute, and
		// expected_keepers.go declares the interface rather than calling it.
		name := filepath.Base(path)
		if strings.HasSuffix(name, "_test.go") || strings.HasSuffix(name, ".pb.go") ||
			name == "expected_keepers.go" {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		// A call has a receiver; the interface declaration does not.
		if strings.Contains(string(body), ".BurnCoins(") {
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			found = append(found, rel)
		}
		return nil
	})
	require.NoError(t, err)

	want := make([]string, 0, len(burnSites))
	for site := range burnSites {
		want = append(want, site)
	}
	sort.Strings(want)
	sort.Strings(found)

	require.Equal(t, want, found,
		"a burn site was added or moved. Add it to burnSites with the source it is "+
			"counted under, or with a reason it must not be — and if it is counted, the "+
			"RecordBurn goes in the same context as the BurnCoins, not outside it.")
}

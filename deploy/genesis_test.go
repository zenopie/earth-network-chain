// Package deploy holds the launch genesis and the checks that keep it honest.
//
// genesis.json is a build artifact — scripts/build-genesis.sh writes it from the
// sources in deploy/genesis/, and nobody edits it by hand. These tests read the
// committed file and re-derive everything about it that can be derived, so a
// mistake in the sources, in the script, or in an edit somebody made anyway
// fails here rather than at height 1 on somebody else's node.
//
// They deliberately do not run the build: they are fast, need no toolchain, and
// answer "is the file we are shipping self-consistent". For "does the file still
// match its sources", run scripts/build-genesis.sh --check.
package deploy

import (
	"encoding/json"
	"math/big"
	"os"
	"testing"
)

type coin struct {
	Denom  string `json:"denom"`
	Amount string `json:"amount"`
}

type genesisDoc struct {
	GenesisTime   string `json:"genesis_time"`
	ChainID       string `json:"chain_id"`
	InitialHeight int64  `json:"initial_height"`
	AppName       string `json:"app_name"`
	AppVersion    string `json:"app_version"`
	Consensus     struct {
		Params struct {
			Version struct {
				App string `json:"app"`
			} `json:"version"`
		} `json:"params"`
	} `json:"consensus"`
	AppState struct {
		Bank struct {
			DenomMetadata []struct {
				Description string `json:"description"`
				DenomUnits  []struct {
					Denom    string `json:"denom"`
					Exponent int    `json:"exponent"`
				} `json:"denom_units"`
				Base    string `json:"base"`
				Display string `json:"display"`
				Name    string `json:"name"`
				Symbol  string `json:"symbol"`
			} `json:"denom_metadata"`
			Supply   []coin `json:"supply"`
			Balances []struct {
				Address string `json:"address"`
				Coins   []coin `json:"coins"`
			} `json:"balances"`
		} `json:"bank"`
		Dex struct {
			PoolMap []struct {
				PoolID       string `json:"pool_id"`
				ReserveErth  coin   `json:"reserve_erth"`
				ReserveToken coin   `json:"reserve_token"`
			} `json:"pool_map"`
			LiquidityAuction *struct {
				ErthForBidders coin `json:"erth_for_bidders"`
				ErthForPool    coin `json:"erth_for_pool"`
			} `json:"liquidity_auction"`
			PolBurns []struct {
				PoolID          string `json:"pool_id"`
				TotalShares     string `json:"total_shares"`
				SharesRemaining string `json:"shares_remaining"`
				DurationSeconds string `json:"duration_seconds"`
			} `json:"pol_burns"`
		} `json:"dex"`
		Personhood struct {
			Params struct {
				VerifyingKeys map[string]string `json:"verifying_keys"`
			} `json:"params"`
		} `json:"personhood"`
		PKI struct {
			Cscas []json.RawMessage `json:"cscas"`
		} `json:"pki"`
		Wasm struct {
			Params struct {
				CodeUploadAccess struct {
					Permission string   `json:"permission"`
					Addresses  []string `json:"addresses"`
				} `json:"code_upload_access"`
				InstantiateDefaultPermission string `json:"instantiate_default_permission"`
			} `json:"params"`
		} `json:"wasm"`
	} `json:"app_state"`
}

type accountsDoc struct {
	Keyed []struct {
		Address string `json:"address"`
		Coins   string `json:"coins"`
	} `json:"keyed"`
	Modules []struct {
		Name    string `json:"name"`
		Address string `json:"address"`
		Coins   []coin `json:"coins"`
	} `json:"modules"`
}

func readJSON[T any](t *testing.T, path string) T {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return v
}

func mustInt(t *testing.T, s, what string) *big.Int {
	t.Helper()
	n, ok := new(big.Int).SetString(s, 10)
	if !ok {
		t.Fatalf("%s: %q is not an integer", what, s)
	}
	return n
}

func loadGenesis(t *testing.T) genesisDoc { return readJSON[genesisDoc](t, "genesis.json") }

// The invariant x/bank checks at InitGenesis, checked here instead so it fails
// in CI rather than on the first node to try to start the chain. It is also the
// one the old hand-editing workflow broke: balances and supply were maintained
// separately, and "recompute bank supply" was a step someone had to remember.
func TestSupplyEqualsSumOfBalances(t *testing.T) {
	g := loadGenesis(t)

	summed := map[string]*big.Int{}
	for _, b := range g.AppState.Bank.Balances {
		for _, c := range b.Coins {
			if summed[c.Denom] == nil {
				summed[c.Denom] = new(big.Int)
			}
			summed[c.Denom].Add(summed[c.Denom], mustInt(t, c.Amount, b.Address+" "+c.Denom))
		}
	}

	declared := map[string]*big.Int{}
	for _, c := range g.AppState.Bank.Supply {
		declared[c.Denom] = mustInt(t, c.Amount, "supply "+c.Denom)
	}

	for denom, want := range summed {
		got, ok := declared[denom]
		if !ok {
			t.Errorf("%s: %s held in balances but absent from supply", denom, want)
			continue
		}
		if got.Cmp(want) != 0 {
			t.Errorf("%s: supply %s but balances sum to %s", denom, got, want)
		}
	}
	for denom, got := range declared {
		if summed[denom] == nil {
			t.Errorf("%s: supply declares %s but no account holds any", denom, got)
		}
	}
}

// Nothing holds a balance except the accounts deploy/genesis/accounts.json names.
// A stray funded address is how a devnet key, a leftover faucet, or an ignite
// scaffold account rides into a launch genesis unnoticed — and once the chain
// starts, whoever has that key has the coins.
func TestOnlyIntendedAccountsHoldBalances(t *testing.T) {
	g := loadGenesis(t)
	accts := readJSON[accountsDoc](t, "genesis/accounts.json")

	intended := map[string]string{}
	for _, a := range accts.Keyed {
		intended[a.Address] = "keyed"
	}
	for _, m := range accts.Modules {
		intended[m.Address] = "module " + m.Name
	}

	seen := map[string]bool{}
	for _, b := range g.AppState.Bank.Balances {
		what, ok := intended[b.Address]
		if !ok {
			t.Errorf("%s holds a genesis balance but is not in accounts.json", b.Address)
			continue
		}
		if seen[b.Address] {
			t.Errorf("%s (%s) appears twice in bank.balances", b.Address, what)
		}
		seen[b.Address] = true
	}
	for addr, what := range intended {
		if !seen[addr] {
			t.Errorf("%s (%s) is in accounts.json but holds nothing", addr, what)
		}
	}
}

// The dex module account escrows every reserve and every earmark in the module,
// so its ERTH balance has to be exactly pool 1's hub reserve plus both auction
// earmarks. A shortfall means the auction cannot pay out or the pool cannot be
// withdrawn from; a surplus is ERTH nothing accounts for.
func TestDexModuleBalanceCoversItsObligations(t *testing.T) {
	g := loadGenesis(t)
	accts := readJSON[accountsDoc](t, "genesis/accounts.json")

	var dexAddr string
	for _, m := range accts.Modules {
		if m.Name == "dex" {
			dexAddr = m.Address
		}
	}
	if dexAddr == "" {
		t.Fatal("accounts.json names no dex module account")
	}

	held := map[string]*big.Int{}
	for _, b := range g.AppState.Bank.Balances {
		if b.Address != dexAddr {
			continue
		}
		for _, c := range b.Coins {
			held[c.Denom] = mustInt(t, c.Amount, "dex "+c.Denom)
		}
	}

	if len(g.AppState.Dex.PoolMap) != 1 {
		t.Fatalf("expected exactly one genesis pool, got %d", len(g.AppState.Dex.PoolMap))
	}
	pool := g.AppState.Dex.PoolMap[0]
	auction := g.AppState.Dex.LiquidityAuction
	if auction == nil {
		t.Fatal("no liquidity auction configured")
	}

	wantErth := new(big.Int).Add(
		mustInt(t, pool.ReserveErth.Amount, "pool reserve_erth"),
		new(big.Int).Add(
			mustInt(t, auction.ErthForBidders.Amount, "erth_for_bidders"),
			mustInt(t, auction.ErthForPool.Amount, "erth_for_pool"),
		),
	)
	if got := held[pool.ReserveErth.Denom]; got == nil || got.Cmp(wantErth) != 0 {
		t.Errorf("dex holds %v %s; pool reserve + both earmarks come to %s",
			got, pool.ReserveErth.Denom, wantErth)
	}

	wantTok := mustInt(t, pool.ReserveToken.Amount, "pool reserve_token")
	if got := held[pool.ReserveToken.Denom]; got == nil || got.Cmp(wantTok) != 0 {
		t.Errorf("dex holds %v %s; pool 1's reserve is %s", got, pool.ReserveToken.Denom, wantTok)
	}

	// The two auction earmarks must be equal, or the pool does not open at the
	// price the auction cleared at and the gap is free to whoever trades first.
	// GenesisState.Validate enforces this too; it is repeated here because it is
	// the kind of thing a well-meaning edit to app_state.json would break.
	if a, b := auction.ErthForBidders.Amount, auction.ErthForPool.Amount; a != b {
		t.Errorf("auction earmarks must be equal, got %s and %s", a, b)
	}
}

// Pool 1's LP shares are sqrt(reserve_erth * reserve_token), the same figure
// CreatePool would mint, and the retirement schedule has to be sized to exactly
// that position. Get this wrong and the schedule either strands shares it can
// never retire or tries to burn more than exist.
func TestPoolSharesAndRetirementScheduleAgree(t *testing.T) {
	g := loadGenesis(t)
	pool := g.AppState.Dex.PoolMap[0]

	want := new(big.Int).Sqrt(new(big.Int).Mul(
		mustInt(t, pool.ReserveErth.Amount, "reserve_erth"),
		mustInt(t, pool.ReserveToken.Amount, "reserve_token"),
	))

	var supply *big.Int
	for _, c := range g.AppState.Bank.Supply {
		if c.Denom == "dexlp/1" {
			supply = mustInt(t, c.Amount, "dexlp/1 supply")
		}
	}
	if supply == nil {
		t.Fatal("no dexlp/1 supply in genesis")
	}
	if supply.Cmp(want) != 0 {
		t.Errorf("dexlp/1 supply is %s; sqrt(reserve_erth * reserve_token) is %s", supply, want)
	}

	if len(g.AppState.Dex.PolBurns) != 1 {
		t.Fatalf("expected one pol burn schedule at genesis, got %d", len(g.AppState.Dex.PolBurns))
	}
	burn := g.AppState.Dex.PolBurns[0]
	if burn.PoolID != "1" {
		t.Errorf("the genesis retirement schedule should be for pool 1, got %s", burn.PoolID)
	}
	if total := mustInt(t, burn.TotalShares, "total_shares"); total.Cmp(supply) != 0 {
		t.Errorf("schedule retires %s shares but only %s exist", total, supply)
	}
	if burn.SharesRemaining != burn.TotalShares {
		t.Errorf("a genesis schedule has not retired anything yet: total_shares %s, shares_remaining %s",
			burn.TotalShares, burn.SharesRemaining)
	}
	if burn.DurationSeconds != "157788000" {
		t.Errorf("retirement should run five Julian years (157788000s), got %s", burn.DurationSeconds)
	}
}

// With no verifying keys, MsgRegister always fails — so the ANML claim, the
// democratic pillar and the whole reason the chain exists sit inert until a
// governance proposal fixes it. Every key file in the sources has to reach the
// genesis file.
func TestVerifyingKeysAreSeeded(t *testing.T) {
	g := loadGenesis(t)

	entries, err := os.ReadDir("genesis/verifying-keys")
	if err != nil {
		t.Fatalf("read verifying-keys: %v", err)
	}
	want := 0
	for _, e := range entries {
		name := e.Name()
		if len(name) < len(".vk.b64") || name[len(name)-len(".vk.b64"):] != ".vk.b64" {
			continue
		}
		want++
		cid := name[:len(name)-len(".vk.b64")]
		if g.AppState.Personhood.Params.VerifyingKeys[cid] == "" {
			t.Errorf("circuit %q has a key file but no key in genesis", cid)
		}
	}
	if want == 0 {
		t.Fatal("no verifying keys in the sources")
	}
	if got := len(g.AppState.Personhood.Params.VerifyingKeys); got != want {
		t.Errorf("genesis carries %d verifying keys, the sources have %d", got, want)
	}
}

// x/pki only accepts a DSC that chains to one of these, so an empty or truncated
// trust store decides that no passport can ever register.
func TestCscaTrustStoreIsSeeded(t *testing.T) {
	g := loadGenesis(t)
	if n := len(g.AppState.PKI.Cscas); n < 500 {
		t.Errorf("genesis carries %d CSCAs; the ICAO master list plus the additional "+
			"certificates come to 539 — a short count means the trust store did not "+
			"fully parse", n)
	}
}

// The header is what a network agrees on before it agrees on anything else.
func TestHeaderMatchesChainJSON(t *testing.T) {
	g := loadGenesis(t)
	c := readJSON[struct {
		ChainID       string `json:"chain_id"`
		GenesisTime   string `json:"genesis_time"`
		InitialHeight int64  `json:"initial_height"`
		AppVersion    string `json:"app_version"`
		AppName       string `json:"app_name"`
		BinaryVersion string `json:"binary_version"`
	}](t, "genesis/chain.json")

	for _, tc := range []struct{ what, got, want string }{
		{"chain_id", g.ChainID, c.ChainID},
		{"genesis_time", g.GenesisTime, c.GenesisTime},
		{"consensus app version", g.Consensus.Params.Version.App, c.AppVersion},
		{"app_name", g.AppName, c.AppName},
		{"app_version", g.AppVersion, c.BinaryVersion},
	} {
		if tc.got != tc.want {
			t.Errorf("%s is %q in genesis.json, %q in chain.json — run scripts/build-genesis.sh",
				tc.what, tc.got, tc.want)
		}
	}
	if g.InitialHeight != c.InitialHeight {
		t.Errorf("initial_height is %d in genesis.json, %d in chain.json",
			g.InitialHeight, c.InitialHeight)
	}
}

// CosmWasm on earth is permissionless: anyone may upload code and anyone may
// instantiate it, paying only gas.
//
// These match wasmd's compiled-in defaults, so this test looks redundant with
// app.TestWasmIsPermissionless — it is not. That one checks what the binary
// would do with no genesis at all; this one checks the file the network actually
// launches from. They can disagree in both directions: a wasmd bump could change
// the default under a genesis that still says Everybody, or someone could
// "simplify" app_state.json by deleting a block that only restates a default and
// leave the launched chain at whatever the next version decides.
//
// Tightening either value is a real decision — it means contracts arrive by
// governance proposal instead of by transaction. It should fail here and be
// changed deliberately, not arrive as a diff nobody read.
func TestWasmGenesisIsPermissionless(t *testing.T) {
	g := loadGenesis(t)
	params := g.AppState.Wasm.Params

	if params.CodeUploadAccess.Permission != "Everybody" {
		t.Errorf("wasm code_upload_access is %q, want %q: uploading a contract would require governance",
			params.CodeUploadAccess.Permission, "Everybody")
	}
	if n := len(params.CodeUploadAccess.Addresses); n != 0 {
		t.Errorf("wasm code_upload_access names %d address(es); an Everybody permission should name none", n)
	}
	if params.InstantiateDefaultPermission != "Everybody" {
		t.Errorf("wasm instantiate_default_permission is %q, want %q",
			params.InstantiateDefaultPermission, "Everybody")
	}
}

// Wallets read denom metadata to show "1.5 ERTH" rather than "1500000uerth".
// Without it Keplr and every block explorer fall back to the base micro-denom,
// because nothing on chain tells them the display unit or its exponent.
//
// This is checked here rather than left to `validate-genesis` because the SDK
// only validates metadata that is present: an empty denom_metadata is a
// perfectly valid genesis, and the chain launched once already with exactly
// that. Absence is the failure mode, and absence is what the SDK cannot catch.
//
// There is no MsgSetDenomMetadata, so the only ways to fix a genesis that ships
// without this are a governance proposal executing as the bank authority, or a
// relaunch.
func TestDenomMetadataIsSeeded(t *testing.T) {
	g := loadGenesis(t)

	want := map[string]struct{ display, symbol string }{
		"uerth": {"erth", "ERTH"},
		"uanml": {"anml", "ANML"},
	}

	got := map[string]bool{}
	for _, m := range g.AppState.Bank.DenomMetadata {
		got[m.Base] = true

		w, ok := want[m.Base]
		if !ok {
			continue // extra metadata is allowed; the two below are required
		}
		if m.Display != w.display {
			t.Errorf("%s display is %q, want %q", m.Base, m.Display, w.display)
		}
		if m.Symbol != w.symbol {
			t.Errorf("%s symbol is %q, want %q", m.Base, m.Symbol, w.symbol)
		}
		if m.Name == "" || m.Description == "" {
			t.Errorf("%s has a blank name or description; the SDK rejects a blank name and wallets show the description", m.Base)
		}

		// The SDK checks unit ordering itself. What it cannot check is that the
		// exponent matches what this chain actually means by one ERTH, which is
		// 1e6 micro-units everywhere else in the repo. An exponent that
		// disagrees misprices every balance a wallet displays by a factor of
		// ten, silently and in the user's favour or against it.
		if len(m.DenomUnits) != 2 {
			t.Fatalf("%s has %d denom units, want 2 (base and display)", m.Base, len(m.DenomUnits))
		}
		if m.DenomUnits[0].Denom != m.Base || m.DenomUnits[0].Exponent != 0 {
			t.Errorf("%s first unit is %s/%d, want %s/0", m.Base, m.DenomUnits[0].Denom, m.DenomUnits[0].Exponent, m.Base)
		}
		if m.DenomUnits[1].Denom != w.display || m.DenomUnits[1].Exponent != 6 {
			t.Errorf("%s display unit is %s/%d, want %s/6", m.Base, m.DenomUnits[1].Denom, m.DenomUnits[1].Exponent, w.display)
		}
	}

	for base := range want {
		if !got[base] {
			t.Errorf("no denom metadata for %s; wallets will show the raw micro-denom", base)
		}
	}
}

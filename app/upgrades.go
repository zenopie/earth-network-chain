package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	storetypes "cosmossdk.io/store/types"
	upgradetypes "cosmossdk.io/x/upgrade/types"
	"github.com/cosmos/cosmos-sdk/types/module"

	dextypes "github.com/earth-network/earth/x/dex/types"
	"github.com/earth-network/earth/x/pki/certs"
	pkitypes "github.com/earth-network/earth/x/pki/types"
)

// Upgrade is one named, governance-scheduled chain upgrade.
//
// When a MsgSoftwareUpgrade proposal passes, the chain halts at the plan height
// with `UPGRADE "<name>" NEEDED`. Operators then swap in a binary whose Upgrades
// list contains an entry with a matching Name; that binary migrates state and
// continues. A binary without the matching entry halts again at the same height.
type Upgrade struct {
	// Name must exactly match the name in the passed MsgSoftwareUpgrade plan.
	Name string

	// CreateHandler runs once, at the upgrade height, before any block is
	// processed. Return RunMigrations to execute the registered module
	// migrations; add bespoke state fixes around it as needed.
	CreateHandler func(app *App) upgradetypes.UpgradeHandler

	// StoreUpgrades declares module stores added, renamed or deleted by this
	// upgrade. This is NOT optional when the module set changes: the commit
	// multistore has to be told about a new store key before it can be loaded,
	// and the chain will fail to start otherwise.
	StoreUpgrades storetypes.StoreUpgrades
}

// Upgrades lists every upgrade this binary can perform. Entries are kept after
// they have been applied, so a node syncing from genesis replays each one in
// turn.
//
// To add an upgrade named "v2" that introduces a new module store:
//
//	{
//	    Name: "v2",
//	    CreateHandler: func(app *App) upgradetypes.UpgradeHandler {
//	        return func(ctx context.Context, plan upgradetypes.Plan, fromVM module.VersionMap) (module.VersionMap, error) {
//	            return app.ModuleManager.RunMigrations(ctx, app.Configurator(), fromVM)
//	        }
//	    },
//	    StoreUpgrades: storetypes.StoreUpgrades{Added: []string{"newmodule"}},
//	}
var Upgrades = []Upgrade{
	// Removes MsgUnregister. Freeing a nullifier made its holder a stranger to
	// Register, which pays the registration reward and mints 1 ANML for any
	// nullifier that is not already live -- so unregister-then-register drew on
	// the human stream's reward pool once per block. See
	// x/personhood/keeper/msg_server_unregister.go.
	//
	// No state migration: the change is entirely in what the handler accepts, and
	// registrations retired before this height stay retired. No StoreUpgrades
	// either -- the module set is unchanged. AppVersion also stays at 1; it is
	// pinned to networks/genesis/chain.json, which x/upgrade does not move, and
	// editing that file would change the genesis hash that
	// RESET_ON_GENESIS_MISMATCH is keyed to.
	{
		Name:          "v0.6.0",
		CreateHandler: defaultUpgradeHandler,
	},

	// Everything after v0.6.0, under one name because it ships in one binary.
	//
	// This entry is the merge of two releases. v0.6.1 was tagged and built but
	// never proposed — no MsgSoftwareUpgrade named "v0.6.1" ever existed on
	// earth-1 — so folding it in costs nothing and removes a governance round
	// trip. That is the whole reason the merge is legitimate: an upgrade name
	// that was never voted on is not a promise anyone made. Had it been
	// scheduled, this entry would have had to stay, because a node replaying
	// history needs a handler for every name the chain actually halted on.
	//
	// From the tagged-but-unproposed v0.6.1:
	//
	//  1. x/dex refuses an LP share denom as a pool asset -- a chain halt
	//     anyone could trigger from an ordinary message.
	//  2. x/dex honours MsgAddLiquidity.min_shares, so a deposit can state what
	//     it will accept rather than taking whatever ratio it lands on.
	//  3. x/personhood carries a registration's ANML clock across a wallet
	//     switch, which used to cost the mover that day's claim.
	//  4. x/pki bounds what one certificate verification can cost -- see
	//     certs.MaxPublicKeyBytes and types.MaxIssuerCandidates.
	//
	// New in v0.7.0, from the 2026-08-28 audit:
	//
	//  5. x/allocation decrements SummedAccrued when a pruned option's balance
	//     is burned. Without it the sweep burned coins the ledger kept counting,
	//     and the module went short in the same block -- BeginBlock prunes,
	//     EndBlock asserts. Permissionlessly armable on a 30-day fuse, and
	//     likelier still to fire by accident the first time a real option was
	//     abandoned unclaimed.
	//  6. x/allocation rebuilds SummedAccrued on genesis import and carries
	//     Residue through it. Neither survived an export/import, which left the
	//     solvency check reading a genuine shortfall as a tolerated surplus.
	//  7. x/dex drops a malformed LP unbonding instead of halting on it, and
	//     genesis validation refuses the three shapes that produce one. The old
	//     behaviour was a permanent halt from a single bad row: the entry is
	//     removed only after a successful payout, and the queue is ordered by
	//     due time, so it sat at the head failing identically every block.
	//  8. The DSC commitment absorbs a curve tag, so two same-width curves
	//     cannot share a Document Signer identity. See certs.DscCommitment.
	//
	// (1), (5) and (7) all have state preconditions, and (8) rewrites state, so
	// the handler is emphatically not the default one. See upgrades_v070.go.
	//
	// No StoreUpgrades: the module set is unchanged. AppVersion stays at 1, for
	// the reason v0.6.0 gives above.
	//
	// (2) is wire-compatible: min_shares is a new field, and a client that sends
	// nothing gets exactly the old behaviour, so transactions already signed and
	// in flight are unaffected and historical ones still decode.
	//
	// (8) is NOT wire-compatible with an un-updated app: a proof from the old
	// circuits produces an old-format commitment that this binary will not
	// reproduce, so registration needs the new circuits in users' hands. That is
	// the one item with a dependency outside this repository.
	{
		Name:          "v0.7.0",
		CreateHandler: upgradeV070,
	},

	// Makes the caretaker slate un-resettable. MsgResetAllocations now rejects
	// STREAM_ID_CARETAKER outright: stake-weighted x/gov is the capital axis, and
	// a repeatable reset of the human slate is a mute button the persons axis has
	// no matching lever against. See
	// x/allocation/keeper/msg_server_reset_allocations.go for the trade this
	// accepts -- it gives up the sybil backstop, and recovery from a captured
	// caretaker slate becomes a binary upgrade.
	//
	// No state migration, no StoreUpgrades: the change is entirely in what the
	// handler accepts. Existing votes, epochs and accrued balances are untouched,
	// and a caretaker reset that already happened stays happened. AppVersion
	// stays at 1 for the reason given on v0.6.0.
	{
		Name:          "v0.8.0",
		CreateHandler: defaultUpgradeHandler,
	},
}

// assertTrustStoreParses checks that no CSCA already in the store is one the
// new key-size ceiling would reject.
//
// The failure this catches is silent, which is why it is worth a check.
// issuerCandidates skips a CSCA whose certificate will not parse — the store
// has always held a few of those and skipping them is correct — so a trust
// anchor pushed past certs.MaxPublicKeyBytes by this upgrade would simply stop
// being consulted, with no error anywhere. The first sign would be a country's
// passports failing to register for no visible reason.
//
// Only ErrPublicKeyTooLarge counts. A certificate that failed to parse before
// this upgrade was already being skipped and is not this upgrade's doing.
func assertTrustStoreParses(ctx context.Context, app *App) error {
	var oversized int
	err := app.PkiKeeper.Cscas.Walk(ctx, nil, func(_ []byte, c pkitypes.Csca) (bool, error) {
		if _, err := certs.ParseCert(c.CertificateDer); errors.Is(err, certs.ErrPublicKeyTooLarge) {
			oversized++
		}
		return false, nil
	})
	if err != nil {
		return fmt.Errorf("v0.7.0: could not read the CSCA trust store: %w", err)
	}
	if oversized > 0 {
		return fmt.Errorf(
			"v0.7.0 refuses to run: %d CSCA(s) in the trust store carry a public key "+
				"larger than the new ceiling of %d bytes and would silently stop being "+
				"trusted. Raise certs.MaxPublicKeyBytes to cover them before upgrading",
			oversized, certs.MaxPublicKeyBytes)
	}
	return nil
}

// assertNoLpDenomPools walks the pool set looking for a reserve denominated in
// LP shares. O(pools), which is affordable exactly once, in an upgrade handler.
func assertNoLpDenomPools(ctx context.Context, app *App) error {
	var offending []string
	err := app.DexKeeper.Pool.Walk(ctx, nil, func(id uint64, p dextypes.Pool) (bool, error) {
		if dextypes.IsLPShareDenom(p.ReserveToken.Denom) || dextypes.IsLPShareDenom(p.ReserveErth.Denom) {
			offending = append(offending,
				fmt.Sprintf("pool %d (%s / %s)", id, p.ReserveErth.Denom, p.ReserveToken.Denom))
		}
		return false, nil
	})
	if err != nil {
		return fmt.Errorf("v0.7.0: could not read the pool set: %w", err)
	}
	if len(offending) > 0 {
		return fmt.Errorf(
			"v0.7.0 refuses to run: %d pool(s) hold an lp share denom and the new "+
				"SetPool guard would halt the chain on the next write to them: %s. "+
				"Retire these pools in a bespoke handler before upgrading",
			len(offending), strings.Join(offending, ", "))
	}
	return nil
}

// setupUpgrades registers the upgrade handlers and, if the node is restarting
// into a pending upgrade, installs the store loader that applies its
// StoreUpgrades.
//
// MUST be called before app.Load(): the store loader has to be in place before
// the multistore is read, and BaseApp panics if it is set afterwards.
func (app *App) setupUpgrades() {
	for _, u := range Upgrades {
		app.UpgradeKeeper.SetUpgradeHandler(u.Name, u.CreateHandler(app))
	}

	upgradeInfo, err := app.UpgradeKeeper.ReadUpgradeInfoFromDisk()
	if err != nil {
		panic(fmt.Sprintf("failed to read upgrade info from disk: %v", err))
	}
	// No pending upgrade, or the operator explicitly chose to skip this height.
	if upgradeInfo.Name == "" || app.UpgradeKeeper.IsSkipHeight(upgradeInfo.Height) {
		return
	}

	for _, u := range Upgrades {
		if u.Name != upgradeInfo.Name {
			continue
		}
		// Copy: the loader keeps the pointer past this loop iteration.
		storeUpgrades := u.StoreUpgrades
		app.SetStoreLoader(upgradetypes.UpgradeStoreLoader(upgradeInfo.Height, &storeUpgrades))
		return
	}
}

// defaultUpgradeHandler runs the standard module migrations and nothing else.
// Suitable for any upgrade that changes logic or parameters but not the set of
// modules.
//
// The shape every upgrade starts from.
func defaultUpgradeHandler(app *App) upgradetypes.UpgradeHandler {
	return func(ctx context.Context, _ upgradetypes.Plan, fromVM module.VersionMap) (module.VersionMap, error) {
		return app.ModuleManager.RunMigrations(ctx, app.Configurator(), fromVM)
	}
}

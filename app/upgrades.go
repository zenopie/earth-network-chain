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

	// Four consensus-affecting changes, shipped under one name because they ship
	// in one binary. Nothing here lives in the handler except the two pre-flight
	// checks: what actually changes is what these message handlers accept, so
	// the upgrade name is the release, and the release is the tag CI builds.
	//
	//  1. x/dex refuses an LP share denom as a pool asset -- the chain halt.
	//  2. x/dex honours MsgAddLiquidity.min_shares, so a deposit can state what
	//     it will accept rather than taking whatever ratio it lands on.
	//  3. x/personhood carries a registration's ANML clock across a wallet
	//     switch, which used to cost the mover that day's claim.
	//  4. x/pki bounds what one certificate verification can cost -- see
	//     certs.MaxPublicKeyBytes and types.MaxIssuerCandidates.
	//
	// (1) is the one with a state precondition and the reason this handler is
	// not the default one; (4) has one too, and both are checked below.
	//
	// --- (1) the halt ---
	//
	// x/dex allowed dexlp/N as a pool's spoke token, and the solvency check
	// cannot survive one. checkPoolTokenSolvency compares a pool's spoke reserve
	// against the module's whole balance of that denom, which is exact only
	// because one pool per token means nothing else claims it. LP shares break
	// that: the module also holds them as the protocol's own position and as
	// escrow against withdrawals in flight, and CheckBalances skips LP denoms on
	// the held side for exactly that reason. A pool claiming one as its reserve
	// makes every other such coin an unaccountable surplus, and a surplus out of
	// the EndBlocker halts the chain. Anyone can hold a dust amount of any
	// pool's shares, so it was a halt available to anybody from an ordinary
	// message. See x/dex/keeper/msg_server_create_pool.go.
	//
	// Its own upgrade rather than folding into v0.6.0, which is in its voting
	// period and already tagged. The behaviour of both upgrades lives in the
	// binary rather than in a handler, so adding this to that entry would put a
	// change into a release voters had already approved without it.
	//
	// No StoreUpgrades: the module set is unchanged. AppVersion stays at 1, for
	// the reason v0.6.0 gives above.
	//
	// The handler is not the default one, because two of these add rules that
	// existing state could violate, and state keeps moving between writing this
	// and running it -- so both are checked at the upgrade height rather than by
	// hand beforehand. SetPool now rejects a pool whose reserve is an LP denom
	// and is reached from EndBlocker paths, so a pool already carrying one would
	// turn this upgrade into the halt it prevents; and a CSCA already in the
	// trust store that the new key ceiling rejects would stop being a trust
	// anchor silently. See assertNoLpDenomPools and assertTrustStoreParses.
	//
	// (2) is wire-compatible: min_shares is a new field, and a client that sends
	// nothing gets exactly the old behaviour, so transactions already signed and
	// in flight are unaffected and historical ones still decode.
	{
		Name:          "v0.6.1",
		CreateHandler: upgradeV061,
	},
}

// upgradeV061 runs this release's two state preconditions, then the standard
// migrations.
//
// The new guard in SetPool cannot distinguish a pool being created from a pool
// being written for the thousandth time, so if legacy state contains one, every
// later touch of it fails — from the EndBlocker, at an arbitrary height, with
// no operator watching. Failing here instead puts the same problem at a
// scheduled height with everyone already looking at the chain, and says what to
// do about it.
//
// Expected never to fire. Creating such a pool needs MsgCreatePool, which is
// refused while the genesis liquidity auction is unsettled, so as long as this
// upgrade lands before the auction opens there is no window in which one can be
// made. That ordering is the actual safety property; this check is what makes
// it verified rather than assumed.
func upgradeV061(app *App) upgradetypes.UpgradeHandler {
	return func(ctx context.Context, _ upgradetypes.Plan, fromVM module.VersionMap) (module.VersionMap, error) {
		if err := assertNoLpDenomPools(ctx, app); err != nil {
			return nil, err
		}
		if err := assertTrustStoreParses(ctx, app); err != nil {
			return nil, err
		}
		return app.ModuleManager.RunMigrations(ctx, app.Configurator(), fromVM)
	}
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
		return fmt.Errorf("v0.6.1: could not read the CSCA trust store: %w", err)
	}
	if oversized > 0 {
		return fmt.Errorf(
			"v0.6.1 refuses to run: %d CSCA(s) in the trust store carry a public key "+
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
		return fmt.Errorf("v0.6.1: could not read the pool set: %w", err)
	}
	if len(offending) > 0 {
		return fmt.Errorf(
			"v0.6.1 refuses to run: %d pool(s) hold an lp share denom and the new "+
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

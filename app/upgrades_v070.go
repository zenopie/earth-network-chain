package app

import (
	"context"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"

	"cosmossdk.io/collections"
	upgradetypes "cosmossdk.io/x/upgrade/types"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	"github.com/cosmos/cosmos-sdk/types/module"

	dextypes "github.com/earth-network/earth/x/dex/types"
	personhoodtypes "github.com/earth-network/earth/x/personhood/types"
	"github.com/earth-network/earth/x/pki/certs"
	"github.com/earth-network/earth/zk/poseidon2"
)

// The verifying keys for the recompiled register circuits, and the Document
// Signer certificates whose commitments this upgrade rewrites.
//
// These are embedded rather than read from disk because they are consensus: the
// handler writes them into state at the upgrade height, so every node has to
// produce byte-identical results from the same binary. A file the operator could
// substitute would be a consensus fork waiting to happen.
//
// Deliberately NOT networks/genesis/verifying-keys/. Those are the keys block 0
// installs, they are baked into networks/genesis.json, and that file's sha256 is
// what RESET_ON_GENESIS_MISMATCH is keyed to — regenerating them would wipe every
// node's data directory. Genesis keeps the old keys for ever, because replaying
// from block 0 needs exactly the keys the chain launched with.
//
//go:embed upgrades/v070/verifying-keys/*.vk.b64
//go:embed upgrades/v070/dsc-certs/*.der
var v070Assets embed.FS

const (
	v070VerifyingKeyDir = "upgrades/v070/verifying-keys"
	v070DscCertDir      = "upgrades/v070/dsc-certs"
)

// upgradeV070 is the combined release: the two chain halts, the genesis
// blindness, and the DSC commitment domain separation, plus the four fixes that
// were tagged as v0.6.1 and never proposed.
//
// Ordering inside the handler matters and is not arbitrary:
//
//  1. Preconditions first, all of them, before a single write. A handler that
//     half-migrates and then refuses is worse than one that refuses.
//  2. The DSC commitment remap, which rewrites registration state.
//  3. The verifying-key swap, which must land in the same state transition as
//     the binary's new commitment format — see the note on
//     v070SwapVerifyingKeys.
//  4. Module migrations last, as usual.
func upgradeV070(app *App) upgradetypes.UpgradeHandler {
	return func(ctx context.Context, _ upgradetypes.Plan, fromVM module.VersionMap) (module.VersionMap, error) {
		// --- preconditions, carried forward from v0.6.1 ---
		if err := assertNoLpDenomPools(ctx, app); err != nil {
			return nil, err
		}
		if err := assertTrustStoreParses(ctx, app); err != nil {
			return nil, err
		}
		// --- preconditions new to v0.7.0 ---
		if err := assertUnbondingsAreWellFormed(ctx, app); err != nil {
			return nil, err
		}
		if err := assertAllocationLedgerBalances(ctx, app); err != nil {
			return nil, err
		}

		remap, err := v070CommitmentRemap()
		if err != nil {
			return nil, err
		}
		if err := v070RemapDscCommitments(ctx, app, remap); err != nil {
			return nil, err
		}
		if err := v070SwapVerifyingKeys(ctx, app); err != nil {
			return nil, err
		}
		return app.ModuleManager.RunMigrations(ctx, app.Configurator(), fromVM)
	}
}

// --- preconditions --------------------------------------------------------

// assertUnbondingsAreWellFormed refuses to run if the queue already holds an
// entry this release's genesis validation would have rejected.
//
// Expected never to fire: MsgRemoveLiquidity has always validated all three
// fields, and the only way in is a genesis import. It is checked anyway because
// state keeps moving between writing this and running it, and because the whole
// point of the item is that nobody was looking at this list.
//
// It does not halt on such an entry any more — SweepMaturedUnbondings drops it
// and emits lp_unbond_payout_failed. But a dropped entry is a provider's
// liquidity not being returned, so if one exists the operator should know at a
// scheduled height, with everyone watching, rather than from an event nobody
// subscribed to.
func assertUnbondingsAreWellFormed(ctx context.Context, app *App) error {
	var bad []string
	err := app.DexKeeper.LpUnbondings.Walk(ctx, nil,
		func(_ collections.Triple[int64, uint64, []byte], u dextypes.LpUnbonding) (bool, error) {
			switch {
			case u.Shares.Amount.IsNil() || !u.Shares.Amount.IsPositive():
				bad = append(bad, fmt.Sprintf("pool %d / %s: shares are %s", u.PoolId, u.Address, u.Shares.Amount))
			case u.Shares.Denom != dextypes.LPShareDenom(u.PoolId):
				bad = append(bad, fmt.Sprintf("pool %d / %s: shares are %s", u.PoolId, u.Address, u.Shares.Denom))
			default:
				if _, err := app.DexKeeper.Pool.Get(ctx, u.PoolId); err != nil {
					bad = append(bad, fmt.Sprintf("pool %d / %s: no such pool", u.PoolId, u.Address))
				}
			}
			return false, nil
		})
	if err != nil {
		return fmt.Errorf("v0.7.0: could not read the lp unbonding queue: %w", err)
	}
	if len(bad) > 0 {
		return fmt.Errorf(
			"v0.7.0 refuses to run: %d malformed lp unbonding(s) are queued and would be dropped "+
				"unpaid by the new sweep: %s. Settle these positions in a bespoke handler before upgrading",
			len(bad), strings.Join(bad, "; "))
	}
	return nil
}

// assertAllocationLedgerBalances checks the running accrued sum against the
// options it claims to sum, and the module's balance against both.
//
// This is the walked version of the check the EndBlocker runs bounded. If the
// prune bug had already fired on this chain the module would be short here, and
// this upgrade is the moment to say so — the alternative is that the fix lands,
// the ledger is still wrong from before, and the chain halts on the first block
// after the upgrade with the fix apparently to blame.
func assertAllocationLedgerBalances(ctx context.Context, app *App) error {
	if err := app.AllocationKeeper.AssertInvariants(ctx); err != nil {
		return fmt.Errorf(
			"v0.7.0 refuses to run: the allocation ledger does not balance before the upgrade, "+
				"so the new solvency check would halt the chain on the first block after it: %w", err)
	}
	return nil
}

// --- the DSC commitment remap ---------------------------------------------

// legacyDscCommitment is the pre-v0.7.0 commitment: Poseidon2 over the canonical
// public-key bytes with no curve tag.
//
// Frozen. It exists only to recognise the commitments already in state, so it
// must never be "kept in sync" with certs.DscCommitment — the two differing is
// the entire mechanism by which this migration finds what to rewrite.
func legacyDscCommitment(pubkey []byte) fr.Element {
	elems := make([]fr.Element, len(pubkey))
	for i, b := range pubkey {
		elems[i].SetUint64(uint64(b))
	}
	return poseidon2.Hash(elems)
}

// v070CommitmentRemap builds old-commitment -> new-commitment from the embedded
// certificates.
//
// The chain stores Registration.dsc_key and never the public key behind it, so a
// commitment already in state cannot be recomputed in the new format from state
// alone — the input is gone. The certificates are recovered from the historical
// MsgRegister transactions that created the registrations, which do carry them,
// and embedded here.
//
// That is only affordable because there is one registration on earth-1. Past
// some number this stops being a migration anyone can perform, which is the
// reason this item was not deferred a second time.
func v070CommitmentRemap() (map[string][]byte, error) {
	entries, err := v070Assets.ReadDir(v070DscCertDir)
	if err != nil {
		return nil, fmt.Errorf("v0.7.0: could not read the embedded DSC certificates: %w", err)
	}
	remap := make(map[string][]byte, len(entries))
	for _, e := range entries {
		der, err := v070Assets.ReadFile(path.Join(v070DscCertDir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("v0.7.0: could not read %s: %w", e.Name(), err)
		}
		cert, err := certs.ParseCert(der)
		if err != nil {
			return nil, fmt.Errorf("v0.7.0: could not parse %s: %w", e.Name(), err)
		}
		canonical := cert.PublicKey.CanonicalBytes()
		oldC := legacyDscCommitment(canonical)
		oldB := oldC.Bytes()
		newC, err := certs.DscCommitmentOf(cert.PublicKey)
		if err != nil {
			return nil, fmt.Errorf("v0.7.0: %s: %w", e.Name(), err)
		}
		newB := newC.Bytes()
		remap[string(oldB[:])] = append([]byte(nil), newB[:]...)
	}
	return remap, nil
}

// v070RemapDscCommitments rewrites every place a DSC commitment is used as an
// identity.
//
// Six keyspaces, and missing one is a silent failure rather than a loud one:
// an un-remapped RegByDsc entry orphans the index, an un-remapped
// RevokedDscCommitments entry means a revoked signer's registrations quietly
// stop being retired.
//
// A commitment with no known certificate is a hard error. Skipping it would
// leave a registration whose dsc_key can never again be produced by the chain
// from any certificate — unrevokable, and invisible to every by-signer query —
// which is precisely the stranding this migration exists to avoid.
func v070RemapDscCommitments(ctx context.Context, app *App, remap map[string][]byte) error {
	pk := &app.PersonhoodKeeper

	// Collected before anything is written: these are Walks over the same
	// keyspaces the loop below mutates, and mutating a store mid-iteration is
	// undefined.
	type regRewrite struct {
		nullifier []byte
		reg       personhoodtypes.Registration
	}
	var regs []regRewrite
	var unknown []string

	if err := pk.Registrations.Walk(ctx, nil, func(nullifier []byte, reg personhoodtypes.Registration) (bool, error) {
		if len(reg.DscKey) == 0 {
			return false, nil
		}
		if _, ok := remap[string(reg.DscKey)]; !ok {
			unknown = append(unknown, hex.EncodeToString(reg.DscKey))
			return false, nil
		}
		regs = append(regs, regRewrite{nullifier: append([]byte(nil), nullifier...), reg: reg})
		return false, nil
	}); err != nil {
		return fmt.Errorf("v0.7.0: could not read the registrations: %w", err)
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return fmt.Errorf(
			"v0.7.0 refuses to run: %d registration(s) carry a dsc_key no embedded certificate "+
				"reproduces, so their commitments cannot be migrated and they would be stranded "+
				"unrevokable: %s. Recover the DSC certificates from the MsgRegister transactions "+
				"that created them and add them to %s",
			len(unknown), strings.Join(unknown, ", "), v070DscCertDir)
	}

	for _, r := range regs {
		oldKey := append([]byte(nil), r.reg.DscKey...)
		newKey := remap[string(oldKey)]

		// 1. The registration record itself.
		r.reg.DscKey = append([]byte(nil), newKey...)
		if err := pk.Registrations.Set(ctx, r.nullifier, r.reg); err != nil {
			return err
		}

		// 2. The (dscKey, nullifier) index.
		if err := pk.RegByDsc.Remove(ctx, collections.Join(oldKey, r.nullifier)); err != nil &&
			!errors.Is(err, collections.ErrNotFound) {
			return err
		}
		if err := pk.RegByDsc.Set(ctx, collections.Join(newKey, r.nullifier)); err != nil {
			return err
		}
	}

	// 3. The per-signer registration counter, 4. the daily rate counter, and
	// 5. the pending-purge set. Each is keyed by commitment and each is walked
	// whole, because a key that has no registration behind it still has to move
	// — a stale entry under an old commitment would never be found again.
	if err := v070RemapBytesKeyedMap(ctx, pk.RegCountByDsc, remap); err != nil {
		return fmt.Errorf("v0.7.0: reg_count_by_dsc: %w", err)
	}
	if err := v070RemapBytesKeyedMap(ctx, pk.DscRate, remap); err != nil {
		return fmt.Errorf("v0.7.0: dsc_rate: %w", err)
	}
	if err := v070RemapBytesKeySet(ctx, pk.PendingDscPurge, remap); err != nil {
		return fmt.Errorf("v0.7.0: pending_dsc_purge: %w", err)
	}

	// 6. x/pki's revocation set. Empty on earth-1 today, migrated anyway: a
	// revocation recorded under the old format would match no registration
	// after this upgrade, so revocation would silently degrade from "retire the
	// registrations" to "close the door", which is the exact difference
	// RevokeDsc's own comment says the commitment half exists to make.
	if err := v070RemapBytesKeySet(ctx, app.PkiKeeper.RevokedDscCommitments, remap); err != nil {
		return fmt.Errorf("v0.7.0: revoked_dsc_commitments: %w", err)
	}
	return nil
}

// v070RemapBytesKeyedMap rekeys a []byte-keyed map, leaving keys the remap does
// not name untouched.
func v070RemapBytesKeyedMap[V any](ctx context.Context, m collections.Map[[]byte, V], remap map[string][]byte) error {
	type kv struct {
		old []byte
		val V
	}
	var moves []kv
	if err := m.Walk(ctx, nil, func(k []byte, v V) (bool, error) {
		if _, ok := remap[string(k)]; ok {
			moves = append(moves, kv{old: append([]byte(nil), k...), val: v})
		}
		return false, nil
	}); err != nil {
		return err
	}
	for _, mv := range moves {
		if err := m.Remove(ctx, mv.old); err != nil {
			return err
		}
		if err := m.Set(ctx, remap[string(mv.old)], mv.val); err != nil {
			return err
		}
	}
	return nil
}

// v070RemapBytesKeySet is the same for a set.
func v070RemapBytesKeySet(ctx context.Context, s collections.KeySet[[]byte], remap map[string][]byte) error {
	var moves [][]byte
	if err := s.Walk(ctx, nil, func(k []byte) (bool, error) {
		if _, ok := remap[string(k)]; ok {
			moves = append(moves, append([]byte(nil), k...))
		}
		return false, nil
	}); err != nil {
		return err
	}
	for _, old := range moves {
		if err := s.Remove(ctx, old); err != nil {
			return err
		}
		if err := s.Set(ctx, remap[string(old)]); err != nil {
			return err
		}
	}
	return nil
}

// --- the verifying-key swap -----------------------------------------------

// v070SwapVerifyingKeys installs the verifying keys for the recompiled circuits.
//
// This is in the handler rather than in a MsgUpdateParams proposal, and the
// reason is not tidiness. params.VerifyingKeys and certs.DscCommitment have to
// change in the same state transition: a proposal passes at whatever height its
// voting period ends, the binary swaps at the plan height, and in the gap
// between them registration is broken in either ordering — a new-circuit proof
// verifies against a new key and then fails the commitment comparison against
// the old binary, and an old-circuit proof fails the new key outright.
//
// Doing it here also keeps the release honest about being one upgrade: one
// MsgSoftwareUpgrade, no companion proposal, and no window.
//
// Replay is unaffected. This is an ordinary deterministic write at a fixed
// height, so a node syncing from block 0 under cosmovisor runs the old binary
// with the old keys — which are the ones networks/genesis.json installs — up to
// this height, and the new pair after it.
func v070SwapVerifyingKeys(ctx context.Context, app *App) error {
	entries, err := v070Assets.ReadDir(v070VerifyingKeyDir)
	if err != nil {
		return fmt.Errorf("v0.7.0: could not read the embedded verifying keys: %w", err)
	}

	params, err := app.PersonhoodKeeper.Params.Get(ctx)
	if err != nil {
		return err
	}
	if params.VerifyingKeys == nil {
		params.VerifyingKeys = make(map[string][]byte, len(entries))
	}

	installed := make([]string, 0, len(entries))
	for _, e := range entries {
		algo := strings.TrimSuffix(e.Name(), ".vk.b64")
		raw, err := v070Assets.ReadFile(path.Join(v070VerifyingKeyDir, e.Name()))
		if err != nil {
			return fmt.Errorf("v0.7.0: could not read %s: %w", e.Name(), err)
		}
		vk, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
		if err != nil {
			return fmt.Errorf("v0.7.0: %s is not valid base64: %w", e.Name(), err)
		}
		if len(vk) == 0 {
			return fmt.Errorf("v0.7.0: %s decodes to nothing", e.Name())
		}
		// Every algorithm the chain already knows must be replaced, not merely
		// added to. A circuit left on its old key would verify old-format proofs
		// whose commitments the new binary can no longer reproduce, so
		// registration under it would fail the comparison every time — the
		// broken-window failure, made permanent.
		if _, ok := params.VerifyingKeys[algo]; !ok {
			return fmt.Errorf(
				"v0.7.0 refuses to run: the embedded key set names %q, which the chain's params "+
					"do not carry. The two must describe the same circuits", algo)
		}
		params.VerifyingKeys[algo] = vk
		installed = append(installed, algo)
	}

	// And the converse: an algorithm in params with no recompiled key would keep
	// its old key and break exactly as described above.
	for algo := range params.VerifyingKeys {
		found := false
		for _, in := range installed {
			if in == algo {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf(
				"v0.7.0 refuses to run: the chain's params carry %q but no recompiled verifying "+
					"key was embedded for it, so registration under it would break at this height", algo)
		}
	}

	if err := params.Validate(); err != nil {
		return fmt.Errorf("v0.7.0: the swapped params do not validate: %w", err)
	}
	return app.PersonhoodKeeper.Params.Set(ctx, params)
}

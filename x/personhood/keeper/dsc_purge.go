package keeper

import (
	"context"
	"errors"
	"strconv"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/x/personhood/types"
)

// Retiring the registrations a revoked Document Signer produced.
//
// Revocation on its own only closes the door: VerifyDsc stops accepting the
// certificate, so no further registrations can be made under it. What it does
// not do is undo the ones already recorded, and those are the ones that matter,
// because each is drawing ANML every day and carrying weight in the democratic
// pillar. Left alone they keep doing so until they lapse, which is a year.
//
// Retiring one zeroes its identity leaf, so its holder stops proving
// membership (claims, votes, caretaker splits) once the identity roots from
// before the purge age out. What the holder already did under their anonymous
// nullifiers cannot be found and stays, bounded: a caretaker split lapses
// within caretaker_vote_seconds, and the proposal revoking a signer excludes
// its registrations from the assembly vote on it (excluded_dsc).

// StartDscPurge marks a Document Signer's registrations for retirement. Called
// when governance revokes the signer, so the cleanup begins on its own rather
// than waiting on a second vote a week later.
//
// Idempotent, and cheap: it records the intent and returns. The work happens in
// BeginBlocker, a bounded batch at a time, because a signer may have produced
// more registrations than one block can afford to retire.
func (k Keeper) StartDscPurge(ctx context.Context, dscKey []byte) error {
	if len(dscKey) == 0 {
		return nil
	}
	if err := k.PendingDscPurge.Set(ctx, dscKey); err != nil {
		return err
	}
	sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(
		sdk.NewEvent("dsc_purge_started", sdk.NewAttribute("dsc", hexOf(dscKey))),
	)
	return nil
}

// purgeRevokedDscs retires up to `budget` registrations belonging to revoked
// signers, returning how many it used.
//
// Runs before the expiry sweep and has the largest share of the block's
// budget, though not all of it (runSweeps reserves a share for each other
// sweep). Both are self-healing and both drain over following blocks, but a
// lapsed voter lingering an extra block costs a rounding error of emission,
// while a revoked signer's voter lingering costs governance weight to somebody
// who should not have it.
func (k Keeper) purgeRevokedDscs(ctx context.Context, budget int) (int, error) {
	if budget <= 0 {
		return 0, nil
	}

	// Collect first: removeRegistration writes to the indexes being walked.
	type victim struct {
		dsc       []byte
		nullifier []byte
	}
	var (
		victims []victim
		drained [][]byte // signers whose registrations are all gone
	)
	// A registration a sweep failed to retire is passed over until its retry
	// time (audit 6 B6-5), within a bound on the entries scanned; a signer
	// whose remaining registrations are all waiting stays pending.
	now := sdk.UnwrapSDKContext(ctx).BlockTime().Unix()
	scanned := 0
	err := k.PendingDscPurge.Walk(ctx, nil, func(dscKey []byte) (bool, error) {
		found := 0
		rng := collections.NewPrefixedPairRange[[]byte, []byte](dscKey)
		if err := k.RegByDsc.Walk(ctx, rng, func(key collections.Pair[[]byte, []byte]) (bool, error) {
			found++
			scanned++
			if waiting, err := k.sweepRetrying(ctx, key.K2(), now); err != nil {
				return true, err
			} else if !waiting {
				victims = append(victims, victim{dsc: dscKey, nullifier: key.K2()})
			}
			return len(victims) >= budget || scanned >= budget*types.SweepScanFactor, nil
		}); err != nil {
			return true, err
		}
		if found == 0 {
			// Nothing left under this signer: the purge for it is complete.
			drained = append(drained, dscKey)
		}
		return len(victims) >= budget || scanned >= budget*types.SweepScanFactor, nil
	})
	if err != nil {
		return 0, err
	}

	for _, dscKey := range drained {
		if err := k.PendingDscPurge.Remove(ctx, dscKey); err != nil {
			return 0, err
		}
		sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(
			sdk.NewEvent("dsc_purge_complete", sdk.NewAttribute("dsc", hexOf(dscKey))),
		)
	}
	if len(victims) == 0 {
		return 0, nil
	}

	for _, v := range victims {
		reg, err := k.Registrations.Get(ctx, v.nullifier)
		if err != nil {
			if errors.Is(err, collections.ErrNotFound) {
				// Index entry outlived its registration; drop the orphaned key.
				if err := k.RegByDsc.Remove(ctx, collections.Join(v.dsc, v.nullifier)); err != nil {
					return 0, err
				}
				continue
			}
			return 0, err
		}
		if err := k.retireOrRetryLater(ctx, reg, "purge_registration", now); err != nil {
			return 0, err
		}
	}

	// Only when the budget ran out (more may be waiting), as the expiry
	// sweep does: indexers read the event as "capped".
	if len(victims) >= budget {
		sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(
			sdk.NewEvent(
				"registration_sweep_capped",
				sdk.NewAttribute("retired", strconv.Itoa(len(victims))),
				sdk.NewAttribute("limit", strconv.Itoa(budget)),
				sdk.NewAttribute("reason", "dsc_revoked"),
			),
		)
	}
	return len(victims), nil
}

// OnDscRevoked implements pki's DscRevocationListener: when governance revokes a
// Document Signer, the registrations it produced are queued for retirement.
func (k Keeper) OnDscRevoked(ctx context.Context, commitment []byte) error {
	return k.StartDscPurge(ctx, commitment)
}

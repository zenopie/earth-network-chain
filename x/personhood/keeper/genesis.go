package keeper

import (
	"context"
	"errors"
	"fmt"

	"cosmossdk.io/collections"

	"github.com/earth-network/earth/x/personhood/types"
)

// InitGenesis initializes the module's state from a provided genesis state. The
// caretaker stream and its registration-rewards option are seeded by
// x/allocation, which owns them; the splits' voters are too, and only their
// leases are here.
//
// Only the registrations themselves are carried. Every index over them — by
// Document Signer, by issuing country, by registration time, the count, and
// the identity tree — is rebuilt here, so an export cannot ship a set of
// counters or nodes that disagrees with the records they count.
func (k Keeper) InitGenesis(ctx context.Context, genState types.GenesisState) error {
	if err := k.Params.Set(ctx, genState.Params); err != nil {
		return err
	}
	if err := k.LastBuyback.Set(ctx, genState.LastBuyback); err != nil {
		return err
	}

	if err := k.IdentitySize.Set(ctx, genState.IdentityTreeSize); err != nil {
		return err
	}
	t, err := k.identityTree(ctx)
	if err != nil {
		return err
	}
	for _, reg := range genState.Registrations {
		leaf, err := IdentityLeaf(reg.Idc, reg.DscKey, reg.ActivatedAt)
		if err != nil {
			return fmt.Errorf("registration %x: %w", reg.Nullifier, err)
		}
		if err := t.Update(reg.LeafIndex, leaf); err != nil {
			return err
		}
		if err := k.addRegistration(ctx, reg); err != nil {
			return err
		}
	}
	for i, r := range genState.IdentityRoots {
		if err := k.putIdentityRoot(ctx, r, i == len(genState.IdentityRoots)-1); err != nil {
			return err
		}
	}
	if err := k.recordIdentityRoot(ctx); err != nil {
		return err
	}

	for _, c := range genState.ClaimNullifiers {
		if err := k.ClaimNullifiers.Set(ctx, collections.Join(c.Day, c.Nullifier)); err != nil {
			return err
		}
	}
	for _, v := range genState.CaretakerVotes {
		if err := k.CaretakerVotes.Set(ctx, v.Nullifier, v.ExpiresAt); err != nil {
			return err
		}
		if err := k.CaretakerExpiry.Set(ctx, collections.Join(v.ExpiresAt, v.Nullifier)); err != nil {
			return err
		}
	}
	return k.CaretakerCount.Set(ctx, uint64(len(genState.CaretakerVotes)))
}

// ExportGenesis returns the module's exported genesis.
func (k Keeper) ExportGenesis(ctx context.Context) (*types.GenesisState, error) {
	var err error
	genesis := types.DefaultGenesis()
	genesis.Params, err = k.Params.Get(ctx)
	if err != nil {
		return nil, err
	}
	// Deliberately not carried: see last_buyback in genesis.proto.
	genesis.LastBuyback = 0

	if err := k.Registrations.Walk(ctx, nil, func(_ []byte, reg types.Registration) (bool, error) {
		genesis.Registrations = append(genesis.Registrations, reg)
		return false, nil
	}); err != nil {
		return nil, err
	}
	if genesis.IdentityTreeSize, err = k.IdentityTreeSize(ctx); err != nil {
		return nil, err
	}

	latest, err := k.LatestIdentityRoot.Get(ctx)
	if err != nil && !errors.Is(err, collections.ErrNotFound) {
		return nil, err
	}
	var latestRec *types.IdentityRoot
	if err := k.IdentityRootsByTime.Walk(ctx, nil, func(key collections.Pair[int64, []byte]) (bool, error) {
		rec, err := k.IdentityRoots.Get(ctx, key.K2())
		if err != nil {
			return true, err
		}
		if string(rec.Root) == string(latest) {
			latestRec = &rec
		} else {
			genesis.IdentityRoots = append(genesis.IdentityRoots, rec)
		}
		return false, nil
	}); err != nil {
		return nil, err
	}
	if latestRec != nil {
		genesis.IdentityRoots = append(genesis.IdentityRoots, *latestRec)
	}

	if err := k.ClaimNullifiers.Walk(ctx, nil, func(key collections.Pair[uint64, []byte]) (bool, error) {
		genesis.ClaimNullifiers = append(genesis.ClaimNullifiers, types.ClaimNullifier{Day: key.K1(), Nullifier: key.K2()})
		return false, nil
	}); err != nil {
		return nil, err
	}
	if err := k.CaretakerVotes.Walk(ctx, nil, func(nf []byte, expiresAt int64) (bool, error) {
		genesis.CaretakerVotes = append(genesis.CaretakerVotes, types.CaretakerVote{Nullifier: nf, ExpiresAt: expiresAt})
		return false, nil
	}); err != nil {
		return nil, err
	}
	return genesis, nil
}

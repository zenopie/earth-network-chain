package keeper

import (
	"context"

	"cosmossdk.io/collections"

	"github.com/earth-network/earth/x/personhood/types"
)

// InitGenesis initializes the module's state from a provided genesis state. The
// human allocation stream and its registration-rewards option are seeded by
// x/allocation, which owns them.
//
// Only the registrations themselves are carried. Every index over them — by
// address, by Document Signer, by issuing country, by registration time, and the
// total count — is rebuilt here, so an export cannot ship a set of counters that
// disagrees with the records they count.
func (k Keeper) InitGenesis(ctx context.Context, genState types.GenesisState) error {
	if err := k.Params.Set(ctx, genState.Params); err != nil {
		return err
	}

	if err := k.LastBuyback.Set(ctx, genState.LastBuyback); err != nil {
		return err
	}

	for _, reg := range genState.Registrations {
		if err := k.restoreRegistration(ctx, reg); err != nil {
			return err
		}
	}
	return k.RegCount.Set(ctx, uint64(len(genState.Registrations)))
}

// restoreRegistration writes one registration and every index that points at it.
func (k Keeper) restoreRegistration(ctx context.Context, reg types.Registration) error {
	addrBz, err := k.addressCodec.StringToBytes(reg.Address)
	if err != nil {
		return err
	}
	if err := k.Registrations.Set(ctx, reg.Nullifier, reg); err != nil {
		return err
	}
	if err := k.RegByAddr.Set(ctx, addrBz, reg.Nullifier); err != nil {
		return err
	}
	if err := k.RegByRegisteredAt.Set(ctx, collections.Join(reg.RegisteredAt, reg.Nullifier)); err != nil {
		return err
	}
	if err := bumpCount(ctx, k.RegCountByDsc, reg.DscKey); err != nil {
		return err
	}
	return bumpCount(ctx, k.RegCountByCountry, reg.Country)
}

// ExportGenesis returns the module's exported genesis.
func (k Keeper) ExportGenesis(ctx context.Context) (*types.GenesisState, error) {
	var err error

	genesis := types.DefaultGenesis()
	genesis.Params, err = k.Params.Get(ctx)
	if err != nil {
		return nil, err
	}

	// Deliberately not carried: see last_buyback in genesis.proto. The buyback
	// mints for elapsed wall-clock time, and a chain restarted from this file did
	// not run during the gap.
	genesis.LastBuyback = 0

	if err := k.Registrations.Walk(ctx, nil, func(_ []byte, reg types.Registration) (bool, error) {
		genesis.Registrations = append(genesis.Registrations, reg)
		return false, nil
	}); err != nil {
		return nil, err
	}

	return genesis, nil
}

package keeper

import (
	"context"

	"github.com/earth-network/earth/x/pki/types"
)

// InitGenesis initializes the module state from genesis.
func (k Keeper) InitGenesis(ctx context.Context, genState types.GenesisState) error {
	if err := k.Params.Set(ctx, genState.Params); err != nil {
		return err
	}
	for i := range genState.Cscas {
		if err := k.AddCscaDER(ctx, genState.Cscas[i].CertificateDer); err != nil {
			return err
		}
	}
	// Revocations are restored after the CSCAs, and are not derived from them:
	// nothing about a certificate records that governance stopped trusting a
	// signer it chains to.
	for _, id := range genState.RevokedDscs {
		if err := k.RevokedDscs.Set(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

// ExportGenesis exports the module state.
func (k Keeper) ExportGenesis(ctx context.Context) (*types.GenesisState, error) {
	params, err := k.Params.Get(ctx)
	if err != nil {
		return nil, err
	}
	gs := &types.GenesisState{Params: params}
	err = k.Cscas.Walk(ctx, nil, func(_ []byte, csca types.Csca) (bool, error) {
		gs.Cscas = append(gs.Cscas, csca)
		return false, nil
	})
	if err != nil {
		return nil, err
	}
	err = k.RevokedDscs.Walk(ctx, nil, func(id []byte) (bool, error) {
		gs.RevokedDscs = append(gs.RevokedDscs, id)
		return false, nil
	})
	if err != nil {
		return nil, err
	}
	return gs, nil
}

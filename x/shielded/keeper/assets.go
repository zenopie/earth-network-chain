package keeper

import (
	"context"
	"encoding/hex"
	"errors"

	"cosmossdk.io/collections"
	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/x/shielded/types"
)

// RegisterAsset admits denom to the pool, recording its in-circuit id both
// ways. Admission is permanent and idempotent: registering a denom twice
// returns the same id. Other modules (private staking's derth/<valoper>)
// register their own denoms through this.
func (k Keeper) RegisterAsset(ctx context.Context, denom string) ([]byte, error) {
	if err := sdk.ValidateDenom(denom); err != nil {
		return nil, err
	}
	if k.IsExcludedAsset(denom) {
		return nil, errorsmod.Wrapf(types.ErrAssetNotRegistered, "%s is never a shielded-pool asset", denom)
	}
	if id, err := k.Assets.Get(ctx, denom); err == nil {
		return id, nil
	} else if !errors.Is(err, collections.ErrNotFound) {
		return nil, err
	}
	a := types.NewAsset(denom)
	// AssetID is a Poseidon2 hash of the denom; a collision here is a hash
	// collision, but checking costs one read and keeps the map a bijection.
	if other, err := k.AssetsByID.Get(ctx, a.AssetId); err == nil {
		return nil, errorsmod.Wrapf(types.ErrAssetNotRegistered, "asset id of %q collides with %q", denom, other)
	} else if !errors.Is(err, collections.ErrNotFound) {
		return nil, err
	}
	if err := k.Assets.Set(ctx, denom, a.AssetId); err != nil {
		return nil, err
	}
	if err := k.AssetsByID.Set(ctx, a.AssetId, denom); err != nil {
		return nil, err
	}
	sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(sdk.NewEvent(types.EventTypeAsset,
		sdk.NewAttribute(types.AttributeKeyDenom, denom),
		sdk.NewAttribute(types.AttributeKeyAssetID, hex.EncodeToString(a.AssetId)),
	))
	return a.AssetId, nil
}

// AssetID returns denom's in-circuit id, or ErrAssetNotRegistered.
func (k Keeper) AssetID(ctx context.Context, denom string) ([]byte, error) {
	id, err := k.Assets.Get(ctx, denom)
	if errors.Is(err, collections.ErrNotFound) {
		return nil, errorsmod.Wrap(types.ErrAssetNotRegistered, denom)
	}
	return id, err
}

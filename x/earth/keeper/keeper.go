package keeper

import (
	"fmt"

	"cosmossdk.io/collections"
	"cosmossdk.io/core/address"
	corestore "cosmossdk.io/core/store"
	"cosmossdk.io/math"
	"github.com/cosmos/cosmos-sdk/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/x/earth/types"
)

// Keeper owns ERTH tokenomics — everything that creates or destroys the token.
//
// The investor pillar's emission is minted here and paid into the fee collector,
// where x/distribution picks it up and splits it under the SDK's standard
// staking rules; gas fees are burned here.
//
// The division of labour with the pillar modules: this module owns how much ERTH
// exists; they own how their share is directed.
type Keeper struct {
	storeService corestore.KVStoreService
	cdc          codec.Codec
	addressCodec address.Codec
	// Address capable of executing a MsgUpdateParams message.
	// Typically, this should be the x/gov module account.
	authority []byte

	bankKeeper types.BankKeeper

	Schema collections.Schema
	Params collections.Item[types.Params]

	// LastMintTime is the previous emission's block time (unix nanos), used to
	// prorate the fixed per-second rate across variable block times.
	LastMintTime collections.Item[int64]

	// Burned is cumulative destroyed supply, keyed by (source, denom). See
	// burns.go for why the chain counts this rather than deriving it.
	Burned collections.Map[collections.Pair[string, string], math.Int]
}

func NewKeeper(
	storeService corestore.KVStoreService,
	cdc codec.Codec,
	addressCodec address.Codec,
	authority []byte,

	bankKeeper types.BankKeeper,
) Keeper {
	if _, err := addressCodec.BytesToString(authority); err != nil {
		panic(fmt.Sprintf("invalid authority address %s: %s", authority, err))
	}

	sb := collections.NewSchemaBuilder(storeService)

	k := Keeper{
		storeService: storeService,
		cdc:          cdc,
		addressCodec: addressCodec,
		authority:    authority,
		bankKeeper:   bankKeeper,

		Params:       collections.NewItem(sb, types.ParamsKey, "params", codec.CollValue[types.Params](cdc)),
		LastMintTime: collections.NewItem(sb, types.LastMintTimeKey, "last_mint_time", collections.Int64Value),
		Burned: collections.NewMap(sb, types.BurnedKey, "burned",
			collections.PairKeyCodec(collections.StringKey, collections.StringKey), sdk.IntValue),
	}

	schema, err := sb.Build()
	if err != nil {
		panic(err)
	}
	k.Schema = schema

	return k
}

// GetAuthority returns the module's authority.
func (k Keeper) GetAuthority() []byte {
	return k.authority
}

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
// The emission is minted here and compounded straight into bonded stake, gas
// fees are burned here, and the two pieces of bookkeeping that fall out of
// compounding (withheld validator commission, and the stake growth index that
// keeps stored stake figures honest) live here too.
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

	bankKeeper    types.BankKeeper
	stakingKeeper types.StakingKeeper

	Schema collections.Schema
	Params collections.Item[types.Params]

	// LastMintTime is the previous emission's block time (unix nanos), used to
	// prorate the fixed per-second rate across variable block times.
	LastMintTime collections.Item[int64]

	// StakeCompoundIndex is the cumulative growth factor of bonded stake from
	// auto-compounding, scaled by 1e18. Anything storing a stake figure
	// normalizes by it — see NormalizeStakeWeight.
	StakeCompoundIndex collections.Item[math.Int]

	// AccruedCommission is commission withheld from the emission per validator,
	// held on this module's account until compounded into their self-delegation.
	AccruedCommission collections.Map[[]byte, math.Int]
}

func NewKeeper(
	storeService corestore.KVStoreService,
	cdc codec.Codec,
	addressCodec address.Codec,
	authority []byte,

	bankKeeper types.BankKeeper,
	stakingKeeper types.StakingKeeper,
) Keeper {
	if _, err := addressCodec.BytesToString(authority); err != nil {
		panic(fmt.Sprintf("invalid authority address %s: %s", authority, err))
	}

	sb := collections.NewSchemaBuilder(storeService)

	k := Keeper{
		storeService:  storeService,
		cdc:           cdc,
		addressCodec:  addressCodec,
		authority:     authority,
		bankKeeper:    bankKeeper,
		stakingKeeper: stakingKeeper,

		Params:       collections.NewItem(sb, types.ParamsKey, "params", codec.CollValue[types.Params](cdc)),
		LastMintTime: collections.NewItem(sb, types.LastMintTimeKey, "last_mint_time", collections.Int64Value),
		StakeCompoundIndex: collections.NewItem(
			sb, types.StakeCompoundIndexKey, "stake_compound_index", sdk.IntValue),
		AccruedCommission: collections.NewMap(
			sb, types.AccruedCommissionKey, "accrued_commission", collections.BytesKey, sdk.IntValue),
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

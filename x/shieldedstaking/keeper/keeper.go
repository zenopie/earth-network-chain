package keeper

import (
	"context"
	"fmt"

	"cosmossdk.io/collections"
	"cosmossdk.io/core/address"
	corestore "cosmossdk.io/core/store"
	"cosmossdk.io/log"
	"github.com/cosmos/cosmos-sdk/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	distrtypes "github.com/cosmos/cosmos-sdk/x/distribution/types"
	govkeeper "github.com/cosmos/cosmos-sdk/x/gov/keeper"

	allocationkeeper "github.com/earth-network/earth/x/allocation/keeper"
	shieldedkeeper "github.com/earth-network/earth/x/shielded/keeper"
	shieldedtypes "github.com/earth-network/earth/x/shielded/types"
	"github.com/earth-network/earth/x/shieldedstaking/types"
)

// Keeper is private staking. Its module account is the only delegator x/staking
// has besides validators' own self-bonds: it holds ERTH queued for delegation,
// matured unbondings not yet claimed, and the derth locked in Groundworks
// positions. Everything a person owns here is a note in x/shielded's pool.
type Keeper struct {
	storeService corestore.KVStoreService
	cdc          codec.Codec
	addressCodec address.Codec
	authority    []byte

	auth       types.AuthKeeper
	bank       types.BankKeeper
	staking    types.StakingKeeper
	distr      types.DistrKeeper
	slashing   types.SlashingKeeper
	shielded   shieldedkeeper.Keeper
	allocation allocationkeeper.Keeper

	modAddr  sdk.AccAddress
	poolAddr sdk.AccAddress
	distAddr sdk.AccAddress

	// gov is bound after app construction (x/gov takes this module's tally
	// function, so this module cannot take x/gov's keeper from the container).
	gov *govRef

	Schema collections.Schema
	Params collections.Item[types.Params]
	Epoch  collections.Item[types.Epoch]

	// Validators is the book per validator operator (bech32).
	Validators collections.Map[string, types.ValidatorState]
	// UnbondRecords backs unbond/<validator>/<epoch>.
	UnbondRecords collections.Map[collections.Pair[string, uint64], types.UnbondRecord]
	// PendingRecords indexes the records still waiting for their SDK
	// undelegation.
	PendingRecords collections.KeySet[collections.Pair[string, uint64]]
	// SlashedValidators are this block's slashed validators (valoper), for
	// EndBlock's re-weigh of their positions (reweighSlashed).
	SlashedValidators collections.KeySet[string]
	// MaturityQueue orders UNBONDING records by (completion ns, validator,
	// epoch).
	MaturityQueue collections.KeySet[collections.Triple[int64, string, uint64]]

	Positions      collections.Map[uint64, types.Position]
	PositionSeq    collections.Sequence
	PositionsByVal collections.KeySet[collections.Pair[string, uint64]]

	Snapshots      collections.Map[uint64, types.ProposalSnapshot]
	SnapshotExpiry collections.KeySet[collections.Pair[int64, uint64]]
	// Votes are keyed (proposal, 0x00||vote_nf) for note votes and
	// (proposal, 0x01||position id) for position votes.
	Votes collections.Map[collections.Pair[uint64, []byte], types.StakeVote]
	// Tallies aggregate Votes per (proposal, validator).
	Tallies collections.Map[collections.Pair[uint64, string], types.VoteTally]
}

type govRef struct{ k *govkeeper.Keeper }

// NewKeeper builds the keeper.
func NewKeeper(
	storeService corestore.KVStoreService,
	cdc codec.Codec,
	addressCodec address.Codec,
	authority []byte,
	auth types.AuthKeeper,
	bank types.BankKeeper,
	staking types.StakingKeeper,
	distr types.DistrKeeper,
	slashing types.SlashingKeeper,
	shielded shieldedkeeper.Keeper,
	allocation allocationkeeper.Keeper,
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
		auth:         auth,
		bank:         bank,
		staking:      staking,
		distr:        distr,
		slashing:     slashing,
		shielded:     shielded,
		allocation:   allocation,
		modAddr:      authtypes.NewModuleAddress(types.ModuleName),
		poolAddr:     authtypes.NewModuleAddress(shieldedtypes.ModuleName),
		distAddr:     authtypes.NewModuleAddress(distrtypes.ModuleName),
		gov:          &govRef{},

		Params: collections.NewItem(sb, types.ParamsKey, "params", codec.CollValue[types.Params](cdc)),
		Epoch:  collections.NewItem(sb, types.EpochKey, "epoch", codec.CollValue[types.Epoch](cdc)),
		Validators: collections.NewMap(sb, types.ValidatorsKey, "validators", collections.StringKey,
			codec.CollValue[types.ValidatorState](cdc)),
		UnbondRecords: collections.NewMap(sb, types.UnbondRecordsKey, "unbond_records",
			collections.PairKeyCodec(collections.StringKey, collections.Uint64Key), codec.CollValue[types.UnbondRecord](cdc)),
		SlashedValidators: collections.NewKeySet(sb, types.SlashedValidatorsKey, "slashed_validators", collections.StringKey),
		PendingRecords: collections.NewKeySet(sb, types.PendingRecordsKey, "pending_records",
			collections.PairKeyCodec(collections.StringKey, collections.Uint64Key)),
		MaturityQueue: collections.NewKeySet(sb, types.MaturityQueueKey, "maturity_queue",
			collections.TripleKeyCodec(collections.Int64Key, collections.StringKey, collections.Uint64Key)),
		Positions: collections.NewMap(sb, types.PositionsKey, "positions", collections.Uint64Key,
			codec.CollValue[types.Position](cdc)),
		PositionSeq: collections.NewSequence(sb, types.PositionSeqKey, "position_seq"),
		PositionsByVal: collections.NewKeySet(sb, types.PositionsByValKey, "positions_by_val",
			collections.PairKeyCodec(collections.StringKey, collections.Uint64Key)),
		Snapshots: collections.NewMap(sb, types.SnapshotsKey, "snapshots", collections.Uint64Key,
			codec.CollValue[types.ProposalSnapshot](cdc)),
		SnapshotExpiry: collections.NewKeySet(sb, types.SnapshotExpiryKey, "snapshot_expiry",
			collections.PairKeyCodec(collections.Int64Key, collections.Uint64Key)),
		Votes: collections.NewMap(sb, types.VotesKey, "votes",
			collections.PairKeyCodec(collections.Uint64Key, collections.BytesKey), codec.CollValue[types.StakeVote](cdc)),
		Tallies: collections.NewMap(sb, types.TalliesKey, "tallies",
			collections.PairKeyCodec(collections.Uint64Key, collections.StringKey), codec.CollValue[types.VoteTally](cdc)),
	}
	schema, err := sb.Build()
	if err != nil {
		panic(err)
	}
	k.Schema = schema
	return k
}

// SetGovKeeper binds x/gov after app construction. Until it is bound no
// proposal is snapshotted, so no stake vote can be cast.
func (k Keeper) SetGovKeeper(g *govkeeper.Keeper) { k.gov.k = g }

// GetAuthority returns the module's authority.
func (k Keeper) GetAuthority() []byte { return k.authority }

// ModuleAddress is the delegator.
func (k Keeper) ModuleAddress() sdk.AccAddress { return k.modAddr }

func (k Keeper) logger(ctx context.Context) log.Logger {
	return sdk.UnwrapSDKContext(ctx).Logger().With("module", "x/"+types.ModuleName)
}

func (k Keeper) valAddr(valoper string) (sdk.ValAddress, error) {
	bz, err := k.staking.ValidatorAddressCodec().StringToBytes(valoper)
	if err != nil {
		return nil, types.ErrValidator.Wrapf("%s: %v", valoper, err)
	}
	return bz, nil
}

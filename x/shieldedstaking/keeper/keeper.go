package keeper

import (
	"context"
	"fmt"

	"cosmossdk.io/collections"
	"cosmossdk.io/core/address"
	corestore "cosmossdk.io/core/store"
	"cosmossdk.io/log"
	"cosmossdk.io/math"
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
// has besides validators' own self-bonds: it holds ERTH queued for delegation
// and matured unbondings not yet paid out. What a person owns here (derth) is
// an owner-locked note in this module's stake note tree; positions hold derth
// on the books; an undelegation is a queued payout to a pool pc; nothing here
// is a coin but ERTH.
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
	// UnbondRecords is each (validator, epoch)'s private undelegation.
	UnbondRecords collections.Map[collections.Pair[string, uint64], types.UnbondRecord]
	// PendingRecords indexes the records still waiting for their SDK
	// undelegation.
	PendingRecords collections.KeySet[collections.Pair[string, uint64]]
	// OrphanRecords indexes the orphan records (requested zero), per
	// validator, until sweepOrphanRecords forgets them.
	OrphanRecords collections.KeySet[collections.Pair[string, uint64]]
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

	// The stake note tree (stake_tree.go).
	StakeTreeNodes collections.Map[collections.Pair[uint32, uint64], []byte]
	StakeTreeSize  collections.Item[uint64]
	// StakeNullifiers maps each spent stake nullifier to its leaf index in
	// the stake nullifier tree (nf_tree.go).
	StakeNullifiers  collections.Map[[]byte, uint64]
	StakeRoots       collections.Map[[]byte, types.StakeRoot]
	StakeRootsByTime collections.KeySet[collections.Pair[int64, []byte]]
	StakeLatestRoot  collections.Item[[]byte]

	// The stake nullifier indexed tree (nf_tree.go).
	StakeNfValues     collections.Map[uint64, []byte]
	StakeNfNodes      collections.Map[collections.Pair[uint32, uint64], []byte]
	StakeNfSize       collections.Item[uint64]
	StakeNfLatestRoot collections.Item[[]byte]
	StakeNfLatestSize collections.Item[uint64]
	// RootsStale: the last end-of-block recording of the stake roots failed,
	// so the latest recorded roots may predate notes spent since (see
	// takeSnapshot). Not exported: InitGenesis records the roots afresh.
	RootsStale collections.Item[bool]

	// RewardEscrows maps each validator's reward escrow account to the
	// validator's address (escrow.go). Rebuilt from x/staking at genesis.
	RewardEscrows collections.Map[[]byte, []byte]

	// Lazy gov snapshots (votes.go).
	SnapshotSeq       collections.Sequence
	SupplyCheckpoints collections.Map[collections.Pair[string, uint64], math.Int]
	CheckpointsBySeq  collections.KeySet[collections.Pair[uint64, string]]
	SnapshotsBySeq    collections.KeySet[collections.Pair[uint64, uint64]]

	// EpochSweep is the epoch-end book sweep's progress (epoch.go).
	EpochSweep collections.Item[types.EpochSweep]
	// GwTotals is, per (validator, option), the sum of derth x percent over
	// the validator's live positions (positions.go); GwEpoch the Groundworks
	// allocation epoch each validator's totals belong to.
	GwTotals collections.Map[collections.Pair[string, uint64], math.Int]
	GwEpoch  collections.Map[string, uint64]
	// RetiringEscrows: (release time ns, validator) for operators that
	// removed their whole self-bond; PendingReleases: removed validators
	// whose escrow release failed and is retried (escrow.go).
	RetiringEscrows collections.KeySet[collections.Pair[int64, []byte]]
	PendingReleases collections.KeySet[[]byte]
	// PendingReleaseCursor is where the next retry round of PendingReleases
	// starts (after this entry; escrow.go).
	PendingReleaseCursor collections.Item[[]byte]

	// Undelegation payouts (payouts.go): every queued payout by id; those
	// not yet tried by (validator, epoch, id); those that failed by
	// (retry_at, id); the MATURED records that still have payouts to make;
	// the id sequence.
	UnbondPayouts   collections.Map[uint64, types.UnbondPayout]
	PayoutsByRecord collections.KeySet[collections.Triple[string, uint64, uint64]]
	PayoutRetries   collections.KeySet[collections.Pair[int64, uint64]]
	MaturedRecords  collections.KeySet[collections.Pair[string, uint64]]
	UnbondPayoutSeq collections.Sequence
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
		OrphanRecords: collections.NewKeySet(sb, types.OrphanRecordsKey, "orphan_records",
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
		StakeTreeNodes: collections.NewMap(sb, types.StakeTreeNodesKey, "stake_tree_nodes",
			collections.PairKeyCodec(collections.Uint32Key, collections.Uint64Key), collections.BytesValue),
		StakeTreeSize:   collections.NewItem(sb, types.StakeTreeSizeKey, "stake_tree_size", collections.Uint64Value),
		StakeNullifiers: collections.NewMap(sb, types.StakeNullifiersKey, "stake_nullifiers", collections.BytesKey, collections.Uint64Value),
		StakeNfValues:   collections.NewMap(sb, types.StakeNfValuesKey, "stake_nf_values", collections.Uint64Key, collections.BytesValue),
		StakeNfNodes: collections.NewMap(sb, types.StakeNfNodesKey, "stake_nf_nodes",
			collections.PairKeyCodec(collections.Uint32Key, collections.Uint64Key), collections.BytesValue),
		StakeNfSize:       collections.NewItem(sb, types.StakeNfSizeKey, "stake_nf_size", collections.Uint64Value),
		StakeNfLatestRoot: collections.NewItem(sb, types.StakeNfLatestRootKey, "stake_nf_latest_root", collections.BytesValue),
		StakeNfLatestSize: collections.NewItem(sb, types.StakeNfLatestSizeKey, "stake_nf_latest_size", collections.Uint64Value),
		RootsStale:        collections.NewItem(sb, types.RootsStaleKey, "roots_stale", collections.BoolValue),
		StakeRoots: collections.NewMap(sb, types.StakeRootsKey, "stake_roots", collections.BytesKey,
			codec.CollValue[types.StakeRoot](cdc)),
		StakeRootsByTime: collections.NewKeySet(sb, types.StakeRootsByTimeKey, "stake_roots_by_time",
			collections.PairKeyCodec(collections.Int64Key, collections.BytesKey)),
		StakeLatestRoot: collections.NewItem(sb, types.StakeLatestRootKey, "stake_latest_root", collections.BytesValue),
		RewardEscrows:   collections.NewMap(sb, types.RewardEscrowsKey, "reward_escrows", collections.BytesKey, collections.BytesValue),
		SnapshotSeq:     collections.NewSequence(sb, types.SnapshotSeqKey, "snapshot_seq"),
		SupplyCheckpoints: collections.NewMap(sb, types.SupplyCheckpointsKey, "supply_checkpoints",
			collections.PairKeyCodec(collections.StringKey, collections.Uint64Key), sdk.IntValue),
		CheckpointsBySeq: collections.NewKeySet(sb, types.CheckpointsBySeqKey, "checkpoints_by_seq",
			collections.PairKeyCodec(collections.Uint64Key, collections.StringKey)),
		SnapshotsBySeq: collections.NewKeySet(sb, types.SnapshotsBySeqKey, "snapshots_by_seq",
			collections.PairKeyCodec(collections.Uint64Key, collections.Uint64Key)),
		EpochSweep: collections.NewItem(sb, types.EpochSweepKey, "epoch_sweep", codec.CollValue[types.EpochSweep](cdc)),
		GwTotals: collections.NewMap(sb, types.GwTotalsKey, "gw_totals",
			collections.PairKeyCodec(collections.StringKey, collections.Uint64Key), sdk.IntValue),
		GwEpoch: collections.NewMap(sb, types.GwEpochKey, "gw_epoch", collections.StringKey, collections.Uint64Value),
		RetiringEscrows: collections.NewKeySet(sb, types.RetiringEscrowsKey, "retiring_escrows",
			collections.PairKeyCodec(collections.Int64Key, collections.BytesKey)),
		PendingReleases: collections.NewKeySet(sb, types.PendingReleasesKey, "pending_releases", collections.BytesKey),
		PendingReleaseCursor: collections.NewItem(sb, types.PendingReleaseCursorKey, "pending_release_cursor",
			collections.BytesValue),
		UnbondPayouts: collections.NewMap(sb, types.UnbondPayoutsKey, "unbond_payouts", collections.Uint64Key,
			codec.CollValue[types.UnbondPayout](cdc)),
		PayoutsByRecord: collections.NewKeySet(sb, types.PayoutsByRecordKey, "payouts_by_record",
			collections.TripleKeyCodec(collections.StringKey, collections.Uint64Key, collections.Uint64Key)),
		PayoutRetries: collections.NewKeySet(sb, types.PayoutRetriesKey, "payout_retries",
			collections.PairKeyCodec(collections.Int64Key, collections.Uint64Key)),
		MaturedRecords: collections.NewKeySet(sb, types.MaturedRecordsKey, "matured_records",
			collections.PairKeyCodec(collections.StringKey, collections.Uint64Key)),
		UnbondPayoutSeq: collections.NewSequence(sb, types.UnbondPayoutSeqKey, "unbond_payout_seq"),
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

// valAddr decodes a validator operator string, refusing any but the
// canonical encoding of its bytes: this module keys its books by the string,
// x/staking by the bytes, so an alias (an uppercase bech32 string decodes to
// the same bytes) would be a second book over the same delegation.
func (k Keeper) valAddr(valoper string) (sdk.ValAddress, error) {
	bz, err := k.staking.ValidatorAddressCodec().StringToBytes(valoper)
	if err != nil {
		return nil, types.ErrValidator.Wrapf("%s: %v", valoper, err)
	}
	canon, err := k.staking.ValidatorAddressCodec().BytesToString(bz)
	if err != nil {
		return nil, types.ErrValidator.Wrapf("%s: %v", valoper, err)
	}
	if canon != valoper {
		return nil, types.ErrValidator.Wrapf("%q is not canonical (%s)", valoper, canon)
	}
	return bz, nil
}

package keeper

import (
	"context"
	"errors"

	"cosmossdk.io/collections"
	"cosmossdk.io/core/address"
	"cosmossdk.io/core/store"
	"github.com/cosmos/cosmos-sdk/codec"
	govkeeper "github.com/cosmos/cosmos-sdk/x/gov/keeper"

	"github.com/earth-network/earth/x/assembly/types"
)

// Keeper is the assembly: the chamber where one live registration is one vote.
//
// It holds no funds, mints nothing, and has no authority address. Everything it
// stores is a count of people.
type Keeper struct {
	cdc          codec.Codec
	addressCodec address.Codec

	personhood types.PersonhoodKeeper
	gov        *govkeeper.Keeper
	allocation types.AllocationKeeper

	// chamberAddr is the module address x/allocation recognises as this chamber
	// when it is asked to remove an option.
	chamberAddr []byte

	Schema collections.Schema

	// ProposalVotes and ProposalTally are the human half of an x/gov proposal.
	// The tally is maintained on every vote so the EndBlocker can resolve a
	// proposal without walking what may be a great many votes in the block that
	// several proposals happen to close in.
	ProposalVotes collections.Map[collections.Pair[uint64, []byte], int32]
	ProposalTally collections.Map[uint64, types.Tally]

	RemovalBallots collections.Map[uint64, types.RemovalBallot]
	RemovalVotes   collections.Map[collections.Pair[uint64, []byte], int32]
	RemovalQueue   collections.KeySet[collections.Pair[int64, uint64]]
}

func NewKeeper(
	storeService store.KVStoreService,
	cdc codec.Codec,
	addressCodec address.Codec,
	chamberAddr []byte,
	personhood types.PersonhoodKeeper,
	gov *govkeeper.Keeper,
	allocation types.AllocationKeeper,
) Keeper {
	sb := collections.NewSchemaBuilder(storeService)

	k := Keeper{
		cdc:          cdc,
		addressCodec: addressCodec,
		personhood:   personhood,
		gov:          gov,
		allocation:   allocation,
		chamberAddr:  chamberAddr,

		ProposalVotes: collections.NewMap(sb, types.ProposalVotesKey, "proposal_votes",
			collections.PairKeyCodec(collections.Uint64Key, collections.BytesKey), collections.Int32Value),
		ProposalTally: collections.NewMap(sb, types.ProposalTallyKey, "proposal_tally",
			collections.Uint64Key, codec.CollValue[types.Tally](cdc)),

		RemovalBallots: collections.NewMap(sb, types.RemovalBallotsKey, "removal_ballots",
			collections.Uint64Key, codec.CollValue[types.RemovalBallot](cdc)),
		RemovalVotes: collections.NewMap(sb, types.RemovalVotesKey, "removal_votes",
			collections.PairKeyCodec(collections.Uint64Key, collections.BytesKey), collections.Int32Value),
		RemovalQueue: collections.NewKeySet(sb, types.RemovalQueueKey, "removal_queue",
			collections.PairKeyCodec(collections.Int64Key, collections.Uint64Key)),
	}

	schema, err := sb.Build()
	if err != nil {
		panic(err)
	}
	k.Schema = schema

	return k
}

// ChamberAddress is the address x/allocation checks a removal against.
func (k Keeper) ChamberAddress() []byte { return k.chamberAddr }

// voterNullifier resolves a signing address to the registration behind it, and
// refuses the account if there is not a live one.
//
// Every message in this module goes through here, and it is the whole of the
// franchise: a passport nobody has registered does not vote, a registration that
// has lapsed does not vote, and a wallet with neither does not vote. What comes
// back is the nullifier rather than the address because that is what the vote is
// recorded against — a registration may be moved to a new wallet, and keyed by
// address one person could vote, move, and vote again.
func (k Keeper) voterNullifier(ctx context.Context, addr string) ([]byte, error) {
	bz, err := k.addressCodec.StringToBytes(addr)
	if err != nil {
		return nil, err
	}
	nullifier, live, err := k.personhood.LiveNullifier(ctx, bz)
	if err != nil {
		return nil, err
	}
	if !live {
		return nil, types.ErrNotRegistered
	}
	return nullifier, nil
}

// castVote records one vote into a (ballot, nullifier) -> option map and moves
// the tally to match, handling the case where this registration has already
// voted and is changing its mind.
//
// Returns the tally as it now stands. The tally is derived here and nowhere
// else, so the count and the votes it counts cannot come apart.
func castVote(
	ctx context.Context,
	votes collections.Map[collections.Pair[uint64, []byte], int32],
	key collections.Pair[uint64, []byte],
	tally types.Tally,
	option types.VoteOption,
) (types.Tally, error) {
	if prev, err := votes.Get(ctx, key); err == nil {
		// Changing a vote, so take the old side back down first. A vote recast
		// the same way is a no-op rather than a second vote.
		switch types.VoteOption(prev) {
		case types.VOTE_OPTION_YES:
			tally.Yes--
		case types.VOTE_OPTION_NO:
			tally.No--
		}
	} else if !errors.Is(err, collections.ErrNotFound) {
		return tally, err
	}

	switch option {
	case types.VOTE_OPTION_YES:
		tally.Yes++
	case types.VOTE_OPTION_NO:
		tally.No++
	}
	if err := votes.Set(ctx, key, int32(option)); err != nil {
		return tally, err
	}
	return tally, nil
}

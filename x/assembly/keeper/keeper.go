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

	// Every round of voting is a ballot with its own id. See types.BallotSeqKey.
	BallotSeq   collections.Sequence
	BallotVotes collections.Map[collections.Pair[uint64, []byte], int32]
	BallotTally collections.Map[uint64, types.Tally]

	ProposalBallot  collections.Map[uint64, uint64]
	RemovalBallots  collections.Map[uint64, types.RemovalBallot]
	RemovalBallotID collections.Map[uint64, uint64]
	RemovalQueue    collections.KeySet[collections.Pair[int64, uint64]]

	// VotedBallots is every vote, by nullifier. It is what makes taking back a
	// retired registration's votes cost what that person voted on, rather than a
	// walk of every ballot.
	VotedBallots  collections.KeySet[collections.Pair[[]byte, uint64]]
	ClosedBallots collections.KeySet[uint64]

	// The v0.9.0 layout, for the v0.9.1 upgrade to move out of. See
	// MigrateToBallots.
	LegacyProposalVotes collections.Map[collections.Pair[uint64, []byte], int32]
	LegacyProposalTally collections.Map[uint64, types.Tally]
	LegacyRemovalVotes  collections.Map[collections.Pair[uint64, []byte], int32]
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

		BallotSeq: collections.NewSequence(sb, types.BallotSeqKey, "ballot_seq"),
		BallotVotes: collections.NewMap(sb, types.BallotVotesKey, "ballot_votes",
			collections.PairKeyCodec(collections.Uint64Key, collections.BytesKey), collections.Int32Value),
		BallotTally: collections.NewMap(sb, types.BallotTallyKey, "ballot_tally",
			collections.Uint64Key, codec.CollValue[types.Tally](cdc)),

		ProposalBallot: collections.NewMap(sb, types.ProposalBallotKey, "proposal_ballot",
			collections.Uint64Key, collections.Uint64Value),
		RemovalBallots: collections.NewMap(sb, types.RemovalBallotsKey, "removal_ballots",
			collections.Uint64Key, codec.CollValue[types.RemovalBallot](cdc)),
		RemovalBallotID: collections.NewMap(sb, types.RemovalBallotIDKey, "removal_ballot_id",
			collections.Uint64Key, collections.Uint64Value),
		RemovalQueue: collections.NewKeySet(sb, types.RemovalQueueKey, "removal_queue",
			collections.PairKeyCodec(collections.Int64Key, collections.Uint64Key)),

		VotedBallots: collections.NewKeySet(sb, types.VotedBallotsKey, "voted_ballots",
			collections.PairKeyCodec(collections.BytesKey, collections.Uint64Key)),
		ClosedBallots: collections.NewKeySet(sb, types.ClosedBallotsKey, "closed_ballots",
			collections.Uint64Key),

		LegacyProposalVotes: collections.NewMap(sb, types.LegacyProposalVotesKey, "proposal_votes",
			collections.PairKeyCodec(collections.Uint64Key, collections.BytesKey), collections.Int32Value),
		LegacyProposalTally: collections.NewMap(sb, types.LegacyProposalTallyKey, "proposal_tally",
			collections.Uint64Key, codec.CollValue[types.Tally](cdc)),
		LegacyRemovalVotes: collections.NewMap(sb, types.LegacyRemovalVotesKey, "removal_votes",
			collections.PairKeyCodec(collections.Uint64Key, collections.BytesKey), collections.Int32Value),
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

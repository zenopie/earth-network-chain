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

// Keeper is the assembly: the chamber where one live registration is one vote,
// cast anonymously with a membership proof.
//
// It holds no funds, mints nothing, and has no authority address. Everything it
// stores is a count of people.
type Keeper struct {
	cdc          codec.Codec
	addressCodec address.Codec

	personhood types.PersonhoodKeeper
	gov        *govkeeper.Keeper
	allocation types.AllocationKeeper
	pki        types.PkiKeeper

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

	ClosedBallots collections.KeySet[uint64]
	// ProposalRound is a proposal's round past its first. See types.ProposalRound.
	ProposalRound collections.Map[uint64, types.ProposalRound]
	// Subjects is each proposal's subjects, fixed as it enters voting. See
	// subjects.go.
	Subjects collections.Map[uint64, types.ProposalSubjects]
	// RemovalCooldown is, per option, the earliest unix time a new removal
	// ballot on it may open. See types.RemovalCooldown.
	RemovalCooldown collections.Map[uint64, int64]
	// OrphanSweepCursor: where closeOrphanedBallots resumes.
	OrphanSweepCursor collections.Item[uint64]
	// SubjectSweepCursor: where the Subjects part of that sweep resumes.
	SubjectSweepCursor collections.Item[uint64]
}

func NewKeeper(
	storeService store.KVStoreService,
	cdc codec.Codec,
	addressCodec address.Codec,
	chamberAddr []byte,
	personhood types.PersonhoodKeeper,
	gov *govkeeper.Keeper,
	allocation types.AllocationKeeper,
	pki types.PkiKeeper,
) Keeper {
	sb := collections.NewSchemaBuilder(storeService)

	k := Keeper{
		cdc:          cdc,
		addressCodec: addressCodec,
		personhood:   personhood,
		gov:          gov,
		allocation:   allocation,
		pki:          pki,
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

		ClosedBallots: collections.NewKeySet(sb, types.ClosedBallotsKey, "closed_ballots",
			collections.Uint64Key),
		ProposalRound: collections.NewMap(sb, types.ProposalRoundKey, "proposal_round",
			collections.Uint64Key, codec.CollValue[types.ProposalRound](cdc)),
		Subjects: collections.NewMap(sb, types.SubjectsKey, "subjects",
			collections.Uint64Key, codec.CollValue[types.ProposalSubjects](cdc)),
		RemovalCooldown: collections.NewMap(sb, types.RemovalCooldownKey, "removal_cooldown",
			collections.Uint64Key, collections.Int64Value),
		OrphanSweepCursor: collections.NewItem(sb, types.OrphanSweepCursorKey, "orphan_sweep_cursor",
			collections.Uint64Value),
		SubjectSweepCursor: collections.NewItem(sb, types.SubjectSweepCursorKey, "subject_sweep_cursor",
			collections.Uint64Value),
	}

	schema, err := sb.Build()
	if err != nil {
		panic(err)
	}
	k.Schema = schema

	return k
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

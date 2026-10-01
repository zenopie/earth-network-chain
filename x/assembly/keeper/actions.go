package keeper

import (
	"context"
	"errors"

	"cosmossdk.io/collections"
	errorsmod "cosmossdk.io/errors"
	storetypes "cosmossdk.io/store/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	v1 "github.com/cosmos/cosmos-sdk/x/gov/types/v1"

	"github.com/earth-network/earth/x/assembly/types"
	personhoodtypes "github.com/earth-network/earth/x/personhood/types"
	shieldedkeeper "github.com/earth-network/earth/x/shielded/keeper"
	shieldedtypes "github.com/earth-network/earth/x/shielded/types"
	"github.com/earth-network/earth/zk/privacy"
)

// The chamber's msgs are x/shielded private actions: the private ante checks
// and verifies each (its fee transfer and its membership proof) before it
// spends the fee, and the handlers apply what was checked. See
// x/shielded/types.PrivateActionHandler.

// RegisterPrivateActions attaches the chamber's msgs to the pool. Called once,
// from module wiring.
func (k Keeper) RegisterPrivateActions(sk types.ShieldedKeeper) {
	sk.RegisterPrivateAction(sdk.MsgTypeURL(&types.MsgVoteProposal{}), voteProposalAction{k})
	sk.RegisterPrivateAction(sdk.MsgTypeURL(&types.MsgProposeRemoval{}), proposeRemovalAction{k})
	sk.RegisterPrivateAction(sdk.MsgTypeURL(&types.MsgVoteRemoval{}), voteRemovalAction{k})
}

// authorized returns the statement the ante verified for msg, refusing a msg
// that did not come through the private ante, and a context on an infinite gas
// meter (the action's work was priced up front).
func authorized(ctx context.Context, msg shieldedtypes.PrivateMsg) (sdk.Context, personhoodtypes.MembershipStatement, error) {
	a, err := shieldedkeeper.AuthorizedAction(ctx, msg.PrivateTransfer())
	if err != nil {
		return sdk.Context{}, personhoodtypes.MembershipStatement{}, err
	}
	st, ok := a.(personhoodtypes.MembershipStatement)
	if !ok {
		return sdk.Context{}, st, shieldedtypes.ErrUnauthorized.Wrap("private action prepared for another msg")
	}
	return sdk.UnwrapSDKContext(ctx).WithGasMeter(storetypes.NewInfiniteGasMeter()), st, nil
}

func verify(k Keeper, ctx context.Context, m personhoodtypes.Membership, prepared any) error {
	return k.personhood.VerifyMembership(ctx, m, prepared.(personhoodtypes.MembershipStatement))
}

// --- ballot inputs -------------------------------------------------------

// proposalRound is the round a vote on proposal counts in and when it opened:
// round 0 opens with the voting period, a later one when the chamber demoted
// the proposal from the expedited track.
func (k Keeper) proposalRound(ctx context.Context, proposal v1.Proposal) (uint64, int64, error) {
	r, err := k.ProposalRound.Get(ctx, proposal.Id)
	if err == nil {
		return r.Round, r.OpenedAt, nil
	}
	if !errors.Is(err, collections.ErrNotFound) {
		return 0, 0, err
	}
	if proposal.VotingStartTime == nil {
		return 0, 0, errorsmod.Wrapf(types.ErrProposalNotVoting, "proposal %d", proposal.Id)
	}
	return 0, proposal.VotingStartTime.Unix(), nil
}

// votingProposal returns proposal id if it is in its voting period.
func (k Keeper) votingProposal(ctx context.Context, id uint64) (v1.Proposal, error) {
	voting, err := k.gov.VotingPeriodProposals.Has(ctx, id)
	if err != nil {
		return v1.Proposal{}, err
	}
	if !voting {
		return v1.Proposal{}, errorsmod.Wrapf(types.ErrProposalNotVoting, "proposal %d", id)
	}
	return k.gov.Proposals.Get(ctx, id)
}

// proposalInputs is the statement (less the signal) a vote on proposal's
// current round proves: scope proposal/id/round; the signer it revokes
// excluded; an identity activated an identity root window before the round
// opened, so a person who switched identity during the round cannot vote in
// it twice.
func (k Keeper) proposalInputs(ctx context.Context, proposal v1.Proposal) (personhoodtypes.MembershipStatement, uint64, error) {
	var st personhoodtypes.MembershipStatement
	round, openedAt, err := k.proposalRound(ctx, proposal)
	if err != nil {
		return st, 0, err
	}
	excluded, err := excludedDsc(proposal)
	if err != nil {
		return st, 0, err
	}
	window, err := k.personhood.IdentityRootWindow(ctx)
	if err != nil {
		return st, 0, err
	}
	st.Scope = privacy.ProposalScope(proposal.Id, round)
	st.ExcludedDsc = excluded
	st.MaxActivation = openedAt - window
	return st, round, nil
}

// removalInputs is the statement (less the signal) a vote on option's open
// removal ballot proves.
func (k Keeper) removalInputs(ctx context.Context, optionID uint64) (personhoodtypes.MembershipStatement, types.RemovalBallot, error) {
	var st personhoodtypes.MembershipStatement
	ballot, err := k.RemovalBallots.Get(ctx, optionID)
	if errors.Is(err, collections.ErrNotFound) {
		return st, ballot, errorsmod.Wrapf(types.ErrBallotNotFound, "option %d", optionID)
	} else if err != nil {
		return st, ballot, err
	}
	window, err := k.personhood.IdentityRootWindow(ctx)
	if err != nil {
		return st, ballot, err
	}
	st.Scope = privacy.RemovalScope(ballot.BallotId)
	st.MaxActivation = ballot.OpenedAt - window
	return st, ballot, nil
}

// --- MsgVoteProposal -----------------------------------------------------

type voteProposalAction struct{ k Keeper }

func (a voteProposalAction) PrivateActionGas(ctx context.Context, _ shieldedtypes.PrivateMsg) (uint64, error) {
	return a.k.personhood.MembershipActionGas(ctx, 2)
}

func (a voteProposalAction) CheckPrivateAction(ctx context.Context, msg shieldedtypes.PrivateMsg) (any, error) {
	m := msg.(*types.MsgVoteProposal)
	proposal, err := a.k.votingProposal(ctx, m.ProposalId)
	if err != nil {
		return nil, err
	}
	st, _, err := a.k.proposalInputs(ctx, proposal)
	if err != nil {
		return nil, err
	}
	if err := a.k.personhood.CheckMembership(ctx, m.Membership); err != nil {
		return nil, err
	}
	if st.Signal, err = a.k.personhood.SignalOf(ctx, m); err != nil {
		return nil, err
	}
	return st, nil
}

func (a voteProposalAction) VerifyPrivateAction(ctx context.Context, msg shieldedtypes.PrivateMsg, prepared any) error {
	return verify(a.k, ctx, msg.(*types.MsgVoteProposal).Membership, prepared)
}

// --- MsgProposeRemoval ---------------------------------------------------

type proposeRemovalAction struct{ k Keeper }

func (a proposeRemovalAction) PrivateActionGas(ctx context.Context, _ shieldedtypes.PrivateMsg) (uint64, error) {
	return a.k.personhood.MembershipActionGas(ctx, 4)
}

func (k Keeper) checkProposeRemoval(ctx context.Context, optionID uint64) error {
	removable, err := k.allocation.GroundworksOptionRemovable(ctx, optionID)
	if err != nil {
		return err
	}
	if !removable {
		return errorsmod.Wrapf(types.ErrNotRemovable, "option %d", optionID)
	}
	// One ballot per option at a time, so an option cannot be kept under a
	// permanent rolling vote by anyone willing to reopen it.
	if has, err := k.RemovalBallots.Has(ctx, optionID); err != nil {
		return err
	} else if has {
		return errorsmod.Wrapf(types.ErrBallotExists, "option %d", optionID)
	}
	return nil
}

func (a proposeRemovalAction) CheckPrivateAction(ctx context.Context, msg shieldedtypes.PrivateMsg) (any, error) {
	m := msg.(*types.MsgProposeRemoval)
	if err := a.k.checkProposeRemoval(ctx, m.OptionId); err != nil {
		return nil, err
	}
	if err := a.k.personhood.CheckMembership(ctx, m.Membership); err != nil {
		return nil, err
	}
	window, err := a.k.personhood.IdentityRootWindow(ctx)
	if err != nil {
		return nil, err
	}
	// Fixed per UTC day, not per block, so a wallet knows the statement it
	// proves before its tx lands: an identity activated a root window before
	// today began.
	day := sdk.UnwrapSDKContext(ctx).BlockTime().Unix() / personhoodtypes.SecondsPerDay
	st := personhoodtypes.MembershipStatement{
		Scope:         privacy.ProposeRemovalScope(m.OptionId, uint64(day)),
		MaxActivation: day*personhoodtypes.SecondsPerDay - window,
	}
	if st.Signal, err = a.k.personhood.SignalOf(ctx, m); err != nil {
		return nil, err
	}
	return st, nil
}

func (a proposeRemovalAction) VerifyPrivateAction(ctx context.Context, msg shieldedtypes.PrivateMsg, prepared any) error {
	return verify(a.k, ctx, msg.(*types.MsgProposeRemoval).Membership, prepared)
}

// --- MsgVoteRemoval ------------------------------------------------------

type voteRemovalAction struct{ k Keeper }

func (a voteRemovalAction) PrivateActionGas(ctx context.Context, _ shieldedtypes.PrivateMsg) (uint64, error) {
	return a.k.personhood.MembershipActionGas(ctx, 2)
}

func (a voteRemovalAction) CheckPrivateAction(ctx context.Context, msg shieldedtypes.PrivateMsg) (any, error) {
	m := msg.(*types.MsgVoteRemoval)
	st, _, err := a.k.removalInputs(ctx, m.OptionId)
	if err != nil {
		return nil, err
	}
	if err := a.k.personhood.CheckMembership(ctx, m.Membership); err != nil {
		return nil, err
	}
	if st.Signal, err = a.k.personhood.SignalOf(ctx, m); err != nil {
		return nil, err
	}
	return st, nil
}

func (a voteRemovalAction) VerifyPrivateAction(ctx context.Context, msg shieldedtypes.PrivateMsg, prepared any) error {
	return verify(a.k, ctx, msg.(*types.MsgVoteRemoval).Membership, prepared)
}

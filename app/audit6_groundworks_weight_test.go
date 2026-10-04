package app

import (
	"errors"
	"testing"
	"time"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	stakingkeeper "github.com/cosmos/cosmos-sdk/x/staking/keeper"
	slashingtypes "github.com/cosmos/cosmos-sdk/x/slashing/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	"github.com/stretchr/testify/require"

	allocationkeeper "github.com/earth-network/earth/x/allocation/keeper"
	allocationtypes "github.com/earth-network/earth/x/allocation/types"
)

// Audit 6 D6-1: a delegation removed from a validator that is not Bonded
// (just created, jailed, unbonding, unbonded) used to keep its weight, with
// no stake behind it. Audit 7 D7-L2: an operator's Groundworks weight is its
// self-bond at a Bonded validator only; outside the active set it weighs
// nothing (its vote kept at weight zero while the bond remains).

type gwWeightEnv struct {
	*stakeEnv
	opt uint64
}

func initGwWeightEnv(t *testing.T) *gwWeightEnv {
	e := initStakeEnv(t)
	gov := authtypes.NewModuleAddress("gov")
	require.NoError(t, e.app.BankKeeper.SendCoins(e.ctx(), e.userAddr(), gov, sdk.NewCoins(sdk.NewInt64Coin("uerth", 10*ssErth))))
	res, err := allocationkeeper.NewMsgServerImpl(e.app.AllocationKeeper).AddAddressOption(e.ctx(), &allocationtypes.MsgAddAddressOption{
		Submitter: e.bech(gov), Stream: allocationtypes.STREAM_ID_GROUNDWORKS, Description: "a public good", Recipient: e.bech(e.userAddr()),
	})
	require.NoError(t, err)
	e.next(5 * time.Second)
	return &gwWeightEnv{stakeEnv: e, opt: res.Id}
}

func (g *gwWeightEnv) vote(op sdk.AccAddress) {
	_, err := allocationkeeper.NewMsgServerImpl(g.app.AllocationKeeper).SetAllocations(g.ctx(), &allocationtypes.MsgSetAllocations{
		Creator: g.bech(op), Stream: allocationtypes.STREAM_ID_GROUNDWORKS,
		Percentages: []allocationtypes.AllocationWeight{{OptionId: g.opt, Percent: 100}},
	})
	require.NoError(g.t, err)
}

// weight is the operator's stored Groundworks weight (0 once the record is gone).
func (g *gwWeightEnv) weight(op sdk.AccAddress) math.Int {
	v, err := g.app.AllocationKeeper.Voters.Get(g.ctx(), collections.Join(uint32(allocationtypes.STREAM_ID_GROUNDWORKS), []byte(op)))
	if errors.Is(err, collections.ErrNotFound) {
		return math.ZeroInt()
	}
	require.NoError(g.t, err)
	return v.Weight
}

// bonded is the operator's weight-bearing stake: its delegations at Bonded
// validators.
func (g *gwWeightEnv) bonded(op sdk.AccAddress) math.Int {
	b, err := g.app.AllocationKeeper.BondedWeight(g.ctx(), op)
	require.NoError(g.t, err)
	return b
}

func (g *gwWeightEnv) allocated() math.Int {
	o, err := g.app.AllocationKeeper.Options.Get(g.ctx(), collections.Join(uint32(allocationtypes.STREAM_ID_GROUNDWORKS), g.opt))
	require.NoError(g.t, err)
	return o.AmountAllocated
}

func (g *gwWeightEnv) undelegate(val sdk.ValAddress, amount math.Int) {
	_, err := stakingkeeper.NewMsgServerImpl(g.app.StakingKeeper).Undelegate(g.ctx(), stakingtypes.NewMsgUndelegate(
		g.bech(sdk.AccAddress(val)), g.valoper(val), sdk.NewCoin("uerth", amount)))
	require.NoError(g.t, err)
}

func (g *gwWeightEnv) status(val sdk.ValAddress) stakingtypes.BondStatus {
	v, err := g.app.StakingKeeper.GetValidator(g.ctx(), val)
	require.NoError(g.t, err)
	return v.GetStatus()
}

func (g *gwWeightEnv) selfBond(val sdk.ValAddress) math.Int {
	d, err := g.app.StakingKeeper.GetDelegation(g.ctx(), sdk.AccAddress(val), val)
	require.NoError(g.t, err)
	v, err := g.app.StakingKeeper.GetValidator(g.ctx(), val)
	require.NoError(g.t, err)
	return v.TokensFromSharesTruncated(d.Shares).TruncateInt()
}

func (g *gwWeightEnv) checkConsistent(op sdk.AccAddress) {
	g.t.Helper()
	require.Equal(g.t, g.bonded(op), g.weight(op), "weight follows the live bond")
	require.NoError(g.t, g.app.AllocationKeeper.AssertHotInvariants(g.ctx()))
}

// The audit's PoC: create, vote and undelegate the whole self-bond in one
// block, while the validator is still Unbonded. The vote finds no weight
// (D7-L2); once Bonded it weighs the bond, and the weight goes with the stake.
func TestAudit6GroundworksSameBlockCreateVoteUndelegate(t *testing.T) {
	g := initGwWeightEnv(t)
	base := g.allocated()
	val, _ := g.createValidator(500 * ssErth)
	op := sdk.AccAddress(val)
	require.Equal(t, stakingtypes.Unbonded, g.status(val))
	_, err := allocationkeeper.NewMsgServerImpl(g.app.AllocationKeeper).SetAllocations(g.ctx(), &allocationtypes.MsgSetAllocations{
		Creator: g.bech(op), Stream: allocationtypes.STREAM_ID_GROUNDWORKS,
		Percentages: []allocationtypes.AllocationWeight{{OptionId: g.opt, Percent: 100}},
	})
	require.ErrorIs(t, err, allocationtypes.ErrNoWeight, "an Unbonded validator's bond weighs nothing")
	require.Equal(t, base, g.allocated())
	g.next(5 * time.Second)
	require.Equal(t, stakingtypes.Bonded, g.status(val))
	g.vote(op)
	require.Equal(t, math.NewInt(500*ssErth), g.weight(op))
	require.Equal(t, base.AddRaw(500*ssErth), g.allocated())

	g.undelegate(val, math.NewInt(500*ssErth))
	require.True(t, g.weight(op).IsZero(), "phantom weight: %s", g.weight(op))
	require.Equal(t, base, g.allocated())
	g.checkConsistent(op)
	g.days(3)
	require.True(t, g.weight(op).IsZero())
	require.Equal(t, base, g.allocated())
	g.checkConsistent(op)
}

// A Bonded validator's operator votes, the validator is jailed (Unbonding),
// the operator withdraws part of the bond, then all of it after the
// validator is Unbonded. The weight tracks the bond at every step.
func TestAudit6GroundworksJailedThenFullUnbond(t *testing.T) {
	g := initGwWeightEnv(t)
	base := g.allocated()
	val, _ := g.createValidator(500 * ssErth)
	op := sdk.AccAddress(val)
	g.next(5 * time.Second)
	require.Equal(t, stakingtypes.Bonded, g.status(val))
	g.vote(op)
	g.checkConsistent(op)

	// Jailed: Unbonding, its tokens untouched, its weight gone (D7-L2) in
	// the block it left the active set; the vote stays.
	v, err := g.app.StakingKeeper.GetValidator(g.ctx(), val)
	require.NoError(t, err)
	cons, err := v.GetConsAddr()
	require.NoError(t, err)
	require.NoError(t, g.app.StakingKeeper.Jail(g.ctx(), cons))
	g.next(5 * time.Second)
	require.Equal(t, stakingtypes.Unbonding, g.status(val))
	g.checkConsistent(op)
	require.True(t, g.weight(op).IsZero())
	require.Equal(t, base, g.allocated())
	g.requireVoteKept(op)

	// A partial withdrawal from the Unbonding validator.
	g.undelegate(val, math.NewInt(200*ssErth))
	g.checkConsistent(op)
	require.True(t, g.weight(op).IsZero())
	g.requireVoteKept(op)

	// Past the unbonding time: Unbonded. The weight is unchanged.
	g.days(22)
	require.Equal(t, stakingtypes.Unbonded, g.status(val))
	g.checkConsistent(op)

	// The rest of the bond, from the Unbonded validator.
	g.undelegate(val, g.selfBond(val))
	require.True(t, g.weight(op).IsZero(), "phantom weight: %s", g.weight(op))
	require.Equal(t, base, g.allocated())
	g.checkConsistent(op)
}

// A Bonded validator's operator withdraws its whole self-bond: Bonded is the
// case that always worked, kept as a guard.
func TestAudit6GroundworksBondedFullUnbond(t *testing.T) {
	g := initGwWeightEnv(t)
	base := g.allocated()
	val, _ := g.createValidator(500 * ssErth)
	op := sdk.AccAddress(val)
	g.next(5 * time.Second)
	require.Equal(t, stakingtypes.Bonded, g.status(val))
	g.vote(op)
	g.undelegate(val, math.NewInt(100*ssErth))
	g.checkConsistent(op)
	g.undelegate(val, g.selfBond(val))
	require.True(t, g.weight(op).IsZero())
	require.Equal(t, base, g.allocated())
	g.checkConsistent(op)
}

// A redelegation of a self-bond is refused (the private staking module is the
// sole delegator besides an operator to its own validator), so the weight
// cannot move through one.
func TestAudit6GroundworksRedelegationRefused(t *testing.T) {
	g := initGwWeightEnv(t)
	val, _ := g.createValidator(500 * ssErth)
	op := sdk.AccAddress(val)
	g.next(5 * time.Second)
	g.vote(op)
	_, err := stakingkeeper.NewMsgServerImpl(g.app.StakingKeeper).BeginRedelegate(g.ctx(), stakingtypes.NewMsgBeginRedelegate(
		g.bech(op), g.valoper(val), g.valoper(g.genesisValidator()), sdk.NewInt64Coin("uerth", 100*ssErth)))
	require.Error(t, err)
	g.checkConsistent(op)
}

// requireVoteKept: the operator's vote is stored (at weight zero) while its
// bond remains outside the active set.
func (g *gwWeightEnv) requireVoteKept(op sdk.AccAddress) {
	g.t.Helper()
	v, err := g.app.AllocationKeeper.Voters.Get(g.ctx(), collections.Join(uint32(allocationtypes.STREAM_ID_GROUNDWORKS), []byte(op)))
	require.NoError(g.t, err)
	require.NotEmpty(g.t, v.Percentages)
}

// Audit 7 D7-L2: a jailed validator's operator loses its Groundworks weight
// in the block its validator leaves the active set, and gets it back, with
// no new vote, in the block it is Bonded again (unjailed).
func TestAudit7GroundworksBondedOnly(t *testing.T) {
	g := initGwWeightEnv(t)
	base := g.allocated()
	val, key := g.createValidator(500 * ssErth)
	op := sdk.AccAddress(val)
	g.next(5 * time.Second)
	g.vote(op)
	require.Equal(t, math.NewInt(500*ssErth), g.weight(op))

	v, err := g.app.StakingKeeper.GetValidator(g.ctx(), val)
	require.NoError(t, err)
	cons, err := v.GetConsAddr()
	require.NoError(t, err)
	require.NoError(t, g.app.SlashingKeeper.Jail(g.ctx(), cons))
	g.next(5 * time.Second)
	require.Equal(t, stakingtypes.Unbonding, g.status(val))
	require.True(t, g.weight(op).IsZero())
	require.Equal(t, base, g.allocated())
	g.requireVoteKept(op)
	g.checkConsistent(op)

	// Unjailed: Bonded again at the block's end, the weight back.
	res := g.run(g.signedTx(key, 300_000, 5_000, slashingtypes.NewMsgUnjail(g.valoper(val))))
	require.Equal(t, uint32(0), res.Code, res.Log)
	g.next(5 * time.Second)
	require.Equal(t, stakingtypes.Bonded, g.status(val))
	require.Equal(t, math.NewInt(500*ssErth), g.weight(op))
	require.Equal(t, base.AddRaw(500*ssErth), g.allocated())
	g.checkConsistent(op)
}

package app

import (
	"testing"
	"time"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/query"
	"github.com/stretchr/testify/require"

	sskeeper "github.com/earth-network/earth/x/shieldedstaking/keeper"
	sstypes "github.com/earth-network/earth/x/shieldedstaking/types"
	"github.com/earth-network/earth/zk/privacy"
)

// Query/Validators: a wallet reads every validator's quote inputs as one
// paged list (no query names the validator it is about to act on). Each
// entry matches the per-validator reads it replaces: the book (B, S, P, U,
// D, W), x/staking's status and jail, x/slashing's tombstone, delegatability
// and the module's redelegation entries out of it. A book whose validator
// x/staking removed comes on the last page.
func TestValidatorsQuery(t *testing.T) {
	e := initStakeEnv(t)
	e.fundPoolDirect(10_000 * ssErth)
	vA, _ := e.createValidator(1_000 * ssErth)
	vB, _ := e.createValidator(1_000 * ssErth)
	vC, _ := e.createValidator(1_000 * ssErth)
	e.next(5 * time.Second)
	e.fakeDelegate(vA, uint64(1_000*ssErth), "a")
	e.fakeDelegate(vC, uint64(10*ssErth), "c")
	e.next(25 * time.Hour) // epoch end: the queues are delegated
	e.next(5 * time.Second)
	infraction := e.height
	e.next(5 * time.Second)
	e.next(5 * time.Second)
	_, err := e.fakeRedelegate(vA, vB, uint64(100*ssErth), "m")
	require.NoError(t, err)
	e.next(5 * time.Second)
	e.doubleSign(vA, infraction) // vA jailed and tombstoned
	e.next(5 * time.Second)
	// A queued delegation at vC (P > 0).
	e.fakeDelegate(vC, uint64(5*ssErth), "c2")

	// A book whose validator x/staking no longer has.
	gone := sdk.ValAddress(privacy.FieldBytes(ssDet("gone", 0))[:20]).String()
	require.NoError(t, e.app.ShieldedStakingKeeper.Validators.Set(e.ctx(), gone, sstypes.ValidatorState{
		Validator: gone, PendingDelegation: math.NewInt(7), PendingUndelegation: math.ZeroInt(),
		EpochRate: math.LegacyOneDec(), DerthSupply: math.NewInt(7), SupplyAtBlockStart: math.ZeroInt(), SlashDebt: math.ZeroInt(),
	}))

	q := sskeeper.NewQueryServerImpl(e.app.ShieldedStakingKeeper)
	all, err := e.app.StakingKeeper.GetAllValidators(e.ctx())
	require.NoError(t, err)

	got := map[string]sstypes.ValidatorQuote{}
	var order []string
	var key []byte
	pages := 0
	for {
		res, err := q.Validators(e.ctx(), &sstypes.QueryValidatorsRequest{Pagination: &query.PageRequest{Key: key, Limit: 2, CountTotal: key == nil}})
		require.NoError(t, err)
		require.Equal(t, e.height+1, res.Height)
		if key == nil {
			require.Equal(t, uint64(len(all)+1), res.Pagination.Total, "x/staking's validators and the removed one's book")
		}
		pages++
		for _, vq := range res.Validators {
			_, dup := got[vq.Validator]
			require.False(t, dup, vq.Validator)
			got[vq.Validator] = vq
			order = append(order, vq.Validator)
		}
		if len(res.Pagination.NextKey) == 0 {
			break
		}
		key = res.Pagination.NextKey
	}
	require.Greater(t, pages, 1)
	require.Len(t, got, len(all)+1)
	require.Equal(t, gone, order[len(order)-1], "removed validators' books come last")

	for _, v := range all {
		vq, ok := got[v.OperatorAddress]
		require.True(t, ok, v.OperatorAddress)
		require.Equal(t, v.OperatorAddress, vq.Staking.OperatorAddress)
		require.Equal(t, v.Status, vq.Staking.Status)
		require.Equal(t, v.Jailed, vq.Staking.Jailed)
		one, err := q.Validator(e.ctx(), &sstypes.QueryValidatorRequest{Validator: v.OperatorAddress})
		require.NoError(t, err)
		require.Equal(t, one.Backing, vq.Backing)
		require.Equal(t, one.Supply, vq.Supply)
		require.Equal(t, one.Rate, vq.Rate)
		require.Equal(t, one.State, vq.Book)
		// B = D + W + P - U.
		if vq.Backing.IsPositive() {
			require.Equal(t, vq.Backing, vq.Delegation.Add(vq.Rewards).Add(vq.Book.PendingDelegation).Sub(vq.Book.PendingUndelegation))
		}
	}

	a := got[e.valoper(vA)]
	require.True(t, a.Staking.Jailed)
	require.True(t, a.Tombstoned)
	require.False(t, a.Delegatable)
	require.Contains(t, a.Refusal, "jailed")
	require.True(t, a.Supply.IsPositive())
	require.True(t, a.Delegation.IsPositive())
	require.Len(t, a.Redelegations, 1)
	require.Equal(t, e.valoper(vB), a.Redelegations[0].DstValidator)
	require.Equal(t, uint32(1), a.Redelegations[0].Entries)
	require.Equal(t, uint32(1), a.Redelegations[0].CountedEntries)

	b := got[e.valoper(vB)]
	require.True(t, b.Delegatable, b.Refusal)
	require.Empty(t, b.Refusal)
	require.False(t, b.Tombstoned)
	require.Empty(t, b.Redelegations)

	c := got[e.valoper(vC)]
	require.True(t, c.Delegatable, c.Refusal)
	require.Equal(t, math.NewInt(5*ssErth), c.Book.PendingDelegation, "the queue")

	g := got[gone]
	require.Empty(t, g.Staking.OperatorAddress)
	require.False(t, g.Delegatable)
	require.NotEmpty(t, g.Refusal)
	require.Equal(t, math.NewInt(7), g.Supply)
	require.Equal(t, math.NewInt(7), g.Backing)

	// Pages are capped; offset paging is x/staking's.
	res, err := q.Validators(e.ctx(), &sstypes.QueryValidatorsRequest{Pagination: &query.PageRequest{Limit: 10_000}})
	require.NoError(t, err)
	require.Len(t, res.Validators, len(all)+1)
	res, err = q.Validators(e.ctx(), &sstypes.QueryValidatorsRequest{Pagination: &query.PageRequest{Offset: 1, Limit: 1}})
	require.NoError(t, err)
	require.Len(t, res.Validators, 1)
	require.Equal(t, order[1], res.Validators[0].Validator)
	res, err = q.Validators(e.ctx(), nil)
	require.NoError(t, err)
	require.Len(t, res.Validators, len(all)+1)
}

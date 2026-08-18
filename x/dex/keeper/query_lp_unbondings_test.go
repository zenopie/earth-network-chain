package keeper_test

import (
	"testing"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"github.com/earth-network/earth/x/dex/keeper"
	"github.com/earth-network/earth/x/dex/types"
)

// A provider sees their own pending withdrawals and nobody else's.
//
// The store is ordered by completion time for the sweep, so this query walks
// and filters rather than seeking. The test therefore cares about two things
// the walk could get wrong: that another provider's entries are excluded, and
// that several entries for the same provider all come back rather than only the
// first match.
func TestLpUnbondingsFiltersByAddress(t *testing.T) {
	f := initFixture(t)
	qs := keeper.NewQueryServerImpl(f.keeper)

	mine := sdk.AccAddress([]byte("provider-mine_______"))
	theirs := sdk.AccAddress([]byte("provider-theirs_____"))

	mineStr, err := f.addressCodec.BytesToString(mine)
	require.NoError(t, err)
	theirsStr, err := f.addressCodec.BytesToString(theirs)
	require.NoError(t, err)

	put := func(completion int64, poolID uint64, addr sdk.AccAddress, addrStr string, shares int64) {
		require.NoError(t, f.keeper.LpUnbondings.Set(
			f.ctx,
			collections.Join3(completion, poolID, addr.Bytes()),
			types.LpUnbonding{
				Address:        addrStr,
				PoolId:         poolID,
				Shares:         sdk.NewCoin(types.LPShareDenom(poolID), math.NewInt(shares)),
				CompletionTime: completion,
			},
		))
	}

	// Two of mine, at different times and pools, with one of theirs between —
	// so a walk that stopped early or matched loosely would show up.
	put(100, 1, mine, mineStr, 10)
	put(200, 1, theirs, theirsStr, 999)
	put(300, 2, mine, mineStr, 20)

	res, err := qs.LpUnbondings(f.ctx, &types.QueryLpUnbondingsRequest{Address: mineStr})
	require.NoError(t, err)
	require.Len(t, res.Unbondings, 2)

	total := math.ZeroInt()
	for _, u := range res.Unbondings {
		require.Equal(t, mineStr, u.Address)
		total = total.Add(u.Shares.Amount)
	}
	require.Equal(t, math.NewInt(30), total, "both entries, not just the first")

	// A provider with nothing pending gets an empty list, not an error: having
	// no withdrawals is the normal state, not a failure to look one up.
	none := sdk.AccAddress([]byte("provider-none_______"))
	noneStr, err := f.addressCodec.BytesToString(none)
	require.NoError(t, err)

	res, err = qs.LpUnbondings(f.ctx, &types.QueryLpUnbondingsRequest{Address: noneStr})
	require.NoError(t, err)
	require.Empty(t, res.Unbondings)
}

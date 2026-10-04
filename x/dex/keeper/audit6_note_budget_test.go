package keeper_test

import (
	"testing"

	"cosmossdk.io/collections"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"github.com/earth-network/earth/x/dex/types"
)

// Audit 6 D-L-D1: the sweep's note budget is checked against what each payout
// would mint, before it mints: a payout that would pass it waits for the next
// block, at the head of the queue and not as a failure.
func TestAudit6LpUnbondNoteBudget(t *testing.T) {
	k, ctx, bank, sh := initNoteFixture(t)
	const id = 1
	pool := types.Pool{PoolId: id, ReserveErth: sdk.NewCoin("uerth", bigInt("1000000000000")),
		ReserveToken: sdk.NewCoin("ufoo", bigInt("1000000000000000000000000")), VolumeWeight: bigInt("0")}
	require.NoError(t, k.SetPool(ctx, id, pool))
	require.NoError(t, k.PoolByToken.Set(ctx, "ufoo", id))
	bank.fundModule(pool.ReserveErth, pool.ReserveToken)
	bank.setSupply(types.LPShareDenom(id), bigInt("1000000000000000000"))
	// Each: a token leg of ~9e20 (98 notes) and an ERTH leg (1 note).
	for i := byte(0); i < 3; i++ {
		shares := sdk.NewCoin(types.LPShareDenom(id), bigInt("900000000000000"))
		bank.fundModule(shares)
		entry := types.LpUnbonding{PoolId: id, Shares: shares, CompletionTime: ctx.BlockTime().Unix(),
			Pc: []byte{1}, Ciphertext: []byte{2}, ErthPc: []byte{3}, ErthCiphertext: []byte{4}, WithdrawalId: []byte{0, 9, i}}
		key, err := k.LpUnbondingKey(entry)
		require.NoError(t, err)
		require.NoError(t, k.LpUnbondings.Set(ctx, key, entry))
	}

	require.NoError(t, k.SweepMaturedUnbondings(ctx))
	require.LessOrEqual(t, len(sh.notes), types.LpUnbondNoteBudget)
	require.Equal(t, 2*99, len(sh.notes), "two payouts fit the budget, the third waits")
	left := 0
	require.NoError(t, k.LpUnbondings.Walk(ctx, nil, func(_ collections.Triple[int64, uint64, []byte], e types.LpUnbonding) (bool, error) {
		left++
		require.Zero(t, e.PayoutAttempts, "deferred, not failed")
		return false, nil
	}))
	require.Equal(t, 1, left)

	require.NoError(t, k.SweepMaturedUnbondings(ctx))
	require.Equal(t, 3*99, len(sh.notes))
}

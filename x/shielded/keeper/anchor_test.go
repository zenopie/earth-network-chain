package keeper_test

import (
	"testing"
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"github.com/earth-network/earth/x/shielded/keeper"
	shieldedtest "github.com/earth-network/earth/x/shielded/testutil"
	"github.com/earth-network/earth/x/shielded/types"
	"github.com/earth-network/earth/zk/privacy"
)

// Audit 5 L-SH1: CheckTx and ReCheckTx refuse an anchor lapsing within the
// margin (it would land after its expiry and fail unpaid); a block still
// takes it until it lapses; AnchorsValidAt (PrepareProposal) follows the
// block's time.
func TestExpiringAnchorRefusedInCheckTx(t *testing.T) {
	f := initFixture(t)
	s := shieldedtest.Default()
	f.shieldScenario(s)
	f.nextBlock(5 * time.Second)
	m := f.scenarioMsg(s, shieldedtest.Send2)
	root := m.Bundle.Actions[0].Anchor

	// A newer note makes another root the latest, so root starts expiring.
	funder := f.addr("funder")
	coin := sdk.NewInt64Coin(types.FeeDenom, 5)
	f.bank.mint(funder, coin)
	_, err := f.msgs.Shield(f.ctx, &types.MsgShield{Sender: f.bech(funder), Amount: coin,
		Pc: privacy.FieldBytes(shieldedtest.Det("later", 1)), Ciphertext: shieldedtest.BlindCT("later")})
	require.NoError(t, err)
	f.nextBlock(5 * time.Second)
	ok, _, expiresAt, err := f.k.Anchor(f.ctx, root)
	require.NoError(t, err)
	require.True(t, ok)
	require.NotZero(t, expiresAt)

	near := f.ctx.WithBlockTime(time.Unix(expiresAt-keeper.AnchorCheckTxMarginSeconds+1, 0))
	_, err = f.k.CheckPrivateMsg(near.WithIsCheckTx(true), m)
	require.ErrorIs(t, err, types.ErrUnknownRoot)
	require.ErrorContains(t, err, "expires")
	_, err = f.k.CheckPrivateMsg(near.WithIsReCheckTx(true), m)
	require.ErrorIs(t, err, types.ErrUnknownRoot)
	_, err = f.k.CheckPrivateMsg(near, m)
	require.NoError(t, err, "a block still takes it")
	require.True(t, f.k.AnchorsValidAt(f.ctx, m, expiresAt))
	require.False(t, f.k.AnchorsValidAt(f.ctx, m, expiresAt+1))
}

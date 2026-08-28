package keeper_test

import (
	"testing"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"github.com/earth-network/earth/x/personhood/keeper"
	"github.com/earth-network/earth/x/personhood/types"
)

// Unregister is the exit the chain did not have. Before it, a registration was
// only ever taken away by the chain -- on expiry, or when its Document Signer
// was revoked -- so undoing one meant waiting a year or resetting the chain from
// genesis. The second is not hypothetical: earth-1 was reset on 2026-08-26 to
// clear exactly one test registration.
func TestUnregisterRetiresTheRegistration(t *testing.T) {
	f := initFixture(t)
	sdkCtx := sdk.UnwrapSDKContext(f.ctx)
	ms := keeper.NewMsgServerImpl(f.keeper)

	addr := sdk.AccAddress([]byte("unregister-me-------"))
	addrStr, err := f.addressCodec.BytesToString(addr)
	require.NoError(t, err)
	nullifier := []byte("nullifier-to-be-freed-----------")

	seedVoter(t, f, sdkCtx, nullifier, addr, sdkCtx.BlockTime().Unix())

	weightBefore := humanTotalWeight(t, f, f.ctx)
	countBefore, err := f.keeper.RegCount.Get(f.ctx)
	require.NoError(t, err)

	resp, err := ms.Unregister(f.ctx, &types.MsgUnregister{Creator: addrStr})
	require.NoError(t, err)
	require.Equal(t, nullifier, resp.Nullifier, "the response names the nullifier it freed")

	// Every index the registration occupied, not just the primary one. A stale
	// row in any of these is a registration that queries can still see.
	_, err = f.keeper.Registrations.Get(f.ctx, nullifier)
	require.ErrorIs(t, err, collections.ErrNotFound, "nullifier still registered")
	_, err = f.keeper.RegByAddr.Get(f.ctx, addr.Bytes())
	require.ErrorIs(t, err, collections.ErrNotFound, "address still maps to a nullifier")
	has, err := f.keeper.RegByRegisteredAt.Has(f.ctx, collections.Join(sdkCtx.BlockTime().Unix(), nullifier))
	require.NoError(t, err)
	require.False(t, has, "still in the expiry index")

	countAfter, err := f.keeper.RegCount.Get(f.ctx)
	require.NoError(t, err)
	require.Equal(t, countBefore-1, countAfter, "live registration count not decremented")

	// The part that is easy to miss: a retired registration must give its vote
	// weight back, or it dilutes every human still verified while its option
	// keeps accruing ERTH nobody directs.
	require.Equal(t, weightBefore.SubRaw(types.VoterWeight).Int64(), humanTotalWeight(t, f, f.ctx).Int64(),
		"vote weight left in the human stream")

	// And the nullifier is genuinely free: the same person can register again.
	_, err = f.keeper.Registrations.Get(f.ctx, nullifier)
	require.ErrorIs(t, err, collections.ErrNotFound)
}

func TestUnregisterRejectsAnAddressThatIsNotRegistered(t *testing.T) {
	f := initFixture(t)
	ms := keeper.NewMsgServerImpl(f.keeper)

	addrStr, err := f.addressCodec.BytesToString(sdk.AccAddress([]byte("never-registered----")))
	require.NoError(t, err)

	_, err = ms.Unregister(f.ctx, &types.MsgUnregister{Creator: addrStr})
	require.ErrorIs(t, err, types.ErrNotRegistered)
}

// An expired registration is still unregisterable. It is retired either way, and
// refusing would leave the row in state until the sweep reached it while telling
// the holder they were not registered.
func TestUnregisterWorksOnAnExpiredRegistration(t *testing.T) {
	f := initFixture(t)
	sdkCtx := sdk.UnwrapSDKContext(f.ctx)
	ms := keeper.NewMsgServerImpl(f.keeper)

	params := types.DefaultParams()
	params.RegistrationValiditySeconds = 1000
	require.NoError(t, f.keeper.Params.Set(f.ctx, params))

	addr := sdk.AccAddress([]byte("lapsed-human--------"))
	addrStr, err := f.addressCodec.BytesToString(addr)
	require.NoError(t, err)
	nullifier := []byte("nullifier-long-since-lapsed-----")

	// Registered far enough in the past that the validity window has closed.
	seedVoter(t, f, sdkCtx, nullifier, addr, sdkCtx.BlockTime().Unix()-5000)

	_, err = ms.Unregister(f.ctx, &types.MsgUnregister{Creator: addrStr})
	require.NoError(t, err, "an expired registration should still be retirable by its holder")

	_, err = f.keeper.RegByAddr.Get(f.ctx, addr.Bytes())
	require.ErrorIs(t, err, collections.ErrNotFound)
}

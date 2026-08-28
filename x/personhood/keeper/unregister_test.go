package keeper_test

import (
	"testing"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"github.com/earth-network/earth/x/personhood/keeper"
	"github.com/earth-network/earth/x/personhood/types"
)

// Unregister is removed. It was the exit the chain did not have, and it was also
// an unbounded draw on the registration reward pool: it freed the nullifier, and
// a free nullifier is a fresh registration as far as Register is concerned. On
// earth-1 the loop ran in six blocks for a second payout of 31,534 ERTH.
//
// These tests pin the removal rather than the old behaviour, and they pin it at
// the message boundary: the handler is the whole of the fix, so an accidental
// restore has to fail here.
func TestUnregisterIsRemoved(t *testing.T) {
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

	_, err = ms.Unregister(f.ctx, &types.MsgUnregister{Creator: addrStr})
	require.ErrorIs(t, err, types.ErrUnregisterRemoved)

	// Rejected, not partially applied. Every index the registration occupies has
	// to still hold it -- a handler that cleared state and then errored would
	// leave the exploit open in a transaction that merely looks like it failed.
	reg, err := f.keeper.Registrations.Get(f.ctx, nullifier)
	require.NoError(t, err, "the registration was retired anyway")
	require.Equal(t, addrStr, reg.Address)
	got, err := f.keeper.RegByAddr.Get(f.ctx, addr.Bytes())
	require.NoError(t, err, "the address no longer maps to a nullifier")
	require.Equal(t, nullifier, got)
	has, err := f.keeper.RegByRegisteredAt.Has(f.ctx, collections.Join(sdkCtx.BlockTime().Unix(), nullifier))
	require.NoError(t, err)
	require.True(t, has, "dropped out of the expiry index")

	countAfter, err := f.keeper.RegCount.Get(f.ctx)
	require.NoError(t, err)
	require.Equal(t, countBefore, countAfter, "live registration count moved")

	require.Equal(t, weightBefore.Int64(), humanTotalWeight(t, f, f.ctx).Int64(),
		"vote weight left the human stream")
}

// The rejection does not depend on there being anything to retire. It is not a
// precondition failure that a future state could satisfy -- the message is gone.
func TestUnregisterIsRemovedForAnAddressThatIsNotRegistered(t *testing.T) {
	f := initFixture(t)
	ms := keeper.NewMsgServerImpl(f.keeper)

	addrStr, err := f.addressCodec.BytesToString(sdk.AccAddress([]byte("never-registered----")))
	require.NoError(t, err)

	_, err = ms.Unregister(f.ctx, &types.MsgUnregister{Creator: addrStr})
	require.ErrorIs(t, err, types.ErrUnregisterRemoved)
	require.NotErrorIs(t, err, types.ErrNotRegistered,
		"removal must not be reported as a state precondition")
}

// An expired registration was the one case unregister still did useful work on:
// it is retired either way, and refusing left the row in state until the sweep
// reached it. That work now belongs entirely to the expiry sweep.
func TestUnregisterIsRemovedForAnExpiredRegistration(t *testing.T) {
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

	seedVoter(t, f, sdkCtx, nullifier, addr, sdkCtx.BlockTime().Unix()-5000)

	_, err = ms.Unregister(f.ctx, &types.MsgUnregister{Creator: addrStr})
	require.ErrorIs(t, err, types.ErrUnregisterRemoved)

	_, err = f.keeper.RegByAddr.Get(f.ctx, addr.Bytes())
	require.NoError(t, err, "the registration was retired anyway")
}

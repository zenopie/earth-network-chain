package keeper

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/earth-network/earth/x/allocation/types"
)

// Adding an ADDRESS option is permissionless, so its one free-text field is the
// cheapest way to buy permanent per-block work: every option in a stream is
// decoded in every EndBlock by CheckStreamWeight.
func TestAddressOptionDescriptionIsCapped(t *testing.T) {
	e := newTestEnv(t)
	require.NoError(t, e.k.InitGenesis(e.ctx, *types.DefaultGenesis()))
	ms := NewMsgServerImpl(e.k)
	_, alice := e.addr("alice")

	_, err := ms.AddAddressOption(e.ctx, &types.MsgAddAddressOption{
		Submitter: alice, Stream: types.STREAM_ID_GROUNDWORKS, Recipient: alice,
		Description: strings.Repeat("a", types.MaxDescriptionLen+1),
	})
	require.ErrorIs(t, err, types.ErrDescriptionTooLong)

	// Rejected before the fee is taken: an oversized description costs the
	// submitter gas, not a burned ERTH.
	require.True(t, e.bank.burned.IsZero(), "the fee must not be burned for a rejected option")

	// The cap is bytes, not characters, because bytes are what state costs — and
	// exactly at the limit is allowed.
	ok, err := ms.AddAddressOption(e.ctx, &types.MsgAddAddressOption{
		Submitter: alice, Stream: types.STREAM_ID_GROUNDWORKS, Recipient: alice,
		Description: strings.Repeat("a", types.MaxDescriptionLen),
	})
	require.NoError(t, err)
	opt, err := e.k.Options.Get(e.ctx, optionKey(types.STREAM_ID_GROUNDWORKS, ok.Id))
	require.NoError(t, err)
	require.Len(t, opt.Description, types.MaxDescriptionLen)
}

// The gov-gated path is capped too. Not because governance needs protecting from
// itself, but so that every option in state is one a message could have made:
// genesis validation applies the same bound, and an export that Validate refuses
// cannot be imported anywhere.
func TestIntegratedOptionDescriptionIsCapped(t *testing.T) {
	e := newTestEnv(t)
	require.NoError(t, e.k.InitGenesis(e.ctx, *types.DefaultGenesis()))
	ms := NewMsgServerImpl(e.k)
	authority, _ := e.k.addressCodec.BytesToString(e.k.GetAuthority())

	_, err := ms.AddIntegratedOption(e.ctx, &types.MsgAddIntegratedOption{
		Authority: authority, Stream: types.STREAM_ID_GROUNDWORKS,
		Handler:     types.HandlerLPRewards,
		Description: strings.Repeat("a", types.MaxDescriptionLen+1),
	})
	require.ErrorIs(t, err, types.ErrDescriptionTooLong)
}

package app

import (
	"context"
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	assemblytypes "github.com/earth-network/earth/x/assembly/types"
	shieldedtypes "github.com/earth-network/earth/x/shielded/types"
)

type allTripped struct{}

func (allTripped) IsAllowed(context.Context, string) (bool, error) { return false, nil }

// Audit 5 L-AS2: a tripped breaker never stops the chamber's votes (every gov
// proposal needs them), and still stops everything else.
func TestAudit5ChamberVotesPassTheBreaker(t *testing.T) {
	b := chamberExemptBreaker{allTripped{}}
	for _, m := range []sdk.Msg{&assemblytypes.MsgVoteProposal{}, &assemblytypes.MsgVoteRemoval{}} {
		ok, err := b.IsAllowed(context.Background(), sdk.MsgTypeURL(m))
		require.NoError(t, err)
		require.True(t, ok)
	}
	ok, err := b.IsAllowed(context.Background(), sdk.MsgTypeURL(&shieldedtypes.MsgSend{}))
	require.NoError(t, err)
	require.False(t, ok)
}

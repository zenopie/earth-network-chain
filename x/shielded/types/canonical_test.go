package types_test

import (
	"strings"
	"testing"

	addresscodec "github.com/cosmos/cosmos-sdk/codec/address"
	"github.com/stretchr/testify/require"

	"github.com/earth-network/earth/x/shielded/types"
)

// A proof binds the receiver's bytes, not its spelling: an uppercase bech32
// re-spelling is refused rather than passed as the same tx under another
// hash.
func TestUnshieldReceiverCanonical(t *testing.T) {
	ac := addresscodec.NewBech32Codec("earth")
	addr, err := ac.BytesToString(make([]byte, 20))
	require.NoError(t, err)
	_, err = (&types.MsgSend{Receiver: addr}).UnshieldReceiver(ac)
	require.NoError(t, err)
	_, err = (&types.MsgSend{Receiver: strings.ToUpper(addr)}).UnshieldReceiver(ac)
	require.ErrorContains(t, err, "canonical")
}

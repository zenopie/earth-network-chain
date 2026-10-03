package app

import (
	"context"

	circuitante "cosmossdk.io/x/circuit/ante"
	sdk "github.com/cosmos/cosmos-sdk/types"

	assemblytypes "github.com/earth-network/earth/x/assembly/types"
)

// chamberVotes are the msgs the circuit breaker never stops: the assembly's
// votes. Every gov proposal needs the chamber's 2/3 of the human votes cast,
// so a tripped vote msg would fail every later proposal, the one resetting
// the breaker included (audit 5 L-AS2). Tripping them is refused outright
// rather than left to a later proposal no chamber could pass.
var chamberVotes = map[string]bool{
	sdk.MsgTypeURL(&assemblytypes.MsgVoteProposal{}): true,
	sdk.MsgTypeURL(&assemblytypes.MsgVoteRemoval{}):  true,
}

// chamberExemptBreaker is the circuit breaker the app runs, in the ante and
// the msg router: x/circuit's, except for chamberVotes.
type chamberExemptBreaker struct{ inner circuitante.CircuitBreaker }

func (b chamberExemptBreaker) IsAllowed(ctx context.Context, typeURL string) (bool, error) {
	if chamberVotes[typeURL] {
		return true, nil
	}
	return b.inner.IsAllowed(ctx, typeURL)
}

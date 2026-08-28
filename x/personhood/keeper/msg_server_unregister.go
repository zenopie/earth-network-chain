package keeper

import (
	"context"

	errorsmod "cosmossdk.io/errors"

	"github.com/earth-network/earth/x/personhood/types"
)

// Unregister is removed. It always fails.
//
// It existed as the exit the chain did not have, and it worked -- but it also
// freed the nullifier, and a free nullifier is a fresh registration. Register
// pays the registration reward and mints 1 ANML whenever the nullifier it sees
// is not already live, so unregister-then-register was an unbounded draw on the
// human stream's reward pool: one passport, once per block. On earth-1 it was
// done in six blocks for a second payout of 31,534 ERTH.
//
// The switch path already covers the case this was mostly used for. Registering
// again from a different wallet moves a live registration and deliberately pays
// nothing -- the circuit binds the registrant's address, so a proof cannot be
// lifted from somebody else's transaction to steal one. What is genuinely lost
// is leaving the registry outright, which now only happens on expiry or a
// Document Signer revocation.
//
// The message, its response and their proto definitions are all kept, and this
// method with them. Deleting the type would unregister it from the interface
// registry, and the historical MsgUnregister in block 4827 would stop decoding
// -- every tx query touching that block would fail rather than show what
// happened. Replay is unaffected either way: cosmovisor runs the pre-upgrade
// binary for pre-upgrade blocks.
func (k msgServer) Unregister(_ context.Context, msg *types.MsgUnregister) (*types.MsgUnregisterResponse, error) {
	return nil, errorsmod.Wrap(types.ErrUnregisterRemoved, msg.Creator)
}

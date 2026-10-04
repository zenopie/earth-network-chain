package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/x/personhood/types"
)

// CheckRegistration runs every check Register's private action makes on the
// registration itself — the passport proof, its binding to the msg's identity
// and notes, the Document Signer, the date, the rate caps — and writes
// nothing. It answers "would this registration be accepted, and for which
// passport?" for the gas-grant backend (`earthd gas-check registration`),
// which funds the fee note a registrant pays MsgRegister's fee from, and so
// sees the msg before its fee bundle exists: the fee is not checked here.
//
// Not a consensus path. Nothing in the state machine calls this.
func (k Keeper) CheckRegistration(ctx context.Context, msg *types.MsgRegister) (nullifier []byte, switched bool, err error) {
	if _, err := msg.Binding(k.addressCodec, sdk.UnwrapSDKContext(ctx).ChainID()); err != nil {
		return nil, false, err
	}
	p, err := k.checkRegistration(ctx, msg)
	if err != nil {
		return nil, false, err
	}
	if err := verifyRegistrationProof(msg, p); err != nil {
		return nil, false, err
	}
	return p.nullifier, p.switched, nil
}

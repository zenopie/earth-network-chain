package keeper

import (
	"context"

	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/x/personhood/types"
	"github.com/earth-network/earth/zk/privacy"
)

// CheckRegistration runs every check Register's private action makes on the
// registration itself — the passport proof, its binding to the msg's identity
// and notes, the Document Signer, the date, the rate caps — and writes
// nothing. It answers "would this registration be accepted, and for which
// passport?" for the gas-grant backend (`earthd gas-check registration`),
// which funds the fee note a registrant pays MsgRegister's fee from, and so
// sees the msg before its fee transfer exists: the fee is not checked here.
//
// Not a consensus path. Nothing in the state machine calls this.
func (k Keeper) CheckRegistration(ctx context.Context, msg *types.MsgRegister) (nullifier []byte, switched bool, err error) {
	if _, err := msg.Binding(k.addressCodec); err != nil {
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

// CheckGasMembership answers "is the prover of m some live registered human,
// asking for this month's transparent gas grant to addr?" for the gas-grant
// backend (`earthd gas-check membership`), which pays one grant of
// transparent ERTH per nullifier per month and never learns who.
//
// m is a membership proof with
//
//	scope            = privacy.GasScope(month)              (month = YYYYMM)
//	signal           = privacy.GasTransparentSignal(chain_id, addr)
//	excluded_dsc     = 0
//	excluded_country = 0
//	max_activation   = maxActivation, at most the block time
//
// against an identity root that is still an anchor (the latest, or one
// superseded within the identity root window). No activation delay: the
// grant only needs the prover to be live, and a nullifier is good for one
// grant a month whenever it was activated. Which month it is is the
// backend's call; this only checks the proof is for the month given.
//
// Not a consensus path. Nothing in the state machine calls this.
func (k Keeper) CheckGasMembership(ctx context.Context, m types.Membership, month uint64, addr []byte, maxActivation uint64) error {
	if err := m.ValidateBasic(); err != nil {
		return err
	}
	if mm := month % 100; month < 200001 || month > 999912 || mm < 1 || mm > 12 {
		return errorsmod.Wrapf(types.ErrInvalidMsg, "month %d is not YYYYMM", month)
	}
	if len(addr) == 0 || len(addr) > 255 {
		return errorsmod.Wrap(types.ErrInvalidMsg, "address must be 1..255 bytes")
	}
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	if now := sdkCtx.BlockTime().Unix(); now < 0 || maxActivation > uint64(now) {
		return errorsmod.Wrapf(types.ErrInvalidMsg, "max_activation %d is after the block time %d", maxActivation, now)
	}
	if err := k.CheckMembership(ctx, m); err != nil {
		return err
	}
	return k.VerifyMembership(ctx, m, MembershipStatement{
		Scope:         privacy.GasScope(month),
		Signal:        privacy.GasTransparentSignal(sdkCtx.ChainID(), addr),
		MaxActivation: int64(maxActivation),
	})
}

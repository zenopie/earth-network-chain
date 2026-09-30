package keeper

import (
	"context"

	corestore "cosmossdk.io/core/store"
	errorsmod "cosmossdk.io/errors"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"

	"github.com/earth-network/earth/x/shieldedspike/types"
)

type BankKeeper interface {
	SendCoinsFromModuleToModule(ctx context.Context, senderModule, recipientModule string, amt sdk.Coins) error
}

// Keeper holds the spend-once nullifier set and pays private fees out of the
// pool (this module's account) into fee_collector.
type Keeper struct {
	storeService corestore.KVStoreService
	bank         BankKeeper
}

func NewKeeper(ss corestore.KVStoreService, bank BankKeeper) Keeper {
	return Keeper{storeService: ss, bank: bank}
}

func nfKey(nf []byte) []byte { return append(append([]byte{}, types.NullifierPrefix...), nf...) }

func (k Keeper) HasNullifier(ctx context.Context, nf []byte) (bool, error) {
	return k.storeService.OpenKVStore(ctx).Has(nfKey(nf))
}

func (k Keeper) SetNullifier(ctx context.Context, nf []byte) error {
	return k.storeService.OpenKVStore(ctx).Set(nfKey(nf), []byte{1})
}

// MinFee is the consensus floor on a private tx's fee, never below 1.
func (k Keeper) MinFee(ctx context.Context) math.Int {
	min := math.NewInt(types.DefaultMinFee)
	bz, err := k.storeService.OpenKVStore(ctx).Get(types.MinFeeKey)
	if err == nil && bz != nil {
		if v, ok := math.NewIntFromString(string(bz)); ok {
			min = v
		}
	}
	if min.LT(math.OneInt()) {
		min = math.OneInt()
	}
	return min
}

func (k Keeper) SetMinFee(ctx context.Context, v math.Int) error {
	return k.storeService.OpenKVStore(ctx).Set(types.MinFeeKey, []byte(v.String()))
}

// PayFee moves fee from the pool to fee_collector, where x/earth's EndBlock
// SplitCollectedFees burns half and distribution pays the rest next block.
func (k Keeper) PayFee(ctx context.Context, fee sdk.Coins) error {
	if !fee.IsAllPositive() {
		return errorsmod.Wrap(sdkerrors.ErrInsufficientFee, "private fee must be positive")
	}
	return k.bank.SendCoinsFromModuleToModule(ctx, types.ModuleName, authtypes.FeeCollectorName, fee)
}

// authorizedKey carries, from the ante into msg execution, the nullifiers whose
// proof and fee the private ante chain accepted for this tx.
type authorizedKey struct{}

func WithAuthorized(ctx sdk.Context, nfs map[string]bool) sdk.Context {
	return ctx.WithValue(authorizedKey{}, nfs)
}

// IsAuthorized reports whether the private ante chain paid for this nullifier
// in the current tx. A private msg reaching its handler any other way — a
// contract's CosmosMsg::Any, an ICA host tx, anything that authorizes a msg by
// "every signer equals the caller" — sees zero signers, passes that loop
// vacuously, and must be stopped here.
func IsAuthorized(ctx sdk.Context, nf []byte) bool {
	nfs, ok := ctx.Value(authorizedKey{}).(map[string]bool)
	return ok && nfs[string(nf)]
}

type msgServer struct{ Keeper }

func NewMsgServerImpl(k Keeper) types.MsgServer { return msgServer{k} }

func (m msgServer) PrivateNoop(goCtx context.Context, msg *types.MsgPrivateNoop) (*types.MsgPrivateNoopResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	if !IsAuthorized(ctx, msg.Nullifier) {
		return nil, errorsmod.Wrap(sdkerrors.ErrUnauthorized, "private msg did not pass the private ante chain")
	}
	ctx.EventManager().EmitEvent(sdk.NewEvent("private_noop", sdk.NewAttribute("fee", msg.Fee)))
	return &types.MsgPrivateNoopResponse{}, nil
}

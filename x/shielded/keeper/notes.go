package keeper

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"strconv"

	errorsmod "cosmossdk.io/errors"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"

	"github.com/earth-network/earth/x/shielded/types"
	"github.com/earth-network/earth/zk/orchard"
	"github.com/earth-network/earth/zk/privacy"
)

// poolDepositKey marks a context in which this keeper is itself moving coins
// into the pool, alongside counting them into the turnstile. The send
// restriction refuses every other transfer to the pool account.
type poolDepositKey struct{}

func depositCtx(ctx context.Context) context.Context {
	return sdk.UnwrapSDKContext(ctx).WithValue(poolDepositKey{}, true)
}

func isPoolDeposit(ctx context.Context) bool {
	v, _ := ctx.Value(poolDepositKey{}).(bool)
	return v
}

// noteFor validates a public-value note and returns its commitment
// cm = H(TAG_CM, AssetID(denom), value, pc).
func (k Keeper) noteFor(ctx context.Context, coin sdk.Coin, pc, ciphertext []byte) ([]byte, error) {
	if !coin.IsValid() || !coin.IsPositive() || !coin.Amount.IsUint64() {
		return nil, errorsmod.Wrapf(types.ErrInvalidNote, "note value %s must be positive and fit a u64", coin)
	}
	pcEl, err := privacy.FieldFromBytes(pc)
	if err != nil {
		return nil, errorsmod.Wrapf(types.ErrInvalidNote, "pc: %v", err)
	}
	if err := types.CheckBlindCiphertext("ciphertext", ciphertext); err != nil {
		return nil, errorsmod.Wrap(types.ErrInvalidNote, err.Error())
	}
	id, err := k.AssetID(ctx, coin.Denom)
	if err != nil {
		return nil, err
	}
	asset, err := privacy.FieldFromBytes(id)
	if err != nil {
		return nil, err
	}
	return privacy.FieldBytes(privacy.CM(asset, coin.Amount.Uint64(), pcEl)), nil
}

// CheckMint refuses, without writing, the pc and ciphertext a later MintNote
// would refuse, and a tree without room for the largest private msg's outputs
// plus the note. For a private action's check, which must refuse before the
// ante spends anything. The asset and the source's balance are the caller's
// to ensure.
func (k Keeper) CheckMint(ctx context.Context, pc, ciphertext []byte) error {
	if _, err := privacy.FieldFromBytes(pc); err != nil {
		return errorsmod.Wrapf(types.ErrInvalidNote, "pc: %v", err)
	}
	if err := types.CheckBlindCiphertext("ciphertext", ciphertext); err != nil {
		return errorsmod.Wrap(types.ErrInvalidNote, err.Error())
	}
	return k.checkCapacity(ctx, types.MaxBundlesPerMsg*orchard.MaxActions+1)
}

// MintNote moves coin out of fromModule's account into the pool and appends a
// note of it to the owner behind pc. It is how other modules pay into the
// pool: personhood's ANML and registration reward, a dex swap's output, an
// unbonding claim.
//
// The coins must already be in fromModule's account; a module issuing new
// supply mints into its own account first and then calls this. MintNote
// never mints, so the turnstile counts exactly the coins that arrived and
// the pool balance stays In - Out without exception.
//
// ciphertext is required: the note's amount-blind v2 ciphertext
// (types.CheckBlindCiphertext), supplied by the msg that asked for the note.
// It is emitted with the note (and on the mint event), and is how the owner
// finds the note: trial decryption, then cm recomputed from the minted
// amount.
func (k Keeper) MintNote(ctx context.Context, fromModule string, coin sdk.Coin, pc, ciphertext []byte) (uint64, []byte, error) {
	if err := notThePool(fromModule); err != nil {
		return 0, nil, err
	}
	cm, err := k.noteFor(ctx, coin, pc, ciphertext)
	if err != nil {
		return 0, nil, err
	}
	if err := k.checkCapacity(ctx, 1); err != nil {
		return 0, nil, err
	}
	if err := k.bankKeeper.SendCoinsFromModuleToModule(depositCtx(ctx), fromModule, types.ModuleName, sdk.NewCoins(coin)); err != nil {
		return 0, nil, err
	}
	pos, err := k.depositNote(ctx, coin, cm, ciphertext)
	if err != nil {
		return 0, nil, err
	}
	sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(sdk.NewEvent(types.EventTypeMint,
		sdk.NewAttribute(types.AttributeKeyModule, fromModule),
		sdk.NewAttribute(types.AttributeKeyAmount, coin.String()),
		sdk.NewAttribute(types.AttributeKeyPosition, strconv.FormatUint(pos, 10)),
		sdk.NewAttribute(types.AttributeKeyCiphertext, base64.StdEncoding.EncodeToString(ciphertext)),
	))
	return pos, cm, nil
}

// Shield moves coin from sender into the pool as one note (MsgShield).
func (k Keeper) Shield(ctx context.Context, sender sdk.AccAddress, coin sdk.Coin, pc, ciphertext []byte) (uint64, []byte, error) {
	cm, err := k.noteFor(ctx, coin, pc, ciphertext)
	if err != nil {
		return 0, nil, err
	}
	if err := k.checkCapacity(ctx, 1); err != nil {
		return 0, nil, err
	}
	if err := k.bankKeeper.SendCoinsFromAccountToModule(depositCtx(ctx), sender, types.ModuleName, sdk.NewCoins(coin)); err != nil {
		return 0, nil, err
	}
	pos, err := k.depositNote(ctx, coin, cm, ciphertext)
	if err != nil {
		return 0, nil, err
	}
	senderStr, _ := k.addressCodec.BytesToString(sender)
	sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(sdk.NewEvent(types.EventTypeShield,
		sdk.NewAttribute(types.AttributeKeySender, senderStr),
		sdk.NewAttribute(types.AttributeKeyAmount, coin.String()),
		sdk.NewAttribute(types.AttributeKeyPosition, strconv.FormatUint(pos, 10)),
		sdk.NewAttribute(types.AttributeKeyCiphertext, base64.StdEncoding.EncodeToString(ciphertext)),
	))
	return pos, cm, nil
}

func (k Keeper) depositNote(ctx context.Context, coin sdk.Coin, cm, ciphertext []byte) (uint64, error) {
	if err := k.countIn(ctx, coin.Denom, coin.Amount); err != nil {
		return 0, err
	}
	return k.appendNote(ctx, cm, ciphertext)
}

// executeBundle is the note side of a verified bundle: spend every action's
// nullifier and append every action's output, in action order. The ante runs
// it, after every check, so that the msg's own handler cannot fail in a way
// that leaves the inputs spent and the outputs missing.
func (k Keeper) executeBundle(ctx context.Context, b *types.Bundle) ([]uint64, error) {
	for i := range b.Actions {
		nf := b.Actions[i].Nullifier
		spent, err := k.Nullifiers.Has(ctx, nf)
		if err != nil {
			return nil, err
		}
		if spent {
			return nil, types.ErrNullifierSpent.Wrapf("%X", nf)
		}
		if err := k.Nullifiers.Set(ctx, nf); err != nil {
			return nil, err
		}
		sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(sdk.NewEvent(types.EventTypeNullifier,
			sdk.NewAttribute(types.AttributeKeyNullifier, hex.EncodeToString(nf)),
		))
	}
	positions := make([]uint64, 0, len(b.Actions))
	for i := range b.Actions {
		pos, err := k.appendNote(ctx, b.Actions[i].Commitment, b.Actions[i].Ciphertext)
		if err != nil {
			return nil, err
		}
		positions = append(positions, pos)
	}
	return positions, nil
}

// payFee moves a private tx's fee from the pool to fee_collector, where
// x/earth's SplitCollectedFees burns half and distribution pays the rest.
func (k Keeper) payFee(ctx context.Context, fee math.Int) error {
	if !fee.IsPositive() {
		return errorsmod.Wrap(types.ErrReleaseMap, "private fee must be positive")
	}
	coins := sdk.NewCoins(sdk.NewCoin(types.FeeDenom, fee))
	if err := k.bankKeeper.SendCoinsFromModuleToModule(ctx, types.ModuleName, authtypes.FeeCollectorName, coins); err != nil {
		return err
	}
	if err := k.countOut(ctx, types.FeeDenom, fee); err != nil {
		return err
	}
	sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(sdk.NewEvent(types.EventTypeFee,
		sdk.NewAttribute(types.AttributeKeyAmount, coins.String()),
	))
	return nil
}

// unshield pays every remainder of an authorized msg to receiver: what its
// bundles released beyond the fee. The ante runs it for an UnshieldMsg, right
// after the fee, so an unshield is atomic with its spend.
func (k Keeper) unshield(ctx context.Context, msg types.PrivateMsg, receiver sdk.AccAddress) error {
	rem, err := types.Remainders(msg)
	if err != nil {
		return err
	}
	recv, _ := k.addressCodec.BytesToString(receiver)
	for _, r := range rem {
		coin, err := release(ctx, msg, r.Denom)
		if err != nil {
			return err
		}
		if err := k.bankKeeper.SendCoinsFromModuleToAccount(ctx, types.ModuleName, receiver, sdk.NewCoins(coin)); err != nil {
			return err
		}
		if err := k.countOut(ctx, coin.Denom, coin.Amount); err != nil {
			return err
		}
		sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(sdk.NewEvent(types.EventTypeUnshield,
			sdk.NewAttribute(types.AttributeKeyReceiver, recv),
			sdk.NewAttribute(types.AttributeKeyAmount, coin.String()),
		))
	}
	return nil
}

// ReleaseToModule pays an authorized msg's whole remainder of denom (its
// bundles' balance of denom, less the fee for uerth) to targetModule's
// account: the entry point for private msgs of other modules (a dex swap
// from notes, a private delegation). Each denom is released once. The
// caller's handler must be for a PrivateMsg whose sighash binds everything
// the payment depends on, and should not fail after this returns: the ante
// has already spent the inputs.
func (k Keeper) ReleaseToModule(ctx context.Context, msg types.PrivateMsg, denom, targetModule string) (sdk.Coin, error) {
	if err := notThePool(targetModule); err != nil {
		return sdk.Coin{}, err
	}
	coin, err := release(ctx, msg, denom)
	if err != nil {
		return sdk.Coin{}, err
	}
	if err := k.bankKeeper.SendCoinsFromModuleToModule(types.WithModuleRelease(ctx, targetModule), types.ModuleName, targetModule, sdk.NewCoins(coin)); err != nil {
		return sdk.Coin{}, err
	}
	if err := k.countOut(ctx, coin.Denom, coin.Amount); err != nil {
		return sdk.Coin{}, err
	}
	sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(sdk.NewEvent(types.EventTypeSpend,
		sdk.NewAttribute(types.AttributeKeyModule, targetModule),
		sdk.NewAttribute(types.AttributeKeyAmount, coin.String()),
	))
	return coin, nil
}

// notThePool refuses the pool's own module account as the other side of a
// module mint, release or fee payment: coins the pool already holds (backing
// existing notes) would back a second note, or pay a fee no note paid, while
// the bank sees nothing move.
func notThePool(module string) error {
	if module == types.ModuleName {
		return types.ErrUnauthorized.Wrap("the shielded pool cannot mint to, release to or pay fees for itself")
	}
	return nil
}

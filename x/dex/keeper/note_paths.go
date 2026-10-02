package keeper

import (
	"context"
	"strconv"

	"cosmossdk.io/collections"
	errorsmod "cosmossdk.io/errors"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/x/dex/types"
	shieldedkeeper "github.com/earth-network/earth/x/shielded/keeper"
	shieldedtypes "github.com/earth-network/earth/x/shielded/types"
)

// The note paths: the dex trading with the shielded pool.
//
// ANML exists only as notes, so every way a person reaches the ANML/ERTH
// pool goes through here, and every transparent leg in ANML is refused
// (swapExactIn, AddLiquidity, CreatePool, RemoveLiquidity without a pc).
//
//   - MsgNoteSwap (unsigned, private): spend any asset from a note, swap it
//     through any pools, mint the output as a note. May pay its fee from an
//     ERTH output.
//   - MsgBuyAnml (signed): a transparent account's ERTH (or any token, through
//     ERTH) for ANML, minted as a note.
//   - MsgAddLiquidityShielded (unsigned, private): both legs of a deposit from
//     notes, the LP shares minted as a note (LP shares are private: only the
//     pools' reserves and module-held liquidity are public), what the pool
//     ratio does not take minted back as notes.
//   - MsgRemoveLiquidityShielded (unsigned, private): shares from notes,
//     escrowed in an LpUnbonding with no account; at maturity both legs are
//     minted as notes (payoutUnbonding).
//   - MsgRemoveLiquidity of a shielded-only pool stores a pc; the matured
//     payout mints the token leg as a note (payoutUnbonding).
//
// The private msgs run their action in the private ante
// (x/shielded/types.PrivateActionExecutor), atomically with the spend of
// their notes: a swap or deposit whose price moved past its bound fails the
// whole tx before anything is spent, instead of failing after the ante had
// released the notes' value into nothing.
//
// The pool's ERTH reserves and this module's balance stay exactly what
// checkPoolTokenSolvency expects: the asset in arrives in this module's
// account (ReleaseToModule) and goes into the reserves; the output leaves the
// reserves and this module's account (MintNote) in the same execution.

// Fixed gas per action, on top of the bundles' and one note write per note
// minted: a two-hop swap settles two pools' rewards and moves their
// reserves; a deposit settles one and mints shares.
const (
	gasNoteSwap             uint64 = 300_000
	gasAddLiquidityShielded uint64 = 300_000
	// A withdrawal escrows and records now, and pays two notes at maturity.
	gasRemoveLiquidityShielded uint64 = 200_000
)

// prepared marks a msg whose action the ante checked.
type prepared struct{ kind string }

// ActionHandler implements x/shielded's PrivateActionHandler and
// PrivateActionExecutor for this module's private msgs.
type ActionHandler struct{ k Keeper }

// NewActionHandler returns the handler registered for this module's msgs.
func NewActionHandler(k Keeper) ActionHandler { return ActionHandler{k: k} }

// RegisterPrivateActions registers h for each of this module's private msgs.
func RegisterPrivateActions(register func(string, shieldedtypes.PrivateActionHandler), h ActionHandler) {
	register(types.TypeMsgNoteSwap, h)
	register(types.TypeMsgAddLiquidityShielded, h)
	register(types.TypeMsgRemoveLiquidityShielded, h)
}

func (h ActionHandler) PrivateActionGas(ctx context.Context, msg shieldedtypes.PrivateMsg) (uint64, error) {
	if h.k.shielded == nil {
		return 0, types.ErrInvalidPrivateMsg.Wrap("no shielded pool")
	}
	_, note, err := h.k.shielded.PrivateGasPrices(ctx)
	if err != nil {
		return 0, err
	}
	switch msg.(type) {
	case *types.MsgNoteSwap:
		return gasNoteSwap + note, nil
	case *types.MsgAddLiquidityShielded:
		return gasAddLiquidityShielded + 3*note, nil
	case *types.MsgRemoveLiquidityShielded:
		return gasRemoveLiquidityShielded + 2*note, nil
	}
	return 0, errorsmod.Wrapf(types.ErrInvalidPrivateMsg, "no private action for %T", msg)
}

// CheckPrivateAction refuses, before any proof is read, what cannot succeed
// whatever the prices: an unknown route or pool, an output asset the pool
// cannot hold, a note it cannot mint. Whether the price holds is the
// execution's to find out, atomically.
func (h ActionHandler) CheckPrivateAction(ctx context.Context, msg shieldedtypes.PrivateMsg) (any, error) {
	k := h.k
	var err error
	switch m := msg.(type) {
	case *types.MsgNoteSwap:
		err = k.checkNoteSwap(ctx, m)
	case *types.MsgAddLiquidityShielded:
		err = k.checkAddShielded(ctx, m)
	case *types.MsgRemoveLiquidityShielded:
		err = k.checkRemoveShielded(ctx, m)
	default:
		err = errorsmod.Wrapf(types.ErrInvalidPrivateMsg, "no private action for %T", msg)
	}
	if err != nil {
		return nil, err
	}
	return prepared{kind: sdk.MsgTypeURL(msg)}, nil
}

// VerifyPrivateAction: no msg of this module carries a proof beyond its
// bundle.
func (h ActionHandler) VerifyPrivateAction(context.Context, shieldedtypes.PrivateMsg, any) error {
	return nil
}

// ExecutesInAnte: a swap and a deposit price against the pools as they
// stand, and a withdrawal escrows what the spend released: all three run
// atomically with their spend.
func (h ActionHandler) ExecutesInAnte(shieldedtypes.PrivateMsg) bool { return true }

// ExecutePrivateAction runs the swap or deposit for the ante.
func (h ActionHandler) ExecutePrivateAction(ctx sdk.Context, msg shieldedtypes.PrivateMsg, _ any) (any, error) {
	switch m := msg.(type) {
	case *types.MsgNoteSwap:
		return h.k.executeNoteSwap(ctx, m)
	case *types.MsgAddLiquidityShielded:
		return h.k.executeAddShielded(ctx, m)
	case *types.MsgRemoveLiquidityShielded:
		return h.k.executeRemoveShielded(ctx, m)
	}
	return nil, errorsmod.Wrapf(types.ErrInvalidPrivateMsg, "no private action for %T", msg)
}

// ---- routes -----------------------------------------------------------------

// checkRoute refuses a swap from denomIn to denomOut with no pools to route
// it through the hub.
func (k Keeper) checkRoute(ctx context.Context, denomIn, denomOut string) error {
	hub, err := k.HubDenom(ctx)
	if err != nil {
		return err
	}
	for _, d := range []string{denomIn, denomOut} {
		if d == hub {
			continue
		}
		if ok, err := k.HasPoolForToken(ctx, d); err != nil {
			return err
		} else if !ok {
			return errorsmod.Wrapf(types.ErrPoolNotFound, "no pool for %s", d)
		}
	}
	return nil
}

// checkNoteOut refuses an output the pool could not mint: an asset it has not
// admitted, a malformed pc or ciphertext, a full tree.
func (k Keeper) checkNoteOut(ctx context.Context, denom string, pc, ct []byte) error {
	if _, err := k.shielded.AssetID(ctx, denom); err != nil {
		return err
	}
	return k.shielded.CheckMint(ctx, pc, ct)
}

// ---- MsgNoteSwap --------------------------------------------------------------

func (k Keeper) checkNoteSwap(ctx context.Context, m *types.MsgNoteSwap) error {
	if err := k.checkRoute(ctx, m.In().Denom, m.DenomOut); err != nil {
		return err
	}
	return k.checkNoteOut(ctx, m.DenomOut, m.Pc, m.Ciphertext)
}

// executeNoteSwap: the note's value into this module, through the pools, the
// fee from output (if any) to fee_collector, the rest minted to pc. Runs in
// the private ante after the bundle was spent; any error (min_amount_out
// not met) fails the whole tx, spend included.
func (k Keeper) executeNoteSwap(ctx sdk.Context, m *types.MsgNoteSwap) (*types.MsgNoteSwapResponse, error) {
	in, err := k.shielded.ReleaseToModule(ctx, m, m.In().Denom, types.ModuleName)
	if err != nil {
		return nil, err
	}
	out, err := k.swapExactIn(ctx, held, held, in, m.DenomOut, math.NewIntFromUint64(m.MinAmountOut))
	if err != nil {
		return nil, err
	}
	note := out
	if m.FeeFromOutput > 0 {
		fee := math.NewIntFromUint64(m.FeeFromOutput)
		if err := k.shielded.PayFeeFromModule(ctx, types.ModuleName, fee); err != nil {
			return nil, err
		}
		note = out.SubAmount(fee) // out >= min_amount_out > fee
	}
	pos, _, err := k.shielded.MintNote(ctx, types.ModuleName, note, m.Pc, m.Ciphertext)
	if err != nil {
		return nil, err
	}
	return &types.MsgNoteSwapResponse{TokenOut: out, Position: pos}, nil
}

// NoteSwap returns the swap the private ante executed.
func (k msgServer) NoteSwap(ctx context.Context, m *types.MsgNoteSwap) (*types.MsgNoteSwapResponse, error) {
	return anteResult[*types.MsgNoteSwapResponse](ctx, m)
}

// anteResult is what a private msg's handler returns: the result of the
// action the ante ran, and nothing else.
func anteResult[R any](ctx context.Context, msg shieldedtypes.PrivateMsg) (R, error) {
	var zero R
	res, executed, err := shieldedkeeper.AuthorizedResult(ctx, msg)
	if err != nil {
		return zero, err
	}
	r, ok := res.(R)
	if !executed || !ok {
		return zero, shieldedtypes.ErrUnauthorized.Wrap("the action was not executed by the private ante")
	}
	return r, nil
}

// ---- MsgBuyAnml -----------------------------------------------------------------

// BuyAnml swaps the creator's token_in for ANML, which stays in this module
// and is minted to pc as a note. Atomic: it is an ordinary signed msg.
func (k msgServer) BuyAnml(ctx context.Context, m *types.MsgBuyAnml) (*types.MsgBuyAnmlResponse, error) {
	if k.shielded == nil {
		return nil, types.ErrInvalidPrivateMsg.Wrap("no shielded pool")
	}
	creator, err := k.addressCodec.StringToBytes(m.Creator)
	if err != nil {
		return nil, errorsmod.Wrap(err, "invalid creator address")
	}
	minOut := math.ZeroInt()
	if m.MinAmountOut != "" {
		var ok bool
		if minOut, ok = math.NewIntFromString(m.MinAmountOut); !ok || minOut.IsNegative() {
			return nil, errorsmod.Wrap(types.ErrInvalidAmount, "invalid min_amount_out")
		}
	}
	if err := k.checkNoteOut(ctx, shieldedtypes.AnmlDenom, m.Pc, m.Ciphertext); err != nil {
		return nil, err
	}
	out, err := k.swapExactIn(ctx, swapParty{addr: creator}, held, m.TokenIn, shieldedtypes.AnmlDenom, minOut)
	if err != nil {
		return nil, err
	}
	pos, _, err := k.shielded.MintNote(ctx, types.ModuleName, out, m.Pc, m.Ciphertext)
	if err != nil {
		return nil, err
	}
	return &types.MsgBuyAnmlResponse{TokenOut: out, Position: pos}, nil
}

// ---- MsgAddLiquidityShielded ------------------------------------------------------

func (k Keeper) checkAddShielded(ctx context.Context, m *types.MsgAddLiquidityShielded) error {
	pool, err := k.Pool.Get(ctx, m.PoolId)
	if err != nil {
		return errorsmod.Wrapf(types.ErrPoolNotFound, "pool %d", m.PoolId)
	}
	if erth, token := m.Legs(); token.Denom != pool.ReserveToken.Denom || erth.Denom != pool.ReserveErth.Denom {
		return errorsmod.Wrapf(types.ErrInvalidDenom, "pool %d takes %s and %s", m.PoolId, pool.ReserveToken.Denom, pool.ReserveErth.Denom)
	}
	// The share asset is admitted to the pool on the first private deposit;
	// the note itself must be mintable.
	if err := k.shielded.CheckMint(ctx, m.SharePc, m.ShareCiphertext); err != nil {
		return err
	}
	return k.checkNoteOut(ctx, pool.ReserveToken.Denom, m.RefundPc, m.RefundCiphertext)
}

// executeAddShielded: both legs into this module, the deposit in the pool
// ratio, the shares minted as a note to share_pc (dexlp/<pool> admitted to
// the pool first), whatever the ratio did not take minted back to
// refund_pc. No account is involved. Runs in the private ante after the
// bundle was spent; any error (below min_shares) fails the whole tx, spend
// included.
func (k Keeper) executeAddShielded(ctx sdk.Context, m *types.MsgAddLiquidityShielded) (*types.MsgAddLiquidityShieldedResponse, error) {
	erth, token := m.Legs()
	tokenIn, err := k.shielded.ReleaseToModule(ctx, m, token.Denom, types.ModuleName)
	if err != nil {
		return nil, err
	}
	erthIn, err := k.shielded.ReleaseToModule(ctx, m, erth.Denom, types.ModuleName)
	if err != nil {
		return nil, err
	}
	// Both legs are already here: the deposit pulls nothing, and mints the
	// shares onto this module.
	shares, depErth, depTok, err := k.deposit(ctx, m.PoolId, erthIn, tokenIn, m.MinShares, nil,
		func(sdk.Coin, sdk.Coin) error { return nil })
	if err != nil {
		return nil, err
	}
	if _, err := k.shielded.RegisterAsset(ctx, shares.Denom); err != nil {
		return nil, err
	}
	sharePos, _, err := k.shielded.MintNote(ctx, types.ModuleName, shares, m.SharePc, m.ShareCiphertext)
	if err != nil {
		return nil, err
	}
	res := &types.MsgAddLiquidityShieldedResponse{
		Shares: shares, RefundErth: erthIn.Sub(depErth), RefundToken: tokenIn.Sub(depTok), SharePosition: sharePos,
	}
	for _, c := range []sdk.Coin{res.RefundErth, res.RefundToken} {
		if c.IsPositive() {
			if _, _, err := k.shielded.MintNote(ctx, types.ModuleName, c, m.RefundPc, m.RefundCiphertext); err != nil {
				return nil, err
			}
		}
	}
	return res, nil
}

// AddLiquidityShielded returns the deposit the private ante executed.
func (k msgServer) AddLiquidityShielded(ctx context.Context, m *types.MsgAddLiquidityShielded) (*types.MsgAddLiquidityShieldedResponse, error) {
	return anteResult[*types.MsgAddLiquidityShieldedResponse](ctx, m)
}

// ---- MsgRemoveLiquidityShielded ---------------------------------------------------

// checkRemoveShielded refuses a private withdrawal that could not pay out: an
// unknown pool, a leg the pool could not mint as a note (both denoms must be
// admitted assets), a malformed pc.
func (k Keeper) checkRemoveShielded(ctx context.Context, m *types.MsgRemoveLiquidityShielded) error {
	pool, err := k.Pool.Get(ctx, m.PoolId)
	if err != nil {
		return errorsmod.Wrapf(types.ErrPoolNotFound, "pool %d", m.PoolId)
	}
	if err := k.checkNoteOut(ctx, pool.ReserveErth.Denom, m.ErthPc, m.ErthCiphertext); err != nil {
		return errorsmod.Wrap(err, "erth_pc")
	}
	if err := k.checkNoteOut(ctx, pool.ReserveToken.Denom, m.TokenPc, m.TokenCiphertext); err != nil {
		return errorsmod.Wrap(err, "token_pc")
	}
	return nil
}

// executeRemoveShielded: the shares from notes onto this module (escrowed, as
// MsgRemoveLiquidity's are), and an LpUnbonding with no account, keyed by the
// withdrawal id, paying both legs as notes at maturity. Runs in the private
// ante after the bundle was spent.
func (k Keeper) executeRemoveShielded(ctx sdk.Context, m *types.MsgRemoveLiquidityShielded) (*types.MsgRemoveLiquidityShieldedResponse, error) {
	if err := k.checkRemoveShielded(ctx, m); err != nil {
		return nil, err
	}
	shares, err := k.shielded.ReleaseToModule(ctx, m, types.LPShareDenom(m.PoolId), types.ModuleName)
	if err != nil {
		return nil, err
	}
	params, err := k.Params.Get(ctx)
	if err != nil {
		return nil, err
	}
	completion := ctx.BlockTime().Unix() + int64(params.LpUnbondingSeconds)
	id := m.WithdrawalID()
	entry := types.LpUnbonding{
		PoolId: m.PoolId, Shares: shares, CompletionTime: completion,
		Pc: m.TokenPc, Ciphertext: m.TokenCiphertext, ErthPc: m.ErthPc, ErthCiphertext: m.ErthCiphertext,
		WithdrawalId: id,
	}
	if err := k.setLpUnbonding(ctx, collections.Join3(completion, m.PoolId, id), entry); err != nil {
		return nil, err
	}
	ctx.EventManager().EmitEvent(sdk.NewEvent("begin_unbond_liquidity",
		sdk.NewAttribute("pool_id", strconv.FormatUint(m.PoolId, 10)),
		sdk.NewAttribute("shares", shares.String()),
		sdk.NewAttribute("completion_time", strconv.FormatInt(completion, 10)),
	))
	return &types.MsgRemoveLiquidityShieldedResponse{CompletionTime: completion}, nil
}

// RemoveLiquidityShielded returns the withdrawal the private ante executed.
func (k msgServer) RemoveLiquidityShielded(ctx context.Context, m *types.MsgRemoveLiquidityShielded) (*types.MsgRemoveLiquidityShieldedResponse, error) {
	return anteResult[*types.MsgRemoveLiquidityShieldedResponse](ctx, m)
}

// SimulateSwapExactIn is what swapping tokenIn for denomOut would pay out
// against the pools as they stand, written nowhere. For wallets choosing a
// note swap's min_amount_out, and tests.
func (k Keeper) SimulateSwapExactIn(ctx context.Context, tokenIn sdk.Coin, denomOut string) (sdk.Coin, error) {
	out, _, err := k.simulateSwapExactIn(ctx, tokenIn, denomOut)
	return out, err
}

// simulateSwapExactIn runs the swap a note swap would (both legs held by the
// dex, pending LP rewards settled first) in a cache context it never writes,
// so neither state nor events escape. Gas is still charged to ctx's meter.
func (k Keeper) simulateSwapExactIn(ctx context.Context, tokenIn sdk.Coin, denomOut string) (sdk.Coin, swapFees, error) {
	cache, _ := sdk.UnwrapSDKContext(ctx).CacheContext()
	return k.swapExactInFees(cache, held, held, tokenIn, denomOut, math.ZeroInt())
}

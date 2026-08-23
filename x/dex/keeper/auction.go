package keeper

import (
	"context"
	"errors"
	"strconv"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/x/dex/types"
)

// The genesis liquidity auction.
//
// Two thirds of the pre-mine is credited to the dex module account at genesis
// and recorded here as two equal earmarks. Nothing moves when the window opens:
// the ERTH is already where it needs to be, and the auction state is only the
// record of what it is spoken for. That is what makes the auction pre-funded
// rather than mint-on-demand — there is no path by which starting one can
// create ERTH that genesis did not already account for.
//
// Settlement happens in the EndBlocker rather than on a message, so nobody can
// hold the auction open by declining to send one. It creates the pool and mints
// the LP shares to the module account; bidders then pull their own ERTH with
// MsgClaimLiquidityAuction, which keeps the settling block's cost independent
// of how many people bid.

// getAuction returns the auction, or ErrAuctionUnavailable if genesis did not
// configure one.
func (k Keeper) getAuction(ctx context.Context) (types.LiquidityAuction, error) {
	a, err := k.LiquidityAuction.Get(ctx)
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return types.LiquidityAuction{}, types.ErrAuctionUnavailable
		}
		return types.LiquidityAuction{}, err
	}
	return a, nil
}

// PoolCreationLocked reports whether MsgCreatePool is currently refused.
//
// Pool creation is closed until the genesis liquidity auction settles, and open
// permanently afterwards. The lock exists because the auction has to be able to
// claim its bid denom, and it cannot defend that denom itself: the dex allows
// one pool per spoke token, StartLiquidityAuction refuses to open when a pool
// already exists for the denom, and nothing can ever delete a pool. So a dust
// pool created before the window opens would block the auction for good — and
// the governance proposal to open it names the denom a full voting period in
// advance, which is ample warning to whoever wants to do that.
//
// It lifts itself. Settlement creates the pool for the bid denom, at which point
// the ordinary one-pool-per-token guard protects it and the lock has nothing
// left to do. Nothing has to be configured, set or remembered — which is the
// reason this is a lock on a state transition rather than a reserved-denom
// parameter that someone has to get right.
//
// A chain with no auction configured is never locked, so devnets and tests that
// skip the auction keep permissionless pool creation.
func (k Keeper) PoolCreationLocked(ctx context.Context) (bool, error) {
	a, err := k.getAuction(ctx)
	if err != nil {
		if errors.Is(err, types.ErrAuctionUnavailable) {
			return false, nil
		}
		return false, err
	}
	return a.Status != types.AUCTION_STATUS_SETTLED, nil
}

// SettleDueAuction creates the pool if an open auction has reached its deadline.
// It is a no-op in every other case, including when no auction is configured.
func (k Keeper) SettleDueAuction(ctx context.Context) error {
	a, err := k.getAuction(ctx)
	if err != nil {
		if errors.Is(err, types.ErrAuctionUnavailable) {
			return nil
		}
		return err
	}
	if a.Status != types.AUCTION_STATUS_OPEN {
		return nil
	}
	if sdk.UnwrapSDKContext(ctx).BlockTime().Unix() < a.EndTime {
		return nil
	}
	return k.settleAuction(ctx, a)
}

// settleAuction closes the bidding window.
//
// With no bids there is nothing to pair the pool earmark against, so the
// auction returns to PENDING with both earmarks intact and governance can open
// another window. Failing back to PENDING rather than SETTLED is deliberate:
// stranding two thirds of the pre-mine on the module account because nobody
// happened to bid would be unrecoverable, since no message can move it.
func (k Keeper) settleAuction(ctx context.Context, a types.LiquidityAuction) error {
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	if !a.TotalRaised.IsPositive() {
		a.Status = types.AUCTION_STATUS_PENDING
		a.BidDenom = ""
		a.EndTime = 0
		if err := k.LiquidityAuction.Set(ctx, a); err != nil {
			return err
		}
		sdkCtx.EventManager().EmitEvent(sdk.NewEvent("liquidity_auction_expired"))
		return nil
	}

	raised := sdk.NewCoin(a.BidDenom, a.TotalRaised)

	poolID, err := k.PoolSeq.Next(ctx)
	if err != nil {
		return err
	}

	// The LP shares are minted to the module account, which has no private key
	// and so can never sign a MsgRemoveLiquidity. Nobody can withdraw this
	// liquidity, governance included — it leaves only by being retired on the
	// schedule registered below, the same as the genesis ANML/ERTH pool's.
	shareAmt := initialShares(a.ErthForPool.Amount, raised.Amount)
	if !shareAmt.IsPositive() {
		return types.ErrZeroShares
	}
	shares := sdk.NewCoin(types.LPShareDenom(poolID), shareAmt)
	if err := k.bankKeeper.MintCoins(ctx, types.ModuleName, sdk.NewCoins(shares)); err != nil {
		return err
	}

	pool := types.Pool{
		PoolId:        poolID,
		ReserveErth:   a.ErthForPool,
		ReserveToken:  raised,
		Volume:        math.ZeroInt(),
		LastVolumeDay: uint64(sdkCtx.BlockTime().Unix()) / 86400,
	}
	if err := k.SetPool(ctx, poolID, pool); err != nil {
		return err
	}
	if err := k.PoolByToken.Set(ctx, raised.Denom, poolID); err != nil {
		return err
	}
	if err := k.initPoolLpIndex(ctx, poolID); err != nil {
		return err
	}

	// Start retiring the position the moment it exists. Its five years run from
	// the day the pool opens rather than from block zero, because governance
	// chooses when to hold the auction and the schedule should not be partly
	// spent before there is anything to retire.
	//
	// burn_token is false: the spoke side is a bridged asset the chain cannot
	// recreate, so only the ERTH is destroyed. See PolBurn in pool.proto.
	if err := k.PolBurns.Set(ctx, poolID, types.PolBurn{
		PoolId:          poolID,
		TotalShares:     shareAmt,
		SharesRemaining: shareAmt,
		StartTime:       sdkCtx.BlockTime().Unix(),
		DurationSeconds: types.PolBurnSeconds,
		BurnToken:       false,
	}); err != nil {
		return err
	}

	a.Status = types.AUCTION_STATUS_SETTLED
	a.PoolId = poolID
	if err := k.LiquidityAuction.Set(ctx, a); err != nil {
		return err
	}

	sdkCtx.EventManager().EmitEvent(
		sdk.NewEvent(
			"liquidity_auction_settled",
			sdk.NewAttribute("pool_id", strconv.FormatUint(poolID, 10)),
			sdk.NewAttribute("raised", raised.String()),
			sdk.NewAttribute("erth_paired", a.ErthForPool.String()),
			sdk.NewAttribute("erth_for_bidders", a.ErthForBidders.String()),
			sdk.NewAttribute("shares", shares.String()),
		),
	)
	return nil
}

// claimableFor returns the ERTH a bidder may take.
//
// Pro-rata division truncates, the same way the AMM's does, so the individual
// shares sum to slightly less than the earmark — under one uerth per bidder.
// That dust stays on the module account and is never spendable, which is the
// same place and the same fate as the rest of the protocol's own liquidity; it
// is not worth a bidder counter to chase.
//
// The cap is defensive rather than load-bearing: truncation cannot overshoot
// the earmark, but a claim must never be able to reach past it into the pool
// reserves sharing the module account.
func (k Keeper) claimableFor(a types.LiquidityAuction, bid types.AuctionBid) math.Int {
	if a.Status != types.AUCTION_STATUS_SETTLED || bid.Claimed {
		return math.ZeroInt()
	}
	if !a.TotalRaised.IsPositive() {
		return math.ZeroInt()
	}
	share := a.ErthForBidders.Amount.Mul(bid.Amount).Quo(a.TotalRaised)
	if remaining := a.ErthForBidders.Amount.Sub(a.Claimed); share.GT(remaining) {
		share = remaining
	}
	return share
}

package keeper

import (
	"context"
	"errors"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	"github.com/earth-network/earth/x/shieldedstaking/types"
)

// The rate.
//
// For validator v, derth/v is a claim on the module's position at v:
//
//	B_v    = D_v + W_v + P_v - U_v                   (backing, uerth)
//	rate_v = B_v / S_v                                (1 while S_v == 0)
//
//	D_v  the module's delegation to v, in tokens (truncated)
//	W_v  its unwithdrawn uerth rewards at v (truncated)
//	P_v  ERTH queued to delegate to v (ValidatorState.pending_delegation)
//	U_v  private undelegations booked against v and not yet undelegated
//	     (ValidatorState.pending_undelegation)
//	S_v  the derth/v outstanding (ValidatorState.derth_supply: stake notes
//	     and positions, each labelled exposure at what it is still worth
//	     (moves.go: a slash of a redelegation into v takes the derth the
//	     burnt value backed off S_v, so the rate does not move); derth is
//	     never a coin)
//
// Every conversion uses the live B_v and S_v, in integers, rounding toward the
// pool: a delegation of a may credit at most floor(a * S / B) derth; an undelegation of d
// is worth floor(d * B / S). Live, not the last epoch's rate, for two reasons:
//
//   - a slash lowers D_v the moment it lands. A stale rate would let whoever
//     saw the evidence first undelegate at the pre-slash value and leave the
//     loss with everyone else;
//   - rewards accrue in W_v every block. Without them a delegation made just
//     before the epoch end would buy a whole epoch's rewards it did not earn.
//
// Slashes also hit U_v (see hooks.go): undelegations booked this epoch are
// still bonded until the epoch ends, so they take their share.

// ValidatorState returns v's book, zero if none.
func (k Keeper) ValidatorState(ctx context.Context, valoper string) (types.ValidatorState, error) {
	vs, err := k.Validators.Get(ctx, valoper)
	if errors.Is(err, collections.ErrNotFound) {
		return types.ValidatorState{
			Validator:           valoper,
			PendingDelegation:   math.ZeroInt(),
			PendingUndelegation: math.ZeroInt(),
			EpochRate:           math.LegacyOneDec(),
			DerthSupply:         math.ZeroInt(),
			SupplyAtBlockStart:  math.ZeroInt(),
			SlashDebt:           math.ZeroInt(),
		}, nil
	}
	if err == nil && vs.DerthSupply.IsNil() {
		vs.DerthSupply = math.ZeroInt()
	}
	if err == nil && vs.SlashDebt.IsNil() {
		vs.SlashDebt = math.ZeroInt()
	}
	if err == nil && vs.SupplyAtBlockStart.IsNil() {
		vs.SupplyAtBlockStart = math.ZeroInt()
	}
	return vs, err
}

// Supply is S_v, the derth/v outstanding.
func (k Keeper) Supply(ctx context.Context, valoper string) math.Int {
	vs, err := k.ValidatorState(ctx, valoper)
	if err != nil {
		return math.ZeroInt()
	}
	return vs.DerthSupply
}

// delegation is the module's delegation to v: its tokens (truncated), its
// shares, and the validator. found is false when either is missing.
func (k Keeper) delegation(ctx context.Context, val sdk.ValAddress) (tokens math.Int, del stakingtypes.Delegation, v stakingtypes.Validator, found bool, err error) {
	v, err = k.staking.GetValidator(ctx, val)
	if errors.Is(err, stakingtypes.ErrNoValidatorFound) {
		return math.ZeroInt(), del, v, false, nil
	} else if err != nil {
		return math.ZeroInt(), del, v, false, err
	}
	del, err = k.staking.GetDelegation(ctx, k.modAddr, val)
	if errors.Is(err, stakingtypes.ErrNoDelegation) {
		return math.ZeroInt(), del, v, false, nil
	} else if err != nil {
		return math.ZeroInt(), del, v, false, err
	}
	if v.DelegatorShares.IsZero() {
		return math.ZeroInt(), del, v, true, nil
	}
	return v.TokensFromShares(del.Shares).TruncateInt(), del, v, true, nil
}

// pendingRewards is the module's unwithdrawn uerth at val, computed the way
// x/distribution's DelegationRewards query does: in a throwaway cache.
func (k Keeper) pendingRewards(ctx context.Context, v stakingtypes.Validator, del stakingtypes.Delegation) (math.Int, error) {
	cc, _ := sdk.UnwrapSDKContext(ctx).CacheContext()
	end, err := k.distr.IncrementValidatorPeriod(cc, v)
	if err != nil {
		return math.Int{}, err
	}
	r, err := k.distr.CalculateDelegationRewards(cc, v, del, end)
	if err != nil {
		return math.Int{}, err
	}
	return r.AmountOf(types.BondDenom).TruncateInt(), nil
}

// Backing is B_v and S_v.
func (k Keeper) Backing(ctx context.Context, valoper string) (backing, supply math.Int, err error) {
	bk, err := k.book(ctx, valoper)
	if err != nil {
		return math.Int{}, math.Int{}, err
	}
	return bk.backing, bk.supply, nil
}

// bookParts is v's book with the parts of its backing: B = D + W + P - U
// (floored at zero), S.
type bookParts struct {
	state      types.ValidatorState
	delegation math.Int // D
	rewards    math.Int // W
	backing    math.Int // B
	supply     math.Int // S
}

func (k Keeper) book(ctx context.Context, valoper string) (bookParts, error) {
	vs, err := k.ValidatorState(ctx, valoper)
	if err != nil {
		return bookParts{}, err
	}
	val, err := k.valAddr(valoper)
	if err != nil {
		return bookParts{}, err
	}
	d, del, v, found, err := k.delegation(ctx, val)
	if err != nil {
		return bookParts{}, err
	}
	w := math.ZeroInt()
	if found && !del.Shares.IsZero() {
		if w, err = k.pendingRewards(ctx, v, del); err != nil {
			return bookParts{}, err
		}
	}
	b := d.Add(w).Add(vs.PendingDelegation).Sub(vs.PendingUndelegation)
	if b.IsNegative() {
		b = math.ZeroInt()
	}
	return bookParts{state: vs, delegation: d, rewards: w, backing: b, supply: vs.DerthSupply}, nil
}

// Rate is the live rate_v.
func (k Keeper) Rate(ctx context.Context, valoper string) (math.LegacyDec, error) {
	b, s, err := k.Backing(ctx, valoper)
	if err != nil {
		return math.LegacyDec{}, err
	}
	return rateOf(b, s), nil
}

func rateOf(b, s math.Int) math.LegacyDec {
	if !s.IsPositive() {
		return math.LegacyOneDec()
	}
	return math.LegacyNewDecFromInt(b).QuoInt(s)
}

// derthFor is the most derth a delegation of amount may credit:
// floor(a * S / B), or a while nothing is outstanding.
func derthFor(amount, backing, supply math.Int) (math.Int, error) {
	if !supply.IsPositive() {
		return amount, nil
	}
	if !backing.IsPositive() {
		return math.Int{}, types.ErrValidator.Wrap("validator's derth is backed by nothing (slashed to zero)")
	}
	return amount.Mul(supply).Quo(backing), nil
}

// valueOf is the ERTH value of d derth: floor(d * B / S).
func valueOf(d, backing, supply math.Int) math.Int {
	if !supply.IsPositive() {
		return math.ZeroInt()
	}
	return d.Mul(backing).Quo(supply)
}

// checkDelegatable refuses a validator this module will not newly delegate
// to: unknown, jailed, tombstoned, or slashed to nothing. The SDK accepts all
// four; delegating to one would at best earn nothing and at worst lose it.
func (k Keeper) checkDelegatable(ctx context.Context, valoper string) error {
	val, err := k.valAddr(valoper)
	if err != nil {
		return err
	}
	v, err := k.staking.GetValidator(ctx, val)
	if err != nil {
		return types.ErrValidator.Wrapf("%s: %v", valoper, err)
	}
	if v.IsJailed() {
		return types.ErrValidator.Wrapf("%s is jailed", valoper)
	}
	if !v.Tokens.IsPositive() || v.InvalidExRate() || v.DelegatorShares.IsZero() {
		return types.ErrValidator.Wrapf("%s has been slashed to nothing", valoper)
	}
	cons, err := v.GetConsAddr()
	if err != nil {
		return types.ErrValidator.Wrap(err.Error())
	}
	if k.slashing.IsTombstoned(ctx, cons) {
		return types.ErrValidator.Wrapf("%s is tombstoned", valoper)
	}
	return nil
}

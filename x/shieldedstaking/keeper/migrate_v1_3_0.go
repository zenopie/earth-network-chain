package keeper

import (
	"context"
	"fmt"

	"cosmossdk.io/collections"
	errorsmod "cosmossdk.io/errors"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/x/shieldedstaking/types"
)

// V130Report is what MigrateV1_3_0 did, for the upgrade handler's log.
type V130Report struct {
	BaseVintage uint32
	Books       int
	Votes       int
	Moves       int
	Failures    []string
}

// MigrateV1_3_0 introduces stake vintages (app upgrade v1.3.0,
// UPGRADE_PLAN.md item 4). Steps 1 and 2 are the vintage accounting every
// later block relies on: they run first, in one cache, and if either fails
// the migration returns the error and writes nothing, so the upgrade
// handler fails and the chain halts at the upgrade height, to be restarted
// by a fixed binary (audit 1, L5: carrying on with no base vintage would
// rename every pre-v1.3.0 note's vintage each month; with no vintage
// supplies, every undelegation would fail). So does an x/staking
// unbonding_time above MaxUnbondingSeconds (a governance change made before
// the upgrade: the label windows would outlast the vintages). Steps 3 to 5
// are best-effort: each runs in its own cache, and a step that fails is
// reported (event shieldedstaking_epoch_failure, stage
// "upgrade_v1_3_0/<step>") and left as it was.
//
//  1. The base vintage is the upgrade block's month: every existing note's
//     unsuffixed derth/<valoper> asset names it.
//  2. Every book's derth_supply becomes its base vintage's supply.
//  3. Every Groundworks vote joins the base vintage and loses its lease
//     (split_expires_at, field 11, now reserved: rewritten without it); the lease queue (prefix 58) is cleared and the
//     totals move from (validator, option) (prefix 29, cleared) to
//     (validator, vintage, option), rebuilt from the votes, so every voter
//     weighs exactly what it did.
//  4. Every open move's credit is in the base vintage.
//  5. Every validator voter is re-filed (unchanged weights).
func (k Keeper) MigrateV1_3_0(ctx context.Context) (V130Report, error) {
	base := k.currentVintage(ctx)
	rep := V130Report{BaseVintage: base}
	step := func(name string, fn func(cc context.Context) error) {
		if err := k.guarded(ctx, fn); err != nil {
			k.failure(ctx, "upgrade_v1_3_0/"+name, "", err)
			rep.Failures = append(rep.Failures, fmt.Sprintf("%s: %v", name, err))
		}
	}
	if err := k.guarded(ctx, func(cc context.Context) error {
		ut, err := k.staking.UnbondingTime(cc)
		if err != nil {
			return err
		}
		if s := unbondingSeconds(ut); s > types.MaxUnbondingSeconds {
			return types.ErrInvariant.Wrapf("x/staking unbonding_time is %ds, above the %ds a stake vintage's label window allows", s, types.MaxUnbondingSeconds)
		}
		if err := k.BaseVintage.Set(cc, base); err != nil {
			return errorsmod.Wrap(err, "base_vintage")
		}
		var books []types.ValidatorState
		if err := k.Validators.Walk(cc, nil, func(_ string, vs types.ValidatorState) (bool, error) {
			books = append(books, vs)
			return false, nil
		}); err != nil {
			return err
		}
		for _, vs := range books {
			if vs.DerthSupply.IsNil() || !vs.DerthSupply.IsPositive() {
				continue
			}
			if err := k.VintageSupplies.Set(cc, collections.Join(vs.Validator, base), vs.DerthSupply); err != nil {
				return errorsmod.Wrap(err, "supplies")
			}
			rep.Books++
		}
		return nil
	}); err != nil {
		return rep, errorsmod.Wrap(err, "v1.3.0: stake vintage accounting")
	}
	var vals []string
	step("groundworks", func(cc context.Context) error {
		if _, err := k.clearPrefix(cc, types.GwLapsesKey); err != nil {
			return err
		}
		if _, err := k.clearPrefix(cc, types.GwTotalsKey); err != nil {
			return err
		}
		var votes []types.GroundworksVote
		if err := k.GwVotes.Walk(cc, nil, func(_ uint64, v types.GroundworksVote) (bool, error) {
			votes = append(votes, v)
			return false, nil
		}); err != nil {
			return err
		}
		for _, v := range votes {
			v.Vintage = base // its lease (field 11, now reserved) is not decoded
			if v.Pending.IsNil() {
				v.Pending = math.ZeroInt()
			}
			if err := k.setVote(cc, v); err != nil {
				return err
			}
		}
		rep.Votes = len(votes)
		if err := k.rebuildGwTotals(cc); err != nil {
			return err
		}
		return k.GwEpoch.Walk(cc, nil, func(v string, _ uint64) (bool, error) {
			vals = append(vals, v)
			return false, nil
		})
	})
	step("moves", func(cc context.Context) error {
		var moves []types.Move
		if err := k.Moves.Walk(cc, nil, func(_ []byte, mv types.Move) (bool, error) {
			moves = append(moves, mv)
			return false, nil
		}); err != nil {
			return err
		}
		for _, mv := range moves {
			mv.Vintage = base
			if err := k.Moves.Set(cc, mv.Key, mv); err != nil {
				return err
			}
		}
		rep.Moves = len(moves)
		return nil
	})
	// x/allocation's BeginBlock has not run in the upgrade block yet: the
	// voters are re-filed at its end (the weights are unchanged anyway).
	for _, v := range vals {
		step("voter", func(cc context.Context) error { return k.GwResync.Set(cc, v) })
	}
	sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(sdk.NewEvent("shieldedstaking_upgrade_v1_3_0",
		sdk.NewAttribute(types.AttributeKeyVintage, fmt.Sprint(base)),
		sdk.NewAttribute("books", fmt.Sprint(rep.Books)),
		sdk.NewAttribute("votes", fmt.Sprint(rep.Votes)),
		sdk.NewAttribute("moves", fmt.Sprint(rep.Moves)),
		sdk.NewAttribute("failures", fmt.Sprint(len(rep.Failures))),
	))
	return rep, nil
}

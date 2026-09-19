package keeper

import (
	"bytes"
	"context"
	"errors"
	"strconv"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/x/allocation/types"
	earthtypes "github.com/earth-network/earth/x/earth/types"
)

// Removing an option that people are actively voting for.
//
// The idle sweep in prune.go can delete an option outright because it only ever
// touches zero-weight ones: "Removing a zero-weight option therefore cannot
// leave a voter's split subtracting from something that is no longer there." A
// struck option breaks that premise on both counts — it has weight, and voters
// name it in splits that resyncVoter replays on every stake change, without a
// transaction and without anyone watching.
//
// So a strike does not delete. It zeroes the option's weight, burns what it had
// accrued, and marks the record removed. The record surviving is what keeps the
// replay safe: resyncVoter finds the option and skips it, rather than finding
// nothing and erroring out of a staking hook.
//
// After that the option is zero-weight, which is the state the idle sweep was
// built for, so it is collected by the machinery that already exists rather than
// by a second deletion path written for this one case.

// RegisterChamber names the address allowed to strike an option. Called once,
// from x/assembly's module wiring — the same direction x/personhood's weight
// source and x/dex's reward handler travel, which is what keeps this module
// unaware of the modules that use it.
func (k Keeper) RegisterChamber(addr []byte) {
	k.chamber.addr = addr
}

// GroundworksOptionRemovable reports whether option id exists on the groundworks
// stream and has not already been struck.
func (k Keeper) GroundworksOptionRemovable(ctx context.Context, id uint64) (bool, error) {
	opt, err := k.Options.Get(ctx, optionKey(types.STREAM_ID_GROUNDWORKS, id))
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	return !opt.Removed, nil
}

// RemoveGroundworksOption strikes a groundworks option at the chamber's
// instruction.
//
// Groundworks only, and not by accident: the caretaker slate is directed by the
// same humans voting continuously, and they can defund an option there by
// re-aiming their own votes in any block. A second instrument pointed at their
// own slate would add nothing and would be one more lever to capture.
//
// Idempotent. A ballot can carry against an option that has meanwhile been
// struck by an earlier one, or collected by the idle sweep; that is a race, not
// a fault, and halting the chain over it in an EndBlocker would be absurd.
func (k Keeper) RemoveGroundworksOption(ctx context.Context, caller []byte, id uint64) error {
	if len(k.chamber.addr) == 0 || !bytes.Equal(caller, k.chamber.addr) {
		return types.ErrInvalidSigner.Wrap("only the assembly may remove an allocation option")
	}
	const stream = types.STREAM_ID_GROUNDWORKS

	opt, err := k.Options.Get(ctx, optionKey(stream, id))
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return nil
		}
		return err
	}
	if opt.Removed {
		return nil
	}

	// Settle first, so everything the option earned while it was live is counted
	// before it is taken away. Skipping this would forfeit a different number
	// than the one the option was actually owed.
	if err := k.AdvanceIndex(ctx, stream); err != nil {
		return err
	}
	rewardIndex, err := k.getRewardIndex(ctx, stream)
	if err != nil {
		return err
	}
	settleOption(&opt, rewardIndex)

	forfeited := accruedOf(opt)
	weight := math.ZeroInt()
	if !opt.AmountAllocated.IsNil() {
		weight = opt.AmountAllocated
	}

	opt.Removed = true
	opt.AmountAllocated = math.ZeroInt()
	opt.Accumulated = math.ZeroInt()
	// Through setOption, unlike the idle sweep: this option has weight and a
	// balance, and setOption is what walks SummedWeight and SummedAccrued down to
	// match. It also reschedules the prune, which now sees a zero-weight option.
	if err := k.setOption(ctx, stream, opt); err != nil {
		return err
	}

	// TotalWeight is the one running figure setOption does not maintain — it
	// moves in resyncVoter, by a voter's whole weight. The voters pointing here
	// are not being touched, so it has to come down by what this option held or
	// the two numbers stop agreeing and the EndBlock invariant halts the chain.
	if weight.IsPositive() {
		total, err := k.getTotalWeight(ctx, stream)
		if err != nil {
			return err
		}
		if err := k.TotalWeight.Set(ctx, key(stream), total.Sub(weight)); err != nil {
			return err
		}
	}

	// An INTEGRATED option is resolved by its handler every block from the
	// bounded key set BeginBlocker walks. Leaving a struck one in that set would
	// keep paying it.
	if opt.Kind == types.ALLOCATION_KIND_INTEGRATED {
		if err := k.IntegratedOptions.Remove(ctx, optionKey(stream, id)); err != nil {
			return err
		}
	}

	// The coins behind the forfeited balance are real — AdvanceIndex minted them
	// as they accrued — so they are burned rather than merely written off, for
	// the same reason the idle sweep burns them. setOption has already taken
	// them out of SummedAccrued; the burn is what takes them out of the module's
	// balance so the two still describe the same thing.
	if forfeited.IsPositive() {
		denom, err := k.HubDenom(ctx)
		if err != nil {
			return err
		}
		burned := sdk.NewCoins(sdk.NewCoin(denom, forfeited))
		if err := k.bankKeeper.BurnCoins(ctx, types.ModuleName, burned); err != nil {
			return err
		}
		if err := k.burnRecorder.RecordBurn(ctx, earthtypes.SourceAllocation, burned); err != nil {
			return err
		}
	}

	sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(sdk.NewEvent(
		"remove_allocation_option",
		sdk.NewAttribute("stream", stream.String()),
		sdk.NewAttribute("option_id", strconv.FormatUint(id, 10)),
		sdk.NewAttribute("weight_released", weight.String()),
		sdk.NewAttribute("forfeited", forfeited.String()),
	))
	return nil
}

// ChamberFacade adapts the keeper to the narrow interface x/assembly declares,
// so that module can depend on two methods rather than on this whole keeper.
type ChamberFacade struct{ k Keeper }

// NewChamberFacade wraps the keeper for x/assembly.
func NewChamberFacade(k Keeper) ChamberFacade { return ChamberFacade{k: k} }

func (f ChamberFacade) RemoveGroundworksOption(ctx context.Context, caller []byte, id uint64) error {
	return f.k.RemoveGroundworksOption(ctx, caller, id)
}

func (f ChamberFacade) GroundworksOptionRemovable(ctx context.Context, id uint64) (bool, error) {
	return f.k.GroundworksOptionRemovable(ctx, id)
}

package keeper

import (
	"context"
	"errors"
	"fmt"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"

	"github.com/earth-network/earth/x/allocation/types"
)

// InitGenesis initializes the module's state from a provided genesis state.
//
// An empty stream list is a fresh chain: both streams start at zero and each is
// seeded with its own option #1. A non-empty one is an import and is restored
// exactly as exported, seeding nothing — re-seeding on top of an import would
// hand out option id 1 a second time and silently repoint every voter who had
// allocated to the first one.
func (k Keeper) InitGenesis(ctx context.Context, genState types.GenesisState) error {
	if err := k.initGenesis(ctx, genState); err != nil {
		return err
	}
	return k.assertGenesisFunded(ctx)
}

// assertGenesisFunded refuses an import the module account cannot back.
//
// This is the accrued-balance counterpart to the total_weight check in
// types.Validate(), and it lives here rather than there for a plain reason:
// Validate sees only this module's own file, and the coins are a bank balance
// declared in another module's. InitGenesis can see both, and the module init
// order runs bank well before allocation, so by the time this fires the balance
// is real.
//
// It reuses CheckSolvency rather than reimplementing the comparison, so genesis
// is held to the exact rule the EndBlocker enforces every block afterwards. A
// genesis that fails here would otherwise start a chain that halts on its own
// invariant at height 1 — which reads as a consensus failure and is far harder
// to diagnose than a refused genesis file.
func (k Keeper) assertGenesisFunded(ctx context.Context) error {
	rep, err := k.CheckSolvency(ctx)
	if err != nil {
		return err
	}
	if rep.Broken() {
		return fmt.Errorf(
			"allocation genesis is underfunded: the options carry %s accrued and residue is %s, "+
				"so the module account needs %s uerth, but genesis gives it %s (short %s). "+
				"Fund the module account in the bank genesis, or export again with the balances that match",
			rep.Accrued, rep.Residue, rep.Accrued.Add(rep.Residue), rep.Held, rep.Short)
	}
	return nil
}

func (k Keeper) initGenesis(ctx context.Context, genState types.GenesisState) error {
	if err := k.Params.Set(ctx, genState.Params); err != nil {
		return err
	}

	// Residue is carried rather than derived — see the field's note in
	// genesis.proto. Restored before anything else so it is in place for the
	// solvency check the moment the module has options to be solvent about.
	residue := math.ZeroInt()
	if !genState.Residue.IsNil() {
		residue = genState.Residue
	}
	if err := k.Residue.Set(ctx, residue); err != nil {
		return err
	}

	if len(genState.Streams) > 0 {
		// SummedAccrued gets exactly the treatment SummedWeight gets in
		// restoreStream, and for the same reason: the options come back verbatim
		// rather than through setOption, so nothing else moves it.
		//
		// Left at zero, the module was blind rather than wrong. Every option keeps
		// its real Accumulated and the module account keeps the coins backing
		// them, but owed = accrued + residue collapses to roughly residue, so
		// Held sails past it and SolvencyReport.Broken() — which tests only Short
		// — reports a tolerated surplus. The check stayed green while unable to
		// see a shortfall up to the size of the whole un-imported balance.
		//
		// It is derived state, so it is not exported and needs no proto field.
		summedAccrued := math.ZeroInt()
		for _, st := range genState.Streams {
			if err := k.restoreStream(ctx, st); err != nil {
				return err
			}
			for _, opt := range st.Options {
				summedAccrued = summedAccrued.Add(accruedOf(opt))
			}
		}
		return k.SummedAccrued.Set(ctx, summedAccrued)
	}

	// Zeroed explicitly, mirroring SummedWeight below, so a fresh chain does not
	// depend on the getter's not-found default. It must be set before the seeded
	// options are appended: those go through setOption, which moves it.
	if err := k.SummedAccrued.Set(ctx, math.ZeroInt()); err != nil {
		return err
	}

	for _, stream := range types.Streams {
		if err := k.RewardIndex.Set(ctx, key(stream), math.ZeroInt()); err != nil {
			return err
		}
		if err := k.TotalWeight.Set(ctx, key(stream), math.ZeroInt()); err != nil {
			return err
		}
		if err := k.SummedWeight.Set(ctx, key(stream), math.ZeroInt()); err != nil {
			return err
		}
		if err := k.Epoch.Set(ctx, key(stream), 0); err != nil {
			return err
		}
		if err := k.LastUpkeep.Set(ctx, key(stream), 0); err != nil {
			return err
		}
		if err := k.OptionSeq.Set(ctx, key(stream), 0); err != nil {
			return err
		}
	}

	// Option #1 of each stream. Both are INTEGRATED and both are id 1 — ids are
	// per stream, so they do not collide, and each is resolved only by the
	// handler registered for its own stream. The capital stream carries a second
	// seeded option: the emergency fund, which pays the community pool.
	regID, err := k.appendOption(ctx, types.STREAM_ID_CARETAKER, types.AllocationOption{
		Description: "Registration rewards",
		Kind:        types.ALLOCATION_KIND_INTEGRATED,
		Handler:     types.HandlerRegistrationRewards,
	})
	if err != nil {
		return err
	}
	// Pre-fund it. The registration reward is drawn as a fraction of whatever the
	// pool holds, so a pool that starts empty pays the first humans nothing —
	// and they are the ones worth paying, because they arrive before there is a
	// network to arrive for.
	//
	// The coins are a bank balance on this module's account, put there by the
	// same genesis file. Nothing is minted here: emission is minted as it
	// accrues, and a genesis balance did not accrue.
	if !genState.RegistrationRewardSeed.IsNil() && genState.RegistrationRewardSeed.IsPositive() {
		opt, err := k.Options.Get(ctx, optionKey(types.STREAM_ID_CARETAKER, regID))
		if err != nil {
			return err
		}
		opt.Accumulated = genState.RegistrationRewardSeed
		if err := k.setOption(ctx, types.STREAM_ID_CARETAKER, opt); err != nil {
			return err
		}
	}
	if _, err := k.appendOption(ctx, types.STREAM_ID_GROUNDWORKS, types.AllocationOption{
		Description: "Volume-weighted LP rewards",
		Kind:        types.ALLOCATION_KIND_INTEGRATED,
		Handler:     types.HandlerLPRewards,
	}); err != nil {
		return err
	}
	// The emergency fund. Seeded here rather than left to a governance proposal
	// so the option exists from height 1 and stakers can direct weight at it
	// without waiting on one; INTEGRATED options are authority-gated to add, so
	// the alternative is a gov vote before the fund can receive anything.
	//
	// It carries no weight until someone votes for it, so seeding costs the other
	// options nothing.
	if _, err := k.appendOption(ctx, types.STREAM_ID_GROUNDWORKS, types.AllocationOption{
		Description: "Emergency fund (community pool)",
		Kind:        types.ALLOCATION_KIND_INTEGRATED,
		Handler:     types.HandlerCommunityPool,
	}); err != nil {
		return err
	}

	return nil
}

// restoreStream writes one exported stream back verbatim, rebuilding the only
// index that is derived: the set of integrated options, which is just the
// options whose kind says so.
func (k Keeper) restoreStream(ctx context.Context, st types.StreamState) error {
	kk := key(st.Stream)

	if err := k.RewardIndex.Set(ctx, kk, st.RewardIndex); err != nil {
		return err
	}
	if err := k.TotalWeight.Set(ctx, kk, st.TotalWeight); err != nil {
		return err
	}
	if err := k.Epoch.Set(ctx, kk, st.Epoch); err != nil {
		return err
	}
	if err := k.LastUpkeep.Set(ctx, kk, st.LastUpkeep); err != nil {
		return err
	}
	if err := k.OptionSeq.Set(ctx, kk, st.OptionSeq); err != nil {
		return err
	}

	// The options are restored verbatim rather than through setOption, so the
	// running sum is rebuilt here from what they carry. It is derived state and
	// so is not exported; genesis validation has already checked that it will
	// come out equal to the total weight being restored above.
	summed := math.ZeroInt()
	for _, opt := range st.Options {
		if !opt.AmountAllocated.IsNil() {
			summed = summed.Add(opt.AmountAllocated)
		}
		if err := k.Options.Set(ctx, optionKey(st.Stream, opt.Id), opt); err != nil {
			return err
		}
		if opt.Kind == types.ALLOCATION_KIND_INTEGRATED {
			if err := k.IntegratedOptions.Set(ctx, optionKey(st.Stream, opt.Id)); err != nil {
				return err
			}
		}
		// The removal schedule is derived, like the running sum, so it is rebuilt
		// here rather than carried. An option that was already dead at export
		// starts its grace period again from the genesis time, which is the only
		// clock a restarted chain has and errs towards keeping things.
		if err := k.refreshPruneSchedule(ctx, st.Stream, opt); err != nil {
			return err
		}
	}

	if err := k.SummedWeight.Set(ctx, kk, summed); err != nil {
		return err
	}

	for _, v := range st.Voters {
		addrBz, err := k.addressCodec.StringToBytes(v.Address)
		if err != nil {
			return err
		}
		if err := k.Voters.Set(ctx, voterKey(st.Stream, addrBz), v.Voter); err != nil {
			return err
		}
	}

	return nil
}

// ExportGenesis returns the module's exported genesis.
//
// Everything a stream needs to resume where it stopped: the reward index and the
// weight it divides by, the epoch that decides whether a voter's record still
// counts, the id sequence, and every option and vote. Exporting only the params
// — which is what this did — silently discarded every option anyone had paid to
// add, every allocation anyone had set, and every option's accrued but unclaimed
// rewards.
func (k Keeper) ExportGenesis(ctx context.Context) (*types.GenesisState, error) {
	genesis := types.DefaultGenesis()
	params, err := k.Params.Get(ctx)
	if err != nil {
		return nil, err
	}
	genesis.Params = params

	// The one figure here that no option carries. See genesis.proto: dropping it
	// strands the coins behind it and blinds the solvency check by the same
	// amount.
	residue, err := k.GetResidue(ctx)
	if err != nil {
		return nil, err
	}
	genesis.Residue = residue

	for _, stream := range types.Streams {
		st, err := k.exportStream(ctx, stream)
		if err != nil {
			return nil, err
		}
		genesis.Streams = append(genesis.Streams, st)
	}

	return genesis, nil
}

func (k Keeper) exportStream(ctx context.Context, stream types.StreamId) (types.StreamState, error) {
	idx, err := k.getRewardIndex(ctx, stream)
	if err != nil {
		return types.StreamState{}, err
	}
	total, err := k.getTotalWeight(ctx, stream)
	if err != nil {
		return types.StreamState{}, err
	}
	epoch, err := k.getEpoch(ctx, stream)
	if err != nil {
		return types.StreamState{}, err
	}
	seq, err := k.OptionSeq.Get(ctx, key(stream))
	if err != nil && !errors.Is(err, collections.ErrNotFound) {
		return types.StreamState{}, err
	}

	st := types.StreamState{
		Stream:      stream,
		RewardIndex: idx,
		TotalWeight: total,
		Epoch:       epoch,
		// Deliberately zero: see last_upkeep in genesis.proto. Carrying the
		// cursor would advance the index by the whole restart gap in one block.
		LastUpkeep: 0,
		OptionSeq:  seq,
	}

	rng := collections.NewPrefixedPairRange[uint32, uint64](key(stream))
	if err := k.Options.Walk(ctx, rng,
		func(_ collections.Pair[uint32, uint64], opt types.AllocationOption) (bool, error) {
			st.Options = append(st.Options, opt)
			return false, nil
		}); err != nil {
		return types.StreamState{}, err
	}

	vrng := collections.NewPrefixedPairRange[uint32, []byte](key(stream))
	if err := k.Voters.Walk(ctx, vrng,
		func(pk collections.Pair[uint32, []byte], v types.Voter) (bool, error) {
			addr, err := k.addressCodec.BytesToString(pk.K2())
			if err != nil {
				return true, err
			}
			st.Voters = append(st.Voters, types.VoterEntry{Address: addr, Voter: v})
			return false, nil
		}); err != nil {
		return types.StreamState{}, err
	}

	return st, nil
}

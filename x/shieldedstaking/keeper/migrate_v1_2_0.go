package keeper

import (
	"context"

	"cosmossdk.io/math"

	"cosmossdk.io/collections"
	storetypes "cosmossdk.io/store/types"

	"github.com/earth-network/earth/x/shieldedstaking/types"
)

// MigrateV1_2_0 retires Groundworks positions for stake note votes (app
// upgrade v1.2.0). Positions shared their store prefixes
// with the votes that replace them (5, the records; 6, the id sequence, which
// the votes continue), so every record under 5 is a position: each is
// deleted with its lease (58), the Groundworks totals (29, 30), the retired
// by-validator index (11) and its gov votes (key 0x01 || id; their weight
// stays in the proposal's tally until it ends), and every validator voter
// they fed is re-filed empty. A deleted position's derth leaves its
// validator's supply (its backing stays, shared by the remaining holders:
// nobody else could ever redeem it; unless it is the whole supply, which is
// left as it is); holders unlock before the upgrade.
// Returns how many positions it deleted.
func (k Keeper) MigrateV1_2_0(ctx context.Context) (int, error) {
	// The validators whose voters positions fed.
	var vals []string
	if err := k.GwEpoch.Walk(ctx, nil, func(v string, _ uint64) (bool, error) {
		vals = append(vals, v)
		return false, nil
	}); err != nil {
		return 0, err
	}
	// Positions decode as votes (fields 1-4 and 7 kept their numbers).
	retired := map[string]math.Int{}
	if err := k.GwVotes.Walk(ctx, nil, func(_ uint64, p types.GroundworksVote) (bool, error) {
		if !p.Derth.IsNil() && p.Derth.IsPositive() {
			d, ok := retired[p.Validator]
			if !ok {
				d = math.ZeroInt()
			}
			retired[p.Validator] = d.Add(p.Derth)
		}
		return false, nil
	}); err != nil {
		return 0, err
	}
	for v, d := range retired {
		if has, err := k.Validators.Has(ctx, v); err != nil {
			return 0, err
		} else if !has {
			continue
		}
		vs, err := k.ValidatorState(ctx, v)
		if err != nil {
			return 0, err
		}
		// All of it in positions: left as it is (a supply of 0 with a
		// backing would have no rate).
		if d.GTE(vs.DerthSupply) {
			continue
		}
		// Checkpointed first, as every supply change is: a proposal
		// snapshotted before the upgrade keeps the supply it was taken at.
		if err := k.checkpointSupply(ctx, &vs); err != nil {
			return 0, err
		}
		vs.DerthSupply = vs.DerthSupply.Sub(d)
		if err := k.Validators.Set(ctx, v, vs); err != nil {
			return 0, err
		}
	}
	n, err := k.clearPrefix(ctx, types.GwVotesKey)
	if err != nil {
		return 0, err
	}
	for _, p := range []collections.Prefix{types.GwLapsesKey, types.GwTotalsKey, types.GwEpochKey, collections.NewPrefix(11)} {
		if _, err := k.clearPrefix(ctx, p); err != nil {
			return 0, err
		}
	}
	var posVotes []collections.Pair[uint64, []byte]
	if err := k.Votes.Walk(ctx, nil, func(key collections.Pair[uint64, []byte], _ types.StakeVote) (bool, error) {
		if len(key.K2()) > 0 && key.K2()[0] == 1 {
			posVotes = append(posVotes, key)
		}
		return false, nil
	}); err != nil {
		return 0, err
	}
	for _, key := range posVotes {
		if err := k.Votes.Remove(ctx, key); err != nil {
			return 0, err
		}
	}
	for _, v := range vals {
		if err := k.syncValidatorVoter(ctx, v); err != nil {
			return 0, err
		}
	}
	return n, nil
}

// clearPrefix deletes every key under prefix, returning how many.
func (k Keeper) clearPrefix(ctx context.Context, prefix collections.Prefix) (int, error) {
	store := k.storeService.OpenKVStore(ctx)
	start := prefix.Bytes()
	it, err := store.Iterator(start, storetypes.PrefixEndBytes(start))
	if err != nil {
		return 0, err
	}
	var keys [][]byte
	for ; it.Valid(); it.Next() {
		keys = append(keys, append([]byte(nil), it.Key()...))
	}
	if err := it.Close(); err != nil {
		return 0, err
	}
	for _, key := range keys {
		if err := store.Delete(key); err != nil {
			return 0, err
		}
	}
	return len(keys), nil
}

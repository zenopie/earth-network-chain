package keeper

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strconv"

	"cosmossdk.io/collections"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/x/shieldedstaking/types"
	"github.com/earth-network/earth/zk/merkle"
	"github.com/earth-network/earth/zk/privacy"
)

// The stake note tree. Delegated stake (derth/<valoper>) exists only as
// notes here: an
// append-only depth-32 Poseidon2 tree like the shielded pool's, with its own
// nullifier set and root window, proven against by circuits/stake. Its notes
// are owner-locked (see the circuit): a stake note can be merged, split,
// undelegated, locked or voted (without spending it, circuits/vote) by its owner, never handed to anyone else.
//
//	spc = H(TAG_SPC, owner_pk, rho, rcm)   cm = H(TAG_STAKE, asset, amount, spc)
//	nf  = H(TAG_SNF, nk, rho, position)     asset = AssetID(stake denom)
//
// Every note is a stake proof's output, its amount hidden: the chain mints
// no stake note (ORCHARD_DESIGN.md 8.1). A note may carry a slash
// label (moves.go), hidden in its commitment like everything else.

// stakeNodeStore backs zk/merkle's tree with StakeTreeNodes.
type stakeNodeStore struct {
	ctx context.Context
	k   Keeper
}

var _ merkle.NodeStore = stakeNodeStore{}

func (s stakeNodeStore) Get(level int, index uint64) (fr.Element, bool, error) {
	bz, err := s.k.StakeTreeNodes.Get(s.ctx, collections.Join(uint32(level), index))
	if errors.Is(err, collections.ErrNotFound) {
		return fr.Element{}, false, nil
	} else if err != nil {
		return fr.Element{}, false, err
	}
	e, err := privacy.FieldFromBytes(bz)
	return e, err == nil, err
}

func (s stakeNodeStore) Set(level int, index uint64, v fr.Element) error {
	return s.k.StakeTreeNodes.Set(s.ctx, collections.Join(uint32(level), index), privacy.FieldBytes(v))
}

func (k Keeper) stakeTree(ctx context.Context) (*merkle.Tree, error) {
	size, err := k.StakeTreeSize.Get(ctx)
	if errors.Is(err, collections.ErrNotFound) {
		size = 0
	} else if err != nil {
		return nil, err
	}
	return merkle.New(stakeNodeStore{ctx: ctx, k: k}, size), nil
}

// StakeTreeState is the stake tree's leaf count and latest recorded root.
func (k Keeper) StakeTreeState(ctx context.Context) (uint64, []byte, error) {
	t, err := k.stakeTree(ctx)
	if err != nil {
		return 0, nil, err
	}
	root, err := k.StakeLatestRoot.Get(ctx)
	if err != nil && !errors.Is(err, collections.ErrNotFound) {
		return 0, nil, err
	}
	return t.Size(), root, nil
}

// StakeCommitment is the stake tree's leaf at position.
func (k Keeper) StakeCommitment(ctx context.Context, position uint64) ([]byte, error) {
	t, err := k.stakeTree(ctx)
	if err != nil {
		return nil, err
	}
	l, err := t.Leaf(position)
	if err != nil {
		return nil, err
	}
	return privacy.FieldBytes(l), nil
}

// checkStakeCapacity refuses before anything is written if n more notes would
// not fit.
func (k Keeper) checkStakeCapacity(ctx context.Context, n uint64) error {
	t, err := k.stakeTree(ctx)
	if err != nil {
		return err
	}
	if t.Size()+n > merkle.Capacity {
		return types.ErrStakeTree.Wrap("stake note tree full")
	}
	return nil
}

// appendStake appends cm, emitting the note with its ciphertext.
func (k Keeper) appendStake(ctx context.Context, cm []byte, attrs ...sdk.Attribute) (uint64, error) {
	leaf, err := privacy.FieldFromBytes(cm)
	if err != nil {
		return 0, types.ErrStakeTree.Wrap(err.Error())
	}
	t, err := k.stakeTree(ctx)
	if err != nil {
		return 0, err
	}
	pos, err := t.Append(leaf)
	if errors.Is(err, merkle.ErrFull) {
		return 0, types.ErrStakeTree.Wrap("stake note tree full")
	} else if err != nil {
		return 0, err
	}
	if err := k.StakeTreeSize.Set(ctx, t.Size()); err != nil {
		return 0, err
	}
	sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(sdk.NewEvent(types.EventTypeStakeNote, append([]sdk.Attribute{
		sdk.NewAttribute(types.AttributeKeyPosition, strconv.FormatUint(pos, 10)),
		sdk.NewAttribute(types.AttributeKeyCommitment, hex.EncodeToString(cm)),
	}, attrs...)...))
	return pos, nil
}

// checkStakeNullifiers refuses a spent nullifier.
func (k Keeper) checkStakeNullifiers(ctx context.Context, nfs [][]byte) error {
	for _, nf := range nfs {
		spent, err := k.StakeNullifiers.Has(ctx, nf)
		if err != nil {
			return err
		}
		if spent {
			return types.ErrStakeNullifierSpent.Wrapf("%X", nf)
		}
	}
	return nil
}

// applyStakeProof spends p's nullifiers and appends its outputs, returning
// their positions. Its checks (CheckPrivateAction) and proof (the ante) have
// passed.
func (k Keeper) applyStakeProof(ctx context.Context, p *types.StakeProof) ([]uint64, error) {
	nfs := p.SpentNullifiers()
	if err := k.checkStakeNullifiers(ctx, nfs); err != nil {
		return nil, err
	}
	for _, nf := range nfs {
		if err := k.insertStakeNullifier(ctx, nf); err != nil {
			return nil, err
		}
	}
	cms, cts := p.Outputs()
	out := make([]uint64, len(cms))
	for i, cm := range cms {
		pos, err := k.appendStake(ctx, cm, sdk.NewAttribute(types.AttributeKeyCiphertext, base64.StdEncoding.EncodeToString(cts[i])))
		if err != nil {
			return nil, err
		}
		out[i] = pos
	}
	return out, nil
}

// ---- roots ------------------------------------------------------------------

// recordStakeRoot records the stake tree's root at the end of a block that
// changed it, and prunes roots past the window (never the latest).
func (k Keeper) recordStakeRoot(ctx context.Context) error {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	t, err := k.stakeTree(ctx)
	if err != nil {
		return err
	}
	// The empty tree's root too: a first delegation pads its input with its
	// own nullifier, so it proves against an anchor before any note exists.
	r, err := t.Root()
	if err != nil {
		return err
	}
	root := privacy.FieldBytes(r)
	latest, err := k.StakeLatestRoot.Get(ctx)
	if err != nil && !errors.Is(err, collections.ErrNotFound) {
		return err
	}
	if !bytes.Equal(latest, root) {
		rec := types.StakeRoot{Root: root, Height: sdkCtx.BlockHeight(), Time: sdkCtx.BlockTime().Unix(), TreeSize: t.Size()}
		if err := k.putStakeRoot(ctx, rec, true); err != nil {
			return err
		}
	}
	params, err := k.Params.Get(ctx)
	if err != nil {
		return err
	}
	return k.pruneStakeRoots(ctx, params.StakeRootWindowSeconds, 100)
}

func (k Keeper) putStakeRoot(ctx context.Context, rec types.StakeRoot, latest bool) error {
	if err := k.StakeRoots.Set(ctx, rec.Root, rec); err != nil {
		return err
	}
	if err := k.StakeRootsByTime.Set(ctx, collections.Join(rec.Time, rec.Root)); err != nil {
		return err
	}
	if !latest {
		return nil
	}
	if err := k.StakeLatestRoot.Set(ctx, rec.Root); err != nil {
		return err
	}
	sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(sdk.NewEvent(types.EventTypeStakeRoot,
		sdk.NewAttribute(types.AttributeKeyRoot, hex.EncodeToString(rec.Root)),
		sdk.NewAttribute(types.AttributeKeyTreeSize, strconv.FormatUint(rec.TreeSize, 10)),
	))
	return nil
}

func (k Keeper) pruneStakeRoots(ctx context.Context, window uint64, limit int) error {
	now := sdk.UnwrapSDKContext(ctx).BlockTime().Unix()
	latest, err := k.StakeLatestRoot.Get(ctx)
	if err != nil && !errors.Is(err, collections.ErrNotFound) {
		return err
	}
	var doomed []collections.Pair[int64, []byte]
	err = k.StakeRootsByTime.Walk(ctx, nil, func(key collections.Pair[int64, []byte]) (bool, error) {
		if len(doomed) >= limit || key.K1()+int64(window) >= now {
			return true, nil
		}
		if !bytes.Equal(key.K2(), latest) {
			doomed = append(doomed, key)
		}
		return false, nil
	})
	if err != nil {
		return err
	}
	for _, key := range doomed {
		if err := k.StakeRootsByTime.Remove(ctx, key); err != nil {
			return err
		}
		if err := k.StakeRoots.Remove(ctx, key.K2()); err != nil {
			return err
		}
	}
	return nil
}

// checkStakeAnchor refuses a root that is not a stake anchor now: recorded
// and within the window, or the latest.
func (k Keeper) checkStakeAnchor(ctx context.Context, root []byte) error {
	rec, err := k.StakeRoots.Get(ctx, root)
	if errors.Is(err, collections.ErrNotFound) {
		return types.ErrStakeTree.Wrapf("unknown stake root %X", root)
	} else if err != nil {
		return err
	}
	latest, err := k.StakeLatestRoot.Get(ctx)
	if err != nil && !errors.Is(err, collections.ErrNotFound) {
		return err
	}
	if bytes.Equal(latest, root) {
		return nil
	}
	params, err := k.Params.Get(ctx)
	if err != nil {
		return err
	}
	if sdk.UnwrapSDKContext(ctx).BlockTime().Unix() > rec.Time+int64(params.StakeRootWindowSeconds) {
		return types.ErrStakeTree.Wrapf("stake root %X has left the window", root)
	}
	return nil
}

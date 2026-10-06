package keeper

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strconv"

	"cosmossdk.io/collections"
	errorsmod "cosmossdk.io/errors"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/x/shielded/types"
	"github.com/earth-network/earth/zk/merkle"
	"github.com/earth-network/earth/zk/privacy"
)

// kvNodeStore backs zk/merkle's tree with the TreeNodes collection.
type kvNodeStore struct {
	ctx context.Context
	k   Keeper
}

var _ merkle.NodeStore = kvNodeStore{}

func (s kvNodeStore) Get(level int, index uint64) (fr.Element, bool, error) {
	bz, err := s.k.TreeNodes.Get(s.ctx, collections.Join(uint32(level), index))
	if errors.Is(err, collections.ErrNotFound) {
		return fr.Element{}, false, nil
	} else if err != nil {
		return fr.Element{}, false, err
	}
	e, err := privacy.FieldFromBytes(bz)
	return e, err == nil, err
}

func (s kvNodeStore) Set(level int, index uint64, v fr.Element) error {
	return s.k.TreeNodes.Set(s.ctx, collections.Join(uint32(level), index), privacy.FieldBytes(v))
}

// tree opens the note tree at its stored size.
func (k Keeper) tree(ctx context.Context) (*merkle.Tree, error) {
	size, err := k.TreeSize.Get(ctx)
	if errors.Is(err, collections.ErrNotFound) {
		size = 0
	} else if err != nil {
		return nil, err
	}
	return merkle.New(kvNodeStore{ctx: ctx, k: k}, size), nil
}

// Size is the number of notes ever appended.
func (k Keeper) Size(ctx context.Context) (uint64, error) {
	t, err := k.tree(ctx)
	if err != nil {
		return 0, err
	}
	return t.Size(), nil
}

// CurrentRoot is the tree root including this block's appends. It becomes a
// valid anchor only once EndBlock records it.
func (k Keeper) CurrentRoot(ctx context.Context) ([]byte, error) {
	t, err := k.tree(ctx)
	if err != nil {
		return nil, err
	}
	r, err := t.Root()
	if err != nil {
		return nil, err
	}
	return privacy.FieldBytes(r), nil
}

// Commitment returns the leaf at position (tests).
func (k Keeper) Commitment(ctx context.Context, position uint64) ([]byte, error) {
	t, err := k.tree(ctx)
	if err != nil {
		return nil, err
	}
	l, err := t.Leaf(position)
	if err != nil {
		return nil, err
	}
	return privacy.FieldBytes(l), nil
}

// appendNote appends cm and emits the note event an indexer rebuilds the
// tree from. The caller has validated cm as a canonical field element.
func (k Keeper) appendNote(ctx context.Context, cm []byte, ciphertext []byte) (uint64, error) {
	leaf, err := privacy.FieldFromBytes(cm)
	if err != nil {
		return 0, errorsmod.Wrap(types.ErrInvalidNote, err.Error())
	}
	t, err := k.tree(ctx)
	if err != nil {
		return 0, err
	}
	pos, err := t.Append(leaf)
	if errors.Is(err, merkle.ErrFull) {
		return 0, types.ErrTreeFull
	} else if err != nil {
		return 0, err
	}
	if err := k.TreeSize.Set(ctx, t.Size()); err != nil {
		return 0, err
	}
	sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(sdk.NewEvent(types.EventTypeNote,
		sdk.NewAttribute(types.AttributeKeyPosition, strconv.FormatUint(pos, 10)),
		sdk.NewAttribute(types.AttributeKeyCommitment, hex.EncodeToString(cm)),
		sdk.NewAttribute(types.AttributeKeyCiphertext, base64.StdEncoding.EncodeToString(ciphertext)),
	))
	return pos, nil
}

// checkCapacity refuses before anything is written if n more notes would not
// fit, so a private tx never spends its inputs and then fails to append.
func (k Keeper) checkCapacity(ctx context.Context, n uint64) error {
	size, err := k.Size(ctx)
	if err != nil {
		return err
	}
	if size+n > merkle.Capacity {
		return types.ErrTreeFull
	}
	return nil
}

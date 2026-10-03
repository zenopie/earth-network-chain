package ultrahonk

import (
	"crypto/sha256"
	"encoding/binary"
	"sync"
)

// VerifiedCache remembers (vk, proof, public inputs) triples that verified,
// for CheckTx only. A proof that verified once verifies again, so a mempool
// can skip re-verifying it; what this stops is a tx reusing valid proofs
// (from a tx that never landed, so its nullifiers are unspent) next to one
// junk proof, which would otherwise cost a node every valid proof again on
// each attempt before reaching the junk one.
//
// Never consult it in DeliverTx: a block's verification must not depend on
// what one node happened to see in its mempool (the answer would be the same,
// but the work, and so the time, would not).
//
// Bounded, first-in first-out. Entries are only added for proofs that
// verified, so filling it costs real proving.
type VerifiedCache struct {
	mu    sync.Mutex
	seen  map[[32]byte]struct{}
	order [][32]byte
	next  int
	runs  uint64 // verifications actually run (cache misses)
}

// NewVerifiedCache returns a cache holding up to size entries.
func NewVerifiedCache(size int) *VerifiedCache {
	if size < 1 {
		size = 1
	}
	return &VerifiedCache{seen: make(map[[32]byte]struct{}, size), order: make([][32]byte, 0, size)}
}

func cacheKey(vk, proof []byte, publicInputs [][]byte) [32]byte {
	h := sha256.New()
	var n [8]byte
	put := func(b []byte) {
		binary.BigEndian.PutUint64(n[:], uint64(len(b)))
		h.Write(n[:])
		h.Write(b)
	}
	put(vk)
	put(proof)
	binary.BigEndian.PutUint64(n[:], uint64(len(publicInputs)))
	h.Write(n[:])
	for _, in := range publicInputs {
		put(in)
	}
	var k [32]byte
	copy(k[:], h.Sum(nil))
	return k
}

// Verify answers from the cache when it holds this triple, and otherwise
// runs verify, remembering the triple if it verified.
func (c *VerifiedCache) Verify(verify func(vk, proof []byte, publicInputs [][]byte) (bool, error),
	vk, proof []byte, publicInputs [][]byte,
) (bool, error) {
	key := cacheKey(vk, proof, publicInputs)
	c.mu.Lock()
	_, hit := c.seen[key]
	c.mu.Unlock()
	if hit {
		return true, nil
	}
	c.mu.Lock()
	c.runs++
	c.mu.Unlock()
	ok, err := verify(vk, proof, publicInputs)
	if ok && err == nil {
		c.add(key)
	}
	return ok, err
}

func (c *VerifiedCache) add(key [32]byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, dup := c.seen[key]; dup {
		return
	}
	if len(c.order) < cap(c.order) {
		c.order = append(c.order, key)
	} else {
		delete(c.seen, c.order[c.next])
		c.order[c.next] = key
		c.next = (c.next + 1) % len(c.order)
	}
	c.seen[key] = struct{}{}
}

// Runs reports how many verifications the cache has run (its misses): what
// CheckTx has spent on proofs, for metrics and tests.
func (c *VerifiedCache) Runs() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.runs
}

// Len reports how many triples the cache holds.
func (c *VerifiedCache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.seen)
}

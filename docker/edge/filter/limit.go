package filter

import (
	"context"
	"time"
)

// A class is a bound on concurrent upstream work of one kind. Each served
// request holds one slot of its class while the node works on it; a request
// that cannot get a slot within the class's wait is answered 503 at once.
// Classes are what keep an allowed-but-expensive call (a simulate, which
// verifies a zero-knowledge proof; a raw store read; a tx search) from
// taking the signer's CPU in bulk: however many arrive, at most cap of them
// reach the node at a time.
//
// There is no per-client state here at all. Per-address limits are
// Cloudflare's (akash/README.md in the deploy repo); this process never sees
// a client's address except as a header it drops.
type class struct {
	name    string
	sem     chan struct{}
	wait    time.Duration // how long to queue for a slot
	timeout time.Duration // upstream deadline, response body included
}

func newClass(name string, capacity int, wait, timeout time.Duration) *class {
	return &class{name: name, sem: make(chan struct{}, capacity), wait: wait, timeout: timeout}
}

func (c *class) acquire(ctx context.Context) bool {
	select {
	case c.sem <- struct{}{}:
		return true
	default:
	}
	t := time.NewTimer(c.wait)
	defer t.Stop()
	select {
	case c.sem <- struct{}{}:
		return true
	case <-t.C:
		return false
	case <-ctx.Done():
		return false
	}
}

func (c *class) release() { <-c.sem }

// The classes. Capacities are per process (one proxy per lease) and sum to
// well under the node's own connection caps (EARTHD_RPC_MAX_OPEN_CONNECTIONS
// 100, EARTHD_API_MAX_OPEN_CONNECTIONS 200), so the node never refuses a
// connection the proxy made and the proxy's 503 is the only overload answer.
type Classes struct {
	light     *class // point reads: status, a block, a commit, a tx by hash
	results   *class // block_results and block ranges: point reads, larger bodies
	query     *class // gRPC queries (abci_query, LCD GETs): metered by query gas
	store     *class // raw /store/ reads: not metered, so few at a time
	broadcast *class // CheckTx: a private tx verifies its proofs here
	simulate  *class // runs the whole tx, proofs included
	search    *class // the LCD tx search: an index scan, unmetered
}

func DefaultClasses() *Classes {
	w := 2 * time.Second
	return &Classes{
		light:     newClass("light", 24, w, 15*time.Second),
		results:   newClass("results", 8, w, 30*time.Second),
		query:     newClass("query", 12, w, 20*time.Second),
		store:     newClass("store", 3, w, 20*time.Second),
		broadcast: newClass("broadcast", 4, 5*time.Second, 20*time.Second),
		simulate:  newClass("simulate", 2, 5*time.Second, 30*time.Second),
		search:    newClass("search", 1, w, 10*time.Second),
	}
}

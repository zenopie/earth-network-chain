package filter

// What rpc.erth.network serves, and to whom (the client inventory is in the
// deploy repo's akash/README.md, "What the clients call"):
//
//   wallets, web app, backend indexer   GET status, blockchain, block,
//                                       block_results, genesis_chunked,
//                                       abci_query (gRPC Query paths)
//   earthd --node (docs, runbook)       JSON-RPC POST: status, block, tx,
//                                       abci_query (gRPC Query paths, Simulate),
//                                       broadcast_tx_sync
//   earthd gas-check (backend)          JSON-RPC POST: status, block,
//                                       abci_query /store/<m>/key (prove) and
//                                       narrow /store/<m>/subspace reads
//   state sync light client             JSON-RPC POST: commit, validators,
//                                       consensus_params
//
// Refused: tx_search and block_search (unmetered kv-index scans; nothing of
// ours needs them: wallets look a tx up by hash, the explorer searches
// through the LCD under its own checks), /websocket, subscriptions,
// broadcast_tx_commit, the mempool and consensus dumps, net_info, genesis
// (genesis_chunked is the same data in pieces), check_tx,
// broadcast_evidence, the unsafe routes, and any route a future CometBFT
// adds.

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
)

var rpcRoutes = map[string]rpcRoute{
	"health":             {},
	"status":             {},
	"abci_info":          {},
	"genesis_chunked":    {args: []rpcArg{{"chunk", KUint}}},
	"block":              {args: []rpcArg{{"height", KInt64Ptr}}},
	"block_results":      {args: []rpcArg{{"height", KInt64Ptr}}},
	"commit":             {args: []rpcArg{{"height", KInt64Ptr}}},
	"consensus_params":   {args: []rpcArg{{"height", KInt64Ptr}}},
	"validators":         {args: []rpcArg{{"height", KInt64Ptr}, {"page", KIntPtr}, {"per_page", KIntPtr}}},
	"blockchain":         {args: []rpcArg{{"minHeight", KInt64}, {"maxHeight", KInt64}}},
	"tx":                 {args: []rpcArg{{"hash", KBytes}, {"prove", KBool}}},
	"abci_query":         {args: []rpcArg{{"path", KString}, {"data", KHexBytes}, {"height", KInt64}, {"prove", KBool}}},
	"broadcast_tx_sync":  {args: []rpcArg{{"tx", KBytes}}},
	"broadcast_tx_async": {args: []rpcArg{{"tx", KBytes}}},
}

var errRefused = errors.New("refused")

// checkRPC decides whether a decoded call is served, and in which class.
func checkRPC(c *Classes, call rpcCall) (*class, error) {
	a := call.args
	switch call.method {
	case "health", "status", "abci_info", "genesis_chunked", "block", "commit", "consensus_params":
		return c.light, nil
	case "block_results", "blockchain":
		return c.results, nil
	case "validators":
		if v := a["per_page"]; v.set && (v.i < 1 || v.i > 100) {
			return nil, fmt.Errorf("%w: per_page must be 1..100", errRefused)
		}
		if v := a["page"]; v.set && (v.i < 1 || v.i > 1000) {
			return nil, fmt.Errorf("%w: page must be 1..1000", errRefused)
		}
		return c.light, nil
	case "tx":
		if len(a["hash"].b) != 32 {
			return nil, fmt.Errorf("%w: hash must be 32 bytes", errRefused)
		}
		return c.light, nil
	case "broadcast_tx_sync", "broadcast_tx_async":
		if len(a["tx"].b) == 0 {
			return nil, fmt.Errorf("%w: empty tx", errRefused)
		}
		return c.broadcast, nil
	case "abci_query":
		return checkABCIQuery(c, a["path"].s, a["data"].b, a["prove"].t)
	}
	return nil, fmt.Errorf("%w: method not served", errRefused)
}

// abciGRPCExtra: gRPC paths served over abci_query beyond the LCD routes'
// own methods (lcdpolicy.go, grpcMethods), for `earthd tx` and the runbook.
var abciGRPCExtra = map[string]func(*Classes) *class{
	// earthd tx: the signer's account number and sequence.
	"/cosmos.auth.v1beta1.Query/Account": func(c *Classes) *class { return c.query },
	// earthd tx --gas auto. Runs the whole tx, proofs included.
	"/cosmos.tx.v1beta1.Service/Simulate": func(c *Classes) *class { return c.simulate },
	// The trust-store runbook (DSC revocation).
	"/cosmos.auth.v1beta1.Query/ModuleAccountByName": func(c *Classes) *class { return c.query },
	"/cosmos.gov.v1.Query/Proposal":                  func(c *Classes) *class { return c.query },
	"/earth.personhood.v1.Query/Registration":        func(c *Classes) *class { return c.query },
	"/earth.personhood.v1.Query/RegistrationsByDsc":  func(c *Classes) *class { return c.query },
	"/earth.assembly.v1.Query/ProposalTally":         func(c *Classes) *class { return c.query },
	// The web app's issuance figure (explorer, SupplyOf at height 1).
	"/cosmos.bank.v1beta1.Query/SupplyOf": func(c *Classes) *class { return c.query },
	// The backend indexer and tree verifier, at a height.
	"/earth.shielded.v1.Query/Tree":                      func(c *Classes) *class { return c.query },
	"/earth.personhood.v1.Query/IdentityTree":            func(c *Classes) *class { return c.query },
	"/earth.personhood.v1.Query/Handles":                 func(c *Classes) *class { return c.query },
	"/earth.shieldedstaking.v1.Query/StakeTree":          func(c *Classes) *class { return c.query },
	"/earth.shieldedstaking.v1.Query/StakeNullifierTree": func(c *Classes) *class { return c.query },
	"/earth.shieldedstaking.v1.Query/DebtTree":           func(c *Classes) *class { return c.query },
}

// Raw store reads. These do not run under the query-gas meter, so each is
// pinned to what earthd gas-check and the backend's known-DSC refresh read:
// point reads (with a proof when the value is empty) in the three stores
// gas-check opens, and prefix reads that stay inside one small set.
var storeKeyModules = map[string]bool{"personhood": true, "pki": true, "shielded": true}

// subspaceRule: a prefix read in store is served when the prefix starts
// with collection and is at least min bytes long. gas-check iterates CSCA
// index ranges (x/pki keeper issuerCandidates: csca_by_ski + AKI, csca_by_dn
// + sha256(DN); the prefix it sends is the range's common prefix, i.e. the
// collection prefix plus almost all of the key); the backend reads the whole
// regs_by_dsc collection (one entry per Document Signer with live
// registrations).
type subspaceRule struct {
	store, collection string
	min               int
}

var subspaceRules = []subspaceRule{
	{"pki", "csca_by_ski", len("csca_by_ski") + 4},
	{"pki", "csca_by_dn", len("csca_by_dn") + 4},
	{"personhood", "regs_by_dsc", len("regs_by_dsc")},
}

const maxStoreKey = 512

func checkABCIQuery(c *Classes, path string, data []byte, prove bool) (*class, error) {
	// The node routes a path that is a registered gRPC method to it, and
	// otherwise splits on "/" (baseapp Query). Matching the decoded string
	// exactly against fixed strings leaves no second reading.
	if cl, ok := grpcAllowed(c, path); ok {
		if prove {
			return nil, fmt.Errorf("%w: prove is not served on gRPC paths", errRefused)
		}
		return cl, nil
	}
	if !strings.HasPrefix(path, "/store/") {
		return nil, fmt.Errorf("%w: abci_query path not served", errRefused)
	}
	parts := strings.Split(path, "/") // "", "store", module, kind
	if len(parts) != 4 {
		return nil, fmt.Errorf("%w: abci_query path not served", errRefused)
	}
	module, kind := parts[2], parts[3]
	switch kind {
	case "key":
		if !storeKeyModules[module] {
			return nil, fmt.Errorf("%w: store not served", errRefused)
		}
		if len(data) == 0 || len(data) > maxStoreKey {
			return nil, fmt.Errorf("%w: key must be 1..%d bytes", errRefused, maxStoreKey)
		}
		return c.store, nil
	case "subspace":
		if prove {
			return nil, fmt.Errorf("%w: prove is not served on subspace reads", errRefused)
		}
		for _, r := range subspaceRules {
			if r.store == module && bytes.HasPrefix(data, []byte(r.collection)) &&
				len(data) >= r.min && len(data) <= maxStoreKey {
				return c.store, nil
			}
		}
		return nil, fmt.Errorf("%w: subspace prefix not served", errRefused)
	}
	return nil, fmt.Errorf("%w: abci_query path not served", errRefused)
}

func grpcAllowed(c *Classes, path string) (*class, bool) {
	if f, ok := abciGRPCExtra[path]; ok {
		return f(c), true
	}
	if grpcMethods[path] {
		return c.query, true
	}
	return nil, false
}

// Package types defines the shielded pool: its state layout, params, msgs and
// the rules a private msg must satisfy before any state is read.
package types

import (
	"cosmossdk.io/collections"
)

const (
	// ModuleName defines the module name. The pool is this module's account.
	ModuleName = "shielded"

	// StoreKey defines the primary module store key.
	StoreKey = ModuleName

	// GovModuleName duplicates the gov module's name to avoid a dependency with x/gov.
	GovModuleName = "gov"

	// FeeDenom is the only denom a private fee is paid in: a bundle's uerth
	// balance pays it.
	FeeDenom = "uerth"

	// AnmlDenom exists only inside the pool (and a few module accounts).
	AnmlDenom = "uanml"

	// CircuitAction and CircuitMembership name the verifying keys in Params.
	CircuitAction     = "action"
	CircuitMembership = "membership"
	// CircuitStake is x/shieldedstaking's owner-locked stake note circuit.
	CircuitStake = "stake"
	// CircuitVote is x/shieldedstaking's stake vote circuit: a stake note
	// votes on a proposal without being spent.
	CircuitVote = "vote"

	// MinActionsPerBundle is the padding rule: every bundle carries at least
	// two actions, so a one-note spend (the commonest shape) is not told
	// apart by its action count. Wallets pad with dummy actions.
	MinActionsPerBundle = 2

	// MaxBundlesPerMsg bounds how many bundles one private msg spends.
	MaxBundlesPerMsg = 2

	// ProofBytes is the exact length of every proof the chain verifies
	// (zk/ultrahonk.ProofSize): bb v5.0.0 UltraHonk ZK-flavor proofs are
	// padded to a constant 458 field elements whatever the circuit (action,
	// stake, membership, passport). bb's verifier ignores trailing bytes, so
	// anything longer would verify as the same proof under another tx hash;
	// anything else is refused before it is parsed.
	ProofBytes = 14_656

	// MaxCiphertextBytes bounds one note ciphertext. A note plaintext (asset,
	// value, rho, rcm, memo) plus an ephemeral key and tag fits in far less;
	// the bound caps what one private tx can make every node store in events
	// and hash into its sighash.
	MaxCiphertextBytes = 1024

	// CheckTxProofCacheSize is how many verified proofs a node remembers for
	// CheckTx (ultrahonk.VerifiedCache): a few blocks' worth of actions.
	CheckTxProofCacheSize = 8192

	// RootPruneLimit caps how many expired roots EndBlock deletes per block.
	// One root is recorded per block at most, so any cap >= 1 keeps up; the
	// slack drains a backlog (after a params change shortening the window).
	RootPruneLimit = 20
)

// Storage prefixes.
var (
	ParamsKey = collections.NewPrefix(0)

	// TreeNodesKey holds the note tree's non-empty nodes, (level, index) ->
	// 32-byte field element. Level 0 is leaves, level 32 the root.
	TreeNodesKey = collections.NewPrefix(1)
	// TreeSizeKey is the append cursor: the number of notes ever appended.
	TreeSizeKey = collections.NewPrefix(2)

	// RootsKey maps a recorded root to its record: O(1) anchor lookup.
	RootsKey = collections.NewPrefix(3)
	// RootsByTimeKey orders recorded roots by (time, root) for pruning. Time
	// rather than height, so a chain restarted from an export at a lower
	// height cannot collide with the imported records.
	RootsByTimeKey = collections.NewPrefix(4)
	// LatestRootKey is the newest recorded root, valid regardless of age.
	LatestRootKey = collections.NewPrefix(5)

	// NullifiersKey is the spent set.
	NullifiersKey = collections.NewPrefix(6)

	// AssetsKey maps denom -> asset id; AssetsByIDKey the reverse.
	AssetsKey     = collections.NewPrefix(7)
	AssetsByIDKey = collections.NewPrefix(8)

	// TurnstilesKey maps denom -> Turnstile.
	TurnstilesKey = collections.NewPrefix(9)
	// DirtyDenomsKey is the set of denoms whose turnstile moved this block;
	// EndBlock checks exactly those against the bank and clears the set.
	DirtyDenomsKey = collections.NewPrefix(10)

	// PrivateActionCountKey counts the actions the private txs of the current
	// block carried. Removed at EndBlock.
	PrivateActionCountKey = collections.NewPrefix(11)
)

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

	// FeeDenom is the only denom a private fee is paid in. The transfer
	// circuit's fee slot is hard-wired to ASSET_ERTH = AssetID("uerth").
	FeeDenom = "uerth"

	// AnmlDenom exists only inside the pool (and a few module accounts).
	AnmlDenom = "uanml"

	// CircuitTransfer and CircuitMembership name the verifying keys in Params.
	CircuitTransfer   = "transfer"
	CircuitMembership = "membership"
	// CircuitNoteVote is the stake-vote circuit (a note at a given root,
	// its vote and spend nullifiers); read by x/shieldedstaking.
	CircuitNoteVote = "note_vote"

	// TransferArity is the transfer circuit's input and output count.
	TransferArity = 3

	// TransferPublicInputs is the transfer circuit's public input count:
	// root, nf[3], cm_out[3], fee, v_pub_out, asset_pub, signal.
	TransferPublicInputs = 11

	// MaxProofBytes bounds a proof's size before anything parses it. bb v5.0.0
	// UltraHonk proofs are 14,656 bytes whatever the circuit.
	MaxProofBytes = 32 * 1024

	// MaxCiphertextBytes bounds one note ciphertext. A note plaintext (asset,
	// value, rho, rcm, memo) plus an ephemeral key and tag fits in far less;
	// the bound caps what one private tx can make every node store in events
	// and hash into its signal.
	MaxCiphertextBytes = 1024

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

	// PrivateTxCountKey counts private txs executed in the current block.
	// Removed at EndBlock.
	PrivateTxCountKey = collections.NewPrefix(11)
)

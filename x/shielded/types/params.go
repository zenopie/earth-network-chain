package types

import (
	"errors"
	"fmt"

	"cosmossdk.io/math"

	"github.com/earth-network/earth/zk/orchard"
)

const (
	// DefaultMinFee is the consensus fee floor for a private tx, in uerth.
	DefaultMinFee int64 = 1000

	// DefaultProofVerificationGas prices one action-proof verification.
	//
	// Same method as x/personhood's DefaultProofVerificationGas: the proof, not
	// the fee, is what could make a block slow, so the charge is set from the
	// block gas limit. An action proof verifies in ~4.3 ms on an M-class core,
	// several times that on a fractional Akash CPU; 2,000,000 against the
	// 100,000,000 block limit admits at most 50 verifications a block, and
	// max_private_actions_per_block caps it lower still.
	DefaultProofVerificationGas uint64 = 2_000_000

	// DefaultNoteGas prices one note write (a commitment appended, with its 33
	// tree-node writes, or a nullifier spent). Measured metered cost of an
	// append is ~150k; the pool's writes run unmetered after this fixed charge.
	DefaultNoteGas uint64 = 150_000

	// DefaultBundleGas prices a bundle's own checks: its digest in the
	// sighash, the binding key (a point sum plus one scalar multiplication per
	// balance) and the binding signature (0.12 ms on an M-class core).
	DefaultBundleGas uint64 = 100_000

	// DefaultRootWindowSeconds is two weeks: long enough for a stake vote to
	// use the root at proposal start, and for a wallet that was offline for a
	// few days to still prove against the root it last synced.
	DefaultRootWindowSeconds uint64 = 14 * 24 * 60 * 60

	// DefaultMaxPrivateActionsPerBlock caps the proofs of a block's private
	// txs: 32 action proofs are ~74M gas with their notes, leaving a quarter
	// of the block for everything else (the same proof budget as the 32
	// one-proof transfers the cap allowed before bundles).
	DefaultMaxPrivateActionsPerBlock uint32 = 32

	// DefaultMaxActionsPerBundle bounds one bundle: a send of up to 15 notes
	// plus change, and an ordinary block's worth of proofs at most.
	DefaultMaxActionsPerBundle uint32 = 16
)

// NewParams creates a new Params instance.
func NewParams(vks map[string][]byte, minFee math.Int, proofGas, noteGas, bundleGas, window uint64, maxActionsBlock, maxActionsBundle uint32) Params {
	return Params{
		VerifyingKeys:             vks,
		MinFee:                    minFee,
		ProofVerificationGas:      proofGas,
		NoteGas:                   noteGas,
		BundleGas:                 bundleGas,
		RootWindowSeconds:         window,
		MaxPrivateActionsPerBlock: maxActionsBlock,
		MaxActionsPerBundle:       maxActionsBundle,
	}
}

// DefaultParams returns a default set of parameters. No verifying keys: until
// governance (or genesis) sets them, every private msg is refused.
func DefaultParams() Params {
	return NewParams(nil, math.NewInt(DefaultMinFee), DefaultProofVerificationGas, DefaultNoteGas, DefaultBundleGas,
		DefaultRootWindowSeconds, DefaultMaxPrivateActionsPerBlock, DefaultMaxActionsPerBundle)
}

// knownCircuits are the verifying-key names this module reads.
var knownCircuits = map[string]bool{CircuitAction: true, CircuitMembership: true}

// Validate validates the set of params. Every field fails closed: a zero here
// would admit a zero-fee tx, unpriced proofs, no usable anchors, or no
// private txs at all, and none of those is a configuration anyone means.
func (p Params) Validate() error {
	if p.MinFee.IsNil() || p.MinFee.LT(math.OneInt()) {
		return errors.New("min_fee must be at least 1")
	}
	if p.ProofVerificationGas == 0 {
		return errors.New("proof_verification_gas must be positive")
	}
	if p.NoteGas == 0 {
		return errors.New("note_gas must be positive")
	}
	if p.BundleGas == 0 {
		return errors.New("bundle_gas must be positive")
	}
	if p.RootWindowSeconds == 0 {
		return errors.New("root_window_seconds must be positive")
	}
	if p.RootWindowSeconds > 365*24*60*60 {
		return errors.New("root_window_seconds must be at most a year")
	}
	if p.MaxActionsPerBundle < MinActionsPerBundle || p.MaxActionsPerBundle > orchard.MaxActions {
		return fmt.Errorf("max_actions_per_bundle must be %d..%d", MinActionsPerBundle, orchard.MaxActions)
	}
	if p.MaxPrivateActionsPerBlock < MaxBundlesPerMsg*p.MaxActionsPerBundle {
		// Below this, a msg of the maximum size could never be included.
		return fmt.Errorf("max_private_actions_per_block must be at least %d x max_actions_per_bundle", MaxBundlesPerMsg)
	}
	for name, vk := range p.VerifyingKeys {
		if !knownCircuits[name] {
			return fmt.Errorf("unknown circuit %q in verifying_keys", name)
		}
		if len(vk) == 0 {
			return fmt.Errorf("verifying key %q is empty", name)
		}
	}
	return nil
}

// ActionGas is the fixed gas of one action: its proof, its nullifier and its
// commitment.
func (p Params) ActionGas() uint64 {
	return p.ProofVerificationGas + 2*p.NoteGas
}

// PrivateMsgGas is the fixed gas a private msg's bundles cost before any of
// their work: bundle_gas per bundle plus ActionGas per action. Charged in
// full before anything is checked against state, so a private tx's gas is a
// function of its shape alone.
func (p Params) PrivateMsgGas(bundles []*Bundle) uint64 {
	var g uint64
	for _, b := range bundles {
		g += p.BundleGas + uint64(len(b.Actions))*p.ActionGas()
	}
	return g
}

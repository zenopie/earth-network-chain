package types

import (
	"errors"
	"fmt"

	"cosmossdk.io/math"
)

const (
	// DefaultMinFee is the consensus fee floor for a private tx, in uerth.
	DefaultMinFee int64 = 1000

	// DefaultProofVerificationGas prices one transfer-proof verification.
	//
	// Same method as x/personhood's DefaultProofVerificationGas: the proof, not
	// the fee, is what could make a block slow, so the charge is set from the
	// block gas limit. A transfer proof verifies in ~4 ms on an M-class core,
	// several times that on a fractional Akash CPU; 2,000,000 against the
	// 100,000,000 block limit admits at most 50 verifications a block, and
	// max_private_txs_per_block caps it lower still.
	DefaultProofVerificationGas uint64 = 2_000_000

	// DefaultNoteGas prices one note write (a commitment appended, with its 33
	// tree-node writes, or a nullifier spent). Measured metered cost of an
	// append is ~150k; the pool's writes run unmetered after this fixed charge.
	DefaultNoteGas uint64 = 150_000

	// DefaultRootWindowSeconds is two weeks: long enough for a stake vote to
	// use the root at proposal start, and for a wallet that was offline for a
	// few days to still prove against the root it last synced.
	DefaultRootWindowSeconds uint64 = 14 * 24 * 60 * 60

	// DefaultMaxPrivateTxsPerBlock caps private txs per block: 32 proofs is
	// ~64M gas, leaving a third of the block for everything else.
	DefaultMaxPrivateTxsPerBlock uint32 = 32
)

// NewParams creates a new Params instance.
func NewParams(vks map[string][]byte, minFee math.Int, proofGas, noteGas, window uint64, maxPrivate uint32) Params {
	return Params{
		VerifyingKeys:         vks,
		MinFee:                minFee,
		ProofVerificationGas:  proofGas,
		NoteGas:               noteGas,
		RootWindowSeconds:     window,
		MaxPrivateTxsPerBlock: maxPrivate,
	}
}

// DefaultParams returns a default set of parameters. No verifying keys: until
// governance (or genesis) sets them, every private msg is refused.
func DefaultParams() Params {
	return NewParams(nil, math.NewInt(DefaultMinFee), DefaultProofVerificationGas, DefaultNoteGas,
		DefaultRootWindowSeconds, DefaultMaxPrivateTxsPerBlock)
}

// knownCircuits are the verifying-key names this module reads.
var knownCircuits = map[string]bool{CircuitTransfer: true, CircuitMembership: true}

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
	if p.RootWindowSeconds == 0 {
		return errors.New("root_window_seconds must be positive")
	}
	if p.RootWindowSeconds > 365*24*60*60 {
		return errors.New("root_window_seconds must be at most a year")
	}
	if p.MaxPrivateTxsPerBlock == 0 {
		return errors.New("max_private_txs_per_block must be positive")
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

// PrivateMsgGas is the fixed gas a private msg carrying one transfer pays
// before any of its work: the proof, three nullifiers and three commitments.
func (p Params) PrivateMsgGas() uint64 {
	return p.ProofVerificationGas + 2*TransferArity*p.NoteGas
}

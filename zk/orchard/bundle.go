package orchard

import (
	"errors"
	"fmt"
	"runtime"
	"sync"

	"github.com/consensys/gnark-crypto/ecc/bn254/fr"

	"github.com/earth-network/earth/zk/privacy"
)

// MaxActions bounds a bundle. It is part of the soundness argument, not only
// a resource limit: the circuits bound every note value to 2^63-1, so per
// base |sum of action values - public balance| <= MaxActions * 2 * (2^63-1)
// + (2^64-1) < 2^70 << n, and a sum that vanishes mod n vanishes over the
// integers (ORCHARD_DESIGN.md 2.6). x/shielded's max_actions_per_bundle
// may only be lower.
const MaxActions = 32

// ActionPublicInputs is the action circuit's public input count:
// anchor, nf, cm_out, cv_x, cv_y, sighash.
const ActionPublicInputs = 6

// Action is one spend and one output, either of which may be a dummy, with
// the note-tree root its spend is proven against.
type Action struct {
	// Anchor is the note-tree root the spend's membership is proven under.
	// A dummy spend's path is not checked, so the circuit leaves it free; the
	// chain still requires a valid anchor for every action, so dummies look
	// like real spends.
	Anchor     fr.Element
	Nullifier  fr.Element
	Commitment fr.Element // the output note's cm
	Cv         Point
	Ciphertext []byte // the output note, encrypted; bound by the sighash
	Proof      []byte
}

// Balance is value of one asset leaving the pool: the bundle's spends of it
// exceed its outputs by Value. The msg says where it goes (the fee, an
// unshield receiver, a module). Only positive balances exist: value enters
// the pool through MintNote/MsgShield, never through a bundle.
type Balance struct {
	Asset fr.Element
	Value uint64
}

// Bundle is a shielded bundle: N actions, the public value balance per
// asset, and the binding signature.
type Bundle struct {
	Actions    []Action
	Balances   []Balance
	BindingSig []byte
}

// Digest is what the sighash binds of a bundle: every field but the proofs
// and the binding signature (both are made over the sighash).
//
//	H(TAG_BUNDLE, N,
//	  anchor_0, nf_0, cm_0, cvx_0, cvy_0, Bytes(ct_0), ...,
//	  M, asset_0, value_0, ...)
//
// N and M are counts; the Poseidon2 sponge also absorbs the input length.
func (b *Bundle) Digest() fr.Element {
	in := make([]fr.Element, 0, 3+6*len(b.Actions)+2*len(b.Balances))
	in = append(in, TagBundle, privacy.U64(uint64(len(b.Actions))))
	for _, a := range b.Actions {
		x, y := XY(a.Cv)
		in = append(in, a.Anchor, a.Nullifier, a.Commitment, x, y, privacy.Bytes(a.Ciphertext))
	}
	in = append(in, privacy.U64(uint64(len(b.Balances))))
	for _, bal := range b.Balances {
		in = append(in, bal.Asset, privacy.U64(bal.Value))
	}
	return privacy.H(in...)
}

// TxFields are the fields of the enclosing tx (outside the msg) that every
// private msg's sighash binds. A private tx is unsigned, so whoever relays it
// could otherwise rewrite them: the memo (an exchange's deposit tag), the
// timeout height (remove the wallet's expiry) and the gas limit (lower it so
// the msg's handler runs out of gas after the ante has spent the notes).
type TxFields struct {
	// Memo is the tx body's memo.
	Memo string
	// TimeoutHeight is the tx body's timeout_height (0: none).
	TimeoutHeight uint64
	// GasLimit is the auth info's fee.gas_limit.
	GasLimit uint64
}

// Sighash is what every action proof of a msg binds as its public
// `sighash`, what any other proof in the msg (membership, passport) binds as
// its signal, and what each bundle's binding signature signs:
//
//	sighash = zk/privacy.Signal(msg_type_url, chain_id,
//	                            K, digest(bundle_0), ..., digest(bundle_K-1),
//	                            Bytes(memo), timeout_height, gas_limit,
//	                            msg fields...)
//	        = H(TAG_SIGNAL, Bytes(msg_type_url), Bytes(chain_id), K, D_0, ...,
//	            Bytes(memo), timeout_height, gas_limit, fields...)
//
// Bytes(memo) is over the memo's UTF-8 bytes; timeout_height and gas_limit
// are u64 field elements. The msg type URL fixes how many fields follow and
// what they mean.
func Sighash(msgType, chainID string, tx TxFields, bundles []*Bundle, fields ...fr.Element) fr.Element {
	f := make([]fr.Element, 0, 4+len(bundles)+len(fields))
	f = append(f, privacy.U64(uint64(len(bundles))))
	for _, b := range bundles {
		f = append(f, b.Digest())
	}
	f = append(f, privacy.Bytes([]byte(tx.Memo)), privacy.U64(tx.TimeoutHeight), privacy.U64(tx.GasLimit))
	return privacy.Signal(msgType, chainID, append(f, fields...)...)
}

// BaseFunc maps an asset id to its value base.
type BaseFunc func(asset fr.Element) (Point, error)

// BindingKey is bvk = sum cv_i - sum_a value_a * G_a. For an honest bundle
// every value term cancels and bvk = (sum rcv_i) * R.
func (b *Bundle) BindingKey(base BaseFunc) (Point, error) {
	var bvk Point // infinity
	for _, a := range b.Actions {
		bvk = Add(bvk, a.Cv)
	}
	for _, bal := range b.Balances {
		g, err := base(bal.Asset)
		if err != nil {
			return Point{}, err
		}
		bvk = Sub(bvk, Mul(g, ScalarU64(bal.Value)))
	}
	return bvk, nil
}

// ErrMalformedBundle covers every stateless refusal.
var ErrMalformedBundle = errors.New("orchard: malformed bundle")

// ValidateBasic is the stateless shape check: 1..MaxActions actions, every cv
// a curve point, nullifiers distinct, balances positive with distinct
// assets, a binding signature of the right length. Callers add their own
// policy (x/shielded: at least two actions, a param maximum).
func (b *Bundle) ValidateBasic() error {
	if len(b.Actions) == 0 || len(b.Actions) > MaxActions {
		return fmt.Errorf("%w: 1..%d actions", ErrMalformedBundle, MaxActions)
	}
	seen := make(map[fr.Element]bool, len(b.Actions))
	for i, a := range b.Actions {
		if !a.Cv.IsOnCurve() {
			return fmt.Errorf("%w: action %d cv", ErrMalformedBundle, i)
		}
		if seen[a.Nullifier] {
			return fmt.Errorf("%w: duplicate nullifier", ErrMalformedBundle)
		}
		seen[a.Nullifier] = true
	}
	assets := make(map[fr.Element]bool, len(b.Balances))
	for _, bal := range b.Balances {
		if bal.Value == 0 || assets[bal.Asset] {
			return fmt.Errorf("%w: balances must be positive, one per asset", ErrMalformedBundle)
		}
		assets[bal.Asset] = true
	}
	if len(b.BindingSig) != BindingSigSize {
		return fmt.Errorf("%w: binding signature must be %d bytes", ErrMalformedBundle, BindingSigSize)
	}
	return nil
}

// PublicInputs is action i's public inputs, in the circuit's order.
func (b *Bundle) PublicInputs(i int, sighash fr.Element) [][]byte {
	a := b.Actions[i]
	x, y := XY(a.Cv)
	return [][]byte{
		privacy.FieldBytes(a.Anchor),
		privacy.FieldBytes(a.Nullifier),
		privacy.FieldBytes(a.Commitment),
		privacy.FieldBytes(x),
		privacy.FieldBytes(y),
		privacy.FieldBytes(sighash),
	}
}

// ProofVerifier verifies one action proof (zk/ultrahonk.Verify with the
// action key). It must be safe for concurrent use.
type ProofVerifier func(proof []byte, publicInputs [][]byte) (bool, error)

// CheckBalance verifies the binding signature under the bvk the balances
// imply. Cheap (one point sum and two scalar multiplications), so callers run
// it before any proof.
func (b *Bundle) CheckBalance(sighash fr.Element, base BaseFunc) error {
	bvk, err := b.BindingKey(base)
	if err != nil {
		return err
	}
	return VerifyBinding(bvk, sighash, b.BindingSig)
}

// Verify is one bundle's whole stateless verification under sighash: shape,
// the binding signature over the value balance, then every action proof
// (VerifyProofs). The anchors' and the nullifiers' state checks are the
// caller's.
func (b *Bundle) Verify(sighash fr.Element, base BaseFunc, verify ProofVerifier) error {
	if err := b.ValidateBasic(); err != nil {
		return err
	}
	if err := b.CheckBalance(sighash, base); err != nil {
		return err
	}
	return VerifyProofs([]*Bundle{b}, sighash, verify)
}

// ActionError is the first action proof (in bundle order, then action
// order) that failed to verify.
type ActionError struct {
	Bundle, Action int
	Err            error // nil: the proof is well formed but invalid
}

func (e *ActionError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("bundle %d action %d: %v", e.Bundle, e.Action, e.Err)
	}
	return fmt.Sprintf("bundle %d action %d: invalid proof", e.Bundle, e.Action)
}

func (e *ActionError) Unwrap() error { return e.Err }

// VerifyProofs verifies every action proof of bundles under sighash, on up to
// GOMAXPROCS goroutines. The outcome is deterministic: every proof is
// verified, and the error returned is always the first failing action in
// order, whatever finished first. nil iff every proof verifies.
func VerifyProofs(bundles []*Bundle, sighash fr.Element, verify ProofVerifier) error {
	type job struct{ b, a int }
	var jobs []job
	for i, b := range bundles {
		for j := range b.Actions {
			jobs = append(jobs, job{i, j})
		}
	}
	errs := make([]*ActionError, len(jobs))
	workers := min(runtime.GOMAXPROCS(0), len(jobs))
	next := make(chan int)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for k := range next {
				jb := jobs[k]
				b := bundles[jb.b]
				ok, err := verifyRecovered(verify, b.Actions[jb.a].Proof, b.PublicInputs(jb.a, sighash))
				if err != nil || !ok {
					errs[k] = &ActionError{Bundle: jb.b, Action: jb.a, Err: err}
				}
			}
		}()
	}
	for k := range jobs {
		next <- k
	}
	close(next)
	wg.Wait()
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

// verifyRecovered runs verify, turning a panic into an error. In a worker
// goroutine a panic is not caught by baseapp's recovery (that only covers the
// goroutine running the tx): it would take the node down, on every node that
// verified the same proof, so a malformed proof that panicked the verifier
// would halt the chain. Recovered, it is that action's ActionError (audit 4,
// I3). A deterministic verifier panics deterministically, so the outcome is
// the same on every node.
func verifyRecovered(verify ProofVerifier, proof []byte, inputs [][]byte) (ok bool, err error) {
	defer func() {
		if r := recover(); r != nil {
			ok, err = false, fmt.Errorf("proof verifier panicked: %v", r)
		}
	}()
	return verify(proof, inputs)
}

// VerifyProofsSequential verifies the same proofs as VerifyProofs, one at a
// time in bundle and action order, and stops at the first that fails. For a
// mempool (CheckTx), where a tx whose first proof is junk must cost one
// verification, not one per action: the binding signature is no filter there,
// since anyone can sign a forged balance over unproven value commitments.
// Same result as VerifyProofs; only the work done on failure differs.
func VerifyProofsSequential(bundles []*Bundle, sighash fr.Element, verify ProofVerifier) error {
	for i, b := range bundles {
		for j := range b.Actions {
			ok, err := verifyRecovered(verify, b.Actions[j].Proof, b.PublicInputs(j, sighash))
			if err != nil || !ok {
				return &ActionError{Bundle: i, Action: j, Err: err}
			}
		}
	}
	return nil
}

// CanonicalBase is the BaseFunc that derives (and caches) ValueBase.
func CanonicalBase(asset fr.Element) (Point, error) { return ValueBase(asset), nil }

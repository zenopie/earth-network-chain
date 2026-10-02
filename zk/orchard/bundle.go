package orchard

import (
	"errors"
	"fmt"

	"github.com/consensys/gnark-crypto/ecc/bn254/fr"

	"github.com/earth-network/earth/zk/privacy"
)

// MaxActions bounds a bundle. It also keeps every per-asset sum far from
// wrapping the scalar field: MaxActions * 2^64 << n.
const MaxActions = 32

// ActionPublicInputs is the action circuit's public input count:
// anchor, nf, cm_out, cv_x, cv_y, sighash.
const ActionPublicInputs = 6

// Action is one spend and one output, either of which may be a dummy.
type Action struct {
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

// Bundle is a shielded bundle: N actions against one anchor, the public
// value balance per asset, and the binding signature.
type Bundle struct {
	Anchor     fr.Element
	Actions    []Action
	Balances   []Balance
	BindingSig []byte
}

// Digest is what the sighash binds of a bundle: everything but the proofs
// and the binding signature (which are made over it).
//
//	H(TAG_BUNDLE, anchor, N, nf_0, cm_0, cvx_0, cvy_0, Bytes(ct_0), ...,
//	  M, asset_0, value_0, ...)
func (b *Bundle) Digest() fr.Element {
	in := []fr.Element{TagBundle, b.Anchor, privacy.U64(uint64(len(b.Actions)))}
	for _, a := range b.Actions {
		x, y := XY(a.Cv)
		in = append(in, a.Nullifier, a.Commitment, x, y, privacy.Bytes(a.Ciphertext))
	}
	in = append(in, privacy.U64(uint64(len(b.Balances))))
	for _, bal := range b.Balances {
		in = append(in, bal.Asset, privacy.U64(bal.Value))
	}
	return privacy.H(in...)
}

// Sighash is what every action proof (and any other proof in the msg, a
// membership proof) binds as its public `sighash`/`signal`, and what each
// bundle's binding signature signs: zk/privacy.Signal over the msg type, the
// chain id, every bundle's digest in order, then the msg's own fields.
func Sighash(msgType, chainID string, bundles []*Bundle, extra ...fr.Element) fr.Element {
	f := make([]fr.Element, 0, len(bundles)+len(extra))
	for _, b := range bundles {
		f = append(f, b.Digest())
	}
	return privacy.Signal(msgType, chainID, append(f, extra...)...)
}

// BaseFunc maps an asset id to its value base. The keeper stores the base
// with the asset at registration; ValueBase recomputes it.
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

// ValidateBasic is the stateless shape check: 1..MaxActions actions, every
// cv a curve point, nullifiers distinct, balances positive with distinct
// assets.
func (b *Bundle) ValidateBasic() error {
	if len(b.Actions) == 0 || len(b.Actions) > MaxActions {
		return fmt.Errorf("%w: 1..%d actions", ErrMalformedBundle, MaxActions)
	}
	seen := map[fr.Element]bool{}
	for i, a := range b.Actions {
		if !a.Cv.IsOnCurve() {
			return fmt.Errorf("%w: action %d cv", ErrMalformedBundle, i)
		}
		if seen[a.Nullifier] {
			return fmt.Errorf("%w: duplicate nullifier", ErrMalformedBundle)
		}
		seen[a.Nullifier] = true
	}
	assets := map[fr.Element]bool{}
	for _, bal := range b.Balances {
		if bal.Value == 0 || assets[bal.Asset] {
			return fmt.Errorf("%w: balances must be positive, one per asset", ErrMalformedBundle)
		}
		assets[bal.Asset] = true
	}
	if len(b.BindingSig) != BindingSigSize {
		return fmt.Errorf("%w: binding signature", ErrMalformedBundle)
	}
	return nil
}

// PublicInputs is action i's public inputs, in the circuit's order.
func (b *Bundle) PublicInputs(i int, sighash fr.Element) [][]byte {
	a := b.Actions[i]
	x, y := XY(a.Cv)
	return [][]byte{
		privacy.FieldBytes(b.Anchor),
		privacy.FieldBytes(a.Nullifier),
		privacy.FieldBytes(a.Commitment),
		privacy.FieldBytes(x),
		privacy.FieldBytes(y),
		privacy.FieldBytes(sighash),
	}
}

// ProofVerifier verifies one action proof (zk/ultrahonk.Verify with the
// action key).
type ProofVerifier func(proof []byte, publicInputs [][]byte) (bool, error)

// Verify is the whole stateless verification of a bundle under sighash:
// shape, the binding signature over the value balance, then every action
// proof. The anchor's and the nullifiers' state checks are the keeper's.
func (b *Bundle) Verify(sighash fr.Element, base BaseFunc, verify ProofVerifier) error {
	if err := b.ValidateBasic(); err != nil {
		return err
	}
	if err := b.CheckBalance(sighash, base); err != nil {
		return err
	}
	for i := range b.Actions {
		ok, err := verify(b.Actions[i].Proof, b.PublicInputs(i, sighash))
		if err != nil {
			return fmt.Errorf("action %d: %w", i, err)
		}
		if !ok {
			return fmt.Errorf("action %d: invalid proof", i)
		}
	}
	return nil
}

// CheckBalance verifies the binding signature under the bvk the balances
// imply. Cheap (one MSM-sized sum and two scalar multiplications), so the
// ante runs it before any proof.
func (b *Bundle) CheckBalance(sighash fr.Element, base BaseFunc) error {
	bvk, err := b.BindingKey(base)
	if err != nil {
		return err
	}
	return VerifyBinding(bvk, sighash, b.BindingSig)
}

// CanonicalBase is the BaseFunc that recomputes ValueBase.
func CanonicalBase(asset fr.Element) (Point, error) { return ValueBase(asset), nil }

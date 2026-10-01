// Package testutil builds witnesses for the privacy circuits from chain
// state, and proves them (or replays committed proofs) for the tests that
// drive x/personhood and x/assembly on a real app.
//
// The app tests run a deterministic chain and, whenever they need a proof,
// ask a Prover for it by name. Normally the Prover reads the committed proof
// from testdata. With EARTH_PROVE_CIRCUITS pointing at the Noir circuits
// workspace (scripts/personhood-fixtures.sh sets it) it writes the witness,
// runs nargo and bb, checks the public inputs bb reports against the ones the
// chain will compute, and commits the new proof. A chain change that moves
// any tree, time or signal makes the committed proofs stop verifying: rerun
// the script.
package testutil

import (
	"fmt"
	"strings"

	"github.com/consensys/gnark-crypto/ecc/bn254/fr"

	shieldedtypes "github.com/earth-network/earth/x/shielded/types"
	"github.com/earth-network/earth/zk/merkle"
	"github.com/earth-network/earth/zk/privacy"
)

// Det is a deterministic pseudo-random field element.
func Det(label string, i uint64) fr.Element {
	return privacy.H(privacy.AssetID("personhood-fixture/"+label), privacy.U64(i))
}

func q(e fr.Element) string { b := e.Bytes(); return fmt.Sprintf("\"0x%x\"", b[:]) }

func arr(es []fr.Element) string {
	p := make([]string, len(es))
	for i, e := range es {
		p[i] = q(e)
	}
	return "[" + strings.Join(p, ", ") + "]"
}

// Membership is one membership proof's witness and public inputs.
type Membership struct {
	IDSecret      fr.Element
	DscKey        fr.Element
	ActivatedAt   uint64
	LeafIndex     uint64
	Root          fr.Element
	Siblings      [merkle.Depth]fr.Element
	Scope         fr.Element
	Signal        fr.Element
	ExcludedDsc   fr.Element
	MaxActivation uint64
}

// Nullifier is the proof's nullifier for its scope.
func (m Membership) Nullifier() fr.Element { return privacy.ScopeNullifier(m.IDSecret, m.Scope) }

// Witness is the Prover.toml and public inputs in ABI order.
func (m Membership) Witness() (string, []fr.Element) {
	var b strings.Builder
	fmt.Fprintf(&b, "id_secret = %s\ndsc_key = %s\nactivated_at = \"%d\"\nleaf_index = \"%d\"\n",
		q(m.IDSecret), q(m.DscKey), m.ActivatedAt, m.LeafIndex)
	fmt.Fprintf(&b, "siblings = %s\n", arr(m.Siblings[:]))
	nf := m.Nullifier()
	fmt.Fprintf(&b, "root = %s\nscope = %s\nnullifier = %s\nsignal = %s\nexcluded_dsc = %s\nmax_activation = \"%d\"\n",
		q(m.Root), q(m.Scope), q(nf), q(m.Signal), q(m.ExcludedDsc), m.MaxActivation)
	return b.String(), []fr.Element{m.Root, m.Scope, nf, m.Signal, m.ExcludedDsc, privacy.U64(m.MaxActivation)}
}

// Note is a note's opening.
type Note struct {
	NK       fr.Element
	Denom    string
	Value    uint64
	Rho, Rcm fr.Element
}

func (n Note) PC() fr.Element { return privacy.PC(privacy.OwnerPK(n.NK), n.Rho, n.Rcm) }
func (n Note) CM() fr.Element { return privacy.CM(privacy.AssetID(n.Denom), n.Value, n.PC()) }

// Fee is a transfer that pays fee out of one uerth note at Position and
// returns the change to Change: slots 0-1 are zero-value dummies (asset uerth),
// slot 2 spends the note.
type Fee struct {
	Note        Note
	Position    uint64
	Tree        *merkle.Tree
	Fee         uint64
	Change      Note // slot-2 output; its Value is Note.Value - Fee
	Ciphertexts [3][]byte
	// DummyRho seeds the two dummy inputs' and outputs' openings.
	DummyRho fr.Element
}

func (f Fee) inputs() [3]Note {
	d0 := Note{NK: f.Note.NK, Denom: "uerth", Rho: privacy.H(f.DummyRho, privacy.U64(0)), Rcm: privacy.H(f.DummyRho, privacy.U64(1))}
	d1 := Note{NK: f.Note.NK, Denom: "uerth", Rho: privacy.H(f.DummyRho, privacy.U64(2)), Rcm: privacy.H(f.DummyRho, privacy.U64(3))}
	return [3]Note{d0, d1, f.Note}
}

func (f Fee) positions() [3]uint64 { return [3]uint64{0, 0, f.Position} }

func (f Fee) outputs() [3]Note {
	o0 := Note{NK: f.Note.NK, Denom: "uerth", Rho: privacy.H(f.DummyRho, privacy.U64(4)), Rcm: privacy.H(f.DummyRho, privacy.U64(5))}
	o1 := Note{NK: f.Note.NK, Denom: "uerth", Rho: privacy.H(f.DummyRho, privacy.U64(6)), Rcm: privacy.H(f.DummyRho, privacy.U64(7))}
	return [3]Note{o0, o1, f.Change}
}

// Nullifiers of the three inputs.
func (f Fee) Nullifiers() [3]fr.Element {
	var nf [3]fr.Element
	in, pos := f.inputs(), f.positions()
	for i := range 3 {
		nf[i] = privacy.NF(f.Note.NK, in[i].Rho, uint32(pos[i]))
	}
	return nf
}

// Transfer is the msg's Transfer (without its proof).
func (f Fee) Transfer() (shieldedtypes.Transfer, error) {
	root, err := f.Tree.Root()
	if err != nil {
		return shieldedtypes.Transfer{}, err
	}
	t := shieldedtypes.Transfer{Root: privacy.FieldBytes(root), Fee: f.Fee}
	nf := f.Nullifiers()
	out := f.outputs()
	for i := range 3 {
		t.Nullifiers = append(t.Nullifiers, privacy.FieldBytes(nf[i]))
		t.Commitments = append(t.Commitments, privacy.FieldBytes(out[i].CM()))
		t.Ciphertexts = append(t.Ciphertexts, f.Ciphertexts[i])
	}
	return t, nil
}

// Witness is the transfer circuit's Prover.toml and public inputs for signal.
func (f Fee) Witness(signal fr.Element) (string, []fr.Element, error) {
	if f.Change.Value+f.Fee != f.Note.Value {
		return "", nil, fmt.Errorf("fee %d + change %d != note %d", f.Fee, f.Change.Value, f.Note.Value)
	}
	root, err := f.Tree.Root()
	if err != nil {
		return "", nil, err
	}
	in, pos, out := f.inputs(), f.positions(), f.outputs()
	var vals, outVals, posS [3]string
	var rho, rcm, outPC, cm [3]fr.Element
	var paths [3]string
	for j := range 3 {
		vals[j] = fmt.Sprintf("\"%d\"", in[j].Value)
		posS[j] = fmt.Sprintf("\"%d\"", pos[j])
		rho[j], rcm[j] = in[j].Rho, in[j].Rcm
		var sib [merkle.Depth]fr.Element
		if in[j].Value != 0 {
			if sib, err = f.Tree.Path(pos[j]); err != nil {
				return "", nil, err
			}
		}
		paths[j] = arr(sib[:])
		outVals[j] = fmt.Sprintf("\"%d\"", out[j].Value)
		outPC[j] = out[j].PC()
		cm[j] = out[j].CM()
	}
	nf := f.Nullifiers()
	var b strings.Builder
	fmt.Fprintf(&b, "asset = %s\nnk = %s\n", q(privacy.AssetID("uerth")), q(f.Note.NK))
	fmt.Fprintf(&b, "in_value = [%s]\nin_rho = %s\nin_rcm = %s\nin_pos = [%s]\n",
		strings.Join(vals[:], ", "), arr(rho[:]), arr(rcm[:]), strings.Join(posS[:], ", "))
	fmt.Fprintf(&b, "in_path = [%s]\n", strings.Join(paths[:], ", "))
	fmt.Fprintf(&b, "out_value = [%s]\nout_pc = %s\n", strings.Join(outVals[:], ", "), arr(outPC[:]))
	var zero fr.Element
	fmt.Fprintf(&b, "root = %s\nnf = %s\ncm_out = %s\nfee = \"%d\"\nv_pub_out = \"0\"\nasset_pub = %s\nsignal = %s\n",
		q(root), arr(nf[:]), arr(cm[:]), f.Fee, q(zero), q(signal))
	pub := []fr.Element{root}
	pub = append(pub, nf[:]...)
	pub = append(pub, cm[:]...)
	pub = append(pub, privacy.U64(f.Fee), privacy.U64(0), zero, signal)
	return b.String(), pub, nil
}

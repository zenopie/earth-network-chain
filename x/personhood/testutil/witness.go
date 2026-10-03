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
	IDSecret        fr.Element
	DscKey          fr.Element
	Country         fr.Element
	ActivatedAt     uint64
	PredecessorAt   uint64
	LeafIndex       uint64
	Root            fr.Element
	Siblings        [merkle.Depth]fr.Element
	Scope           fr.Element
	Signal          fr.Element
	ExcludedDsc     fr.Element
	ExcludedCountry fr.Element
	MaxActivation   uint64
	MaxPredecessor  uint64
}

// Nullifier is the proof's nullifier for its scope.
func (m Membership) Nullifier() fr.Element { return privacy.ScopeNullifier(m.IDSecret, m.Scope) }

// Witness is the Prover.toml and public inputs in ABI order.
func (m Membership) Witness() (string, []fr.Element) {
	var b strings.Builder
	fmt.Fprintf(&b, "id_secret = %s\ndsc_key = %s\ncountry = %s\nactivated_at = \"%d\"\npredecessor_at = \"%d\"\nleaf_index = \"%d\"\n",
		q(m.IDSecret), q(m.DscKey), q(m.Country), m.ActivatedAt, m.PredecessorAt, m.LeafIndex)
	fmt.Fprintf(&b, "siblings = %s\n", arr(m.Siblings[:]))
	nf := m.Nullifier()
	fmt.Fprintf(&b, "root = %s\nscope = %s\nnullifier = %s\nsignal = %s\nexcluded_dsc = %s\nexcluded_country = %s\nmax_activation = \"%d\"\nmax_predecessor = \"%d\"\n",
		q(m.Root), q(m.Scope), q(nf), q(m.Signal), q(m.ExcludedDsc), q(m.ExcludedCountry), m.MaxActivation, m.MaxPredecessor)
	return b.String(), []fr.Element{m.Root, m.Scope, nf, m.Signal, m.ExcludedDsc, m.ExcludedCountry, privacy.U64(m.MaxActivation), privacy.U64(m.MaxPredecessor)}
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

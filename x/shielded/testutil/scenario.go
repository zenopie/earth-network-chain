// Package testutil is a deterministic shielded-pool scenario of Orchard-style
// bundles, shared by x/shielded's keeper tests and the app tests that replay
// it on a real chain, plus the action prover (prover.go) whose cached proofs
// they verify.
//
// The chain appends notes in a fixed order (the Shields first, then each
// accepted Send's outputs in action order), so both sides rebuild the exact
// tree every action was proven against. Every value is derived, so every
// action's public inputs, and hence its proof file under
// x/shielded/testdata/proofs, is reproducible. Change anything here and run
// scripts/shielded-fixtures.sh to prove what is missing.
package testutil

import (
	"bytes"
	"fmt"
	"sort"
	"strings"

	"github.com/consensys/gnark-crypto/ecc/bn254/fr"

	"github.com/earth-network/earth/x/shielded/types"
	"github.com/earth-network/earth/zk/merkle"
	"github.com/earth-network/earth/zk/orchard"
	"github.com/earth-network/earth/zk/privacy"
)

// ChainID is the chain the scenario's sighashes are bound to.
const ChainID = "earth-shielded-test"

// MsgSendType is MsgSend's type URL, the first thing its sighash binds.
const MsgSendType = "/earth.shielded.v1.MsgSend"

// Det is a deterministic pseudo-random field element.
func Det(label string, i uint64) fr.Element {
	return privacy.H(privacy.AssetID("shielded-fixture/"+label), privacy.U64(i))
}

// Wallet is a spending key and a note-encryption key.
type Wallet struct {
	NK fr.Element
	EK [32]byte // X25519 secret
}

func (w Wallet) OwnerPK() fr.Element { return privacy.OwnerPK(w.NK) }

// Address is the wallet's shielded address.
func (w Wallet) Address() privacy.ShieldedAddress {
	pub, err := privacy.EKPub(w.EK)
	if err != nil {
		panic(err)
	}
	return privacy.ShieldedAddress{OwnerPK: w.OwnerPK(), EKPub: pub}
}

func detKey(label string, i uint64) [32]byte {
	var k [32]byte
	copy(k[:], privacy.FieldBytes(Det(label, i)))
	return k
}

// Note is a note's opening.
type Note struct {
	Owner Wallet
	Denom string
	Value uint64
	Rho   fr.Element
	Rcm   fr.Element
}

func (n Note) PC() fr.Element { return privacy.PC(n.Owner.OwnerPK(), n.Rho, n.Rcm) }
func (n Note) CM() fr.Element {
	return privacy.CM(privacy.AssetID(n.Denom), n.Value, n.PC())
}

// Action is one action of a Send: a real spend of the note at Position (or a
// dummy spend when Spend is nil) and an output (a dummy when its value is 0).
type Action struct {
	Spend    *Note
	Position uint32
	Out      Note
}

// Send is one MsgSend: Owner's notes spent through one bundle.
type Send struct {
	Name     string
	Owner    Wallet
	Actions  []Action
	Fee      uint64
	Receiver []byte // raw address bytes, nil unless something is unshielded
	// Refused marks a send the chain must refuse: its outputs are never
	// appended, so later sends' trees leave them out.
	Refused bool
}

// Scenario is the whole deterministic history.
type Scenario struct {
	Shields []Note
	Sends   []Send
}

var (
	Alice = Wallet{NK: Det("nk", 1), EK: detKey("ek", 1)}
	Bob   = Wallet{NK: Det("nk", 2), EK: detKey("ek", 2)}
	// Nobody receives the dummy outputs.
	Nobody = Wallet{NK: Det("nk", 3), EK: detKey("ek", 3)}

	// Receiver is where the unshield pays (20 raw address bytes).
	Receiver = []byte("shielded-fixture-rcv")
)

func note(owner Wallet, denom string, value uint64, label string, i uint64) Note {
	return Note{Owner: owner, Denom: denom, Value: value, Rho: Det(label+"/rho", i), Rcm: Det(label+"/rcm", i)}
}

// Scenario sends by name, in order.
const (
	Send2        = 0 // a send padded to two actions
	Multi3       = 1 // ANML send + ERTH fee, one mixed-asset action
	Unshield2    = 2 // unshield of uerth paying its fee out of what it releases
	Consolidate  = 3 // ten actions: eight small notes and two changes merged
	DoubleSpend  = 4 // send2's note again, under a fresh bundle: refused
	SingleAction = 5 // one action, a real proof: refused by the padding rule
)

// Default is the scenario:
//
//	positions 0..10  MsgShield (Alice): 1,000,000 uerth; 100,000 uerth;
//	                 5,000,000 uanml; eight notes of 2,000 uerth
//	send2       Alice -> Bob 700,000 uerth, change 270,000; fee 30,000.  11,12
//	multi3      Alice -> Bob 1,000,000 uanml; the ERTH note pays the fee and
//	            its spend action outputs the ANML change (mixed assets);
//	            ERTH change 60,000; fee 40,000.                          13..15
//	unshield2   Bob unshields 500,000 uerth to Receiver from his 700,000,
//	            change 170,000; the fee comes out of the unshield.       16,17
//	consolidate Alice merges 8 x 2,000 + 270,000 + 60,000 into 216,000;
//	            fee 130,000 (ten actions).                               18..27
//	doublespend Alice spends position 0 again: refused (nullifier spent).
//	single1     Bob spends his 170,000 in one action: refused (padding).
func Default() Scenario {
	const erth, anml = types.FeeDenom, types.AnmlDenom
	s := Scenario{Shields: []Note{
		note(Alice, erth, 1_000_000, "shield", 0),
		note(Alice, erth, 100_000, "shield", 1),
		note(Alice, anml, 5_000_000, "shield", 2),
	}}
	for k := range uint64(8) {
		s.Shields = append(s.Shields, note(Alice, erth, 2_000, "small", k))
	}
	sh := func(i int) *Note { return &s.Shields[i] }
	dummy := func(label string, i uint64) Note { return note(Nobody, erth, 0, label, i) }

	send2 := Send{Name: "send2", Owner: Alice, Fee: 30_000, Actions: []Action{
		{Spend: sh(0), Position: 0, Out: note(Bob, erth, 700_000, "send2/out", 0)},
		{Out: note(Alice, erth, 270_000, "send2/out", 1)},
	}}
	multi3 := Send{Name: "multi3", Owner: Alice, Fee: 40_000, Actions: []Action{
		{Spend: sh(2), Position: 2, Out: note(Bob, anml, 1_000_000, "multi3/out", 0)},
		{Spend: sh(1), Position: 1, Out: note(Alice, anml, 4_000_000, "multi3/out", 1)},
		{Out: note(Alice, erth, 60_000, "multi3/out", 2)},
	}}
	unshield2 := Send{Name: "unshield2", Owner: Bob, Fee: 30_000, Receiver: Receiver, Actions: []Action{
		{Spend: &send2.Actions[0].Out, Position: 11, Out: note(Bob, erth, 170_000, "unshield2/out", 0)},
		{Out: dummy("unshield2/out", 1)},
	}}
	cons := Send{Name: "consolidate", Owner: Alice, Fee: 130_000}
	for k := range 8 {
		out := dummy("consolidate/out", uint64(k))
		if k == 0 {
			out = note(Alice, erth, 216_000, "consolidate/out", 0)
		}
		cons.Actions = append(cons.Actions, Action{Spend: sh(3 + k), Position: uint32(3 + k), Out: out})
	}
	cons.Actions = append(cons.Actions,
		Action{Spend: &send2.Actions[1].Out, Position: 12, Out: dummy("consolidate/out", 8)},
		Action{Spend: &multi3.Actions[2].Out, Position: 15, Out: dummy("consolidate/out", 9)},
	)
	double := Send{Name: "doublespend", Owner: Alice, Fee: 30_000, Refused: true, Actions: []Action{
		{Spend: sh(0), Position: 0, Out: note(Alice, erth, 970_000, "doublespend/out", 0)},
		{Out: dummy("doublespend/out", 1)},
	}}
	single := Send{Name: "single1", Owner: Bob, Fee: 30_000, Refused: true, Actions: []Action{
		{Spend: &unshield2.Actions[0].Out, Position: 16, Out: note(Bob, erth, 140_000, "single1/out", 0)},
	}}
	s.Sends = []Send{send2, multi3, unshield2, cons, double, single}
	return s
}

// TreeBefore is the note tree as it stands when send i is proven: every
// shield, then the outputs of the accepted sends before i.
func (s Scenario) TreeBefore(i int) (*merkle.Tree, error) {
	t := merkle.NewMem()
	for _, n := range s.Shields {
		if _, err := t.Append(n.CM()); err != nil {
			return nil, err
		}
	}
	for _, sd := range s.Sends[:i] {
		if sd.Refused {
			continue
		}
		for _, a := range sd.Actions {
			if _, err := t.Append(a.Out.CM()); err != nil {
				return nil, err
			}
		}
	}
	return t, nil
}

// dummySpend is action j's dummy spend note: value 0, a fresh rho.
func (sd Send) dummySpend(j int) Note {
	return note(sd.Owner, types.FeeDenom, 0, sd.Name+"/dummy", uint64(j))
}

// spendOf is action j's spent note and position (a dummy at position 0).
func (sd Send) spendOf(j int) (Note, uint32) {
	a := sd.Actions[j]
	if a.Spend == nil {
		return sd.dummySpend(j), 0
	}
	return *a.Spend, a.Position
}

// Rcv is action j's value-commitment randomness.
func (sd Send) Rcv(j int) fr.Element { return Det(sd.Name+"/rcv", uint64(j)) }

// Balances is the bundle's value balance: per denom, spends less outputs,
// every one positive (one per denom, in denom order).
func (sd Send) Balances() []types.ValueBalance {
	net := map[string]int64{}
	for j := range sd.Actions {
		sp, _ := sd.spendOf(j)
		net[sp.Denom] += int64(sp.Value)
		net[sd.Actions[j].Out.Denom] -= int64(sd.Actions[j].Out.Value)
	}
	var out []types.ValueBalance
	for d, v := range net {
		if v < 0 {
			panic(fmt.Sprintf("%s creates %s", sd.Name, d))
		}
		if v > 0 {
			out = append(out, types.ValueBalance{Denom: d, Amount: uint64(v)})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Denom < out[j].Denom })
	return out
}

// ct is out's real ciphertext ("earth note v1", zk/privacy.EncryptNote) to its
// owner's address, with a deterministic ephemeral key.
func ct(out Note, label string, i int) []byte {
	pt := privacy.NotePlaintext{AssetID: privacy.AssetID(out.Denom), Value: out.Value, Rho: out.Rho, Rcm: out.Rcm}
	copy(pt.Memo[:], fmt.Sprintf("%s:%d", label, i))
	c, err := privacy.EncryptNote(pt, out.CM(), out.Owner.Address().EKPub, detKey("esk/"+label, uint64(i)))
	if err != nil {
		panic(err)
	}
	return c
}

// Bundle is send i's bundle without proofs or binding signature: what the
// sighash binds.
func (s Scenario) Bundle(i int) (*types.Bundle, error) {
	sd := s.Sends[i]
	t, err := s.TreeBefore(i)
	if err != nil {
		return nil, err
	}
	root, err := t.Root()
	if err != nil {
		return nil, err
	}
	b := &types.Bundle{Balances: sd.Balances()}
	for j, a := range sd.Actions {
		sp, pos := sd.spendOf(j)
		cv := orchard.ValueCommit(privacy.AssetID(sp.Denom), sp.Value, privacy.AssetID(a.Out.Denom), a.Out.Value, sd.Rcv(j))
		b.Actions = append(b.Actions, types.Action{
			Anchor:     privacy.FieldBytes(root),
			Nullifier:  privacy.FieldBytes(privacy.NF(sd.Owner.NK, sp.Rho, pos)),
			Commitment: privacy.FieldBytes(a.Out.CM()),
			Cv:         orchard.PointBytes(cv),
			Ciphertext: ct(a.Out, sd.Name, j),
		})
	}
	return b, nil
}

// SighashFields is MsgSend's fields for send i: Bytes(receiver), fee.
func (sd Send) SighashFields() []fr.Element {
	return []fr.Element{privacy.Bytes(sd.Receiver), privacy.U64(sd.Fee)}
}

// Sighash is send i's MsgSend sighash on ChainID in a tx with fields tx,
// computed from the scenario's own values (the chain recomputes it from the
// msg and the tx).
func (s Scenario) Sighash(i int, b *types.Bundle, tx types.TxFields) (fr.Element, error) {
	ob, err := b.ToOrchard()
	if err != nil {
		return fr.Element{}, err
	}
	return orchard.Sighash(MsgSendType, ChainID, tx, []*orchard.Bundle{ob}, s.Sends[i].SighashFields()...), nil
}

// ActionWitness is action j of send i under sighash: its Prover.toml for the
// action circuit and the public inputs the chain computes.
func (s Scenario) ActionWitness(i, j int, b *types.Bundle, sighash fr.Element) (string, [][]byte, error) {
	sd := s.Sends[i]
	t, err := s.TreeBefore(i)
	if err != nil {
		return "", nil, err
	}
	sp, pos := sd.spendOf(j)
	var path [merkle.Depth]fr.Element
	if sp.Value != 0 {
		if path, err = t.Path(uint64(pos)); err != nil {
			return "", nil, err
		}
	}
	ob, err := b.ToOrchard()
	if err != nil {
		return "", nil, err
	}
	out := sd.Actions[j].Out
	return ActionToml(ActionInputs{
		NK: sd.Owner.NK, SAsset: privacy.AssetID(sp.Denom), SValue: sp.Value, SRho: sp.Rho, SRcm: sp.Rcm,
		SPos: pos, SPath: path[:], OAsset: privacy.AssetID(out.Denom), OValue: out.Value, OPC: out.PC(),
		Rcv: sd.Rcv(j),
	}, ob.PublicInputs(j, sighash)), ob.PublicInputs(j, sighash), nil
}

// ActionInputs is an action circuit witness's private part.
type ActionInputs struct {
	NK, SAsset fr.Element
	SValue     uint64
	SRho, SRcm fr.Element
	SPos       uint32
	SPath      []fr.Element
	OAsset     fr.Element
	OValue     uint64
	OPC, Rcv   fr.Element
}

// ActionToml is the action circuit's Prover.toml for in and the public
// inputs pub (anchor, nf, cm_out, cv_x, cv_y, sighash).
func ActionToml(in ActionInputs, pub [][]byte) string {
	q := func(b []byte) string { return fmt.Sprintf("\"0x%x\"", b) }
	qe := func(e fr.Element) string { return q(privacy.FieldBytes(e)) }
	path := make([]string, merkle.Depth)
	for k := range path {
		var e fr.Element
		if k < len(in.SPath) {
			e = in.SPath[k]
		}
		path[k] = qe(e)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "nk = %s\ns_asset = %s\ns_value = \"%d\"\ns_rho = %s\ns_rcm = %s\ns_pos = \"%d\"\ns_path = [%s]\n",
		qe(in.NK), qe(in.SAsset), in.SValue, qe(in.SRho), qe(in.SRcm), in.SPos, strings.Join(path, ", "))
	fmt.Fprintf(&b, "o_asset = %s\no_value = \"%d\"\no_pc = %s\nrcv = %s\n", qe(in.OAsset), in.OValue, qe(in.OPC), qe(in.Rcv))
	names := []string{"anchor", "nf", "cm_out", "cv_x", "cv_y", "sighash"}
	for k, n := range names {
		fmt.Fprintf(&b, "%s = %s\n", n, q(pub[k]))
	}
	return b.String()
}

// Sign makes send i's binding signature over sighash (deterministic nonce
// input, so fixtures are stable).
func (s Scenario) Sign(i int, sighash fr.Element) ([]byte, error) {
	sd := s.Sends[i]
	rcvs := make([]fr.Element, len(sd.Actions))
	for j := range rcvs {
		rcvs[j] = sd.Rcv(j)
	}
	return orchard.SignBinding(orchard.BindingSigningKey(rcvs), sighash, bytes.NewReader(make([]byte, 32)))
}

// Bsk is send i's binding signing key, for tests re-signing a tampered msg.
func (s Scenario) Bsk(i int) []fr.Element {
	sd := s.Sends[i]
	rcvs := make([]fr.Element, len(sd.Actions))
	for j := range rcvs {
		rcvs[j] = sd.Rcv(j)
	}
	return rcvs
}

// Msg builds send i as a proven, signed MsgSend for a tx with fields tx.
// receiver is the bech32 of Receiver (or "" when the send unshields
// nothing); prove supplies each action's proof from its witness.
func (s Scenario) Msg(i int, receiver string, tx types.TxFields, prove func(toml string, pub [][]byte) []byte) (*types.MsgSend, error) {
	b, err := s.Bundle(i)
	if err != nil {
		return nil, err
	}
	sighash, err := s.Sighash(i, b, tx)
	if err != nil {
		return nil, err
	}
	for j := range b.Actions {
		toml, pub, err := s.ActionWitness(i, j, b, sighash)
		if err != nil {
			return nil, err
		}
		b.Actions[j].Proof = prove(toml, pub)
	}
	if b.BindingSig, err = s.Sign(i, sighash); err != nil {
		return nil, err
	}
	m := &types.MsgSend{Bundle: *b, Fee: s.Sends[i].Fee}
	if s.Sends[i].Receiver != nil {
		m.Receiver = receiver
	}
	return m, nil
}

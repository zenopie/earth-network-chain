// Package testutil is a deterministic shielded-pool scenario shared by the
// proof generator (tools/privacyfixtures shielded, run by
// scripts/shielded-fixtures.sh) and the tests that replay it on a real chain.
//
// The chain appends notes in a fixed order — the Shields first, then each
// Transfer's three outputs — so both sides can rebuild the exact tree every
// proof was made against. Change anything here and the committed proofs in
// x/shielded/testdata stop verifying: regenerate them.
package testutil

import (
	"fmt"
	"strings"

	"github.com/consensys/gnark-crypto/ecc/bn254/fr"

	"github.com/earth-network/earth/x/shielded/types"
	"github.com/earth-network/earth/zk/merkle"
	"github.com/earth-network/earth/zk/privacy"
)

// ChainID is the chain the scenario's signals are bound to.
const ChainID = "earth-shielded-test"

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

// Input is one transfer input: a note at Position, or a dummy (Value 0) with
// a fresh rho whose nullifier is published but whose membership is not
// checked.
type Input struct {
	Note     Note
	Position uint32
}

// Spend is one transfer.
type Spend struct {
	Name        string
	Denom       string // the hidden asset A of slots 0-1
	Owner       Wallet // owns every real input
	In          [3]Input
	Out         [3]Note // slot 2 is uerth
	Ciphertexts [3][]byte
	Fee         uint64
	ValueOut    uint64
	Receiver    []byte // raw address bytes, nil unless ValueOut > 0
}

// Scenario is the whole deterministic history.
type Scenario struct {
	Shields   []Note
	Transfers []Spend
}

var (
	Alice = Wallet{NK: Det("nk", 1), EK: detKey("ek", 1)}
	Bob   = Wallet{NK: Det("nk", 2), EK: detKey("ek", 2)}

	// Receiver is where transfer 1 unshields to (20 raw address bytes).
	Receiver = []byte("shielded-fixture-rcv")
)

func note(owner Wallet, denom string, value uint64, label string, i uint64) Note {
	return Note{Owner: owner, Denom: denom, Value: value, Rho: Det(label+"/rho", i), Rcm: Det(label+"/rcm", i)}
}

// ct is out's real ciphertext ("earth note v1", zk/privacy.EncryptNote) to
// its owner's address, with a deterministic ephemeral key.
func ct(out Note, label string, i int) []byte {
	pt := privacy.NotePlaintext{AssetID: privacy.AssetID(out.Denom), Value: out.Value, Rho: out.Rho, Rcm: out.Rcm}
	copy(pt.Memo[:], fmt.Sprintf("%s:%d", label, i))
	c, err := privacy.EncryptNote(pt, out.CM(), out.Owner.Address().EKPub, detKey("esk/"+label, uint64(i)))
	if err != nil {
		panic(err)
	}
	return c
}

// Default is the scenario the committed proofs were made for:
//
//	positions 0,1   MsgShield: Alice 1,000,000 uerth and 100,000 uerth (fee note)
//	transfer 0      Alice -> Bob 700,000; change 300,000 to Alice; fee note
//	                change 80,000 to Bob; fee 20,000.        outputs at 2,3,4
//	transfer 1      Bob unshields 500,000 to Receiver, keeps 200,000, a zero
//	                output, fee change 60,000; fee 20,000.  outputs at 5,6,7
func Default() Scenario {
	const erth = types.FeeDenom
	s := Scenario{
		Shields: []Note{
			note(Alice, erth, 1_000_000, "shield", 0),
			note(Alice, erth, 100_000, "shield", 1),
		},
	}
	t0 := Spend{
		Name: "transfer0", Denom: erth, Owner: Alice,
		In: [3]Input{
			{Note: s.Shields[0], Position: 0},
			{Note: note(Alice, erth, 0, "t0/dummy", 0), Position: 0},
			{Note: s.Shields[1], Position: 1},
		},
		Out: [3]Note{
			note(Bob, erth, 700_000, "t0/out", 0),
			note(Alice, erth, 300_000, "t0/out", 1),
			note(Bob, erth, 80_000, "t0/out", 2),
		},
		Fee: 20_000,
	}
	for i := range 3 {
		t0.Ciphertexts[i] = ct(t0.Out[i], "t0", i)
	}
	t1 := Spend{
		Name: "transfer1", Denom: erth, Owner: Bob,
		In: [3]Input{
			{Note: t0.Out[0], Position: 2},
			{Note: note(Bob, erth, 0, "t1/dummy", 0), Position: 0},
			{Note: t0.Out[2], Position: 4},
		},
		Out: [3]Note{
			note(Bob, erth, 200_000, "t1/out", 0),
			note(Bob, erth, 0, "t1/out", 1),
			note(Bob, erth, 60_000, "t1/out", 2),
		},
		Fee:      20_000,
		ValueOut: 500_000,
		Receiver: Receiver,
	}
	for i := range 3 {
		t1.Ciphertexts[i] = ct(t1.Out[i], "t1", i)
	}
	s.Transfers = []Spend{t0, t1}
	return s
}

// TreeBefore is the note tree as it stands when transfer i is proven: every
// shield, then the outputs of transfers 0..i-1.
func (s Scenario) TreeBefore(i int) (*merkle.Tree, error) {
	t := merkle.NewMem()
	for _, n := range s.Shields {
		if _, err := t.Append(n.CM()); err != nil {
			return nil, err
		}
	}
	for _, sp := range s.Transfers[:i] {
		for _, o := range sp.Out {
			if _, err := t.Append(o.CM()); err != nil {
				return nil, err
			}
		}
	}
	return t, nil
}

// Nullifiers of s's inputs.
func (sp Spend) Nullifiers() [3]fr.Element {
	var nf [3]fr.Element
	for i, in := range sp.In {
		nf[i] = privacy.NF(sp.Owner.NK, in.Note.Rho, in.Position)
	}
	return nf
}

// Signal is MsgTransfer's signal for this spend on ChainID.
func (sp Spend) Signal() fr.Element {
	return privacy.TransferSignal(ChainID, sp.Receiver, sp.Ciphertexts, 0)
}

// DenomOut is the unshielded denom, "" when nothing leaves the pool.
func (sp Spend) DenomOut() string {
	if sp.ValueOut == 0 {
		return ""
	}
	return sp.Denom
}

// Transfer builds the msg's Transfer for spend i with proof.
func (s Scenario) Transfer(i int, proof []byte) (types.Transfer, error) {
	sp := s.Transfers[i]
	t, err := s.TreeBefore(i)
	if err != nil {
		return types.Transfer{}, err
	}
	root, err := t.Root()
	if err != nil {
		return types.Transfer{}, err
	}
	out := types.Transfer{
		Proof: proof, Root: privacy.FieldBytes(root),
		Fee: sp.Fee, ValueOut: sp.ValueOut, DenomOut: sp.DenomOut(),
	}
	nf := sp.Nullifiers()
	for j := range 3 {
		out.Nullifiers = append(out.Nullifiers, privacy.FieldBytes(nf[j]))
		out.Commitments = append(out.Commitments, privacy.FieldBytes(sp.Out[j].CM()))
		out.Ciphertexts = append(out.Ciphertexts, sp.Ciphertexts[j])
	}
	return out, nil
}

// Witness is spend i's Prover.toml and the public inputs the chain computes,
// in ABI order.
func (s Scenario) Witness(i int) (string, []fr.Element, error) {
	sp := s.Transfers[i]
	t, err := s.TreeBefore(i)
	if err != nil {
		return "", nil, err
	}
	root, err := t.Root()
	if err != nil {
		return "", nil, err
	}
	q := func(e fr.Element) string { b := e.Bytes(); return fmt.Sprintf("\"0x%x\"", b[:]) }
	arr := func(es []fr.Element) string {
		p := make([]string, len(es))
		for j, e := range es {
			p[j] = q(e)
		}
		return "[" + strings.Join(p, ", ") + "]"
	}
	var vals, outVals, pos [3]string
	var rho, rcm, outPC [3]fr.Element
	var paths [3]string
	for j, in := range sp.In {
		vals[j] = fmt.Sprintf("\"%d\"", in.Note.Value)
		pos[j] = fmt.Sprintf("\"%d\"", in.Position)
		rho[j], rcm[j] = in.Note.Rho, in.Note.Rcm
		var sib [merkle.Depth]fr.Element
		if in.Note.Value != 0 {
			if sib, err = t.Path(uint64(in.Position)); err != nil {
				return "", nil, err
			}
		}
		paths[j] = arr(sib[:])
		outVals[j] = fmt.Sprintf("\"%d\"", sp.Out[j].Value)
		outPC[j] = sp.Out[j].PC()
	}
	nf := sp.Nullifiers()
	var cm [3]fr.Element
	for j := range 3 {
		cm[j] = sp.Out[j].CM()
	}
	var assetPub fr.Element
	if sp.ValueOut > 0 {
		assetPub = privacy.AssetID(sp.Denom)
	}
	signal := sp.Signal()

	var b strings.Builder
	fmt.Fprintf(&b, "# Generated by tools/privacyfixtures shielded (%s). Do not edit.\n", sp.Name)
	fmt.Fprintf(&b, "asset = %s\nnk = %s\n", q(privacy.AssetID(sp.Denom)), q(sp.Owner.NK))
	fmt.Fprintf(&b, "in_value = [%s]\nin_rho = %s\nin_rcm = %s\nin_pos = [%s]\n",
		strings.Join(vals[:], ", "), arr(rho[:]), arr(rcm[:]), strings.Join(pos[:], ", "))
	fmt.Fprintf(&b, "in_path = [%s]\n", strings.Join(paths[:], ", "))
	fmt.Fprintf(&b, "out_value = [%s]\nout_pc = %s\n", strings.Join(outVals[:], ", "), arr(outPC[:]))
	fmt.Fprintf(&b, "root = %s\nnf = %s\ncm_out = %s\nfee = \"%d\"\nv_pub_out = \"%d\"\nasset_pub = %s\nsignal = %s\n",
		q(root), arr(nf[:]), arr(cm[:]), sp.Fee, sp.ValueOut, q(assetPub), q(signal))

	pub := []fr.Element{root}
	pub = append(pub, nf[:]...)
	pub = append(pub, cm[:]...)
	pub = append(pub, privacy.U64(sp.Fee), privacy.U64(sp.ValueOut), assetPub, signal)
	return b.String(), pub, nil
}

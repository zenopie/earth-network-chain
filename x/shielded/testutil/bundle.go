package testutil

import (
	"bytes"
	"fmt"
	"sort"

	"cosmossdk.io/core/address"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"

	"github.com/earth-network/earth/x/shielded/types"
	"github.com/earth-network/earth/zk/merkle"
	"github.com/earth-network/earth/zk/orchard"
	"github.com/earth-network/earth/zk/privacy"
)

// The bundle builder every module's tests use: a plan of actions (real or
// dummy spends and outputs) against one note tree, laid out as an unproven
// bundle, put into the msg, and then proven under the msg's own sighash
// (types.Sighash) and signed. Every value is the caller's or derived from
// Seed, so a deterministic test produces the same public inputs, and so the
// same cached proofs, on every run.

// PlanSpend is a note being spent: its owner's nk, its opening and position.
type PlanSpend struct {
	NK       fr.Element
	Denom    string
	Value    uint64
	Rho, Rcm fr.Element
	Position uint64
}

// PlanOutput is an action's output note: asset, value and pc (value 0 is a
// dummy). Ciphertext is emitted as is.
type PlanOutput struct {
	Denom      string
	Value      uint64
	PC         fr.Element
	Ciphertext []byte
}

// PlanAction is one action: Spend nil is a dummy spend (value 0, a fresh rho
// derived from the plan's Seed).
type PlanAction struct {
	Spend *PlanSpend
	Out   PlanOutput
}

// Plan is one bundle: its actions, all proven against Tree's root.
type Plan struct {
	// Seed labels everything derived (dummy rhos, rcvs); distinct per bundle.
	Seed    string
	Tree    *merkle.Tree
	Actions []PlanAction
	// DummyNK owns the dummy spends (any key will do).
	DummyNK fr.Element
}

func (p *Plan) det(label string, i int) fr.Element {
	return privacy.H(privacy.AssetID("bundle-plan/"+p.Seed+"/"+label), privacy.U64(uint64(i)))
}

// spend is action j's spent note (the dummy for a nil Spend).
func (p *Plan) spend(j int) PlanSpend {
	if s := p.Actions[j].Spend; s != nil {
		return *s
	}
	return PlanSpend{NK: p.DummyNK, Denom: types.FeeDenom, Rho: p.det("dummy-rho", j), Rcm: p.det("dummy-rcm", j)}
}

// Rcv is action j's value-commitment randomness.
func (p *Plan) Rcv(j int) fr.Element { return p.det("rcv", j) }

// Balances is the plan's value balance: per denom, spends less outputs, each
// positive, in denom order. It panics on a plan creating value.
func (p *Plan) Balances() []types.ValueBalance {
	net := map[string]int64{}
	for j, a := range p.Actions {
		s := p.spend(j)
		net[s.Denom] += int64(s.Value)
		net[a.Out.Denom] -= int64(a.Out.Value)
	}
	var out []types.ValueBalance
	for d, v := range net {
		if v < 0 {
			panic(fmt.Sprintf("plan %s creates %d%s", p.Seed, -v, d))
		}
		if v > 0 {
			out = append(out, types.ValueBalance{Denom: d, Amount: uint64(v)})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Denom < out[j].Denom })
	return out
}

// Unproven lays the plan out as a bundle with neither proofs nor binding
// signature: everything the sighash binds. Put it into the msg (by value),
// then call Prove with the msg's copy.
func (p *Plan) Unproven() (types.Bundle, error) {
	root, err := p.Tree.Root()
	if err != nil {
		return types.Bundle{}, err
	}
	b := types.Bundle{Balances: p.Balances()}
	for j, a := range p.Actions {
		s := p.spend(j)
		out := a.Out
		if out.Denom == "" {
			out.Denom = types.FeeDenom
		}
		cv := orchard.ValueCommit(privacy.AssetID(s.Denom), s.Value, privacy.AssetID(out.Denom), out.Value, p.Rcv(j))
		ct := out.Ciphertext
		if ct == nil {
			ct = NoteCT(fmt.Sprintf("plan-ct:%s:%d", p.Seed, j))
		}
		b.Actions = append(b.Actions, types.Action{
			Anchor:     privacy.FieldBytes(root),
			Nullifier:  privacy.FieldBytes(privacy.NF(s.NK, s.Rho, uint32(s.Position))),
			Commitment: privacy.FieldBytes(privacy.CM(privacy.AssetID(out.Denom), out.Value, out.PC)),
			Cv:         orchard.PointBytes(cv),
			Ciphertext: ct,
		})
	}
	return b, nil
}

// ProveFunc proves one action from its Prover.toml and public inputs.
type ProveFunc func(toml string, pub [][]byte) ([]byte, error)

// Prove fills b's action proofs and binding signature under sighash. b must
// be the plan's Unproven bundle (as the msg carries it).
func (p *Plan) Prove(b *types.Bundle, sighash fr.Element, prove ProveFunc) error {
	if len(b.Actions) != len(p.Actions) {
		return fmt.Errorf("plan %s: bundle has %d actions, plan %d", p.Seed, len(b.Actions), len(p.Actions))
	}
	ob, err := b.ToOrchard()
	if err != nil {
		return err
	}
	rcvs := make([]fr.Element, len(p.Actions))
	for j, a := range p.Actions {
		s := p.spend(j)
		var path [merkle.Depth]fr.Element
		if s.Value != 0 {
			if path, err = p.Tree.Path(s.Position); err != nil {
				return err
			}
		}
		out := a.Out
		if out.Denom == "" {
			out.Denom = types.FeeDenom
		}
		pub := ob.PublicInputs(j, sighash)
		toml := ActionToml(ActionInputs{
			NK: s.NK, SAsset: privacy.AssetID(s.Denom), SValue: s.Value, SRho: s.Rho, SRcm: s.Rcm,
			SPos: uint32(s.Position), SPath: path[:], OAsset: privacy.AssetID(out.Denom), OValue: out.Value,
			OPC: out.PC, Rcv: p.Rcv(j),
		}, pub)
		if b.Actions[j].Proof, err = prove(toml, pub); err != nil {
			return fmt.Errorf("plan %s action %d: %w", p.Seed, j, err)
		}
		rcvs[j] = p.Rcv(j)
	}
	b.BindingSig, err = orchard.SignBinding(orchard.BindingSigningKey(rcvs), sighash, bytes.NewReader(make([]byte, 32)))
	return err
}

// ProveMsg proves every bundle of msg, plans[i] for msg.PrivateBundles()[i],
// under msg's sighash on chainID in a tx with fields tx (the memo, timeout
// height and gas limit the tx will carry). Every other field of msg must be
// final.
func ProveMsg(msg types.PrivateMsg, chainID string, tx types.TxFields, ac address.Codec, plans []*Plan, prove ProveFunc) error {
	bs := msg.PrivateBundles()
	if len(bs) != len(plans) {
		return fmt.Errorf("%d bundles, %d plans", len(bs), len(plans))
	}
	sighash, err := types.Sighash(msg, chainID, tx, ac)
	if err != nil {
		return err
	}
	for i, p := range plans {
		if err := p.Prove(bs[i], sighash, prove); err != nil {
			return err
		}
	}
	return nil
}

// FeePlan is the common fee bundle: one uerth note at position pays fee, the
// change (note.Value - fee) goes to changePC, and a dummy action pads it to
// two.
func FeePlan(seed string, tree *merkle.Tree, note PlanSpend, fee uint64, changePC fr.Element) *Plan {
	return &Plan{Seed: seed, Tree: tree, DummyNK: note.NK, Actions: []PlanAction{
		{Spend: &note, Out: PlanOutput{Denom: types.FeeDenom, Value: note.Value - fee, PC: changePC}},
		{Out: PlanOutput{Denom: types.FeeDenom, PC: privacy.H(privacy.AssetID("bundle-plan/"+seed+"/dummy-pc"), privacy.U64(0))}},
	}}
}

// Command orchardfixtures writes an N-action shielded bundle for the action
// circuit (earth-network-mobile/circuits/action): one Prover.toml per action,
// the public inputs Go expects for each, and bundle.json holding everything the
// chain sees (anchor, nf, cm, cv, ciphertexts, balances, binding signature,
// the msg the sighash binds). nargo execute accepting each toml is the
// Go<->Noir parity check for the value bases, R and the value commitments.
//
//	go run ./tools/orchardfixtures <n> <outdir>
//
// The bundle (a private ANML send paying an ERTH fee):
//
//	n = 1:  spend ERTH 1_000_000 -> out ERTH 990_000              (fee 10_000)
//	n = 2:  spend ERTH 1_000_000 -> out ERTH 600_000
//	        dummy spend          -> out ERTH 390_000
//	n >= 3: spend ERTH 1_000_000 -> out ANML 500                  (mixed assets)
//	        spend ANML 700       -> out ERTH 990_000              (mixed assets)
//	        dummy spend          -> out ANML 200
//	        then pairs: spend ANML k -> out ANML k-1; dummy -> out ANML 1
//	        and, if one is left, spend ERTH 7 -> out ERTH 7
//
// Balance: ERTH 10_000 (the fee), every other asset 0.
package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/consensys/gnark-crypto/ecc/bn254/fr"

	"github.com/earth-network/earth/zk/merkle"
	"github.com/earth-network/earth/zk/orchard"
	"github.com/earth-network/earth/zk/privacy"
)

func det(label string, i uint64) fr.Element {
	return privacy.H(privacy.AssetID("orchard-fixture/"+label), privacy.U64(i))
}

func h(e fr.Element) string {
	b := e.Bytes()
	return "0x" + hex.EncodeToString(b[:])
}

func q(e fr.Element) string { return strconv.Quote(h(e)) }

func must(err error) {
	if err != nil {
		panic(err)
	}
}

// spec is one action's plain content.
type spec struct {
	sAsset fr.Element
	sValue uint64 // 0: dummy spend
	oAsset fr.Element
	oValue uint64
}

// BundleJSON is what the chain sees, hex-encoded.
type BundleJSON struct {
	MsgType    string        `json:"msg_type"`
	ChainID    string        `json:"chain_id"`
	Tx         TxJSON        `json:"tx"`
	Actions    []ActionJSON  `json:"actions"`
	Balances   []BalanceJSON `json:"balances"`
	BindingSig string        `json:"binding_sig"`
	Sighash    string        `json:"sighash"`
}

// TxJSON is the tx fields the sighash binds (orchard.TxFields).
type TxJSON struct {
	Memo          string `json:"memo"`
	TimeoutHeight uint64 `json:"timeout_height"`
	GasLimit      uint64 `json:"gas_limit"`
}

type ActionJSON struct {
	Anchor     string `json:"anchor"`
	Nullifier  string `json:"nf"`
	Commitment string `json:"cm"`
	Cv         string `json:"cv"` // x || y
	Ciphertext string `json:"ct"`
}

type BalanceJSON struct {
	Denom string `json:"denom"`
	Asset string `json:"asset"`
	Value uint64 `json:"value"`
}

func specs(n int) []spec {
	erth, anml := privacy.AssetID("uerth"), privacy.AssetID("uanml")
	if n == 1 {
		return []spec{{erth, 1_000_000, erth, 990_000}}
	}
	if n == 2 {
		return []spec{{erth, 1_000_000, erth, 600_000}, {det("dummyasset", 0), 0, erth, 390_000}}
	}
	s := []spec{
		{erth, 1_000_000, anml, 500},
		{anml, 700, erth, 990_000},
		{det("dummyasset", 0), 0, anml, 200},
	}
	for k := uint64(1); len(s) < n; k++ {
		if n-len(s) >= 2 {
			s = append(s, spec{anml, 1000 * k, anml, 1000*k - 1}, spec{det("dummyasset", k), 0, anml, 1})
		} else {
			s = append(s, spec{erth, 7, erth, 7})
		}
	}
	return s
}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: orchardfixtures <n> <outdir>")
		os.Exit(2)
	}
	n, err := strconv.Atoi(os.Args[1])
	must(err)
	out := os.Args[2]
	ss := specs(n)

	// Note tree: unrelated notes, with the real spends at positions 3, 6, 9, ...
	t := merkle.NewMem()
	nk := det("nk", 0)
	opk := privacy.OwnerPK(nk)
	type spent struct {
		pos      uint64
		rho, rcm fr.Element
	}
	sp := make([]spent, n)
	for i, s := range ss {
		sp[i] = spent{pos: uint64(3 * (i + 1)), rho: det("rho", uint64(i)), rcm: det("rcm", uint64(i))}
		if s.sValue == 0 {
			sp[i].pos = uint64(1_000_000 + i) // dummy: any position, not in the tree
		}
	}
	for p := uint64(0); p < uint64(3*n+5); p++ {
		cm := privacy.CM(privacy.AssetID("uerth"), p+1, det("otherpc", p))
		for i, s := range ss {
			if s.sValue != 0 && sp[i].pos == p {
				cm = privacy.CM(s.sAsset, s.sValue, privacy.PC(opk, sp[i].rho, sp[i].rcm))
			}
		}
		_, err := t.Append(cm)
		must(err)
	}
	anchor, err := t.Root()
	must(err)

	b := &orchard.Bundle{}
	rcvs := make([]fr.Element, n)
	outPC := make([]fr.Element, n)
	paths := make([]string, n)
	bal := map[fr.Element]int64{}
	for i, s := range ss {
		rcvs[i] = det("rcv", uint64(i))
		outPC[i] = privacy.PC(privacy.OwnerPK(det("recipient", uint64(i))), det("orho", uint64(i)), det("orcm", uint64(i)))
		path := make([]fr.Element, merkle.Depth)
		if s.sValue != 0 {
			sib, err := t.Path(sp[i].pos)
			must(err)
			copy(path, sib[:])
		}
		parts := make([]string, len(path))
		for j, e := range path {
			parts[j] = q(e)
		}
		paths[i] = "[" + strings.Join(parts, ", ") + "]"
		ct := bytes.Repeat([]byte{byte(i + 1)}, 217) // stand-in ciphertext
		b.Actions = append(b.Actions, orchard.Action{
			Anchor:     anchor, // dummies too: the chain requires a valid anchor for every action
			Nullifier:  privacy.NF(nk, sp[i].rho, uint32(sp[i].pos)),
			Commitment: privacy.CM(s.oAsset, s.oValue, outPC[i]),
			Cv:         orchard.ValueCommit(s.sAsset, s.sValue, s.oAsset, s.oValue, rcvs[i]),
			Ciphertext: ct,
		})
		bal[s.sAsset] += int64(s.sValue)
		bal[s.oAsset] -= int64(s.oValue)
	}
	erth := privacy.AssetID("uerth")
	for a, v := range bal {
		if a != erth && v != 0 {
			panic("fixture unbalanced")
		}
	}
	b.Balances = []orchard.Balance{{Asset: erth, Value: uint64(bal[erth])}}

	const msgType, chainID = "/earth.orchard.fixture", "earth-1"
	tx := orchard.TxFields{Memo: "orchard fixture", TimeoutHeight: 1_000_000, GasLimit: 3_000_000}
	sighash := orchard.Sighash(msgType, chainID, tx, []*orchard.Bundle{b})
	sig, err := orchard.SignBinding(orchard.BindingSigningKey(rcvs), sighash, bytes.NewReader(make([]byte, 32)))
	must(err)
	b.BindingSig = sig
	must(b.CheckBalance(sighash, orchard.CanonicalBase))

	bj := BundleJSON{MsgType: msgType, ChainID: chainID, Tx: TxJSON(tx), BindingSig: hex.EncodeToString(sig), Sighash: h(sighash)}
	for i, s := range ss {
		a := b.Actions[i]
		dir := filepath.Join(out, fmt.Sprintf("action_%d", i))
		must(os.MkdirAll(dir, 0o755))
		cvx, cvy := orchard.XY(a.Cv)
		var tb strings.Builder
		fmt.Fprintf(&tb, "# Generated by tools/orchardfixtures. Do not edit.\n")
		fmt.Fprintf(&tb, "nk = %s\ns_asset = %s\ns_value = \"%d\"\ns_rho = %s\ns_rcm = %s\ns_pos = \"%d\"\ns_path = %s\n",
			q(nk), q(s.sAsset), s.sValue, q(sp[i].rho), q(sp[i].rcm), sp[i].pos, paths[i])
		fmt.Fprintf(&tb, "o_asset = %s\no_value = \"%d\"\no_pc = %s\nrcv = %s\n", q(s.oAsset), s.oValue, q(outPC[i]), q(rcvs[i]))
		fmt.Fprintf(&tb, "anchor = %s\nnf = %s\ncm_out = %s\ncv_x = %s\ncv_y = %s\nsighash = %s\n",
			q(anchor), q(a.Nullifier), q(a.Commitment), q(cvx), q(cvy), q(sighash))
		must(os.WriteFile(filepath.Join(dir, "Prover.toml"), []byte(tb.String()), 0o644))
		var raw []byte
		for _, in := range b.PublicInputs(i, sighash) {
			raw = append(raw, in...)
		}
		must(os.WriteFile(filepath.Join(dir, "public_inputs.expected"), raw, 0o644))
		bj.Actions = append(bj.Actions, ActionJSON{
			Anchor:    h(a.Anchor),
			Nullifier: h(a.Nullifier), Commitment: h(a.Commitment),
			Cv: hex.EncodeToString(orchard.PointBytes(a.Cv)), Ciphertext: hex.EncodeToString(a.Ciphertext),
		})
	}
	for _, x := range b.Balances {
		bj.Balances = append(bj.Balances, BalanceJSON{Denom: "uerth", Asset: h(x.Asset), Value: x.Value})
	}
	js, err := json.MarshalIndent(bj, "", "  ")
	must(err)
	must(os.WriteFile(filepath.Join(out, "bundle.json"), js, 0o644))
}

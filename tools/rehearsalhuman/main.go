// Command rehearsalhuman gives an upgrade rehearsal's throwaway chain one
// registered human, and casts that human's YES on a proposal.
//
// Since x/assembly, every governance proposal also needs two thirds of the
// human votes cast, and a proposal with no human vote never passes. A
// rehearsal that only votes with its validator therefore fails; this is the
// human half of the vote, done the way a wallet does it: a real passport
// proof, real action proofs for the fee bundles and a real membership proof.
//
//	go run ./tools/rehearsalhuman prepare  -chain-id ID -genesis FILE -circuits DIR -work DIR
//	go run ./tools/rehearsalhuman register -chain-id ID -node URL -work DIR
//	go run ./tools/rehearsalhuman vote     -chain-id ID -node URL -work DIR -proposal N
//
// prepare (before the chain starts, from the repo root) proves a passport for
// x/personhood/testutil's registration A1, dated today and bound to ID; adds
// its freshly made CSCA to genesis pki.cscas; and puts two fee notes the tool
// owns into the shielded pool's genesis. It compiles the circuits once into
// DIR/circuits and checks their verifying keys against genesis, so the later
// steps only execute and prove. register and vote each spend one fee note,
// both proven against the genesis root (an anchor for root_window_seconds
// after it is superseded). Requires nargo and bb (v5.0.0) on PATH or in
// ~/.nargo/bin, ~/.bb.
package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"cosmossdk.io/log"
	"cosmossdk.io/math"
	rpchttp "github.com/cometbft/cometbft/rpc/client/http"
	dbm "github.com/cosmos/cosmos-db"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	"github.com/cosmos/cosmos-sdk/baseapp"
	"github.com/cosmos/cosmos-sdk/client/flags"
	addresscodec "github.com/cosmos/cosmos-sdk/codec/address"
	simtestutil "github.com/cosmos/cosmos-sdk/testutil/sims"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	gogoproto "github.com/cosmos/gogoproto/proto"

	"github.com/earth-network/earth/app"
	assemblytypes "github.com/earth-network/earth/x/assembly/types"
	personhoodtest "github.com/earth-network/earth/x/personhood/testutil"
	personhoodtypes "github.com/earth-network/earth/x/personhood/types"
	shieldedtest "github.com/earth-network/earth/x/shielded/testutil"
	shieldedtypes "github.com/earth-network/earth/x/shielded/types"
	"github.com/earth-network/earth/zk/merkle"
	"github.com/earth-network/earth/zk/privacy"
)

const (
	human   = "A1"
	variant = "lean_poa_p256_sha256"
	// noteValue is each genesis fee note: 10,000 ERTH.
	noteValue = 10_000_000_000
	// gasLimit and fee of every private tx here: the fee covers the node's
	// 0.005uerth floor at this limit, and the limit is within 5x what a
	// registration or a vote uses.
	gasLimit = 12_000_000
	fee      = 80_000
)

// state is what prepare leaves for register and vote.
type state struct {
	ChainID     string   `json:"chain_id"`
	Proof       string   `json:"proof"` // hex
	Signals     []string `json:"signals"`
	DscDer      string   `json:"dsc_der"` // base64
	Commitments []string `json:"commitments"`
	NotePos     []uint64 `json:"note_positions"`
	NullifierAt uint32   `json:"nullifier_index"`
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: rehearsalhuman <prepare|register|vote> [flags]")
		os.Exit(2)
	}
	fs := flag.NewFlagSet(os.Args[1], flag.ExitOnError)
	chainID := fs.String("chain-id", "", "chain id")
	genesis := fs.String("genesis", "", "genesis.json to patch (prepare)")
	circuits := fs.String("circuits", "", "the mobile repo's circuits workspace (prepare)")
	work := fs.String("work", "", "working directory")
	node := fs.String("node", "tcp://127.0.0.1:26657", "CometBFT RPC")
	proposal := fs.Uint64("proposal", 0, "proposal id (vote)")
	_ = fs.Parse(os.Args[2:])
	if *chainID == "" || *work == "" {
		fail(errors.New("-chain-id and -work are required"))
	}
	var err error
	switch os.Args[1] {
	case "prepare":
		err = prepare(*chainID, *genesis, *circuits, *work)
	case "register":
		err = register(*chainID, *node, *work)
	case "vote":
		err = vote(*chainID, *node, *work, *proposal)
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "rehearsalhuman:", err)
	os.Exit(1)
}

var ac = addresscodec.NewBech32Codec("earth")

// payer owns the genesis fee notes and their change.
var payer = personhoodtest.WalletNK("rehearsal-payer")

func feeNote(i int) personhoodtest.Note {
	return personhoodtest.Note{NK: payer, Denom: "uerth", Value: noteValue,
		Rho: personhoodtest.Det("rehearsal-fee/rho", uint64(i)), Rcm: personhoodtest.Det("rehearsal-fee/rcm", uint64(i))}
}

// registerMsg is A1's MsgRegister without its proof and fee.
func registerMsg() *personhoodtypes.MsgRegister {
	r := personhoodtest.Registrations[human]
	return &personhoodtypes.MsgRegister{
		SignatureAlgorithm: variant,
		Idc:                privacy.FieldBytes(r.IDC()),
		PcAnml:             privacy.FieldBytes(r.AnmlNote().PC()), CiphertextAnml: r.CiphertextAnml(),
		PcErth: privacy.FieldBytes(r.ErthPC()), CiphertextErth: r.CiphertextErth(),
		AffiliateHandle: r.ReferrerHandle,
	}
}

// ---- prepare ------------------------------------------------------------------

func prepare(chainID, genesisPath, circuits, work string) error {
	if genesisPath == "" || circuits == "" {
		return errors.New("prepare needs -genesis and -circuits")
	}
	if err := os.MkdirAll(work, 0o755); err != nil {
		return err
	}
	raw, err := os.ReadFile(genesisPath)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var g map[string]any
	if err := dec.Decode(&g); err != nil {
		return err
	}
	appState := g["app_state"].(map[string]any)

	// The verifying keys the chain will use, and the circuits compiled once.
	vks := map[string]string{
		"action":     get(appState, "shielded", "params", "verifying_keys", "action").(string),
		"membership": get(appState, "shielded", "params", "verifying_keys", "membership").(string),
		variant:      get(appState, "personhood", "params", "verifying_keys", variant).(string),
	}
	ws := filepath.Join(work, "circuits")
	_ = os.RemoveAll(ws)
	if _, err := run(".", "cp", "-R", circuits, ws); err != nil {
		return err
	}
	_ = os.RemoveAll(filepath.Join(ws, "target"))
	for c, b64 := range vks {
		vk, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return fmt.Errorf("%s verifying key: %w", c, err)
		}
		if err := os.WriteFile(filepath.Join(work, c+".vk"), vk, 0o644); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "compiling %s\n", c)
		if _, err := run(ws, "nargo", "compile", "--package", c, "--silence-warnings"); err != nil {
			return err
		}
		out := filepath.Join(work, "vk-"+c)
		if _, err := run(ws, "bb", "write_vk", "-b", "target/"+c+".json", "-o", out, "-t", "noir-recursive"); err != nil {
			return err
		}
		got, err := os.ReadFile(filepath.Join(out, "vk"))
		if err != nil {
			return err
		}
		if !bytes.Equal(got, vk) {
			return fmt.Errorf("the %s circuit under %s does not match genesis's verifying key", c, circuits)
		}
	}

	// The passport: A1's, proven today, bound to this chain.
	msg := registerMsg()
	binding, err := msg.Binding(ac, chainID)
	if err != nil {
		return err
	}
	date := time.Now().UTC().Format("060102")
	pass := filepath.Join(work, "passport")
	if _, err := run(".", "go", "run", "./tools/poafixtures", variant, pass,
		"address="+binding.String(), "doc="+personhoodtest.Registrations[human].Doc, "date="+date); err != nil {
		return err
	}
	toml, err := os.ReadFile(filepath.Join(pass, "Prover.toml"))
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "proving the passport")
	proof, pub, err := prove(work, variant, string(toml), nil)
	if err != nil {
		return err
	}
	var signals []string
	for i := 0; i+32 <= len(pub); i += 32 {
		signals = append(signals, new(big.Int).SetBytes(pub[i:i+32]).String())
	}
	csca, err := os.ReadFile(filepath.Join(pass, "csca.der"))
	if err != nil {
		return err
	}
	dsc, err := os.ReadFile(filepath.Join(pass, "dsc.der"))
	if err != nil {
		return err
	}

	// Its CSCA in the trust store.
	pki := appState["pki"].(map[string]any)
	cscas, _ := pki["cscas"].([]any)
	pki["cscas"] = append(cscas, map[string]any{"certificate_der": base64.StdEncoding.EncodeToString(csca)})

	// Two fee notes in the pool, with the coins behind them.
	sh := appState["shielded"].(map[string]any)
	cms, _ := sh["commitments"].([]any)
	st := state{ChainID: chainID, Proof: hex.EncodeToString(proof), Signals: signals,
		DscDer: base64.StdEncoding.EncodeToString(dsc)}
	for _, c := range cms {
		bz, err := base64.StdEncoding.DecodeString(c.(string))
		if err != nil {
			return err
		}
		st.Commitments = append(st.Commitments, hex.EncodeToString(bz))
	}
	for i := 0; i < 2; i++ {
		cm := privacy.FieldBytes(feeNote(i).CM())
		st.NotePos = append(st.NotePos, uint64(len(st.Commitments)))
		st.Commitments = append(st.Commitments, hex.EncodeToString(cm))
		cms = append(cms, base64.StdEncoding.EncodeToString(cm))
	}
	sh["commitments"] = cms
	added := math.NewInt(2 * noteValue)
	turn, _ := sh["turnstiles"].([]any)
	found := false
	for _, t := range turn {
		m := t.(map[string]any)
		if m["denom"] == "uerth" {
			in, _ := math.NewIntFromString(fmt.Sprint(m["in"]))
			m["in"] = in.Add(added).String()
			found = true
		}
	}
	if !found {
		turn = append(turn, map[string]any{"denom": "uerth", "in": added.String(), "out": "0"})
	}
	sh["turnstiles"] = turn
	if err := credit(appState["bank"].(map[string]any), authtypes.NewModuleAddress(shieldedtypes.ModuleName).String(), added); err != nil {
		return err
	}
	ni, err := json.Number(fmt.Sprint(get(appState, "personhood", "params", "nullifier_index"))).Int64()
	if err != nil {
		return err
	}
	st.NullifierAt = uint32(ni)

	out, err := json.MarshalIndent(g, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(genesisPath, out, 0o644); err != nil {
		return err
	}
	bz, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(work, "state.json"), bz, 0o644)
}

func get(m map[string]any, path ...string) any {
	var cur any = m
	for _, p := range path {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = mm[p]
	}
	return cur
}

// credit adds amount uerth to addr's genesis balance and to the supply.
func credit(bank map[string]any, addr string, amount math.Int) error {
	addCoin := func(coins []any) []any {
		for _, c := range coins {
			m := c.(map[string]any)
			if m["denom"] == "uerth" {
				a, _ := math.NewIntFromString(fmt.Sprint(m["amount"]))
				m["amount"] = a.Add(amount).String()
				return coins
			}
		}
		coins = append(coins, map[string]any{"denom": "uerth", "amount": amount.String()})
		sort.Slice(coins, func(i, j int) bool {
			return coins[i].(map[string]any)["denom"].(string) < coins[j].(map[string]any)["denom"].(string)
		})
		return coins
	}
	balances, _ := bank["balances"].([]any)
	found := false
	for _, b := range balances {
		m := b.(map[string]any)
		if m["address"] == addr {
			coins, _ := m["coins"].([]any)
			m["coins"] = addCoin(coins)
			found = true
		}
	}
	if !found {
		balances = append(balances, map[string]any{"address": addr, "coins": addCoin(nil)})
		sort.Slice(balances, func(i, j int) bool {
			return balances[i].(map[string]any)["address"].(string) < balances[j].(map[string]any)["address"].(string)
		})
	}
	bank["balances"] = balances
	supply, _ := bank["supply"].([]any)
	bank["supply"] = addCoin(supply)
	return nil
}

// ---- proving ------------------------------------------------------------------

// prove executes circuit on toml in work's compiled workspace and proves it
// against work/<circuit>.vk, returning the proof and bb's public inputs;
// with want set, they must equal it.
func prove(work, circuit, toml string, want []byte) ([]byte, []byte, error) {
	ws := filepath.Join(work, "circuits")
	if err := os.WriteFile(filepath.Join(ws, circuit, "Prover.toml"), []byte(toml), 0o644); err != nil {
		return nil, nil, err
	}
	if out, err := run(ws, "nargo", "execute", "--package", circuit, "--silence-warnings"); err != nil {
		return nil, nil, fmt.Errorf("%s refuses the witness: %s", circuit, out)
	}
	out := filepath.Join(work, "proof-"+circuit)
	_ = os.RemoveAll(out)
	vk, err := filepath.Abs(filepath.Join(work, circuit+".vk"))
	if err != nil {
		return nil, nil, err
	}
	if _, err := run(ws, "bb", "prove", "-b", "target/"+circuit+".json", "-w", "target/"+circuit+".gz",
		"-k", vk, "-o", out, "-t", "noir-recursive"); err != nil {
		return nil, nil, err
	}
	pub, err := os.ReadFile(filepath.Join(out, "public_inputs"))
	if err != nil {
		return nil, nil, err
	}
	if want != nil && !bytes.Equal(pub, want) {
		return nil, nil, fmt.Errorf("%s: bb's public inputs differ from the chain's", circuit)
	}
	proof, err := os.ReadFile(filepath.Join(out, "proof"))
	return proof, pub, err
}

func actionProver(work string) shieldedtest.ProveFunc {
	return func(toml string, pub [][]byte) ([]byte, error) {
		proof, _, err := prove(work, "action", toml, bytes.Join(pub, nil))
		return proof, err
	}
}

func run(dir, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	home, _ := os.UserHomeDir()
	cmd.Env = append(os.Environ(), "PATH="+filepath.Join(home, ".nargo/bin")+":"+filepath.Join(home, ".bb")+":"+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s %s: %w\n%s", name, strings.Join(args, " "), err, out)
	}
	return string(out), nil
}

// ---- register and vote --------------------------------------------------------

func loadState(work string) (state, error) {
	var st state
	bz, err := os.ReadFile(filepath.Join(work, "state.json"))
	if err != nil {
		return st, err
	}
	return st, json.Unmarshal(bz, &st)
}

// genesisTree is the pool's note tree at genesis: the root both fee bundles
// prove against.
func genesisTree(st state) (*merkle.Tree, error) {
	t := merkle.NewMem()
	for _, c := range st.Commitments {
		bz, err := hex.DecodeString(c)
		if err != nil {
			return nil, err
		}
		l, err := privacy.FieldFromBytes(bz)
		if err != nil {
			return nil, err
		}
		if _, err := t.Append(l); err != nil {
			return nil, err
		}
	}
	return t, nil
}

// feePlan spends fee note i, its change back to the payer.
func feePlan(st state, i int, seed string) (*shieldedtest.Plan, error) {
	tree, err := genesisTree(st)
	if err != nil {
		return nil, err
	}
	n := feeNote(i)
	change := personhoodtest.Note{NK: payer, Denom: "uerth", Value: noteValue - fee,
		Rho: personhoodtest.Det(seed+"/change-rho", 0), Rcm: personhoodtest.Det(seed+"/change-rcm", 0)}
	return shieldedtest.FeePlan(seed, tree, shieldedtest.PlanSpend{
		NK: n.NK, Denom: "uerth", Value: n.Value, Rho: n.Rho, Rcm: n.Rcm, Position: st.NotePos[i],
	}, fee, change.PC()), nil
}

var txFields = shieldedtypes.TxFields{GasLimit: gasLimit}

func register(chainID, node, work string) error {
	st, err := loadState(work)
	if err != nil {
		return err
	}
	msg := registerMsg()
	if msg.Proof, err = hex.DecodeString(st.Proof); err != nil {
		return err
	}
	msg.PublicSignals = st.Signals
	if msg.DscDer, err = base64.StdEncoding.DecodeString(st.DscDer); err != nil {
		return err
	}
	plan, err := feePlan(st, 0, "rehearsal/register")
	if err != nil {
		return err
	}
	if msg.Fee, err = plan.Unproven(); err != nil {
		return err
	}
	if err := shieldedtest.ProveMsg(msg, chainID, txFields, ac, []*shieldedtest.Plan{plan}, actionProver(work)); err != nil {
		return err
	}
	return broadcast(chainID, node, msg)
}

func vote(chainID, node, work string, proposal uint64) error {
	if proposal == 0 {
		return errors.New("vote needs -proposal")
	}
	st, err := loadState(work)
	if err != nil {
		return err
	}
	c, err := rpchttp.New(node, "/websocket")
	if err != nil {
		return err
	}
	var in assemblytypes.QueryBallotInputsResponse
	if err := query(c, "/earth.assembly.v1.Query/BallotInputs", &assemblytypes.QueryBallotInputsRequest{ProposalId: proposal}, &in); err != nil {
		return err
	}
	nfDec, ok := new(big.Int).SetString(st.Signals[st.NullifierAt], 10)
	if !ok {
		return errors.New("bad nullifier signal")
	}
	var nf [32]byte
	nfDec.FillBytes(nf[:])
	var reg personhoodtypes.QueryRegistrationResponse
	if err := query(c, "/earth.personhood.v1.Query/Registration", &personhoodtypes.QueryRegistrationRequest{Nullifier: hex.EncodeToString(nf[:])}, &reg); err != nil {
		return err
	}
	if !reg.Registered || reg.Expired {
		return errors.New("the rehearsal human is not registered (run register first)")
	}
	var leaves personhoodtypes.QueryIdentityLeavesResponse
	if err := query(c, "/earth.personhood.v1.Query/IdentityLeaves", &personhoodtypes.QueryIdentityLeavesRequest{Limit: 1000}, &leaves); err != nil {
		return err
	}
	tree := merkle.NewMem()
	for _, l := range leaves.Leaves {
		e, err := privacy.FieldFromBytes(l)
		if err != nil {
			return err
		}
		if _, err := tree.Append(e); err != nil {
			return err
		}
	}
	root, err := tree.Root()
	if err != nil {
		return err
	}
	var it personhoodtypes.QueryIdentityTreeResponse
	if err := query(c, "/earth.personhood.v1.Query/IdentityTree", &personhoodtypes.QueryIdentityTreeRequest{}, &it); err != nil {
		return err
	}
	if !bytes.Equal(it.LatestRoot, privacy.FieldBytes(root)) {
		return errors.New("the identity tree's latest root is not the leaves' root yet; retry after a block")
	}

	plan, err := feePlan(st, 1, fmt.Sprintf("rehearsal/vote/%d", proposal))
	if err != nil {
		return err
	}
	msg := &assemblytypes.MsgVoteProposal{ProposalId: proposal, Option: assemblytypes.VOTE_OPTION_YES}
	if msg.Fee, err = plan.Unproven(); err != nil {
		return err
	}
	signal, err := shieldedtypes.Sighash(msg, chainID, txFields, ac)
	if err != nil {
		return err
	}
	if err := shieldedtest.ProveMsg(msg, chainID, txFields, ac, []*shieldedtest.Plan{plan}, actionProver(work)); err != nil {
		return err
	}
	r := reg.Registration
	sib, err := tree.Path(r.LeafIndex)
	if err != nil {
		return err
	}
	field := func(b []byte) (fr.Element, error) {
		if len(b) == 0 {
			return fr.Element{}, nil
		}
		return privacy.FieldFromBytes(b)
	}
	dsc, err := field(r.DscKey)
	if err != nil {
		return err
	}
	scope, err := field(in.Scope)
	if err != nil {
		return err
	}
	exDsc, err := field(in.ExcludedDsc)
	if err != nil {
		return err
	}
	exCountry, err := field(in.ExcludedCountry)
	if err != nil {
		return err
	}
	w := personhoodtest.Membership{
		IDSecret: personhoodtest.Registrations[human].IDSecret(), DscKey: dsc, Country: privacy.CountryField(r.Country),
		ActivatedAt: uint64(r.ActivatedAt), PredecessorAt: uint64(r.PredecessorAt), LeafIndex: r.LeafIndex,
		Root: root, Siblings: sib, Scope: scope, Signal: signal, ExcludedDsc: exDsc, ExcludedCountry: exCountry,
		MaxActivation: in.MaxActivation, MaxPredecessor: in.MaxPredecessor,
	}
	toml, pub := w.Witness()
	var want []byte
	for _, e := range pub {
		want = append(want, privacy.FieldBytes(e)...)
	}
	proof, _, err := prove(work, "membership", toml, want)
	if err != nil {
		return err
	}
	msg.Membership = personhoodtypes.Membership{Proof: proof, Root: privacy.FieldBytes(root), Nullifier: privacy.FieldBytes(w.Nullifier())}
	return broadcast(chainID, node, msg)
}

func query(c *rpchttp.HTTP, path string, req, res gogoproto.Message) error {
	bz, err := gogoproto.Marshal(req)
	if err != nil {
		return err
	}
	out, err := c.ABCIQuery(context.Background(), path, bz)
	if err != nil {
		return err
	}
	if out.Response.Code != 0 {
		return fmt.Errorf("%s: %s", path, out.Response.Log)
	}
	return gogoproto.Unmarshal(out.Response.Value, res)
}

// broadcast encodes msg as the private tx it was proven for, sends it and
// waits for its block.
func broadcast(chainID, node string, msg shieldedtypes.PrivateMsg) error {
	home, err := os.MkdirTemp("", "rehearsalhuman-app")
	if err != nil {
		return err
	}
	defer os.RemoveAll(home)
	a := app.New(log.NewNopLogger(), dbm.NewMemDB(), nil, true, simtestutil.AppOptionsMap{flags.FlagHome: home},
		baseapp.SetChainID(chainID))
	b := a.TxConfig().NewTxBuilder()
	if err := b.SetMsgs(msg); err != nil {
		return err
	}
	b.SetGasLimit(txFields.GasLimit)
	b.SetFeeAmount(sdk.NewCoins(sdk.NewCoin("uerth", shieldedtypes.TotalFee(msg))))
	bz, err := a.TxConfig().TxEncoder()(b.GetTx())
	if err != nil {
		return err
	}
	c, err := rpchttp.New(node, "/websocket")
	if err != nil {
		return err
	}
	res, err := c.BroadcastTxSync(context.Background(), bz)
	if err != nil {
		return err
	}
	if res.Code != 0 {
		return fmt.Errorf("%s refused in CheckTx (code %d): %s", sdk.MsgTypeURL(msg), res.Code, res.Log)
	}
	for i := 0; i < 60; i++ {
		time.Sleep(time.Second)
		tx, err := c.Tx(context.Background(), res.Hash, false)
		if err != nil {
			continue
		}
		if tx.TxResult.Code != 0 {
			return fmt.Errorf("%s failed in block %d (code %d): %s", sdk.MsgTypeURL(msg), tx.Height, tx.TxResult.Code, tx.TxResult.Log)
		}
		fmt.Printf("%s landed in block %d (%X)\n", sdk.MsgTypeURL(msg), tx.Height, res.Hash)
		return nil
	}
	return fmt.Errorf("%s (%X) did not land within a minute", sdk.MsgTypeURL(msg), res.Hash)
}

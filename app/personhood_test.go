package app

// Private personhood on the real app: passport registrations bound to an
// identity commitment and notes, ANML claims, caretaker splits and assembly
// votes proven by membership in the identity tree, every one of them an
// unsigned private tx paying its fee from a shielded note.
//
// The chain here is deterministic (fixed genesis time, fixed keys, fixed
// block times), so every tree, time and signal is reproducible and the proofs
// in x/personhood/testdata/app replay. scripts/personhood-fixtures.sh reruns
// this test with EARTH_PROVE_CIRCUITS set, which proves each witness against
// the chain's real trees as the test reaches it.

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"cosmossdk.io/collections"
	"cosmossdk.io/log"
	"cosmossdk.io/math"
	abci "github.com/cometbft/cometbft/abci/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	dbm "github.com/cosmos/cosmos-db"
	"github.com/cosmos/cosmos-sdk/baseapp"
	"github.com/cosmos/cosmos-sdk/client/flags"
	simtestutil "github.com/cosmos/cosmos-sdk/testutil/sims"
	sdk "github.com/cosmos/cosmos-sdk/types"
	govv1 "github.com/cosmos/cosmos-sdk/x/gov/types/v1"
	"github.com/stretchr/testify/require"

	allocationtypes "github.com/earth-network/earth/x/allocation/types"
	assemblykeeper "github.com/earth-network/earth/x/assembly/keeper"
	assemblytypes "github.com/earth-network/earth/x/assembly/types"
	personhoodkeeper "github.com/earth-network/earth/x/personhood/keeper"
	personhoodtest "github.com/earth-network/earth/x/personhood/testutil"
	personhoodtypes "github.com/earth-network/earth/x/personhood/types"
	pkitypes "github.com/earth-network/earth/x/pki/types"
	shieldedtest "github.com/earth-network/earth/x/shielded/testutil"
	shieldedtypes "github.com/earth-network/earth/x/shielded/types"
	"github.com/earth-network/earth/zk/merkle"
	"github.com/earth-network/earth/zk/orchard"
	"github.com/earth-network/earth/zk/privacy"
)

const (
	phFee      = 80_000
	phGas      = 12_000_000
	phFeeNote  = 2_000_000
	phFeeNotes = 50
	phDay      = int64(86400)
	// phR is the test chain's caretaker_vote_seconds (lease length R).
	phR = int64(6 * 3600)
	// phH is the test chain's handle_lease_seconds.
	phH = int64(3 * 86400)
)

var (
	// phGenesis is inside the passport proofs' current_date skew (250101).
	phGenesis = time.Date(2025, 1, 1, 1, 0, 0, 0, time.UTC)
	phDay0    = phGenesis.Unix() / phDay
	passports = filepath.Join("..", "x", "personhood", "testdata", "passports")
)

type passport struct {
	proof   []byte
	signals []string
	dscDER  []byte
	csca    []byte
	nf      []byte
}

func loadPassport(t *testing.T, name string) passport {
	t.Helper()
	rd := func(f string) []byte {
		b, err := os.ReadFile(filepath.Join(passports, name, f))
		require.NoError(t, err)
		return b
	}
	p := passport{proof: rd("proof"), dscDER: rd("dsc.der"), csca: rd("csca.der")}
	pub := rd("public_inputs")
	for i := 0; i+32 <= len(pub); i += 32 {
		p.signals = append(p.signals, new(big.Int).SetBytes(pub[i:i+32]).String())
	}
	n, ok := new(big.Int).SetString(string(rd("expected_nullifier")), 10)
	require.True(t, ok)
	p.nf = make([]byte, 32)
	n.FillBytes(p.nf)
	return p
}

type phEnv struct {
	*shieldedEnv
	prover *personhoodtest.Prover
	// actions proves the fee bundles' actions (cached by public inputs).
	actions  *shieldedtest.Prover
	feeNotes []feeNote
	payer    fr.Element
}

type feeNote struct {
	note personhoodtest.Note
	pos  uint64
}

func initPersonhoodEnv(t *testing.T) *phEnv {
	t.Helper()
	prover := &personhoodtest.Prover{Dir: filepath.Join("..", "x", "personhood", "testdata", "app")}
	actions := shieldedtest.ForDir(t, filepath.Join("..", "x", "personhood", "testdata", "app", "actions"), "scripts/personhood-fixtures.sh")
	actionVK := actions.VerifyingKey(t)
	membershipVK, err := prover.VK("membership")
	require.NoError(t, err)
	moveVK, err := prover.VK("move")
	require.NoError(t, err)
	leanVK, err := os.ReadFile(filepath.Join(passports, "lean_poa_p256_sha256.vk"))
	require.NoError(t, err)

	se := initShieldedEnvWith(t, shieldedEnvOpts{
		genesisTime: phGenesis,
		keySeed:     "personhood",
		tweak: func(t *testing.T, app *App, st map[string]json.RawMessage) {
			cdc := app.AppCodec()
			var sh shieldedtypes.GenesisState
			require.NoError(t, cdc.UnmarshalJSON(st[shieldedtypes.ModuleName], &sh))
			sh.Params.VerifyingKeys = map[string][]byte{
				shieldedtypes.CircuitAction: actionVK, shieldedtypes.CircuitMembership: membershipVK,
				shieldedtypes.CircuitMove: moveVK,
			}
			st[shieldedtypes.ModuleName] = cdc.MustMarshalJSON(&sh)

			var ph personhoodtypes.GenesisState
			require.NoError(t, cdc.UnmarshalJSON(st[personhoodtypes.ModuleName], &ph))
			ph.Params.VerifyingKeys = map[string][]byte{"lean_poa_p256_sha256": leanVK}
			ph.Params.RegistrationValiditySeconds = 4 * 86400
			ph.Params.CaretakerVoteSeconds = uint64(phR)
			ph.Params.HandleLeaseSeconds = uint64(phH)
			st[personhoodtypes.ModuleName] = cdc.MustMarshalJSON(&ph)

			var pki pkitypes.GenesisState
			require.NoError(t, cdc.UnmarshalJSON(st[pkitypes.ModuleName], &pki))
			for _, name := range personhoodtest.RegistrationNames() {
				pki.Cscas = append(pki.Cscas, pkitypes.Csca{CertificateDer: loadPassport(t, name).csca})
			}
			st[pkitypes.ModuleName] = cdc.MustMarshalJSON(&pki)
		},
	})
	e := &phEnv{shieldedEnv: se, prover: prover, actions: actions, payer: personhoodtest.WalletNK("payer")}

	// Fee notes: phFeeNotes notes of phFeeNote uerth, owned by the payer.
	for batch := 0; batch < phFeeNotes/10; batch++ {
		var msgs []sdk.Msg
		var notes []personhoodtest.Note
		for i := 0; i < 10; i++ {
			j := uint64(batch*10 + i)
			n := personhoodtest.Note{NK: e.payer, Denom: "uerth", Value: phFeeNote,
				Rho: personhoodtest.Det("feenote/rho", j), Rcm: personhoodtest.Det("feenote/rcm", j)}
			notes = append(notes, n)
			msgs = append(msgs, &shieldedtypes.MsgShield{Sender: e.bech(e.userAddr()),
				Amount: sdk.NewInt64Coin("uerth", phFeeNote), Pc: privacy.FieldBytes(n.PC()),
				Ciphertext: shieldedtest.BlindCT(fmt.Sprintf("fee-note/%d", j))})
		}
		fb := e.finalize(e.signedTx(4_000_000, e.fee(20_000), msgs...))
		requireOK(t, fb.TxResults[0])
		evs := eventsOf(fb.TxResults[0].Events, shieldedtypes.EventTypeNote)
		require.Len(t, evs, 10)
		for i, ev := range evs {
			var pos uint64
			_, err := fmt.Sscan(ev["position"], &pos)
			require.NoError(t, err)
			e.feeNotes = append(e.feeNotes, feeNote{note: notes[i], pos: pos})
		}
	}
	return e
}

// at finalizes a block at time t.
func (e *phEnv) at(t time.Time, txs ...[]byte) *abci.ResponseFinalizeBlock {
	e.t.Helper()
	require.True(e.t, t.After(e.now), "blocks move forward")
	return e.finalizeAfter(t.Sub(e.now), txs...)
}

func (e *phEnv) noteTree() *merkle.Tree {
	e.t.Helper()
	k := e.app.ShieldedKeeper
	size, err := k.Size(e.ctx())
	require.NoError(e.t, err)
	t := merkle.NewMem()
	for i := uint64(0); i < size; i++ {
		cm, err := k.Commitment(e.ctx(), i)
		require.NoError(e.t, err)
		l, err := privacy.FieldFromBytes(cm)
		require.NoError(e.t, err)
		_, err = t.Append(l)
		require.NoError(e.t, err)
	}
	return t
}

func (e *phEnv) identityTree() *merkle.Tree {
	e.t.Helper()
	k := e.app.PersonhoodKeeper
	size, err := k.IdentityTreeSize(e.ctx())
	require.NoError(e.t, err)
	t := merkle.NewMem()
	for i := uint64(0); i < size; i++ {
		l, err := k.IdentityLeafAt(e.ctx(), i)
		require.NoError(e.t, err)
		_, err = t.Append(l)
		require.NoError(e.t, err)
	}
	return t
}

func ct(name string, i int) []byte {
	return shieldedtest.BlindCT(fmt.Sprintf("personhood-ct:%s:%d", name, i))
}

// feeFor plans the msg's fee bundle: the next fee note pays phFee, its
// change back to the payer, padded with a dummy action.
func (e *phEnv) feeFor(name string) *shieldedtest.Plan {
	e.t.Helper()
	require.NotEmpty(e.t, e.feeNotes, "out of fee notes")
	fn := e.feeNotes[0]
	e.feeNotes = e.feeNotes[1:]
	change := personhoodtest.Note{NK: e.payer, Denom: "uerth", Value: phFeeNote - phFee,
		Rho: personhoodtest.Det(name+"/change/rho", 0), Rcm: personhoodtest.Det(name+"/change/rcm", 0)}
	return shieldedtest.FeePlan(name, e.noteTree(), shieldedtest.PlanSpend{
		NK: fn.note.NK, Denom: "uerth", Value: fn.note.Value, Rho: fn.note.Rho, Rcm: fn.note.Rcm, Position: fn.pos,
	}, phFee, change.PC())
}

// member is the membership a msg proves: whose registration, and the
// statement (maxAct, maxPred: its max_activation and max_predecessor; set
// them to prove a statement the chain will not accept).
type member struct {
	reg             string
	scope           fr.Element
	excluded        fr.Element
	excludedCountry fr.Element
	maxAct          int64
	maxPred         int64
}

const noBound = personhoodtypes.NoBound

// prove fills msg's fee bundle proofs and binding signature (and its
// membership proof). The fee bundle must already be set on msg (unproven,
// from f) so the sighash can be computed; the membership proof binds that
// sighash as its signal.
func (e *phEnv) prove(name string, msg shieldedtypes.PrivateMsg, f *shieldedtest.Plan, m *member) {
	e.t.Helper()
	ac := e.app.AuthKeeper.AddressCodec()
	signal, err := shieldedtypes.Sighash(msg, shieldedtest.ChainID, phTx, ac)
	require.NoError(e.t, err)
	require.NoError(e.t, shieldedtest.ProveMsg(msg, shieldedtest.ChainID, phTx, ac, []*shieldedtest.Plan{f}, e.actions.TryProve), name)
	if m == nil {
		return
	}
	r := personhoodtest.Registrations[m.reg]
	reg, err := e.app.PersonhoodKeeper.Registrations.Get(e.ctx(), loadPassport(e.t, m.reg).nf)
	require.NoError(e.t, err, "registration %s", m.reg)
	tree := e.identityTree()
	sib, err := tree.Path(reg.LeafIndex)
	require.NoError(e.t, err)
	root, err := tree.Root()
	require.NoError(e.t, err)
	dsc, err := privacy.FieldFromBytes(reg.DscKey)
	require.NoError(e.t, err)
	w := personhoodtest.Membership{
		IDSecret: r.IDSecret(), DscKey: dsc, Country: privacy.CountryField(reg.Country), ActivatedAt: uint64(reg.ActivatedAt),
		PredecessorAt: uint64(reg.PredecessorAt),
		LeafIndex:     reg.LeafIndex, Root: root, Siblings: sib, Scope: m.scope, Signal: signal,
		ExcludedDsc: m.excluded, ExcludedCountry: m.excludedCountry,
		MaxActivation: personhoodtypes.BoundInput(m.maxAct), MaxPredecessor: personhoodtypes.BoundInput(m.maxPred),
	}
	mt, mpub := w.Witness()
	mproof, err := e.prover.Proof(name+".membership", "membership", mt, mpub)
	require.NoError(e.t, err, name)
	nf := w.Nullifier()
	mem := personhoodtypes.Membership{Proof: mproof, Root: privacy.FieldBytes(root), Nullifier: privacy.FieldBytes(nf)}
	switch x := msg.(type) {
	case *personhoodtypes.MsgClaimAnml:
		x.Membership = mem
	case *personhoodtypes.MsgSetCaretaker:
		x.Membership = mem
	case *personhoodtypes.MsgBindHandle:
		x.Membership = mem
	case *assemblytypes.MsgVoteProposal:
		x.Membership = mem
	case *assemblytypes.MsgProposeRemoval:
		x.Membership = mem
	case *assemblytypes.MsgVoteRemoval:
		x.Membership = mem
	default:
		e.t.Fatalf("no membership on %T", msg)
	}
}

// unverified is f's fee bundle with placeholder proofs and binding
// signature: well formed, for a msg the chain must refuse before verifying
// anything.
func (e *phEnv) unverified(f *shieldedtest.Plan) shieldedtypes.Bundle {
	e.t.Helper()
	b := e.bundle(f)
	for i := range b.Actions {
		b.Actions[i].Proof = make([]byte, shieldedtypes.ProofBytes) // never verified
	}
	b.BindingSig = make([]byte, orchard.BindingSigSize)
	return b
}

// stubFeeBundle is a well-formed, unproven two-action fee bundle paying fee.
func stubFeeBundle(fee uint64) shieldedtypes.Bundle {
	cv := orchard.PointBytes(orchard.ValueCommit(privacy.AssetID("uerth"), fee, privacy.AssetID("uerth"), 0, privacy.U64(7)))
	b := shieldedtypes.Bundle{Balances: []shieldedtypes.ValueBalance{{Denom: "uerth", Amount: fee}},
		BindingSig: make([]byte, orchard.BindingSigSize)}
	for i := range uint64(2) {
		b.Actions = append(b.Actions, shieldedtypes.Action{Anchor: make([]byte, 32),
			Nullifier: privacy.FieldBytes(privacy.U64(i + 1)), Commitment: make([]byte, 32), Cv: cv, Proof: make([]byte, shieldedtypes.ProofBytes),
			Ciphertext: shieldedtest.NoteCT(fmt.Sprintf("unverified/%d", i))})
	}
	return b
}

// bundle is f's unproven fee bundle, for the msg to carry while its sighash
// is computed.
func (e *phEnv) bundle(f *shieldedtest.Plan) shieldedtypes.Bundle {
	e.t.Helper()
	b, err := f.Unproven()
	require.NoError(e.t, err)
	return b
}

func (e *phEnv) tx(msg sdk.Msg) []byte { return e.privateTx(phGas, nil, msg) }

// phTx is the tx every private msg here is proven for (e.tx's: gas phGas).
var phTx = shieldedtypes.TxFields{GasLimit: phGas}

// register builds registration name's MsgRegister, fully proven.
func (e *phEnv) register(name string) *personhoodtypes.MsgRegister {
	e.t.Helper()
	r := personhoodtest.Registrations[name]
	p := loadPassport(e.t, name)
	f := e.feeFor("register/" + name)
	msg := &personhoodtypes.MsgRegister{
		Fee: e.bundle(f), Proof: p.proof, PublicSignals: p.signals, SignatureAlgorithm: "lean_poa_p256_sha256", DscDer: p.dscDER,
		Idc: privacy.FieldBytes(r.IDC()), PcAnml: privacy.FieldBytes(r.AnmlNote().PC()), CiphertextAnml: r.CiphertextAnml(),
		PcErth: privacy.FieldBytes(r.ErthPC()), CiphertextErth: r.CiphertextErth(),
	}
	if r.ReferrerHandle != "" {
		msg.AffiliateHandle = r.ReferrerHandle
	}
	e.prove("register/"+name, msg, f, nil)
	return msg
}

func claimPC(name string) personhoodtest.Note {
	return personhoodtest.Note{NK: personhoodtest.WalletNK("claims"), Denom: "uanml", Value: 1_000_000,
		Rho: personhoodtest.Det(name+"/rho", 0), Rcm: personhoodtest.Det(name+"/rcm", 0)}
}

// claim builds a claim for day by registration reg. maxAct < 0 proves the
// chain's own bound.
func (e *phEnv) claim(name, reg string, day int64, maxAct int64) *personhoodtypes.MsgClaimAnml {
	e.t.Helper()
	f := e.feeFor("claim/" + name)
	msg := &personhoodtypes.MsgClaimAnml{Fee: e.bundle(f), Day: uint64(day),
		Pc: privacy.FieldBytes(claimPC(name).PC()), Ciphertext: ct(name, 20)}
	if maxAct < 0 {
		maxAct = (day - 1) * phDay
	}
	e.prove("claim/"+name, msg, f, &member{reg: reg, scope: privacy.ClaimScope(uint64(day)), maxAct: maxAct, maxPred: noBound})
	return msg
}

// caretaker casts split as registration reg, proving max_predecessor maxPred.
func (e *phEnv) caretaker(name, reg string, maxPred int64, split []allocationtypes.AllocationWeight) *personhoodtypes.MsgSetCaretaker {
	e.t.Helper()
	f := e.feeFor("caretaker/" + name)
	msg := &personhoodtypes.MsgSetCaretaker{Fee: e.bundle(f), Percentages: split, MaxPredecessor: uint64(maxPred)}
	e.prove("caretaker/"+name, msg, f, &member{reg: reg, scope: privacy.CaretakerScope(), maxAct: noBound, maxPred: maxPred})
	return msg
}

// bindHandle binds handle to human's shielded address (or releases, handle
// "") as registration reg, proving max_predecessor maxPred.
func (e *phEnv) bindHandle(name, reg, handle, human string, maxPred int64) *personhoodtypes.MsgBindHandle {
	e.t.Helper()
	f := e.feeFor("handle/" + name)
	msg := &personhoodtypes.MsgBindHandle{Fee: e.bundle(f), MaxPredecessor: uint64(maxPred), Handle: handle}
	if handle != "" {
		msg.Address = personhoodtest.ShieldedAddress(human).Encode()
	}
	e.prove("handle/"+name, msg, f, &member{reg: reg, scope: privacy.HandleScope(), maxAct: noBound, maxPred: maxPred})
	return msg
}

// moveProof proves the move of what reg holds in scope to its successor to,
// for a msg with sighash signal.
func (e *phEnv) tryMoveProof(name, reg, to string, scope, signal fr.Element) (personhoodtypes.MoveProof, error) {
	e.t.Helper()
	from, succ := personhoodtest.Registrations[reg], personhoodtest.Registrations[to]
	next, err := e.app.PersonhoodKeeper.Registrations.Get(e.ctx(), loadPassport(e.t, to).nf)
	if err != nil {
		return personhoodtypes.MoveProof{}, err
	}
	tree := e.identityTree()
	root, err := tree.Root()
	require.NoError(e.t, err)
	// The succession from reg's identity to to's, or (none: a move the
	// circuit refuses) to's own leaf in its place.
	succIndex := next.LeafIndex
	oldIdc, newIdc := privacy.FieldBytes(from.IDC()), privacy.FieldBytes(succ.IDC())
	require.NoError(e.t, e.app.PersonhoodKeeper.Successions.Walk(e.ctx(), nil, func(i uint64, sc personhoodtypes.Succession) (bool, error) {
		if bytes.Equal(sc.IdcOld, oldIdc) && bytes.Equal(sc.IdcNew, newIdc) {
			succIndex = i
			return true, nil
		}
		return false, nil
	}))
	succSib, err := tree.Path(succIndex)
	require.NoError(e.t, err)
	sib, err := tree.Path(next.LeafIndex)
	require.NoError(e.t, err)
	dsc, err := privacy.FieldFromBytes(next.DscKey)
	require.NoError(e.t, err)
	w := personhoodtest.Move{
		OldSecret: from.IDSecret(), NewSecret: succ.IDSecret(), SuccessionIndex: succIndex, SuccessionSiblings: succSib,
		DscKey: dsc, Country: privacy.CountryField(next.Country), ActivatedAt: uint64(next.ActivatedAt),
		PredecessorAt: uint64(next.PredecessorAt), LeafIndex: next.LeafIndex, Siblings: sib,
		Root: root, Scope: scope, Signal: signal,
	}
	toml, pub := w.Witness()
	proof, err := e.prover.Proof(name+".move", "move", toml, pub)
	if err != nil {
		return personhoodtypes.MoveProof{}, err
	}
	return personhoodtypes.MoveProof{Proof: proof, Root: privacy.FieldBytes(root),
		OldNullifier: privacy.FieldBytes(w.OldNullifier()), NewNullifier: privacy.FieldBytes(w.NewNullifier())}, nil
}

// moveCaretaker moves reg's split to its successor to.
func (e *phEnv) moveCaretaker(name, reg, to string) *personhoodtypes.MsgMoveCaretaker {
	e.t.Helper()
	f := e.feeFor("move-caretaker/" + name)
	msg := &personhoodtypes.MsgMoveCaretaker{Fee: e.bundle(f)}
	e.proveMove("move-caretaker/"+name, msg, f, reg, to, privacy.CaretakerScope(), &msg.Move)
	return msg
}

// moveHandle moves reg's handle to its successor to.
func (e *phEnv) moveHandle(name, reg, handle, to string) *personhoodtypes.MsgMoveHandle {
	e.t.Helper()
	f := e.feeFor("move-handle/" + name)
	msg := &personhoodtypes.MsgMoveHandle{Fee: e.bundle(f), Handle: handle}
	e.proveMove("move-handle/"+name, msg, f, reg, to, privacy.HandleScope(), &msg.Move)
	return msg
}

// proveMove proves msg's fee bundle and its move proof (into *mv), both
// under msg's sighash.
func (e *phEnv) proveMove(name string, msg shieldedtypes.PrivateMsg, f *shieldedtest.Plan, reg, to string, scope fr.Element, mv *personhoodtypes.MoveProof) {
	e.t.Helper()
	ac := e.app.AuthKeeper.AddressCodec()
	signal, err := shieldedtypes.Sighash(msg, shieldedtest.ChainID, phTx, ac)
	require.NoError(e.t, err)
	require.NoError(e.t, shieldedtest.ProveMsg(msg, shieldedtest.ChainID, phTx, ac, []*shieldedtest.Plan{f}, e.actions.TryProve), name)
	*mv, err = e.tryMoveProof(name, reg, to, scope, signal)
	require.NoError(e.t, err, name)
}

// handle is the directory's entry for h.
func (e *phEnv) handle(h string) personhoodtypes.HandleEntry {
	e.t.Helper()
	res, err := personhoodkeeper.NewQueryServerImpl(e.app.PersonhoodKeeper).Handle(e.ctx(),
		&personhoodtypes.QueryHandleRequest{Handle: h})
	require.NoError(e.t, err)
	return res.Handle
}

func (e *phEnv) ballotInputs(req *assemblytypes.QueryBallotInputsRequest) *assemblytypes.QueryBallotInputsResponse {
	e.t.Helper()
	res, err := assemblykeeper.NewQueryServerImpl(e.app.AssemblyKeeper).BallotInputs(e.ctx(), req)
	require.NoError(e.t, err)
	return res
}

func field(t *testing.T, b []byte) fr.Element {
	t.Helper()
	f, err := privacy.FieldFromBytes(b)
	require.NoError(t, err)
	return f
}

func (e *phEnv) voteProposal(name, reg string, id uint64, opt assemblytypes.VoteOption) *assemblytypes.MsgVoteProposal {
	e.t.Helper()
	in := e.ballotInputs(&assemblytypes.QueryBallotInputsRequest{ProposalId: id})
	f := e.feeFor("vote/" + name)
	msg := &assemblytypes.MsgVoteProposal{Fee: e.bundle(f), ProposalId: id, Option: opt}
	e.prove("vote/"+name, msg, f, &member{reg: reg, scope: field(e.t, in.Scope), excluded: field(e.t, in.ExcludedDsc),
		excludedCountry: field(e.t, in.ExcludedCountry), maxAct: int64(in.MaxActivation), maxPred: int64(in.MaxPredecessor)})
	return msg
}

// voteProposalAs proves a vote on id against a statement of the caller's
// choosing rather than the chain's.
func (e *phEnv) voteProposalAs(name, reg string, id uint64, opt assemblytypes.VoteOption, m member) *assemblytypes.MsgVoteProposal {
	e.t.Helper()
	f := e.feeFor("vote/" + name)
	msg := &assemblytypes.MsgVoteProposal{Fee: e.bundle(f), ProposalId: id, Option: opt}
	m.reg = reg
	e.prove("vote/"+name, msg, f, &m)
	return msg
}

func (e *phEnv) proposeRemoval(name, reg string, option uint64) *assemblytypes.MsgProposeRemoval {
	e.t.Helper()
	f := e.feeFor("propose/" + name)
	msg := &assemblytypes.MsgProposeRemoval{Fee: e.bundle(f), OptionId: option}
	now := e.now.Unix()
	e.prove("propose/"+name, msg, f, &member{reg: reg,
		scope: privacy.ProposeRemovalScope(option, uint64(now/phDay)), maxAct: noBound,
		maxPred: now/phDay*phDay - personhoodtypes.ActivationMarginSeconds})
	return msg
}

func (e *phEnv) voteRemoval(name, reg string, option uint64, opt assemblytypes.VoteOption) *assemblytypes.MsgVoteRemoval {
	e.t.Helper()
	in := e.ballotInputs(&assemblytypes.QueryBallotInputsRequest{OptionId: option})
	f := e.feeFor("vote-removal/" + name)
	msg := &assemblytypes.MsgVoteRemoval{Fee: e.bundle(f), OptionId: option, Option: opt}
	e.prove("vote-removal/"+name, msg, f, &member{reg: reg, scope: field(e.t, in.Scope), maxAct: int64(in.MaxActivation), maxPred: int64(in.MaxPredecessor)})
	return msg
}

func (e *phEnv) mustDeliver(msg sdk.Msg) *abci.ResponseFinalizeBlock {
	e.t.Helper()
	bz := e.tx(msg)
	res := e.checkTx(bz)
	require.Equal(e.t, uint32(0), res.Code, res.Log)
	fb := e.finalize(bz)
	requireOK(e.t, fb.TxResults[0])
	return fb
}

func (e *phEnv) registration(name string) (personhoodtypes.Registration, bool) {
	e.t.Helper()
	reg, err := e.app.PersonhoodKeeper.Registrations.Get(e.ctx(), loadPassport(e.t, name).nf)
	if err != nil {
		return reg, false
	}
	return reg, true
}

func (e *phEnv) leaf(i uint64) *fr.Element {
	e.t.Helper()
	l, err := e.app.PersonhoodKeeper.IdentityLeafAt(e.ctx(), i)
	require.NoError(e.t, err)
	return &l
}

func (e *phEnv) caretakers() uint64 {
	e.t.Helper()
	res, err := personhoodkeeper.NewQueryServerImpl(e.app.PersonhoodKeeper).CaretakerVoterCount(e.ctx(), &personhoodtypes.QueryCaretakerVoterCountRequest{})
	require.NoError(e.t, err)
	return res.Count
}

func mintedNotes(r *abci.ExecTxResult) []map[string]string {
	return eventsOf(r.Events, shieldedtypes.EventTypeNote)
}

func hasCommitment(r *abci.ExecTxResult, cm fr.Element) bool {
	want := fmt.Sprintf("%x", privacy.FieldBytes(cm))
	for _, n := range mintedNotes(r) {
		if n["commitment"] == want {
			return true
		}
	}
	return false
}

// requireReferral checks the referral note a registration minted: the
// register event names the handle, its amount and position; the mint event at
// that position carries the handle owner's owner_pk and the opening
// privacy.ReferralOpening derives; and its commitment is in the tree.
func requireReferral(t *testing.T, r *abci.ExecTxResult, regEv map[string]string, handle string, reg personhoodtest.Registration) {
	t.Helper()
	require.Equal(t, handle, regEv["handle"])
	amt, ok := math.NewIntFromString(regEv["referral"])
	require.True(t, ok)
	require.True(t, amt.IsPositive())
	nfBytes, err := hex.DecodeString(regEv["nullifier"])
	require.NoError(t, err)
	nf, err := privacy.FieldFromBytes(nfBytes)
	require.NoError(t, err)
	leaf, err := strconv.ParseUint(regEv["leaf_index"], 10, 64)
	require.NoError(t, err)
	note := reg.ReferralNote(nf, leaf)
	note.Value = amt.Uint64()
	var mint map[string]string
	for _, m := range eventsOf(r.Events, "shielded_mint") {
		if m["position"] == regEv["referral_position"] {
			mint = m
		}
	}
	require.NotNil(t, mint, "the referral note's mint event")
	require.Equal(t, "", mint["ciphertext"])
	require.Equal(t, fmt.Sprintf("%x", privacy.FieldBytes(privacy.OwnerPK(personhoodtest.WalletNK(reg.Referrer)))), mint["owner_pk"])
	require.Equal(t, fmt.Sprintf("%x", privacy.FieldBytes(note.Rho)), mint["rho"])
	require.Equal(t, fmt.Sprintf("%x", privacy.FieldBytes(note.Rcm)), mint["rcm"])
	require.True(t, hasCommitment(r, privacy.CM(privacy.AssetID("uerth"), note.Value, note.PC())), "the referral note to the handle's owner")
}

func dayStart(d int64) time.Time { return time.Unix(d*phDay, 0).UTC() }

func TestPrivatePersonhood(t *testing.T) {
	e := initPersonhoodEnv(t)
	k := e.app.PersonhoodKeeper
	ctxNow := func() sdk.Context { return e.ctx() }

	// ---------------------------------------------------------------- register
	// A1: a new registration. The fee comes out of a shielded note; the leaf,
	// 1 ANML and the reward go to the identity and notes the proof is bound to.
	regA1 := e.register("A1")
	supplyBefore := e.app.BankKeeper.GetSupply(ctxNow(), "uerth").Amount
	fb := e.mustDeliver(regA1)
	r := fb.TxResults[0]
	a1, ok := e.registration("A1")
	require.True(t, ok)
	require.Equal(t, uint64(0), a1.LeafIndex)
	require.Equal(t, e.now.Unix(), a1.ActivatedAt)
	require.Equal(t, "UT", a1.Country, "the issuing CSCA's country")
	require.Zero(t, a1.PredecessorAt, "a passport never registered before has no predecessor")
	wantLeaf := privacy.IdentityLeaf(personhoodtest.Registrations["A1"].IDC(), field(t, a1.DscKey), privacy.CountryField("UT"), uint64(a1.ActivatedAt), 0)
	require.Equal(t, wantLeaf, *e.leaf(0))
	require.True(t, hasCommitment(r, personhoodtest.Registrations["A1"].AnmlNote().CM()), "1 ANML to pc_anml")
	regEv := eventsOf(r.Events, "register")[0]
	require.Equal(t, "false", regEv["switched"])
	reward, ok := math.NewIntFromString(regEv["reward"])
	require.True(t, ok)
	require.True(t, reward.IsPositive())
	require.True(t, hasCommitment(r, privacy.CM(privacy.AssetID("uerth"), reward.Uint64(), personhoodtest.Registrations["A1"].ErthPC())),
		"the reward as an ERTH note to pc_erth")
	// The fee reached fee_collector, and half of it was burned.
	require.Equal(t, fmt.Sprintf("%duerth", phFee/2), burned(fb))
	_ = supplyBefore
	require.NoError(t, e.app.ShieldedKeeper.AssertInvariants(ctxNow()))
	// Replaying the same tx: its fee notes are spent.
	res := e.checkTx(e.tx(regA1))
	require.Equal(t, shieldedtypes.ErrNullifierSpent.ABCICode(), res.Code, res.Log)

	// A registration read out of a block cannot be redirected: the passport
	// proof is bound to its idc and pcs. Refused in CheckTx, before either
	// proof is verified or any fee note spent.
	lifted := *regA1
	lifted.Fee = e.unverified(e.feeFor("lifted"))
	lifted.PcErth = privacy.FieldBytes(personhoodtest.Det("thief", 0))
	res = e.checkTx(e.tx(&lifted))
	require.Equal(t, personhoodtypes.ErrBadPublicInputs.ABCICode(), res.Code, res.Log)
	spent, err := e.app.ShieldedKeeper.Nullifiers.Has(ctxNow(), lifted.Fee.Actions[0].Nullifier)
	require.NoError(t, err)
	require.False(t, spent)
	// Replaying the registration under a fresh fee: its binding has landed
	// and is refused for reuse (the A -> B -> A replay, re-audit R1).
	refeed := *regA1
	refeed.Fee = lifted.Fee
	res = e.checkTx(e.tx(&refeed))
	require.Equal(t, personhoodtypes.ErrBindingUsed.ABCICode(), res.Code, res.Log)

	// B and C1.
	e.mustDeliver(e.register("B"))
	e.mustDeliver(e.register("C1"))
	cnt, err := k.RegCount.Get(ctxNow())
	require.NoError(t, err)
	require.Equal(t, uint64(3), cnt)

	// Nothing in state names an account.
	b, _ := e.registration("B")
	require.Equal(t, uint64(1), b.LeafIndex)

	// ---------------------------------------------------------------- assembly
	// A proposal enters voting a day and two hours after genesis; A and B were
	// activated before voting opened minus the activation margin (a day), so
	// both may vote.
	e.at(phGenesis.Add(24*time.Hour + 2*time.Hour + 10*time.Minute))
	prop, err := govv1.NewMsgSubmitProposal(nil, e.fee(1_000_000), e.bech(e.userAddr()), "private-personhood-test", "private personhood", "test", false)
	require.NoError(t, err)
	fb = e.finalize(e.signedTx(1_000_000, e.fee(10_000), prop))
	requireOK(t, fb.TxResults[0])
	const pid = uint64(1)
	in := e.ballotInputs(&assemblytypes.QueryBallotInputsRequest{ProposalId: pid})
	require.Equal(t, privacy.FieldBytes(privacy.ProposalScope(pid, 0)), in.Scope)
	require.Equal(t, uint64(noBound), in.MaxActivation, "ballots bound the predecessor, not the activation")
	require.Equal(t, uint64(e.now.Unix()-personhoodtypes.ActivationMarginSeconds), in.MaxPredecessor)

	e.at(phGenesis.Add(24*time.Hour + 3*time.Hour + 20*time.Minute))
	e.mustDeliver(e.voteProposal("A-yes", "A1", pid, assemblytypes.VOTE_OPTION_YES))
	aq := assemblykeeper.NewQueryServerImpl(e.app.AssemblyKeeper)
	tallyOf := func() assemblytypes.Tally {
		res, err := aq.ProposalTally(ctxNow(), &assemblytypes.QueryProposalTallyRequest{ProposalId: pid})
		require.NoError(t, err)
		return res.Tally
	}
	require.Equal(t, assemblytypes.Tally{Yes: 1}, tallyOf())
	// The same person again: same nullifier, the vote is replaced.
	e.mustDeliver(e.voteProposal("A-no", "A1", pid, assemblytypes.VOTE_OPTION_NO))
	require.Equal(t, assemblytypes.Tally{No: 1}, tallyOf())
	e.mustDeliver(e.voteProposal("B-yes", "B", pid, assemblytypes.VOTE_OPTION_YES))
	require.Equal(t, assemblytypes.Tally{Yes: 1, No: 1}, tallyOf())

	// A proposal revoking two Document Signers of one country (B's and C1's,
	// both under a "UT" CSCA) excludes that whole country: its subjects are
	// fixed as it enters voting, from x/pki's view of each signer's issuer.
	country, placed, err := e.app.PkiKeeper.DscIssuerCountry(ctxNow(), loadPassport(t, "B").dscDER)
	require.NoError(t, err)
	require.True(t, placed)
	require.Equal(t, "UT", country)
	var revokes []sdk.Msg
	for _, name := range []string{"B", "C1"} {
		revokes = append(revokes, &pkitypes.MsgRevokeDsc{Authority: e.bech(e.app.GovKeeper.GetGovernanceAccount(ctxNow()).GetAddress()),
			CertificateDer: loadPassport(t, name).dscDER})
	}
	prop2, err := govv1.NewMsgSubmitProposal(revokes, e.fee(1_000_000), e.bech(e.userAddr()), "", "revoke UT signers", "test", false)
	require.NoError(t, err)
	fb = e.finalize(e.signedTx(2_000_000, e.fee(20_000), prop2))
	requireOK(t, fb.TxResults[0])
	const pid2 = uint64(2)
	in2 := e.ballotInputs(&assemblytypes.QueryBallotInputsRequest{ProposalId: pid2})
	require.Equal(t, privacy.FieldBytes(privacy.CountryField("UT")), in2.ExcludedCountry)
	require.Equal(t, make([]byte, 32), in2.ExcludedDsc)
	// A1 is a UT registration too: no proof of its leaf satisfies the
	// statement, and one made as if nothing were excluded is refused.
	res = e.checkTx(e.tx(e.voteProposalAs("A-ut", "A1", pid2, assemblytypes.VOTE_OPTION_NO,
		member{scope: field(t, in2.Scope), maxAct: int64(in2.MaxActivation), maxPred: int64(in2.MaxPredecessor)})))
	require.Equal(t, personhoodtypes.ErrInvalidMembership.ABCICode(), res.Code, res.Log)

	// ------------------------------------------------------- DSC revocation
	// B proves membership against today's root, then B's Document Signer is
	// revoked: the purge zeroes B's leaf. Within the root window the old root
	// still anchors; after it, B's proof is refused.
	e.at(phGenesis.Add(24*time.Hour + 3*time.Hour + 40*time.Minute))
	stale := e.voteProposal("B-stale", "B", pid, assemblytypes.VOTE_OPTION_NO)
	pk, err := e.app.PkiKeeper.VerifyDsc(ctxNow(), loadPassport(t, "B").dscDER)
	require.NoError(t, err)
	require.NoError(t, e.app.PkiKeeper.RevokeDsc(ctxNow(), pk))
	e.finalize() // BeginBlock purges
	_, ok = e.registration("B")
	require.False(t, ok, "B's registration was purged")
	require.True(t, e.leaf(1).IsZero(), "and its leaf zeroed")
	cnt, _ = k.RegCount.Get(ctxNow())
	require.Equal(t, uint64(2), cnt)
	e.at(e.now.Add(61 * time.Minute))
	res = e.checkTx(e.tx(stale))
	require.Equal(t, personhoodtypes.ErrUnknownIdentityRoot.ABCICode(), res.Code, res.Log)

	// ---------------------------------------------------------------- caretaker
	// A1 is a fresh registrant (predecessor_at 0): it casts a split at once,
	// proving max_predecessor 0, under its caretaker nullifier.
	split := []allocationtypes.AllocationWeight{{OptionId: allocationtypes.RegistrationRewardOptionID, Percent: 100}}
	ok2 := e.caretaker("A", "A1", 0, split)
	fb = e.mustDeliver(ok2)
	require.Equal(t, uint64(1), e.caretakers())
	voter, err := e.app.AllocationKeeper.Voters.Get(ctxNow(),
		collections.Join(uint32(allocationtypes.STREAM_ID_CARETAKER), ok2.Membership.Nullifier))
	require.NoError(t, err, "the split is filed under the caretaker nullifier")
	require.Equal(t, int64(personhoodtypes.VoterWeight), voter.Weight.Int64())
	caretakerExpiry := e.now.Unix() + phR

	// A1 claims the handle "alice" (its shielded address) at once too. C1
	// cannot take a live handle A holds. The handle and address are bound by
	// the sighash: a relayer cannot swap them.
	alice := e.bindHandle("A1", "A1", "alice", "A", 0)
	for _, mutate := range []func(*personhoodtypes.MsgBindHandle){
		func(m *personhoodtypes.MsgBindHandle) { m.Handle = "mallory" },
		func(m *personhoodtypes.MsgBindHandle) { m.Address = personhoodtest.ShieldedAddress("M").Encode() },
	} {
		bad := *alice
		mutate(&bad)
		res = e.checkTx(e.tx(&bad))
		require.NotEqual(t, uint32(0), res.Code, "a relayer swapped the handle or address")
	}
	fb = e.mustDeliver(alice)
	require.Equal(t, "live", e.handle("alice").Status)
	require.Equal(t, personhoodtest.ShieldedAddress("A").Encode(), e.handle("alice").Address)
	// owner: the handle-scope nullifier holding it, the bind's own (public)
	// membership nullifier, in the query and the event.
	require.Equal(t, hex.EncodeToString(alice.Membership.Nullifier), e.handle("alice").Owner)
	require.Equal(t, hex.EncodeToString(alice.Membership.Nullifier), eventsOf(fb.TxResults[0].Events, "handle_bound")[0]["owner"])
	res = e.checkTx(e.tx(e.bindHandle("C1-taken", "C1", "alice", "C", 0)))
	require.Equal(t, personhoodtypes.ErrHandleTaken.ABCICode(), res.Code, res.Log)

	// The caretaker split lapses R after it was cast and is swept.
	e.at(time.Unix(caretakerExpiry+10, 0).UTC())
	require.Equal(t, uint64(0), e.caretakers())
	_, err = e.app.AllocationKeeper.Voters.Get(ctxNow(),
		collections.Join(uint32(allocationtypes.STREAM_ID_CARETAKER), ok2.Membership.Nullifier))
	require.Error(t, err, "cleared from the stream")

	// ---------------------------------------------------------------- claims
	// Day 2: A claims. A second claim the same day (same nullifier) is refused.
	d2 := phDay0 + 2
	e.at(dayStart(d2).Add(30 * time.Minute))
	c := e.claim("A-d2", "A1", d2, -1)
	fb = e.mustDeliver(c)
	require.True(t, hasCommitment(fb.TxResults[0], claimPC("A-d2").CM()), "1 ANML to the claim's pc")
	res = e.checkTx(e.tx(e.claim("A-d2-again", "A1", d2, -1)))
	require.Equal(t, personhoodtypes.ErrClaimTooSoon.ABCICode(), res.Code, res.Log)
	// A claim for another day is refused before any proof is checked.
	wrong := *c
	wrong.Day = uint64(d2 + 1)
	wrong.Fee = e.unverified(e.feeFor("wrong-day"))
	res = e.checkTx(e.tx(&wrong))
	require.Equal(t, personhoodtypes.ErrWrongDay.ABCICode(), res.Code, res.Log)

	// A removal ballot on groundworks option 1, opened and voted anonymously
	// (opening needs an identity activated the activation margin, a day,
	// before today began).
	e.at(dayStart(d2).Add(35 * time.Minute))
	e.mustDeliver(e.proposeRemoval("A", "A1", 1))
	e.mustDeliver(e.voteRemoval("A", "A1", 1, assemblytypes.VOTE_OPTION_YES))
	ballots, err := aq.RemovalBallots(ctxNow(), &assemblytypes.QueryRemovalBallotsRequest{})
	require.NoError(t, err)
	require.Len(t, ballots.Ballots, 1)
	require.Equal(t, assemblytypes.Tally{Yes: 1}, ballots.Ballots[0].Tally)
	// A1 proves tomorrow's claim now, before switching away.
	e.at(dayStart(d2).Add(40 * time.Minute))
	a1Tomorrow := e.claim("A1-d3", "A1", d2+1, -1)
	// Naming a handle nobody holds ("amy", before A1 takes it below) is
	// refused before the passport proof.
	res = e.checkTx(e.tx(e.register("D1")))
	require.Equal(t, personhoodtypes.ErrNoReferrer.ABCICode(), res.Code, res.Log)

	// --------------------------------------------------------- before a switch
	// A1's last split lapsed, so it casts one again (no wait: no
	// predecessor), and changes "alice" to "amy" (a live holder: unbounded;
	// the old handle is freed at once). After the switch both move to A2 with
	// move proofs (circuits/move).
	e.at(dayStart(d2).Add(45 * time.Minute))
	e.mustDeliver(e.caretaker("A-again", "A1", 0, split))
	e.mustDeliver(e.bindHandle("A1-amy", "A1", "amy", "A", noBound))
	require.Equal(t, "free", e.handle("alice").Status)
	amyOwner := e.handle("amy").Owner
	// What A1 would send after moving both away (proven now, while its leaf
	// is live; refused below once it has moved them).
	a1Again := e.caretaker("A1-again", "A1", 0, split)
	a1Claim := e.bindHandle("A1-again", "A1", "alice-2", "A", 0)

	// ---------------------------------------------------------------- switch
	// A re-registers the same passport under a new identity secret: the old
	// leaf is zeroed, the new one appended, nothing is paid or rate-counted.
	e.at(dayStart(d2).Add(time.Hour))
	fb = e.mustDeliver(e.register("A2"))
	regEv = eventsOf(fb.TxResults[0].Events, "register")[0]
	require.Equal(t, "true", regEv["switched"])
	require.Equal(t, "0", regEv["reward"])
	require.Len(t, mintedNotes(fb.TxResults[0]), 2, "only the fee bundle's outputs")
	require.True(t, e.leaf(0).IsZero())
	a2, ok := e.registration("A2")
	require.True(t, ok)
	require.Equal(t, e.now.Unix(), a2.ActivatedAt)
	require.Equal(t, e.now.Unix(), a2.PredecessorAt, "a switch: the leaf records it")

	// A2 holds no split and no handle, and its predecessor (A1) does: it
	// may cast a split or claim a handle only once everything A1 could hold
	// has lapsed (a statement over its own predecessor_at is refused). One
	// split and one live handle per passport. A1's split still counts and
	// "amy" still resolves to A1's nullifier. A2 may not vote on the
	// proposal A1 voted on: its predecessor is after the ballot opened (an
	// honest statement fails; the chain's bound is refused).
	e.at(e.now.Add(30 * time.Minute))
	res = e.checkTx(e.tx(e.caretaker("A2-early", "A2", a2.PredecessorAt, split)))
	require.Equal(t, personhoodtypes.ErrInvalidMsg.ABCICode(), res.Code, res.Log)
	res = e.checkTx(e.tx(e.bindHandle("A2-early", "A2", "a-two", "A", a2.PredecessorAt)))
	require.Equal(t, personhoodtypes.ErrInvalidMsg.ABCICode(), res.Code, res.Log)
	require.Equal(t, uint64(1), e.caretakers())
	require.Equal(t, "live", e.handle("amy").Status)
	require.Equal(t, amyOwner, e.handle("amy").Owner)
	inA2 := e.ballotInputs(&assemblytypes.QueryBallotInputsRequest{ProposalId: pid})
	res = e.checkTx(e.tx(e.voteProposalAs("A2-dbl", "A2", pid, assemblytypes.VOTE_OPTION_YES,
		member{scope: field(t, inA2.Scope), maxAct: noBound, maxPred: a2.PredecessorAt})))
	require.Equal(t, personhoodtypes.ErrInvalidMembership.ABCICode(), res.Code, res.Log)
	cnt, _ = k.RegCount.Get(ctxNow())
	require.Equal(t, uint64(2), cnt)

	// ---------------------------------------------------------------- moves
	// The switch appended the succession (A1, A2) to the identity tree. A
	// move proof shows the prover knows both secrets, that A2 succeeded A1
	// under the same passport and is live: the split and "amy" pass to A2,
	// with no wait, and A1 may never cast or claim again.
	mc := e.moveCaretaker("A1-A2", "A1", "A2")
	fb = e.mustDeliver(mc)
	require.Equal(t, uint64(1), e.caretakers(), "moved, not added")
	require.Equal(t, hex.EncodeToString(mc.Move.NewNullifier), eventsOf(fb.TxResults[0].Events, "move_caretaker")[0]["nullifier"])
	// Replayed: refused (its fee note is spent and A1 holds no split).
	res = e.checkTx(e.tx(mc))
	require.NotEqual(t, uint32(0), res.Code, "a replayed move")
	mv := e.moveHandle("A1-A2", "A1", "amy", "A2")
	// The proof binds its msg's sighash: carried by another msg (a fee
	// bundle proven for that msg) it does not verify.
	f := e.feeFor("move-handle/rebound")
	rebound := &personhoodtypes.MsgMoveHandle{Fee: e.bundle(f), Handle: "amy"}
	require.NoError(t, shieldedtest.ProveMsg(rebound, shieldedtest.ChainID, phTx, e.app.AuthKeeper.AddressCodec(),
		[]*shieldedtest.Plan{f}, e.actions.TryProve))
	rebound.Move = mv.Move
	res = e.checkTx(e.tx(rebound))
	require.Equal(t, personhoodtypes.ErrInvalidMove.ABCICode(), res.Code, res.Log)
	// Nor does the split's move proof (another scope) move the handle.
	rebound.Move = mc.Move
	res = e.checkTx(e.tx(rebound))
	require.NotEqual(t, uint32(0), res.Code, res.Log)
	fb = e.mustDeliver(mv)
	require.Equal(t, "live", e.handle("amy").Status)
	require.Equal(t, hex.EncodeToString(mv.Move.NewNullifier), e.handle("amy").Owner, "the successor's nullifier")
	movedEv := eventsOf(fb.TxResults[0].Events, "handle_moved")[0]
	require.Equal(t, hex.EncodeToString(mv.Move.NewNullifier), movedEv["owner"])
	require.Equal(t, hex.EncodeToString(mv.Move.OldNullifier), movedEv["previous_owner"])
	// (Refused: moved out, and its pre-switch root has aged out too; the
	// moved-out rule alone is TestCaretakerPredecessorBound's and
	// TestHandlePredecessorAndMove's.)
	res = e.checkTx(e.tx(a1Again))
	require.NotEqual(t, uint32(0), res.Code, res.Log)
	res = e.checkTx(e.tx(a1Claim))
	require.NotEqual(t, uint32(0), res.Code, res.Log)
	// No move to another passport's identity: there is no succession (A1,
	// C1), so no witness exists (the circuit refuses it).
	_, err = e.tryMoveProof("A1-C1", "A1", "C1", privacy.HandleScope(), fr.Element{})
	require.Error(t, err, "a move to another passport")
	// A2 holds the moved split and handle and refreshes both with no wait
	// (any max_predecessor: it creates neither).
	e.at(e.now.Add(time.Minute))
	e.mustDeliver(e.caretaker("A2", "A2", noBound, split))
	require.Equal(t, uint64(1), e.caretakers())
	e.mustDeliver(e.bindHandle("A2-renew", "A2", "amy", "A", noBound))

	// Day 3: A1's proof, made against a root from before the switch, is past
	// its window. A2 cannot claim yet (activated yesterday): a proof over its
	// own activated_at is refused.
	d3 := phDay0 + 3
	e.at(dayStart(d3).Add(30 * time.Minute))
	res = e.checkTx(e.tx(a1Tomorrow))
	require.Equal(t, personhoodtypes.ErrUnknownIdentityRoot.ABCICode(), res.Code, res.Log)
	res = e.checkTx(e.tx(e.claim("A2-d3", "A2", d3, a2.ActivatedAt)))
	require.Equal(t, personhoodtypes.ErrInvalidMembership.ABCICode(), res.Code, res.Log)

	// Day 4: A2 claims.
	d4 := phDay0 + 4
	e.at(dayStart(d4).Add(30 * time.Minute))
	e.mustDeliver(e.claim("A2-d4", "A2", d4, -1))

	// ---------------------------------------------------------------- expiry
	// C1 (and the purged B) were registered four days ago: C1 lapses and its
	// leaf is zeroed. C re-enters with a new identity and is paid as new,
	// referred by A.
	e.at(dayStart(d4).Add(2 * time.Hour))
	c1Index := uint64(2)
	_, ok = e.registration("C1")
	require.False(t, ok)
	require.True(t, e.leaf(c1Index).IsZero())
	// C2 names "amy" (still A1's, claimed before the switch): the chain mints the referrer's half as a note to the
	// handle's registered address (A's), with an opening it derives from the
	// passport nullifier and leaf index and publishes on the mint event; the
	// registrant's half is C2's own note. The passport binding commits to the
	// handle: a relayer cannot swap it, and the registrant has no say in
	// where the referral note goes (audit 5 P1).
	regC2 := e.register("C2")
	bad := *regC2
	bad.AffiliateHandle = "alice"
	res = e.checkTx(e.tx(&bad))
	require.NotEqual(t, uint32(0), res.Code, "a relayer swapped the affiliate")
	fb = e.mustDeliver(regC2)
	regEv = eventsOf(fb.TxResults[0].Events, "register")[0]
	require.Equal(t, "false", regEv["switched"])
	rewardC2, ok := math.NewIntFromString(regEv["reward"])
	require.True(t, ok)
	require.True(t, rewardC2.IsPositive())
	requireReferral(t, fb.TxResults[0], regEv, "amy", personhoodtest.Registrations["C2"])
	// C2 is a re-entry (C1 lapsed): its leaf has a predecessor, so it waits
	// before creating a split or claiming a handle (anything C1 held may
	// still be live); a statement over its own predecessor_at is refused.
	c2r, ok := e.registration("C2")
	require.True(t, ok)
	require.Equal(t, e.now.Unix(), c2r.PredecessorAt)
	res = e.checkTx(e.tx(e.caretaker("C2-early", "C2", c2r.PredecessorAt, split)))
	require.Equal(t, personhoodtypes.ErrInvalidMsg.ABCICode(), res.Code, res.Log)
	res = e.checkTx(e.tx(e.bindHandle("C2-early", "C2", "cee", "C", c2r.PredecessorAt)))
	require.Equal(t, personhoodtypes.ErrInvalidMsg.ABCICode(), res.Code, res.Log)
	require.True(t, hasCommitment(fb.TxResults[0], privacy.CM(privacy.AssetID("uerth"), rewardC2.Uint64(), personhoodtest.Registrations["C2"].ErthPC())))
	// D1 names "amy" too.
	fb = e.mustDeliver(e.register("D1"))
	regEv = eventsOf(fb.TxResults[0].Events, "register")[0]
	rewardD1, ok := math.NewIntFromString(regEv["reward"])
	require.True(t, ok && rewardD1.IsPositive())
	requireReferral(t, fb.TxResults[0], regEv, "amy", personhoodtest.Registrations["D1"])

	// D1, a fresh registrant, claims "dee" at once. Rebinding the same
	// nullifier may change the address and keep the handle. A change to
	// another handle frees the old one at once.
	e.at(e.now.Add(time.Minute))
	e.mustDeliver(e.bindHandle("D1-dee", "D1", "dee", "D", 0))
	e.mustDeliver(e.bindHandle("D1-addr", "D1", "dee", "D-alt", noBound))
	require.Equal(t, personhoodtest.ShieldedAddress("D-alt").Encode(), e.handle("dee").Address, "the handle follows the address")
	e.mustDeliver(e.bindHandle("D1-change", "D1", "dee-2", "D-alt", noBound))
	require.Equal(t, "free", e.handle("dee").Status)
	require.Equal(t, "live", e.handle("dee-2").Status)
	cnt, _ = k.RegCount.Get(ctxNow())
	require.Equal(t, uint64(3), cnt)
	dir, err := personhoodkeeper.NewQueryServerImpl(k).Handles(ctxNow(), &personhoodtypes.QueryHandlesRequest{})
	require.NoError(t, err)
	require.Len(t, dir.Handles, 2) // amy (A1), dee-2 (D1)

	require.NoError(t, e.app.ShieldedKeeper.AssertInvariants(ctxNow()))

	// ------------------------------------------------------ genesis round trip
	exported, err := e.app.ExportAppStateAndValidators(false, nil, nil)
	require.NoError(t, err)
	var appState map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(exported.AppState, &appState))
	fresh := New(log.NewNopLogger(), dbm.NewMemDB(), nil, true, simtestutil.AppOptionsMap{flags.FlagHome: t.TempDir()},
		baseapp.SetChainID(shieldedtest.ChainID))
	fctx := fresh.NewUncachedContext(false, cmtproto.Header{ChainID: shieldedtest.ChainID, Height: e.height, Time: e.now})
	_, err = fresh.ModuleManager.InitGenesis(fctx, fresh.AppCodec(), appState)
	require.NoError(t, err)
	ph1, err := k.ExportGenesis(ctxNow())
	require.NoError(t, err)
	ph2, err := fresh.PersonhoodKeeper.ExportGenesis(fctx)
	require.NoError(t, err)
	require.Equal(t, ph1, ph2)
	r1, _ := k.CurrentIdentityRoot(ctxNow())
	r2, _ := fresh.PersonhoodKeeper.CurrentIdentityRoot(fctx)
	require.Equal(t, r1, r2)
	as1, err := e.app.AssemblyKeeper.ExportGenesis(ctxNow())
	require.NoError(t, err)
	as2, err := fresh.AssemblyKeeper.ExportGenesis(fctx)
	require.NoError(t, err)
	// A proposal's ballot is re-created on import under a fresh id (its scope
	// does not depend on it); removal ballots keep theirs.
	require.Greater(t, as2.BallotSeq, as1.BallotSeq)
	as2.BallotSeq = as1.BallotSeq
	require.Equal(t, as1, as2)
}

// A private personhood or assembly msg reaching its handler any way but the
// private ante (a contract's CosmosMsg::Any, an ICA host tx) is refused, and a
// signed tx cannot carry one.
func TestPrivatePersonhoodBypassRefused(t *testing.T) {
	e := initShieldedEnv(t)
	tr := stubFeeBundle(1000)
	mem := personhoodtypes.Membership{Proof: make([]byte, shieldedtypes.ProofBytes), Root: make([]byte, 32), Nullifier: make([]byte, 32)}
	for _, msg := range []sdk.Msg{
		&personhoodtypes.MsgClaimAnml{Fee: tr, Membership: mem, Pc: make([]byte, 32), Ciphertext: shieldedtest.BlindCT("c")},
		&personhoodtypes.MsgSetCaretaker{Fee: tr, Membership: mem},
		&personhoodtypes.MsgBindHandle{Fee: tr, Membership: mem},
		&personhoodtypes.MsgRegister{Fee: tr, Proof: make([]byte, shieldedtypes.ProofBytes), PublicSignals: []string{"1"}, SignatureAlgorithm: "lean_poa_p256_sha256",
			Idc: make([]byte, 32), PcAnml: make([]byte, 32), PcErth: make([]byte, 32),
			CiphertextAnml: shieldedtest.BlindCT("a"), CiphertextErth: shieldedtest.BlindCT("e")},
		&assemblytypes.MsgVoteProposal{Fee: tr, Membership: mem, ProposalId: 1, Option: assemblytypes.VOTE_OPTION_YES},
		&assemblytypes.MsgProposeRemoval{Fee: tr, Membership: mem, OptionId: 1},
		&assemblytypes.MsgVoteRemoval{Fee: tr, Membership: mem, OptionId: 1, Option: assemblytypes.VOTE_OPTION_YES},
	} {
		h := e.app.MsgServiceRouter().Handler(msg)
		require.NotNil(t, h, "%T", msg)
		_, err := h(e.ctx(), msg)
		require.ErrorIs(t, err, shieldedtypes.ErrUnauthorized, "%T", msg)
		res := e.checkTx(e.signedTx(phGas, e.fee(50_000), msg))
		require.NotEqual(t, uint32(0), res.Code, "%T", msg)
	}
}

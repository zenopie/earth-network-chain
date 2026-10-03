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
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
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
	phFeeNotes = 40
	phDay      = int64(86400)
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
	leanVK, err := os.ReadFile(filepath.Join(passports, "lean_poa.vk"))
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
			}
			st[shieldedtypes.ModuleName] = cdc.MustMarshalJSON(&sh)

			var ph personhoodtypes.GenesisState
			require.NoError(t, cdc.UnmarshalJSON(st[personhoodtypes.ModuleName], &ph))
			ph.Params.VerifyingKeys = map[string][]byte{"lean_poa": leanVK}
			ph.Params.RegistrationValiditySeconds = 4 * 86400
			ph.Params.CaretakerVoteSeconds = 86400
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

func ct(name string, i int) []byte { return shieldedtest.BlindCT(fmt.Sprintf("personhood-ct:%s:%d", name, i)) }

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
// statement. maxAct overrides the chain's max_activation (to prove a statement
// the chain will not accept).
type member struct {
	reg             string
	scope           fr.Element
	excluded        fr.Element
	excludedCountry fr.Element
	maxAct          int64
}

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
		LeafIndex: reg.LeafIndex, Root: root, Siblings: sib, Scope: m.scope, Signal: signal,
		ExcludedDsc: m.excluded, ExcludedCountry: m.excludedCountry, MaxActivation: uint64(m.maxAct),
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
	case *personhoodtypes.MsgBindReferrer:
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
			Nullifier: privacy.FieldBytes(privacy.U64(i + 1)), Commitment: make([]byte, 32), Cv: cv, Proof: make([]byte, shieldedtypes.ProofBytes)})
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
		Fee: e.bundle(f), Proof: p.proof, PublicSignals: p.signals, SignatureAlgorithm: "lean_poa", DscDer: p.dscDER,
		Idc: privacy.FieldBytes(r.IDC()), PcAnml: privacy.FieldBytes(r.AnmlNote().PC()), CiphertextAnml: r.CiphertextAnml(),
		PcErth: privacy.FieldBytes(r.ErthPC()), CiphertextErth: r.CiphertextErth(),
	}
	if r.Referrer != "" {
		msg.Affiliate = e.bech(personhoodtest.ReferralAddress(r.Referrer))
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
	e.prove("claim/"+name, msg, f, &member{reg: reg, scope: privacy.ClaimScope(uint64(day)), maxAct: maxAct})
	return msg
}

func (e *phEnv) caretaker(name, reg string, maxAct int64, split []allocationtypes.AllocationWeight) *personhoodtypes.MsgSetCaretaker {
	e.t.Helper()
	f := e.feeFor("caretaker/" + name)
	msg := &personhoodtypes.MsgSetCaretaker{Fee: e.bundle(f), Percentages: split, MaxActivation: uint64(maxAct)}
	e.prove("caretaker/"+name, msg, f, &member{reg: reg, scope: privacy.CaretakerScope(), maxAct: maxAct})
	return msg
}

// bindReferrer binds human's referral address (or clears, human "") as
// registration reg.
func (e *phEnv) bindReferrer(name, reg, human string, maxAct int64) *personhoodtypes.MsgBindReferrer {
	e.t.Helper()
	f := e.feeFor("referrer/" + name)
	msg := &personhoodtypes.MsgBindReferrer{Fee: e.bundle(f), MaxActivation: uint64(maxAct)}
	if human != "" {
		msg.Address = e.bech(personhoodtest.ReferralAddress(human))
	}
	e.prove("referrer/"+name, msg, f, &member{reg: reg, scope: privacy.ReferrerScope(), maxAct: maxAct})
	return msg
}

func (e *phEnv) referrerLive(human string) bool {
	e.t.Helper()
	res, err := personhoodkeeper.NewQueryServerImpl(e.app.PersonhoodKeeper).Referrer(e.ctx(),
		&personhoodtypes.QueryReferrerRequest{Address: e.bech(personhoodtest.ReferralAddress(human))})
	require.NoError(e.t, err)
	return res.Live
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
		excludedCountry: field(e.t, in.ExcludedCountry), maxAct: int64(in.MaxActivation)})
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
		scope: privacy.ProposeRemovalScope(option, uint64(now/phDay)), maxAct: now/phDay*phDay - 3600})
	return msg
}

func (e *phEnv) voteRemoval(name, reg string, option uint64, opt assemblytypes.VoteOption) *assemblytypes.MsgVoteRemoval {
	e.t.Helper()
	in := e.ballotInputs(&assemblytypes.QueryBallotInputsRequest{OptionId: option})
	f := e.feeFor("vote-removal/" + name)
	msg := &assemblytypes.MsgVoteRemoval{Fee: e.bundle(f), OptionId: option, Option: opt}
	e.prove("vote-removal/"+name, msg, f, &member{reg: reg, scope: field(e.t, in.Scope), maxAct: int64(in.MaxActivation)})
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

func (e *phEnv) deliverCode(msg sdk.Msg) *abci.ExecTxResult {
	e.t.Helper()
	return e.finalize(e.tx(msg)).TxResults[0]
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
	wantLeaf := privacy.IdentityLeaf(personhoodtest.Registrations["A1"].IDC(), field(t, a1.DscKey), privacy.CountryField("UT"), uint64(a1.ActivatedAt))
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
	// A proposal enters voting two hours after genesis; A and B were activated
	// before voting opened minus the root window, so both may vote.
	e.at(phGenesis.Add(2*time.Hour + 10*time.Minute))
	prop, err := govv1.NewMsgSubmitProposal(nil, e.fee(1_000_000), e.bech(e.userAddr()), "private-personhood-test", "private personhood", "test", false)
	require.NoError(t, err)
	fb = e.finalize(e.signedTx(1_000_000, e.fee(10_000), prop))
	requireOK(t, fb.TxResults[0])
	const pid = uint64(1)
	in := e.ballotInputs(&assemblytypes.QueryBallotInputsRequest{ProposalId: pid})
	require.Equal(t, privacy.FieldBytes(privacy.ProposalScope(pid, 0)), in.Scope)
	require.Equal(t, uint64(e.now.Unix()-3600), in.MaxActivation)

	e.at(phGenesis.Add(3*time.Hour + 20*time.Minute))
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
		member{scope: field(t, in2.Scope), maxAct: int64(in2.MaxActivation)})))
	require.Equal(t, personhoodtypes.ErrInvalidMembership.ABCICode(), res.Code, res.Log)

	// ------------------------------------------------------- DSC revocation
	// B proves membership against today's root, then B's Document Signer is
	// revoked: the purge zeroes B's leaf. Within the root window the old root
	// still anchors; after it, B's proof is refused.
	e.at(phGenesis.Add(3*time.Hour + 40*time.Minute))
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
	// A's first split needs activated_at <= max_activation <= now - R - window.
	// One minute short of that, a proof bounded by A's own activated_at is
	// refused.
	d1 := dayStart(phDay0 + 1)
	early := a1.ActivatedAt + phDay + 3600 - 60
	e.at(time.Unix(early, 0).UTC())
	split := []allocationtypes.AllocationWeight{{OptionId: allocationtypes.RegistrationRewardOptionID, Percent: 100}}
	res = e.checkTx(e.tx(e.caretaker("A-early", "A1", a1.ActivatedAt, split)))
	require.Equal(t, personhoodtypes.ErrInvalidMsg.ABCICode(), res.Code, res.Log)
	e.at(time.Unix(a1.ActivatedAt+phDay+2*3600+60, 0).UTC())
	ok2 := e.caretaker("A", "A1", e.now.Unix()/3600*3600-phDay-3600, split)
	fb = e.mustDeliver(ok2)
	require.Equal(t, uint64(1), e.caretakers())
	voter, err := e.app.AllocationKeeper.Voters.Get(ctxNow(),
		collections.Join(uint32(allocationtypes.STREAM_ID_CARETAKER), ok2.Membership.Nullifier))
	require.NoError(t, err, "the split is filed under the caretaker nullifier")
	require.Equal(t, int64(personhoodtypes.VoterWeight), voter.Weight.Int64())
	caretakerExpiry := e.now.Unix() + phDay
	_ = d1

	// A binds a referral address under the same activation rule. C1 cannot
	// take an address A holds.
	maxAct := e.now.Unix()/3600*3600 - phDay - 3600
	e.mustDeliver(e.bindReferrer("A1", "A1", "A", maxAct))
	require.True(t, e.referrerLive("A"))
	res = e.checkTx(e.tx(e.bindReferrer("C1-taken", "C1", "A", maxAct)))
	require.Equal(t, personhoodtypes.ErrReferrerBound.ABCICode(), res.Code, res.Log)

	// A removal ballot on groundworks option 1, opened and voted anonymously
	// (opening needs an identity activated before today began).
	e.mustDeliver(e.proposeRemoval("A", "A1", 1))
	e.mustDeliver(e.voteRemoval("A", "A1", 1, assemblytypes.VOTE_OPTION_YES))
	ballots, err := aq.RemovalBallots(ctxNow(), &assemblytypes.QueryRemovalBallotsRequest{})
	require.NoError(t, err)
	require.Len(t, ballots.Ballots, 1)
	require.Equal(t, assemblytypes.Tally{Yes: 1}, ballots.Ballots[0].Tally)

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
	// A1 proves tomorrow's claim now, before switching away.
	e.at(dayStart(d2).Add(40 * time.Minute))
	a1Tomorrow := e.claim("A1-d3", "A1", d2+1, -1)

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
	cnt, _ = k.RegCount.Get(ctxNow())
	require.Equal(t, uint64(2), cnt)

	// The caretaker split cast on day 1 lapses R later and is swept.
	e.at(time.Unix(caretakerExpiry+10, 0).UTC())
	require.Equal(t, uint64(0), e.caretakers())
	_, err = e.app.AllocationKeeper.Voters.Get(ctxNow(),
		collections.Join(uint32(allocationtypes.STREAM_ID_CARETAKER), ok2.Membership.Nullifier))
	require.Error(t, err, "cleared from the stream")

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
	// A1's binding lapsed a day after it was made and was swept: naming A is
	// refused, before the passport proof is verified.
	require.False(t, e.referrerLive("A"))
	res = e.checkTx(e.tx(e.register("C2")))
	require.Equal(t, personhoodtypes.ErrNoReferrer.ABCICode(), res.Code, res.Log)
	// A2 (switched in on day 2) binds A's address again; C2 then pays A's
	// half to it in transparent ERTH, and the registrant's half as a note.
	e.mustDeliver(e.bindReferrer("A2", "A2", "A", e.now.Unix()/3600*3600-phDay-3600))
	require.True(t, e.referrerLive("A"))
	aAddr := sdk.AccAddress(personhoodtest.ReferralAddress("A"))
	before := e.app.BankKeeper.GetBalance(ctxNow(), aAddr, "uerth").Amount
	fb = e.mustDeliver(e.register("C2"))
	regEv = eventsOf(fb.TxResults[0].Events, "register")[0]
	require.Equal(t, "false", regEv["switched"])
	rewardC2, ok := math.NewIntFromString(regEv["reward"])
	require.True(t, ok)
	require.True(t, rewardC2.IsPositive())
	paid := e.app.BankKeeper.GetBalance(ctxNow(), aAddr, "uerth").Amount.Sub(before)
	require.True(t, paid.IsPositive())
	require.True(t, rewardC2.Sub(paid).Abs().LTE(math.OneInt()), "the referrer's half: %s vs %s", paid, rewardC2)
	require.True(t, hasCommitment(fb.TxResults[0], privacy.CM(privacy.AssetID("uerth"), rewardC2.Uint64(), personhoodtest.Registrations["C2"].ErthPC())))
	// Rebinding the same nullifier moves the binding.
	e.at(e.now.Add(time.Minute))
	e.mustDeliver(e.bindReferrer("A2-move", "A2", "A-alt", e.now.Unix()/3600*3600-phDay-3600))
	require.False(t, e.referrerLive("A"))
	require.True(t, e.referrerLive("A-alt"))
	cnt, _ = k.RegCount.Get(ctxNow())
	require.Equal(t, uint64(2), cnt)

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
		&personhoodtypes.MsgBindReferrer{Fee: tr, Membership: mem},
		&personhoodtypes.MsgRegister{Fee: tr, Proof: make([]byte, shieldedtypes.ProofBytes), PublicSignals: []string{"1"}, SignatureAlgorithm: "lean_poa",
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

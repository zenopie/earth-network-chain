package types_test

import (
	"bytes"
	"fmt"
	"testing"

	"cosmossdk.io/math"
	addresscodec "github.com/cosmos/cosmos-sdk/codec/address"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	shieldedtest "github.com/earth-network/earth/x/shielded/testutil"
	"github.com/earth-network/earth/x/shielded/types"
	"github.com/earth-network/earth/zk/orchard"
	"github.com/earth-network/earth/zk/privacy"
	"github.com/earth-network/earth/zk/ultrahonk"
)

// placeholderProof is a proof of the right length: shape only.
func placeholderProof(string, [][]byte) []byte { return make([]byte, types.ProofBytes) }

// validSend is the scenario's unshield2 with placeholder proofs: shape only.
func validSend(t *testing.T) *types.MsgSend {
	t.Helper()
	m, err := shieldedtest.Default().Msg(shieldedtest.Unshield2, "earth1receiver", types.TxFields{}, placeholderProof)
	require.NoError(t, err)
	return m
}

func TestMsgSendValidateBasic(t *testing.T) {
	require.NoError(t, validSend(t).ValidateBasic())
	nonCanonical := bytes.Repeat([]byte{0xff}, 32)
	offCurve := func() []byte {
		b := validSend(t).Bundle.Actions[0].Cv
		c := append([]byte(nil), b...)
		c[63] ^= 1
		return c
	}()

	cases := map[string]func(m *types.MsgSend){
		"one action (padding)": func(m *types.MsgSend) { m.Bundle.Actions = m.Bundle.Actions[:1] },
		"no actions":           func(m *types.MsgSend) { m.Bundle.Actions = nil },
		"33 actions": func(m *types.MsgSend) {
			for len(m.Bundle.Actions) < orchard.MaxActions+1 {
				a := m.Bundle.Actions[0]
				a.Nullifier = privacy.FieldBytes(privacy.U64(uint64(len(m.Bundle.Actions))))
				m.Bundle.Actions = append(m.Bundle.Actions, a)
			}
		},
		"no proof":             func(m *types.MsgSend) { m.Bundle.Actions[0].Proof = nil },
		"huge proof":           func(m *types.MsgSend) { m.Bundle.Actions[1].Proof = make([]byte, 32*1024) },
		"proof one byte long":  func(m *types.MsgSend) { m.Bundle.Actions[1].Proof = append(m.Bundle.Actions[1].Proof, 0) },
		"proof one byte short": func(m *types.MsgSend) { m.Bundle.Actions[1].Proof = m.Bundle.Actions[1].Proof[:types.ProofBytes-1] },
		"proof a field long":   func(m *types.MsgSend) { m.Bundle.Actions[1].Proof = make([]byte, types.ProofBytes+32) },
		"short anchor":         func(m *types.MsgSend) { m.Bundle.Actions[0].Anchor = m.Bundle.Actions[0].Anchor[:31] },
		"non-canonical anchor": func(m *types.MsgSend) { m.Bundle.Actions[1].Anchor = nonCanonical },
		"non-canonical nf":     func(m *types.MsgSend) { m.Bundle.Actions[1].Nullifier = nonCanonical },
		"duplicate nf":         func(m *types.MsgSend) { m.Bundle.Actions[1].Nullifier = m.Bundle.Actions[0].Nullifier },
		"non-canonical cm":     func(m *types.MsgSend) { m.Bundle.Actions[0].Commitment = nonCanonical },
		"cv off the curve":     func(m *types.MsgSend) { m.Bundle.Actions[0].Cv = offCurve },
		"cv short":             func(m *types.MsgSend) { m.Bundle.Actions[0].Cv = m.Bundle.Actions[0].Cv[:63] },
		"cv x >= p": func(m *types.MsgSend) {
			m.Bundle.Actions[0].Cv = append(nonCanonical, m.Bundle.Actions[0].Cv[32:]...)
		},
		"huge ciphertext":   func(m *types.MsgSend) { m.Bundle.Actions[0].Ciphertext = make([]byte, types.MaxCiphertextBytes+1) },
		"zero balance":      func(m *types.MsgSend) { m.Bundle.Balances[0].Amount = 0 },
		"duplicate balance": func(m *types.MsgSend) { m.Bundle.Balances = append(m.Bundle.Balances, m.Bundle.Balances[0]) },
		"bad denom":         func(m *types.MsgSend) { m.Bundle.Balances[0].Denom = "!" },
		"too many balances": func(m *types.MsgSend) {
			for k := range 5 {
				m.Bundle.Balances = append(m.Bundle.Balances, types.ValueBalance{Denom: fmt.Sprintf("ux%d", k), Amount: 1})
			}
		},
		"short binding sig":       func(m *types.MsgSend) { m.Bundle.BindingSig = m.Bundle.BindingSig[:95] },
		"no fee":                  func(m *types.MsgSend) { m.Fee = 0 },
		"fee above uerth balance": func(m *types.MsgSend) { m.Fee = m.Bundle.Balances[0].Amount + 1 },
		"remainder, no receiver":  func(m *types.MsgSend) { m.Receiver = "" },
		"receiver, no remainder":  func(m *types.MsgSend) { m.Fee = m.Bundle.Balances[0].Amount },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			m := validSend(t)
			mutate(m)
			require.Error(t, m.ValidateBasic())
		})
	}
	// A u64-max balance is fine (Remainders sums across bundles with an
	// overflow check).
	m := validSend(t)
	m.Bundle.Balances[0].Amount = ^uint64(0)
	m.Fee = ^uint64(0)
	m.Receiver = ""
	require.NoError(t, m.ValidateBasic())
}

func TestRemainders(t *testing.T) {
	m := validSend(t)
	rem, err := types.Remainders(m)
	require.NoError(t, err)
	require.Equal(t, []types.Remainder{{Denom: types.FeeDenom, Amount: 500_000}}, rem)
	m.Bundle.Balances = append(m.Bundle.Balances, types.ValueBalance{Denom: types.AnmlDenom, Amount: 7})
	rem, err = types.Remainders(m)
	require.NoError(t, err)
	require.Equal(t, []types.Remainder{{Denom: types.AnmlDenom, Amount: 7}, {Denom: types.FeeDenom, Amount: 500_000}}, rem, "denom order")
	m.Fee = 530_001
	_, err = types.Remainders(m)
	require.ErrorIs(t, err, types.ErrReleaseMap)
	require.Equal(t, uint64(530_001), types.TotalFee(m).Uint64())
}

// The sighash covers the msg type, chain id, bundle, the tx's memo, timeout
// height and gas limit, and the msg's fields.
func TestSighash(t *testing.T) {
	ac := addresscodec.NewBech32Codec("earth")
	s := shieldedtest.Default()
	tx := types.TxFields{Memo: "deposit 42", TimeoutHeight: 900, GasLimit: 1_234_567}
	m, err := s.Msg(shieldedtest.Unshield2, "", tx, placeholderProof)
	require.NoError(t, err)
	m.Receiver, err = ac.BytesToString(shieldedtest.Receiver)
	require.NoError(t, err)
	got, err := types.Sighash(m, shieldedtest.ChainID, tx, ac)
	require.NoError(t, err)
	b, err := s.Bundle(shieldedtest.Unshield2)
	require.NoError(t, err)
	want, err := s.Sighash(shieldedtest.Unshield2, b, tx)
	require.NoError(t, err)
	require.Equal(t, want, got)
	// Proofs and the binding signature are not bound (they are made over it).
	m.Bundle.Actions[0].Proof = []byte("other")
	m.Bundle.BindingSig = make([]byte, 96)
	again, err := types.Sighash(m, shieldedtest.ChainID, tx, ac)
	require.NoError(t, err)
	require.Equal(t, got, again)
	for name, other := range map[string]types.TxFields{
		"memo":           {Memo: "deposit 43", TimeoutHeight: 900, GasLimit: 1_234_567},
		"no memo":        {TimeoutHeight: 900, GasLimit: 1_234_567},
		"timeout height": {Memo: "deposit 42", GasLimit: 1_234_567},
		"gas limit":      {Memo: "deposit 42", TimeoutHeight: 900, GasLimit: 1_234_568},
	} {
		h, err := types.Sighash(m, shieldedtest.ChainID, other, ac)
		require.NoError(t, err)
		require.NotEqual(t, got, h, name)
	}
	m.Fee++
	moved, err := types.Sighash(m, shieldedtest.ChainID, tx, ac)
	require.NoError(t, err)
	require.NotEqual(t, got, moved)
}

// ProofBytes is the verifier's proof size.
func TestProofBytes(t *testing.T) {
	require.Equal(t, ultrahonk.ProofSize, types.ProofBytes)
	require.NoError(t, types.CheckProofLength(make([]byte, types.ProofBytes)))
	for _, n := range []int{0, 1, types.ProofBytes - 32, types.ProofBytes - 1, types.ProofBytes + 1, types.ProofBytes + 32} {
		require.Error(t, types.CheckProofLength(make([]byte, n)), n)
	}
}

func TestPrivateMsgGas(t *testing.T) {
	p := types.DefaultParams()
	b2 := &types.Bundle{Actions: make([]types.Action, 2)}
	b10 := &types.Bundle{Actions: make([]types.Action, 10)}
	per := types.DefaultProofVerificationGas + 2*types.DefaultNoteGas
	require.Equal(t, per, p.ActionGas())
	require.Equal(t, types.DefaultBundleGas+2*per, p.PrivateMsgGas([]*types.Bundle{b2}))
	require.Equal(t, 2*types.DefaultBundleGas+12*per, p.PrivateMsgGas([]*types.Bundle{b2, b10}))
}

func TestMsgShieldValidateBasic(t *testing.T) {
	pc := privacy.FieldBytes(shieldedtest.Det("pc", 0))
	ct := shieldedtest.BlindCT("shield")
	ok := types.MsgShield{Amount: sdk.NewInt64Coin("uerth", 1), Pc: pc, Ciphertext: ct}
	require.NoError(t, ok.ValidateBasic())
	for name, m := range map[string]types.MsgShield{
		"zero":     {Amount: sdk.NewInt64Coin("uerth", 0), Pc: pc, Ciphertext: ct},
		"over u64": {Amount: sdk.NewCoin("uerth", math.NewIntFromUint64(^uint64(0)).AddRaw(1)), Pc: pc, Ciphertext: ct},
		"bad pc":   {Amount: ok.Amount, Pc: bytes.Repeat([]byte{0xff}, 32), Ciphertext: ct},
		"short pc": {Amount: ok.Amount, Pc: pc[:31], Ciphertext: ct},
		// The note discovery rule: every minted note carries its blind
		// ciphertext, exactly 177 bytes.
		"no ciphertext":    {Amount: ok.Amount, Pc: pc},
		"short ciphertext": {Amount: ok.Amount, Pc: pc, Ciphertext: ct[:176]},
		"v1 ciphertext":    {Amount: ok.Amount, Pc: pc, Ciphertext: make([]byte, 217)},
		"huge ciphertext":  {Amount: ok.Amount, Pc: pc, Ciphertext: make([]byte, types.MaxCiphertextBytes+1)},
	} {
		require.Error(t, m.ValidateBasic(), name)
	}
}

func TestParamsValidate(t *testing.T) {
	require.NoError(t, types.DefaultParams().Validate())
	for name, mutate := range map[string]func(p *types.Params){
		"zero min fee":    func(p *types.Params) { p.MinFee = math.ZeroInt() },
		"nil min fee":     func(p *types.Params) { p.MinFee = math.Int{} },
		"zero proof gas":  func(p *types.Params) { p.ProofVerificationGas = 0 },
		"zero note gas":   func(p *types.Params) { p.NoteGas = 0 },
		"zero window":     func(p *types.Params) { p.RootWindowSeconds = 0 },
		"zero bundle gas": func(p *types.Params) { p.BundleGas = 0 },
		"no actions":      func(p *types.Params) { p.MaxPrivateActionsPerBlock = 0 },
		"bundle of one":   func(p *types.Params) { p.MaxActionsPerBundle = 1 },
		"bundle of 33":    func(p *types.Params) { p.MaxActionsPerBundle = 33; p.MaxPrivateActionsPerBlock = 66 },
		"block below two max bundles": func(p *types.Params) {
			p.MaxPrivateActionsPerBlock = 2*p.MaxActionsPerBundle - 1
		},
		"unknown vk": func(p *types.Params) { p.VerifyingKeys = map[string][]byte{"passport": {1}} },
		"retired vk": func(p *types.Params) { p.VerifyingKeys = map[string][]byte{"transfer": {1}} },
		"empty vk":   func(p *types.Params) { p.VerifyingKeys = map[string][]byte{types.CircuitAction: nil} },
	} {
		p := types.DefaultParams()
		mutate(&p)
		require.Error(t, p.Validate(), name)
	}
}

func TestGenesisValidate(t *testing.T) {
	require.NoError(t, types.DefaultGenesis().Validate())
	cm := privacy.FieldBytes(shieldedtest.Det("cm", 0))
	for name, mutate := range map[string]func(g *types.GenesisState){
		"no uerth":         func(g *types.GenesisState) { g.Assets = g.Assets[1:] },
		"dup asset":        func(g *types.GenesisState) { g.Assets = append(g.Assets, g.Assets[0]) },
		"wrong asset id":   func(g *types.GenesisState) { g.Assets[1].AssetId = g.Assets[0].AssetId },
		"bad commitment":   func(g *types.GenesisState) { g.Commitments = [][]byte{{1}} },
		"dup nullifier":    func(g *types.GenesisState) { g.Nullifiers = [][]byte{cm, cm} },
		"root beyond size": func(g *types.GenesisState) { g.Roots = []types.RootRecord{{Root: cm, TreeSize: 1}} },
		"stray turnstile": func(g *types.GenesisState) {
			g.Turnstiles = []types.Turnstile{{Denom: "unope", In: math.OneInt(), Out: math.ZeroInt()}}
		},
		"out > in": func(g *types.GenesisState) {
			g.Turnstiles = []types.Turnstile{{Denom: "uerth", In: math.OneInt(), Out: math.NewInt(2)}}
		},
	} {
		g := types.DefaultGenesis()
		mutate(g)
		require.Error(t, g.Validate(), name)
	}
}

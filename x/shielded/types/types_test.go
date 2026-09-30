package types_test

import (
	"bytes"
	"testing"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	shieldedtest "github.com/earth-network/earth/x/shielded/testutil"
	"github.com/earth-network/earth/x/shielded/types"
	"github.com/earth-network/earth/zk/privacy"
)

func validTransfer(t *testing.T) types.MsgTransfer {
	t.Helper()
	tr, err := shieldedtest.Default().Transfer(0, []byte("proof"))
	require.NoError(t, err)
	return types.MsgTransfer{Transfer: tr}
}

func TestMsgTransferValidateBasic(t *testing.T) {
	require.NoError(t, (&types.MsgTransfer{Transfer: validTransfer(t).Transfer}).ValidateBasic())
	nonCanonical := bytes.Repeat([]byte{0xff}, 32)
	cases := map[string]func(m *types.MsgTransfer){
		"no proof":            func(m *types.MsgTransfer) { m.Transfer.Proof = nil },
		"huge proof":          func(m *types.MsgTransfer) { m.Transfer.Proof = make([]byte, types.MaxProofBytes+1) },
		"short root":          func(m *types.MsgTransfer) { m.Transfer.Root = m.Transfer.Root[:31] },
		"non-canonical root":  func(m *types.MsgTransfer) { m.Transfer.Root = nonCanonical },
		"two nullifiers":      func(m *types.MsgTransfer) { m.Transfer.Nullifiers = m.Transfer.Nullifiers[:2] },
		"non-canonical nf":    func(m *types.MsgTransfer) { m.Transfer.Nullifiers[2] = nonCanonical },
		"duplicate nf":        func(m *types.MsgTransfer) { m.Transfer.Nullifiers[2] = m.Transfer.Nullifiers[0] },
		"four commitments":    func(m *types.MsgTransfer) { m.Transfer.Commitments = append(m.Transfer.Commitments, m.Transfer.Root) },
		"non-canonical cm":    func(m *types.MsgTransfer) { m.Transfer.Commitments[1] = nonCanonical },
		"huge ciphertext":     func(m *types.MsgTransfer) { m.Transfer.Ciphertexts[0] = make([]byte, types.MaxCiphertextBytes+1) },
		"asset without value": func(m *types.MsgTransfer) { m.Transfer.DenomOut = "uerth" },
		"value without asset": func(m *types.MsgTransfer) { m.Transfer.ValueOut = 1; m.Receiver = "x" },
		"value, no receiver":  func(m *types.MsgTransfer) { m.Transfer.ValueOut, m.Transfer.DenomOut = 1, "uerth" },
		"receiver, no value":  func(m *types.MsgTransfer) { m.Receiver = "earth1xyz" },
		"bad denom": func(m *types.MsgTransfer) {
			m.Transfer.ValueOut, m.Transfer.DenomOut, m.Receiver = 1, "!", "x"
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			m := validTransfer(t)
			mutate(&m)
			require.Error(t, m.ValidateBasic())
		})
	}
}

func TestMsgShieldValidateBasic(t *testing.T) {
	pc := privacy.FieldBytes(shieldedtest.Det("pc", 0))
	ok := types.MsgShield{Amount: sdk.NewInt64Coin("uerth", 1), Pc: pc}
	require.NoError(t, ok.ValidateBasic())
	for name, m := range map[string]types.MsgShield{
		"zero":       {Amount: sdk.NewInt64Coin("uerth", 0), Pc: pc},
		"over u64":   {Amount: sdk.NewCoin("uerth", math.NewIntFromUint64(^uint64(0)).AddRaw(1)), Pc: pc},
		"bad pc":     {Amount: ok.Amount, Pc: bytes.Repeat([]byte{0xff}, 32)},
		"short pc":   {Amount: ok.Amount, Pc: pc[:31]},
		"ciphertext": {Amount: ok.Amount, Pc: pc, Ciphertext: make([]byte, types.MaxCiphertextBytes+1)},
	} {
		require.Error(t, m.ValidateBasic(), name)
	}
}

// asset_pub and signal are computed, not carried: 0 unless value leaves, and
// the scenario's own public inputs otherwise.
func TestPublicInputsLayout(t *testing.T) {
	s := shieldedtest.Default()
	for i := range s.Transfers {
		_, want, err := s.Witness(i)
		require.NoError(t, err)
		tr, err := s.Transfer(i, []byte("p"))
		require.NoError(t, err)
		var assetPub = want[9]
		got := tr.PublicInputs(assetPub, s.Transfers[i].Signal())
		require.Len(t, got, types.TransferPublicInputs)
		for j := range want {
			require.Equal(t, privacy.FieldBytes(want[j]), got[j], "input %d", j)
		}
	}
	// transfer 0 unshields nothing: asset_pub is 0.
	_, pub, _ := s.Witness(0)
	require.True(t, pub[9].IsZero())
}

func TestParamsValidate(t *testing.T) {
	require.NoError(t, types.DefaultParams().Validate())
	for name, mutate := range map[string]func(p *types.Params){
		"zero min fee":   func(p *types.Params) { p.MinFee = math.ZeroInt() },
		"nil min fee":    func(p *types.Params) { p.MinFee = math.Int{} },
		"zero proof gas": func(p *types.Params) { p.ProofVerificationGas = 0 },
		"zero note gas":  func(p *types.Params) { p.NoteGas = 0 },
		"zero window":    func(p *types.Params) { p.RootWindowSeconds = 0 },
		"no txs":         func(p *types.Params) { p.MaxPrivateTxsPerBlock = 0 },
		"unknown vk":     func(p *types.Params) { p.VerifyingKeys = map[string][]byte{"passport": {1}} },
		"empty vk":       func(p *types.Params) { p.VerifyingKeys = map[string][]byte{types.CircuitTransfer: nil} },
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

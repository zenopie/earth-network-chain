package keeper

import (
	"context"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"cosmossdk.io/math"
	storetypes "cosmossdk.io/store/types"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	addresscodec "github.com/cosmos/cosmos-sdk/codec/address"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/cosmos/cosmos-sdk/testutil"
	sdk "github.com/cosmos/cosmos-sdk/types"
	moduletestutil "github.com/cosmos/cosmos-sdk/types/module/testutil"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/stretchr/testify/require"

	allocationtypes "github.com/earth-network/earth/x/allocation/types"
	personhoodtest "github.com/earth-network/earth/x/personhood/testutil"
	"github.com/earth-network/earth/x/personhood/types"
	"github.com/earth-network/earth/x/pki/certs"
	shieldedtypes "github.com/earth-network/earth/x/shielded/types"
	"github.com/earth-network/earth/zk/privacy"
)

// stubAllocation stands in for x/allocation where the emission stream is not
// under test.
type stubAllocation struct{}

func (stubAllocation) AdvanceIndex(context.Context, allocationtypes.StreamId) error { return nil }
func (stubAllocation) ClearVoter(context.Context, allocationtypes.StreamId, []byte) error {
	return nil
}
func (stubAllocation) ValidateSplit(context.Context, allocationtypes.StreamId, []allocationtypes.AllocationWeight) error {
	return nil
}
func (stubAllocation) SetVoterSplit(context.Context, allocationtypes.StreamId, []byte, []allocationtypes.AllocationWeight, math.Int) error {
	return nil
}
func (stubAllocation) DrawFromOption(context.Context, allocationtypes.StreamId, uint64, int64) (math.Int, error) {
	return math.ZeroInt(), nil
}
func (stubAllocation) PayOutToModule(context.Context, string, math.Int) error { return nil }

// stubShielded records the notes minted.
type stubShielded struct{ minted *[]sdk.Coin }

func (s stubShielded) MintNote(_ context.Context, _ string, coin sdk.Coin, _, _ []byte) (uint64, []byte, error) {
	if s.minted != nil {
		*s.minted = append(*s.minted, coin)
	}
	return 0, nil, nil
}
func (stubShielded) RegisterPrivateAction(string, shieldedtypes.PrivateActionHandler) {}
func (stubShielded) VerifyCircuit(context.Context, string, []byte, [][]byte) error    { return nil }
func (stubShielded) PrivateGasPrices(context.Context) (uint64, uint64, error) {
	return 1, 1, nil
}

// stubPki is a test PkiKeeper: it either rejects the DSC outright or returns a
// fixed canonical public key for it.
type stubPki struct {
	pubkey  *certs.PublicKey
	err     error
	revoked bool
	country string
}

func (s stubPki) IsCommitmentRevoked(context.Context, []byte) (bool, error) {
	return s.revoked, nil
}

func (s stubPki) VerifyDscIssuer(_ context.Context, _ []byte) (*certs.PublicKey, string, error) {
	if s.err != nil {
		return nil, "", s.err
	}
	return s.pubkey, s.country, nil
}

type stubDex struct{}

func (stubDex) HubDenom(context.Context) (string, error)              { return "uerth", nil }
func (stubDex) HasPoolForToken(context.Context, string) (bool, error) { return false, nil }
func (stubDex) SwapExactInForModule(context.Context, string, sdk.Coin, string, math.Int) (sdk.Coin, error) {
	return sdk.Coin{}, nil
}
func (stubDex) TwapObservation(context.Context, string) (math.LegacyDec, math.LegacyDec, int64, error) {
	return math.LegacyDec{}, math.LegacyDec{}, 0, errNoStubPool
}
func (stubDex) QuoteHubToToken(context.Context, string, math.Int) (math.Int, error) {
	return math.Int{}, errNoStubPool
}

var errNoStubPool = errors.New("stub dex: no pool")

// passportDir holds real lean_poa proofs bound to x/personhood/testutil's
// registrations (scripts/personhood-fixtures.sh).
var passportDir = filepath.Join("..", "testdata", "passports")

func readFileAt(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return b
}

// passportMsg is registration name's MsgRegister, without a fee.
func passportMsg(t *testing.T, name string) *types.MsgRegister {
	t.Helper()
	r := personhoodtest.Registrations[name]
	pub := readFileAt(t, filepath.Join(passportDir, name, "public_inputs"))
	var signals []string
	for i := 0; i+32 <= len(pub); i += 32 {
		signals = append(signals, new(big.Int).SetBytes(pub[i:i+32]).String())
	}
	m := &types.MsgRegister{
		Proof: readFileAt(t, filepath.Join(passportDir, name, "proof")), PublicSignals: signals,
		SignatureAlgorithm: "lean_poa", DscDer: readFileAt(t, filepath.Join(passportDir, name, "dsc.der")),
		Idc: privacy.FieldBytes(r.IDC()), PcAnml: privacy.FieldBytes(r.AnmlNote().PC()), PcErth: privacy.FieldBytes(r.ErthPC()),
	}
	if r.Referrer != "" {
		m.AffiliatePc = privacy.FieldBytes(r.ReferrerPC())
	}
	return m
}

func leanParams(t *testing.T) types.Params {
	p := types.DefaultParams()
	p.VerifyingKeys = map[string][]byte{"lean_poa": readFileAt(t, filepath.Join(passportDir, "lean_poa.vk"))}
	p.NullifierIndex, p.DscKeyIndex, p.CurrentDateIndex, p.AddressIndex = 2, 3, 0, 1
	return p
}

// passportTime is inside every 250101 fixture's current_date skew.
var passportTime = time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)

func regKeeper(t *testing.T, pki types.PkiKeeper) (Keeper, sdk.Context) {
	t.Helper()
	encCfg := moduletestutil.MakeTestEncodingConfig()
	ac := addresscodec.NewBech32Codec(sdk.GetConfig().GetBech32AccountAddrPrefix())
	storeKey := storetypes.NewKVStoreKey(types.StoreKey)
	base := testutil.DefaultContextWithDB(t, storeKey, storetypes.NewTransientStoreKey("transient_test")).Ctx
	k := NewKeeper(runtime.NewKVStoreService(storeKey), encCfg.Codec, ac, authtypes.NewModuleAddress(types.GovModuleName),
		nil, stubDex{}, pki, stubAllocation{}, &burnLog{}, stubShielded{})
	ctx := base.WithBlockTime(passportTime)
	require.NoError(t, k.Params.Set(ctx, leanParams(t)))
	return k, ctx
}

func checkAndVerify(k Keeper, ctx sdk.Context, m *types.MsgRegister) (preparedRegistration, error) {
	p, err := k.checkRegistration(ctx, m)
	if err != nil {
		return p, err
	}
	return p, verifyRegistrationProof(m, p)
}

func dscKeyOf(t *testing.T, name string) *certs.PublicKey {
	t.Helper()
	c, err := certs.ParseCert(readFileAt(t, filepath.Join(passportDir, name, "dsc.der")))
	require.NoError(t, err)
	return c.PublicKey
}

// A passport proof is bound to the identity commitment and notes the msg
// names: change any of them and the chain refuses it before verifying.
func TestRegistrationBinding(t *testing.T) {
	k, ctx := regKeeper(t, stubPki{pubkey: dscKeyOf(t, "A1")})
	m := passportMsg(t, "A1")
	p, err := checkAndVerify(k, ctx, m)
	require.NoError(t, err)
	require.False(t, p.switched)
	want, _ := new(big.Int).SetString(string(readFileAt(t, filepath.Join(passportDir, "A1", "expected_nullifier"))), 10)
	require.Equal(t, want.Bytes(), new(big.Int).SetBytes(p.nullifier).Bytes())

	other := privacy.FieldBytes(personhoodtest.Det("someone-else", 0))
	for name, mutate := range map[string]func(*types.MsgRegister){
		"idc":       func(m *types.MsgRegister) { m.Idc = other },
		"pc_anml":   func(m *types.MsgRegister) { m.PcAnml = other },
		"pc_erth":   func(m *types.MsgRegister) { m.PcErth = other },
		"affiliate": func(m *types.MsgRegister) { m.AffiliatePc = other },
	} {
		m := passportMsg(t, "A1")
		mutate(m)
		_, err := checkAndVerify(k, ctx, m)
		require.ErrorIs(t, err, types.ErrBadPublicInputs, name)
	}
	// B names A's affiliate pc; dropping it breaks the binding too.
	kB, ctxB := regKeeper(t, stubPki{pubkey: dscKeyOf(t, "B")})
	mB := passportMsg(t, "B")
	_, err = checkAndVerify(kB, ctxB, mB)
	require.NoError(t, err)
	mB.AffiliatePc = nil
	_, err = checkAndVerify(kB, ctxB, mB)
	require.ErrorIs(t, err, types.ErrBadPublicInputs)
}

// The proof's dsc_key must equal the commitment of the certificate x/pki
// verified: a trusted DSC with another key, an untrusted DSC, or none, are
// refused.
func TestRegistrationDscBinding(t *testing.T) {
	m := passportMsg(t, "A1")
	k, ctx := regKeeper(t, stubPki{err: errors.New("no trusted issuing CSCA")})
	_, err := checkAndVerify(k, ctx, m)
	require.Error(t, err)

	csca, err := certs.ParseCert(readFileAt(t, filepath.Join(passportDir, "A1", "csca.der")))
	require.NoError(t, err)
	k, ctx = regKeeper(t, stubPki{pubkey: csca.PublicKey})
	_, err = checkAndVerify(k, ctx, m)
	require.ErrorIs(t, err, types.ErrBadPublicInputs)

	k, ctx = regKeeper(t, stubPki{pubkey: dscKeyOf(t, "A1")})
	m.DscDer = nil
	_, err = checkAndVerify(k, ctx, m)
	require.ErrorIs(t, err, types.ErrBadPublicInputs)
}

// current_date is pinned to block time; zero skew disables registration.
func TestRegistrationCurrentDatePinning(t *testing.T) {
	k, ctx := regKeeper(t, stubPki{pubkey: dscKeyOf(t, "A1")})
	m := passportMsg(t, "A1")
	_, err := checkAndVerify(k, ctx.WithBlockTime(time.Date(2025, 2, 1, 0, 0, 0, 0, time.UTC)), m)
	require.ErrorIs(t, err, types.ErrBadPublicInputs)
	p := leanParams(t)
	p.CurrentDateMaxSkewSeconds = 0
	require.NoError(t, k.Params.Set(ctx, p))
	_, err = checkAndVerify(k, ctx, m)
	require.ErrorIs(t, err, types.ErrBadPublicInputs)
}

// A tampered proof fails verification.
func TestRegistrationProofMustVerify(t *testing.T) {
	k, ctx := regKeeper(t, stubPki{pubkey: dscKeyOf(t, "A1")})
	m := passportMsg(t, "A1")
	m.Proof = append([]byte(nil), m.Proof...)
	m.Proof[100] ^= 1
	_, err := checkAndVerify(k, ctx, m)
	require.ErrorIs(t, err, types.ErrInvalidProof)
}

// ParseSignal refuses n+p: the same field element under another spelling would
// be a second dedup key for one passport.
func TestParseSignalRejectsNonCanonical(t *testing.T) {
	p := fr.Modulus()
	_, err := types.ParseSignal("250101")
	require.NoError(t, err)
	for _, s := range []string{
		p.String(),
		new(big.Int).Add(p, big.NewInt(250101)).String(),
		"-1", "x",
	} {
		_, err := types.ParseSignal(s)
		require.Error(t, err, s)
	}
}

func TestYYMMDDToUnix(t *testing.T) {
	f := func(yymmdd int64) []byte {
		var buf [32]byte
		new(big.Int).SetInt64(yymmdd).FillBytes(buf[:])
		return buf[:]
	}
	got, err := yymmddToUnix(f(250101))
	require.NoError(t, err)
	require.Equal(t, time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC).Unix(), got)
	for _, bad := range []int64{251301, 250132, 250100, 1000000} {
		_, err := yymmddToUnix(f(bad))
		require.Error(t, err, bad)
	}
}

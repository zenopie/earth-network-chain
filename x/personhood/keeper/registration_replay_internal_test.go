package keeper

import (
	shieldedtest "github.com/earth-network/earth/x/shielded/testutil"
	"path/filepath"
	"testing"
	"time"

	storetypes "cosmossdk.io/store/types"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	addresscodec "github.com/cosmos/cosmos-sdk/codec/address"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/cosmos/cosmos-sdk/testutil"
	sdk "github.com/cosmos/cosmos-sdk/types"
	moduletestutil "github.com/cosmos/cosmos-sdk/types/module/testutil"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/stretchr/testify/require"

	personhoodtest "github.com/earth-network/earth/x/personhood/testutil"
	"github.com/earth-network/earth/x/personhood/types"
	"github.com/earth-network/earth/x/pki/certs"
	pkikeeper "github.com/earth-network/earth/x/pki/keeper"
	pkitypes "github.com/earth-network/earth/x/pki/types"
	shieldedtypes "github.com/earth-network/earth/x/shielded/types"
	"github.com/earth-network/earth/zk/privacy"
)

// Re-audit R1 (A -> B -> A replay): a holder registered to idc A (a public
// tx), then switched to idc B. Anyone replaying the public A registration
// within the current_date skew would have it accepted as a switch back to A.
// A landed binding is refused for reuse, by the ante and by `earthd
// gas-check registration` (CheckRegistration) alike, until the date check
// refuses the proof anyway.
func TestRegistrationABAReplayRefused(t *testing.T) {
	encCfg := moduletestutil.MakeTestEncodingConfig()
	ac := addresscodec.NewBech32Codec(sdk.GetConfig().GetBech32AccountAddrPrefix())
	pkiStore := storetypes.NewKVStoreKey(pkitypes.StoreKey)
	phStore := storetypes.NewKVStoreKey(types.StoreKey)
	ctx := testutil.DefaultContextWithKeys(map[string]*storetypes.KVStoreKey{
		pkitypes.StoreKey: pkiStore, types.StoreKey: phStore,
	}, nil, nil).WithBlockTime(passportTime).WithChainID(shieldedtest.ChainID)
	ctx = shieldedtypes.WithTxFields(ctx, shieldedtypes.TxFields{})
	pki := pkikeeper.NewKeeper(runtime.NewKVStoreService(pkiStore), encCfg.Codec, ac, authtypes.NewModuleAddress(pkitypes.GovModuleName))
	require.NoError(t, pki.InitGenesis(ctx, pkitypes.GenesisState{
		Params: pkitypes.NewParams(),
		Cscas:  []pkitypes.Csca{{CertificateDer: readFileAt(t, filepath.Join(passportDir, "A1", "csca.der"))}},
	}))
	k := NewKeeper(runtime.NewKVStoreService(phStore), encCfg.Codec, ac, authtypes.NewModuleAddress(types.GovModuleName),
		nil, stubDex{}, pki, stubAllocation{}, &burnLog{}, stubShielded{})
	params := leanParams(t)
	require.NoError(t, k.Params.Set(ctx, params))

	replayed := passportMsg(t, "A1") // the original registration to idc A, public on chain
	nf, _, err := k.CheckRegistration(ctx, replayed)
	require.NoError(t, err)
	p, err := k.checkRegistration(ctx, replayed)
	require.NoError(t, err)
	// It lands (what Register records), then the holder switches to idc B.
	require.NoError(t, k.markBindingUsed(ctx, p.binding, p.proofDate))
	b := privacy.FieldBytes(privacy.U64(424242))
	require.NoError(t, k.addRegistration(ctx, types.Registration{Nullifier: nf, RegisteredAt: ctx.BlockTime().Unix(),
		ActivatedAt: ctx.BlockTime().Unix(), Idc: b}))

	// The replay of A is refused, by the ante's check and by gas-check.
	_, err = k.checkRegistration(ctx, replayed)
	require.ErrorIs(t, err, types.ErrBindingUsed)
	_, _, err = k.CheckRegistration(ctx, replayed)
	require.ErrorIs(t, err, types.ErrBindingUsed)
	// ...a day and a half later, still inside the skew: still refused.
	ctx2 := ctx.WithBlockTime(passportTime.Add(36 * time.Hour))
	_, err = k.checkRegistration(ctx2, replayed)
	require.ErrorIs(t, err, types.ErrBindingUsed)

	// Exported and imported with the rest of genesis.
	gs, err := k.ExportGenesis(ctx)
	require.NoError(t, err)
	require.Len(t, gs.UsedBindings, 1)
	require.Equal(t, p.binding, gs.UsedBindings[0].Binding)

	// The entry outlives the skew: once it is swept, the date check refuses.
	until, err := k.UsedBindings.Get(ctx, p.binding)
	require.NoError(t, err)
	require.Equal(t, p.proofDate+types.MaxCurrentDateMaxSkewSeconds+types.UsedBindingGraceSeconds, until)
	ctx3 := ctx.WithBlockTime(time.Unix(until, 0))
	n, err := k.sweepUsedBindings(ctx3, 10)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	has, err := k.UsedBindings.Has(ctx3, p.binding)
	require.NoError(t, err)
	require.False(t, has)
	_, err = k.checkRegistration(ctx3, replayed)
	require.ErrorIs(t, err, types.ErrBadPublicInputs, "past the entry's expiry the proof's current_date is out of the skew")
}

// Audit B-1/B-3: a switch must be proven on a later date than the live
// registration. A proof that reached a block but failed (so its binding was
// never marked used) cannot be replayed over the registration its holder
// made in its place the same day; and one passport switches at most once
// per proof date, so a holder cannot fill its signer's shared daily cap.
func TestSwitchNeedsALaterProofDate(t *testing.T) {
	encCfg := moduletestutil.MakeTestEncodingConfig()
	ac := addresscodec.NewBech32Codec(sdk.GetConfig().GetBech32AccountAddrPrefix())
	pkiStore := storetypes.NewKVStoreKey(pkitypes.StoreKey)
	phStore := storetypes.NewKVStoreKey(types.StoreKey)
	ctx := testutil.DefaultContextWithKeys(map[string]*storetypes.KVStoreKey{
		pkitypes.StoreKey: pkiStore, types.StoreKey: phStore,
	}, nil, nil).WithBlockTime(passportTime).WithChainID(shieldedtest.ChainID)
	ctx = shieldedtypes.WithTxFields(ctx, shieldedtypes.TxFields{})
	pki := pkikeeper.NewKeeper(runtime.NewKVStoreService(pkiStore), encCfg.Codec, ac, authtypes.NewModuleAddress(pkitypes.GovModuleName))
	require.NoError(t, pki.InitGenesis(ctx, pkitypes.GenesisState{
		Params: pkitypes.NewParams(),
		Cscas:  []pkitypes.Csca{{CertificateDer: readFileAt(t, filepath.Join(passportDir, "A1", "csca.der"))}},
	}))
	k := NewKeeper(runtime.NewKVStoreService(phStore), encCfg.Codec, ac, authtypes.NewModuleAddress(types.GovModuleName),
		nil, stubDex{}, pki, stubAllocation{}, &burnLog{}, stubShielded{})
	require.NoError(t, k.Params.Set(ctx, leanParams(t)))

	failed := passportMsg(t, "A1") // reached a block, failed in the ante: public, binding unused
	nf, _, err := k.CheckRegistration(ctx, failed)
	require.NoError(t, err)
	p, err := k.checkRegistration(ctx, failed)
	require.NoError(t, err)
	commitment, err := certs.DscCommitmentOf(dscKeyOf(t, "A1"))
	require.NoError(t, err)
	dsc := commitment.Bytes()

	// The holder registered again the same day, to idc B, proven that day.
	live := types.Registration{Nullifier: nf, RegisteredAt: ctx.BlockTime().Unix(), ActivatedAt: ctx.BlockTime().Unix(),
		Idc: privacy.FieldBytes(privacy.U64(424242)), DscKey: dsc[:], ProofDate: p.proofDate}
	require.NoError(t, k.addRegistration(ctx, live))
	_, err = k.checkRegistration(ctx, failed)
	require.ErrorIs(t, err, types.ErrSwitchProofStale, "a same-day proof cannot replace the live registration")
	_, _, err = k.CheckRegistration(ctx, failed)
	require.ErrorIs(t, err, types.ErrSwitchProofStale)

	// Proven a day earlier than the proof, the live registration can be
	// switched from (a holder's own later switch).
	live.ProofDate = p.proofDate - 86400
	require.NoError(t, k.Registrations.Set(ctx, nf, live))
	got, err := k.checkRegistration(ctx, failed)
	require.NoError(t, err)
	require.True(t, got.switched)
}

// replayKeeper is a keeper over a real x/pki trusting A1's CSCA (A2, A3 and
// SALE are signed by A1's signer), at passportTime.
func replayKeeper(t *testing.T) (Keeper, sdk.Context) {
	t.Helper()
	encCfg := moduletestutil.MakeTestEncodingConfig()
	ac := addresscodec.NewBech32Codec(sdk.GetConfig().GetBech32AccountAddrPrefix())
	pkiStore := storetypes.NewKVStoreKey(pkitypes.StoreKey)
	phStore := storetypes.NewKVStoreKey(types.StoreKey)
	ctx := testutil.DefaultContextWithKeys(map[string]*storetypes.KVStoreKey{
		pkitypes.StoreKey: pkiStore, types.StoreKey: phStore,
	}, nil, nil).WithBlockTime(passportTime).WithChainID(shieldedtest.ChainID)
	ctx = shieldedtypes.WithTxFields(ctx, shieldedtypes.TxFields{})
	pki := pkikeeper.NewKeeper(runtime.NewKVStoreService(pkiStore), encCfg.Codec, ac, authtypes.NewModuleAddress(pkitypes.GovModuleName))
	require.NoError(t, pki.InitGenesis(ctx, pkitypes.GenesisState{
		Params: pkitypes.NewParams(),
		Cscas:  []pkitypes.Csca{{CertificateDer: readFileAt(t, filepath.Join(passportDir, "A1", "csca.der"))}},
	}))
	k := NewKeeper(runtime.NewKVStoreService(phStore), encCfg.Codec, ac, authtypes.NewModuleAddress(types.GovModuleName),
		nil, stubDex{}, pki, stubAllocation{}, &burnLog{}, stubShielded{})
	require.NoError(t, k.Params.Set(ctx, leanParams(t)))
	return k, ctx
}

// Audit R2-B1: registration proves knowledge of the idc's secret. The sale:
// seller A, live at A2, switches her passport to buyer K's idc Z (K gave her
// only Z) so that a move along the succession (A2, Z) would hand K her split
// or handle with no way back. Her proof is bound to Z and its notes (the
// binding holds), but the circuit outputs the idc of the secret she proved
// with, and no secret of hers gives Z: refused, by the ante and by gas-check.
// Naming the idc her proof does output breaks the binding instead.
func TestRegistrationNeedsTheIdcSecret(t *testing.T) {
	k, ctx := replayKeeper(t)
	a1 := passportMsg(t, "A1")
	p, err := k.checkRegistration(ctx, a1)
	require.NoError(t, err)
	require.Equal(t, a1.Idc, p.pubInputs[leanParams(t).IdcIndex], "the proof's idc output is the msg's")
	commitment, err := certs.DscCommitmentOf(dscKeyOf(t, "A1"))
	require.NoError(t, err)
	dsc := commitment.Bytes()
	a2 := personhoodtest.Registrations["A2"]
	require.NoError(t, k.addRegistration(ctx, types.Registration{Nullifier: p.nullifier, RegisteredAt: ctx.BlockTime().Unix(),
		ActivatedAt: ctx.BlockTime().Unix(), Idc: privacy.FieldBytes(a2.IDC()), DscKey: dsc[:], ProofDate: p.proofDate - 86400}))

	sale := passportMsg(t, "SALE")
	buyer := personhoodtest.Registrations["SALE"]
	require.Equal(t, privacy.FieldBytes(buyer.IDC()), sale.Idc)
	binding, err := sale.Binding(k.addressCodec, ctx.ChainID())
	require.NoError(t, err)
	require.Equal(t, privacy.FieldBytes(binding), privacy.FieldBytes(mustField(t, sale.PublicSignals[1])), "bound to the buyer's idc")
	_, err = k.checkRegistration(ctx, sale)
	require.ErrorIs(t, err, types.ErrBadPublicInputs)
	require.ErrorContains(t, err, "identity commitment")
	_, _, err = k.CheckRegistration(ctx, sale)
	require.ErrorIs(t, err, types.ErrBadPublicInputs)
	// The proof verifies, so the refusal is the idc: it is what a seller
	// without the buyer's secret can make.
	require.NoError(t, verifyRegistrationProof(sale, preparedRegistration{vk: leanParams(t).VerifyingKeys["lean_poa_p256_sha256"],
		pubInputs: signalsBytes(t, sale.PublicSignals)}))
	ownIdc := privacy.FieldBytes(privacy.IDC(buyer.ProofSecret()))
	require.Equal(t, ownIdc, signalsBytes(t, sale.PublicSignals)[4], "the idc of the secret she proved with")
	sale.Idc = ownIdc
	_, err = k.checkRegistration(ctx, sale)
	require.ErrorIs(t, err, types.ErrBadPublicInputs, "naming her own idc breaks the binding to the buyer's notes")
}

// Audit R2-B1, R2-B2: an idc registered before, by any passport, is refused.
// A, after A1 -> A2 (and a move of her handle and split to A2, which marks
// A1 moved out), switches back to A1's identity with fresh notes (A3; not a
// replay of A1's binding): refused, where it used to strand what had moved
// to A2. The used set is exported and imported, and an import without it is
// refused.
func TestUsedIdcRefused(t *testing.T) {
	k, ctx := replayKeeper(t)
	a1 := passportMsg(t, "A1")
	p, err := k.checkRegistration(ctx, a1)
	require.NoError(t, err)
	commitment, err := certs.DscCommitmentOf(dscKeyOf(t, "A1"))
	require.NoError(t, err)
	dsc := commitment.Bytes()
	idcA1 := privacy.FieldBytes(personhoodtest.Registrations["A1"].IDC())
	idcA2 := privacy.FieldBytes(personhoodtest.Registrations["A2"].IDC())

	// State after A1 -> A2, as a genesis: A's passport live at A2, the
	// succession (A1, A2) at leaf 1, both idcs used.
	gs := types.DefaultGenesis()
	gs.Params = leanParams(t)
	now := ctx.BlockTime().Unix()
	gs.IdentityTreeSize = 3
	gs.Registrations = []types.Registration{{Nullifier: p.nullifier, LeafIndex: 2, RegisteredAt: now, ActivatedAt: now,
		PredecessorAt: now, DscKey: dsc[:], Idc: idcA2, ProofDate: p.proofDate - 86400}}
	gs.Passports = []types.PassportSeen{{Nullifier: p.nullifier, LastIdc: idcA2}}
	gs.Successions = []types.Succession{{LeafIndex: 1, IdcOld: idcA1, IdcNew: idcA2}}
	gs.UsedIdcs = [][]byte{idcA1, idcA2}

	// Without A1's idc in used_idcs the genesis is refused.
	bad := *gs
	bad.UsedIdcs = [][]byte{idcA2}
	require.ErrorContains(t, bad.Validate(), "used_idcs lacks succession")
	bad.UsedIdcs = [][]byte{idcA1, idcA2, idcA1}
	require.ErrorContains(t, bad.Validate(), "listed twice")
	require.NoError(t, k.InitGenesis(ctx, *gs))

	a3 := passportMsg(t, "A3")
	require.Equal(t, idcA1, a3.Idc)
	_, err = k.checkRegistration(ctx, a3)
	require.ErrorIs(t, err, types.ErrIdcUsed, "A -> B -> A")
	_, _, err = k.CheckRegistration(ctx, a3)
	require.ErrorIs(t, err, types.ErrIdcUsed)
	// Not because of the binding: a fresh registration's binding is unused.
	b3, err := a3.Binding(k.addressCodec, ctx.ChainID())
	require.NoError(t, err)
	used, err := k.UsedBindings.Has(ctx, privacy.FieldBytes(b3))
	require.NoError(t, err)
	require.False(t, used)

	// Another passport's first registration to an idc any passport used is
	// refused alike: B's idc, once used elsewhere.
	kB, ctxB := regKeeper(t, stubPki{pubkey: dscKeyOf(t, "B")})
	mB := passportMsg(t, "B")
	_, err = kB.checkRegistration(ctxB, mB)
	require.NoError(t, err)
	require.NoError(t, kB.UsedIdcs.Set(ctxB, mB.Idc))
	_, err = kB.checkRegistration(ctxB, mB)
	require.ErrorIs(t, err, types.ErrIdcUsed)

	// Round trip.
	out, err := k.ExportGenesis(ctx)
	require.NoError(t, err)
	require.ElementsMatch(t, [][]byte{idcA1, idcA2}, out.UsedIdcs)
}

func signalsBytes(t *testing.T, signals []string) [][]byte {
	t.Helper()
	out := make([][]byte, len(signals))
	for i, s := range signals {
		out[i] = privacy.FieldBytes(mustField(t, s))
	}
	return out
}

func mustField(t *testing.T, s string) fr.Element {
	t.Helper()
	e, err := types.ParseSignal(s)
	require.NoError(t, err)
	return e
}

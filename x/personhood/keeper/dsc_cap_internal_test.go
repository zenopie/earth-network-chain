package keeper

import (
	"context"
	"errors"
	"github.com/stretchr/testify/require"
	"testing"
	"time"

	storetypes "cosmossdk.io/store/types"
	addresscodec "github.com/cosmos/cosmos-sdk/codec/address"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/cosmos/cosmos-sdk/testutil"
	sdk "github.com/cosmos/cosmos-sdk/types"
	moduletestutil "github.com/cosmos/cosmos-sdk/types/module/testutil"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"

	"github.com/earth-network/earth/x/personhood/types"
	"github.com/earth-network/earth/x/pki/certs"
	"github.com/earth-network/earth/zk/privacy"
)

// revocablePki is a PkiKeeper whose revocation set the test drives.
type revocablePki struct{ revoked map[string]bool }

func (p *revocablePki) VerifyDscIssuer(context.Context, []byte) (*certs.PublicKey, string, error) {
	return nil, "", nil
}
func (p *revocablePki) IsCommitmentRevoked(_ context.Context, c []byte) (bool, error) {
	return p.revoked[string(c)], nil
}

func capKeeper(t *testing.T) (Keeper, *revocablePki, sdk.Context) {
	t.Helper()
	encCfg := moduletestutil.MakeTestEncodingConfig()
	storeKey := storetypes.NewKVStoreKey(types.StoreKey)
	base := testutil.DefaultContextWithDB(t, storeKey, storetypes.NewTransientStoreKey("transient_test")).Ctx
	pki := &revocablePki{revoked: map[string]bool{}}
	k := NewKeeper(
		runtime.NewKVStoreService(storeKey),
		encCfg.Codec,
		addresscodec.NewBech32Codec(sdk.GetConfig().GetBech32AccountAddrPrefix()),
		authtypes.NewModuleAddress(types.GovModuleName),
		nil, stubDex{}, pki, stubAllocation{}, &burnLog{}, stubShielded{},
	)
	ctx := base.WithBlockTime(time.Unix(1_700_000_000, 0).UTC())
	if err := k.Params.Set(ctx, types.DefaultParams()); err != nil {
		t.Fatal(err)
	}
	return k, pki, ctx
}

var testDsc = []byte("document-signer-commitment------")

// register simulates the rate-limited part of a registration: check, then count.
func register(t *testing.T, k Keeper, ctx sdk.Context, dsc []byte, country string) error {
	t.Helper()
	if err := k.checkRegistrationRate(ctx, dsc, country); err != nil {
		return err
	}
	if err := k.recordRegistrationRate(ctx, dsc, country); err != nil {
		t.Fatal(err)
	}
	return nil
}

// TestDscCapBoundsACompromisedSigner is the finding this closes. A stolen
// signing key can mint unlimited valid proofs; the cap is what makes the damage
// before governance reacts a finite number rather than whatever the attacker
// managed.
func TestDscCapBoundsACompromisedSigner(t *testing.T) {
	k, _, ctx := capKeeper(t)

	accepted := 0
	for i := 0; i < types.DefaultDscDailyRegistrationFloor+500; i++ {
		if err := register(t, k, ctx, testDsc, "UT"); err != nil {
			break
		}
		accepted++
	}
	if accepted != types.DefaultDscDailyRegistrationFloor {
		t.Fatalf("accepted %d registrations from one signer, cap is %d",
			accepted, types.DefaultDscDailyRegistrationFloor)
	}
	if err := register(t, k, ctx, testDsc, "UT"); err == nil {
		t.Fatal("signer past its daily cap was still accepted")
	}
}

// TestCapIsADeferralNotABan: the day rolls and the signer works again. The
// alternative — suspending the signer until governance acts — would let one
// stolen key lock a whole country out of the chain for the length of a vote.
func TestCapIsADeferralNotABan(t *testing.T) {
	k, _, ctx := capKeeper(t)
	for i := 0; i < types.DefaultDscDailyRegistrationFloor; i++ {
		if err := register(t, k, ctx, testDsc, "UT"); err != nil {
			t.Fatalf("rejected at %d, below the cap: %v", i, err)
		}
	}
	if err := register(t, k, ctx, testDsc, "UT"); err == nil {
		t.Fatal("expected the cap to bite")
	}

	tomorrow := ctx.WithBlockTime(ctx.BlockTime().Add(24 * time.Hour))
	if err := register(t, k, tomorrow, testDsc, "UT"); err != nil {
		t.Fatalf("still refused after the day rolled: %v", err)
	}
}

// TestCapWidensWithTheNetwork: the share term has to lift the ceiling as
// adoption grows, or governance is stuck raising a constant forever.
func TestCapWidensWithTheNetwork(t *testing.T) {
	params := types.DefaultParams()

	atGenesis := params.DscDailyCap(0)
	if atGenesis != types.DefaultDscDailyRegistrationFloor {
		t.Fatalf("with no network history the floor should govern, got %d", atGenesis)
	}
	// 100k registrations yesterday: 25% of that is well past the floor.
	grown := params.DscDailyCap(100_000)
	if grown != 25_000 {
		t.Fatalf("cap should widen to the share of a large network, got %d", grown)
	}
	if grown < atGenesis {
		t.Fatal("the share term must never tighten the cap below the floor")
	}
}

// TestCountryCapCatchesAMintedSignerFleet is why the per-signer cap is not
// enough on its own. A compromised CSCA can mint fresh Document Signers at will,
// and each arrives with a full unused allowance — so bounding the signer alone
// bounds nothing.
func TestCountryCapCatchesAMintedSignerFleet(t *testing.T) {
	k, _, ctx := capKeeper(t)

	accepted := 0
	for sig := 0; sig < 100; sig++ {
		// A brand new signer for every batch, exactly as a compromised CSCA
		// would produce.
		dsc := append([]byte("minted-signer-"), byte(sig/10), byte(sig%10))
		for i := 0; i < types.DefaultDscDailyRegistrationFloor; i++ {
			if err := register(t, k, ctx, dsc, "UT"); err != nil {
				goto done
			}
			accepted++
		}
	}
done:
	if accepted != types.DefaultCountryDailyRegistrationFloor {
		t.Fatalf("a fleet of fresh signers registered %d; the country cap is %d",
			accepted, types.DefaultCountryDailyRegistrationFloor)
	}
}

// seedReg writes a registration with its identity leaf, as Register does.
func seedReg(t *testing.T, k Keeper, ctx sdk.Context, i int, dsc []byte, at int64) types.Registration {
	t.Helper()
	idc := privacy.FieldBytes(privacy.U64(uint64(i + 1)))
	dscField := privacy.FieldBytes(privacy.H(privacy.Bytes(dsc)))
	leaf, err := IdentityLeaf(idc, dscField, "", at, 0)
	require.NoError(t, err)
	idx, err := k.appendLeaf(ctx, leaf)
	require.NoError(t, err)
	reg := types.Registration{
		Nullifier: []byte{byte(i / 256), byte(i % 256), 'n'}, LeafIndex: idx,
		RegisteredAt: at, ActivatedAt: at, DscKey: dsc, Idc: idc,
	}
	require.NoError(t, k.addRegistration(ctx, reg))
	return reg
}

// TestPurgeZeroesLeavesInBoundedBatches: a revoked signer's registrations are
// retired a bounded batch per block, each leaf zeroed, until none remain.
func TestPurgeZeroesLeavesInBoundedBatches(t *testing.T) {
	k, _, ctx := capKeeper(t)
	const total = 250
	for i := 0; i < total; i++ {
		seedReg(t, k, ctx, i, testDsc, ctx.BlockTime().Unix())
	}
	other := seedReg(t, k, ctx, total, []byte("other-signer"), ctx.BlockTime().Unix())
	require.NoError(t, k.StartDscPurge(ctx, testDsc))

	used, err := k.purgeRevokedDscs(ctx, types.DefaultRegistrationSweepLimit)
	require.NoError(t, err)
	require.Equal(t, types.DefaultRegistrationSweepLimit, used)
	for i := 0; i < 10; i++ {
		_, err := k.purgeRevokedDscs(ctx, types.DefaultRegistrationSweepLimit)
		require.NoError(t, err)
	}
	require.Equal(t, 1, countRegistrations(t, k, ctx))
	for i := uint64(0); i < total; i++ {
		l, err := k.IdentityLeafAt(ctx, i)
		require.NoError(t, err)
		require.True(t, l.IsZero(), "leaf %d", i)
	}
	l, err := k.IdentityLeafAt(ctx, other.LeafIndex)
	require.NoError(t, err)
	require.False(t, l.IsZero(), "another signer's leaf stays")
	has, err := k.PendingDscPurge.Has(ctx, testDsc)
	require.NoError(t, err)
	require.False(t, has, "drained signer left in the pending purge set")
	n, err := k.getRegCount(ctx)
	require.NoError(t, err)
	require.Equal(t, uint64(1), n)
}

// TestSweepBudgetIsSharedAcrossBothReasons is the BeginBlocker bound: the
// purge, the expiry sweep and the caretaker sweep share one budget.
func TestSweepBudgetIsSharedAcrossBothReasons(t *testing.T) {
	k, _, ctx := capKeeper(t)
	const total = 400
	for i := 0; i < total; i++ {
		seedReg(t, k, ctx, i, testDsc, ctx.BlockTime().Unix())
	}
	require.NoError(t, k.StartDscPurge(ctx, testDsc))
	later := ctx.WithBlockTime(ctx.BlockTime().Add(400 * 24 * time.Hour))
	before := countRegistrations(t, k, later)
	require.NoError(t, k.BeginBlocker(later))
	retired := before - countRegistrations(t, k, later)
	require.LessOrEqual(t, retired, types.DefaultRegistrationSweepLimit)
	require.Positive(t, retired)
}

func countRegistrations(t *testing.T, k Keeper, ctx sdk.Context) int {
	t.Helper()
	n := 0
	require.NoError(t, k.Registrations.Walk(ctx, nil, func([]byte, types.Registration) (bool, error) {
		n++
		return false, nil
	}))
	return n
}

// TestEmptyCountryIsCapped: a signer whose issuer names no country used to skip
// the country cap entirely. It now shares the UnknownCountry bucket.
func TestEmptyCountryIsCapped(t *testing.T) {
	k, _, ctx := capKeeper(t)
	p, _ := k.Params.Get(ctx)
	p.CountryDailyRegistrationFloor = 3
	p.DscDailyRegistrationFloor = 100
	if err := k.Params.Set(ctx, p); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		dsc := []byte{byte(i)} // a fresh signer each time: only the country cap can bind
		if err := register(t, k, ctx, dsc, ""); err != nil {
			t.Fatalf("registration %d: %v", i, err)
		}
	}
	if err := register(t, k, ctx, []byte{9}, ""); !errors.Is(err, types.ErrRegistrationRateLimited) {
		t.Fatalf("4th no-country registration = %v, want rate limited", err)
	}
}

// TestNetworkCapBoundsManyCountries: each country has its own allowance, so
// the sum across countries was unbounded until the network cap.
func TestNetworkCapBoundsManyCountries(t *testing.T) {
	k, _, ctx := capKeeper(t)
	p, _ := k.Params.Get(ctx)
	p.NetworkDailyRegistrationFloor = 5
	if err := k.Params.Set(ctx, p); err != nil {
		t.Fatal(err)
	}
	countries := []string{"AA", "BB", "CC", "DD", "EE"}
	for i, c := range countries {
		if err := register(t, k, ctx, []byte{byte(i)}, c); err != nil {
			t.Fatalf("%s: %v", c, err)
		}
	}
	if err := register(t, k, ctx, []byte{42}, "FF"); !errors.Is(err, types.ErrRegistrationRateLimited) {
		t.Fatalf("6th registration network-wide = %v, want rate limited", err)
	}

	// Tomorrow the cap is max(floor, 3x today's 5) = 15.
	tomorrow := ctx.WithBlockTime(ctx.BlockTime().Add(24 * time.Hour))
	for i := 0; i < 15; i++ {
		if err := register(t, k, tomorrow, []byte{byte(100 + i)}, "C"+string(rune('A'+i))); err != nil {
			t.Fatalf("day two, registration %d: %v", i, err)
		}
	}
	if err := register(t, k, tomorrow, []byte{200}, "ZZ"); !errors.Is(err, types.ErrRegistrationRateLimited) {
		t.Fatalf("day two, 16th = %v, want rate limited", err)
	}
}

// TestLiveRegistrationIsASwitch: a nullifier with an unexpired registration is
// a wallet switch, which Register exempts from the daily caps.
func TestLiveRegistrationIsASwitch(t *testing.T) {
	k, _, ctx := capKeeper(t)
	null := []byte("nullifier-of-someone-registered-")
	if live, err := k.isLiveRegistration(ctx, null); err != nil || live {
		t.Fatalf("unknown nullifier: live=%v err=%v", live, err)
	}
	if err := k.Registrations.Set(ctx, null, types.Registration{Nullifier: null, RegisteredAt: ctx.BlockTime().Unix()}); err != nil {
		t.Fatal(err)
	}
	if live, err := k.isLiveRegistration(ctx, null); err != nil || !live {
		t.Fatalf("fresh registration: live=%v err=%v", live, err)
	}
	p, _ := k.Params.Get(ctx)
	later := ctx.WithBlockTime(ctx.BlockTime().Add(time.Duration(p.RegistrationValiditySeconds+1) * time.Second))
	if live, err := k.isLiveRegistration(later, null); err != nil || live {
		t.Fatalf("lapsed registration: live=%v err=%v", live, err)
	}
}

package keeper

import (
	"context"
	"errors"
	"testing"
	"time"

	"cosmossdk.io/collections"
	storetypes "cosmossdk.io/store/types"
	addresscodec "github.com/cosmos/cosmos-sdk/codec/address"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/cosmos/cosmos-sdk/testutil"
	sdk "github.com/cosmos/cosmos-sdk/types"
	moduletestutil "github.com/cosmos/cosmos-sdk/types/module/testutil"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"

	"github.com/earth-network/earth/x/personhood/types"
	"github.com/earth-network/earth/x/pki/certs"
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
		nil, stubDex{}, pki, stubAllocation{}, &burnLog{},
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

// TestRevocationStopsTheAnmlImmediately covers the lazy half of the response.
// The daily claim re-reads the registration, so revocation has to bite there at
// once — the purge sweep is bounded per block, and every block a large signer
// takes to work through would otherwise be another day's ANML.
func TestRevocationStopsTheAnmlImmediately(t *testing.T) {
	k, pki, ctx := capKeeper(t)
	addr := sdk.AccAddress("human_______________")
	nullifier := []byte("nullifier-1")

	reg := types.Registration{
		Nullifier:    nullifier,
		Address:      addr.String(),
		RegisteredAt: ctx.BlockTime().Unix(),
		DscKey:       testDsc,
	}
	if err := k.Registrations.Set(ctx, nullifier, reg); err != nil {
		t.Fatal(err)
	}
	if err := k.RegByAddr.Set(ctx, addr.Bytes(), nullifier); err != nil {
		t.Fatal(err)
	}

	if _, err := k.requireValidHuman(ctx, addr); err != nil {
		t.Fatalf("a live registration should be valid: %v", err)
	}

	pki.revoked[string(testDsc)] = true

	if _, err := k.requireValidHuman(ctx, addr); err == nil {
		t.Fatal("registration under a revoked signer is still being paid")
	}
}

// TestPurgeRetiresWeightInBoundedBatches covers the half that cannot be lazy.
// A stream stores its total weight and only moves it when a voter is explicitly
// cleared, so revoked registrations have to actually be walked and retired — and
// that walk has to fit in a block.
func TestPurgeRetiresWeightInBoundedBatches(t *testing.T) {
	k, _, ctx := capKeeper(t)

	const total = 250
	for i := 0; i < total; i++ {
		nullifier := []byte{byte(i / 256), byte(i % 256), 'n'}
		addr := sdk.AccAddress([]byte{byte(i / 256), byte(i % 256), 'a', 'd', 'd', 'r'})
		reg := types.Registration{
			Nullifier:    nullifier,
			Address:      addr.String(),
			RegisteredAt: ctx.BlockTime().Unix(),
			DscKey:       testDsc,
		}
		if err := k.Registrations.Set(ctx, nullifier, reg); err != nil {
			t.Fatal(err)
		}
		if err := k.RegByAddr.Set(ctx, addr.Bytes(), nullifier); err != nil {
			t.Fatal(err)
		}
		if err := k.RegByDsc.Set(ctx, collections.Join(testDsc, nullifier)); err != nil {
			t.Fatal(err)
		}
	}
	if err := k.StartDscPurge(ctx, testDsc); err != nil {
		t.Fatal(err)
	}

	// One block must retire at most the budget, not all 250.
	used, err := k.purgeRevokedDscs(ctx, types.DefaultRegistrationSweepLimit)
	if err != nil {
		t.Fatal(err)
	}
	if used != types.DefaultRegistrationSweepLimit {
		t.Fatalf("first block retired %d, want the full budget %d", used, types.DefaultRegistrationSweepLimit)
	}

	// And the backlog must drain over following blocks rather than stall.
	for i := 0; i < 10; i++ {
		if _, err := k.purgeRevokedDscs(ctx, types.DefaultRegistrationSweepLimit); err != nil {
			t.Fatal(err)
		}
	}
	remaining := 0
	if err := k.RegByDsc.Walk(ctx, nil, func(collections.Pair[[]byte, []byte]) (bool, error) {
		remaining++
		return false, nil
	}); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatalf("%d registrations still held by the revoked signer", remaining)
	}

	// The signer drops out of the pending set once it is clean, so the sweep
	// stops costing anything.
	if has, err := k.PendingDscPurge.Has(ctx, testDsc); err != nil {
		t.Fatal(err)
	} else if has {
		t.Fatal("drained signer left in the pending purge set")
	}
}

// TestSweepBudgetIsSharedAcrossBothReasons is the BeginBlocker bound. Nothing
// meters this work — BeginBlock runs on an infinite gas meter and consumes no
// block gas — so the two sweeps must come out of one budget rather than two that
// happen to sum to something nobody chose.
func TestSweepBudgetIsSharedAcrossBothReasons(t *testing.T) {
	k, _, ctx := capKeeper(t)

	const total = 400
	for i := 0; i < total; i++ {
		nullifier := []byte{byte(i / 256), byte(i % 256), 'n'}
		addr := sdk.AccAddress([]byte{byte(i / 256), byte(i % 256), 'a', 'd', 'd', 'r'})
		reg := types.Registration{
			Nullifier: nullifier, Address: addr.String(),
			RegisteredAt: ctx.BlockTime().Unix(), DscKey: testDsc,
		}
		if err := k.Registrations.Set(ctx, nullifier, reg); err != nil {
			t.Fatal(err)
		}
		if err := k.RegByAddr.Set(ctx, addr.Bytes(), nullifier); err != nil {
			t.Fatal(err)
		}
		if err := k.RegByDsc.Set(ctx, collections.Join(testDsc, nullifier)); err != nil {
			t.Fatal(err)
		}
		if err := k.RegByRegisteredAt.Set(ctx, collections.Join(reg.RegisteredAt, nullifier)); err != nil {
			t.Fatal(err)
		}
	}
	if err := k.StartDscPurge(ctx, testDsc); err != nil {
		t.Fatal(err)
	}

	// Far enough forward that every registration is also long expired, so both
	// sweeps have unlimited work available and only the budget holds them back.
	later := ctx.WithBlockTime(ctx.BlockTime().Add(400 * 24 * time.Hour))

	before := countRegistrations(t, k, later)
	if err := k.BeginBlocker(later); err != nil {
		t.Fatal(err)
	}
	retired := before - countRegistrations(t, k, later)

	if retired > types.DefaultRegistrationSweepLimit {
		t.Fatalf("one block retired %d registrations against a shared budget of %d — "+
			"the sweeps are not sharing a bound", retired, types.DefaultRegistrationSweepLimit)
	}
}

func countRegistrations(t *testing.T, k Keeper, ctx sdk.Context) int {
	t.Helper()
	n := 0
	if err := k.Registrations.Walk(ctx, nil, func([]byte, types.Registration) (bool, error) {
		n++
		return false, nil
	}); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestRevocationStopsTheVoteImmediately is the franchise's version of the
// test above. The chamber asks LiveNullifier when a vote is cast and the
// allocation stream asks Weight; both have to see the revocation before the
// purge reaches the registration, which may be many blocks later.
func TestRevocationStopsTheVoteImmediately(t *testing.T) {
	k, pki, ctx := capKeeper(t)
	addr := sdk.AccAddress("voter_______________")
	nullifier := []byte("nullifier-v")
	reg := types.Registration{
		Nullifier:    nullifier,
		Address:      addr.String(),
		RegisteredAt: ctx.BlockTime().Unix(),
		DscKey:       testDsc,
	}
	if err := k.Registrations.Set(ctx, nullifier, reg); err != nil {
		t.Fatal(err)
	}
	if err := k.RegByAddr.Set(ctx, addr.Bytes(), nullifier); err != nil {
		t.Fatal(err)
	}

	check := func(wantLive bool) {
		t.Helper()
		_, live, err := k.LiveNullifier(ctx, addr)
		if err != nil || live != wantLive {
			t.Fatalf("LiveNullifier = %v, %v; want %v", live, err, wantLive)
		}
		w, err := k.Weight(ctx, addr)
		if err != nil || w.IsPositive() != wantLive {
			t.Fatalf("Weight = %s, %v; want positive=%v", w, err, wantLive)
		}
	}
	check(true)
	pki.revoked[string(testDsc)] = true
	check(false)
}

type recordingListener struct{ retired [][]byte }

func (l *recordingListener) OnRegistrationRetired(_ context.Context, nullifier []byte) error {
	l.retired = append(l.retired, nullifier)
	return nil
}

// TestRetirementIsAnnounced: every registration the revocation purge and the
// expiry sweep retire is announced to the listeners, which is how x/assembly
// takes back the votes filed under it.
func TestRetirementIsAnnounced(t *testing.T) {
	k, _, ctx := capKeeper(t)
	listener := &recordingListener{}
	k.RegisterRetirementListener(listener)

	add := func(i int, dsc []byte, at int64) {
		nullifier := []byte{byte(i), 'n'}
		addr := sdk.AccAddress([]byte{byte(i), 'a', 'd', 'd', 'r'})
		reg := types.Registration{Nullifier: nullifier, Address: addr.String(), RegisteredAt: at, DscKey: dsc}
		if err := k.Registrations.Set(ctx, nullifier, reg); err != nil {
			t.Fatal(err)
		}
		if err := k.RegByAddr.Set(ctx, addr.Bytes(), nullifier); err != nil {
			t.Fatal(err)
		}
		if err := k.RegByDsc.Set(ctx, collections.Join(dsc, nullifier)); err != nil {
			t.Fatal(err)
		}
		if err := k.RegByRegisteredAt.Set(ctx, collections.Join(at, nullifier)); err != nil {
			t.Fatal(err)
		}
	}
	now := ctx.BlockTime().Unix()
	add(1, testDsc, now)              // revoked
	add(2, []byte("other-signer"), 0) // long expired
	add(3, []byte("other-signer"), now)

	if err := k.StartDscPurge(ctx, testDsc); err != nil {
		t.Fatal(err)
	}
	if _, err := k.purgeRevokedDscs(ctx, types.DefaultRegistrationSweepLimit); err != nil {
		t.Fatal(err)
	}
	if _, err := k.sweepExpiredRegistrations(ctx, types.DefaultRegistrationSweepLimit); err != nil {
		t.Fatal(err)
	}

	got := map[string]bool{}
	for _, n := range listener.retired {
		got[string(n)] = true
	}
	if len(got) != 2 || !got[string([]byte{1, 'n'})] || !got[string([]byte{2, 'n'})] {
		t.Fatalf("announced %q, want the revoked and the expired registration only", listener.retired)
	}
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

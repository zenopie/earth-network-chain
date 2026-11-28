package app

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"cosmossdk.io/collections"
	upgradetypes "cosmossdk.io/x/upgrade/types"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	"github.com/stretchr/testify/require"

	personhoodtest "github.com/earth-network/earth/x/personhood/testutil"
	personhoodtypes "github.com/earth-network/earth/x/personhood/types"
	shieldedtest "github.com/earth-network/earth/x/shielded/testutil"
	shieldedtypes "github.com/earth-network/earth/x/shielded/types"
	"github.com/earth-network/earth/zk/privacy"
)

// claimDays builds a MsgClaimAnmlDays for first..day by registration reg,
// proven (claim circuit) against the current identity tree. maxAct < 0
// proves the chain's own bound; prove false leaves placeholder proofs, for a
// msg the chain must refuse before verifying anything.
func (e *phEnv) claimDays(name, reg string, first, day, maxAct int64, prove bool) *personhoodtypes.MsgClaimAnmlDays {
	e.t.Helper()
	f := e.feeFor("claimdays/" + name)
	msg := &personhoodtypes.MsgClaimAnmlDays{Fee: e.bundle(f), FirstDay: uint64(first), Day: uint64(day),
		Pc: privacy.FieldBytes(claimPC(name).PC()), Ciphertext: ct(name, 21)}
	if maxAct < 0 {
		maxAct = (first - 1) * phDay
	}
	r := personhoodtest.Registrations[reg]
	rg, err := e.app.PersonhoodKeeper.Registrations.Get(e.ctx(), loadPassport(e.t, reg).nf)
	require.NoError(e.t, err, "registration %s", reg)
	tree := e.identityTree()
	sib, err := tree.Path(rg.LeafIndex)
	require.NoError(e.t, err)
	root, err := tree.Root()
	require.NoError(e.t, err)
	dsc, err := privacy.FieldFromBytes(rg.DscKey)
	require.NoError(e.t, err)
	var scopes []fr.Element
	for d := first; d <= day; d++ {
		scopes = append(scopes, privacy.ClaimScope(uint64(d)))
	}
	w := personhoodtest.Claim{
		IDSecret: r.IDSecret(), DscKey: dsc, Country: privacy.CountryField(rg.Country), ActivatedAt: uint64(rg.ActivatedAt),
		PredecessorAt: uint64(rg.PredecessorAt), LeafIndex: rg.LeafIndex, Root: root, Siblings: sib, Scopes: scopes,
		MaxActivation: personhoodtypes.BoundInput(maxAct),
	}
	for _, nf := range w.Nullifiers() {
		msg.Claim.Nullifiers = append(msg.Claim.Nullifiers, privacy.FieldBytes(nf))
	}
	msg.Claim.Root = privacy.FieldBytes(root)
	if !prove {
		msg.Fee = e.unverified(f)
		msg.Claim.Proof = make([]byte, shieldedtypes.ProofBytes)
		return msg
	}
	ac := e.app.AuthKeeper.AddressCodec()
	w.Signal, err = shieldedtypes.Sighash(msg, shieldedtest.ChainID, phTx, ac)
	require.NoError(e.t, err)
	require.NoError(e.t, shieldedtest.ProveMsg(msg, shieldedtest.ChainID, phTx, ac, []*shieldedtest.Plan{f}, e.actions.TryProve), name)
	toml, pub := w.Witness()
	msg.Claim.Proof, err = e.prover.Proof("claimdays/"+name+".claim", "claim", toml, pub)
	require.NoError(e.t, err, name)
	return msg
}

// claimsOn lists the claim nullifiers recorded for day.
func (e *phEnv) claimsOn(day int64) int {
	e.t.Helper()
	n := 0
	rng := collections.NewPrefixedPairRange[uint64, []byte](uint64(day))
	require.NoError(e.t, e.app.PersonhoodKeeper.ClaimNullifiers.Walk(e.ctx(), rng, func(collections.Pair[uint64, []byte]) (bool, error) {
		n++
		return false, nil
	}))
	return n
}

// TestClaimAnmlDays: a run of unclaimed days ending today, claimed with one
// proof, mints one note of one ANML per day and records each day's
// nullifier. The first claim's delay holds: the run's activation bound is its
// first day's. MsgClaimAnml is retired.
func TestClaimAnmlDays(t *testing.T) {
	e := initPersonhoodEnv(t)
	k := e.app.PersonhoodKeeper
	// The env's registrations lapse after four days; these claims span two
	// weeks.
	params, err := k.Params.Get(e.ctx())
	require.NoError(t, err)
	params.RegistrationValiditySeconds = 30 * 86400
	require.NoError(t, k.Params.Set(e.ctx(), params))
	e.mustDeliver(e.register("A1"))
	a1, ok := e.registration("A1")
	require.True(t, ok)
	require.Equal(t, phDay0, a1.ActivatedAt/phDay, "registered on day 0: first claim on day 2")

	// Day 5: days 2..5, four ANML in one note, one claim_anml event a day.
	d := phDay0 + 5
	e.at(dayStart(d).Add(time.Hour))
	// The first claim's delay: a run from day 1 proves an identity activated
	// by the end of day 0, which A1 is, but the chain bounds the run by its
	// first day (activated before day 0 began), and refuses the proof.
	r := e.checkTx(e.tx(e.claimDays("A-1-5", "A1", phDay0+1, d, a1.ActivatedAt, true)))
	require.Equal(t, personhoodtypes.ErrInvalidMembership.ABCICode(), r.Code, r.Log)

	run := e.claimDays("A-2-5", "A1", phDay0+2, d, -1, true)
	fb := e.mustDeliver(run)
	res := fb.TxResults[0]
	require.True(t, hasCommitment(res, privacy.CM(privacy.AssetID("uanml"), 4_000_000, claimPC("A-2-5").PC())), "4 ANML in one note")
	evs := eventsOf(res.Events, "claim_anml")
	require.Len(t, evs, 4)
	for i, ev := range evs {
		require.Equal(t, fmt.Sprint(phDay0+2+int64(i)), ev["day"])
		require.Equal(t, evs[0]["position"], ev["position"])
	}
	for day := phDay0 + 2; day <= d; day++ {
		require.Equal(t, 1, e.claimsOn(day), "day %d", day)
	}

	// Those days are claimed: the run again and a shorter run inside it are
	// refused before any proof, naming the last claimed day.
	for _, first := range []int64{phDay0 + 2, phDay0 + 5} {
		r = e.checkTx(e.tx(e.claimDays(fmt.Sprintf("again-%d", first), "A1", first, d, -1, false)))
		require.Equal(t, personhoodtypes.ErrDayClaimed.ABCICode(), r.Code, r.Log)
	}
	// MsgClaimAnml is retired: refused before any proof, nothing charged.
	retired := &personhoodtypes.MsgClaimAnml{Fee: e.unverified(e.feeFor("retired")), Day: uint64(d + 1),
		Membership: personhoodtypes.Membership{Proof: make([]byte, shieldedtypes.ProofBytes), Root: run.Claim.Root, Nullifier: privacy.FieldBytes(privacy.U64(77))},
		Pc:         privacy.FieldBytes(claimPC("retired").PC()), Ciphertext: ct("retired", 20)}
	r = e.checkTx(e.tx(retired))
	require.Equal(t, personhoodtypes.ErrClaimRetired.ABCICode(), r.Code, r.Log)

	// Day 7: a run ending yesterday is refused; one of eight days is not a
	// msg at all.
	d = phDay0 + 7
	e.at(dayStart(d).Add(time.Hour))
	r = e.checkTx(e.tx(e.claimDays("not-today", "A1", phDay0+6, d-1, -1, false)))
	require.Equal(t, personhoodtypes.ErrWrongDay.ABCICode(), r.Code, r.Log)
	long := e.claimDays("eight", "A1", phDay0+6, d, -1, false)
	long.FirstDay = uint64(d - 7)
	long.Claim.Nullifiers = append(long.Claim.Nullifiers, make([][]byte, 6)...)
	require.ErrorIs(t, long.ValidateBasic(), personhoodtypes.ErrInvalidMsg)

	// Days 6..7: two ANML.
	fb = e.mustDeliver(e.claimDays("A-6-7", "A1", phDay0+6, d, -1, true))
	require.True(t, hasCommitment(fb.TxResults[0], privacy.CM(privacy.AssetID("uanml"), 2_000_000, claimPC("A-6-7").PC())))

	// The claim floor (set by v1.4.0 to the day before it ran): no run
	// reaches before it, however short the run.
	d = phDay0 + 10
	e.at(dayStart(d).Add(time.Hour))
	require.NoError(t, k.ClaimFloorDay.Set(e.ctx(), uint64(d-1)))
	r = e.checkTx(e.tx(e.claimDays("below-floor", "A1", d-2, d, -1, false)))
	require.Equal(t, personhoodtypes.ErrWrongDay.ABCICode(), r.Code, r.Log)
	gs, err := k.ExportGenesis(e.ctx())
	require.NoError(t, err)
	require.Equal(t, uint64(d-1), gs.ClaimFloorDay, "exported")

	// Claim nullifiers are kept for the window and a day: on day 15, days
	// before 8 are gone, day 8 (the margin day) on is kept.
	require.NoError(t, k.ClaimNullifiers.Set(e.ctx(), collections.Join(uint64(phDay0+8), privacy.FieldBytes(privacy.U64(8)))))
	e.at(dayStart(phDay0 + 15).Add(time.Hour))
	e.finalize()
	require.Zero(t, e.claimsOn(phDay0+7))
	require.Zero(t, e.claimsOn(phDay0+2))
	require.Equal(t, 1, e.claimsOn(phDay0+8))
	require.NoError(t, e.app.ShieldedKeeper.AssertInvariants(e.ctx()))
}

// The v1.4.0 handler installs the claim key and sets the claim floor to the
// day before it ran: the oldest day whose nullifiers the two-day prune kept.
func TestUpgradeV1_4_0(t *testing.T) {
	e := initShieldedEnv(t)
	params, err := e.app.ShieldedKeeper.Params.Get(e.ctx())
	require.NoError(t, err)
	delete(params.VerifyingKeys, shieldedtypes.CircuitClaim)
	require.NoError(t, e.app.ShieldedKeeper.Params.Set(e.ctx(), params))

	ctx := e.ctx()
	_, err = v1_4_0Handler(e.app)(ctx, upgradetypes.Plan{Name: UpgradeV1_4_0}, e.app.ModuleManager.GetVersionMap())
	require.NoError(t, err)

	params, err = e.app.ShieldedKeeper.Params.Get(e.ctx())
	require.NoError(t, err)
	// The embedded key is the one the real-proof tests verify against.
	want, err := os.ReadFile(filepath.Join("..", "x", "personhood", "testdata", "app", "claim.vk"))
	require.NoError(t, err)
	require.Equal(t, want, params.VerifyingKeys[shieldedtypes.CircuitClaim])
	floor, err := e.app.PersonhoodKeeper.ClaimFloorDay.Get(e.ctx())
	require.NoError(t, err)
	require.Equal(t, uint64(e.ctx().BlockTime().Unix()/personhoodtypes.SecondsPerDay-1), floor)
}

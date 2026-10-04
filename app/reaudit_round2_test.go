package app

import (
	"fmt"
	"testing"
	"time"

	"cosmossdk.io/collections"
	storetypes "cosmossdk.io/store/types"
	sdk "github.com/cosmos/cosmos-sdk/types"

	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	txtypes "github.com/cosmos/cosmos-sdk/types/tx"
	"github.com/stretchr/testify/require"

	shieldedtypes "github.com/earth-network/earth/x/shielded/types"
	sstypes "github.com/earth-network/earth/x/shieldedstaking/types"
)

// Re-audit R7 ("one tx encoding"): a relayer re-spelling an unsigned private
// tx without touching anything the sighash binds (a deprecated AuthInfo.tip,
// a non-critical unknown field appended to the body, one inside the msg) is
// refused, in CheckTx and in a block. Only the canonical encoding lands.
func TestPrivateTxRespellingsRefused(t *testing.T) {
	e := initDexEnv(t)
	e.shield(uint64(10_000 * ssErth))
	e.shield(uint64(100 * ssErth))
	erth := e.w.unspent("uerth", uint64(1_000*ssErth))
	anml, _ := e.noteSwap(erth, uint64(1_000*ssErth), "uanml", 1, 0)
	_ = e.buildLegs(ssFee, leg{anml, 5})
	in := e.w.unspent("uerth", uint64(ssErth))
	dm, _, _ := e.delegateMsg(e.genesisValidator(), in, uint64(ssErth))
	orig := e.privateTx(dm)

	var raw txtypes.TxRaw
	require.NoError(t, raw.Unmarshal(orig))
	var ai txtypes.AuthInfo
	require.NoError(t, ai.Unmarshal(raw.AuthInfoBytes))
	ai.Tip = &txtypes.Tip{Tipper: "x"} //nolint:staticcheck
	tipped := raw
	tipped.AuthInfoBytes, _ = ai.Marshal()
	tbz, _ := tipped.Marshal()

	// field 1025, varint: tag (1025<<3)|0 = 0x88 0x40, value 1
	padded := raw
	padded.BodyBytes = append(append([]byte(nil), raw.BodyBytes...), 0x88, 0x40, 0x01)
	pbz, _ := padded.Marshal()

	// the same unknown field inside the msg's Any value
	var body txtypes.TxBody
	require.NoError(t, body.Unmarshal(raw.BodyBytes))
	body.Messages[0].Value = append(append([]byte(nil), body.Messages[0].Value...), 0x88, 0x40, 0x01)
	inner := raw
	inner.BodyBytes, _ = body.Marshal()
	ibz, _ := inner.Marshal()

	for name, bz := range map[string][]byte{"tip": tbz, "body-noncritical": pbz, "msg-noncritical": ibz} {
		res := e.checkTx(bz)
		require.NotZero(t, res.Code, "%s: %s", name, res.Log)
		require.Contains(t, []uint32{sdkerrors.ErrInvalidRequest.ABCICode(), sdkerrors.ErrTxDecode.ABCICode()}, res.Code, "%s: %s", name, res.Log)
		r := e.run(bz)
		require.NotZero(t, r.Code, "%s delivered: %s", name, r.Log)
	}
	res := e.checkTx(orig)
	require.Zero(t, res.Code, res.Log)
	r := e.run(orig)
	require.Zero(t, r.Code, r.Log)
}

// Re-audit R2 (CheckTx bypass): a valid fee bundle (reusable: the tx never
// lands, so its nullifiers stay unspent) next to a junk stake proof. The
// msg's own proof is verified first, and proofs CheckTx saw verify are
// remembered: each junk attempt costs one verification, however many actions
// the bundles hold. The mirror image (a valid stake proof, a junk bundle
// proof) costs at most one new verification per attempt too.
func TestCheckTxValidBundleJunkActionProofCostsOne(t *testing.T) {
	e := initDexEnv(t)
	e.shield(uint64(10_000 * ssErth))
	e.shield(uint64(100 * ssErth))
	erth := e.w.unspent("uerth", uint64(1_000*ssErth))
	anml, _ := e.noteSwap(erth, uint64(1_000*ssErth), "uanml", 1, 0)
	_ = e.buildLegs(ssFee, leg{anml, 5})
	in := e.w.unspent("uerth", uint64(ssErth))
	dm, _, _ := e.delegateMsg(e.genesisValidator(), in, uint64(ssErth))
	require.Greater(t, len(dm.Bundle.Actions), 1)
	sk := e.app.ShieldedKeeper
	for i := 0; i < 5; i++ {
		m := *dm
		m.Stake.Proof = append([]byte(nil), dm.Stake.Proof...)
		m.Stake.Proof[100+i] ^= 0x01
		before := sk.CheckTxVerifications()
		res := e.checkTx(e.privateTx(&m))
		require.Equal(t, sstypes.ErrInvalidStakeProof.ABCICode(), res.Code, res.Log)
		require.Equal(t, uint64(1), sk.CheckTxVerifications()-before, "junk stake proof #%d", i)
	}
	for i := 0; i < 5; i++ {
		m := *dm
		m.Bundle.Actions = append([]shieldedtypes.Action(nil), dm.Bundle.Actions...)
		last := len(m.Bundle.Actions) - 1
		m.Bundle.Actions[last].Proof = append([]byte(nil), dm.Bundle.Actions[last].Proof...)
		m.Bundle.Actions[last].Proof[100+i] ^= 0x01
		before := sk.CheckTxVerifications()
		res := e.checkTx(e.privateTx(&m))
		require.NotZero(t, res.Code, res.Log)
		// the stake proof (first time only) and the earlier bundle proofs
		// (first time only), then the junk one
		if i > 0 {
			require.Equal(t, uint64(1), sk.CheckTxVerifications()-before, "junk bundle proof #%d", i)
		}
	}
	// The honest tx still passes, and lands.
	res := e.checkTx(e.privateTx(dm))
	require.Zero(t, res.Code, res.Log)
}

// Re-audit R5 (GwEpoch leak): a validator's Groundworks entry goes with its
// last position, and the epoch end never walks the Groundworks index: its
// voters re-weigh with the bounded book sweep. Stale entries (here forced in
// state) cost an epoch end nothing.
func TestGroundworksIndexNoLeakAndEpochCostBounded(t *testing.T) {
	g := initGwEnv(t)
	a := g.lockPos(g.v, 2*gwE, 1, g.split(100))
	g.unlockPos(a, 1)
	_, err := g.app.ShieldedStakingKeeper.GwEpoch.Get(g.ctx(), g.valoper(g.v))
	require.ErrorIs(t, err, collections.ErrNotFound, "GwEpoch removed with the last position")

	epochEndGas := func(n int) uint64 {
		cc, _ := g.ctx().CacheContext()
		for i := 0; i < n; i++ {
			va := sdk.ValAddress([]byte(fmt.Sprintf("pocvalidator%08d", i)))
			require.NoError(t, g.app.ShieldedStakingKeeper.GwEpoch.Set(cc, g.valoper(va), 0))
		}
		ep, err := g.app.ShieldedStakingKeeper.Epoch.Get(cc)
		require.NoError(t, err)
		ctx := cc.WithBlockTime(time.Unix(ep.EndTime, 0)).WithGasMeter(storetypes.NewGasMeter(1 << 60))
		require.NoError(t, g.app.ShieldedStakingKeeper.EndBlocker(ctx))
		return ctx.GasMeter().GasConsumed()
	}
	base, many := epochEndGas(0), epochEndGas(10_000)
	t.Logf("epoch end gas: no stale entries %d, 10000 stale entries %d", base, many)
	require.InDelta(t, float64(base), float64(many), 5_000)
}

// Re-audit R8: x/staking's governance lowers max_entries under the floor the
// module checked at genesis. The epoch end reports it, defers the
// undelegation that would not fit (its record stays PENDING) instead of
// failing the validator's whole book, and settles it once there is room.
func TestEpochEndDefersUndelegationPastMaxEntries(t *testing.T) {
	e := initStakeEnv(t)
	vB, _ := e.createValidator(1000 * ssErth)
	e.next(5 * time.Second)
	e.shield(uint64(5_000 * ssErth))
	e.shield(uint64(100 * ssErth))
	dn := e.delegate(vB, uint64(2_000*ssErth))
	e.days(1)
	un1 := e.undelegate(vB, dn, uint64(500*ssErth))
	dn = e.unspentStake(dn.denom)
	e.days(1)
	require.Equal(t, sstypes.UNBOND_STATUS_UNBONDING, e.record(vB, un1.epoch).Status)

	sp, err := e.app.StakingKeeper.GetParams(e.ctx())
	require.NoError(t, err)
	old := sp.MaxEntries
	sp.MaxEntries = 1
	require.NoError(t, e.app.StakingKeeper.SetParams(e.ctx(), sp))

	un2 := e.undelegate(vB, dn, uint64(300*ssErth))
	r := e.next(24 * time.Hour)
	deferred := false
	for _, ev := range eventsOf(r.Events, sstypes.EventTypeUnbondingDeferred) {
		deferred = deferred || ev[sstypes.AttributeKeyValidator] == e.valoper(vB)
	}
	require.True(t, deferred, "undelegation deferred")
	floor := false
	for _, ev := range eventsOf(r.Events, sstypes.EventTypeEpochFailure) {
		floor = floor || ev[sstypes.AttributeKeyStage] == "unbonding_floor"
	}
	require.True(t, floor, "floor violation reported")
	require.Equal(t, sstypes.UNBOND_STATUS_PENDING, e.record(vB, un2.epoch).Status)

	sp.MaxEntries = old
	require.NoError(t, e.app.StakingKeeper.SetParams(e.ctx(), sp))
	e.days(1)
	require.Equal(t, sstypes.UNBOND_STATUS_UNBONDING, e.record(vB, un2.epoch).Status)
}

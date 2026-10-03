package keeper

import (
	"context"
	"testing"
	"time"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"
	storetypes "cosmossdk.io/store/types"
	addresscodec "github.com/cosmos/cosmos-sdk/codec/address"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/cosmos/cosmos-sdk/testutil"
	sdk "github.com/cosmos/cosmos-sdk/types"
	moduletestutil "github.com/cosmos/cosmos-sdk/types/module/testutil"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	"github.com/stretchr/testify/require"

	allocationkeeper "github.com/earth-network/earth/x/allocation/keeper"
	allocationtypes "github.com/earth-network/earth/x/allocation/types"
	"github.com/earth-network/earth/x/personhood/types"
	shieldedtypes "github.com/earth-network/earth/x/shielded/types"
	"github.com/earth-network/earth/zk/orchard"
	"github.com/earth-network/earth/zk/privacy"
)

type stakingStub struct{}

func (stakingStub) BondDenom(context.Context) (string, error) { return "uerth", nil }
func (stakingStub) GetDelegatorBonded(context.Context, sdk.AccAddress) (math.Int, error) {
	return math.ZeroInt(), nil
}
func (stakingStub) GetDelegation(context.Context, sdk.AccAddress, sdk.ValAddress) (stakingtypes.Delegation, error) {
	return stakingtypes.Delegation{}, nil
}
func (stakingStub) GetValidator(context.Context, sdk.ValAddress) (stakingtypes.Validator, error) {
	return stakingtypes.Validator{}, nil
}

// alBank is a do-nothing bank for x/allocation's minting.
type alBank struct{}

func (alBank) SpendableCoins(context.Context, sdk.AccAddress) sdk.Coins { return nil }
func (alBank) GetSupply(context.Context, string) sdk.Coin               { return sdk.Coin{} }
func (alBank) GetBalance(_ context.Context, _ sdk.AccAddress, d string) sdk.Coin {
	return sdk.NewInt64Coin(d, 0)
}
func (alBank) SendCoinsFromModuleToAccount(context.Context, string, sdk.AccAddress, sdk.Coins) error {
	return nil
}
func (alBank) SendCoinsFromAccountToModule(context.Context, sdk.AccAddress, string, sdk.Coins) error {
	return nil
}
func (alBank) SendCoinsFromModuleToModule(context.Context, string, string, sdk.Coins) error {
	return nil
}
func (alBank) MintCoins(context.Context, string, sdk.Coins) error { return nil }
func (alBank) BurnCoins(context.Context, string, sdk.Coins) error { return nil }

// caretakerKeepers is this keeper over a real x/allocation keeper with the
// caretaker stream seeded (option 1, the registration-reward pool).
func caretakerKeepers(t *testing.T) (Keeper, allocationkeeper.Keeper, sdk.Context) {
	t.Helper()
	encCfg := moduletestutil.MakeTestEncodingConfig()
	ac := addresscodec.NewBech32Codec(sdk.GetConfig().GetBech32AccountAddrPrefix())
	phKey := storetypes.NewKVStoreKey(types.StoreKey)
	alKey := storetypes.NewKVStoreKey(allocationtypes.StoreKey)
	ctx := testutil.DefaultContextWithKeys(map[string]*storetypes.KVStoreKey{
		types.StoreKey: phKey, allocationtypes.StoreKey: alKey,
	}, map[string]*storetypes.TransientStoreKey{"t": storetypes.NewTransientStoreKey("t")}, nil).
		WithBlockTime(time.Unix(1_800_000_000, 0).UTC())
	ctx = shieldedtypes.WithTxFields(ctx, shieldedtypes.TxFields{}) // as the private ante records them
	authority := authtypes.NewModuleAddress(types.GovModuleName)
	ak := allocationkeeper.NewKeeper(runtime.NewKVStoreService(alKey), encCfg.Codec, ac, authority, alBank{}, stakingStub{}, &burnLog{})
	require.NoError(t, ak.Options.Set(ctx, collections.Join(uint32(types.AllocationStream), uint64(1)), allocationtypes.AllocationOption{
		Id: 1, Stream: types.AllocationStream, Kind: allocationtypes.ALLOCATION_KIND_INTEGRATED,
		AmountAllocated: math.ZeroInt(), Accumulated: math.ZeroInt(), LastRewardIndex: math.ZeroInt(),
	}))
	k := NewKeeper(runtime.NewKVStoreService(phKey), encCfg.Codec, ac, authority, nil, stubDex{}, nil, ak, &burnLog{}, stubShielded{})
	p := types.DefaultParams()
	p.CaretakerVoteSeconds = 1000
	require.NoError(t, k.Params.Set(ctx, p))
	return k, ak, ctx
}

func totalWeight(t *testing.T, ak allocationkeeper.Keeper, ctx sdk.Context) int64 {
	t.Helper()
	w, err := ak.TotalWeight.Get(ctx, uint32(types.AllocationStream))
	if err != nil {
		return 0
	}
	return w.Int64()
}

// A split is filed under its caretaker nullifier at the fixed weight, a
// refresh replaces it, and it lapses R after it was cast.
func TestCaretakerLeaseAndSweep(t *testing.T) {
	k, ak, ctx := caretakerKeepers(t)
	split := []allocationtypes.AllocationWeight{{OptionId: 1, Percent: 100}}
	nf1, nf2 := privacy.FieldBytes(privacy.U64(1)), privacy.FieldBytes(privacy.U64(2))
	now := ctx.BlockTime().Unix()

	require.NoError(t, k.setCaretakerVote(ctx, nf1, split, now+1000))
	require.NoError(t, k.setCaretakerVote(ctx, nf2, split, now+1500))
	require.Equal(t, int64(2*types.VoterWeight), totalWeight(t, ak, ctx))
	// A refresh by the same nullifier replaces, and moves the lease.
	require.NoError(t, k.setCaretakerVote(ctx, nf1, split, now+2000))
	require.Equal(t, int64(2*types.VoterWeight), totalWeight(t, ak, ctx))
	n, err := k.getCaretakerCount(ctx)
	require.NoError(t, err)
	require.Equal(t, uint64(2), n)

	// nf2 lapses first.
	later := ctx.WithBlockTime(time.Unix(now+1600, 0))
	used, err := k.sweepCaretakerVotes(later, 100)
	require.NoError(t, err)
	require.Equal(t, 1, used)
	require.Equal(t, int64(types.VoterWeight), totalWeight(t, ak, later))
	_, err = ak.Voters.Get(later, collections.Join(uint32(types.AllocationStream), nf2))
	require.ErrorIs(t, err, collections.ErrNotFound)

	// An empty split clears the caster's vote at once.
	require.NoError(t, k.setCaretakerVote(later, nf1, nil, 0))
	require.Zero(t, totalWeight(t, ak, later))
	n, _ = k.getCaretakerCount(later)
	require.Zero(t, n)
	used, err = k.sweepCaretakerVotes(later.WithBlockTime(time.Unix(now+5000, 0)), 100)
	require.NoError(t, err)
	require.Zero(t, used)
}

// A new caretaker split refuses a max_predecessor at or after now - R -
// activation margin (an identity that replaced another cannot vote beside
// its predecessor's live split); activation is not bounded (a fresh
// registrant casts at once). A prover holding a split (cast, or moved to
// it) refreshes it under any max_predecessor; one that moved its split
// away may not cast again.
func TestCaretakerPredecessorBound(t *testing.T) {
	k, _, ctx := caretakerKeepers(t)
	now := ctx.BlockTime().Unix()
	bound := now - 1000 - types.ActivationMarginSeconds
	nf := privacy.FieldBytes(privacy.U64(77))
	split := []allocationtypes.AllocationWeight{{OptionId: 1, Percent: 100}}
	m := &types.MsgSetCaretaker{Fee: feeStub(), MaxPredecessor: uint64(bound - 1), Percentages: split,
		Membership: types.Membership{Nullifier: nf}}
	st, err := k.caretakerStatement(ctx, m)
	require.NoError(t, err)
	require.Equal(t, bound-1, st.MaxPredecessor)
	require.Equal(t, types.NoBound, st.MaxActivation)
	require.Equal(t, privacy.CaretakerScope(), st.Scope)
	// The bound itself is refused (audit 4, C7), and anything later.
	m.MaxPredecessor++
	_, err = k.caretakerStatement(ctx, m)
	require.ErrorIs(t, err, types.ErrInvalidMsg)
	// Holding a split: any bound.
	require.NoError(t, k.setCaretakerVote(ctx, nf, split, now+1000))
	m.MaxPredecessor = uint64(types.NoBound)
	_, err = k.caretakerStatement(ctx, m)
	require.NoError(t, err)

	// A move hands the split (and expiry) to the new owner; the mover may
	// never cast again; the new owner refreshes with no wait.
	owner := privacy.FieldBytes(privacy.U64(78))
	exp, err := k.applyMoveCaretaker(ctx, nf, owner)
	require.NoError(t, err)
	require.Equal(t, now+1000, exp)
	has, err := k.CaretakerVotes.Has(ctx, nf)
	require.NoError(t, err)
	require.False(t, has)
	m.MaxPredecessor = 0
	_, err = k.caretakerStatement(ctx, m)
	require.ErrorIs(t, err, types.ErrCaretakerMovedOut)
	_, err = k.applyMoveCaretaker(ctx, owner, nf)
	require.ErrorIs(t, err, types.ErrCaretakerMovedOut, "nor receive one")
	mo := &types.MsgSetCaretaker{Fee: feeStub(), MaxPredecessor: uint64(types.NoBound), Percentages: split,
		Membership: types.Membership{Nullifier: owner}}
	_, err = k.caretakerStatement(ctx, mo)
	require.NoError(t, err)
	n, err := k.getCaretakerCount(ctx)
	require.NoError(t, err)
	require.Equal(t, uint64(1), n, "moved, not added")
}

// A claim is for today only, once per nullifier, with an identity activated
// before yesterday began; stale claim nullifiers are pruned after two days.
func TestClaimChecksAndPrune(t *testing.T) {
	k, _, ctx := caretakerKeepers(t)
	today := uint64(ctx.BlockTime().Unix() / types.SecondsPerDay)
	// Record a root so the anchor check can pass.
	require.NoError(t, k.recordIdentityRoot(ctx))
	root, err := k.CurrentIdentityRoot(ctx)
	require.NoError(t, err)
	m := &types.MsgClaimAnml{Fee: feeStub(), Day: today, Pc: privacy.FieldBytes(privacy.U64(9)),
		Membership: types.Membership{Proof: make([]byte, shieldedtypes.ProofBytes), Root: root, Nullifier: privacy.FieldBytes(privacy.U64(3))}}
	st, err := k.checkClaim(ctx, m)
	require.NoError(t, err)
	require.Equal(t, int64(today-1)*types.SecondsPerDay, st.MaxActivation)
	require.Equal(t, privacy.ClaimScope(today), st.Scope)

	m.Day = today - 1
	_, err = k.checkClaim(ctx, m)
	require.ErrorIs(t, err, types.ErrWrongDay)
	m.Day = today

	require.NoError(t, k.ClaimNullifiers.Set(ctx, collections.Join(today, m.Membership.Nullifier)))
	_, err = k.checkClaim(ctx, m)
	require.ErrorIs(t, err, types.ErrClaimTooSoon)

	// An unknown root is refused.
	m.Membership.Nullifier = privacy.FieldBytes(privacy.U64(4))
	m.Membership.Root = privacy.FieldBytes(privacy.U64(12345))
	_, err = k.checkClaim(ctx, m)
	require.ErrorIs(t, err, types.ErrUnknownIdentityRoot)

	// Two days on, today's claim nullifier is pruned.
	later := ctx.WithBlockTime(ctx.BlockTime().Add(48 * time.Hour))
	require.NoError(t, k.pruneClaimNullifiers(later, 100))
	has, err := k.ClaimNullifiers.Has(later, collections.Join(today, privacy.FieldBytes(privacy.U64(3))))
	require.NoError(t, err)
	require.False(t, has)
}

// A superseded identity root anchors for the window after its block, the
// latest always; a zeroed leaf therefore stops proving one window after.
func TestIdentityRootWindow(t *testing.T) {
	k, _, ctx := caretakerKeepers(t)
	leaf, err := IdentityLeaf(privacy.FieldBytes(privacy.U64(1)), nil, "", 5, 0)
	require.NoError(t, err)
	_, err = k.appendLeaf(ctx, leaf)
	require.NoError(t, err)
	require.NoError(t, k.EndBlocker(ctx))
	old, err := k.CurrentIdentityRoot(ctx)
	require.NoError(t, err)
	require.NoError(t, k.CheckIdentityAnchor(ctx, old))

	next := ctx.WithBlockTime(ctx.BlockTime().Add(10 * time.Second))
	require.NoError(t, k.zeroLeaf(next, 0))
	require.NoError(t, k.EndBlocker(next))
	require.NoError(t, k.CheckIdentityAnchor(next, old), "inside the window")
	w := time.Duration(types.DefaultIdentityRootWindowSeconds+1) * time.Second
	expired := ctx.WithBlockTime(ctx.BlockTime().Add(w))
	require.ErrorIs(t, k.CheckIdentityAnchor(expired, old), types.ErrUnknownIdentityRoot)
	latest, err := k.CurrentIdentityRoot(next)
	require.NoError(t, err)
	require.NoError(t, k.CheckIdentityAnchor(expired.WithBlockTime(expired.BlockTime().Add(100*24*time.Hour)), latest),
		"the latest root never expires")
	// EndBlock prunes the expired record.
	require.NoError(t, k.EndBlocker(expired))
	has, err := k.IdentityRoots.Has(expired, old)
	require.NoError(t, err)
	require.False(t, has)
}

// feeStub is a well-formed, unproven fee bundle of 1000uerth: enough for the
// sighash a membership statement binds.
func feeStub() shieldedtypes.Bundle {
	cv := orchard.PointBytes(orchard.ValueCommit(privacy.AssetID("uerth"), 1000, privacy.AssetID("uerth"), 0, privacy.U64(7)))
	b := shieldedtypes.Bundle{Balances: []shieldedtypes.ValueBalance{{Denom: "uerth", Amount: 1000}}, BindingSig: make([]byte, 96)}
	for i := range uint64(2) {
		b.Actions = append(b.Actions, shieldedtypes.Action{Anchor: make([]byte, 32),
			Nullifier: privacy.FieldBytes(privacy.U64(i + 1)), Commitment: make([]byte, 32), Cv: cv, Proof: make([]byte, shieldedtypes.ProofBytes)})
	}
	return b
}

// Audit 3 L5: governance lowering R (or the root window) must not let a
// switched-to identity lease beside its predecessor's lease cast under the
// old R. The old R keeps bounding activation until every lease cast under
// it has lapsed; the root window never enters the bound.
func TestAudit3LoweredLeaseLengthHeld(t *testing.T) {
	k, _, ctx := caretakerKeepers(t)
	ms := NewMsgServerImpl(k)
	params, err := k.Params.Get(ctx)
	require.NoError(t, err)
	oldR := params.CaretakerVoteSecondsOrDefault()
	now := ctx.BlockTime().Unix()
	b0, err := k.LeaseActivationBound(ctx)
	require.NoError(t, err)
	require.Equal(t, now-oldR-types.ActivationMarginSeconds, b0)

	// The root window does not move the bound, either way.
	for _, w := range []uint64{60, types.SecondsPerDay} {
		params.IdentityRootWindowSeconds = w
		_, err = ms.UpdateParams(ctx, &types.MsgUpdateParams{Authority: authorityOf(t, k), Params: params})
		require.NoError(t, err)
		b, err := k.LeaseActivationBound(ctx)
		require.NoError(t, err)
		require.Equal(t, b0, b)
	}

	// Lower R to a tenth: the bound keeps the old R until now + old R.
	params.CaretakerVoteSeconds = uint64(oldR / 10)
	_, err = ms.UpdateParams(ctx, &types.MsgUpdateParams{Authority: authorityOf(t, k), Params: params})
	require.NoError(t, err)
	b, err := k.LeaseActivationBound(ctx)
	require.NoError(t, err)
	require.Equal(t, b0, b, "a lowered R does not shorten the bound while old leases run")
	held := ctx.WithBlockTime(time.Unix(now+oldR-1, 0))
	b, err = k.LeaseActivationBound(held)
	require.NoError(t, err)
	require.Equal(t, now+oldR-1-oldR-types.ActivationMarginSeconds, b)
	// Every lease cast under the old R has lapsed: the new R applies.
	after := ctx.WithBlockTime(time.Unix(now+oldR, 0))
	b, err = k.LeaseActivationBound(after)
	require.NoError(t, err)
	require.Equal(t, now+oldR-oldR/10-types.ActivationMarginSeconds, b)

	// The hold survives export/import.
	gs, err := k.ExportGenesis(ctx)
	require.NoError(t, err)
	require.Equal(t, types.LeaseHold{Seconds: oldR, Until: now + oldR}, gs.LeaseHold)
	require.NoError(t, gs.Validate())
}

func authorityOf(t *testing.T, k Keeper) string {
	t.Helper()
	a, err := k.addressCodec.BytesToString(k.GetAuthority())
	require.NoError(t, err)
	return a
}

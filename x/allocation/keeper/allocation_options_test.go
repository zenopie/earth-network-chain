package keeper

import (
	"context"
	"testing"
	"time"

	"cosmossdk.io/math"
	storetypes "cosmossdk.io/store/types"
	addresscodec "github.com/cosmos/cosmos-sdk/codec/address"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/cosmos/cosmos-sdk/testutil"
	sdk "github.com/cosmos/cosmos-sdk/types"
	moduletestutil "github.com/cosmos/cosmos-sdk/types/module/testutil"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	"github.com/earth-network/earth/x/allocation/types"
)

type stubBank struct{ burned sdk.Coins }

func (s *stubBank) SpendableCoins(context.Context, sdk.AccAddress) sdk.Coins { return nil }
func (s *stubBank) GetSupply(context.Context, string) sdk.Coin               { return sdk.Coin{} }
func (s *stubBank) SendCoinsFromModuleToAccount(context.Context, string, sdk.AccAddress, sdk.Coins) error {
	return nil
}
func (s *stubBank) SendCoinsFromAccountToModule(context.Context, sdk.AccAddress, string, sdk.Coins) error {
	return nil
}
func (s *stubBank) MintCoins(context.Context, string, sdk.Coins) error { return nil }
func (s *stubBank) BurnCoins(_ context.Context, _ string, amt sdk.Coins) error {
	s.burned = s.burned.Add(amt...)
	return nil
}

// stubStaking reports a fixed bonded amount per delegator, which is the capital
// stream's weight.
type stubStaking struct{ bonded map[string]math.Int }

func newStubStaking() *stubStaking { return &stubStaking{bonded: map[string]math.Int{}} }

func (*stubStaking) BondDenom(context.Context) (string, error) { return "uerth", nil }
func (s *stubStaking) GetDelegatorBonded(_ context.Context, addr sdk.AccAddress) (math.Int, error) {
	if v, ok := s.bonded[addr.String()]; ok {
		return v, nil
	}
	return math.ZeroInt(), nil
}
func (*stubStaking) GetDelegation(context.Context, sdk.AccAddress, sdk.ValAddress) (stakingtypes.Delegation, error) {
	return stakingtypes.Delegation{}, nil
}
func (*stubStaking) GetValidator(_ context.Context, valAddr sdk.ValAddress) (stakingtypes.Validator, error) {
	return stakingtypes.Validator{OperatorAddress: valAddr.String()}, nil
}

// stubHumans is the human stream's weight source: a set of addresses that count
// as live registrations, each carrying the same fixed weight.
type stubHumans struct{ registered map[string]bool }

func newStubHumans() *stubHumans { return &stubHumans{registered: map[string]bool{}} }

func (s *stubHumans) add(addr sdk.AccAddress) { s.registered[addr.String()] = true }

func (s *stubHumans) Weight(_ context.Context, addr []byte) (math.Int, error) {
	if s.registered[sdk.AccAddress(addr).String()] {
		return math.NewInt(types.HumanVoterWeight), nil
	}
	return math.ZeroInt(), nil
}

// stubDex stands in for the LP-rewards handler x/dex registers in the app.
type stubDex struct{ distributed math.Int }

type testEnv struct {
	k       Keeper
	ctx     sdk.Context
	bank    *stubBank
	dex     *stubDex
	staking *stubStaking
	humans  *stubHumans
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	encCfg := moduletestutil.MakeTestEncodingConfig()
	ac := addresscodec.NewBech32Codec(sdk.GetConfig().GetBech32AccountAddrPrefix())
	storeKey := storetypes.NewKVStoreKey(types.StoreKey)
	ctx := testutil.DefaultContextWithDB(t, storeKey, storetypes.NewTransientStoreKey("transient_test")).Ctx

	bank := &stubBank{burned: sdk.NewCoins()}
	dex := &stubDex{distributed: math.ZeroInt()}
	staking := newStubStaking()
	humans := newStubHumans()

	k := NewKeeper(runtime.NewKVStoreService(storeKey), encCfg.Codec, ac,
		authtypes.NewModuleAddress(types.GovModuleName), bank, staking)

	// The same registrations the app performs from x/dex and x/personhood.
	k.RegisterWeightSource(types.STREAM_ID_CARETAKER, humans)
	k.RegisterIntegratedHandler(types.STREAM_ID_GROUNDWORKS, types.HandlerLPRewards,
		func(_ context.Context, accrued math.Int) (math.Int, error) {
			dex.distributed = dex.distributed.Add(accrued)
			return accrued, nil // fully distributed
		})
	k.RegisterIntegratedHandler(types.STREAM_ID_CARETAKER, types.HandlerRegistrationRewards,
		func(context.Context, math.Int) (math.Int, error) {
			return math.ZeroInt(), nil // stacks; drawn down on registration
		})

	return &testEnv{k: k, ctx: ctx, bank: bank, dex: dex, staking: staking, humans: humans}
}

func (e *testEnv) addr(name string) (sdk.AccAddress, string) {
	acc := sdk.AccAddress(authtypes.NewModuleAddress(name))
	s, _ := e.k.addressCodec.BytesToString(acc)
	return acc, s
}

// TestGenesisSeedsBothStreams pins the trap that made ids per-stream: the human
// stream's registration-rewards option and the capital stream's LP-rewards
// option are both #1, and they must be two different options.
func TestGenesisSeedsBothStreams(t *testing.T) {
	e := newTestEnv(t)
	if err := e.k.InitGenesis(e.ctx, *types.DefaultGenesis()); err != nil {
		t.Fatal(err)
	}

	human, err := e.k.Options.Get(e.ctx, optionKey(types.STREAM_ID_CARETAKER, 1))
	if err != nil {
		t.Fatalf("human option #1: %v", err)
	}
	if human.Handler != types.HandlerRegistrationRewards || human.Stream != types.STREAM_ID_CARETAKER {
		t.Fatalf("human option #1 = %+v, want the registration-rewards handler", human)
	}

	capital, err := e.k.Options.Get(e.ctx, optionKey(types.STREAM_ID_GROUNDWORKS, 1))
	if err != nil {
		t.Fatalf("capital option #1: %v", err)
	}
	if capital.Handler != types.HandlerLPRewards || capital.Stream != types.STREAM_ID_GROUNDWORKS {
		t.Fatalf("capital option #1 = %+v, want the lp_rewards handler", capital)
	}
}

// TestIntegratedAndAddressOptions validates the two allocation types: INTEGRATED
// (gov-only, resolved every block via a registered handler) and ADDRESS
// (permissionless + fee, lazy/claim-based, never touched in BeginBlocker).
func TestIntegratedAndAddressOptions(t *testing.T) {
	e := newTestEnv(t)
	k, ctx := e.k, e.ctx
	if err := k.InitGenesis(ctx, *types.DefaultGenesis()); err != nil {
		t.Fatal(err)
	}
	ms := NewMsgServerImpl(k)
	authority, _ := k.addressCodec.BytesToString(k.GetAuthority())
	_, alice := e.addr("alice")

	if has, _ := k.IntegratedOptions.Has(ctx, optionKey(types.STREAM_ID_GROUNDWORKS, 1)); !has {
		t.Fatal("capital option #1 not in the integrated set")
	}

	// AddAddressOption is permissionless and burns the fee; option is NOT integrated.
	if _, err := ms.AddAddressOption(ctx, &types.MsgAddAddressOption{
		Submitter: alice, Stream: types.STREAM_ID_GROUNDWORKS, Recipient: alice, Description: "grant",
	}); err != nil {
		t.Fatalf("AddAddressOption: %v", err)
	}
	if got := e.bank.burned.AmountOf("uerth"); !got.Equal(math.NewInt(types.DefaultAddressOptionFee)) {
		t.Fatalf("fee burned = %s, want %d", got, types.DefaultAddressOptionFee)
	}
	if opt2, _ := k.Options.Get(ctx, optionKey(types.STREAM_ID_GROUNDWORKS, 2)); opt2.Kind != types.ALLOCATION_KIND_ADDRESS {
		t.Fatalf("option #2 kind = %v, want ADDRESS", opt2.Kind)
	}
	if has, _ := k.IntegratedOptions.Has(ctx, optionKey(types.STREAM_ID_GROUNDWORKS, 2)); has {
		t.Fatal("ADDRESS option must NOT be in the integrated set")
	}

	// AddIntegratedOption: non-authority rejected; unknown handler rejected; a
	// handler belonging to the other stream rejected; valid ok.
	if _, err := ms.AddIntegratedOption(ctx, &types.MsgAddIntegratedOption{
		Authority: alice, Stream: types.STREAM_ID_GROUNDWORKS, Handler: types.HandlerLPRewards,
	}); err == nil {
		t.Fatal("expected rejection: non-authority adding integrated option")
	}
	if _, err := ms.AddIntegratedOption(ctx, &types.MsgAddIntegratedOption{
		Authority: authority, Stream: types.STREAM_ID_GROUNDWORKS, Handler: "nope",
	}); err == nil {
		t.Fatal("expected rejection: unknown handler")
	}
	if _, err := ms.AddIntegratedOption(ctx, &types.MsgAddIntegratedOption{
		Authority: authority, Stream: types.STREAM_ID_CARETAKER, Handler: types.HandlerLPRewards,
	}); err == nil {
		t.Fatal("expected rejection: capital handler attached to the human stream")
	}
	if _, err := ms.AddIntegratedOption(ctx, &types.MsgAddIntegratedOption{
		Authority: authority, Stream: types.STREAM_ID_GROUNDWORKS, Handler: types.HandlerLPRewards, Description: "more lp",
	}); err != nil {
		t.Fatalf("valid AddIntegratedOption: %v", err)
	}
	if has, _ := k.IntegratedOptions.Has(ctx, optionKey(types.STREAM_ID_GROUNDWORKS, 3)); !has {
		t.Fatal("option #3 should be in the integrated set")
	}

	// BeginBlocker resolves integrated options via their handler; address is untouched.
	set := func(stream types.StreamId, id uint64, amt int64) {
		o, _ := k.Options.Get(ctx, optionKey(stream, id))
		o.Accumulated = math.NewInt(amt)
		_ = k.Options.Set(ctx, optionKey(stream, id), o)
	}
	set(types.STREAM_ID_GROUNDWORKS, 1, 500) // integrated (lp_rewards)
	set(types.STREAM_ID_GROUNDWORKS, 2, 300) // address
	set(types.STREAM_ID_CARETAKER, 1, 700)   // integrated, but its handler resolves nothing
	ctx = ctx.WithBlockTime(time.Unix(1000, 0))
	if err := k.BeginBlocker(ctx); err != nil {
		t.Fatalf("BeginBlocker: %v", err)
	}
	if !e.dex.distributed.Equal(math.NewInt(500)) {
		t.Fatalf("integrated handler distributed %s, want 500", e.dex.distributed)
	}
	if o1, _ := k.Options.Get(ctx, optionKey(types.STREAM_ID_GROUNDWORKS, 1)); !o1.Accumulated.IsZero() {
		t.Fatalf("integrated option accumulated = %s, want 0 (resolved)", o1.Accumulated)
	}
	if o2, _ := k.Options.Get(ctx, optionKey(types.STREAM_ID_GROUNDWORKS, 2)); !o2.Accumulated.Equal(math.NewInt(300)) {
		t.Fatalf("ADDRESS option accumulated = %s, want 300 (untouched, lazy)", o2.Accumulated)
	}
	if h1, _ := k.Options.Get(ctx, optionKey(types.STREAM_ID_CARETAKER, 1)); !h1.Accumulated.Equal(math.NewInt(700)) {
		t.Fatalf("registration-rewards pool = %s, want 700 (stacks until a registration draws it)", h1.Accumulated)
	}
}

// TestAddressOptionClaimer covers the optional claimer on ADDRESS options: with
// no claimer set anyone may trigger the claim, with one set only that address
// may — and in both cases the payout goes to the recipient, never the caller.
func TestAddressOptionClaimer(t *testing.T) {
	e := newTestEnv(t)
	k, ctx := e.k, e.ctx
	if err := k.InitGenesis(ctx, *types.DefaultGenesis()); err != nil {
		t.Fatal(err)
	}
	ms := NewMsgServerImpl(k)
	_, recipient := e.addr("recipient")
	_, claimer := e.addr("claimer")
	_, stranger := e.addr("stranger")

	stream := types.STREAM_ID_GROUNDWORKS
	open, err := ms.AddAddressOption(ctx, &types.MsgAddAddressOption{
		Submitter: recipient, Stream: stream, Recipient: recipient, Description: "open",
	})
	if err != nil {
		t.Fatal(err)
	}
	restricted, err := ms.AddAddressOption(ctx, &types.MsgAddAddressOption{
		Submitter: recipient, Stream: stream, Recipient: recipient, Claimer: claimer, Description: "restricted",
	})
	if err != nil {
		t.Fatal(err)
	}
	if o, _ := k.Options.Get(ctx, optionKey(stream, open.Id)); o.Claimer != "" {
		t.Fatalf("open option claimer = %q, want empty", o.Claimer)
	}
	if o, _ := k.Options.Get(ctx, optionKey(stream, restricted.Id)); o.Claimer != claimer {
		t.Fatalf("restricted option claimer = %q, want %q", o.Claimer, claimer)
	}

	// A stranger may trigger the open option, but not the restricted one.
	if _, err := ms.ClaimAllocation(ctx, &types.MsgClaimAllocation{Creator: stranger, Stream: stream, OptionId: open.Id}); err != nil {
		t.Fatalf("stranger claiming open option: %v", err)
	}
	if _, err := ms.ClaimAllocation(ctx, &types.MsgClaimAllocation{Creator: stranger, Stream: stream, OptionId: restricted.Id}); err == nil {
		t.Fatal("expected rejection: stranger triggering a restricted option")
	}
	// The designated claimer may. Note the recipient is NOT automatically
	// allowed once a claimer is set — the claimer field is the sole gate.
	if _, err := ms.ClaimAllocation(ctx, &types.MsgClaimAllocation{Creator: claimer, Stream: stream, OptionId: restricted.Id}); err != nil {
		t.Fatalf("designated claimer: %v", err)
	}

	// Payout goes to the recipient regardless of who triggered it.
	o, _ := k.Options.Get(ctx, optionKey(stream, open.Id))
	o.Accumulated = math.NewInt(700)
	if err := k.Options.Set(ctx, optionKey(stream, open.Id), o); err != nil {
		t.Fatal(err)
	}
	if _, err := ms.ClaimAllocation(ctx, &types.MsgClaimAllocation{Creator: stranger, Stream: stream, OptionId: open.Id}); err != nil {
		t.Fatal(err)
	}
	evs := sdk.UnwrapSDKContext(ctx).EventManager().Events()
	last := evs[len(evs)-1]
	var gotRecipient, gotAmount string
	for _, a := range last.Attributes {
		switch a.Key {
		case "recipient":
			gotRecipient = a.Value
		case "amount":
			gotAmount = a.Value
		}
	}
	if gotRecipient != recipient || gotAmount != "700" {
		t.Fatalf("paid %s to %q, want 700 to recipient %q", gotAmount, gotRecipient, recipient)
	}
	if o, _ := k.Options.Get(ctx, optionKey(stream, open.Id)); !o.Accumulated.IsZero() {
		t.Fatalf("accumulated after claim = %s, want 0", o.Accumulated)
	}
}

// TestStreamsAreIndependent is the whole point of the merge: one engine, two
// sets of state. A vote in one stream must not appear in the other's totals, and
// eligibility is decided per stream — a registered human with no stake can vote
// in one and not the other.
func TestStreamsAreIndependent(t *testing.T) {
	e := newTestEnv(t)
	k, ctx := e.k, e.ctx
	if err := k.InitGenesis(ctx, *types.DefaultGenesis()); err != nil {
		t.Fatal(err)
	}
	ms := NewMsgServerImpl(k)

	acc, addr := e.addr("human-no-stake")
	e.humans.add(acc)

	vote := []types.AllocationWeight{{OptionId: 1, Percent: 100}}
	if _, err := ms.SetAllocations(ctx, &types.MsgSetAllocations{
		Creator: addr, Stream: types.STREAM_ID_CARETAKER, Percentages: vote,
	}); err != nil {
		t.Fatalf("registered human voting in the human stream: %v", err)
	}
	// No bonded stake, so the capital stream refuses the same address.
	if _, err := ms.SetAllocations(ctx, &types.MsgSetAllocations{
		Creator: addr, Stream: types.STREAM_ID_GROUNDWORKS, Percentages: vote,
	}); err == nil {
		t.Fatal("expected rejection: no bonded stake in the capital stream")
	}

	humanTotal, err := k.getTotalWeight(ctx, types.STREAM_ID_CARETAKER)
	if err != nil {
		t.Fatal(err)
	}
	if !humanTotal.Equal(math.NewInt(types.HumanVoterWeight)) {
		t.Fatalf("human total weight = %s, want %d", humanTotal, types.HumanVoterWeight)
	}
	capitalTotal, err := k.getTotalWeight(ctx, types.STREAM_ID_GROUNDWORKS)
	if err != nil {
		t.Fatal(err)
	}
	if !capitalTotal.IsZero() {
		t.Fatalf("capital total weight = %s, want 0 — the streams share no state", capitalTotal)
	}

	// A staker votes in the capital stream at their bonded weight, and that
	// weight is not normalized down to the human stream's flat 100.
	staker, stakerAddr := e.addr("staker")
	e.staking.bonded[staker.String()] = math.NewInt(5_000)
	if _, err := ms.SetAllocations(ctx, &types.MsgSetAllocations{
		Creator: stakerAddr, Stream: types.STREAM_ID_GROUNDWORKS, Percentages: vote,
	}); err != nil {
		t.Fatalf("staker voting in the capital stream: %v", err)
	}
	capitalTotal, err = k.getTotalWeight(ctx, types.STREAM_ID_GROUNDWORKS)
	if err != nil {
		t.Fatal(err)
	}
	if !capitalTotal.Equal(math.NewInt(5_000)) {
		t.Fatalf("capital total weight = %s, want 5000 (bonded stake, not a flat vote)", capitalTotal)
	}
}

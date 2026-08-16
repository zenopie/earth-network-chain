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

	"github.com/earth-network/earth/x/deflation/types"
)

type stubBank struct{ burned sdk.Coins }

func (s *stubBank) SpendableCoins(context.Context, sdk.AccAddress) sdk.Coins { return nil }
func (s *stubBank) GetSupply(context.Context, string) sdk.Coin              { return sdk.Coin{} }
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

// stubStaking records what was delegated through it and can be told which
// validators exist, so the commission tests can assert that compounding really
// produced a self-delegation rather than just clearing a ledger.
type stubStaking struct {
	// missing marks validators that should not resolve, standing in for one that
	// has been removed entirely.
	missing   map[string]bool
	delegated map[string]math.Int
}

func newStubStaking() *stubStaking {
	return &stubStaking{missing: map[string]bool{}, delegated: map[string]math.Int{}}
}

func (*stubStaking) BondDenom(context.Context) (string, error) { return "uerth", nil }
func (*stubStaking) GetDelegatorBonded(context.Context, sdk.AccAddress) (math.Int, error) {
	return math.ZeroInt(), nil
}
func (*stubStaking) GetDelegation(context.Context, sdk.AccAddress, sdk.ValAddress) (stakingtypes.Delegation, error) {
	return stakingtypes.Delegation{}, nil
}
func (s *stubStaking) GetValidator(_ context.Context, valAddr sdk.ValAddress) (stakingtypes.Validator, error) {
	if s.missing[valAddr.String()] {
		return stakingtypes.Validator{}, stakingtypes.ErrNoValidatorFound
	}
	return stakingtypes.Validator{OperatorAddress: valAddr.String()}, nil
}

func (s *stubStaking) Delegate(
	_ context.Context, _ sdk.AccAddress, amt math.Int,
	_ stakingtypes.BondStatus, validator stakingtypes.Validator, _ bool,
) (math.LegacyDec, error) {
	cur, ok := s.delegated[validator.OperatorAddress]
	if !ok {
		cur = math.ZeroInt()
	}
	s.delegated[validator.OperatorAddress] = cur.Add(amt)
	return math.LegacyNewDecFromInt(amt), nil
}

// stubEarth stands in for the tokenomics module. Returning the weight unchanged
// is the identity normalization — what a compounding index of 1.0 produces,
// i.e. a chain where nothing has compounded yet.
type stubEarth struct{}

func (stubEarth) NormalizeStakeWeight(_ context.Context, weight math.Int) (math.Int, error) {
	return weight, nil
}

type stubDex struct{ distributed math.Int }

func (s *stubDex) DistributeLPRewards(_ context.Context, amount math.Int) (math.Int, error) {
	s.distributed = s.distributed.Add(amount)
	return amount, nil // fully distributed
}

func newKeeperForTest(t *testing.T) (Keeper, sdk.Context, *stubBank, *stubDex) {
	t.Helper()
	k, ctx, bank, dex, _ := newKeeperForTestWithStaking(t)
	return k, ctx, bank, dex
}

// newKeeperForTestWithStaking also hands back the staking stub, for tests that
// need to assert on delegations or make a validator disappear.
func newKeeperForTestWithStaking(t *testing.T) (Keeper, sdk.Context, *stubBank, *stubDex, *stubStaking) {
	t.Helper()
	encCfg := moduletestutil.MakeTestEncodingConfig()
	ac := addresscodec.NewBech32Codec(sdk.GetConfig().GetBech32AccountAddrPrefix())
	storeKey := storetypes.NewKVStoreKey(types.StoreKey)
	ctx := testutil.DefaultContextWithDB(t, storeKey, storetypes.NewTransientStoreKey("transient_test")).Ctx
	bank := &stubBank{burned: sdk.NewCoins()}
	dex := &stubDex{distributed: math.ZeroInt()}
	staking := newStubStaking()
	k := NewKeeper(runtime.NewKVStoreService(storeKey), encCfg.Codec, ac,
		authtypes.NewModuleAddress(types.GovModuleName), bank, staking, dex, stubEarth{})
	return k, ctx, bank, dex, staking
}

// TestIntegratedAndAddressOptions validates the two allocation types: INTEGRATED
// (gov-only, resolved every block via a registered handler) and ADDRESS
// (permissionless + fee, lazy/claim-based, never touched in BeginBlocker).
func TestIntegratedAndAddressOptions(t *testing.T) {
	k, ctx, bank, dex := newKeeperForTest(t)
	if err := k.InitGenesis(ctx, *types.DefaultGenesis()); err != nil {
		t.Fatal(err)
	}
	ms := NewMsgServerImpl(k)
	authority, _ := k.addressCodec.BytesToString(k.GetAuthority())
	alice, _ := k.addressCodec.BytesToString(authtypes.NewModuleAddress("alice"))

	// Genesis option #1 is INTEGRATED (lp_rewards) and in the integrated set.
	opt1, err := k.AllocationOptions.Get(ctx, types.LPRewardsOptionID)
	if err != nil || opt1.Kind != types.ALLOCATION_KIND_INTEGRATED || opt1.Handler != types.HandlerLPRewards {
		t.Fatalf("option #1 not integrated lp_rewards: %+v (%v)", opt1, err)
	}
	if has, _ := k.IntegratedOptions.Has(ctx, 1); !has {
		t.Fatal("option #1 not in integrated set")
	}

	// AddAddressOption is permissionless and burns the fee; option is NOT integrated.
	if _, err := ms.AddAddressOption(ctx, &types.MsgAddAddressOption{Submitter: alice, Recipient: alice, Description: "grant"}); err != nil {
		t.Fatalf("AddAddressOption: %v", err)
	}
	if got := bank.burned.AmountOf("uerth"); !got.Equal(math.NewInt(types.DefaultAddressOptionFee)) {
		t.Fatalf("fee burned = %s, want %d", got, types.DefaultAddressOptionFee)
	}
	if opt2, _ := k.AllocationOptions.Get(ctx, 2); opt2.Kind != types.ALLOCATION_KIND_ADDRESS {
		t.Fatalf("option #2 kind = %v, want ADDRESS", opt2.Kind)
	}
	if has, _ := k.IntegratedOptions.Has(ctx, 2); has {
		t.Fatal("ADDRESS option must NOT be in the integrated set")
	}

	// AddIntegratedOption: non-authority rejected; unknown handler rejected; valid ok.
	if _, err := ms.AddIntegratedOption(ctx, &types.MsgAddIntegratedOption{Authority: alice, Handler: types.HandlerLPRewards}); err == nil {
		t.Fatal("expected rejection: non-authority adding integrated option")
	}
	if _, err := ms.AddIntegratedOption(ctx, &types.MsgAddIntegratedOption{Authority: authority, Handler: "nope"}); err == nil {
		t.Fatal("expected rejection: unknown handler")
	}
	if _, err := ms.AddIntegratedOption(ctx, &types.MsgAddIntegratedOption{Authority: authority, Handler: types.HandlerLPRewards, Description: "more lp"}); err != nil {
		t.Fatalf("valid AddIntegratedOption: %v", err)
	}
	if has, _ := k.IntegratedOptions.Has(ctx, 3); !has {
		t.Fatal("option #3 should be in the integrated set")
	}

	// BeginBlocker resolves integrated options via their handler; address is untouched.
	set := func(id uint64, amt int64) {
		o, _ := k.AllocationOptions.Get(ctx, id)
		o.Accumulated = math.NewInt(amt)
		_ = k.AllocationOptions.Set(ctx, id, o)
	}
	set(1, 500) // integrated (lp_rewards)
	set(2, 300) // address
	ctx = ctx.WithBlockTime(time.Unix(1000, 0))
	if err := k.BeginBlocker(ctx); err != nil {
		t.Fatalf("BeginBlocker: %v", err)
	}
	if !dex.distributed.Equal(math.NewInt(500)) {
		t.Fatalf("integrated handler distributed %s, want 500", dex.distributed)
	}
	if o1, _ := k.AllocationOptions.Get(ctx, 1); !o1.Accumulated.IsZero() {
		t.Fatalf("integrated option accumulated = %s, want 0 (resolved)", o1.Accumulated)
	}
	if o2, _ := k.AllocationOptions.Get(ctx, 2); !o2.Accumulated.Equal(math.NewInt(300)) {
		t.Fatalf("ADDRESS option accumulated = %s, want 300 (untouched, lazy)", o2.Accumulated)
	}
}

// TestAddressOptionClaimer covers the optional claimer on ADDRESS options: with
// no claimer set anyone may trigger the claim, with one set only that address
// may — and in both cases the payout goes to the recipient, never the caller.
func TestAddressOptionClaimer(t *testing.T) {
	k, ctx, _, _ := newKeeperForTest(t)
	if err := k.InitGenesis(ctx, *types.DefaultGenesis()); err != nil {
		t.Fatal(err)
	}
	ms := NewMsgServerImpl(k)
	recipient, _ := k.addressCodec.BytesToString(authtypes.NewModuleAddress("recipient"))
	claimer, _ := k.addressCodec.BytesToString(authtypes.NewModuleAddress("claimer"))
	stranger, _ := k.addressCodec.BytesToString(authtypes.NewModuleAddress("stranger"))

	// #2: open (no claimer). #3: restricted to `claimer`.
	open, err := ms.AddAddressOption(ctx, &types.MsgAddAddressOption{Submitter: recipient, Recipient: recipient, Description: "open"})
	if err != nil {
		t.Fatal(err)
	}
	restricted, err := ms.AddAddressOption(ctx, &types.MsgAddAddressOption{Submitter: recipient, Recipient: recipient, Claimer: claimer, Description: "restricted"})
	if err != nil {
		t.Fatal(err)
	}
	if o, _ := k.AllocationOptions.Get(ctx, open.Id); o.Claimer != "" {
		t.Fatalf("open option claimer = %q, want empty", o.Claimer)
	}
	if o, _ := k.AllocationOptions.Get(ctx, restricted.Id); o.Claimer != claimer {
		t.Fatalf("restricted option claimer = %q, want %q", o.Claimer, claimer)
	}

	// A stranger may trigger the open option, but not the restricted one.
	if _, err := ms.ClaimAllocation(ctx, &types.MsgClaimAllocation{Creator: stranger, OptionId: open.Id}); err != nil {
		t.Fatalf("stranger claiming open option: %v", err)
	}
	if _, err := ms.ClaimAllocation(ctx, &types.MsgClaimAllocation{Creator: stranger, OptionId: restricted.Id}); err == nil {
		t.Fatal("expected rejection: stranger triggering a restricted option")
	}
	// The designated claimer may. Note the recipient is NOT automatically
	// allowed once a claimer is set — the claimer field is the sole gate.
	if _, err := ms.ClaimAllocation(ctx, &types.MsgClaimAllocation{Creator: claimer, OptionId: restricted.Id}); err != nil {
		t.Fatalf("designated claimer: %v", err)
	}

	// Payout goes to the recipient regardless of who triggered it.
	set := func(id uint64, amt int64) {
		o, _ := k.AllocationOptions.Get(ctx, id)
		o.Accumulated = math.NewInt(amt)
		_ = k.AllocationOptions.Set(ctx, id, o)
	}
	set(open.Id, 700)
	if _, err := ms.ClaimAllocation(ctx, &types.MsgClaimAllocation{Creator: stranger, OptionId: open.Id}); err != nil {
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
	if o, _ := k.AllocationOptions.Get(ctx, open.Id); !o.Accumulated.IsZero() {
		t.Fatalf("accumulated after claim = %s, want 0", o.Accumulated)
	}
}

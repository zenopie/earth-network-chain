package keeper

import (
	"context"
	"testing"

	"cosmossdk.io/math"
	storetypes "cosmossdk.io/store/types"
	addresscodec "github.com/cosmos/cosmos-sdk/codec/address"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/cosmos/cosmos-sdk/testutil"
	sdk "github.com/cosmos/cosmos-sdk/types"
	moduletestutil "github.com/cosmos/cosmos-sdk/types/module/testutil"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"

	"github.com/earth-network/earth/x/personhood/types"
)

// stubBankBurn records fee burns for the ADDRESS-option test.
type stubBankBurn struct{ burned sdk.Coins }

func (s *stubBankBurn) GetSupply(context.Context, string) sdk.Coin { return sdk.Coin{} }
func (s *stubBankBurn) SendCoinsFromModuleToAccount(context.Context, string, sdk.AccAddress, sdk.Coins) error {
	return nil
}
func (s *stubBankBurn) SendCoinsFromAccountToModule(context.Context, sdk.AccAddress, string, sdk.Coins) error {
	return nil
}
func (s *stubBankBurn) MintCoins(context.Context, string, sdk.Coins) error { return nil }
func (s *stubBankBurn) BurnCoins(_ context.Context, _ string, amt sdk.Coins) error {
	s.burned = s.burned.Add(amt...)
	return nil
}

// TestDemocraticIntegratedAndAddressOptions mirrors the deflation test for the
// caretaker pillar: INTEGRATED options are gov-only and live in the integrated
// set (resolved every block by their handler — registration_rewards is a no-op
// since the pool pays out on registration); ADDRESS options are permissionless,
// burn the fee, and are never iterated in BeginBlocker.
func TestDemocraticIntegratedAndAddressOptions(t *testing.T) {
	encCfg := moduletestutil.MakeTestEncodingConfig()
	ac := addresscodec.NewBech32Codec(sdk.GetConfig().GetBech32AccountAddrPrefix())
	storeKey := storetypes.NewKVStoreKey(types.StoreKey)
	ctx := testutil.DefaultContextWithDB(t, storeKey, storetypes.NewTransientStoreKey("transient_test")).Ctx
	bank := &stubBankBurn{burned: sdk.NewCoins()}
	k := NewKeeper(runtime.NewKVStoreService(storeKey), encCfg.Codec, ac,
		authtypes.NewModuleAddress(types.GovModuleName), bank, stubDex{}, nil)
	if err := k.InitGenesis(ctx, *types.DefaultGenesis()); err != nil {
		t.Fatal(err)
	}
	ms := NewMsgServerImpl(k)
	authority, _ := ac.BytesToString(k.GetAuthority())
	alice, _ := ac.BytesToString(authtypes.NewModuleAddress("alice"))

	// Genesis option #1 is INTEGRATED (registration_rewards) and in the set.
	opt1, err := k.DemOptions.Get(ctx, types.RegistrationRewardOptionID)
	if err != nil || opt1.Kind != types.DEMOCRATIC_KIND_INTEGRATED || opt1.Handler != types.HandlerRegistrationRewards {
		t.Fatalf("option #1 not integrated registration_rewards: %+v (%v)", opt1, err)
	}
	if has, _ := k.DemIntegratedOptions.Has(ctx, 1); !has {
		t.Fatal("option #1 not in integrated set")
	}

	// AddAddressOption is permissionless and burns the fee; option is NOT integrated.
	if _, err := ms.AddAddressOption(ctx, &types.MsgAddAddressOption{Submitter: alice, Recipient: alice, Description: "grant"}); err != nil {
		t.Fatalf("AddAddressOption: %v", err)
	}
	if got := bank.burned.AmountOf("uerth"); !got.Equal(math.NewIntFromUint64(types.DefaultParams().AddressOptionFee)) {
		t.Fatalf("fee burned = %s, want %d", got, types.DefaultParams().AddressOptionFee)
	}
	if opt2, _ := k.DemOptions.Get(ctx, 2); opt2.Kind != types.DEMOCRATIC_KIND_ADDRESS {
		t.Fatalf("option #2 kind = %v, want ADDRESS", opt2.Kind)
	}
	if has, _ := k.DemIntegratedOptions.Has(ctx, 2); has {
		t.Fatal("ADDRESS option must NOT be in the integrated set")
	}

	// AddIntegratedOption: non-authority rejected; unknown handler rejected; valid ok.
	if _, err := ms.AddIntegratedOption(ctx, &types.MsgAddIntegratedOption{Authority: alice, Handler: types.HandlerRegistrationRewards}); err == nil {
		t.Fatal("expected rejection: non-authority adding integrated option")
	}
	if _, err := ms.AddIntegratedOption(ctx, &types.MsgAddIntegratedOption{Authority: authority, Handler: "nope"}); err == nil {
		t.Fatal("expected rejection: unknown handler")
	}
	if _, err := ms.AddIntegratedOption(ctx, &types.MsgAddIntegratedOption{Authority: authority, Handler: types.HandlerRegistrationRewards, Description: "more"}); err != nil {
		t.Fatalf("valid AddIntegratedOption: %v", err)
	}
	if has, _ := k.DemIntegratedOptions.Has(ctx, 3); !has {
		t.Fatal("option #3 should be in the integrated set")
	}
}

// TestDemocraticAddressOptionClaimer covers the optional claimer: empty means
// anyone may trigger the claim, set means only that address may — and either
// way the payout goes to the recipient, never the caller.
func TestDemocraticAddressOptionClaimer(t *testing.T) {
	encCfg := moduletestutil.MakeTestEncodingConfig()
	ac := addresscodec.NewBech32Codec(sdk.GetConfig().GetBech32AccountAddrPrefix())
	storeKey := storetypes.NewKVStoreKey(types.StoreKey)
	ctx := testutil.DefaultContextWithDB(t, storeKey, storetypes.NewTransientStoreKey("transient_test")).Ctx
	k := NewKeeper(runtime.NewKVStoreService(storeKey), encCfg.Codec, ac,
		authtypes.NewModuleAddress(types.GovModuleName), &stubBankBurn{burned: sdk.NewCoins()}, stubDex{}, nil)
	if err := k.InitGenesis(ctx, *types.DefaultGenesis()); err != nil {
		t.Fatal(err)
	}
	ms := NewMsgServerImpl(k)
	recipient, _ := ac.BytesToString(authtypes.NewModuleAddress("recipient"))
	claimer, _ := ac.BytesToString(authtypes.NewModuleAddress("claimer"))
	stranger, _ := ac.BytesToString(authtypes.NewModuleAddress("stranger"))

	open, err := ms.AddAddressOption(ctx, &types.MsgAddAddressOption{Submitter: recipient, Recipient: recipient, Description: "open"})
	if err != nil {
		t.Fatal(err)
	}
	restricted, err := ms.AddAddressOption(ctx, &types.MsgAddAddressOption{Submitter: recipient, Recipient: recipient, Claimer: claimer, Description: "restricted"})
	if err != nil {
		t.Fatal(err)
	}
	if o, _ := k.DemOptions.Get(ctx, open.Id); o.Claimer != "" {
		t.Fatalf("open option claimer = %q, want empty", o.Claimer)
	}

	// A stranger may trigger the open option, but not the restricted one.
	if _, err := ms.ClaimDemocraticAllocation(ctx, &types.MsgClaimDemocraticAllocation{Creator: stranger, OptionId: open.Id}); err != nil {
		t.Fatalf("stranger claiming open option: %v", err)
	}
	if _, err := ms.ClaimDemocraticAllocation(ctx, &types.MsgClaimDemocraticAllocation{Creator: stranger, OptionId: restricted.Id}); err == nil {
		t.Fatal("expected rejection: stranger triggering a restricted option")
	}
	if _, err := ms.ClaimDemocraticAllocation(ctx, &types.MsgClaimDemocraticAllocation{Creator: claimer, OptionId: restricted.Id}); err != nil {
		t.Fatalf("designated claimer: %v", err)
	}

	// Payout goes to the recipient regardless of who triggered it.
	o, _ := k.DemOptions.Get(ctx, open.Id)
	o.Accumulated = math.NewInt(700)
	if err := k.DemOptions.Set(ctx, open.Id, o); err != nil {
		t.Fatal(err)
	}
	if _, err := ms.ClaimDemocraticAllocation(ctx, &types.MsgClaimDemocraticAllocation{Creator: stranger, OptionId: open.Id}); err != nil {
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
}

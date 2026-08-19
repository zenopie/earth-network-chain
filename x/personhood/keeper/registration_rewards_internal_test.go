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

	allocationtypes "github.com/earth-network/earth/x/allocation/types"
	"github.com/earth-network/earth/x/personhood/types"
)

// recordingAllocation captures the bps a draw was requested at. That number is
// the whole mechanism: the unmatched half stays in the pool by never being
// drawn, so asserting on the payout alone would not distinguish this from the
// old behaviour that drew everything and handed it all to the registree.
type recordingAllocation struct {
	drawnAtBps int64
	payout     math.Int
}

func (r *recordingAllocation) AdvanceIndex(context.Context, allocationtypes.StreamId) error {
	return nil
}
func (r *recordingAllocation) ClearVoter(context.Context, allocationtypes.StreamId, []byte) error {
	return nil
}
func (r *recordingAllocation) DrawFromOption(
	_ context.Context, _ allocationtypes.StreamId, _ uint64, bps int64,
) (math.Int, error) {
	r.drawnAtBps = bps
	return r.payout, nil
}

// payingBank records what each address was paid, and what was minted.
type payingBank struct {
	minted math.Int
	paid   map[string]math.Int
}

func newPayingBank() *payingBank {
	return &payingBank{minted: math.ZeroInt(), paid: map[string]math.Int{}}
}

func (b *payingBank) GetSupply(context.Context, string) sdk.Coin { return sdk.Coin{} }
func (b *payingBank) SendCoinsFromModuleToAccount(
	_ context.Context, _ string, to sdk.AccAddress, amt sdk.Coins,
) error {
	cur, ok := b.paid[to.String()]
	if !ok {
		cur = math.ZeroInt()
	}
	b.paid[to.String()] = cur.Add(amt.AmountOf("uerth"))
	return nil
}
func (b *payingBank) SendCoinsFromAccountToModule(context.Context, sdk.AccAddress, string, sdk.Coins) error {
	return nil
}
func (b *payingBank) MintCoins(_ context.Context, _ string, amt sdk.Coins) error {
	b.minted = b.minted.Add(amt.AmountOf("uerth"))
	return nil
}
func (b *payingBank) BurnCoins(context.Context, string, sdk.Coins) error { return nil }

func rewardKeeper(t *testing.T, alloc *recordingAllocation, bank *payingBank) (Keeper, context.Context) {
	t.Helper()
	encCfg := moduletestutil.MakeTestEncodingConfig()
	ac := addresscodec.NewBech32Codec(sdk.GetConfig().GetBech32AccountAddrPrefix())
	storeKey := storetypes.NewKVStoreKey(types.StoreKey)
	ctx := testutil.DefaultContextWithDB(t, storeKey, storetypes.NewTransientStoreKey("transient_test")).Ctx
	k := NewKeeper(
		runtime.NewKVStoreService(storeKey),
		encCfg.Codec,
		ac,
		authtypes.NewModuleAddress(types.GovModuleName),
		bank,
		stubDex{},
		nil,
		alloc,
	)
	return k, ctx
}

func addrOf(b byte) sdk.AccAddress {
	return sdk.AccAddress{b, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20}
}

// A referred registration draws the full rate and splits it in half.
func TestRegistrationRewardSplitsWithReferrer(t *testing.T) {
	alloc := &recordingAllocation{payout: math.NewInt(1000)}
	bank := newPayingBank()
	k, ctx := rewardKeeper(t, alloc, bank)

	registree, referrer := addrOf(1), addrOf(2)
	got, err := k.payRegistrationReward(ctx, registree, referrer)
	if err != nil {
		t.Fatalf("payRegistrationReward: %v", err)
	}

	if alloc.drawnAtBps != types.RegistrationRewardBps {
		t.Fatalf("drew at %d bps, want %d", alloc.drawnAtBps, types.RegistrationRewardBps)
	}
	if !got.Equal(math.NewInt(500)) {
		t.Fatalf("registree got %s, want 500", got)
	}
	if p := bank.paid[referrer.String()]; !p.Equal(math.NewInt(500)) {
		t.Fatalf("referrer got %s, want 500", p)
	}
	if !bank.minted.Equal(math.NewInt(1000)) {
		t.Fatalf("minted %s, want 1000", bank.minted)
	}
}

// An unreferred registration draws only half the rate, leaving the referrer's
// half in the pool. The registree is paid exactly what a referred one is, which
// is the property that matters: naming a referrer must not cost the registree
// anything, or nobody rationally names one.
func TestRegistrationRewardKeepsReferrerShareInPool(t *testing.T) {
	alloc := &recordingAllocation{payout: math.NewInt(500)} // half the pool draw
	bank := newPayingBank()
	k, ctx := rewardKeeper(t, alloc, bank)

	registree := addrOf(1)
	got, err := k.payRegistrationReward(ctx, registree, nil)
	if err != nil {
		t.Fatalf("payRegistrationReward: %v", err)
	}

	if want := int64(types.RegistrationRewardBps / 2); alloc.drawnAtBps != want {
		t.Fatalf("drew at %d bps, want %d", alloc.drawnAtBps, want)
	}
	if !got.Equal(math.NewInt(500)) {
		t.Fatalf("registree got %s, want 500 — the same as a referred registration", got)
	}
	// Nothing is minted for an absent referrer.
	if !bank.minted.Equal(math.NewInt(500)) {
		t.Fatalf("minted %s, want 500", bank.minted)
	}
	if len(bank.paid) != 1 {
		t.Fatalf("paid %d addresses, want only the registree", len(bank.paid))
	}
}

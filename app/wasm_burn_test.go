package app

import (
	"context"
	"errors"
	"testing"

	storetypes "cosmossdk.io/store/types"
	wasmvmtypes "github.com/CosmWasm/wasmvm/v3/types"
	addresscodec "github.com/cosmos/cosmos-sdk/codec/address"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/cosmos/cosmos-sdk/testutil"
	sdk "github.com/cosmos/cosmos-sdk/types"
	moduletestutil "github.com/cosmos/cosmos-sdk/types/module/testutil"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/stretchr/testify/require"

	earthmodulekeeper "github.com/earth-network/earth/x/earth/keeper"
	earthmodule "github.com/earth-network/earth/x/earth/module"
	earthmoduletypes "github.com/earth-network/earth/x/earth/types"
)

// stubMessenger stands in for wasmd's default handler chain: it records that it
// was consulted and returns whatever the test needs. The decorator must always
// delegate before deciding anything.
type stubMessenger struct {
	called bool
	err    error
}

func (s *stubMessenger) DispatchMsg(
	sdk.Context, sdk.AccAddress, string, wasmvmtypes.CosmosMsg,
) ([]sdk.Event, [][]byte, [][]*codectypes.Any, error) {
	s.called = true
	return nil, nil, nil, s.err
}

// stubBank satisfies the earth keeper's bank surface. Nothing here burns for
// real — the decorator's job is the counter, and wasmd owns the actual
// BurnCoins.
type stubBank struct{}

func (stubBank) SpendableCoins(context.Context, sdk.AccAddress) sdk.Coins { return nil }
func (stubBank) SendCoinsFromModuleToModule(context.Context, string, string, sdk.Coins) error {
	return nil
}
func (stubBank) SendCoinsFromModuleToAccount(context.Context, string, sdk.AccAddress, sdk.Coins) error {
	return nil
}
func (stubBank) MintCoins(context.Context, string, sdk.Coins) error { return nil }
func (stubBank) BurnCoins(context.Context, string, sdk.Coins) error { return nil }

// newBurnFixture builds a real earth keeper over a test store so the burn
// counters can be read back. A stub keeper would only prove the decorator calls
// something; this proves the total actually moves.
func newBurnFixture(t *testing.T) (sdk.Context, earthmodulekeeper.Keeper) {
	t.Helper()

	encCfg := moduletestutil.MakeTestEncodingConfig(earthmodule.AppModule{})
	storeKey := storetypes.NewKVStoreKey(earthmoduletypes.StoreKey)
	ctx := testutil.DefaultContextWithDB(t, storeKey, storetypes.NewTransientStoreKey("transient_test")).Ctx

	k := earthmodulekeeper.NewKeeper(
		runtime.NewKVStoreService(storeKey),
		encCfg.Codec,
		addresscodec.NewBech32Codec(sdk.GetConfig().GetBech32AccountAddrPrefix()),
		authtypes.NewModuleAddress(earthmoduletypes.GovModuleName),
		stubBank{},
	)
	return ctx, k
}

func burnMsg(amount string) wasmvmtypes.CosmosMsg {
	return wasmvmtypes.CosmosMsg{
		Bank: &wasmvmtypes.BankMsg{
			Burn: &wasmvmtypes.BurnMsg{
				Amount: wasmvmtypes.Array[wasmvmtypes.Coin]{
					{Denom: "uerth", Amount: amount},
				},
			},
		},
	}
}

func totalWasmBurn(t *testing.T, ctx sdk.Context, k earthmodulekeeper.Keeper) sdk.Coins {
	t.Helper()
	bySource, err := k.BurnedBySource(ctx)
	require.NoError(t, err)
	for _, b := range bySource {
		if b.Source == earthmoduletypes.SourceWasm {
			return b.Amount
		}
	}
	return sdk.NewCoins()
}

// TestBurnRecorderCountsContractBurn is the whole point of the decorator: a
// contract's BankMsg::Burn reduces supply inside wasmd, and without this the
// chain's only record of destroyed supply would never hear about it.
func TestBurnRecorderCountsContractBurn(t *testing.T) {
	ctx, k := newBurnFixture(t)
	inner := &stubMessenger{}
	rec := burnRecorder{inner: inner, earth: k}

	_, _, _, err := rec.DispatchMsg(ctx, sdk.AccAddress("contract"), "", burnMsg("2500"))
	require.NoError(t, err)
	require.True(t, inner.called, "the real handler still runs; this only observes")

	require.Equal(t, "2500uerth", totalWasmBurn(t, ctx, k).String())

	// A second burn accumulates rather than replacing.
	_, _, _, err = rec.DispatchMsg(ctx, sdk.AccAddress("contract"), "", burnMsg("500"))
	require.NoError(t, err)
	require.Equal(t, "3000uerth", totalWasmBurn(t, ctx, k).String())
}

// TestBurnRecorderSkipsFailedDispatch is the ordering guarantee RecordBurn asks
// for: the counter must live or die with the burn it describes. A dispatch that
// failed destroyed nothing, so counting it would invent supply that never left.
func TestBurnRecorderSkipsFailedDispatch(t *testing.T) {
	ctx, k := newBurnFixture(t)
	inner := &stubMessenger{err: errors.New("insufficient funds")}
	rec := burnRecorder{inner: inner, earth: k}

	_, _, _, err := rec.DispatchMsg(ctx, sdk.AccAddress("contract"), "", burnMsg("2500"))

	require.Error(t, err, "the inner handler's error propagates unchanged")
	require.True(t, inner.called)
	require.True(t, totalWasmBurn(t, ctx, k).IsZero(), "a failed burn records nothing")
}

// TestBurnRecorderIgnoresOtherMessages guards the other direction. Every
// contract message passes through DispatchMsg — sends, staking, IBC, arbitrary
// Any — and only Burn reduces supply. Recording on anything else would inflate
// the burn total with ordinary transfers.
func TestBurnRecorderIgnoresOtherMessages(t *testing.T) {
	ctx, k := newBurnFixture(t)
	inner := &stubMessenger{}
	rec := burnRecorder{inner: inner, earth: k}

	send := wasmvmtypes.CosmosMsg{
		Bank: &wasmvmtypes.BankMsg{Send: &wasmvmtypes.SendMsg{
			ToAddress: "earth1abc",
			Amount:    wasmvmtypes.Array[wasmvmtypes.Coin]{{Denom: "uerth", Amount: "2500"}},
		}},
	}
	_, _, _, err := rec.DispatchMsg(ctx, sdk.AccAddress("contract"), "", send)
	require.NoError(t, err)
	require.True(t, inner.called)
	require.True(t, totalWasmBurn(t, ctx, k).IsZero(), "a send is not a burn")

	// A message with no Bank payload at all must not panic on the nil check.
	_, _, _, err = rec.DispatchMsg(ctx, sdk.AccAddress("contract"), "", wasmvmtypes.CosmosMsg{
		Custom: []byte(`{}`),
	})
	require.NoError(t, err)
	require.True(t, totalWasmBurn(t, ctx, k).IsZero())
}

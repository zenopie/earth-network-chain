package keeper_test

import (
	"context"
	"testing"
	"time"

	"cosmossdk.io/core/address"
	"cosmossdk.io/math"
	storetypes "cosmossdk.io/store/types"
	addresscodec "github.com/cosmos/cosmos-sdk/codec/address"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/cosmos/cosmos-sdk/testutil"
	sdk "github.com/cosmos/cosmos-sdk/types"
	moduletestutil "github.com/cosmos/cosmos-sdk/types/module/testutil"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/stretchr/testify/require"

	"github.com/earth-network/earth/x/shielded/keeper"
	module "github.com/earth-network/earth/x/shielded/module"
	shieldedtest "github.com/earth-network/earth/x/shielded/testutil"
	"github.com/earth-network/earth/x/shielded/types"
	"github.com/earth-network/earth/zk/privacy"
)

// fakeBank is an in-memory bank that runs the keeper's send restriction on
// every send, the way the real bank keeper runs it after app wiring.
type fakeBank struct {
	balances    map[string]sdk.Coins
	blocked     map[string]bool
	restriction func(ctx context.Context, from, to sdk.AccAddress, amt sdk.Coins) (sdk.AccAddress, error)
}

func newFakeBank() *fakeBank {
	return &fakeBank{balances: map[string]sdk.Coins{}, blocked: map[string]bool{}}
}

func (b *fakeBank) mint(addr sdk.AccAddress, coins ...sdk.Coin) {
	b.balances[string(addr)] = b.balances[string(addr)].Add(sdk.NewCoins(coins...)...)
}

func (b *fakeBank) send(ctx context.Context, from, to sdk.AccAddress, amt sdk.Coins) error {
	if b.restriction != nil {
		var err error
		if to, err = b.restriction(ctx, from, to, amt); err != nil {
			return err
		}
	}
	bal, neg := b.balances[string(from)].SafeSub(amt...)
	if neg {
		return sdkerrInsufficient
	}
	b.balances[string(from)] = bal
	b.balances[string(to)] = b.balances[string(to)].Add(amt...)
	return nil
}

var sdkerrInsufficient = types.ErrInvariant.Wrap("fake bank: insufficient funds")

func mod(name string) sdk.AccAddress { return authtypes.NewModuleAddress(name) }

func (b *fakeBank) GetBalance(_ context.Context, addr sdk.AccAddress, denom string) sdk.Coin {
	return sdk.NewCoin(denom, b.balances[string(addr)].AmountOf(denom))
}
func (b *fakeBank) GetAllBalances(_ context.Context, addr sdk.AccAddress) sdk.Coins {
	return b.balances[string(addr)]
}
func (b *fakeBank) SendCoinsFromAccountToModule(ctx context.Context, from sdk.AccAddress, m string, amt sdk.Coins) error {
	return b.send(ctx, from, mod(m), amt)
}
func (b *fakeBank) SendCoinsFromModuleToAccount(ctx context.Context, m string, to sdk.AccAddress, amt sdk.Coins) error {
	if b.blocked[string(to)] {
		return types.ErrSendRestricted.Wrap("fake bank: blocked")
	}
	return b.send(ctx, mod(m), to, amt)
}
func (b *fakeBank) SendCoinsFromModuleToModule(ctx context.Context, from, to string, amt sdk.Coins) error {
	return b.send(ctx, mod(from), mod(to), amt)
}
func (b *fakeBank) BlockedAddr(addr sdk.AccAddress) bool                  { return b.blocked[string(addr)] }
func (b *fakeBank) IsSendEnabledCoins(context.Context, ...sdk.Coin) error { return nil }

type fakeAuth struct{ ac address.Codec }

func (a fakeAuth) AddressCodec() address.Codec                               { return a.ac }
func (fakeAuth) GetModuleAddress(name string) sdk.AccAddress                 { return mod(name) }
func (fakeAuth) GetModuleAccount(context.Context, string) sdk.ModuleAccountI { return nil }

type fixture struct {
	t      *testing.T
	ctx    sdk.Context
	k      keeper.Keeper
	bank   *fakeBank
	ac     address.Codec
	msgs   types.MsgServer
	prover *shieldedtest.Prover
}

const personhood = "personhood"

// testTx is the tx every keeper test's private msg is proven for.
var testTx = types.TxFields{Memo: "keeper test", GasLimit: 2_500_000}

// initFixture builds a keeper over a fresh store with default genesis, the
// action verifying key set, and the scenario's chain id.
func initFixture(t *testing.T) *fixture {
	t.Helper()
	f := initFixtureEmpty(t, newFakeBank())
	gs := types.DefaultGenesis()
	gs.Params.VerifyingKeys = map[string][]byte{types.CircuitAction: f.prover.VerifyingKey(t)}
	require.NoError(t, f.k.InitGenesis(f.ctx, *gs))
	return f
}

// initFixtureEmpty is a keeper over a fresh store, before InitGenesis.
func initFixtureEmpty(t *testing.T, bank *fakeBank) *fixture {
	t.Helper()
	encCfg := moduletestutil.MakeTestEncodingConfig(module.AppModule{})
	ac := addresscodec.NewBech32Codec("earth")
	key := storetypes.NewKVStoreKey(types.StoreKey)
	ctx := testutil.DefaultContextWithKeys(map[string]*storetypes.KVStoreKey{types.StoreKey: key}, nil, nil).
		WithChainID(shieldedtest.ChainID).
		WithBlockHeight(1).
		WithBlockTime(time.Unix(1_800_000_000, 0).UTC())
	// As the private ante records them for a tx (keeper tests run no ante).
	ctx = types.WithTxFields(ctx, testTx)
	k := keeper.NewKeeper(runtime.NewKVStoreService(key), encCfg.Codec, ac,
		authtypes.NewModuleAddress(types.GovModuleName), fakeAuth{ac}, bank,
		[]string{types.AnmlDenom}, []string{personhood})
	bank.restriction = k.SendRestriction
	return &fixture{t: t, ctx: ctx, k: k, bank: bank, ac: ac, msgs: keeper.NewMsgServerImpl(k), prover: shieldedtest.DefaultProver(t)}
}

// nextBlock ends the current block and starts the next one dt later.
func (f *fixture) nextBlock(dt time.Duration) {
	f.t.Helper()
	require.NoError(f.t, f.k.EndBlocker(f.ctx))
	f.ctx = f.ctx.WithBlockHeight(f.ctx.BlockHeight() + 1).WithBlockTime(f.ctx.BlockTime().Add(dt)).
		WithEventManager(sdk.NewEventManager())
}

func (f *fixture) addr(label string) sdk.AccAddress {
	return sdk.AccAddress([]byte(label + "--------------------")[:20])
}

func (f *fixture) bech(a sdk.AccAddress) string {
	s, err := f.ac.BytesToString(a)
	require.NoError(f.t, err)
	return s
}

// shieldScenario shields the scenario's notes from a funded account.
func (f *fixture) shieldScenario(s shieldedtest.Scenario) {
	f.t.Helper()
	funder := f.addr("funder")
	for _, n := range s.Shields {
		coin := sdk.NewCoin(n.Denom, math.NewIntFromUint64(n.Value))
		f.bank.mint(funder, coin)
		_, err := f.msgs.Shield(f.ctx, &types.MsgShield{Sender: f.bech(funder), Amount: coin, Pc: privacy.FieldBytes(n.PC())})
		require.NoError(f.t, err)
	}
}

// runPrivate drives a private msg through the keeper side of the ante, then
// its handler, as the private chain does.
func (f *fixture) runPrivate(msg *types.MsgSend) (*types.MsgSendResponse, error) {
	if err := msg.ValidateBasic(); err != nil {
		return nil, err
	}
	prepared, err := f.k.CheckPrivateMsg(f.ctx, msg)
	if err != nil {
		return nil, err
	}
	if err := f.k.VerifyPrivateMsg(f.ctx, prepared); err != nil {
		return nil, err
	}
	cctx, write := f.ctx.CacheContext()
	actx, err := f.k.ExecutePrivateMsg(cctx, msg)
	if err != nil {
		return nil, err
	}
	write() // the ante's writes stand whatever the handler does
	hctx, writeMsg := f.ctx.CacheContext()
	res, err := f.msgs.Send(keeper.CarryAuthorization(hctx, actx), msg)
	if err != nil {
		return nil, err
	}
	writeMsg()
	return res, nil
}

// scenarioMsg is send i of s, proven (from the proof cache).
func (f *fixture) scenarioMsg(s shieldedtest.Scenario, i int) *types.MsgSend {
	f.t.Helper()
	msg, err := s.Msg(i, f.bech(shieldedtest.Receiver), testTx, func(toml string, pub [][]byte) []byte {
		return f.prover.Prove(f.t, toml, pub)
	})
	require.NoError(f.t, err)
	return msg
}

// verify is CheckPrivateMsg then VerifyPrivateMsg.
func (f *fixture) verify(ctx sdk.Context, msg types.PrivateMsg) error {
	prepared, err := f.k.CheckPrivateMsg(ctx, msg)
	if err != nil {
		return err
	}
	return f.k.VerifyPrivateMsg(ctx, prepared)
}

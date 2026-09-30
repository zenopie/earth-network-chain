package keeper_test

import (
	"bytes"
	"testing"
	"time"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/stretchr/testify/require"

	"github.com/earth-network/earth/x/shielded/keeper"
	shieldedtest "github.com/earth-network/earth/x/shielded/testutil"
	"github.com/earth-network/earth/x/shielded/types"
	"github.com/earth-network/earth/zk/privacy"
)

// The scenario end to end at the keeper: shields, a private send, an
// unshield — each proof verified for real against a tree the keeper built.
func TestScenarioShieldTransferUnshield(t *testing.T) {
	f := initFixture(t)
	s := shieldedtest.Default()
	f.shieldScenario(s)
	f.nextBlock(5 * time.Second)

	// transfer 0: private send.
	msg0 := f.scenarioMsg(s, 0)
	res, err := f.runPrivate(msg0)
	require.NoError(t, err)
	require.Equal(t, []uint64{2, 3, 4}, res.Positions)
	f.nextBlock(5 * time.Second)

	// Double spend: every nullifier is now spent.
	_, err = f.k.CheckPrivateMsg(f.ctx, msg0)
	require.ErrorIs(t, err, types.ErrNullifierSpent)

	// transfer 1: unshield 500,000 to the receiver.
	msg1 := f.scenarioMsg(s, 1)
	res, err = f.runPrivate(msg1)
	require.NoError(t, err)
	require.Equal(t, []uint64{5, 6, 7}, res.Positions)
	require.Equal(t, int64(500_000), f.bank.GetBalance(f.ctx, shieldedtest.Receiver, types.FeeDenom).Amount.Int64())

	// Turnstile: 1,100,000 in; 2 x 20,000 fees + 500,000 out.
	ts, err := f.k.Turnstile(f.ctx, types.FeeDenom)
	require.NoError(t, err)
	require.Equal(t, int64(1_100_000), ts.In.Int64())
	require.Equal(t, int64(540_000), ts.Out.Int64())
	require.Equal(t, int64(40_000), f.bank.GetBalance(f.ctx, authtypes.NewModuleAddress(authtypes.FeeCollectorName), types.FeeDenom).Amount.Int64())
	f.nextBlock(5 * time.Second) // asserts the moved turnstiles
	require.NoError(t, f.k.AssertInvariants(f.ctx))

	// The keeper's tree is the scenario's tree.
	want, err := s.TreeBefore(len(s.Transfers))
	require.NoError(t, err)
	wantRoot, err := want.Root()
	require.NoError(t, err)
	got, err := f.k.CurrentRoot(f.ctx)
	require.NoError(t, err)
	require.Equal(t, privacy.FieldBytes(wantRoot), got)
	size, err := f.k.Size(f.ctx)
	require.NoError(t, err)
	require.Equal(t, uint64(8), size)
}

func TestProofBindsEveryPublicInput(t *testing.T) {
	f := initFixture(t)
	s := shieldedtest.Default()
	f.shieldScenario(s)
	f.nextBlock(5 * time.Second)
	_, err := f.runPrivate(f.scenarioMsg(s, 0))
	require.NoError(t, err)
	f.nextBlock(5 * time.Second)

	mutations := map[string]func(m *types.MsgTransfer){
		"receiver":   func(m *types.MsgTransfer) { m.Receiver = f.bech(f.addr("thief")) },
		"ciphertext": func(m *types.MsgTransfer) { m.Transfer.Ciphertexts[1] = []byte("garbage") },
		"fee":        func(m *types.MsgTransfer) { m.Transfer.Fee++ },
		"value_out":  func(m *types.MsgTransfer) { m.Transfer.ValueOut-- },
		"commitment": func(m *types.MsgTransfer) {
			m.Transfer.Commitments[0] = privacy.FieldBytes(shieldedtest.Det("x", 1))
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			m := f.scenarioMsg(s, 1)
			mutate(m)
			prepared, err := f.k.CheckPrivateMsg(f.ctx, m)
			require.NoError(t, err)
			require.ErrorIs(t, f.k.VerifyPrivateMsg(f.ctx, prepared), types.ErrInvalidProof)
		})
	}
	// Wrong chain: the signal binds the chain id.
	m := f.scenarioMsg(s, 1)
	other := f.ctx.WithChainID("earth-1")
	prepared, err := f.k.CheckPrivateMsg(other, m)
	require.NoError(t, err)
	require.ErrorIs(t, f.k.VerifyPrivateMsg(other, prepared), types.ErrInvalidProof)

	// Unshield of an asset the proof did not hide: asset_pub comes from the
	// registry and no longer matches.
	_, err = f.k.RegisterAsset(f.ctx, "ufoo")
	require.NoError(t, err)
	m = f.scenarioMsg(s, 1)
	m.Transfer.DenomOut = "ufoo"
	prepared, err = f.k.CheckPrivateMsg(f.ctx, m)
	require.NoError(t, err)
	require.ErrorIs(t, f.k.VerifyPrivateMsg(f.ctx, prepared), types.ErrInvalidProof)
}

func TestCheckPrivateMsgRefusals(t *testing.T) {
	f := initFixture(t)
	s := shieldedtest.Default()
	f.shieldScenario(s)
	f.nextBlock(5 * time.Second)

	// Unknown root.
	m := f.scenarioMsg(s, 0)
	m.Transfer.Root = privacy.FieldBytes(shieldedtest.Det("root", 9))
	_, err := f.k.CheckPrivateMsg(f.ctx, m)
	require.ErrorIs(t, err, types.ErrUnknownRoot)

	// Unregistered denom out.
	m = f.scenarioMsg(s, 0)
	m.Transfer.ValueOut, m.Transfer.DenomOut, m.Receiver = 1, "unope", f.bech(f.addr("r"))
	_, err = f.k.CheckPrivateMsg(f.ctx, m)
	require.ErrorIs(t, err, types.ErrAssetNotRegistered)

	// Unshielding ANML to an account: the bank would refuse it after the ante
	// spent the inputs, so the check refuses it first.
	m = f.scenarioMsg(s, 0)
	m.Transfer.ValueOut, m.Transfer.DenomOut, m.Receiver = 1, types.AnmlDenom, f.bech(f.addr("r"))
	_, err = f.k.CheckPrivateMsg(f.ctx, m)
	require.ErrorIs(t, err, types.ErrSendRestricted)

	// The pool itself as receiver: it would count Out with no coins moving.
	m = f.scenarioMsg(s, 0)
	m.Transfer.ValueOut, m.Transfer.DenomOut, m.Receiver = 1, types.FeeDenom, f.bech(f.k.PoolAddress())
	_, err = f.k.CheckPrivateMsg(f.ctx, m)
	require.ErrorIs(t, err, types.ErrSendRestricted)

	// Blocked receiver.
	blocked := f.addr("blocked")
	f.bank.blocked[string(blocked)] = true
	m = f.scenarioMsg(s, 0)
	m.Transfer.ValueOut, m.Transfer.DenomOut, m.Receiver = 1, types.FeeDenom, f.bech(blocked)
	_, err = f.k.CheckPrivateMsg(f.ctx, m)
	require.ErrorIs(t, err, types.ErrSendRestricted)

	// No verifying key: refused, not accepted.
	params, err := f.k.Params.Get(f.ctx)
	require.NoError(t, err)
	params.VerifyingKeys = nil
	require.NoError(t, f.k.Params.Set(f.ctx, params))
	prepared, err := f.k.CheckPrivateMsg(f.ctx, f.scenarioMsg(s, 0))
	require.NoError(t, err)
	require.ErrorIs(t, f.k.VerifyPrivateMsg(f.ctx, prepared), types.ErrMissingVerifyingKey)
}

// A private msg's handler refuses unless the ante authorized its transfer in
// this tx: the defence against a contract or ICA host dispatching it.
func TestHandlerRequiresAnteAuthorization(t *testing.T) {
	f := initFixture(t)
	s := shieldedtest.Default()
	f.shieldScenario(s)
	f.nextBlock(5 * time.Second)
	m := f.scenarioMsg(s, 0)

	_, err := f.msgs.Transfer(f.ctx, m)
	require.ErrorIs(t, err, types.ErrUnauthorized)
	require.False(t, keeper.AuthorizedNullifiers(f.ctx, m.Transfer.Nullifiers...))

	// Authorized for a different transfer: still refused.
	other := f.scenarioMsg(s, 1)
	actx := keeper.WithAuthorizedTransfer(f.ctx, &other.Transfer, []uint64{0, 1, 2})
	_, err = f.msgs.Transfer(actx, m)
	require.ErrorIs(t, err, types.ErrUnauthorized)

	// SpendToModule pays an authorized transfer's value_out once.
	_, err = f.runPrivate(m)
	require.NoError(t, err)
	f.nextBlock(5 * time.Second)
	cctx, _ := f.ctx.CacheContext()
	prepared, err := f.k.CheckPrivateMsg(cctx, other)
	require.NoError(t, err)
	require.NoError(t, f.k.VerifyPrivateMsg(cctx, prepared))
	actx, err = f.k.ExecutePrivateMsg(cctx, other)
	require.NoError(t, err)
	coin, err := f.k.SpendToModule(actx, &other.Transfer, personhood)
	require.NoError(t, err)
	require.Equal(t, "500000uerth", coin.String())
	require.Equal(t, int64(500_000), f.bank.GetBalance(actx, mod(personhood), types.FeeDenom).Amount.Int64())
	_, err = f.k.SpendToModule(actx, &other.Transfer, personhood)
	require.ErrorIs(t, err, types.ErrAlreadyReleased)
	_, err = f.msgs.Transfer(actx, other)
	require.ErrorIs(t, err, types.ErrAlreadyReleased)
	require.NoError(t, f.k.AssertInvariants(actx))
}

func TestSendRestriction(t *testing.T) {
	f := initFixture(t)
	user, other := f.addr("user"), f.addr("other")
	f.bank.mint(user, sdk.NewInt64Coin(types.FeeDenom, 1000), sdk.NewInt64Coin(types.AnmlDenom, 1000))

	// Nothing reaches the pool except through the keeper.
	err := f.bank.send(f.ctx, user, f.k.PoolAddress(), sdk.NewCoins(sdk.NewInt64Coin(types.FeeDenom, 1)))
	require.ErrorIs(t, err, types.ErrSendRestricted)
	err = f.bank.SendCoinsFromModuleToModule(f.ctx, personhood, types.ModuleName, sdk.NewCoins(sdk.NewInt64Coin(types.FeeDenom, 1)))
	require.ErrorIs(t, err, types.ErrSendRestricted)

	// ANML goes only to the pool and the listed modules.
	err = f.bank.send(f.ctx, user, other, sdk.NewCoins(sdk.NewInt64Coin(types.AnmlDenom, 1)))
	require.ErrorIs(t, err, types.ErrSendRestricted)
	err = f.bank.send(f.ctx, user, mod("gov"), sdk.NewCoins(sdk.NewInt64Coin(types.AnmlDenom, 1)))
	require.ErrorIs(t, err, types.ErrSendRestricted)
	require.NoError(t, f.bank.send(f.ctx, user, mod(personhood), sdk.NewCoins(sdk.NewInt64Coin(types.AnmlDenom, 1))))
	// ERTH moves freely.
	require.NoError(t, f.bank.send(f.ctx, user, other, sdk.NewCoins(sdk.NewInt64Coin(types.FeeDenom, 1))))

	// MsgShield of ANML is a counted deposit.
	pc := privacy.FieldBytes(shieldedtest.Det("pc", 1))
	_, err = f.msgs.Shield(f.ctx, &types.MsgShield{Sender: f.bech(user), Amount: sdk.NewInt64Coin(types.AnmlDenom, 10), Pc: pc})
	require.NoError(t, err)

	// MintNote from a module holding the coins.
	f.bank.mint(mod(personhood), sdk.NewInt64Coin(types.AnmlDenom, 1_000_000))
	pos, cm, err := f.k.MintNote(f.ctx, personhood, sdk.NewInt64Coin(types.AnmlDenom, 1_000_000), pc, nil)
	require.NoError(t, err)
	require.Equal(t, uint64(1), pos)
	pcEl, _ := privacy.FieldFromBytes(pc)
	require.Equal(t, privacy.FieldBytes(privacy.CM(privacy.AssetID(types.AnmlDenom), 1_000_000, pcEl)), cm)
	ts, err := f.k.Turnstile(f.ctx, types.AnmlDenom)
	require.NoError(t, err)
	require.Equal(t, int64(1_000_010), ts.In.Int64())
	require.NoError(t, f.k.AssertInvariants(f.ctx))

	// MintNote refuses an unregistered denom, a non-canonical pc, a value
	// beyond u64, and coins the module does not hold.
	_, _, err = f.k.MintNote(f.ctx, personhood, sdk.NewInt64Coin("unope", 1), pc, nil)
	require.ErrorIs(t, err, types.ErrAssetNotRegistered)
	_, _, err = f.k.MintNote(f.ctx, personhood, sdk.NewInt64Coin(types.AnmlDenom, 1), bytes.Repeat([]byte{0xff}, 32), nil)
	require.ErrorIs(t, err, types.ErrInvalidNote)
	huge := sdk.NewCoin(types.AnmlDenom, math.NewIntFromUint64(^uint64(0)).AddRaw(1))
	_, _, err = f.k.MintNote(f.ctx, personhood, huge, pc, nil)
	require.ErrorIs(t, err, types.ErrInvalidNote)
	_, _, err = f.k.MintNote(f.ctx, personhood, sdk.NewInt64Coin(types.AnmlDenom, 5), pc, nil)
	require.Error(t, err)
}

func TestInvariantCatchesUncountedCoins(t *testing.T) {
	f := initFixture(t)
	s := shieldedtest.Default()
	f.shieldScenario(s)
	f.nextBlock(5 * time.Second)
	require.NoError(t, f.k.AssertInvariants(f.ctx))

	// A surplus: coins in the pool that no turnstile counted.
	f.bank.mint(f.k.PoolAddress(), sdk.NewInt64Coin(types.FeeDenom, 1))
	require.ErrorIs(t, f.k.AssertInvariants(f.ctx), types.ErrInvariant)
	// EndBlock checks the denoms that moved this block: shield one more uerth
	// note and the block halts.
	f.shieldScenario(shieldedtest.Scenario{Shields: s.Shields[:1]})
	require.ErrorIs(t, f.k.EndBlocker(f.ctx), types.ErrInvariant)

	// A denom held with no turnstile at all.
	g := initFixture(t)
	g.bank.mint(g.k.PoolAddress(), sdk.NewInt64Coin("ustray", 1))
	require.ErrorIs(t, g.k.AssertInvariants(g.ctx), types.ErrInvariant)
}

func TestRootWindow(t *testing.T) {
	f := initFixture(t)
	params, err := f.k.Params.Get(f.ctx)
	require.NoError(t, err)
	window := time.Duration(params.RootWindowSeconds) * time.Second

	// Genesis recorded the empty root.
	empty, err := f.k.CurrentRoot(f.ctx)
	require.NoError(t, err)
	ok, _, _, err := f.k.Anchor(f.ctx, empty)
	require.NoError(t, err)
	require.True(t, ok)

	s := shieldedtest.Default()
	f.shieldScenario(shieldedtest.Scenario{Shields: s.Shields[:1]})
	r1, _ := f.k.CurrentRoot(f.ctx)
	ok, _, _, _ = f.k.Anchor(f.ctx, r1)
	require.False(t, ok, "a root is an anchor only once its block ends")
	f.nextBlock(5 * time.Second)
	ok, _, _, _ = f.k.Anchor(f.ctx, r1)
	require.True(t, ok)

	// An unchanged tree records nothing new.
	f.nextBlock(5 * time.Second)
	roots, err := f.k.ExportGenesis(f.ctx)
	require.NoError(t, err)
	require.Len(t, roots.Roots, 2)

	// The latest root outlives the window; the empty root does not.
	f.nextBlock(window + time.Hour)
	ok, _, _, _ = f.k.Anchor(f.ctx, r1)
	require.True(t, ok, "the latest root never expires")
	ok, _, _, _ = f.k.Anchor(f.ctx, empty)
	require.False(t, ok)
	f.nextBlock(5 * time.Second) // EndBlock prunes it
	has, err := f.k.Roots.Has(f.ctx, empty)
	require.NoError(t, err)
	require.False(t, has, "pruned")

	// Once superseded, an old latest root expires by its own age.
	f.shieldScenario(shieldedtest.Scenario{Shields: s.Shields[1:]})
	f.nextBlock(5 * time.Second)
	ok, _, _, _ = f.k.Anchor(f.ctx, r1)
	require.False(t, ok)
}

func TestBlockCap(t *testing.T) {
	f := initFixture(t)
	params, err := f.k.Params.Get(f.ctx)
	require.NoError(t, err)
	params.MaxPrivateTxsPerBlock = 2
	require.NoError(t, f.k.Params.Set(f.ctx, params))
	require.NoError(t, f.k.CountPrivateTx(f.ctx))
	require.NoError(t, f.k.CountPrivateTx(f.ctx))
	require.ErrorIs(t, f.k.CountPrivateTx(f.ctx), types.ErrBlockCap)
	f.nextBlock(5 * time.Second)
	require.NoError(t, f.k.CountPrivateTx(f.ctx), "the count resets every block")
}

func TestGenesisRoundTrip(t *testing.T) {
	f := initFixture(t)
	s := shieldedtest.Default()
	f.shieldScenario(s)
	f.nextBlock(5 * time.Second)
	_, err := f.runPrivate(f.scenarioMsg(s, 0))
	require.NoError(t, err)
	f.nextBlock(5 * time.Second)
	_, err = f.runPrivate(f.scenarioMsg(s, 1))
	require.NoError(t, err)
	f.nextBlock(5 * time.Second)

	gs, err := f.k.ExportGenesis(f.ctx)
	require.NoError(t, err)
	require.NoError(t, gs.Validate())
	require.Len(t, gs.Commitments, 8)
	require.Len(t, gs.Nullifiers, 6)
	require.Len(t, gs.Roots, 4) // empty, after shields, after t0, after t1

	// Import into a fresh keeper over the same bank balances.
	g := initFixtureEmpty(t, f.bank)
	g.ctx = g.ctx.WithBlockTime(f.ctx.BlockTime()).WithBlockHeight(f.ctx.BlockHeight())
	require.NoError(t, g.k.InitGenesis(g.ctx, *gs))
	gs2, err := g.k.ExportGenesis(g.ctx)
	require.NoError(t, err)
	require.Equal(t, gs, gs2)
	r1, _ := f.k.CurrentRoot(f.ctx)
	r2, _ := g.k.CurrentRoot(g.ctx)
	require.Equal(t, r1, r2)

	// The spent set came across: transfer 1 cannot be replayed.
	_, err = g.k.CheckPrivateMsg(g.ctx, g.scenarioMsg(s, 1))
	require.ErrorIs(t, err, types.ErrNullifierSpent)

	// A genesis whose turnstiles disagree with the bank is refused.
	bad := *gs
	bad.Turnstiles = nil
	g3 := initFixtureEmpty(t, f.bank)
	require.ErrorIs(t, g3.k.InitGenesis(g3.ctx, bad), types.ErrInvariant)
}

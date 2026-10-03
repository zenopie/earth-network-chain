package keeper_test

import (
	"bytes"
	"testing"
	"time"

	"cosmossdk.io/math"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/stretchr/testify/require"

	"github.com/earth-network/earth/x/shielded/keeper"
	shieldedtest "github.com/earth-network/earth/x/shielded/testutil"
	"github.com/earth-network/earth/x/shielded/types"
	"github.com/earth-network/earth/zk/orchard"
	"github.com/earth-network/earth/zk/privacy"
)

// runScenario shields the scenario's notes and runs its accepted sends up to
// (not including) send n, a block each.
func (f *fixture) runScenario(s shieldedtest.Scenario, n int) {
	f.t.Helper()
	f.shieldScenario(s)
	f.nextBlock(5 * time.Second)
	for i := range n {
		if s.Sends[i].Refused {
			continue
		}
		_, err := f.runPrivate(f.scenarioMsg(s, i))
		require.NoError(f.t, err, s.Sends[i].Name)
		f.nextBlock(5 * time.Second)
	}
}

// The scenario end to end at the keeper: shields, then bundles of 2, 3 (mixed
// assets), 2 (an unshield paying its fee from what it releases) and 10
// actions, each action proof and binding signature verified for real against
// a tree the keeper built; then a double spend and a one-action bundle,
// refused.
func TestScenarioBundles(t *testing.T) {
	f := initFixture(t)
	s := shieldedtest.Default()
	f.shieldScenario(s)
	f.nextBlock(5 * time.Second)
	feeCollector := authtypes.NewModuleAddress(authtypes.FeeCollectorName)

	want := map[int][]uint64{
		shieldedtest.Send2:       {11, 12},
		shieldedtest.Multi3:      {13, 14, 15},
		shieldedtest.Unshield2:   {16, 17},
		shieldedtest.Consolidate: {18, 19, 20, 21, 22, 23, 24, 25, 26, 27},
	}
	for _, i := range []int{shieldedtest.Send2, shieldedtest.Multi3, shieldedtest.Unshield2, shieldedtest.Consolidate} {
		msg := f.scenarioMsg(s, i)
		res, err := f.runPrivate(msg)
		require.NoError(t, err, s.Sends[i].Name)
		require.Equal(t, want[i], res.Positions, s.Sends[i].Name)
		// Every nullifier is now spent: the same msg again is a double spend.
		_, err = f.k.CheckPrivateMsg(f.ctx, msg)
		require.ErrorIs(t, err, types.ErrNullifierSpent)
		f.nextBlock(5 * time.Second)
	}

	// The unshield paid 500,000 to the receiver and its 30,000 fee out of the
	// same 530,000 uerth balance: no fee note.
	require.Equal(t, int64(500_000), f.bank.GetBalance(f.ctx, shieldedtest.Receiver, types.FeeDenom).Amount.Int64())
	// Fees: 30,000 + 40,000 + 30,000 + 130,000.
	require.Equal(t, int64(230_000), f.bank.GetBalance(f.ctx, feeCollector, types.FeeDenom).Amount.Int64())
	ts, err := f.k.Turnstile(f.ctx, types.FeeDenom)
	require.NoError(t, err)
	require.Equal(t, int64(1_116_000), ts.In.Int64())
	require.Equal(t, int64(730_000), ts.Out.Int64())
	ts, err = f.k.Turnstile(f.ctx, types.AnmlDenom)
	require.NoError(t, err)
	require.Equal(t, int64(5_000_000), ts.In.Int64())
	require.True(t, ts.Out.IsZero(), "the ANML send released nothing")
	require.NoError(t, f.k.AssertInvariants(f.ctx))

	// The keeper's tree is the scenario's tree.
	wantTree, err := s.TreeBefore(shieldedtest.DoubleSpend)
	require.NoError(t, err)
	wantRoot, err := wantTree.Root()
	require.NoError(t, err)
	got, err := f.k.CurrentRoot(f.ctx)
	require.NoError(t, err)
	require.Equal(t, privacy.FieldBytes(wantRoot), got)
	size, err := f.k.Size(f.ctx)
	require.NoError(t, err)
	require.Equal(t, uint64(28), size)

	// A fresh bundle (new outputs, a new sighash, valid proofs) spending
	// send2's note again: its nullifier is already in the set.
	double := f.scenarioMsg(s, shieldedtest.DoubleSpend)
	require.NoError(t, double.ValidateBasic())
	_, err = f.runPrivate(double)
	require.ErrorIs(t, err, types.ErrNullifierSpent)

	// One action with a real proof and a valid binding signature: the bundle
	// verifies in zk/orchard, and the chain refuses it for its shape alone.
	single := f.scenarioMsg(s, shieldedtest.SingleAction)
	ob, err := single.Bundle.ToOrchard()
	require.NoError(t, err)
	sighash, err := types.Sighash(single, shieldedtest.ChainID, testTx, f.ac)
	require.NoError(t, err)
	require.NoError(t, ob.Verify(sighash, orchard.CanonicalBase, func(p []byte, in [][]byte) (bool, error) {
		return f.k.VerifyCircuit(f.ctx, types.CircuitAction, p, in) == nil, nil
	}))
	require.ErrorIs(t, single.ValidateBasic(), types.ErrInvalidBundle)
	require.ErrorContains(t, single.ValidateBasic(), "pad with dummies")
}

// The chain's sighash is the scenario's (the wallet's): the msg type, the
// chain id, the bundle digest, the receiver and the fee.
func TestSighashMatchesWallet(t *testing.T) {
	s := shieldedtest.Default()
	f := initFixture(t)
	for i := range s.Sends {
		msg := f.scenarioMsg(s, i)
		got, err := types.Sighash(msg, shieldedtest.ChainID, testTx, f.ac)
		require.NoError(t, err)
		b, err := s.Bundle(i)
		require.NoError(t, err)
		want, err := s.Sighash(i, b, testTx)
		require.NoError(t, err)
		require.Equal(t, want, got, s.Sends[i].Name)
	}
}

// Everything the sighash binds is bound: change any of it and the binding
// signature fails; re-sign it with the real binding key (a wallet that leaked
// its rcvs, or the relay that built it) and the action proofs fail, because
// each binds the old sighash. A proof cannot be moved into another tx.
func TestSighashBindsEverything(t *testing.T) {
	f := initFixture(t)
	s := shieldedtest.Default()
	f.runScenario(s, shieldedtest.Unshield2)
	i := shieldedtest.Unshield2

	mutations := map[string]func(m *types.MsgSend){
		"receiver":   func(m *types.MsgSend) { m.Receiver = f.bech(f.addr("thief")) },
		"ciphertext": func(m *types.MsgSend) { m.Bundle.Actions[1].Ciphertext = shieldedtest.NoteCT("garbage") },
		"commitment": func(m *types.MsgSend) {
			m.Bundle.Actions[0].Commitment = privacy.FieldBytes(shieldedtest.Det("x", 1))
		},
		"fee and receiver's share": func(m *types.MsgSend) { m.Fee += 1_000 },
		"actions swapped": func(m *types.MsgSend) {
			m.Bundle.Actions[0], m.Bundle.Actions[1] = m.Bundle.Actions[1], m.Bundle.Actions[0]
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			m := f.scenarioMsg(s, i)
			mutate(m)
			require.NoError(t, m.ValidateBasic())
			require.ErrorIs(t, f.verify(f.ctx, m), types.ErrInvalidBindingSig)
			// Re-signed over the new sighash with the true bsk: the
			// balance holds, the proofs do not.
			sh, err := types.Sighash(m, shieldedtest.ChainID, testTx, f.ac)
			require.NoError(t, err)
			m.Bundle.BindingSig, err = orchard.SignBinding(orchard.BindingSigningKey(s.Bsk(i)), sh, bytes.NewReader(make([]byte, 32)))
			require.NoError(t, err)
			require.ErrorIs(t, f.verify(f.ctx, m), types.ErrInvalidProof)
		})
	}

	// Wrong chain: the sighash binds the chain id.
	other := f.ctx.WithChainID("earth-1")
	require.ErrorIs(t, f.verify(other, f.scenarioMsg(s, i)), types.ErrInvalidBindingSig)

	// A proof lifted into another msg: send2's proof of action 0, placed in a
	// bundle otherwise identical to unshield2's.
	m := f.scenarioMsg(s, i)
	m.Bundle.Actions[0].Proof = f.scenarioMsg(s, shieldedtest.Send2).Bundle.Actions[0].Proof
	require.ErrorIs(t, f.verify(f.ctx, m), types.ErrInvalidProof)
	require.ErrorContains(t, f.verify(f.ctx, m), "bundle 0 action 0")

	// And the real msg still verifies.
	require.NoError(t, f.verify(f.ctx, f.scenarioMsg(s, i)))
}

// Value cannot be created: a balance claiming more (or less) than the
// actions commit to fails the binding signature, even signed with the real
// binding key, and a cv changed after proving fails both.
func TestInflationRefused(t *testing.T) {
	f := initFixture(t)
	s := shieldedtest.Default()
	f.runScenario(s, shieldedtest.Unshield2)
	i := shieldedtest.Unshield2

	resign := func(m *types.MsgSend) {
		sh, err := types.Sighash(m, shieldedtest.ChainID, testTx, f.ac)
		require.NoError(t, err)
		m.Bundle.BindingSig, err = orchard.SignBinding(orchard.BindingSigningKey(s.Bsk(i)), sh, bytes.NewReader(make([]byte, 32)))
		require.NoError(t, err)
	}
	for name, mutate := range map[string]func(m *types.MsgSend){
		"balance +1": func(m *types.MsgSend) { m.Bundle.Balances[0].Amount++ },
		"balance -1": func(m *types.MsgSend) { m.Bundle.Balances[0].Amount-- },
		"extra ANML out": func(m *types.MsgSend) {
			m.Bundle.Balances = append(m.Bundle.Balances, types.ValueBalance{Denom: types.AnmlDenom, Amount: 1})
		},
		"cv shifted by G": func(m *types.MsgSend) {
			cv, _ := orchard.PointFromBytes(m.Bundle.Actions[1].Cv)
			m.Bundle.Actions[1].Cv = orchard.PointBytes(orchard.Add(cv, orchard.ValueBase(privacy.AssetID(types.FeeDenom))))
		},
	} {
		t.Run(name, func(t *testing.T) {
			m := f.scenarioMsg(s, i)
			mutate(m)
			resign(m)
			prepared, err := f.k.CheckPrivateMsg(f.ctx, m)
			if err != nil {
				// The ANML balance has no receiver able to take it.
				require.ErrorIs(t, err, types.ErrSendRestricted)
				err = f.k.VerifyPrivateMsg(f.ctx, keeper.PreparedPrivateMsg{Msg: m, Bundles: mustOrchard(t, m), Sighash: mustSighash(t, f, m)})
			} else {
				err = f.k.VerifyPrivateMsg(f.ctx, prepared)
			}
			require.ErrorIs(t, err, types.ErrInvalidBindingSig)
		})
	}
}

// The -G attack: two outputs of v ANML, one under G and one under -G, net to
// zero, so their binding signature verifies and v ANML would come from
// nothing. The chain cannot see it; the action circuit's canonical-y check is
// the whole defence. Shown here: the forged bundle balances at the binding
// level, no witness for the -G action exists (the circuit refuses it, when
// EARTH_CIRCUITS is set), and with any proof the chain holds in its place the
// bundle is refused.
func TestNegatedBaseInflationNeedsAnImpossibleProof(t *testing.T) {
	f := initFixture(t)
	s := shieldedtest.Default()
	f.shieldScenario(s)
	f.nextBlock(5 * time.Second)
	tree, err := s.TreeBefore(0)
	require.NoError(t, err)
	root, err := tree.Root()
	require.NoError(t, err)

	const v = 1_000_000
	anml := privacy.AssetID(types.AnmlDenom)
	g := orchard.ValueBase(anml)
	r1, r2 := shieldedtest.Det("forge/rcv", 0), shieldedtest.Det("forge/rcv", 1)
	out1 := shieldedtest.Note{Owner: shieldedtest.Alice, Denom: types.AnmlDenom, Value: v, Rho: shieldedtest.Det("forge/rho", 0), Rcm: shieldedtest.Det("forge/rcm", 0)}
	out2 := out1
	out2.Rho, out2.Rcm = shieldedtest.Det("forge/rho", 1), shieldedtest.Det("forge/rcm", 1)
	cv1 := orchard.ValueCommit(privacy.AssetID(types.FeeDenom), 0, anml, v, r1) // honest: -v*G + r1*R
	cv2 := orchard.Add(orchard.Mul(g, orchard.ScalarU64(v)), orchard.Mul(orchard.R, orchard.ScalarFromField(r2)))
	dummy := func(j uint64) (fr.Element, fr.Element) {
		return shieldedtest.Det("forge/drho", j), shieldedtest.Det("forge/drcm", j)
	}
	rho1, rcm1 := dummy(0)
	rho2, rcm2 := dummy(1)
	b := types.Bundle{Actions: []types.Action{
		{Anchor: privacy.FieldBytes(root), Nullifier: privacy.FieldBytes(privacy.NF(shieldedtest.Alice.NK, rho1, 0)),
			Commitment: privacy.FieldBytes(out1.CM()), Cv: orchard.PointBytes(cv1), Ciphertext: shieldedtest.NoteCT("forge/1")},
		{Anchor: privacy.FieldBytes(root), Nullifier: privacy.FieldBytes(privacy.NF(shieldedtest.Alice.NK, rho2, 0)),
			Commitment: privacy.FieldBytes(out2.CM()), Cv: orchard.PointBytes(cv2), Ciphertext: shieldedtest.NoteCT("forge/2")},
	}}
	// A MsgSend must pay a fee, and nothing is spent: the forger mints the
	// fee the same way, a third action committing +1*G_uerth (a -G_uerth
	// output of 1), against a public uerth balance of 1.
	m := &types.MsgSend{Bundle: b, Fee: 1}
	m.Bundle.Balances = []types.ValueBalance{{Denom: types.FeeDenom, Amount: 1}}
	gErth := orchard.ValueBase(privacy.AssetID(types.FeeDenom))
	r3 := shieldedtest.Det("forge/rcv", 2)
	rho3, _ := dummy(2)
	cv3 := orchard.Add(orchard.Mul(gErth, orchard.ScalarU64(1)), orchard.Mul(orchard.R, orchard.ScalarFromField(r3)))
	out3 := out1
	out3.Rho, out3.Denom, out3.Value = shieldedtest.Det("forge/rho", 2), types.FeeDenom, 1
	m.Bundle.Actions = append(m.Bundle.Actions, types.Action{Anchor: privacy.FieldBytes(root),
		Nullifier: privacy.FieldBytes(privacy.NF(shieldedtest.Alice.NK, rho3, 0)), Commitment: privacy.FieldBytes(out3.CM()), Cv: orchard.PointBytes(cv3),
		Ciphertext: shieldedtest.NoteCT("forge/3")})
	sighash, err := types.Sighash(m, shieldedtest.ChainID, testTx, f.ac)
	require.NoError(t, err)
	m.Bundle.BindingSig, err = orchard.SignBinding(orchard.BindingSigningKey([]fr.Element{r1, r2, r3}), sighash, bytes.NewReader(make([]byte, 32)))
	require.NoError(t, err)

	// 1. At the binding level the forgery balances.
	ob, err := m.Bundle.ToOrchard()
	require.NoError(t, err)
	require.NoError(t, ob.CheckBalance(sighash, orchard.CanonicalBase), "bvk cannot see the -G base")

	// 2. The honest action has a proof; the forged one has no witness.
	pub0 := ob.PublicInputs(0, sighash)
	toml0 := shieldedtest.ActionToml(shieldedtest.ActionInputs{NK: shieldedtest.Alice.NK, SAsset: privacy.AssetID(types.FeeDenom),
		SRho: rho1, SRcm: rcm1, OAsset: anml, OValue: v, OPC: out1.PC(), Rcv: r1}, pub0)
	m.Bundle.Actions[0].Proof = f.prover.Prove(t, toml0, pub0)
	pub1 := ob.PublicInputs(1, sighash)
	toml1 := shieldedtest.ActionToml(shieldedtest.ActionInputs{NK: shieldedtest.Alice.NK, SAsset: privacy.AssetID(types.FeeDenom),
		SRho: rho2, SRcm: rcm2, OAsset: anml, OValue: v, OPC: out2.PC(), Rcv: r2}, pub1)
	if shieldedtest.Circuits() != "" {
		_, err := f.prover.TryProve(toml1, pub1)
		require.ErrorIs(t, err, shieldedtest.ErrWitnessRefused, "the circuit must refuse a cv under -G")
		require.ErrorContains(t, err, "bad value commitment")
	} else {
		t.Log("EARTH_CIRCUITS unset: not re-running the circuit on the forged witness (circuits/action tests it: test_negated_*_base_rejected)")
	}

	// 3. Any proof the attacker does have, in the forged slots: refused.
	m.Bundle.Actions[1].Proof = m.Bundle.Actions[0].Proof
	m.Bundle.Actions[2].Proof = m.Bundle.Actions[0].Proof
	require.NoError(t, m.ValidateBasic())
	_, err = f.runPrivate(m)
	require.ErrorIs(t, err, types.ErrInvalidProof)
	require.ErrorContains(t, err, "bundle 0 action 1")
	ts, err := f.k.Turnstile(f.ctx, types.AnmlDenom)
	require.NoError(t, err)
	require.True(t, ts.Out.IsZero())
}

func mustOrchard(t *testing.T, m *types.MsgSend) []*orchard.Bundle {
	ob, err := m.Bundle.ToOrchard()
	require.NoError(t, err)
	return []*orchard.Bundle{ob}
}

func mustSighash(t *testing.T, f *fixture, m *types.MsgSend) fr.Element {
	sh, err := types.Sighash(m, shieldedtest.ChainID, testTx, f.ac)
	require.NoError(t, err)
	return sh
}

func TestCheckPrivateMsgRefusals(t *testing.T) {
	f := initFixture(t)
	s := shieldedtest.Default()
	f.shieldScenario(s)
	f.nextBlock(5 * time.Second)

	// Unknown anchor, on a real spend and on a dummy alike.
	for _, j := range []int{0, 1} {
		m := f.scenarioMsg(s, shieldedtest.Send2)
		m.Bundle.Actions[j].Anchor = privacy.FieldBytes(shieldedtest.Det("root", 9))
		_, err := f.k.CheckPrivateMsg(f.ctx, m)
		require.ErrorIs(t, err, types.ErrUnknownRoot, "action %d", j)
	}

	// Unregistered denom.
	m := f.scenarioMsg(s, shieldedtest.Send2)
	m.Bundle.Balances = append(m.Bundle.Balances, types.ValueBalance{Denom: "unope", Amount: 1})
	m.Receiver = f.bech(f.addr("r"))
	_, err := f.k.CheckPrivateMsg(f.ctx, m)
	require.ErrorIs(t, err, types.ErrAssetNotRegistered)

	// Unshielding ANML to an account: the bank would refuse it after the ante
	// spent the inputs, so the check refuses it first.
	m = f.scenarioMsg(s, shieldedtest.Send2)
	m.Bundle.Balances = append(m.Bundle.Balances, types.ValueBalance{Denom: types.AnmlDenom, Amount: 1})
	m.Receiver = f.bech(f.addr("r"))
	_, err = f.k.CheckPrivateMsg(f.ctx, m)
	require.ErrorIs(t, err, types.ErrSendRestricted)

	// The pool itself as receiver: it would count Out with no coins moving.
	m = f.scenarioMsg(s, shieldedtest.Send2)
	m.Fee -= 1
	m.Receiver = f.bech(f.k.PoolAddress())
	_, err = f.k.CheckPrivateMsg(f.ctx, m)
	require.ErrorIs(t, err, types.ErrSendRestricted)

	// Blocked receiver.
	blocked := f.addr("blocked")
	f.bank.blocked[string(blocked)] = true
	m = f.scenarioMsg(s, shieldedtest.Send2)
	m.Fee -= 1
	m.Receiver = f.bech(blocked)
	_, err = f.k.CheckPrivateMsg(f.ctx, m)
	require.ErrorIs(t, err, types.ErrSendRestricted)

	// max_actions_per_bundle below the bundle's size.
	params, err := f.k.Params.Get(f.ctx)
	require.NoError(t, err)
	params.MaxActionsPerBundle = 2
	params.MaxPrivateActionsPerBlock = 4
	require.NoError(t, params.Validate())
	require.NoError(t, f.k.Params.Set(f.ctx, params))
	_, err = f.k.CheckPrivateMsg(f.ctx, f.scenarioMsg(s, shieldedtest.Multi3))
	require.ErrorIs(t, err, types.ErrInvalidBundle)
	require.ErrorContains(t, err, "max_actions_per_bundle")

	// No verifying key: refused, not accepted.
	params.VerifyingKeys = nil
	require.NoError(t, f.k.Params.Set(f.ctx, params))
	prepared, err := f.k.CheckPrivateMsg(f.ctx, f.scenarioMsg(s, shieldedtest.Send2))
	require.NoError(t, err)
	require.ErrorIs(t, f.k.VerifyPrivateMsg(f.ctx, prepared), types.ErrMissingVerifyingKey)
}

// A private msg's handler refuses unless the ante authorized this very msg in
// this tx: the defence against a contract or ICA host dispatching it.
func TestHandlerRequiresAnteAuthorization(t *testing.T) {
	f := initFixture(t)
	s := shieldedtest.Default()
	f.runScenario(s, shieldedtest.Unshield2)
	m := f.scenarioMsg(s, shieldedtest.Unshield2)

	_, err := f.msgs.Send(f.ctx, m)
	require.ErrorIs(t, err, types.ErrUnauthorized)
	require.False(t, keeper.AuthorizedNullifiers(f.ctx, m.Bundle.Nullifiers()...))

	// Authorized for a different msg: still refused, even one differing only
	// in its receiver.
	other := f.scenarioMsg(s, shieldedtest.Send2)
	actx, err := keeper.AuthorizeMsg(f.ctx, other, nil, 0)
	require.NoError(t, err)
	_, err = f.msgs.Send(actx, m)
	require.ErrorIs(t, err, types.ErrUnauthorized)
	twin := *m
	twin.Receiver = f.bech(f.addr("twin"))
	actx, err = keeper.AuthorizeMsg(f.ctx, &twin, nil, 0)
	require.NoError(t, err)
	_, err = f.msgs.Send(actx, m)
	require.ErrorIs(t, err, types.ErrUnauthorized)

	// ReleaseToModule pays an authorized msg's remainder of a denom once,
	// whole; a denom it does not release is refused.
	actx, err = keeper.AuthorizeMsg(f.ctx, m, nil, 0)
	require.NoError(t, err)
	require.True(t, keeper.AuthorizedNullifiers(actx, m.Bundle.Nullifiers()...))
	coin, err := f.k.ReleaseToModule(actx, m, types.FeeDenom, personhood)
	require.NoError(t, err)
	require.Equal(t, "500000uerth", coin.String(), "the balance less the fee")
	_, err = f.k.ReleaseToModule(actx, m, types.FeeDenom, personhood)
	require.ErrorIs(t, err, types.ErrAlreadyReleased)
	_, err = f.k.ReleaseToModule(actx, m, types.AnmlDenom, personhood)
	require.ErrorIs(t, err, types.ErrReleaseMap)

	// Through the ante, the unshield is paid there and nothing is left.
	cctx, _ := f.ctx.CacheContext()
	require.NoError(t, f.verify(cctx, m))
	actx, err = f.k.ExecutePrivateMsg(cctx, m)
	require.NoError(t, err)
	_, err = f.k.ReleaseToModule(actx, m, types.FeeDenom, personhood)
	require.ErrorIs(t, err, types.ErrAlreadyReleased)
	res, err := f.msgs.Send(actx, m)
	require.NoError(t, err)
	require.Equal(t, []uint64{16, 17}, res.Positions)
	require.NoError(t, f.k.AssertInvariants(actx))
}

// A fee from output (a Phase 2 claim or swap paying out of what it produces)
// must be paid in full, exactly, before the ante returns.
func TestFeeFromOutputMustBePaid(t *testing.T) {
	f := initFixture(t)
	s := shieldedtest.Default()
	m := f.scenarioMsg(s, shieldedtest.Send2)
	actx, err := keeper.AuthorizeMsg(f.ctx, m, nil, 7_000)
	require.NoError(t, err)
	require.ErrorIs(t, f.k.ExecutePrivateAction(actx, m, nil), sdkerrors.ErrInsufficientFee)
	f.bank.mint(mod(personhood), sdk.NewInt64Coin(types.FeeDenom, 10_000))
	require.ErrorIs(t, f.k.PayFeeFromModule(actx, personhood, math.NewInt(6_999)), types.ErrUnauthorized)
	require.NoError(t, f.k.PayFeeFromModule(actx, personhood, math.NewInt(7_000)))
	require.ErrorIs(t, f.k.PayFeeFromModule(actx, personhood, math.NewInt(1)), types.ErrUnauthorized, "paid once")
	require.NoError(t, f.k.ExecutePrivateAction(actx, m, nil))
	require.Equal(t, int64(7_000), f.bank.GetBalance(actx, authtypes.NewModuleAddress(authtypes.FeeCollectorName), types.FeeDenom).Amount.Int64())
	// Outside the ante: no authorization, no payment.
	require.ErrorIs(t, f.k.PayFeeFromModule(f.ctx, personhood, math.NewInt(1)), types.ErrUnauthorized)
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
	_, err = f.msgs.Shield(f.ctx, &types.MsgShield{Sender: f.bech(user), Amount: sdk.NewInt64Coin(types.AnmlDenom, 10), Pc: pc,
		Ciphertext: shieldedtest.BlindCT("anml")})
	require.NoError(t, err)

	// MintNote from a module holding the coins.
	f.bank.mint(mod(personhood), sdk.NewInt64Coin(types.AnmlDenom, 1_000_000))
	pos, cm, err := f.k.MintNote(f.ctx, personhood, sdk.NewInt64Coin(types.AnmlDenom, 1_000_000), pc, shieldedtest.BlindCT("mint"))
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
	_, _, err = f.k.MintNote(f.ctx, personhood, sdk.NewInt64Coin("unope", 1), pc, shieldedtest.BlindCT("mint"))
	require.ErrorIs(t, err, types.ErrAssetNotRegistered)
	_, _, err = f.k.MintNote(f.ctx, personhood, sdk.NewInt64Coin(types.AnmlDenom, 1), bytes.Repeat([]byte{0xff}, 32), shieldedtest.BlindCT("mint"))
	require.ErrorIs(t, err, types.ErrInvalidNote)
	huge := sdk.NewCoin(types.AnmlDenom, math.NewIntFromUint64(^uint64(0)).AddRaw(1))
	_, _, err = f.k.MintNote(f.ctx, personhood, huge, pc, shieldedtest.BlindCT("mint"))
	require.ErrorIs(t, err, types.ErrInvalidNote)
	_, _, err = f.k.MintNote(f.ctx, personhood, sdk.NewInt64Coin(types.AnmlDenom, 5), pc, shieldedtest.BlindCT("mint"))
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

// The block cap counts actions, not txs.
func TestBlockCapCountsActions(t *testing.T) {
	f := initFixture(t)
	params, err := f.k.Params.Get(f.ctx)
	require.NoError(t, err)
	params.MaxPrivateActionsPerBlock = 5
	require.NoError(t, f.k.Params.Set(f.ctx, params))
	require.NoError(t, f.k.CountPrivateActions(f.ctx, 2))
	require.NoError(t, f.k.CountPrivateActions(f.ctx, 3))
	require.ErrorIs(t, f.k.CountPrivateActions(f.ctx, 1), types.ErrBlockCap)
	f.nextBlock(5 * time.Second)
	require.ErrorIs(t, f.k.CountPrivateActions(f.ctx, 6), types.ErrBlockCap, "one msg over the cap never fits")
	require.NoError(t, f.k.CountPrivateActions(f.ctx, 5), "the count resets every block")
}

func TestGenesisRoundTrip(t *testing.T) {
	f := initFixture(t)
	s := shieldedtest.Default()
	f.runScenario(s, shieldedtest.DoubleSpend)

	gs, err := f.k.ExportGenesis(f.ctx)
	require.NoError(t, err)
	require.NoError(t, gs.Validate())
	require.Len(t, gs.Commitments, 28)
	require.Len(t, gs.Nullifiers, 17)
	require.Len(t, gs.Roots, 6) // empty, after shields, after each of 4 sends

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

	// The spent set came across: the consolidation cannot be replayed.
	_, err = g.k.CheckPrivateMsg(g.ctx, g.scenarioMsg(s, shieldedtest.Consolidate))
	require.ErrorIs(t, err, types.ErrNullifierSpent)

	// A genesis whose turnstiles disagree with the bank is refused.
	bad := *gs
	bad.Turnstiles = nil
	g3 := initFixtureEmpty(t, f.bank)
	g3.ctx = g3.ctx.WithBlockTime(f.ctx.BlockTime()).WithBlockHeight(f.ctx.BlockHeight())
	require.ErrorIs(t, g3.k.InitGenesis(g3.ctx, bad), types.ErrInvariant)
}

func TestShieldedOnlyPrefix(t *testing.T) {
	f := initFixture(t)
	const staking = "shieldedstaking"
	f.k.RegisterShieldedOnlyPrefix("derth/", staking)
	require.Panics(t, func() { f.k.RegisterShieldedOnlyPrefix("derth/x", staking) }, "nested prefixes")
	require.True(t, f.k.IsShieldedOnly("derth/earthvaloper1abc"))
	require.False(t, f.k.IsShieldedOnly("uerth"))

	user := f.addr("user")
	d := sdk.NewInt64Coin("derth/earthvaloper1abc", 100)
	f.bank.mint(mod(staking), d)
	// Never to an ordinary account, nor to modules outside the family's list
	// (personhood may hold ANML, not derth).
	require.ErrorIs(t, f.bank.SendCoinsFromModuleToAccount(f.ctx, staking, user, sdk.NewCoins(d)), types.ErrSendRestricted)
	require.ErrorIs(t, f.bank.SendCoinsFromModuleToModule(f.ctx, staking, personhood, sdk.NewCoins(d)), types.ErrSendRestricted)
	// Into the pool (through the keeper) and back to the registering module.
	_, err := f.k.RegisterAsset(f.ctx, d.Denom)
	require.NoError(t, err)
	_, _, err = f.k.MintNote(f.ctx, staking, d, privacy.FieldBytes(shieldedtest.Det("pc", 9)), shieldedtest.BlindCT("mint"))
	require.NoError(t, err)
	require.NoError(t, f.k.AssertInvariants(f.ctx))
}

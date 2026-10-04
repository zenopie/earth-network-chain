package app

import (
	"encoding/json"
	"fmt"
	"strconv"
	"testing"
	"time"

	"cosmossdk.io/collections"
	"cosmossdk.io/log"
	"cosmossdk.io/math"
	abci "github.com/cometbft/cometbft/abci/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	dbm "github.com/cosmos/cosmos-db"
	"github.com/cosmos/cosmos-sdk/baseapp"
	"github.com/cosmos/cosmos-sdk/client/flags"
	simtestutil "github.com/cosmos/cosmos-sdk/testutil/sims"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	v1 "github.com/cosmos/cosmos-sdk/x/gov/types/v1"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	"github.com/stretchr/testify/require"

	allocationkeeper "github.com/earth-network/earth/x/allocation/keeper"
	allocationtypes "github.com/earth-network/earth/x/allocation/types"
	shieldedtest "github.com/earth-network/earth/x/shielded/testutil"
	sskeeper "github.com/earth-network/earth/x/shieldedstaking/keeper"
	sstypes "github.com/earth-network/earth/x/shieldedstaking/types"
	"github.com/earth-network/earth/zk/privacy"
)

// ---- private redelegation helpers --------------------------------------------

// redelegateMsg moves amount of the stake note in (derth/<src>) to dst: the
// change back as a derth/<src> note, the derth/<dst> minted to a fresh stake
// note of the wallet.
func (e *stakeEnv) redelegateMsg(src, dst sdk.ValAddress, in *snote, amount uint64) (*sstypes.MsgRedelegate, *pendingBundle, *stakePlan, *snote) {
	p := e.feeOnly()
	dn := e.freshStake(sstypes.DerthDenom(e.valoper(dst)), 0)
	sp := e.stake(&stakePlan{denom: in.denom, ins: []*snote{in}, outs: []*snote{e.freshStake(in.denom, in.amount-amount)},
		vOut: amount, mint: dn})
	m := &sstypes.MsgRedelegate{Bundle: p.b, SrcValidator: e.valoper(src), DstValidator: e.valoper(dst), Amount: amount, Stake: sp.proof}
	e.prove(m, p)
	e.proveStake(m, sp)
	return m, p, sp, dn
}

// redelegate runs redelegateMsg and returns the derth/<dst> note and the
// tx result.
func (e *stakeEnv) redelegate(src, dst sdk.ValAddress, in *snote, amount uint64) (*snote, *abci.ExecTxResult) {
	e.t.Helper()
	m, p, sp, dn := e.redelegateMsg(src, dst, in, amount)
	res := e.run(e.privateTx(m))
	require.Equal(e.t, uint32(0), res.Code, res.Log)
	e.settle(p)
	e.settleStake(sp)
	return e.mintedStake(res, dn), res
}

// fakeRedelegate runs a redelegation's action as the private ante would,
// without a proof or notes: for driving x/staking's limits and slashes.
// Test-only.
func (e *stakeEnv) fakeRedelegate(src, dst sdk.ValAddress, amount uint64, label string) (*sstypes.MsgRedelegateResponse, error) {
	m := &sstypes.MsgRedelegate{SrcValidator: e.valoper(src), DstValidator: e.valoper(dst), Amount: amount,
		Stake: sstypes.StakeProof{SpcMint: privacy.FieldBytes(ssDet("redelegate-spc/"+label, 0)), SpcCiphertext: shieldedtest.BlindCT("redelegate/" + label)}}
	res, err := sskeeper.NewActionHandler(e.app.ShieldedStakingKeeper).ExecutePrivateAction(e.ctx(), m, nil)
	if err != nil {
		return nil, err
	}
	return res.(*sstypes.MsgRedelegateResponse), nil
}

// redelegateStub is a well-formed, unproven MsgRedelegate with a real fee
// bundle layout and a stake nullifier never spent: for a msg the chain must
// refuse before verifying anything.
func (e *stakeEnv) redelegateStub(src, dst sdk.ValAddress, amount uint64, label string) *sstypes.MsgRedelegate {
	p := e.feeOnly()
	z := make([]byte, 32)
	m := &sstypes.MsgRedelegate{Bundle: p.b, SrcValidator: e.valoper(src), DstValidator: e.valoper(dst), Amount: amount,
		Stake: sstypes.StakeProof{Anchor: z, SpcMint: privacy.FieldBytes(ssDet("stub-spc/"+label, 0)), OwnerTag: z,
			Nullifiers: [][]byte{privacy.FieldBytes(ssDet("stub-snf/"+label, 0)), z}, Commitments: [][]byte{z, z},
			Ciphertexts: [][]byte{nil, nil}, SpcCiphertext: shieldedtest.BlindCT("stub/" + label)}}
	unproven(m)
	return m
}

// requireRedelegationRefused: CheckTx refuses m with ErrRedelegation
// (containing want), and its fee note is not spent.
func (e *stakeEnv) requireRedelegationRefused(m *sstypes.MsgRedelegate, want string) {
	e.t.Helper()
	res := e.checkTx(e.privateTx(m))
	require.Equal(e.t, sstypes.ErrRedelegation.ABCICode(), res.Code, res.Log)
	require.Contains(e.t, res.Log, want)
	spent, err := e.app.ShieldedKeeper.Nullifiers.Has(e.ctx(), m.Bundle.Actions[0].Nullifier)
	require.NoError(e.t, err)
	require.False(e.t, spent, "nothing spent")
}

func (e *stakeEnv) redelegationQuery(src, dst sdk.ValAddress) *sstypes.QueryRedelegationResponse {
	e.t.Helper()
	q, err := sskeeper.NewQueryServerImpl(e.app.ShieldedStakingKeeper).Redelegation(e.ctx(),
		&sstypes.QueryRedelegationRequest{SrcValidator: e.valoper(src), DstValidator: e.valoper(dst)})
	require.NoError(e.t, err)
	return q
}

func evInt(t *testing.T, ev map[string]string, key string) math.Int {
	t.Helper()
	v, ok := math.NewIntFromString(ev[key])
	require.True(t, ok, "%s=%q", key, ev[key])
	return v
}

// ---- tests --------------------------------------------------------------------

// A private staker moves derth from A to B: the stake leaves A's delegation
// and joins B's in the same block (x/staking's redelegation, no unbonding),
// the derth/B note is worth what the derth/A was, the change stays at A,
// both books keep their rates, and the stake earns at B at once. A value
// that fits in A's delegation queue moves as a book entry, with no x/staking
// entry. x/staking's transitive rule then holds B: a redelegation out of
// B's bonded stake is refused before anything is spent.
func TestRedelegateMovesStakeWithoutGap(t *testing.T) {
	e := initStakeEnv(t)
	vA, _ := e.createValidator(1000 * ssErth)
	vB, _ := e.createValidator(1000 * ssErth)
	e.next(5 * time.Second)
	mod := e.app.ShieldedStakingKeeper.ModuleAddress()
	e.shield(uint64(5_000 * ssErth))
	e.shield(uint64(100 * ssErth))
	e.shield(uint64(100 * ssErth))
	dA, dB := sstypes.DerthDenom(e.valoper(vA)), sstypes.DerthDenom(e.valoper(vB))

	n := e.delegate(vA, uint64(3_000*ssErth))
	e.days(2) // delegated, then an epoch of rewards compounded
	e.next(time.Hour)
	rateA := e.rate(vA)
	require.True(t, rateA.GT(math.LegacyOneDec()), "rate %s", rateA)
	supplyA := e.state(vA).DerthSupply
	delA := e.modDelegation(vA)
	require.True(t, e.modDelegation(vB).IsZero())

	// --- 1,000 derth/A to B, out of A's bonded stake (its queue is empty).
	b, res := e.redelegate(vA, vB, n, uint64(1_000*ssErth))
	evs := eventsOf(res.Events, sstypes.EventTypeRedelegate)
	require.Len(t, evs, 1)
	ev := evs[0]
	require.Equal(t, e.valoper(vA), ev["src_validator"])
	require.Equal(t, e.valoper(vB), ev["dst_validator"])
	require.Equal(t, fmt.Sprint(1_000*ssErth), ev["derth"])
	value, minted, bonded, queued := evInt(t, ev, "value"), evInt(t, ev, "minted"), evInt(t, ev, "bonded"), evInt(t, ev, "queued")
	// A's queue held only the rewards since the epoch end (withdrawn into it
	// first): they move as a book entry, the rest moves bonded.
	require.Equal(t, value, bonded.Add(queued))
	require.True(t, bonded.GT(queued), "bonded %s queued %s", bonded, queued)
	// Worth what it was at A's live rate (one 5 s block of rewards after
	// rateA was read).
	atRead := rateA.MulInt64(1_000 * ssErth).TruncateInt()
	require.True(t, value.GTE(atRead), "value %s, at read %s", value, atRead)
	require.InEpsilon(t, atRead.Int64(), value.Int64(), 1e-3)
	// B had no derth: rate 1, so the derth/B minted is what arrived, at most
	// x/staking's truncation below the value.
	require.Equal(t, minted.Uint64(), b.amount)
	require.True(t, value.Sub(minted).GTE(math.ZeroInt()) && value.Sub(minted).LTE(math.NewInt(2)), "value %s minted %s", value, minted)
	require.Equal(t, dB, b.denom)
	// A's book: exactly 1,000 derth less; the change stays at A.
	require.Equal(t, supplyA.SubRaw(1_000*ssErth), e.state(vA).DerthSupply)
	require.Equal(t, minted, e.state(vB).DerthSupply)
	require.Equal(t, uint64(2_000*ssErth), e.stakeBalance(dA))
	require.Equal(t, b.amount, e.stakeBalance(dB))
	// x/staking: the module's stake moved in this block: one entry A -> B.
	red, err := e.app.StakingKeeper.GetRedelegation(e.ctx(), mod, vA, vB)
	require.NoError(t, err)
	require.Len(t, red.Entries, 1)
	require.Equal(t, bonded, red.Entries[0].InitialBalance)
	completion, err := strconv.ParseInt(ev["completion_time"], 10, 64)
	require.NoError(t, err)
	require.Equal(t, red.Entries[0].CompletionTime.UnixNano(), completion)
	require.InDelta(t, delA.Sub(bonded).Int64(), e.modDelegation(vA).Int64(), 2)
	require.InDelta(t, bonded.Int64(), e.modDelegation(vB).Int64(), 2)
	require.True(t, e.state(vB).PendingDelegation.GTE(queued), "the queued part waits in B's queue")
	e.invariants()

	// Query/Redelegation: one A -> B entry of 32; B is now a destination
	// with a maturing entry (locked as a source until it completes).
	q := e.redelegationQuery(vA, vB)
	require.Equal(t, uint32(1), q.Entries)
	require.Equal(t, uint32(32), q.MaxEntries)
	require.Equal(t, completion, q.PairFreesAt)
	require.Zero(t, q.SrcLockedUntil)
	require.Equal(t, completion, e.redelegationQuery(vB, vA).SrcLockedUntil)

	// --- it earns at B at once: B's live rate rises within the hour, no
	// epoch end needed; the epoch end compounds it.
	r0 := e.rate(vB)
	e.next(time.Hour)
	require.True(t, e.rate(vB).GT(r0), "rate %s -> %s", r0, e.rate(vB))
	e.days(1)
	require.True(t, e.state(vB).EpochRate.GT(math.LegacyOneDec()))
	e.invariants()

	// --- from A's queue: a delegation this epoch waits in A's queue; a
	// redelegation that fits in it moves as a book entry, no x/staking
	// entry, and joins B's queue for B's epoch end.
	q2 := e.delegate(vA, uint64(500*ssErth))
	pB := e.state(vB).PendingDelegation
	_, res = e.redelegate(vA, vB, q2, q2.amount*3/4)
	ev = eventsOf(res.Events, sstypes.EventTypeRedelegate)[0]
	value2 := evInt(t, ev, "value")
	require.Equal(t, value2, evInt(t, ev, "queued"))
	require.Equal(t, "0", ev["bonded"])
	require.Equal(t, "", ev["completion_time"])
	red, err = e.app.StakingKeeper.GetRedelegation(e.ctx(), mod, vA, vB)
	require.NoError(t, err)
	require.Len(t, red.Entries, 1, "no new x/staking entry")
	require.True(t, e.state(vB).PendingDelegation.Sub(pB).GTE(value2), "B's queue took the value")
	e.invariants()
	e.days(1)
	e.invariants()

	// --- B received a maturing redelegation: x/staking refuses any
	// redelegation out of the module's bonded stake at B until it
	// completes. Refused in CheckTx, before anything is spent.
	e.requireRedelegationRefused(e.redelegateStub(vB, vA, uint64(100*ssErth), "transitive"), "transitive")
	e.invariants()
}

// x/staking's max_entries bounds the module's maturing redelegations per
// (source, destination) pair, shared by every private staker. The next is
// refused (before anything is spent); another pair, or a value that fits in
// the source's queue, still moves; once the entries complete, the pair is
// free again.
func TestRedelegateMaxEntries(t *testing.T) {
	// unbonding_time 2 days and max_entries 3 (the module's floor:
	// ceil(2 days / 1 day) + 1 = 3 entries).
	e, err := initStakeEnvWith(t, func(appState map[string]json.RawMessage, _ sdk.AccAddress) {
		var st map[string]any
		require.NoError(t, json.Unmarshal(appState["staking"], &st))
		params := st["params"].(map[string]any)
		params["unbonding_time"] = "172800s"
		params["max_entries"] = 3
		bz, err := json.Marshal(st)
		require.NoError(t, err)
		appState["staking"] = bz
	})
	require.NoError(t, err)
	e.auditFundPool(20_000 * ssErth)
	vA, _ := e.createValidator(1000 * ssErth)
	vB, _ := e.createValidator(1000 * ssErth)
	vC, _ := e.createValidator(1000 * ssErth)
	e.next(5 * time.Second)
	e.auditDelegate(vA, uint64(5_000*ssErth), "a")
	e.auditDelegate(vB, uint64(1_000*ssErth), "b")
	e.auditDelegate(vC, uint64(1_000*ssErth), "c")
	e.days(1)
	e.next(time.Hour)
	e.shield(uint64(100 * ssErth)) // fees for the refused msgs

	for i := range 3 {
		r, err := e.fakeRedelegate(vA, vB, uint64(600*ssErth), fmt.Sprint("ab", i))
		require.NoError(t, err)
		require.Positive(t, r.CompletionTime)
		e.next(5 * time.Second)
	}
	q := e.redelegationQuery(vA, vB)
	require.Equal(t, uint32(3), q.Entries)
	require.Equal(t, uint32(3), q.MaxEntries)
	require.Positive(t, q.PairFreesAt)
	e.invariants()

	// The fourth A -> B is refused: by the action, and in CheckTx before
	// anything is spent.
	_, err = e.fakeRedelegate(vA, vB, uint64(600*ssErth), "ab3")
	require.ErrorIs(t, err, sstypes.ErrRedelegation)
	require.ErrorContains(t, err, "max_entries")
	e.requireRedelegationRefused(e.redelegateStub(vA, vB, uint64(600*ssErth), "full"), "max_entries")

	// Another pair is not full.
	_, err = e.fakeRedelegate(vA, vC, uint64(600*ssErth), "ac")
	require.NoError(t, err)
	// A value that fits in A's queue needs no x/staking entry.
	e.auditDelegate(vA, uint64(500*ssErth), "queue")
	r, err := e.fakeRedelegate(vA, vB, uint64(200*ssErth), "queued")
	require.NoError(t, err)
	require.Zero(t, r.CompletionTime)
	require.Equal(t, uint32(3), e.redelegationQuery(vA, vB).Entries)
	e.next(5 * time.Second)
	e.invariants()

	// Two days on, the entries have completed: the pair is free.
	e.days(3)
	require.Zero(t, e.redelegationQuery(vA, vB).Entries)
	_, err = e.fakeRedelegate(vA, vB, uint64(600*ssErth), "again")
	require.NoError(t, err)
	e.next(5 * time.Second)
	e.invariants()
}

// No transitive redelegation: while stake the module redelegated INTO A
// matures, x/staking refuses a redelegation out of the module's bonded
// stake at A (the module is one delegator: one person's redelegation into A
// holds every private staker of A). Undelegating is not affected, and a
// value that fits in A's queue still moves. Once the incoming entry
// completes, A is free.
func TestRedelegateTransitiveRefused(t *testing.T) {
	e := initStakeEnv(t)
	e.auditFundPool(20_000 * ssErth)
	vA, _ := e.createValidator(1000 * ssErth)
	vB, _ := e.createValidator(1000 * ssErth)
	vC, _ := e.createValidator(1000 * ssErth)
	e.next(5 * time.Second)
	e.auditDelegate(vA, uint64(2_000*ssErth), "a")
	e.auditDelegate(vC, uint64(2_000*ssErth), "c")
	e.days(1)
	e.next(time.Hour)
	e.shield(uint64(100 * ssErth))

	in, err := e.fakeRedelegate(vC, vA, uint64(1_000*ssErth), "ca")
	require.NoError(t, err)
	require.Positive(t, in.CompletionTime)
	e.next(5 * time.Second)
	require.Equal(t, in.CompletionTime, e.redelegationQuery(vA, vB).SrcLockedUntil)

	_, err = e.fakeRedelegate(vA, vB, uint64(1_000*ssErth), "ab")
	require.ErrorIs(t, err, sstypes.ErrRedelegation)
	require.ErrorContains(t, err, "transitive")
	e.requireRedelegationRefused(e.redelegateStub(vA, vB, uint64(1_000*ssErth), "transitive"), "transitive")

	// Undelegating from A goes on.
	e.fakeUndelegate(vA, uint64(100*ssErth), "out")
	// A value within A's queue moves without x/staking.
	e.auditDelegate(vA, uint64(300*ssErth), "queue")
	r, err := e.fakeRedelegate(vA, vB, uint64(200*ssErth), "queued")
	require.NoError(t, err)
	require.Zero(t, r.CompletionTime)
	e.next(5 * time.Second)
	e.invariants()

	// Past the incoming entry's completion, A redelegates again.
	e.days(22)
	require.Zero(t, e.redelegationQuery(vA, vB).SrcLockedUntil)
	r, err = e.fakeRedelegate(vA, vB, uint64(1_000*ssErth), "free")
	require.NoError(t, err)
	require.Positive(t, r.CompletionTime)
	e.next(5 * time.Second)
	e.invariants()
}

// A slash of A for an infraction before a redelegation A -> B reaches the
// redelegation's entry while it matures: x/staking takes slash_fraction x
// the entry's shares from the module's delegation at B, and B's book
// absorbs it pro rata (every derth/B loses through B's rate; B's epoch rate
// follows at the end of the block). B's undelegations already under way
// are not touched (x/staking would take the slash from them first: the
// module sets them aside for the slash), the rewards the slash's unbond
// would pay are booked, and every invariant holds.
func TestRedelegateSlashDuringMaturity(t *testing.T) {
	e := initStakeEnv(t)
	e.auditFundPool(20_000 * ssErth)
	vA, _ := e.createValidator(1000 * ssErth)
	vB, _ := e.createValidator(1000 * ssErth)
	e.next(5 * time.Second)
	mod := e.app.ShieldedStakingKeeper.ModuleAddress()
	e.auditDelegate(vA, uint64(4_000*ssErth), "a")
	e.auditDelegate(vB, uint64(2_000*ssErth), "b")
	e.days(1)

	// The infraction (x/evidence slashes entries created at or after the
	// distribution height, infraction - 1: leave two blocks).
	e.next(5 * time.Second)
	e.next(5 * time.Second)
	infraction := e.height
	ctx := e.ctx()
	valA, err := e.app.StakingKeeper.GetValidator(ctx, vA)
	require.NoError(t, err)
	power := valA.ConsensusPower(e.app.StakingKeeper.PowerReduction(ctx))
	consA, err := valA.GetConsAddr()
	require.NoError(t, err)

	// After the infraction: an undelegation from B reaches x/staking (its
	// entry begins after the infraction), then A -> B moves 1,000.
	e.fakeUndelegate(vB, uint64(20*ssErth), "b-out")
	e.days(1)
	ubd, err := e.app.StakingKeeper.GetUnbondingDelegation(e.ctx(), mod, vB)
	require.NoError(t, err)
	require.Len(t, ubd.Entries, 1)
	require.Greater(t, ubd.Entries[0].CreationHeight, infraction)
	ubdBalance := ubd.Entries[0].Balance
	_, err = e.fakeRedelegate(vA, vB, uint64(1_000*ssErth), "ab")
	require.NoError(t, err)
	e.next(5 * time.Second)
	red, err := e.app.StakingKeeper.GetRedelegation(e.ctx(), mod, vA, vB)
	require.NoError(t, err)
	require.Len(t, red.Entries, 1)
	require.Greater(t, red.Entries[0].CreationHeight, infraction)
	entry := red.Entries[0]
	e.invariants()

	delB := e.modDelegation(vB)
	rateB := e.rate(vB)
	epochB := e.state(vB).EpochRate
	supplyB := e.state(vB).DerthSupply
	valB, err := e.app.StakingKeeper.GetValidator(e.ctx(), vB)
	require.NoError(t, err)
	res := e.block(5*time.Second, []abci.Misbehavior{{
		Type: abci.MisbehaviorType_DUPLICATE_VOTE, Validator: abci.Validator{Address: consA, Power: power},
		Height: infraction, Time: e.times[infraction], TotalVotingPower: power + 100,
	}})
	require.Empty(t, eventsOf(res.Events, sstypes.EventTypeEpochFailure), "no deferred work")

	ctx = e.ctx()
	// The entry's slash: 5% of its shares at B, from the module's
	// delegation at B.
	slashed := math.LegacyNewDecWithPrec(5, 2).Mul(entry.SharesDst).MulInt(valB.Tokens).Quo(valB.DelegatorShares).TruncateInt()
	require.True(t, slashed.IsPositive())
	require.InDelta(t, delB.Sub(slashed).Int64(), e.modDelegation(vB).Int64(), 2)
	// B's undelegation in flight is untouched (and back in place).
	ubd, err = e.app.StakingKeeper.GetUnbondingDelegation(ctx, mod, vB)
	require.NoError(t, err)
	require.Len(t, ubd.Entries, 1)
	require.Equal(t, ubdBalance, ubd.Entries[0].Balance)
	has, err := e.app.ShieldedStakingKeeper.ShelteredUnbondings.Has(ctx, vB)
	require.NoError(t, err)
	require.False(t, has)
	// B's book absorbs it: same supply, a lower rate, its epoch rate
	// lowered at the end of the block (positions re-weigh at it).
	require.Equal(t, supplyB, e.state(vB).DerthSupply)
	require.True(t, e.rate(vB).LT(rateB), "rate %s -> %s", rateB, e.rate(vB))
	require.True(t, e.state(vB).EpochRate.LT(epochB), "epoch rate %s -> %s", epochB, e.state(vB).EpochRate)
	// A was slashed and tombstoned.
	valA, err = e.app.StakingKeeper.GetValidator(ctx, vA)
	require.NoError(t, err)
	require.True(t, valA.IsJailed())
	// The books still balance: the rewards the slash's unbond would have
	// paid outside them were booked first.
	e.invariants()

	// Through maturity: the entry completes, B's undelegation pays its full
	// value, everything balances.
	e.days(22)
	e.invariants()
	_, err = e.app.StakingKeeper.GetRedelegation(e.ctx(), mod, vA, vB)
	require.ErrorIs(t, err, stakingtypes.ErrNoRedelegation)
}

// Votes on a proposal whose snapshot predates the move: a derth/A note
// votes as A once, before or after it moves (its nullifier entered the tree
// after the snapshot's nullifier root), and its derth/B note never votes on
// that proposal (minted after the snapshot, it is not under the note root).
// No unit of stake votes twice; the tally's power stays within bonded
// stake.
func TestRedelegateVoteSnapshot(t *testing.T) {
	e := initStakeEnv(t)
	vA, _ := e.createValidator(1000 * ssErth)
	vB, _ := e.createValidator(1000 * ssErth)
	e.next(5 * time.Second)
	e.shield(uint64(5_000 * ssErth))
	e.shield(uint64(200 * ssErth))
	e.shield(uint64(200 * ssErth))
	n1 := e.delegate(vA, uint64(1_000*ssErth))
	n2 := e.delegate(vA, uint64(600*ssErth))
	e.delegate(vA, uint64(400*ssErth)) // stays at A, does not vote
	e.delegate(vB, uint64(500*ssErth))
	e.days(1)
	prop := e.submitProposal()

	// n1 votes as A, then moves to B whole.
	e.stakeVote(n1, prop, v1.OptionYes)
	b1, _ := e.redelegate(vA, vB, n1, n1.amount)
	// n1 cannot vote again; b1 cannot vote on this proposal.
	again, _, _ := e.stakeVoteMsg(n1, prop, v1.NewNonSplitVoteOption(v1.OptionNo), 0, false)
	res := e.checkTx(e.privateTx(again))
	require.Equal(t, sstypes.ErrVoteNullifierUsed.ABCICode(), res.Code, res.Log)
	sb1, _, vpb1 := e.stakeVoteMsg(b1, prop, v1.NewNonSplitVoteOption(v1.OptionNo), 0, false)
	_, err := e.tryProveVote(sb1, vpb1)
	requireRefused(t, err)

	// n2 moves first, then votes: as A, once; b2 cannot.
	b2, _ := e.redelegate(vA, vB, n2, n2.amount)
	e.stakeVote(n2, prop, v1.OptionNo)
	again, _, _ = e.stakeVoteMsg(n2, prop, v1.NewNonSplitVoteOption(v1.OptionYes), 0, false)
	res = e.checkTx(e.privateTx(again))
	require.Equal(t, sstypes.ErrVoteNullifierUsed.ABCICode(), res.Code, res.Log)
	sb2, _, vpb2 := e.stakeVoteMsg(b2, prop, v1.NewNonSplitVoteOption(v1.OptionYes), 0, false)
	_, err = e.tryProveVote(sb2, vpb2)
	requireRefused(t, err)

	// The votes are A's, each once; B has none.
	dec := func(x uint64) math.LegacyDec { return math.LegacyNewDecFromInt(math.NewIntFromUint64(x)) }
	tA, err := e.app.ShieldedStakingKeeper.Tallies.Get(e.ctx(), collections.Join(prop, e.valoper(vA)))
	require.NoError(t, err)
	require.Equal(t, dec(sstypes.RoundVoteWeight(n1.amount)), tA.Yes)
	require.Equal(t, dec(sstypes.RoundVoteWeight(n2.amount)), tA.No)
	_, err = e.app.ShieldedStakingKeeper.Tallies.Get(e.ctx(), collections.Join(prop, e.valoper(vB)))
	require.ErrorIs(t, err, collections.ErrNotFound)
	require.Equal(t, 2, countVotes(t, e, prop))

	// The tally counts no stake twice: its total power is within the bonded
	// stake (the moved stake follows B's own vote; A's private votes are
	// fractions of A's snapshot supply applied to what is left at A).
	ctx, _ := e.ctx().CacheContext()
	p, err := e.app.GovKeeper.Proposals.Get(ctx, prop)
	require.NoError(t, err)
	_, _, tr, err := e.app.GovKeeper.Tally(ctx, p)
	require.NoError(t, err)
	total := math.ZeroInt()
	for _, s := range []string{tr.YesCount, tr.NoCount, tr.AbstainCount, tr.NoWithVetoCount} {
		v, ok := math.NewIntFromString(s)
		require.True(t, ok)
		total = total.Add(v)
	}
	bondedTokens, err := e.app.StakingKeeper.TotalBondedTokens(ctx)
	require.NoError(t, err)
	require.True(t, total.LTE(bondedTokens), "tally %s, bonded %s", total, bondedTokens)
	noA, ok := math.NewIntFromString(tr.NoCount)
	require.True(t, ok)
	require.True(t, noA.LTE(e.modDelegation(vA)), "A's private votes count against what is left at A")
	e.invariants()
}

// Groundworks weight is per validator and lives in positions. A note
// redelegation leaves A's positions and rates as they are; a position's
// weight moves from A to B by unlocking it (a derth/A note), redelegating
// the note and locking it at B: A's voter loses the weight, B's voter gains
// it at B's epoch rate, and the option's allocation follows.
func TestRedelegateGroundworksWeight(t *testing.T) {
	e := initStakeEnv(t)
	vA, _ := e.createValidator(1000 * ssErth)
	vB, _ := e.createValidator(1000 * ssErth)
	e.next(5 * time.Second)
	e.shield(uint64(3_000 * ssErth))
	e.shield(uint64(100 * ssErth))
	e.shield(uint64(100 * ssErth))
	gw := allocationtypes.STREAM_ID_GROUNDWORKS
	ak := e.app.AllocationKeeper
	gov := authtypes.NewModuleAddress("gov")
	fund := sdk.NewCoins(sdk.NewInt64Coin("uerth", 10*ssErth))
	require.NoError(t, e.app.BankKeeper.SendCoins(e.ctx(), e.userAddr(), gov, fund))
	_, err := allocationkeeper.NewMsgServerImpl(ak).AddAddressOption(e.ctx(), &allocationtypes.MsgAddAddressOption{
		Submitter: e.bech(gov), Stream: gw, Description: "a public good", Recipient: e.bech(e.userAddr()),
	})
	require.NoError(t, err)
	e.next(5 * time.Second)
	opt := []allocationtypes.AllocationWeight{{OptionId: 1, Percent: 100}}
	voter := func(v sdk.ValAddress) (allocationtypes.Voter, error) {
		return ak.Voters.Get(e.ctx(), collections.Join(uint32(gw), sstypes.ValidatorVoterKey(v)))
	}
	allocated := func() math.Int {
		o, err := ak.Options.Get(e.ctx(), collections.Join(uint32(gw), uint64(1)))
		require.NoError(t, err)
		return o.AmountAllocated
	}

	dn := e.delegate(vA, uint64(1_000*ssErth))
	e.delegate(vB, uint64(500*ssErth))
	e.days(2)
	rateA := e.state(vA).EpochRate
	require.True(t, rateA.GT(math.LegacyOneDec()))

	id := e.lock(dn, uint64(500*ssErth), positionKey(1), opt)
	wA := rateA.MulInt64(500 * ssErth).TruncateInt()
	vtA, err := voter(vA)
	require.NoError(t, err)
	require.Equal(t, wA, vtA.Weight)
	_, err = voter(vB)
	require.ErrorIs(t, err, collections.ErrNotFound)
	require.Equal(t, wA, allocated())

	// A note redelegation (the lock's change) leaves A's positions and
	// epoch rate alone.
	change := e.unspentStake(dn.denom)
	require.NotNil(t, change)
	e.redelegate(vA, vB, change, uint64(200*ssErth))
	require.Equal(t, rateA, e.state(vA).EpochRate)
	vtA, err = voter(vA)
	require.NoError(t, err)
	require.Equal(t, wA, vtA.Weight)
	e.invariants()

	// Unlock the position: A's voter goes.
	pt := e.feeOnly()
	back := e.freshStake(dn.denom, 0)
	usp := e.ownerProof(positionKey(1), back)
	um := &sstypes.MsgUnlockPosition{Bundle: pt.b, PositionId: id, Stake: usp.proof}
	e.prove(um, pt)
	e.proveStake(um, usp)
	fb := e.run(e.privateTx(um))
	require.Equal(t, uint32(0), fb.Code, fb.Log)
	e.settle(pt)
	back = e.mintedStake(fb, back)
	_, err = voter(vA)
	require.ErrorIs(t, err, collections.ErrNotFound)
	require.True(t, allocated().IsZero())

	// Redelegate it to B and lock it there: B's voter carries it, at B's
	// epoch rate.
	b, _ := e.redelegate(vA, vB, back, back.amount)
	e.lock(b, b.amount, positionKey(2), opt)
	wB := e.state(vB).EpochRate.MulInt(math.NewIntFromUint64(b.amount)).TruncateInt()
	vtB, err := voter(vB)
	require.NoError(t, err)
	require.Equal(t, wB, vtB.Weight)
	require.Equal(t, []allocationtypes.OptionWeight{{OptionId: 1, Weight: wB}}, vtB.OptionWeights)
	_, err = voter(vA)
	require.ErrorIs(t, err, collections.ErrNotFound)
	require.Equal(t, wB, allocated())
	e.invariants()
}

// Redelegations in flight cross a genesis: x/staking exports the module's
// entries, this module's InitGenesis accepts them (and still refuses anyone
// else's), the books and invariants carry over, and the entry matures on
// the imported chain's terms.
func TestRedelegateGenesisRoundTrip(t *testing.T) {
	e := initStakeEnv(t)
	e.auditFundPool(20_000 * ssErth)
	vA, _ := e.createValidator(1000 * ssErth)
	vB, _ := e.createValidator(1000 * ssErth)
	e.next(5 * time.Second)
	mod := e.app.ShieldedStakingKeeper.ModuleAddress()
	e.auditDelegate(vA, uint64(2_000*ssErth), "a")
	e.days(1)
	e.next(time.Hour)
	r, err := e.fakeRedelegate(vA, vB, uint64(1_000*ssErth), "ab")
	require.NoError(t, err)
	require.Positive(t, r.CompletionTime)
	e.next(5 * time.Second)
	e.invariants()

	importExport := func(edit func(appState map[string]json.RawMessage)) (*App, sdk.Context, error) {
		exported, err := e.app.ExportAppStateAndValidators(false, nil, nil)
		require.NoError(t, err)
		var appState map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(exported.AppState, &appState))
		if edit != nil {
			edit(appState)
		}
		fresh := New(log.NewNopLogger(), dbm.NewMemDB(), nil, true, simtestutil.AppOptionsMap{flags.FlagHome: t.TempDir()},
			baseapp.SetChainID(ssChainID))
		fctx := fresh.NewUncachedContext(false, cmtproto.Header{ChainID: ssChainID, Height: e.height, Time: e.now})
		err = func() (err error) {
			defer func() {
				if rec := recover(); rec != nil {
					err = fmt.Errorf("%v", rec)
				}
			}()
			_, err = fresh.ModuleManager.InitGenesis(fctx, fresh.AppCodec(), appState)
			return err
		}()
		return fresh, fctx, err
	}
	fresh, fctx, err := importExport(nil)
	require.NoError(t, err)
	red, err := fresh.StakingKeeper.GetRedelegation(fctx, mod, vA, vB)
	require.NoError(t, err)
	require.Len(t, red.Entries, 1)
	require.Equal(t, r.CompletionTime, red.Entries[0].CompletionTime.UnixNano())
	gs1, err := e.app.ShieldedStakingKeeper.ExportGenesis(e.ctx())
	require.NoError(t, err)
	gs2, err := fresh.ShieldedStakingKeeper.ExportGenesis(fctx)
	require.NoError(t, err)
	require.Equal(t, gs1, gs2)
	require.NoError(t, fresh.ShieldedStakingKeeper.AssertInvariants(fctx))
	// Still transitive-locked on the imported chain: B received.
	q, err := sskeeper.NewQueryServerImpl(fresh.ShieldedStakingKeeper).Redelegation(fctx,
		&sstypes.QueryRedelegationRequest{SrcValidator: e.valoper(vB), DstValidator: e.valoper(vA)})
	require.NoError(t, err)
	require.Equal(t, r.CompletionTime, q.SrcLockedUntil)

	// Anyone else's redelegation is refused at genesis.
	_, _, err = importExport(func(appState map[string]json.RawMessage) {
		var st map[string]any
		require.NoError(t, json.Unmarshal(appState["staking"], &st))
		reds := st["redelegations"].([]any)
		other := reds[0].(map[string]any)
		other["delegator_address"] = e.bech(sdk.AccAddress(vA))
		bz, err := json.Marshal(st)
		require.NoError(t, err)
		appState["staking"] = bz
	})
	require.ErrorContains(t, err, "genesis redelegation")
	require.ErrorContains(t, err, "only private staking redelegates")

	// The original matures and the entry goes.
	e.days(22)
	_, err = e.app.StakingKeeper.GetRedelegation(e.ctx(), mod, vA, vB)
	require.ErrorIs(t, err, stakingtypes.ErrNoRedelegation)
	e.invariants()
}

// Invariant 9: a redelegation that is not the module's (x/staking state
// nothing on chain can make: the hooks refuse it) is reported, and so is an
// unbonding delegation left set aside after a slash.
func TestRedelegateInvariant(t *testing.T) {
	e := initStakeEnv(t)
	e.auditFundPool(10_000 * ssErth)
	vA, _ := e.createValidator(1000 * ssErth)
	vB, _ := e.createValidator(1000 * ssErth)
	e.next(5 * time.Second)
	e.auditDelegate(vA, uint64(1_000*ssErth), "a")
	e.days(1)
	_, err := e.fakeRedelegate(vA, vB, uint64(800*ssErth), "ab")
	require.NoError(t, err)
	e.next(5 * time.Second)
	e.invariants()

	ctx, _ := e.ctx().CacheContext()
	sk := e.app.StakingKeeper
	red, err := sk.GetRedelegation(ctx, e.app.ShieldedStakingKeeper.ModuleAddress(), vA, vB)
	require.NoError(t, err)
	red.DelegatorAddress = e.bech(sdk.AccAddress(vA))
	require.NoError(t, sk.SetRedelegation(ctx, red))
	require.ErrorContains(t, e.app.ShieldedStakingKeeper.AssertInvariants(ctx), "only private staking redelegates")

	ctx, _ = e.ctx().CacheContext()
	ubd := stakingtypes.NewUnbondingDelegation(e.app.ShieldedStakingKeeper.ModuleAddress(), vB, 1, e.now, math.OneInt(), 1,
		sk.ValidatorAddressCodec(), e.app.AuthKeeper.AddressCodec())
	require.NoError(t, e.app.ShieldedStakingKeeper.ShelteredUnbondings.Set(ctx, vB, ubd))
	require.ErrorContains(t, e.app.ShieldedStakingKeeper.AssertInvariants(ctx), "set aside")
}

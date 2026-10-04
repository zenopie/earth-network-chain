package app

import (
	"encoding/hex"
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
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
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

// quoteRedelegate is the derth/<dst> a wallet names for moving amount
// derth/<src>: its value at src's live rate, bought at dst's, less 1% for the
// rates' drift until the block (exactly the value while dst has no derth).
func (e *stakeEnv) quoteRedelegate(src, dst sdk.ValAddress, amount uint64) uint64 {
	bA, sA, err := e.app.ShieldedStakingKeeper.Backing(e.ctx(), e.valoper(src))
	require.NoError(e.t, err)
	u := math.NewIntFromUint64(amount).Mul(bA).Quo(sA)
	bB, sB, err := e.app.ShieldedStakingKeeper.Backing(e.ctx(), e.valoper(dst))
	require.NoError(e.t, err)
	if !sB.IsPositive() {
		return u.Uint64() - u.Uint64()/100
	}
	d := u.Mul(sB).Quo(bB).Uint64()
	return d - d/100
}

// redelegateMsg moves amount of the stake note in (derth/<src>) to dst: the
// change back at src (a zero note when nothing is left; a labelled note
// keeps its label), the credit merged into the wallet's unlabelled dst note
// (or a padding input when it has none), labelled with the move.
func (e *stakeEnv) redelegateMsg(src, dst sdk.ValAddress, in *snote, amount uint64) (*sstypes.MsgRedelegate, *pendingBundle, *stakePlan) {
	p := e.feeOnly()
	dstDenom := sstypes.DerthDenom(e.valoper(dst))
	sp := &stakePlan{denom: in.denom, ins: []*snote{in}, vOut: amount,
		credit: &creditLane{denom: dstDenom, in: e.unlabelledStake(dstDenom), vIn: e.quoteRedelegate(src, dst, amount),
			moveTime: uint64(e.now.Unix())}}
	if in.amount > amount {
		sp.out = e.freshStake(in.denom, in.amount-amount)
	}
	e.stake(sp)
	m := &sstypes.MsgRedelegate{Bundle: p.b, SrcValidator: e.valoper(src), DstValidator: e.valoper(dst), Amount: amount,
		DstDerth: sp.credit.vIn, MoveTime: sp.credit.moveTime, Stake: sp.proof}
	e.prove(m, p)
	e.proveStake(m, sp)
	return m, p, sp
}

// redelegate runs redelegateMsg and returns the wallet's derth/<dst> note
// (merged, labelled) and the tx result.
func (e *stakeEnv) redelegate(src, dst sdk.ValAddress, in *snote, amount uint64) (*snote, *abci.ExecTxResult) {
	e.t.Helper()
	m, p, sp := e.redelegateMsg(src, dst, in, amount)
	res := e.run(e.privateTx(m))
	require.Equal(e.t, uint32(0), res.Code, res.Log)
	e.settle(p)
	e.settleStake(sp)
	require.True(e.t, sp.credit.out.known, "the derth/<dst> note is in the stake tree")
	return sp.credit.out, res
}

// fakeRedelegate runs a redelegation's action as the private ante would,
// without a proof or notes (fakeStake): for driving moves, slashes and
// x/staking's entries. Test-only.
func (e *stakeEnv) fakeRedelegate(src, dst sdk.ValAddress, amount uint64, label string) (*sstypes.MsgRedelegateResponse, error) {
	m := &sstypes.MsgRedelegate{SrcValidator: e.valoper(src), DstValidator: e.valoper(dst), Amount: amount,
		DstDerth: e.quoteRedelegate(src, dst, amount), MoveTime: uint64(e.now.Unix()), Stake: fakeStake("redelegate/"+label, true)}
	res, err := sskeeper.NewActionHandler(e.app.ShieldedStakingKeeper).ExecutePrivateAction(e.ctx(), m, nil)
	if err != nil {
		return nil, err
	}
	return res.(*sstypes.MsgRedelegateResponse), nil
}

// fakeMoveKey is the move key fakeRedelegate(label) records.
func fakeMoveKey(label string) []byte {
	return privacy.FieldBytes(ssDet("fake-snf/redelegate/"+label, 1))
}

// move is the open move key, if any.
func (e *stakeEnv) move(key []byte) (sstypes.Move, bool) {
	mv, err := e.app.ShieldedStakingKeeper.Moves.Get(e.ctx(), key)
	if err != nil {
		return mv, false
	}
	return mv, true
}

// doubleSign has val misbehave at infraction (a past height): x/evidence
// slashes 5% of its power then, and every module redelegation entry from val
// created since. Returns the block's result.
func (e *stakeEnv) doubleSign(val sdk.ValAddress, infraction int64) *abci.ResponseFinalizeBlock {
	e.t.Helper()
	ctx := e.ctx()
	v, err := e.app.StakingKeeper.GetValidator(ctx, val)
	require.NoError(e.t, err)
	power := v.ConsensusPower(e.app.StakingKeeper.PowerReduction(ctx))
	cons, err := v.GetConsAddr()
	require.NoError(e.t, err)
	return e.block(5*time.Second, []abci.Misbehavior{{
		Type: abci.MisbehaviorType_DUPLICATE_VOTE, Validator: abci.Validator{Address: cons, Power: power},
		Height: infraction, Time: e.times[infraction], TotalVotingPower: power + 100,
	}})
}

func evInt(t *testing.T, ev map[string]string, key string) math.Int {
	t.Helper()
	v, ok := math.NewIntFromString(ev[key])
	require.True(t, ok, "%s=%q", key, ev[key])
	return v
}

// requireRateKept: a slash of redelegations into v moved no honest
// holder's value: v's live rate r before the slash's block did not fall (it
// rose by at most the block's rewards), though without the debt taken off
// its supply it would have.
func (e *stakeEnv) requireRateKept(v sdk.ValAddress, r math.LegacyDec, debt math.Int) {
	e.t.Helper()
	now := e.rate(v)
	require.True(e.t, now.GTE(r), "rate %s -> %s", r, now)
	require.True(e.t, now.LTE(r.Mul(math.LegacyNewDecWithPrec(1001, 3))), "rate %s -> %s", r, now)
	b, s, err := e.app.ShieldedStakingKeeper.Backing(e.ctx(), e.valoper(v))
	require.NoError(e.t, err)
	uncovered := math.LegacyNewDecFromInt(b).QuoInt(s.Add(debt))
	require.True(e.t, uncovered.LT(r), "without the debt the rate would be %s, below %s", uncovered, r)
}

// ---- tests --------------------------------------------------------------------

// A private staker moves derth from A to B: the stake leaves A's delegation
// and joins B's in the same block (unbonded at A, bonded at B, the
// redelegation entry recorded by the module), the derth/B credit is merged
// into the wallet's B note and labelled with the move, worth what the derth/A
// was, the change stays at A, both books keep their rates, and the stake
// earns at B at once. A value that fits in A's delegation queue moves as a
// book entry, with no x/staking entry. There is no transitive lock: stake at
// B moves out again at once, all but the labelled exposure.
func TestRedelegateMovesStakeWithoutGap(t *testing.T) {
	e := initStakeEnv(t)
	vA, _ := e.createValidator(1000 * ssErth)
	vB, _ := e.createValidator(1000 * ssErth)
	e.next(5 * time.Second)
	mod := e.app.ShieldedStakingKeeper.ModuleAddress()
	e.shield(uint64(5_000 * ssErth))
	e.shield(uint64(100 * ssErth))
	e.shield(uint64(100 * ssErth))
	e.shield(uint64(100 * ssErth))
	dA, dB := sstypes.DerthDenom(e.valoper(vA)), sstypes.DerthDenom(e.valoper(vB))

	n := e.delegate(vA, uint64(3_000*ssErth))
	old := e.delegate(vB, uint64(400*ssErth)) // the wallet's B note, unlabelled
	e.days(2)                                 // delegated, then an epoch of rewards compounded
	e.next(time.Hour)
	rateA, rateB := e.rate(vA), e.rate(vB)
	require.True(t, rateA.GT(math.LegacyOneDec()), "rate %s", rateA)
	supplyA, supplyB := e.state(vA).DerthSupply, e.state(vB).DerthSupply
	delA := e.modDelegation(vA)
	delB := e.modDelegation(vB)

	// --- 1,000 derth/A to B, out of A's bonded stake (its queue is empty).
	b, res := e.redelegate(vA, vB, n, uint64(1_000*ssErth))
	evs := eventsOf(res.Events, sstypes.EventTypeRedelegate)
	require.Len(t, evs, 1)
	ev := evs[0]
	require.Equal(t, e.valoper(vA), ev["src_validator"])
	require.Equal(t, e.valoper(vB), ev["dst_validator"])
	require.Equal(t, fmt.Sprint(1_000*ssErth), ev["derth"])
	value, credited, bonded, queued := evInt(t, ev, "value"), evInt(t, ev, "credited"), evInt(t, ev, "bonded"), evInt(t, ev, "queued")
	require.Equal(t, value, bonded.Add(queued))
	require.True(t, bonded.GT(queued), "bonded %s queued %s", bonded, queued)
	atRead := rateA.MulInt64(1_000 * ssErth).TruncateInt()
	require.True(t, value.GTE(atRead), "value %s, at read %s", value, atRead)
	require.InEpsilon(t, atRead.Int64(), value.Int64(), 1e-3)
	// The credit: what the wallet named, at most what arrived buys at B's
	// rate; merged into the old B note and labelled with the move.
	require.Equal(t, credited.Uint64(), b.exposed)
	require.Equal(t, old.amount+b.exposed, b.amount)
	require.True(t, old.spent, "the old B note was merged")
	require.Equal(t, ev["move_key"], hex.EncodeToString(privacy.FieldBytes(b.moveKey)))
	require.Equal(t, fmt.Sprint(b.moveTime), ev["move_time"])
	require.True(t, rateB.MulInt(credited).TruncateInt().LTE(value), "the credit is paid for")
	require.Equal(t, dB, b.denom)
	require.Equal(t, supplyA.SubRaw(1_000*ssErth), e.state(vA).DerthSupply)
	require.Equal(t, supplyB.Add(credited), e.state(vB).DerthSupply)
	require.Equal(t, uint64(2_000*ssErth), e.stakeBalance(dA))
	require.Equal(t, b.amount, e.stakeBalance(dB), "one note per validator")
	// x/staking: one entry A -> B, recorded by the module, owned by the move.
	red, err := e.app.StakingKeeper.GetRedelegation(e.ctx(), mod, vA, vB)
	require.NoError(t, err)
	require.Len(t, red.Entries, 1)
	require.Equal(t, bonded, red.Entries[0].InitialBalance)
	completion, err := strconv.ParseInt(ev["completion_time"], 10, 64)
	require.NoError(t, err)
	require.Equal(t, red.Entries[0].CompletionTime.UnixNano(), completion)
	mv, ok := e.move(privacy.FieldBytes(b.moveKey))
	require.True(t, ok)
	require.Equal(t, red.Entries[0].SharesDst, mv.Shares)
	require.Equal(t, credited, mv.Credited)
	require.Equal(t, credited, mv.Retained)
	q, err := sskeeper.NewQueryServerImpl(e.app.ShieldedStakingKeeper).Move(e.ctx(),
		&sstypes.QueryMoveRequest{Key: hex.EncodeToString(mv.Key)})
	require.NoError(t, err)
	require.True(t, q.Found)
	require.False(t, q.Slashed)
	require.InDelta(t, delA.Sub(bonded).Int64(), e.modDelegation(vA).Int64(), 2)
	require.InDelta(t, delB.Add(bonded).Int64(), e.modDelegation(vB).Int64(), 2)
	e.invariants()

	// --- it earns at B at once: B's live rate rises within the hour.
	r0 := e.rate(vB)
	e.next(time.Hour)
	require.True(t, e.rate(vB).GT(r0), "rate %s -> %s", r0, e.rate(vB))
	e.days(1)
	e.invariants()

	// --- pro rata to A's book (audit 7, A7-1): a delegation this epoch
	// waits in A's queue; a redelegation takes its value out of the queue
	// and the bonded stake in the book's proportion, never out of the queue
	// first, so its bonded part keeps an x/staking entry and a move that a
	// slash of A reaches. Its credit, labelled, cannot merge into the
	// labelled B note: a second B note.
	q2 := e.delegate(vA, uint64(500*ssErth))
	vsA := e.state(vA)
	pA, dA0 := vsA.PendingDelegation, e.modDelegation(vA).Sub(vsA.PendingUndelegation)
	pB := e.state(vB).PendingDelegation
	b2, res := e.redelegate(vA, vB, q2, uint64(150*ssErth))
	ev = eventsOf(res.Events, sstypes.EventTypeRedelegate)[0]
	value2, queued2, bonded2 := evInt(t, ev, "value"), evInt(t, ev, "queued"), evInt(t, ev, "bonded")
	require.Equal(t, value2, queued2.Add(bonded2))
	require.True(t, queued2.IsPositive() && bonded2.IsPositive(), "queued %s bonded %s", queued2, bonded2)
	// The queue's share of the book (the rewards collected into the queue
	// first move it by less than a uerth in a thousand).
	share := math.LegacyNewDecFromInt(pA).QuoInt(pA.Add(dA0))
	require.InEpsilon(t, share.MulInt(value2).MustFloat64(), float64(queued2.Int64()), 1e-3)
	require.NotEmpty(t, ev["completion_time"])
	mv2, ok := e.move(privacy.FieldBytes(b2.moveKey))
	require.True(t, ok, "the bonded part's move, which a slash of A reaches")
	require.False(t, b.spent, "a labelled note takes no credit")
	red, err = e.app.StakingKeeper.GetRedelegation(e.ctx(), mod, vA, vB)
	require.NoError(t, err)
	require.Len(t, red.Entries, 2, "an entry for the bonded part")
	require.Equal(t, bonded2, red.Entries[1].InitialBalance)
	require.Equal(t, red.Entries[1].SharesDst, mv2.Shares)
	require.True(t, e.state(vB).PendingDelegation.Sub(pB).GTE(queued2), "B's queue took the queued part")
	e.invariants()
	e.days(1)
	e.invariants()

	// --- no transitive lock: B received a maturing redelegation, yet the
	// unexposed part of the B note moves out of B's bonded stake at once.
	// The exposure stays in the note.
	back, res := e.redelegate(vB, vA, b, old.amount/2)
	ev = eventsOf(res.Events, sstypes.EventTypeRedelegate)[0]
	require.True(t, evInt(t, ev, "bonded").IsPositive(), "out of B's bonded stake")
	require.NotEmpty(t, ev["completion_time"])
	rest := e.noteLabelled(dB, b.moveKey)
	require.NotNil(t, rest)
	require.Equal(t, b.moveKey, rest.moveKey, "the label stays with the change")
	require.Equal(t, b.exposed, rest.exposed)
	require.Equal(t, b.amount-old.amount/2, rest.amount)
	require.True(t, back.labelled(), "the move B -> A labels its own credit")
	e.invariants()
}

// No lock-out: a griefer's redelegation INTO A (dust, maturing for 21 days)
// does not stop anyone else's redelegation out of A's bonded stake, and a
// pair takes far more than x/staking's max_entries (32) maturing entries.
// Moves in one block share one entry.
func TestRedelegateNoLockout(t *testing.T) {
	e := initStakeEnv(t)
	e.fundPoolDirect(300_000 * ssErth)
	vA, _ := e.createValidator(1000 * ssErth)
	vB, _ := e.createValidator(1000 * ssErth)
	vC, _ := e.createValidator(1000 * ssErth)
	e.next(5 * time.Second)
	mod := e.app.ShieldedStakingKeeper.ModuleAddress()
	e.fakeDelegate(vA, uint64(100_000*ssErth), "a")
	e.fakeDelegate(vC, uint64(2_000*ssErth), "c")
	e.days(1)
	e.next(time.Hour)

	// The griefer: C -> A, bonded (the rest of its value).
	g, err := e.fakeRedelegate(vC, vA, uint64(500*ssErth), "grief")
	require.NoError(t, err)
	require.Positive(t, g.CompletionTime)
	e.next(5 * time.Second)

	// Everyone else still moves A -> B out of A's bonded stake, 40 times in
	// 40 blocks: 40 entries, past max_entries. (The first takes A's queue of
	// rewards with it; each later one finds a block's worth.)
	for i := range 40 {
		amount := uint64(300 * ssErth)
		if i == 0 {
			amount = uint64(5_000 * ssErth)
		}
		r, err := e.fakeRedelegate(vA, vB, amount, fmt.Sprint("ab", i))
		require.NoError(t, err, "move %d", i)
		require.Positive(t, r.CompletionTime)
		e.next(5 * time.Second)
	}
	maxEntries, err := e.app.StakingKeeper.MaxEntries(e.ctx())
	require.NoError(t, err)
	red, err := e.app.StakingKeeper.GetRedelegation(e.ctx(), mod, vA, vB)
	require.NoError(t, err)
	require.Len(t, red.Entries, 40)
	require.Greater(t, len(red.Entries), int(maxEntries))
	e.invariants()

	// Two moves in one block share its entry (and are slashed together, pro
	// rata).
	_, err = e.fakeRedelegate(vA, vB, uint64(300*ssErth), "same1")
	require.NoError(t, err)
	_, err = e.fakeRedelegate(vA, vB, uint64(300*ssErth), "same2")
	require.NoError(t, err)
	// Both books are marked for the end of the block's Groundworks re-weigh
	// (audit 7), and cleared by it.
	for _, v := range []sdk.ValAddress{vA, vB} {
		has, err := e.app.ShieldedStakingKeeper.SlashedValidators.Has(e.ctx(), e.valoper(v))
		require.NoError(t, err)
		require.True(t, has)
	}
	e.next(5 * time.Second)
	for _, v := range []sdk.ValAddress{vA, vB} {
		has, err := e.app.ShieldedStakingKeeper.SlashedValidators.Has(e.ctx(), e.valoper(v))
		require.NoError(t, err)
		require.False(t, has)
	}
	red, err = e.app.StakingKeeper.GetRedelegation(e.ctx(), mod, vA, vB)
	require.NoError(t, err)
	require.Len(t, red.Entries, 41)
	m1, ok1 := e.move(fakeMoveKey("same1"))
	m2, ok2 := e.move(fakeMoveKey("same2"))
	require.True(t, ok1 && ok2)
	require.Equal(t, m1.EntryHeight, m2.EntryHeight)
	require.Equal(t, red.Entries[40].SharesDst, m1.Shares.Add(m2.Shares))
	e.invariants()

	// Through maturity: the entries complete, the moves are forgotten.
	e.days(22)
	_, err = e.app.StakingKeeper.GetRedelegation(e.ctx(), mod, vA, vB)
	require.ErrorIs(t, err, stakingtypes.ErrNoRedelegation)
	_, ok := e.move(fakeMoveKey("same1"))
	require.False(t, ok)
	e.invariants()
}

// The slash debt, end to end with real proofs. A double-signs; the wallet
// redelegates A -> B AFTER the infraction and BEFORE the evidence. When the
// evidence lands, x/staking burns 5% of the move's entry shares from the
// module's delegation at B; the module takes the derth they backed off B's
// supply, so B's rate (every honest holder's value) does not move, and the
// move owes it through its debt row. The exposed note pays wherever it goes
// next: merged with a top-up it keeps its label; it cannot leave the note
// while the window is open; its vote counts the haircut; once the window
// closes, clearing the label gives the note exactly its unexposed part plus
// the retained exposure, and an undelegation of it pays that, no more.
func TestRedelegateSlashDebt(t *testing.T) {
	e := initStakeEnv(t)
	e.fundPoolDirect(20_000 * ssErth)
	vA, _ := e.createValidator(1000 * ssErth)
	vB, _ := e.createValidator(1000 * ssErth)
	e.next(5 * time.Second)
	e.shield(uint64(5_000 * ssErth))
	for range 6 {
		e.shield(uint64(100 * ssErth))
	}
	dB := sstypes.DerthDenom(e.valoper(vB))
	n := e.delegate(vA, uint64(2_000*ssErth))
	e.fakeDelegate(vB, uint64(3_000*ssErth), "honest") // B's other holders
	e.days(1)
	e.next(time.Hour)

	// The infraction, then the move (two blocks later: x/evidence slashes
	// entries created at or after infraction - 1).
	e.next(5 * time.Second)
	infraction := e.height
	e.next(5 * time.Second)
	e.next(5 * time.Second)
	b, _ := e.redelegate(vA, vB, n, uint64(1_000*ssErth))
	key := privacy.FieldBytes(b.moveKey)
	mv, ok := e.move(key)
	require.True(t, ok)
	require.Greater(t, mv.EntryHeight, infraction)
	e.invariants()

	// The evidence.
	rateB := e.rate(vB)
	supplyB := e.state(vB).DerthSupply
	delB := e.modDelegation(vB)
	res := e.doubleSign(vA, infraction)
	require.Empty(t, eventsOf(res.Events, sstypes.EventTypeEpochFailure), "no deferred work")
	sd := eventsOf(res.Events, sstypes.EventTypeSlashDebt)
	require.Len(t, sd, 1)
	debtD := evInt(t, sd[0], "debt")
	require.True(t, debtD.IsPositive())
	require.True(t, e.modDelegation(vB).LT(delB), "x/staking burnt the entry's slash at B")
	// B's honest holders keep their value: the rate does not fall.
	e.requireRateKept(vB, rateB, debtD)
	require.Equal(t, supplyB.Sub(debtD), e.state(vB).DerthSupply)
	require.Equal(t, debtD, e.state(vB).SlashDebt)
	// The move owes it: about 5% of its credit (the slash was 5% of the
	// value moved; the credit has since earned at B).
	mv, ok = e.move(key)
	require.True(t, ok)
	require.Equal(t, mv.Credited.Sub(debtD), mv.Retained)
	// x/staking burnt 5% of the entry (the bonded part of the move); the
	// debt is the derth that backed, at B's rate.
	red, err := e.app.StakingKeeper.GetRedelegation(e.ctx(), e.app.ShieldedStakingKeeper.ModuleAddress(), vA, vB)
	require.NoError(t, err)
	burnt := evInt(t, sd[0], "value")
	require.InEpsilon(t, red.Entries[0].InitialBalance.Int64()/20, burnt.Int64(), 1e-6)
	require.InEpsilon(t, math.LegacyNewDecFromInt(burnt).Quo(rateB).TruncateInt64(), debtD.Int64(), 1e-3)
	q, err := sskeeper.NewQueryServerImpl(e.app.ShieldedStakingKeeper).Move(e.ctx(), &sstypes.QueryMoveRequest{Key: hex.EncodeToString(key)})
	require.NoError(t, err)
	require.True(t, q.Slashed)
	require.Equal(t, mv.Retained.Uint64(), q.Retained)
	moved := eventsOf(res.Events, sstypes.EventTypeMoveSlashed)
	require.Len(t, moved, 1)
	require.Equal(t, hex.EncodeToString(key), moved[0]["move_key"])
	e.invariants()

	// The exposed note moves on: a top-up merges into it and keeps the
	// label (the exposure untouched).
	b2 := e.delegate(vB, uint64(200*ssErth))
	require.True(t, b.spent)
	require.Equal(t, b.moveKey, b2.moveKey)
	require.Equal(t, b.exposed, b2.exposed)
	// It cannot leave while the window is open: an undelegation of more
	// than the unexposed part has no proof, nor a clear.
	um, usp := e.undelegateUnproven(vB, b2, b2.amount-b2.exposed+1)
	_, err = e.tryProveStake(um, usp)
	requireRefused(t, err)
	// Its vote counts the haircut.
	prop := e.submitProposal()
	e.next(5 * time.Second)
	value := e.clearedValue(b2)
	require.Equal(t, b2.amount-b2.exposed+mv.Retained.Uint64(), value)
	over, _, vpo := e.stakeVoteMsg(b2, prop, v1.NewNonSplitVoteOption(v1.OptionYes), sstypes.RoundVoteWeight(b2.amount), false)
	_, err = e.tryProveVote(over, vpo)
	requireRefused(t, err)
	vote := e.stakeVoteNotes([]*snote{b2}, prop, v1.OptionYes)
	require.Equal(t, sstypes.RoundVoteWeight(value), vote.Weight)
	e.invariants()

	// The window closes (the entry matures; the move is forgotten, its debt
	// row stays): the label clears at the retained value, and the cleared
	// note undelegates exactly that.
	e.days(22)
	_, ok = e.move(key)
	require.False(t, ok)
	q, err = sskeeper.NewQueryServerImpl(e.app.ShieldedStakingKeeper).Move(e.ctx(), &sstypes.QueryMoveRequest{Key: hex.EncodeToString(key)})
	require.NoError(t, err)
	require.False(t, q.Found)
	require.True(t, q.Slashed)
	cur := e.unspentStake(dB)
	require.Equal(t, b2, cur)
	cleared := e.restake([]*snote{cur}, true)
	require.False(t, cleared.labelled())
	require.Equal(t, value, cleared.amount)
	rate := e.rate(vB)
	un := e.undelegate(vB, cleared, cleared.amount)
	// It pays its value at B's rate: the exposure's cut is not paid out.
	require.InEpsilon(t, rate.MulInt64(int64(value)).TruncateInt64(), int64(un.value), 1e-3)
	e.invariants()
}

// A slash of A for an infraction before a redelegation A -> B reaches the
// entry while it matures (driven without notes): B's rate holds, B's supply
// gives up the derth the burnt value backed (the moves' debt), B's
// undelegations already under way are untouched (set aside for the slash),
// the rewards the slash's unbond would pay are booked, and every invariant
// holds. A move created before the infraction is not charged.
func TestRedelegateSlashDuringMaturity(t *testing.T) {
	e := initStakeEnv(t)
	e.fundPoolDirect(20_000 * ssErth)
	vA, _ := e.createValidator(1000 * ssErth)
	vB, _ := e.createValidator(1000 * ssErth)
	e.next(5 * time.Second)
	mod := e.app.ShieldedStakingKeeper.ModuleAddress()
	e.fakeDelegate(vA, uint64(4_000*ssErth), "a")
	e.fakeDelegate(vB, uint64(2_000*ssErth), "b")
	e.days(1)

	// A move before the infraction: not exposed to it.
	_, err := e.fakeRedelegate(vA, vB, uint64(300*ssErth), "before")
	require.NoError(t, err)
	e.next(5 * time.Second)
	e.next(5 * time.Second)
	e.next(5 * time.Second)
	infraction := e.height

	// After the infraction: an undelegation from B reaches x/staking, then
	// two moves A -> B in one block, and one more later.
	e.fakeUndelegate(vB, uint64(20*ssErth), "b-out")
	e.days(1)
	ubd, err := e.app.StakingKeeper.GetUnbondingDelegation(e.ctx(), mod, vB)
	require.NoError(t, err)
	require.Len(t, ubd.Entries, 1)
	ubdBalance := ubd.Entries[0].Balance
	_, err = e.fakeRedelegate(vA, vB, uint64(1_000*ssErth), "ab1")
	require.NoError(t, err)
	_, err = e.fakeRedelegate(vA, vB, uint64(500*ssErth), "ab2")
	require.NoError(t, err)
	e.next(5 * time.Second)
	_, err = e.fakeRedelegate(vA, vB, uint64(250*ssErth), "ab3")
	require.NoError(t, err)
	e.next(5 * time.Second)
	red, err := e.app.StakingKeeper.GetRedelegation(e.ctx(), mod, vA, vB)
	require.NoError(t, err)
	require.Len(t, red.Entries, 3)
	e.invariants()

	rateB := e.rate(vB)
	supplyB := e.state(vB).DerthSupply
	res := e.doubleSign(vA, infraction)
	require.Empty(t, eventsOf(res.Events, sstypes.EventTypeEpochFailure), "no deferred work")
	ctx := e.ctx()
	// B's undelegation in flight is untouched (and back in place).
	ubd, err = e.app.StakingKeeper.GetUnbondingDelegation(ctx, mod, vB)
	require.NoError(t, err)
	require.Len(t, ubd.Entries, 1)
	require.Equal(t, ubdBalance, ubd.Entries[0].Balance)
	has, err := e.app.ShieldedStakingKeeper.ShelteredUnbondings.Has(ctx, vB)
	require.NoError(t, err)
	require.False(t, has)
	// B's rate holds; its supply gave up the debt.
	debtD := evInt(t, eventsOf(res.Events, sstypes.EventTypeSlashDebt)[0], "debt")
	e.requireRateKept(vB, rateB, debtD)
	require.Equal(t, supplyB.Sub(debtD), e.state(vB).DerthSupply)
	// The three moves after the infraction owe it, pro rata to their shares
	// (5% each); the one before owes nothing.
	before, _ := e.move(fakeMoveKey("before"))
	require.Equal(t, before.Credited, before.Retained)
	sum := math.ZeroInt()
	for _, l := range []string{"ab1", "ab2", "ab3"} {
		mv, ok := e.move(fakeMoveKey(l))
		require.True(t, ok)
		cut := mv.Credited.Sub(mv.Retained)
		require.InEpsilon(t, mv.Credited.Int64()/20, cut.Int64(), 0.05, l)
		sum = sum.Add(cut)
	}
	require.Equal(t, debtD, sum)
	require.Len(t, eventsOf(res.Events, sstypes.EventTypeMoveSlashed), 3)
	root, size, err := e.app.ShieldedStakingKeeper.DebtRoot(ctx)
	require.NoError(t, err)
	require.Equal(t, uint64(4), size, "the sentinel and three rows")
	rr, err := e.debtTree().Root()
	require.NoError(t, err)
	require.Equal(t, privacy.FieldBytes(rr), root)
	// A was slashed and tombstoned.
	valA, err := e.app.StakingKeeper.GetValidator(ctx, vA)
	require.NoError(t, err)
	require.True(t, valA.IsJailed())
	e.invariants()

	// Through maturity: the entries complete, B's undelegation pays its full
	// value, the debt rows stay.
	e.days(22)
	e.invariants()
	_, err = e.app.StakingKeeper.GetRedelegation(e.ctx(), mod, vA, vB)
	require.ErrorIs(t, err, stakingtypes.ErrNoRedelegation)
	_, size, err = e.app.ShieldedStakingKeeper.DebtRoot(e.ctx())
	require.NoError(t, err)
	require.Equal(t, uint64(4), size)
}

// The merge rules, with real proofs: one note per validator; a top-up of a
// labelled note keeps its label; a credit merges only into an unlabelled
// note (a second labelled credit makes a second note); two labelled notes do
// not merge, and the exposure does not leave its note, until their windows
// close; then each clears (unslashed: at its whole exposure) and the notes
// merge into one.
func TestRedelegateMergeRules(t *testing.T) {
	e := initStakeEnv(t)
	vA, _ := e.createValidator(1000 * ssErth)
	vB, _ := e.createValidator(1000 * ssErth)
	vC, _ := e.createValidator(1000 * ssErth)
	e.next(5 * time.Second)
	e.shield(uint64(6_000 * ssErth))
	for range 8 {
		e.shield(uint64(100 * ssErth))
	}
	dB := sstypes.DerthDenom(e.valoper(vB))
	// One note per validator: a second delegation merges.
	a1 := e.delegate(vA, uint64(1_000*ssErth))
	a2 := e.delegate(vA, uint64(500*ssErth))
	require.True(t, a1.spent)
	require.Equal(t, a1.amount+uint64(495*ssErth), a2.amount, "the top-up merged (1%% quote margin)")
	c := e.delegate(vC, uint64(1_000*ssErth))
	e.days(1)

	// A -> B: the wallet has no B note (a padding input), the credit is a
	// new labelled note.
	b1, _ := e.redelegate(vA, vB, a2, uint64(600*ssErth))
	require.True(t, b1.labelled())
	require.Equal(t, b1.amount, b1.exposed)
	// C -> B while the B note is labelled: a second labelled note.
	b2, _ := e.redelegate(vC, vB, c, uint64(400*ssErth))
	require.False(t, b1.spent)
	require.NotEqual(t, b1.moveKey, b2.moveKey)
	require.Equal(t, b1.amount+b2.amount, e.stakeBalance(dB))
	// A top-up merges into one of them and keeps its label.
	b3 := e.delegate(vB, uint64(100*ssErth))
	require.True(t, b1.spent || b2.spent)
	require.True(t, b3.labelled())
	other := b1
	if b1.spent {
		other = b2
	}
	// The two labelled notes do not merge: no proof.
	p := e.feeOnly()
	sp := e.stake(&stakePlan{denom: dB, ins: []*snote{b3, other}, out: e.freshStake(dB, b3.amount+other.amount)})
	m := &sstypes.MsgRestake{Bundle: p.b, Validator: e.valoper(vB), Stake: sp.proof}
	_, err := e.tryProveStake(m, sp)
	requireRefused(t, err)
	// Nor does a lock take exposed derth.
	p = e.feeOnly()
	lsp := e.changePlan(other, other.amount, positionKey(9), false)
	lm := &sstypes.MsgLockPosition{Bundle: p.b, Validator: e.valoper(vB), Amount: other.amount, Stake: lsp.proof}
	_, err = e.tryProveStake(lm, lsp)
	requireRefused(t, err)
	// Nor can a label clear before its window closes (the chain refuses an
	// early clear_before; the circuit, an open window).
	early := &stakePlan{denom: dB, ins: []*snote{other}, out: e.freshStake(dB, other.amount), clear: true}
	e.stake(early)
	require.Less(t, early.proof.ClearBefore, other.moveTime, "the window is open")
	em := &sstypes.MsgRestake{Bundle: e.feeOnly().b, Validator: e.valoper(vB), Stake: early.proof}
	_, err = e.tryProveStake(em, early)
	requireRefused(t, err)
	e.invariants()

	// Past the windows: clear each (never slashed: the whole exposure), then
	// merge into one note.
	e.days(22)
	c1 := e.restake([]*snote{b3}, true)
	require.False(t, c1.labelled())
	require.Equal(t, b3.amount, c1.amount)
	one := e.restake([]*snote{c1, other}, true)
	require.False(t, one.labelled())
	require.Equal(t, c1.amount+other.amount, one.amount)
	require.Equal(t, one.amount, e.stakeBalance(dB))
	e.invariants()
}

// Votes on a proposal whose snapshot predates a move: a derth/A note votes
// as A once, before or after it moves (its nullifier entered the tree after
// the snapshot's nullifier root), and its derth/B credit never votes on that
// proposal (made after the snapshot, it is not under the note root). The
// same holds for a top-up: the note that held the pre-existing value at the
// snapshot votes it after being merged away; the merged note does not; no
// unit of stake votes twice.
func TestRedelegateVoteSnapshot(t *testing.T) {
	e := initStakeEnv(t)
	vA, _ := e.createValidator(1000 * ssErth)
	vB, _ := e.createValidator(1000 * ssErth)
	vC, _ := e.createValidator(1000 * ssErth)
	vD, _ := e.createValidator(1000 * ssErth)
	e.next(5 * time.Second)
	e.shield(uint64(5_000 * ssErth))
	for range 8 {
		e.shield(uint64(200 * ssErth))
	}
	n1 := e.delegate(vA, uint64(1_000*ssErth))
	n2 := e.delegate(vC, uint64(600*ssErth))
	t1 := e.delegate(vD, uint64(500*ssErth))
	e.days(1)
	prop := e.submitProposal()

	// n1 votes as A, then moves to B whole (its credit: a second B note).
	e.stakeVote(n1, prop, v1.OptionYes)
	b1, _ := e.redelegate(vA, vB, n1, n1.amount)
	again, _, _ := e.stakeVoteMsg(n1, prop, v1.NewNonSplitVoteOption(v1.OptionNo), 0, false)
	res := e.checkTx(e.privateTx(again))
	require.Equal(t, sstypes.ErrVoteNullifierUsed.ABCICode(), res.Code, res.Log)
	sb1, _, vpb1 := e.stakeVoteMsg(b1, prop, v1.NewNonSplitVoteOption(v1.OptionNo), 0, false)
	_, err := e.tryProveVote(sb1, vpb1)
	requireRefused(t, err)

	// n2 moves first (C -> B), then votes as C, once; its credit cannot.
	b2, _ := e.redelegate(vC, vB, n2, n2.amount)
	e.stakeVote(n2, prop, v1.OptionNo)
	sb2, _, vpb2 := e.stakeVoteMsg(b2, prop, v1.NewNonSplitVoteOption(v1.OptionYes), 0, false)
	_, err = e.tryProveVote(sb2, vpb2)
	requireRefused(t, err)

	// t1 (D) is topped up after the snapshot: the merged note cannot vote on
	// this proposal; t1, merged away, still votes its own value, once.
	t2 := e.delegate(vD, uint64(300*ssErth))
	require.True(t, t1.spent)
	st2, _, vpt2 := e.stakeVoteMsg(t2, prop, v1.NewNonSplitVoteOption(v1.OptionYes), 0, false)
	_, err = e.tryProveVote(st2, vpt2)
	requireRefused(t, err)
	e.stakeVote(t1, prop, v1.OptionAbstain)
	again, _, _ = e.stakeVoteMsg(t1, prop, v1.NewNonSplitVoteOption(v1.OptionYes), 0, false)
	res = e.checkTx(e.privateTx(again))
	require.Equal(t, sstypes.ErrVoteNullifierUsed.ABCICode(), res.Code, res.Log)

	// The votes: A's, C's and D's (t1's pre-existing value), each once; B
	// (the credits) has none.
	dec := func(x uint64) math.LegacyDec { return math.LegacyNewDecFromInt(math.NewIntFromUint64(x)) }
	tally := func(v sdk.ValAddress) sstypes.VoteTally {
		tl, err := e.app.ShieldedStakingKeeper.Tallies.Get(e.ctx(), collections.Join(prop, e.valoper(v)))
		require.NoError(t, err)
		return tl
	}
	require.Equal(t, dec(sstypes.RoundVoteWeight(n1.amount)), tally(vA).Yes)
	require.Equal(t, dec(sstypes.RoundVoteWeight(n2.amount)), tally(vC).No)
	require.Equal(t, dec(sstypes.RoundVoteWeight(t1.amount)), tally(vD).Abstain)
	_, err = e.app.ShieldedStakingKeeper.Tallies.Get(e.ctx(), collections.Join(prop, e.valoper(vB)))
	require.ErrorIs(t, err, collections.ErrNotFound)
	require.Equal(t, 3, countVotes(t, e, prop))

	// The tally counts no stake twice: its total power is within the bonded
	// stake.
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
	e.invariants()
}

// Groundworks weight is per validator and lives in positions. A note
// redelegation leaves A's positions and rates as they are; a position's
// weight moves from A to B by unlocking it (its derth merged back into the
// A note), redelegating and, once the move's window closes and the label
// clears, locking at B: A's voter loses the weight, B's voter gains it at
// B's epoch rate, and the option's allocation follows.
func TestRedelegateGroundworksWeight(t *testing.T) {
	e := initStakeEnv(t)
	vA, _ := e.createValidator(1000 * ssErth)
	vB, _ := e.createValidator(1000 * ssErth)
	e.next(5 * time.Second)
	e.shield(uint64(3_000 * ssErth))
	for range 6 {
		e.shield(uint64(100 * ssErth))
	}
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
	e.days(2)
	rateA := e.state(vA).EpochRate
	require.True(t, rateA.GT(math.LegacyOneDec()))

	id := e.lock(dn, uint64(500*ssErth), positionKey(1), opt)
	wA := rateA.MulInt64(500 * ssErth).TruncateInt()
	vtA, err := voter(vA)
	require.NoError(t, err)
	require.Equal(t, wA, vtA.Weight)
	require.Equal(t, wA, allocated())

	// A note redelegation (the lock's change) leaves A's positions and
	// epoch rate alone.
	change := e.unspentStake(dn.denom)
	require.NotNil(t, change)
	_, rres := e.redelegate(vA, vB, change, uint64(200*ssErth))
	require.Equal(t, rateA, e.state(vA).EpochRate)
	vtA, err = voter(vA)
	require.NoError(t, err)
	require.Equal(t, wA, vtA.Weight)
	// Both sides were re-filed at the end of the move's block (audit 7):
	// the re-weigh set is empty again.
	for _, v := range []sdk.ValAddress{vA, vB} {
		has, err := e.app.ShieldedStakingKeeper.SlashedValidators.Has(e.ctx(), e.valoper(v))
		require.NoError(t, err)
		require.False(t, has)
	}
	require.NotEmpty(t, eventsOf(rres.Events, sstypes.EventTypeRedelegate))
	e.invariants()

	// Unlock the position: its derth merges into the A note; A's voter goes.
	pt := e.feeOnly()
	usp := e.unlockPlan(id, positionKey(1))
	um := &sstypes.MsgUnlockPosition{Bundle: pt.b, PositionId: id, Stake: usp.proof}
	e.prove(um, pt)
	e.proveStake(um, usp)
	fb := e.run(e.privateTx(um))
	require.Equal(t, uint32(0), fb.Code, fb.Log)
	e.settle(pt)
	e.settleStake(usp)
	back := usp.out
	require.True(t, back.known)
	_, err = voter(vA)
	require.ErrorIs(t, err, collections.ErrNotFound)
	require.True(t, allocated().IsZero())

	// Redelegate it all to B (merged into the labelled B note? no: a
	// labelled note takes no credit, so a second B note); once the windows
	// close, clear and merge, and lock at B: B's voter carries it, at B's
	// epoch rate.
	e.redelegate(vA, vB, back, back.amount)
	e.days(22)
	dB := sstypes.DerthDenom(e.valoper(vB))
	var bs []*snote
	for _, n := range e.sw.notes {
		if n.known && !n.spent && n.denom == dB {
			bs = append(bs, n)
		}
	}
	require.Len(t, bs, 2)
	c1 := e.restake(bs[:1], true)
	b := e.restake([]*snote{c1, bs[1]}, true)
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

// Redelegations in flight and the slash debt cross a genesis: x/staking
// exports the module's entries, this module its moves, debt rows and the
// longest unbonding time seen; InitGenesis rebuilds the same debt tree,
// accepts the entries their moves own (and refuses anyone else's, or an
// entry its moves do not add up to), and the books and invariants carry
// over.
func TestRedelegateGenesisRoundTrip(t *testing.T) {
	e := initStakeEnv(t)
	e.fundPoolDirect(20_000 * ssErth)
	vA, _ := e.createValidator(1000 * ssErth)
	vB, _ := e.createValidator(1000 * ssErth)
	e.next(5 * time.Second)
	mod := e.app.ShieldedStakingKeeper.ModuleAddress()
	e.fakeDelegate(vA, uint64(4_000*ssErth), "a")
	e.fakeDelegate(vB, uint64(1_000*ssErth), "b")
	e.days(1)
	e.next(5 * time.Second)
	infraction := e.height
	e.next(5 * time.Second)
	e.next(5 * time.Second)
	r, err := e.fakeRedelegate(vA, vB, uint64(1_000*ssErth), "ab")
	require.NoError(t, err)
	require.Positive(t, r.CompletionTime)
	e.next(5 * time.Second)
	_, err = e.fakeRedelegate(vA, vB, uint64(500*ssErth), "ab2")
	require.NoError(t, err)
	e.next(5 * time.Second)
	e.doubleSign(vA, infraction)
	e.next(5 * time.Second)
	e.invariants()
	_, size, err := e.app.ShieldedStakingKeeper.DebtRoot(e.ctx())
	require.NoError(t, err)
	require.Equal(t, uint64(3), size)

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
	require.Len(t, red.Entries, 2)
	gs1, err := e.app.ShieldedStakingKeeper.ExportGenesis(e.ctx())
	require.NoError(t, err)
	gs2, err := fresh.ShieldedStakingKeeper.ExportGenesis(fctx)
	require.NoError(t, err)
	require.Equal(t, gs1, gs2)
	require.Len(t, gs1.Moves, 2)
	require.Len(t, gs1.DebtRows, 2)
	require.Positive(t, gs1.MaxUnbondingSeconds)
	root1, _, err := e.app.ShieldedStakingKeeper.DebtRoot(e.ctx())
	require.NoError(t, err)
	root2, _, err := fresh.ShieldedStakingKeeper.DebtRoot(fctx)
	require.NoError(t, err)
	require.Equal(t, root1, root2)
	require.NoError(t, fresh.ShieldedStakingKeeper.AssertInvariants(fctx))

	edit := func(f func(gs *sstypes.GenesisState)) func(map[string]json.RawMessage) {
		return func(appState map[string]json.RawMessage) {
			var gs sstypes.GenesisState
			require.NoError(t, e.app.AppCodec().UnmarshalJSON(appState[sstypes.ModuleName], &gs))
			f(&gs)
			bz, err := e.app.AppCodec().MarshalJSON(&gs)
			require.NoError(t, err)
			appState[sstypes.ModuleName] = bz
		}
	}
	// An entry its moves do not add up to.
	_, _, err = importExport(edit(func(gs *sstypes.GenesisState) { gs.Moves = gs.Moves[:1] }))
	require.Error(t, err)
	// A cut move without its debt row, a row with another value.
	_, _, err = importExport(edit(func(gs *sstypes.GenesisState) { gs.DebtRows = gs.DebtRows[:1] }))
	require.Error(t, err)
	_, _, err = importExport(edit(func(gs *sstypes.GenesisState) { gs.DebtRows[0].Retained++ }))
	require.Error(t, err)
	// Audit 7: a move whose completion is not its entry's (it would be
	// pruned early); a debt row or move keyed by no spent stake nullifier.
	_, _, err = importExport(edit(func(gs *sstypes.GenesisState) { gs.Moves[0].Completion-- }))
	require.ErrorContains(t, err, "completion")
	_, _, err = importExport(edit(func(gs *sstypes.GenesisState) {
		for i, nf := range gs.StakeNullifiers {
			if string(nf) == string(gs.DebtRows[0].Key) {
				gs.StakeNullifiers = append(gs.StakeNullifiers[:i:i], gs.StakeNullifiers[i+1:]...)
				return
			}
		}
		t.Fatal("the row's key is a spent stake nullifier")
	}))
	require.ErrorContains(t, err, "not a spent stake nullifier")
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
	require.ErrorContains(t, err, "only private staking redelegates")

	// The original matures, the entries go, the debt rows stay.
	e.days(22)
	_, err = e.app.StakingKeeper.GetRedelegation(e.ctx(), mod, vA, vB)
	require.ErrorIs(t, err, stakingtypes.ErrNoRedelegation)
	_, size, err = e.app.ShieldedStakingKeeper.DebtRoot(e.ctx())
	require.NoError(t, err)
	require.Equal(t, uint64(3), size)
	e.invariants()
}

// Invariants 9 and 10: a redelegation that is not the module's, an entry
// without the moves that own it, a move whose retained is not its row's,
// an unbonding delegation left set aside, a slash left watched: each is
// reported.
func TestRedelegateInvariant(t *testing.T) {
	e := initStakeEnv(t)
	e.fundPoolDirect(10_000 * ssErth)
	vA, _ := e.createValidator(1000 * ssErth)
	vB, _ := e.createValidator(1000 * ssErth)
	e.next(5 * time.Second)
	e.fakeDelegate(vA, uint64(1_000*ssErth), "a")
	e.days(1)
	_, err := e.fakeRedelegate(vA, vB, uint64(800*ssErth), "ab")
	require.NoError(t, err)
	e.next(5 * time.Second)
	e.invariants()
	k := e.app.ShieldedStakingKeeper
	sk := e.app.StakingKeeper

	ctx, _ := e.ctx().CacheContext()
	red, err := sk.GetRedelegation(ctx, k.ModuleAddress(), vA, vB)
	require.NoError(t, err)
	red.DelegatorAddress = e.bech(sdk.AccAddress(vA))
	require.NoError(t, sk.SetRedelegation(ctx, red))
	require.ErrorContains(t, k.AssertInvariants(ctx), "only private staking redelegates")

	ctx, _ = e.ctx().CacheContext()
	mv, ok := e.move(fakeMoveKey("ab"))
	require.True(t, ok)
	mv.Shares = mv.Shares.QuoInt64(2)
	require.NoError(t, k.Moves.Set(ctx, mv.Key, mv))
	require.ErrorContains(t, k.AssertInvariants(ctx), "its moves hold")

	ctx, _ = e.ctx().CacheContext()
	mv, _ = e.move(fakeMoveKey("ab"))
	mv.Retained = mv.Retained.SubRaw(1)
	require.NoError(t, k.Moves.Set(ctx, mv.Key, mv))
	require.ErrorContains(t, k.AssertInvariants(ctx), "without a debt row")

	ctx, _ = e.ctx().CacheContext()
	ubd := stakingtypes.NewUnbondingDelegation(k.ModuleAddress(), vB, 1, e.now, math.OneInt(), 1,
		sk.ValidatorAddressCodec(), e.app.AuthKeeper.AddressCodec())
	require.NoError(t, k.ShelteredUnbondings.Set(ctx, vB, ubd))
	require.ErrorContains(t, k.AssertInvariants(ctx), "set aside")

	ctx, _ = e.ctx().CacheContext()
	require.NoError(t, k.WatchSrc.Set(ctx, e.valoper(vA)))
	require.ErrorContains(t, k.AssertInvariants(ctx), "still watched")
}

// noteLabelled is the wallet's unspent note of denom labelled with the move
// key, nil if none.
func (e *stakeEnv) noteLabelled(denom string, key fr.Element) *snote {
	for _, n := range e.sw.notes {
		if n.known && !n.spent && n.denom == denom && n.moveKey == key {
			return n
		}
	}
	return nil
}

// undelegateUnproven is an undelegation of amount of in, built but not
// proven (its stake proof for tryProveStake).
func (e *stakeEnv) undelegateUnproven(val sdk.ValAddress, in *snote, amount uint64) (*sstypes.MsgUndelegate, *stakePlan) {
	p := e.feeOnly()
	out := e.w.fresh("uerth", 0)
	sp := &stakePlan{denom: in.denom, ins: []*snote{in}, vOut: amount}
	if in.amount > amount {
		sp.out = e.freshStake(in.denom, in.amount-amount)
	}
	e.stake(sp)
	m := &sstypes.MsgUndelegate{Bundle: p.b, Validator: e.valoper(val), Amount: amount, Stake: sp.proof,
		Pc: privacy.FieldBytes(e.w.pc(out)), Ciphertext: shieldedtest.BlindCT(fmt.Sprintf("payout/%d", e.w.seq))}
	return m, sp
}

// fillEntryCap fills the module's (a, b) redelegation to
// MaxEntryHeightsPerPair entries, at the heights after its single entry's,
// entry i owned by moves[i] fake moves of 1,000 shares each (1 if unset).
// Test-only: that many blocks of moves would take minutes.
func (e *stakeEnv) fillEntryCap(a, b sdk.ValAddress, moves map[int]int) stakingtypes.Redelegation {
	e.t.Helper()
	k := e.app.ShieldedStakingKeeper
	ctx := e.ctx()
	red, err := e.app.StakingKeeper.GetRedelegation(ctx, k.ModuleAddress(), a, b)
	require.NoError(e.t, err)
	require.Len(e.t, red.Entries, 1)
	last := red.Entries[0]
	for i := 1; i < sstypes.MaxEntryHeightsPerPair; i++ {
		n := moves[i]
		if n == 0 {
			n = 1
		}
		en := last
		en.CreationHeight = last.CreationHeight + int64(i)
		en.InitialBalance = math.NewInt(int64(1_000 * n))
		en.SharesDst = math.LegacyNewDec(int64(1_000 * n))
		en.UnbondingId = last.UnbondingId + 10_000 + uint64(i)
		red.Entries = append(red.Entries, en)
		for j := 0; j < n; j++ {
			key := privacy.FieldBytes(ssDet(fmt.Sprintf("cap-move/%d", i), uint64(j)))
			require.NoError(e.t, k.Moves.Set(ctx, key, sstypes.Move{Key: key, SrcValidator: e.valoper(a), DstValidator: e.valoper(b),
				Height: en.CreationHeight, MoveTime: uint64(e.now.Unix()), Credited: math.NewInt(1_000), Shares: math.LegacyNewDec(1_000),
				EntryHeight: en.CreationHeight, Completion: en.CompletionTime.UnixNano(), Retained: math.NewInt(1_000)}))
			require.NoError(e.t, k.MovesByEntry.Set(ctx, collections.Join3(e.valoper(a)+"/"+e.valoper(b), en.CreationHeight, key)))
			require.NoError(e.t, k.MovesByCompletion.Set(ctx, collections.Join(en.CompletionTime.UnixNano(), key)))
		}
	}
	require.NoError(e.t, e.app.StakingKeeper.SetRedelegation(ctx, red))
	// Past the filled heights, as if those blocks had passed.
	for e.height <= red.Entries[len(red.Entries)-1].CreationHeight {
		e.next(time.Second)
	}
	e.invariants()
	return red
}

// At MaxEntryHeightsPerPair entries for a pair, a bonded move first merges
// the two oldest entries (the later height, the earlier completion, their
// moves re-filed under it) and then adds its own entry at its own height:
// a move is never in an entry older than itself, so a slash for an
// infraction before it always reaches it (audit 7: joining the latest entry
// let a move made after an infraction, into a pair kept at the cap, escape
// it). The move pays gas for the pair's record (A7-L1).
func TestRedelegateEntryCap(t *testing.T) {
	e := initStakeEnv(t)
	e.fundPoolDirect(100_000 * ssErth)
	vA, _ := e.createValidator(1000 * ssErth)
	vB, _ := e.createValidator(1000 * ssErth)
	e.next(5 * time.Second)
	k := e.app.ShieldedStakingKeeper
	mod := k.ModuleAddress()
	e.fakeDelegate(vA, uint64(50_000*ssErth), "a")
	e.days(1)
	e.next(time.Hour)
	h := sskeeper.NewActionHandler(k)
	quiet := &sstypes.MsgRedelegate{SrcValidator: e.valoper(vA), DstValidator: e.valoper(vB)}
	g0, err := h.PrivateActionGas(e.ctx(), quiet)
	require.NoError(t, err)
	_, err = e.fakeRedelegate(vA, vB, uint64(5_000*ssErth), "first")
	require.NoError(t, err)
	e.next(5 * time.Second)
	g1, err := h.PrivateActionGas(e.ctx(), quiet)
	require.NoError(t, err)
	require.Greater(t, g1, g0, "the pair's record is paid for")

	red := e.fillEntryCap(vA, vB, nil)
	e0, e1 := red.Entries[0], red.Entries[1]
	gCap, err := h.PrivateActionGas(e.ctx(), quiet)
	require.NoError(t, err)
	require.Greater(t, gCap, g1+uint64(sstypes.MaxEntryHeightsPerPair-1)*2_000, "%d", gCap)

	_, err = e.fakeRedelegate(vA, vB, uint64(500*ssErth), "over")
	require.NoError(t, err)
	red, err = e.app.StakingKeeper.GetRedelegation(e.ctx(), mod, vA, vB)
	require.NoError(t, err)
	require.Len(t, red.Entries, sstypes.MaxEntryHeightsPerPair)
	merged := red.Entries[0]
	require.Equal(t, e1.CreationHeight, merged.CreationHeight, "the later height")
	require.True(t, e0.CompletionTime.Equal(merged.CompletionTime), "the earlier completion")
	require.Equal(t, e0.InitialBalance.Add(e1.InitialBalance), merged.InitialBalance)
	require.Equal(t, e0.SharesDst.Add(e1.SharesDst), merged.SharesDst)
	require.Equal(t, e1.UnbondingId, merged.UnbondingId)
	_, err = e.app.StakingKeeper.GetRedelegationByUnbondingID(e.ctx(), e0.UnbondingId)
	require.Error(t, err, "the merged-away entry's unbonding id is gone")
	first, ok := e.move(fakeMoveKey("first"))
	require.True(t, ok)
	require.Equal(t, merged.CreationHeight, first.EntryHeight, "re-filed under the merged entry")
	require.Equal(t, merged.CompletionTime.UnixNano(), first.Completion)
	tip := red.Entries[len(red.Entries)-1]
	over, ok := e.move(fakeMoveKey("over"))
	require.True(t, ok)
	require.Equal(t, e.ctx().BlockHeight(), tip.CreationHeight, "its own entry, at its own height")
	require.Equal(t, tip.CreationHeight, over.EntryHeight)
	require.Equal(t, tip.SharesDst, over.Shares)
	e.next(5 * time.Second)
	e.invariants()
}

// With no two of the oldest entries small enough to re-file (more than
// MaxMergeMoves moves between them, MergeTries pairs), a move at the cap
// joins the latest entry instead, as a last resort: never refused.
func TestRedelegateEntryCapFallback(t *testing.T) {
	e := initStakeEnv(t)
	e.fundPoolDirect(100_000 * ssErth)
	vA, _ := e.createValidator(1000 * ssErth)
	vB, _ := e.createValidator(1000 * ssErth)
	e.next(5 * time.Second)
	mod := e.app.ShieldedStakingKeeper.ModuleAddress()
	e.fakeDelegate(vA, uint64(50_000*ssErth), "a")
	e.days(1)
	e.next(time.Hour)
	_, err := e.fakeRedelegate(vA, vB, uint64(5_000*ssErth), "first")
	require.NoError(t, err)
	e.next(5 * time.Second)
	big := map[int]int{}
	for i := 1; i <= sstypes.MergeTries; i += 2 {
		big[i] = sstypes.MaxMergeMoves + 1
	}
	red := e.fillEntryCap(vA, vB, big)
	tip := red.Entries[len(red.Entries)-1]

	r, err := e.fakeRedelegate(vA, vB, uint64(500*ssErth), "over")
	require.NoError(t, err)
	red, err = e.app.StakingKeeper.GetRedelegation(e.ctx(), mod, vA, vB)
	require.NoError(t, err)
	require.Len(t, red.Entries, sstypes.MaxEntryHeightsPerPair)
	got := red.Entries[len(red.Entries)-1]
	require.Equal(t, tip.CreationHeight, got.CreationHeight)
	require.True(t, tip.CompletionTime.Equal(got.CompletionTime))
	mv, ok := e.move(fakeMoveKey("over"))
	require.True(t, ok)
	require.Equal(t, tip.CreationHeight, mv.EntryHeight)
	require.Equal(t, r.CompletionTime, mv.Completion)
	require.Equal(t, tip.SharesDst.Add(mv.Shares), got.SharesDst)
	e.next(5 * time.Second)
	e.invariants()
}

// exactRedelegate drives a redelegation (ante faked, as fakeRedelegate)
// crediting all that the value buys at dst but for x/staking's truncation of
// the bonded part (a uerth, plus one for the rounding of the buy): the
// mover's credit is as close to its value as the chain accepts, so what it
// keeps is what the move is worth.
func (e *stakeEnv) exactRedelegate(src, dst sdk.ValAddress, amount uint64, label string) *sstypes.MsgRedelegateResponse {
	e.t.Helper()
	k := e.app.ShieldedStakingKeeper
	bA, sA, err := k.Backing(e.ctx(), e.valoper(src))
	require.NoError(e.t, err)
	u := math.NewIntFromUint64(amount).Mul(bA).Quo(sA)
	bB, sB, err := k.Backing(e.ctx(), e.valoper(dst))
	require.NoError(e.t, err)
	credit := u.SubRaw(2).Mul(sB).Quo(bB)
	m := &sstypes.MsgRedelegate{SrcValidator: e.valoper(src), DstValidator: e.valoper(dst), Amount: amount,
		DstDerth: credit.Uint64(), MoveTime: uint64(e.now.Unix()), Stake: fakeStake("redelegate/"+label, true)}
	res, err := sskeeper.NewActionHandler(k).ExecutePrivateAction(e.ctx(), m, nil)
	require.NoError(e.t, err)
	return res.(*sstypes.MsgRedelegateResponse)
}

// a7Outcome is one run of the A7-1 scenario: what A's remaining holders lost
// to the slash (a fraction of their value) and what the attacker's stake is
// worth after it (uerth).
type a7Outcome struct {
	holdersLoss math.LegacyDec
	attacker    math.LegacyDec
	queued      math.Int
	bonded      math.Int
}

// runA7Scenario: A has a 1,000 ERTH self-bond; an attacker holds 1,000 ERTH
// of derth/A and an honest holder 3,000, both bonded; a newcomer queues
// 2,000 in the current epoch. A double-signs; then, if move, the attacker
// moves all its derth/A to B (it saw the evidence coming); then the
// evidence lands.
func runA7Scenario(t *testing.T, move bool) a7Outcome {
	e := initStakeEnv(t)
	e.fundPoolDirect(20_000 * ssErth)
	vA, _ := e.createValidator(1000 * ssErth)
	vB, _ := e.createValidator(1000 * ssErth)
	e.next(5 * time.Second)
	att := e.fakeDelegate(vA, uint64(1_000*ssErth), "attacker")
	e.fakeDelegate(vA, uint64(3_000*ssErth), "honest")
	e.fakeDelegate(vB, uint64(3_000*ssErth), "b")
	e.days(1)
	e.next(time.Hour)
	e.fakeDelegate(vA, uint64(2_000*ssErth), "newcomer") // queued this epoch
	require.True(t, e.state(vA).PendingDelegation.GTE(math.NewInt(2_000*ssErth)))

	e.next(5 * time.Second)
	infraction := e.height
	e.next(5 * time.Second)
	e.next(5 * time.Second)
	out := a7Outcome{queued: math.ZeroInt(), bonded: math.ZeroInt()}
	var key []byte
	if move {
		r := e.exactRedelegate(vA, vB, att.Derth, "attacker")
		key = fakeMoveKey("attacker")
		out.queued = math.NewIntFromUint64(r.Value) // refined from the move below
		mv, ok := e.move(key)
		require.True(t, ok, "the bonded part's move: the slash reaches it")
		require.Greater(t, mv.EntryHeight, infraction)
	}
	before := e.rate(vA)
	e.doubleSign(vA, infraction)
	after := e.rate(vA)
	out.holdersLoss = math.LegacyOneDec().Sub(after.Quo(before))
	if move {
		mv, ok := e.move(key)
		require.True(t, ok)
		require.True(t, mv.Retained.LT(mv.Credited), "the mover's label owes the slash: %s of %s", mv.Retained, mv.Credited)
		out.attacker = e.rate(vB).MulInt(mv.Retained)
	} else {
		out.attacker = after.MulInt64(int64(att.Derth))
	}
	e.invariants()
	return out
}

// Audit 7, A7-1: a redelegation took its value out of the source's queue
// first. That part carried no entry, so a mover who saw a slash of the source
// coming left with the unslashable slice of the book, and its remaining
// holders paid the mover's share (PoC: the attacker avoided 33.3 ERTH of a 5%
// slash; A's holders lost 4.00% instead of 3.33%). A move now leaves the
// queue and the bonded stake pro rata: the mover pays its share through its
// label, and the remaining holders lose no more than without the move.
func TestQueueEscapesSlash(t *testing.T) {
	stay := runA7Scenario(t, false)
	moved := runA7Scenario(t, true)
	t.Logf("no move: holders lose %s, attacker worth %s", stay.holdersLoss, stay.attacker)
	t.Logf("move:    holders lose %s, attacker worth %s", moved.holdersLoss, moved.attacker)
	require.True(t, stay.holdersLoss.IsPositive())
	// The remaining holders lose no more than without the move (a part in
	// 10^4 for the rounding and the block's rewards).
	tol := math.LegacyNewDecWithPrec(1, 4)
	require.True(t, moved.holdersLoss.LTE(stay.holdersLoss.Mul(math.LegacyOneDec().Add(tol))),
		"holders lose %s with the move, %s without", moved.holdersLoss, stay.holdersLoss)
	// The mover pays its share: worth no more than had it stayed.
	require.True(t, moved.attacker.LTE(stay.attacker.Mul(math.LegacyOneDec().Add(tol))),
		"the mover is worth %s, %s had it stayed", moved.attacker, stay.attacker)
	require.True(t, moved.attacker.GTE(stay.attacker.Mul(math.LegacyOneDec().Sub(math.LegacyNewDecWithPrec(1, 3)))),
		"nor much less: %s against %s", moved.attacker, stay.attacker)
}

// The queue-first path is kept only where no slash can reach the source's
// stake: a validator the module holds no bonded stake at yet (its first
// delegations wait in its queue) moves the value out of the queue alone, with
// no entry and no move, since a slash of it takes nothing from the book.
func TestQueueOnlyWithoutBondedStake(t *testing.T) {
	e := initStakeEnv(t)
	e.fundPoolDirect(20_000 * ssErth)
	vA, _ := e.createValidator(1000 * ssErth)
	vB, _ := e.createValidator(1000 * ssErth)
	e.next(5 * time.Second)
	e.fakeDelegate(vB, uint64(1_000*ssErth), "b")
	e.days(1)
	e.next(time.Hour)
	d := e.fakeDelegate(vA, uint64(2_000*ssErth), "first") // queued, nothing bonded at A
	require.True(t, e.modDelegation(vA).IsZero())
	r, err := e.fakeRedelegate(vA, vB, d.Derth/2, "queued")
	require.NoError(t, err)
	require.Zero(t, r.CompletionTime)
	_, ok := e.move(fakeMoveKey("queued"))
	require.False(t, ok)
	_, err = e.app.StakingKeeper.GetRedelegation(e.ctx(), e.app.ShieldedStakingKeeper.ModuleAddress(), vA, vB)
	require.Error(t, err, "no x/staking entry")
	e.invariants()
}

// Audit 7, A7-2: a zero-height export with open moves did not re-import:
// x/staking's prep moves every redelegation entry to height 0 while the
// moves kept their entry heights. The open moves are now dropped (no slash on
// the new chain reaches a height-0 entry); their debt rows stay, so a label
// still clears at its row's value, or whole when its move was never
// slashed. Two entries of one pair at height 0 are accepted, and every
// invariant holds after the prep and after the import.
func TestZeroHeightExportWithMoves(t *testing.T) {
	e := initStakeEnv(t)
	e.fundPoolDirect(20_000 * ssErth)
	vA, _ := e.createValidator(1000 * ssErth)
	vB, _ := e.createValidator(1000 * ssErth)
	e.next(5 * time.Second)
	e.fakeDelegate(vA, uint64(4_000*ssErth), "a")
	e.fakeDelegate(vB, uint64(1_000*ssErth), "b")
	e.days(1)
	e.next(time.Hour)
	k := e.app.ShieldedStakingKeeper

	// A slashed move (a debt row) and, in a later block, one never slashed.
	e.next(5 * time.Second)
	infraction := e.height
	e.next(5 * time.Second)
	e.next(5 * time.Second)
	_, err := e.fakeRedelegate(vA, vB, uint64(1_000*ssErth), "slashed")
	require.NoError(t, err)
	e.doubleSign(vA, infraction)
	slashedKey, cleanKey := fakeMoveKey("slashed"), fakeMoveKey("clean")
	row, err := k.DebtRetained.Get(e.ctx(), slashedKey)
	require.NoError(t, err, "the slashed move's debt row")
	e.next(5 * time.Second)
	_, err = e.fakeRedelegate(vA, vB, uint64(500*ssErth), "clean")
	require.NoError(t, err)
	e.next(5 * time.Second)
	_, ok := e.move(cleanKey)
	require.True(t, ok)
	red, err := e.app.StakingKeeper.GetRedelegation(e.ctx(), k.ModuleAddress(), vA, vB)
	require.NoError(t, err)
	require.Len(t, red.Entries, 2)
	e.invariants()

	// The prep alone keeps every invariant (10 included).
	ctx, _ := e.ctx().CacheContext()
	e.app.prepForZeroHeightGenesis(ctx, nil)
	require.NoError(t, k.AssertInvariants(ctx))
	n := 0
	require.NoError(t, k.Moves.Walk(ctx, nil, func([]byte, sstypes.Move) (bool, error) { n++; return false, nil }))
	require.Zero(t, n, "the open moves are dropped")

	// The export re-imports, with both entries at height 0, no moves, and
	// the debt rows kept.
	exported, err := e.app.ExportAppStateAndValidators(true, nil, nil)
	require.NoError(t, err)
	var appState map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(exported.AppState, &appState))
	fresh := New(log.NewNopLogger(), dbm.NewMemDB(), nil, true, simtestutil.AppOptionsMap{flags.FlagHome: t.TempDir()},
		baseapp.SetChainID(ssChainID))
	fctx := fresh.NewUncachedContext(false, cmtproto.Header{ChainID: ssChainID, Height: 1, Time: e.now})
	_, err = fresh.ModuleManager.InitGenesis(fctx, fresh.AppCodec(), appState)
	require.NoError(t, err, "InitGenesis runs AssertInvariants")
	require.NoError(t, fresh.ShieldedStakingKeeper.AssertInvariants(fctx))
	fred, err := fresh.StakingKeeper.GetRedelegation(fctx, k.ModuleAddress(), vA, vB)
	require.NoError(t, err)
	require.Len(t, fred.Entries, 2)
	for _, en := range fred.Entries {
		require.Zero(t, en.CreationHeight)
	}
	gs, err := fresh.ShieldedStakingKeeper.ExportGenesis(fctx)
	require.NoError(t, err)
	require.Empty(t, gs.Moves)
	got, err := fresh.ShieldedStakingKeeper.DebtRetained.Get(fctx, slashedKey)
	require.NoError(t, err, "the slashed label still clears at its row")
	require.Equal(t, row, got)
	_, err = fresh.ShieldedStakingKeeper.DebtRetained.Get(fctx, cleanKey)
	require.Error(t, err, "the clean label clears whole: no row")
	r0, _, err := k.DebtRoot(e.ctx())
	require.NoError(t, err)
	r1, _, err := fresh.ShieldedStakingKeeper.DebtRoot(fctx)
	require.NoError(t, err)
	require.Equal(t, r0, r1, "the same debt tree")
}

// Audit 7, A7-L2: which entries a slash reached was inferred from the count
// of x/staking's unbonds (the last that many), but x/staking makes none for
// an entry whose slash truncates to nothing, which governance makes common
// by lowering a slash fraction (0.0001: every entry under 10,000 uerth). The
// slash is now replayed with x/slashing's fractions: the large entry it
// reached owes it, the dust entry after it, which it skipped, owes nothing.
func TestSlashSkipsDustEntry(t *testing.T) {
	e := initStakeEnv(t)
	e.fundPoolDirect(20_000 * ssErth)
	vA, _ := e.createValidator(1000 * ssErth)
	vB, _ := e.createValidator(1000 * ssErth)
	e.next(5 * time.Second)
	e.fakeDelegate(vA, uint64(4_000*ssErth), "a")
	e.fakeDelegate(vB, uint64(1_000*ssErth), "b")
	e.days(1)
	e.next(time.Hour)
	k := e.app.ShieldedStakingKeeper
	ctx := e.ctx()
	sp, err := e.app.SlashingKeeper.GetParams(ctx)
	require.NoError(t, err)
	sp.SlashFractionDoubleSign = math.LegacyNewDecWithPrec(1, 4)
	require.NoError(t, e.app.SlashingKeeper.SetParams(ctx, sp))

	e.next(5 * time.Second)
	infraction := e.height
	e.next(5 * time.Second)
	e.next(5 * time.Second)
	_, err = e.fakeRedelegate(vA, vB, uint64(1_000*ssErth), "large")
	require.NoError(t, err)
	e.next(5 * time.Second)
	// A dust entry after it (a move whose bonded part is 5,000 uerth),
	// filled in directly.
	ctx = e.ctx()
	red, err := e.app.StakingKeeper.GetRedelegation(ctx, k.ModuleAddress(), vA, vB)
	require.NoError(t, err)
	require.Len(t, red.Entries, 1)
	dust := red.Entries[0]
	dust.CreationHeight = ctx.BlockHeight()
	dust.InitialBalance = math.NewInt(5_000)
	dust.SharesDst = math.LegacyNewDec(5_000)
	dust.UnbondingId += 1_000
	red.Entries = append(red.Entries, dust)
	require.NoError(t, e.app.StakingKeeper.SetRedelegation(ctx, red))
	dustKey := privacy.FieldBytes(ssDet("dust-move", 0))
	mv := sstypes.Move{Key: dustKey, SrcValidator: e.valoper(vA), DstValidator: e.valoper(vB), Height: dust.CreationHeight,
		MoveTime: uint64(e.now.Unix()), Credited: math.NewInt(5_000), Shares: dust.SharesDst, EntryHeight: dust.CreationHeight,
		Completion: dust.CompletionTime.UnixNano(), Retained: math.NewInt(5_000)}
	require.NoError(t, k.Moves.Set(ctx, dustKey, mv))
	require.NoError(t, k.MovesByEntry.Set(ctx, collections.Join3(e.valoper(vA)+"/"+e.valoper(vB), mv.EntryHeight, dustKey)))
	require.NoError(t, k.MovesByCompletion.Set(ctx, collections.Join(mv.Completion, dustKey)))
	e.next(5 * time.Second)
	e.invariants()

	res := e.doubleSign(vA, infraction)
	require.Empty(t, eventsOf(res.Events, sstypes.EventTypeEpochFailure), "the slash is attributed")
	sd := eventsOf(res.Events, sstypes.EventTypeSlashDebt)
	require.Len(t, sd, 1)
	require.Equal(t, "1", sd[0]["entries"], "x/staking unbonded once: the large entry")
	_, err = k.DebtRetained.Get(e.ctx(), fakeMoveKey("large"))
	require.NoError(t, err, "the large move owes the slash")
	_, err = k.DebtRetained.Get(e.ctx(), dustKey)
	require.Error(t, err, "the dust move, which the slash skipped, owes nothing")
	e.invariants()
}

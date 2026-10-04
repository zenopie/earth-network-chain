package app

import (
	"testing"
	"time"

	"cosmossdk.io/math"

	"github.com/stretchr/testify/require"

	sskeeper "github.com/earth-network/earth/x/shieldedstaking/keeper"
	sstypes "github.com/earth-network/earth/x/shieldedstaking/types"
)

// Audit 6 C-L2: a position holds at most 2^63-1 derth, as its unlock note and
// genesis require; v_out is a public u64, so the bound is the chain's.
func TestAudit6LockPositionNoteBound(t *testing.T) {
	g := initGwEnv(t)
	m := &sstypes.MsgLockPosition{Validator: g.valoper(g.v), Amount: ^uint64(0) - 1, Splits: g.split(100),
		Stake: sstypes.StakeProof{OwnerTag: ownerTag(0)}}
	_, err := sskeeper.NewMsgServerImpl(g.app.ShieldedStakingKeeper).LockPosition(g.fakeAuthorized(m), m)
	require.ErrorIs(t, err, sstypes.ErrAmount)
}

// Audit 6 C-L1: a slash of a validator with no private stake writes no book.
func TestAudit6SlashWithoutBookWritesNone(t *testing.T) {
	e := initStakeEnv(t)
	v, _ := e.createValidator(1000 * ssErth)
	e.next(5 * time.Second)
	k := e.app.ShieldedStakingKeeper
	has, err := k.Validators.Has(e.ctx(), e.valoper(v))
	require.NoError(t, err)
	require.False(t, has)
	val, err := e.app.StakingKeeper.GetValidator(e.ctx(), v)
	require.NoError(t, err)
	cons, err := val.GetConsAddr()
	require.NoError(t, err)
	power := val.ConsensusPower(e.app.StakingKeeper.PowerReduction(e.ctx()))
	_, err = e.app.StakingKeeper.Slash(e.ctx(), cons, e.height, power, math.LegacyNewDecWithPrec(1, 2))
	require.NoError(t, err)
	e.next(5 * time.Second)
	has, err = k.Validators.Has(e.ctx(), e.valoper(v))
	require.NoError(t, err)
	require.False(t, has, "no book for a validator with no private stake")
}

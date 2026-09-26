package keeper

import (
	"context"
	"testing"
	"time"

	"cosmossdk.io/collections"
	codecaddress "cosmossdk.io/core/address"
	"cosmossdk.io/math"
	storetypes "cosmossdk.io/store/types"
	"github.com/cosmos/cosmos-sdk/baseapp"
	addresscodec "github.com/cosmos/cosmos-sdk/codec/address"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/cosmos/cosmos-sdk/testutil"
	sdk "github.com/cosmos/cosmos-sdk/types"
	moduletestutil "github.com/cosmos/cosmos-sdk/types/module/testutil"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	govkeeper "github.com/cosmos/cosmos-sdk/x/gov/keeper"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"
	v1 "github.com/cosmos/cosmos-sdk/x/gov/types/v1"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	"github.com/stretchr/testify/require"

	"github.com/earth-network/earth/x/assembly/types"
	pkitypes "github.com/earth-network/earth/x/pki/types"
)

// A real x/gov keeper, stubbed only at its edges.
//
// The chamber's whole mechanism is an interaction with x/gov's proposal queues
// and its tally, so a fake x/gov would test nothing worth knowing. The stubs
// below stand in for the keepers x/gov itself talks to — accounts, bank,
// staking, distribution — and do as little as the keeper will accept.

type stubAccount struct{ ac codecaddress.Codec }

func (s stubAccount) AddressCodec() codecaddress.Codec { return s.ac }
func (stubAccount) GetAccount(context.Context, sdk.AccAddress) sdk.AccountI {
	return nil
}
func (stubAccount) GetModuleAddress(name string) sdk.AccAddress {
	return authtypes.NewModuleAddress(name)
}
func (stubAccount) GetModuleAccount(_ context.Context, name string) sdk.ModuleAccountI {
	return authtypes.NewEmptyModuleAccount(name)
}
func (stubAccount) SetModuleAccount(context.Context, sdk.ModuleAccountI) {}

// stubGovBank records what x/gov burns, so a test can tell a burned deposit
// from a refunded one.
type stubGovBank struct{ burned *sdk.Coins }

func (stubGovBank) GetAllBalances(context.Context, sdk.AccAddress) sdk.Coins { return sdk.NewCoins() }
func (stubGovBank) GetBalance(_ context.Context, _ sdk.AccAddress, denom string) sdk.Coin {
	return sdk.NewCoin(denom, math.ZeroInt())
}
func (stubGovBank) LockedCoins(context.Context, sdk.AccAddress) sdk.Coins    { return sdk.NewCoins() }
func (stubGovBank) SpendableCoins(context.Context, sdk.AccAddress) sdk.Coins { return sdk.NewCoins() }
func (stubGovBank) SendCoinsFromModuleToAccount(context.Context, string, sdk.AccAddress, sdk.Coins) error {
	return nil
}
func (stubGovBank) SendCoinsFromAccountToModule(context.Context, sdk.AccAddress, string, sdk.Coins) error {
	return nil
}
func (b stubGovBank) BurnCoins(_ context.Context, _ string, c sdk.Coins) error {
	*b.burned = b.burned.Add(c...)
	return nil
}

// stubGovStaking reports a bonded set that exists but votes nothing. The tally
// then reaches its quorum check and fails there, which is the ordinary fate of
// a proposal nobody stake-voted on — and exactly what a test of the chamber
// wants behind it, since the chamber's decision has to stand whatever stake did.
type stubGovStaking struct{ bonded math.Int }

func (stubGovStaking) ValidatorAddressCodec() codecaddress.Codec {
	return addresscodec.NewBech32Codec(sdk.GetConfig().GetBech32ValidatorAddrPrefix())
}
func (stubGovStaking) IterateBondedValidatorsByPower(context.Context, func(int64, stakingtypes.ValidatorI) bool) error {
	return nil
}
func (s stubGovStaking) TotalBondedTokens(context.Context) (math.Int, error) { return s.bonded, nil }
func (stubGovStaking) IterateDelegations(context.Context, sdk.AccAddress, func(int64, stakingtypes.DelegationI) bool) error {
	return nil
}

type stubDistr struct{}

func (stubDistr) FundCommunityPool(context.Context, sdk.Coins, sdk.AccAddress) error { return nil }

type stubRouter struct{}

func (stubRouter) Handler(sdk.Msg) baseapp.MsgServiceHandler         { return nil }
func (stubRouter) HandlerByTypeURL(string) baseapp.MsgServiceHandler { return nil }

// stubPersonhood is the electoral roll: a set of addresses, each standing for
// one registration. The nullifier is what a vote is filed under, so two
// addresses can deliberately share one — that is a person who moved wallets.
type stubPersonhood struct {
	nullifiers map[string][]byte
	// dsc is each nullifier's Document Signer commitment, where a test sets one.
	dsc map[string][]byte
}

func (s *stubPersonhood) register(addr sdk.AccAddress, nullifier string) {
	s.nullifiers[addr.String()] = []byte(nullifier)
}
func (s *stubPersonhood) lapse(addr sdk.AccAddress) { delete(s.nullifiers, addr.String()) }

func (s *stubPersonhood) LiveNullifier(_ context.Context, addr []byte) ([]byte, bool, error) {
	n, ok := s.nullifiers[sdk.AccAddress(addr).String()]
	return n, ok, nil
}

func (s *stubPersonhood) RegistrationDsc(_ context.Context, nullifier []byte) ([]byte, error) {
	return s.dsc[string(nullifier)], nil
}

// stubAllocation records what the chamber asked of x/allocation.
type stubAllocation struct {
	removable map[uint64]bool
	removed   []uint64
	failWith  error
}

func (s *stubAllocation) RemoveGroundworksOption(_ context.Context, _ []byte, id uint64) error {
	if s.failWith != nil {
		return s.failWith
	}
	s.removed = append(s.removed, id)
	s.removable[id] = false
	return nil
}

func (s *stubAllocation) GroundworksOptionRemovable(_ context.Context, id uint64) (bool, error) {
	return s.removable[id], nil
}

type testEnv struct {
	k          Keeper
	ms         types.MsgServer
	ctx        sdk.Context
	gov        *govkeeper.Keeper
	humans     *stubPersonhood
	allocation *stubAllocation
	govBank    stubGovBank
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	encCfg := moduletestutil.MakeTestEncodingConfig()
	// Proposals carry their messages as Any, and x/gov unpacks them on read, so
	// any message a test puts in a proposal has to be known here — as it is in
	// the app, where every module registers its own.
	pkitypes.RegisterInterfaces(encCfg.InterfaceRegistry)
	ac := addresscodec.NewBech32Codec(sdk.GetConfig().GetBech32AccountAddrPrefix())

	govStoreKey := storetypes.NewKVStoreKey(govtypes.StoreKey)
	asmStoreKey := storetypes.NewKVStoreKey(types.StoreKey)
	cms := testutil.DefaultContextWithDB(t, govStoreKey, storetypes.NewTransientStoreKey("transient_test"))
	ctx := cms.Ctx
	cms.CMS.MountStoreWithDB(asmStoreKey, storetypes.StoreTypeIAVL, cms.DB)
	require.NoError(t, cms.CMS.LoadLatestVersion())

	govAuthority, err := ac.BytesToString(authtypes.NewModuleAddress(govtypes.ModuleName))
	require.NoError(t, err)

	govBank := stubGovBank{burned: &sdk.Coins{}}
	gov := govkeeper.NewKeeper(
		encCfg.Codec,
		runtime.NewKVStoreService(govStoreKey),
		stubAccount{ac: ac},
		govBank,
		stubGovStaking{bonded: math.NewInt(1_000_000)},
		stubDistr{},
		stubRouter{},
		govtypes.DefaultConfig(),
		govAuthority,
	)
	require.NoError(t, gov.Params.Set(ctx, v1.DefaultParams()))

	humans := &stubPersonhood{nullifiers: map[string][]byte{}, dsc: map[string][]byte{}}
	allocation := &stubAllocation{removable: map[uint64]bool{}}

	k := NewKeeper(
		runtime.NewKVStoreService(asmStoreKey),
		encCfg.Codec,
		ac,
		authtypes.NewModuleAddress(types.ModuleName),
		humans,
		gov,
		allocation,
	)

	return &testEnv{
		k:          k,
		ms:         NewMsgServerImpl(k),
		ctx:        ctx,
		gov:        gov,
		humans:     humans,
		allocation: allocation,
		govBank:    govBank,
	}
}

// addr mints a deterministic account address and registers it as one human,
// unless nullifier is empty.
func (e *testEnv) addr(t *testing.T, name, nullifier string) (sdk.AccAddress, string) {
	t.Helper()
	acc := sdk.AccAddress(authtypes.NewModuleAddress(name))
	if nullifier != "" {
		e.humans.register(acc, nullifier)
	}
	s, err := e.k.addressCodec.BytesToString(acc)
	require.NoError(t, err)
	return acc, s
}

// openProposal puts a proposal into its voting period, ending at endsAt.
//
// Built directly rather than through MsgSubmitProposal: the chamber cares about
// a proposal that is open and then closes, and going through submission would
// drag in deposits and message routing that have nothing to do with what is
// being tested.
func (e *testEnv) openProposal(t *testing.T, id uint64, endsAt time.Time) v1.Proposal {
	t.Helper()
	submit := endsAt.Add(-time.Hour)
	proposal := v1.Proposal{
		Id:              id,
		Status:          v1.StatusVotingPeriod,
		SubmitTime:      &submit,
		DepositEndTime:  &endsAt,
		VotingStartTime: &submit,
		VotingEndTime:   &endsAt,
		Title:           "a proposal",
		Summary:         "a proposal",
	}
	require.NoError(t, e.gov.SetProposal(e.ctx, proposal))
	require.NoError(t, e.gov.ActiveProposalsQueue.Set(e.ctx, collections.Join(endsAt, id), id))
	return proposal
}

// openExpedited is the same, on x/gov's fast track.
func (e *testEnv) openExpedited(t *testing.T, id uint64, endsAt time.Time) v1.Proposal {
	t.Helper()
	proposal := e.openProposal(t, id, endsAt)
	proposal.Expedited = true
	require.NoError(t, e.gov.SetProposal(e.ctx, proposal))
	return proposal
}

// voteAll casts the same vote from a set of freshly registered humans.
func (e *testEnv) voteAll(t *testing.T, id uint64, option types.VoteOption, names ...string) {
	t.Helper()
	for _, name := range names {
		_, addr := e.addr(t, name, "null-"+name)
		_, err := e.ms.VoteProposal(e.ctx, &types.MsgVoteProposal{
			Voter: addr, ProposalId: id, Option: option,
		})
		require.NoError(t, err)
	}
}

// collKeyTime builds x/gov's active-queue key.
func collKeyTime(t time.Time, id uint64) collections.Pair[time.Time, uint64] {
	return collections.Join(t, id)
}

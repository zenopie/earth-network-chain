package app

import (
	errorsmod "cosmossdk.io/errors"

	"cosmossdk.io/core/address"
	"github.com/cosmos/cosmos-sdk/baseapp"
	sdk "github.com/cosmos/cosmos-sdk/types"
	distrtypes "github.com/cosmos/cosmos-sdk/x/distribution/types"

	shieldedstakingante "github.com/earth-network/earth/x/shieldedstaking/ante"
	sstypes "github.com/earth-network/earth/x/shieldedstaking/types"
)

// OperatorRewardsRouter is the message router every module that dispatches
// msgs on someone's behalf gets in place of baseapp's: x/authz (MsgExec, from
// any route), x/gov and x/group (proposal execution) through depinject's
// interface binding (operatorRouterBinding), and the ICA host and x/wasm
// (contract CosmosMsgs, DistributionMsg and Any alike) by hand in app/ibc.go
// and app/wasm.go. It applies shieldedstakingante.CheckOperatorRewardsMsg to
// each msg before its handler runs: a validator's self-bond rewards and
// commission compound into its self-bond at the epoch end, so the operator
// cannot claim them (MsgWithdrawDelegatorReward,
// MsgWithdrawValidatorCommission) or pay them elsewhere
// (MsgSetWithdrawAddress) by any route. Since every route that executes a
// msg without the ante goes through here, nothing slips through to recover
// from; what accrues stays with x/distribution until the epoch compounds it. A plain tx reaches baseapp's own router, which is not this
// one: the ante's WithdrawAddrFilterDecorator applies the same check there.
//
// The keeper is set after depinject builds it (SetChecker); until then the
// two msgs are refused (fail closed) and every other msg passes.
type OperatorRewardsRouter struct {
	inner *baseapp.MsgServiceRouter
	ac    address.Codec
	k     shieldedstakingante.WithdrawChecker
}

var _ baseapp.MessageRouter = (*OperatorRewardsRouter)(nil)

// depinject's names for the binding that hands OperatorRewardsRouter to
// every module asking for a baseapp.MessageRouter (AppConfig).
const (
	messageRouterTypeName  = "github.com/cosmos/cosmos-sdk/baseapp/baseapp.MessageRouter"
	operatorRouterTypeName = "github.com/earth-network/earth/app/*app.OperatorRewardsRouter"
)

// ProvideOperatorRewardsRouter wraps runtime's msg router.
func ProvideOperatorRewardsRouter(msr *baseapp.MsgServiceRouter) *OperatorRewardsRouter {
	return &OperatorRewardsRouter{inner: msr}
}

// SetChecker installs x/shieldedstaking's keeper.
func (r *OperatorRewardsRouter) SetChecker(ac address.Codec, k shieldedstakingante.WithdrawChecker) {
	r.ac, r.k = ac, k
}

func (r *OperatorRewardsRouter) wrap(h baseapp.MsgServiceHandler) baseapp.MsgServiceHandler {
	if h == nil {
		return nil
	}
	return func(ctx sdk.Context, msg sdk.Msg) (*sdk.Result, error) {
		if r.k == nil {
			switch msg.(type) {
			case *distrtypes.MsgSetWithdrawAddress, *distrtypes.MsgWithdrawDelegatorReward,
				*distrtypes.MsgWithdrawValidatorCommission:
				return nil, errorsmod.Wrap(sstypes.ErrOperatorRewardClaim, "operator rewards router not wired")
			}
		} else if err := shieldedstakingante.CheckOperatorRewardsMsg(ctx, r.ac, r.k, msg); err != nil {
			return nil, err
		}
		return h(ctx, msg)
	}
}

func (r *OperatorRewardsRouter) Handler(msg sdk.Msg) baseapp.MsgServiceHandler {
	return r.wrap(r.inner.Handler(msg))
}

func (r *OperatorRewardsRouter) HandlerByTypeURL(typeURL string) baseapp.MsgServiceHandler {
	return r.wrap(r.inner.HandlerByTypeURL(typeURL))
}

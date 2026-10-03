package types

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

// PrivateActionHandler is how another module attaches an action of its own to
// a private msg: an ANML claim, a caretaker split, an assembly vote. The msg
// still pays its fee with its bundles, which the private ante runs exactly as
// it runs a MsgSend's; the handler adds the checks and proofs only its module
// can make (a membership proof against the identity tree, a passport proof),
// which the ante runs in the same pass:
//
//	charge PrivateMsgGas + PrivateActionGas
//	bundle state checks, CheckPrivateAction
//	binding signatures and action proofs, VerifyPrivateAction  (not on recheck or simulate)
//	spend, append, pay the fee; record the authorization
//
// Nothing is written until every check and both proofs have passed, so a tx
// whose action would be refused never spends its fee notes in CheckTx, and a
// block never carries an action whose proof was not verified.
//
// The action's effects are the module's msg handler's, not the ante's. The
// handler must refuse unless keeper.AuthorizedAction returns what
// CheckPrivateAction prepared for this very msg (a zero-signer msg reaches a
// handler vacuously through a contract's CosmosMsg::Any or an ICA host tx), and
// it should re-check whatever state it depends on: effects run in the msg's
// own cached context, so a handler failure reverts the action while the fee,
// paid in the ante, stays paid.
//
// Registered per msg type URL with keeper.RegisterPrivateAction, once, from
// module wiring. A PrivateMsg with no handler registered (MsgSend) has no
// action beyond its bundles. The handler releases the msg's remainders
// (Remainders) to its module with keeper.ReleaseToModule.
type PrivateActionHandler interface {
	// PrivateActionGas is the fixed gas the action costs on top of the
	// bundles', charged before any of its work.
	PrivateActionGas(ctx context.Context, msg PrivateMsg) (uint64, error)
	// CheckPrivateAction runs the action's stateful checks and returns what
	// its proofs are verified against and its handler needs. It must not
	// write.
	CheckPrivateAction(ctx context.Context, msg PrivateMsg) (any, error)
	// VerifyPrivateAction verifies the action's own proofs against what
	// CheckPrivateAction prepared. It must not write.
	VerifyPrivateAction(ctx context.Context, msg PrivateMsg, prepared any) error
}

// PrivateActionExecutor is implemented by an action handler whose action, for
// some msgs, must be atomic with the spend of its bundles. The private ante
// then runs the action itself, right after executing the bundles, in its
// own state: either every effect lands (notes spent, action done, fee paid)
// or the ante fails and nothing does.
//
// Two kinds of action need this:
//
//   - one whose outcome depends on market state the msg cannot pin (a swap's
//     output against min_out, a deposit's shares against min_shares). Run in
//     the handler, a front-run that moved the price would fail it after the
//     ante had spent the input notes, and the released value would be stuck
//     in the pool with no note for it;
//   - one paying its fee from its output (FeeFromOutputMsg). The fee does not
//     exist until the action has run, and must not depend on a handler that
//     could fail after the notes are spent.
//
// ExecutePrivateAction must pay msg's fee from output, if any, in full with
// keeper.PayFeeFromModule (the ante checks it was), and returns what the msg's
// handler reports (keeper.AuthorizedResult). The handler must then do nothing
// but return it.
//
// The price of atomicity: a tx whose action fails in DeliverTx (after
// passing CheckTx, because the state moved in between) fails in the ante and
// pays no fee, as any SDK tx failing its ante in DeliverTx does. It spends
// nothing either. max_private_actions_per_block bounds how much of a block such
// txs can take.
type PrivateActionExecutor interface {
	// ExecutesInAnte reports whether the ante runs msg's action.
	ExecutesInAnte(msg PrivateMsg) bool
	// ExecutePrivateAction runs msg's action after its bundles were spent,
	// with what CheckPrivateAction prepared.
	ExecutePrivateAction(ctx sdk.Context, msg PrivateMsg, prepared any) (any, error)
}

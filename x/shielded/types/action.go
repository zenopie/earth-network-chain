package types

import "context"

// PrivateActionHandler is how another module attaches an action of its own to
// a private msg: an ANML claim, a caretaker split, an assembly vote. The msg
// still pays its fee with its embedded transfer, which the private ante runs
// exactly as it runs a MsgTransfer's; the handler adds the checks and proofs
// only its module can make (a membership proof against the identity tree, a
// passport proof), which the ante runs in the same pass:
//
//	charge PrivateMsgGas + PrivateActionGas
//	transfer state checks, CheckPrivateAction
//	transfer proof, VerifyPrivateAction          (not on recheck or simulate)
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
// module wiring. A PrivateMsg with no handler registered (MsgTransfer) has no
// action beyond its transfer.
type PrivateActionHandler interface {
	// PrivateActionGas is the fixed gas the action costs on top of the
	// transfer's, charged before any of its work.
	PrivateActionGas(ctx context.Context, msg PrivateMsg) (uint64, error)
	// CheckPrivateAction runs the action's stateful checks and returns what
	// its proofs are verified against and its handler needs. It must not
	// write.
	CheckPrivateAction(ctx context.Context, msg PrivateMsg) (any, error)
	// VerifyPrivateAction verifies the action's own proofs against what
	// CheckPrivateAction prepared. It must not write.
	VerifyPrivateAction(ctx context.Context, msg PrivateMsg, prepared any) error
}

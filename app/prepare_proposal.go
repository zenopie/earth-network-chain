package app

import (
	"github.com/cosmos/cosmos-sdk/baseapp"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/mempool"

	abci "github.com/cometbft/cometbft/abci/types"

	shieldedkeeper "github.com/earth-network/earth/x/shielded/keeper"
	shieldedtypes "github.com/earth-network/earth/x/shielded/types"
)

// privateCapPrepareProposal wraps the SDK's default PrepareProposal (no-op
// mempool: CometBFT's txs in order, cut to the block's byte and gas limits)
// and leaves out a private tx whose actions would take the block past
// x/shielded's max_private_actions_per_block. The ante refuses such a tx in
// FinalizeBlock anyway (ErrBlockCap); proposing it would only spend block
// space on a tx certain to fail, and drop it from every mempool. Left out
// here, it stays in the proposer's mempool for a later block.
//
// The count is conservative: it includes private txs that will fail their
// ante for another reason (whose count FinalizeBlock discards). Nothing about
// validity depends on it -- ProcessProposal does not check the cap.
func privateCapPrepareProposal(sk shieldedkeeper.Keeper, dec sdk.TxDecoder) func(*baseapp.BaseApp) {
	return func(b *baseapp.BaseApp) {
		inner := baseapp.NewDefaultProposalHandler(mempool.NoOpMempool{}, b).PrepareProposalHandler()
		b.SetPrepareProposal(func(ctx sdk.Context, req *abci.RequestPrepareProposal) (*abci.ResponsePrepareProposal, error) {
			resp, err := inner(ctx, req)
			if err != nil || resp == nil {
				return resp, err
			}
			params, err := sk.Params.Get(ctx)
			if err != nil {
				return resp, nil // never fail a proposal over the filter
			}
			limit, used := uint64(params.MaxPrivateActionsPerBlock), uint64(0)
			out := make([][]byte, 0, len(resp.Txs))
			for _, bz := range resp.Txs {
				tx, err := dec(bz)
				// A tx past its timeout_height fails in this block whatever
				// it is (TxTimeoutHeightDecorator: height > timeout_height),
				// so it is left out before it can count toward the private
				// cap and crowd out a tx that would land (audit 4, L1).
				if err == nil {
					if t, ok := tx.(sdk.TxWithTimeoutHeight); ok {
						if th := t.GetTimeoutHeight(); th != 0 && req.Height > 0 && th < uint64(req.Height) {
							continue
						}
					}
				}
				if n, ok := privateActions(tx, err); ok {
					if used+n > limit {
						continue
					}
					// An anchor that has lapsed by this block's time fails the
					// tx in its ante, after it took block space and the cap
					// (audit 5 L-SH1).
					if !sk.AnchorsValidAt(ctx, tx.GetMsgs()[0].(shieldedtypes.PrivateMsg), req.Time.Unix()) {
						continue
					}
					used += n
				}
				out = append(out, bz)
			}
			resp.Txs = out
			return resp, nil
		})
	}
}

// privateActions is the action count of a well-formed private tx (exactly
// one msg, a PrivateMsg), as the ante counts it.
func privateActions(tx sdk.Tx, err error) (uint64, bool) {
	if err != nil {
		return 0, false
	}
	msgs := tx.GetMsgs()
	if len(msgs) != 1 {
		return 0, false
	}
	pm, ok := msgs[0].(shieldedtypes.PrivateMsg)
	if !ok {
		return 0, false
	}
	return uint64(shieldedtypes.ActionCount(pm)), true
}

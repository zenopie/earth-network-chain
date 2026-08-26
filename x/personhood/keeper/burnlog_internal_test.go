package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

// burnLog stands in for x/earth's burn counters. The keeper only ever adds to
// them, so recording per source is enough to assert what a burn path destroyed.
type burnLog struct {
	bySource map[string]sdk.Coins
}

func (b *burnLog) RecordBurn(_ context.Context, source string, coins sdk.Coins) error {
	if b.bySource == nil {
		b.bySource = map[string]sdk.Coins{}
	}
	b.bySource[source] = b.bySource[source].Add(coins...)
	return nil
}

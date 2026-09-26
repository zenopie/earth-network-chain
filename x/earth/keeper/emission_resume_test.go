package keeper

import (
	"testing"
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

// An export from a month before the relaunch used to mint the month in block one.
func TestResumeClockSkipsTheGapBeforeGenesis(t *testing.T) {
	genesis := time.Unix(1_800_000_000, 0)
	ctx := sdk.Context{}.WithBlockTime(genesis)

	exported := genesis.Add(-30 * 24 * time.Hour).UnixNano()
	if got := resumeClock(ctx, exported); got != genesis.UnixNano() {
		t.Fatalf("resumeClock = %d, want the genesis time %d", got, genesis.UnixNano())
	}
	if got := resumeClock(ctx, 0); got != 0 {
		t.Fatalf("never minted must stay 0, got %d", got)
	}
	later := genesis.Add(time.Hour).UnixNano()
	if got := resumeClock(ctx, later); got != later {
		t.Fatalf("a clock ahead of genesis is left alone, got %d", got)
	}
}

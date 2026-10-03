package types_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/earth-network/earth/x/personhood/types"
)

// Audit 4 C5: window <= max trade <= accrual is checked on the values in
// force, so a window raised past the default one-hour trade cap (trade left
// at zero) is refused, as is a window past the accrual cap.
func TestAudit4BuybackWindowValidatedAgainstEffectiveTradeCap(t *testing.T) {
	p := types.DefaultParams()
	require.NoError(t, p.Validate())

	p.BuybackMaxTradeSeconds = 0
	p.BuybackTwapWindowSeconds = 2 * 3600
	require.ErrorContains(t, p.Validate(), "buyback_max_trade_seconds")

	p = types.DefaultParams()
	p.BuybackTwapWindowSeconds = 2 * 86400
	p.BuybackMaxTradeSeconds = 2 * 86400
	require.ErrorContains(t, p.Validate(), "at most buyback_max_accrual_seconds")

	p = types.DefaultParams()
	p.BuybackTwapWindowSeconds = 2 * 3600
	p.BuybackMaxTradeSeconds = 3 * 3600
	require.NoError(t, p.Validate())
}

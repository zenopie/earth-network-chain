package ante_test

import (
	"testing"

	storetypes "cosmossdk.io/store/types"
	"github.com/cosmos/cosmos-sdk/testutil"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/stretchr/testify/require"

	"github.com/earth-network/earth/x/shielded/ante"
)

// Audit 4 I3: a panic below RecoverDecorator is an error returned with the
// context it was handed, so the gas already charged on its meter is reported
// (and consumed from the block) like any failed ante's; out-of-gas still
// panics for SetUpContextDecorator to convert.
func TestAudit4RecoverDecoratorKeepsGas(t *testing.T) {
	key := storetypes.NewKVStoreKey("t")
	ctx := testutil.DefaultContextWithDB(t, key, storetypes.NewTransientStoreKey("tt")).Ctx.
		WithGasMeter(storetypes.NewGasMeter(1_000_000))
	h := sdk.ChainAnteDecorators(ante.RecoverDecorator{}, panicking{})
	out, err := h(ctx, nil, false)
	require.ErrorIs(t, err, sdkerrors.ErrPanic)
	require.Equal(t, uint64(400_000), out.GasMeter().GasConsumed())

	oog := sdk.ChainAnteDecorators(ante.RecoverDecorator{}, panicking{oog: true})
	require.Panics(t, func() { _, _ = oog(ctx.WithGasMeter(storetypes.NewGasMeter(1_000_000)), nil, false) })
}

type panicking struct{ oog bool }

func (p panicking) AnteHandle(ctx sdk.Context, _ sdk.Tx, _ bool, _ sdk.AnteHandler) (sdk.Context, error) {
	ctx.GasMeter().ConsumeGas(400_000, "proofs")
	if p.oog {
		panic(storetypes.ErrorOutOfGas{Descriptor: "x"})
	}
	panic("verifier blew up")
}

package app

import (
	"errors"
	"fmt"

	"cosmossdk.io/core/address"
	corestoretypes "cosmossdk.io/core/store"
	circuitante "cosmossdk.io/x/circuit/ante"
	circuitkeeper "cosmossdk.io/x/circuit/keeper"

	wasmkeeper "github.com/CosmWasm/wasmd/x/wasm/keeper"
	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	"github.com/cosmos/cosmos-sdk/runtime"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/auth/ante"
	ibcante "github.com/cosmos/ibc-go/v10/modules/core/ante"
	ibckeeper "github.com/cosmos/ibc-go/v10/modules/core/keeper"

	shieldedante "github.com/earth-network/earth/x/shielded/ante"
	shieldedkeeper "github.com/earth-network/earth/x/shielded/keeper"
	shieldedstakingante "github.com/earth-network/earth/x/shieldedstaking/ante"
)

// HandlerOptions extends the SDK's ante options with what x/wasm, x/circuit and
// IBC need.
type HandlerOptions struct {
	ante.HandlerOptions

	IBCKeeper             *ibckeeper.Keeper
	CircuitKeeper         *circuitkeeper.Keeper
	WasmKeeper            *wasmkeeper.Keeper
	WasmNodeConfig        *wasmtypes.NodeConfig
	TXCounterStoreService corestoretypes.KVStoreService
	ShieldedKeeper        *shieldedkeeper.Keeper
	StakingValidatorCodec address.Codec
	// WithdrawChecker refuses a validator operator's withdraw address
	// elsewhere (x/shieldedstaking's keeper).
	WithdrawChecker shieldedstakingante.WithdrawChecker
}

// NewAnteHandler builds this chain's ante chain.
//
// Until x/wasm arrived the app used whatever x/auth/tx/config's depinject
// provider built, which is ante.NewAnteHandler with default options. Contracts
// make that insufficient: three of the decorators below feed the contract
// runtime state it cannot obtain any other way, and one of them is what stops a
// simulated query from running forever. The order here is wasmd's, and order is
// consensus — moving a decorator is a state-machine change.
//
// Two decorators are new to this chain rather than required by wasm, and are
// here because their absence was a latent bug:
//
//   - The circuit breaker. x/circuit is in the module list and its gov messages
//     work, but without this decorator nothing consults the tripped-message set,
//     so "disable this message type" silently did nothing. The one lever the
//     chain has for halting a misbehaving module during an incident was wired to
//     a switch that was not connected.
//   - The redundant relay filter. It refuses IBC packets that another relayer
//     already delivered, so a losing relayer pays no fee for the duplicate.
//     Standard on every IBC chain; without it relaying against earth is more
//     expensive than relaying against anyone else, which shows up as nobody
//     relaying.
//
// Matching the SDK default matters in one more place: the auth module config
// sets EnableUnorderedTransactions, and in SDK v0.53 unordered support lives
// inside NewSigVerificationDecorator rather than in a decorator of its own. It
// survives here because that decorator is still in the chain with default
// options; drop it or pass SigVerifyOptions and unordered txs break.
func NewAnteHandler(options HandlerOptions) (sdk.AnteHandler, error) {
	if options.AccountKeeper == nil {
		return nil, errors.New("account keeper is required for ante builder")
	}
	if options.BankKeeper == nil {
		return nil, errors.New("bank keeper is required for ante builder")
	}
	if options.SignModeHandler == nil {
		return nil, errors.New("sign mode handler is required for ante builder")
	}
	if options.IBCKeeper == nil {
		return nil, errors.New("IBC keeper is required for ante builder")
	}
	if options.CircuitKeeper == nil {
		return nil, errors.New("circuit keeper is required for ante builder")
	}
	if options.WasmKeeper == nil {
		return nil, errors.New("wasm keeper is required for ante builder")
	}
	if options.WasmNodeConfig == nil {
		return nil, errors.New("wasm node config is required for ante builder")
	}
	if options.TXCounterStoreService == nil {
		return nil, errors.New("wasm tx counter store service is required for ante builder")
	}
	if options.StakingValidatorCodec == nil {
		return nil, errors.New("staking validator address codec is required for ante builder")
	}
	if options.ShieldedKeeper == nil {
		return nil, errors.New("shielded keeper is required for ante builder")
	}
	if options.WithdrawChecker == nil {
		return nil, errors.New("withdraw checker is required for ante builder")
	}
	sk := *options.ShieldedKeeper

	anteDecorators := []sdk.AnteDecorator{
		ante.NewSetUpContextDecorator(), // outermost: must run first, it installs the gas meter
		// Caps gas for simulation only. A contract query has no fee paying for
		// it, so without this a `wasmd query wasm contract-state smart` against
		// a deliberately non-terminating contract runs until the node dies.
		wasmkeeper.NewLimitSimulationGasDecorator(options.WasmNodeConfig.SimulationGasLimit),
		// Puts the tx's position in the block into the context. Contracts read
		// it to build unique ids that do not collide within a block.
		wasmkeeper.NewCountTXDecorator(options.TXCounterStoreService),
		wasmkeeper.NewGasRegisterDecorator(options.WasmKeeper.GetGasRegister()),
		wasmkeeper.NewTxContractsDecorator(),
		circuitante.NewCircuitBreakerDecorator(options.CircuitKeeper),
		ante.NewExtensionOptionsDecorator(options.ExtensionOptionChecker),
		ante.NewValidateBasicDecorator(),
		// Staking is private: transparent delegation is refused except a
		// validator's self-bond. The staking hook enforces it everywhere; this
		// refuses a plain tx before it pays a fee for nothing.
		shieldedstakingante.StakingMsgFilterDecorator{
			AddressCodec: options.AccountKeeper.AddressCodec(), ValidatorCodec: options.StakingValidatorCodec,
		},
		// A validator's self-bond rewards and commission compound: no claim
		// (MsgWithdrawDelegatorReward/ValidatorCommission) and no withdraw
		// address elsewhere. app/operator_router.go covers the other routes.
		shieldedstakingante.WithdrawAddrFilterDecorator{
			AddressCodec: options.AccountKeeper.AddressCodec(), K: options.WithdrawChecker,
		},
		ante.NewTxTimeoutHeightDecorator(),
		ante.NewValidateMemoDecorator(options.AccountKeeper),
		ante.NewConsumeGasForTxSizeDecorator(options.AccountKeeper),
		// ANML exists only in the shielded pool; it cannot pay a fee.
		shieldedante.RejectFeeDenomsDecorator{Denoms: sk.ShieldedOnlyDenoms()},
		ante.NewDeductFeeDecorator(options.AccountKeeper, options.BankKeeper, options.FeegrantKeeper, options.TxFeeChecker),
		ante.NewSetPubKeyDecorator(options.AccountKeeper), // must precede every signature verification decorator
		ante.NewValidateSigCountDecorator(options.AccountKeeper),
		ante.NewSigGasConsumeDecorator(options.AccountKeeper, options.SigGasConsumer),
		ante.NewSigVerificationDecorator(options.AccountKeeper, options.SignModeHandler),
		ante.NewIncrementSequenceDecorator(options.AccountKeeper),
		ibcante.NewRedundantRelayDecorator(options.IBCKeeper),
	}

	normal := sdk.ChainAnteDecorators(anteDecorators...)

	// Unsigned private txs: one PrivateMsg, no signatures, fee paid from the
	// shielded pool by the msg's bundles. See x/shielded/ante for why
	// each SDK decorator missing here cannot run on a tx with no account.
	//
	// NOTE: nodes must run the no-op app mempool (app.toml mempool.max-txs =
	// -1, the default). The SDK's priority and sender-nonce mempools key txs by
	// signer and sequence and reject a tx with no signers outright.
	private := sdk.ChainAnteDecorators(
		ante.NewSetUpContextDecorator(),
		wasmkeeper.NewLimitSimulationGasDecorator(options.WasmNodeConfig.SimulationGasLimit),
		circuitante.NewCircuitBreakerDecorator(options.CircuitKeeper),
		shieldedante.ValidateTxDecorator{},
		ante.NewTxTimeoutHeightDecorator(),
		ante.NewValidateMemoDecorator(options.AccountKeeper),
		ante.NewConsumeGasForTxSizeDecorator(options.AccountKeeper),
		shieldedante.PrivateMsgDecorator{K: sk},
	)
	return shieldedante.NewRouter(normal, private), nil
}

// setAnteHandler replaces the ante handler that x/auth/tx/config installed as a
// baseapp option during Build. Safe to call afterwards and before Load: baseapp
// only reads the handler when it first runs a transaction.
func (app *App) setAnteHandler() error {
	anteHandler, err := NewAnteHandler(HandlerOptions{
		HandlerOptions: ante.HandlerOptions{
			AccountKeeper:   app.AuthKeeper,
			BankKeeper:      app.BankKeeper,
			SignModeHandler: app.txConfig.SignModeHandler(),
			FeegrantKeeper:  app.FeeGrantKeeper,
			SigGasConsumer:  ante.DefaultSigVerificationGasConsumer,
		},
		IBCKeeper:             app.IBCKeeper,
		CircuitKeeper:         &app.CircuitBreakerKeeper,
		WasmKeeper:            &app.WasmKeeper,
		WasmNodeConfig:        &app.WasmNodeConfig,
		TXCounterStoreService: runtime.NewKVStoreService(app.GetKey(wasmtypes.StoreKey)),
		ShieldedKeeper:        &app.ShieldedKeeper,
		StakingValidatorCodec: app.StakingKeeper.ValidatorAddressCodec(),
		WithdrawChecker:       app.ShieldedStakingKeeper,
	})
	if err != nil {
		return fmt.Errorf("building ante handler: %w", err)
	}

	app.SetAnteHandler(anteHandler)
	return nil
}

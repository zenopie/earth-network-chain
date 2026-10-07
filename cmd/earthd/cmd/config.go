package cmd

import (
	"strings"

	"github.com/earth-network/earth/app"

	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	cmtcfg "github.com/cometbft/cometbft/config"
	serverconfig "github.com/cosmos/cosmos-sdk/server/config"
)

// initCometBFTConfig helps to override default CometBFT Config values.
// return cmtcfg.DefaultConfig if no custom configuration is required for the application.
func initCometBFTConfig() *cmtcfg.Config {
	return cmtcfg.DefaultConfig()
}

// initAppConfig helps to override default appConfig template and configs.
// return "", nil if no custom configuration is required for the application.
func initAppConfig() (string, interface{}) {
	// The [wasm] block is node-local, not consensus: query_gas_limit,
	// memory_cache_size and contract debug logging may differ between nodes
	// without forking the chain. simulation_gas_limit is the one worth knowing
	// about: it is what stops a simulated call to a non-terminating contract
	// from pinning a core. Left unset, wasmd would fall back to the block gas
	// limit (100M, ~15 s of contract CPU per simulate); Earth's ante falls
	// back to app.DefaultSimulationGasLimit instead (app/ante.go), and the
	// template writes that value so app.toml shows it.
	type CustomAppConfig struct {
		serverconfig.Config `mapstructure:",squash"`

		Wasm wasmtypes.NodeConfig `mapstructure:"wasm"`
	}

	// MinGasPrices is left empty: every validator sets its own in app.toml
	// (the node refuses to start without one).
	srvCfg := serverconfig.DefaultConfig()

	// The app mempool must be the no-op one (mempool.max-txs = -1, the SDK
	// default, pinned here so the template every node writes says so).
	// Private txs are unsigned, and the SDK's priority and sender-nonce
	// mempools key txs by signer and sequence and refuse any tx with none:
	// a node running one would drop every private tx. See app/ante.go and
	// docker/entrypoint.sh, which forces it on every start.
	srvCfg.Mempool.MaxTxs = -1

	wasmCfg := wasmtypes.DefaultNodeConfig()
	simLimit := app.DefaultSimulationGasLimit
	wasmCfg.SimulationGasLimit = &simLimit

	customAppConfig := CustomAppConfig{
		Config: *srvCfg,
		Wasm:   wasmCfg,
	}

	customAppTemplate := strings.Replace(serverconfig.DefaultConfigTemplate,
		"max-txs = {{ .Mempool.MaxTxs }}",
		"# EARTH: keep -1. Private (shielded) txs are unsigned, and the SDK's app-side\n"+
			"# mempools refuse any tx with no signer, so any other value drops every\n"+
			"# private tx. The container entrypoint forces -1 on every start.\n"+
			"max-txs = {{ .Mempool.MaxTxs }}", 1) + wasmtypes.ConfigTemplate(wasmCfg)

	return customAppTemplate, customAppConfig
}

package cmd

import (
	"strings"

	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	cmtcfg "github.com/cometbft/cometbft/config"
	serverconfig "github.com/cosmos/cosmos-sdk/server/config"
)

// initCometBFTConfig helps to override default CometBFT Config values.
// return cmtcfg.DefaultConfig if no custom configuration is required for the application.
func initCometBFTConfig() *cmtcfg.Config {
	cfg := cmtcfg.DefaultConfig()

	// these values put a higher strain on node memory
	// cfg.P2P.MaxNumInboundPeers = 100
	// cfg.P2P.MaxNumOutboundPeers = 40

	return cfg
}

// initAppConfig helps to override default appConfig template and configs.
// return "", nil if no custom configuration is required for the application.
func initAppConfig() (string, interface{}) {
	// The [wasm] block is node-local, not consensus: query_gas_limit,
	// memory_cache_size and contract debug logging may differ between nodes
	// without forking the chain. simulation_gas_limit is the one worth knowing
	// about — left unset it falls back to the block gas limit, which is what
	// stops a simulated call to a non-terminating contract from pinning a core.
	type CustomAppConfig struct {
		serverconfig.Config `mapstructure:",squash"`

		Wasm wasmtypes.NodeConfig `mapstructure:"wasm"`
	}

	// Optionally allow the chain developer to overwrite the SDK's default
	// server config.
	srvCfg := serverconfig.DefaultConfig()
	// The SDK's default minimum gas price is set to "" (empty value) inside
	// app.toml. If left empty by validators, the node will halt on startup.
	// However, the chain developer can set a default app.toml value for their
	// validators here.
	//
	// In summary:
	// - if you leave srvCfg.MinGasPrices = "", all validators MUST tweak their
	//   own app.toml config,
	// - if you set srvCfg.MinGasPrices non-empty, validators CAN tweak their
	//   own app.toml to override, or use this default value.
	//
	// In tests, we set the min gas prices to 0.
	// srvCfg.MinGasPrices = "0stake"

	// The app mempool must be the no-op one (mempool.max-txs = -1, the SDK
	// default, pinned here so the template every node writes says so).
	// Private txs are unsigned, and the SDK's priority and sender-nonce
	// mempools key txs by signer and sequence and refuse any tx with none:
	// a node running one would drop every private tx. See app/ante.go and
	// docker/entrypoint.sh, which forces it on every start.
	srvCfg.Mempool.MaxTxs = -1

	customAppConfig := CustomAppConfig{
		Config: *srvCfg,
		Wasm:   wasmtypes.DefaultNodeConfig(),
	}

	customAppTemplate := strings.Replace(serverconfig.DefaultConfigTemplate,
		"max-txs = {{ .Mempool.MaxTxs }}",
		"# EARTH: keep -1. Private (shielded) txs are unsigned, and the SDK's app-side\n"+
			"# mempools refuse any tx with no signer, so any other value drops every\n"+
			"# private tx. The container entrypoint forces -1 on every start.\n"+
			"max-txs = {{ .Mempool.MaxTxs }}", 1) + wasmtypes.DefaultConfigTemplate()

	return customAppTemplate, customAppConfig
}

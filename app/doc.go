// Package app wires the earth chain: the Cosmos SDK app (depinject config in
// app_config.go), the private-tx ante router (unsigned shielded txs go
// through x/shielded/ante), the operator-route guards, CosmWasm, IBC,
// governance upgrades and genesis export. Most end-to-end tests live here: they
// drive a deterministic chain and verify committed proof fixtures.
package app

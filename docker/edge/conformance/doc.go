// Package conformance checks earth-edge's filter against the code it stands
// in front of: CometBFT v0.38.21's RPC argument decoding (cometbft_test.go)
// and grpc-gateway v1.16.0's route matching over every GET route the chain's
// and the SDK's protos annotate (gateway_test.go). A separate module so the
// proxy itself depends on nothing but the standard library; the versions
// here must match the chain's go.mod.
package conformance

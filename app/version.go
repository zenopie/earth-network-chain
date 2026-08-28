package app

// AppVersion is the consensus app version this binary implements.
//
// It is the same number as networks/genesis/chain.json's app_version, and the
// two must move together at every coordinated upgrade. See the
// SetProtocolVersion call in app.go for what breaks when they disagree, and
// TestAppVersionMatchesGenesis for the check that catches it.
const AppVersion uint64 = 1

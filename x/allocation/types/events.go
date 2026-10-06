package types

// Lease events, for alerting (ORCHARD_DESIGN.md section 9, Leases).
// None of them halts anything; each one means weight is counting, or
// emission is moving, other than the lease rule says.
const (
	// EventTypeLeaseRetireFailed: a lease due at expires_at could not be
	// retired. It keeps its weight (and its options keep earning the
	// stream's emission on it) until retry_at, a day later, and is tried
	// again then, for ever. Alert on any occurrence: a deterministic failure
	// repeats daily and the weight never leaves.
	EventTypeLeaseRetireFailed = "lease_retire_failed"
	// EventTypeLeaseSettleHeld: a settle other than the BeginBlock sweep
	// found a lease due and stopped the index at expires_at. Should never
	// occur (only a module settling Groundworks in BeginBlock before
	// x/allocation could cause it); alert on any occurrence.
	EventTypeLeaseSettleHeld = "lease_settle_held"
	// EventTypeLeaseBacklogDrained: one BeginBlock sweep walked more than
	// LapseBacklogAlert lapse seconds (lapse_seconds), i.e. drained the
	// backlog of a halt in one block. Exact, but a slow block; informational.
	EventTypeLeaseBacklogDrained = "lease_backlog_drained"

	AttributeKeyStream       = "stream"
	AttributeKeyLapser       = "lapser"
	AttributeKeyKey          = "key"
	AttributeKeyExpiresAt    = "expires_at"
	AttributeKeyRetryAt      = "retry_at"
	AttributeKeyError        = "error"
	AttributeKeyLapseSeconds = "lapse_seconds"
)

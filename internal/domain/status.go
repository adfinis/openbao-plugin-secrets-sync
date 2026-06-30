// Package domain contains stable secret sync domain types.
package domain

// SyncState is the stable external status state for a sync object.
type SyncState string

const (
	SyncStateUnknown       SyncState = "UNKNOWN"
	SyncStatePending       SyncState = "PENDING"
	SyncStateSynced        SyncState = "SYNCED"
	SyncStateDrifted       SyncState = "DRIFTED"
	SyncStateRemoteMissing SyncState = "REMOTE_MISSING"
	SyncStateDisabled      SyncState = "DISABLED"
	SyncStateInternalError SyncState = "INTERNAL_ERROR"
)

// Package domain contains stable secret sync domain types.
package domain

// SyncState is the stable external status state for a sync object.
type SyncState string

const (
	SyncStateUnknown                SyncState = "UNKNOWN"
	SyncStateNoAssociation          SyncState = "NO_ASSOCIATION"
	SyncStatePending                SyncState = "PENDING"
	SyncStateSynced                 SyncState = "SYNCED"
	SyncStateDrifted                SyncState = "DRIFTED"
	SyncStateRemoteMissing          SyncState = "REMOTE_MISSING"
	SyncStateRemoteOwnershipLost    SyncState = "REMOTE_OWNERSHIP_LOST"
	SyncStateDestinationAuthError   SyncState = "DESTINATION_AUTH_ERROR"
	SyncStateDestinationPolicyError SyncState = "DESTINATION_POLICY_ERROR"
	SyncStateDestinationRateLimited SyncState = "DESTINATION_RATE_LIMITED"
	SyncStateDestinationUnavailable SyncState = "DESTINATION_UNAVAILABLE"
	SyncStateValidationError        SyncState = "VALIDATION_ERROR"
	SyncStateQueueBlocked           SyncState = "QUEUE_BLOCKED"
	SyncStateDisabled               SyncState = "DISABLED"
	SyncStateInternalError          SyncState = "INTERNAL_ERROR"
)

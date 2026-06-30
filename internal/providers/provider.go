// Package providers defines the destination provider contract.
package providers

import "context"

// Provider adapts the core sync engine to one destination type.
type Provider interface {
	Type() string
	Capabilities() Capabilities
	Validate(context.Context, DestinationConfig) error
	Plan(context.Context, PlanRequest) (*PlanResult, error)
	Upsert(context.Context, UpsertRequest) (*SyncResult, error)
	Delete(context.Context, DeleteRequest) (*SyncResult, error)
	ReadState(context.Context, ReadStateRequest) (*RemoteState, error)
	Health(context.Context, DestinationConfig) (*HealthResult, error)
}

// Capabilities declares destination behavior the core engine may rely on.
type Capabilities struct {
	SupportsValueReadback       bool
	SupportsMetadataReadback    bool
	SupportsPayloadHashMetadata bool
	SupportsUpdateIfOwned       bool
	SupportsDeleteIfOwned       bool
	SupportsSecretPath          bool
	SupportsSecretKey           bool
	MaxPayloadBytes             int
}

// DestinationConfig is provider-specific destination configuration.
type DestinationConfig struct {
	Name   string
	Config map[string]string
}

// ErrorClass is a stable class for provider and provider-boundary failures.
type ErrorClass string

const (
	PlanActionCreate   = "create"
	PlanActionUpdate   = "update"
	PlanActionNoop     = "noop"
	PlanActionConflict = "conflict"
	PlanActionBlocked  = "blocked"
)

const (
	ErrorClassValidation  ErrorClass = "validation"
	ErrorClassAuthn       ErrorClass = "authn"
	ErrorClassAuthz       ErrorClass = "authz"
	ErrorClassRateLimit   ErrorClass = "rate_limit"
	ErrorClassUnavailable ErrorClass = "unavailable"
	ErrorClassCollision   ErrorClass = "collision"
	ErrorClassOwnership   ErrorClass = "ownership"
	ErrorClassDrift       ErrorClass = "drift"
	ErrorClassCapacity    ErrorClass = "capacity"
	ErrorClassInternal    ErrorClass = "internal"
)

// Error carries a stable class without forcing providers to expose raw API errors.
type Error struct {
	Class   ErrorClass
	Message string
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Message == "" {
		return string(e.Class)
	}
	return e.Message
}

// PlanRequest describes a dry-run provider operation.
type PlanRequest struct {
	Destination   DestinationConfig
	ResolvedName  string
	Format        string
	PayloadSHA256 string
	PayloadBytes  int
	SourcePath    string
	SourceVersion int
	AssociationID string
	ObjectID      string
}

// PlanResult describes the provider action that would be taken.
type PlanResult struct {
	Action     string
	Message    string
	ErrorClass ErrorClass
}

// UpsertRequest describes a remote create or update operation.
type UpsertRequest struct {
	Destination   DestinationConfig
	ResolvedName  string
	Format        string
	Payload       []byte
	PayloadSHA256 string
	SourcePath    string
	SourceVersion int
	AssociationID string
	ObjectID      string
}

// DeleteRequest describes a remote delete operation.
type DeleteRequest struct {
	Destination   DestinationConfig
	ResolvedName  string
	SourcePath    string
	SourceVersion int
	AssociationID string
	ObjectID      string
}

// ReadStateRequest describes a remote state lookup.
type ReadStateRequest struct {
	Destination   DestinationConfig
	ResolvedName  string
	PayloadSHA256 string
	SourcePath    string
	SourceVersion int
	AssociationID string
	ObjectID      string
}

// RemoteState is the provider's view of one remote object.
type RemoteState struct {
	Exists         bool
	OwnershipKnown bool
	Owned          bool
	PayloadSHA256  string
	SourceVersion  int
	RemoteVersion  string
}

// SyncResult describes the result of one remote mutation.
type SyncResult struct {
	RemoteVersion string
}

// HealthResult describes destination health.
type HealthResult struct {
	Healthy    bool
	Message    string
	ErrorClass ErrorClass
}

// Package providers defines the destination provider contract.
package providers

import "context"

// Provider adapts the core sync engine to one destination type.
type Provider interface {
	// Type returns the stable provider type used in storage, API paths, and policy.
	Type() string
	// Capabilities describes which sync shapes and safety checks this provider supports.
	Capabilities() Capabilities
	// ValidateConfig validates a destination config without opening a long-lived runtime.
	// Provider-specific validation failures should return *Error with a stable class.
	ValidateConfig(context.Context, DestinationConfig) error
	// NormalizeAssociationConfig validates provider-specific association settings,
	// applies stable defaults, and returns the opaque provider identity component
	// used by the core for association identity and remote-name reservations.
	NormalizeAssociationConfig(context.Context, DestinationConfig, AssociationConfig) (AssociationConfig, error)
	// OpenDestination builds a configured runtime for one destination.
	// Returning a nil runtime with nil error violates the provider contract.
	OpenDestination(context.Context, DestinationConfig) (DestinationRuntime, error)
}

// DestinationRuntime is a configured provider destination ready for operations.
type DestinationRuntime interface {
	// Health checks whether the configured destination can be used. It must not mutate
	// remote state; unhealthy destinations should return HealthResult with ErrorClass set.
	Health(context.Context) (*HealthResult, error)
	// Plan reports the action a later Upsert or Delete would take without mutating remote
	// state. Expected remote conflicts, validation failures, ownership mismatches, and
	// unavailable dependencies should be represented as PlanResult values, usually with
	// Action set to PlanActionConflict or PlanActionBlocked and ErrorClass set. Reserve
	// non-nil errors for failures that prevent producing a provider plan at all.
	Plan(context.Context, PlanRequest) (*PlanResult, error)
	// Upsert creates or updates the remote object. Provider-side failures must return
	// *Error so the core can classify retry, status, and operator diagnostics.
	Upsert(context.Context, UpsertRequest) (*SyncResult, error)
	// Delete removes or detaches the remote object according to provider semantics.
	// Provider-side failures must return *Error so the core can classify the result.
	Delete(context.Context, DeleteRequest) (*SyncResult, error)
	// ReadState reports the provider's current view of a remote object without mutating it.
	// Provider-side failures must return *Error; a missing object is RemoteState.Exists=false.
	ReadState(context.Context, ReadStateRequest) (*RemoteState, error)
	// Close releases runtime resources and must be safe to call more than once.
	Close(context.Context) error
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
	SupportsDataMap             bool
	MaxPayloadBytes             int
}

// DestinationConfig is provider-specific destination configuration.
type DestinationConfig struct {
	Name   string
	Config map[string]string
}

// AssociationConfig is provider-specific configuration applied to every remote
// object produced by one association. Identity must be stable, non-sensitive,
// and derived only by the provider during normalization.
type AssociationConfig struct {
	Config   map[string]string
	Identity string
}

// RuntimeIdentity identifies the OpenBao mount instance that produced a provider request.
type RuntimeIdentity struct {
	PluginInstanceID string
	RestoreEpoch     string
}

// RequestIdentity identifies the OpenBao association that owns a provider object.
type RequestIdentity struct {
	AssociationID    string
	SourcePath       string
	ObjectID         string
	PluginInstanceID string
	RestoreEpoch     string
}

// Complete reports whether the required ownership fields are present.
func (i RequestIdentity) Complete() bool {
	return i.AssociationID != "" && i.SourcePath != "" && i.ObjectID != ""
}

// ErrorClass is a stable class for provider and provider-boundary failures.
type ErrorClass string

const (
	// PlanActionCreate means the remote object does not exist and would be created.
	PlanActionCreate = "create"
	// PlanActionUpdate means the remote object exists and would be updated.
	PlanActionUpdate = "update"
	// PlanActionNoop means the remote object already matches desired state.
	PlanActionNoop = "noop"
	// PlanActionConflict means a remote object exists but is not owned by this plugin.
	PlanActionConflict = "conflict"
	// PlanActionBlocked means the provider cannot safely produce a mutating operation.
	PlanActionBlocked = "blocked"
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

const (
	// RemoteStateVerificationValue means the provider compared remote payload bytes.
	RemoteStateVerificationValue = "value"
	// RemoteStateVerificationMetadata means the provider compared provider metadata only.
	RemoteStateVerificationMetadata = "metadata"
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
	Runtime       RuntimeIdentity
	Association   AssociationConfig
	ResolvedName  string
	Format        string
	PayloadSHA256 string
	PayloadBytes  int
	DataMap       bool
	DataMapKeys   []string
	SourcePath    string
	SourceVersion int
	AssociationID string
	ObjectID      string
}

// OwnershipIdentity returns the association identity used for provider ownership checks.
func (r PlanRequest) OwnershipIdentity() RequestIdentity {
	return requestIdentity(r.Runtime, r.AssociationID, r.SourcePath, r.ObjectID)
}

// PlanResult describes the provider action that would be taken.
type PlanResult struct {
	Action     string
	Message    string
	ErrorClass ErrorClass
}

// UpsertRequest describes a remote create or update operation.
type UpsertRequest struct {
	Runtime        RuntimeIdentity
	Association    AssociationConfig
	ResolvedName   string
	Format         string
	Payload        []byte
	PayloadSHA256  string
	IdempotencyKey string
	DataMap        map[string][]byte
	SourcePath     string
	SourceVersion  int
	AssociationID  string
	ObjectID       string
}

// OwnershipIdentity returns the association identity used for provider ownership checks.
func (r UpsertRequest) OwnershipIdentity() RequestIdentity {
	return requestIdentity(r.Runtime, r.AssociationID, r.SourcePath, r.ObjectID)
}

// DeleteRequest describes a remote delete operation.
type DeleteRequest struct {
	Runtime        RuntimeIdentity
	Association    AssociationConfig
	ResolvedName   string
	IdempotencyKey string
	DataMap        bool
	SourcePath     string
	SourceVersion  int
	AssociationID  string
	ObjectID       string
}

// OwnershipIdentity returns the association identity used for provider ownership checks.
func (r DeleteRequest) OwnershipIdentity() RequestIdentity {
	return requestIdentity(r.Runtime, r.AssociationID, r.SourcePath, r.ObjectID)
}

// ReadStateRequest describes a remote state lookup.
type ReadStateRequest struct {
	Runtime       RuntimeIdentity
	Association   AssociationConfig
	ResolvedName  string
	PayloadSHA256 string
	DataMap       bool
	SourcePath    string
	SourceVersion int
	AssociationID string
	ObjectID      string
}

// OwnershipIdentity returns the association identity used for provider ownership checks.
func (r ReadStateRequest) OwnershipIdentity() RequestIdentity {
	return requestIdentity(r.Runtime, r.AssociationID, r.SourcePath, r.ObjectID)
}

func requestIdentity(
	runtime RuntimeIdentity,
	associationID string,
	sourcePath string,
	objectID string,
) RequestIdentity {
	return RequestIdentity{
		AssociationID:    associationID,
		SourcePath:       sourcePath,
		ObjectID:         objectID,
		PluginInstanceID: runtime.PluginInstanceID,
		RestoreEpoch:     runtime.RestoreEpoch,
	}
}

// RemoteState is the provider's view of one remote object.
type RemoteState struct {
	Exists         bool
	OwnershipKnown bool
	Owned          bool
	PayloadSHA256  string
	SourceVersion  int
	RemoteVersion  string
	Verification   string
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

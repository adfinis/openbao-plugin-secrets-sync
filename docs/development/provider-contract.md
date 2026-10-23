---
verifiedBy:
  - internal/providers/provider.go
  - internal/providers/registry.go
  - internal/providers/transport_error.go
  - internal/providers/providertest/provider.go
  - internal/providers/awssecretsmanager/provider.go
  - internal/providers/kubernetessecrets/provider.go
  - internal/providers/gitlab/project_variables.go
  - internal/providers/fake/fake.go
  - internal/payload/payload.go
  - internal/backend/backend.go
  - internal/backend/provider_runtime_cache.go
  - internal/backend/path_associations.go
  - internal/backend/path_destinations.go
  - internal/backend/path_reconcile.go
  - internal/backend/dispatcher.go
---

# Provider contract

Providers adapt the core sync engine to destination APIs. They are Go packages
compiled into the plugin binary, not separate plugin processes.

The core engine owns OpenBao request handling, storage, authorization shape,
association validation, payload construction and hashing, queueing, retry
decisions, status, redaction, and safety policy. Providers own destination
config validation, client construction, remote state inspection, idempotent
mutation, ownership metadata, and error classification.

Providers must:

- receive only resolved destination config and prepared payloads;
- never receive OpenBao request objects, read OpenBao storage, or make OpenBao
  policy decisions;
- classify every failure into a stable error class;
- never log or return secret values, credentials, tokens, or raw provider
  responses that can contain secret material;
- honor context cancellation and timeouts;
- make repeated upsert and delete requests idempotent;
- write ownership metadata where the destination supports it; and
- report partial success precisely.

## Interface

The backend resolves providers through a registry that rejects nil providers,
empty types, and duplicate types:

```go
func NewRegistry(providerList ...Provider) (*Registry, error)
func MustNewRegistry(providerList ...Provider) *Registry
func (r *Registry) Get(providerType string) (Provider, bool)
func (r *Registry) MustGet(providerType string) (Provider, error)
func (r *Registry) Providers() []Provider
```

Each provider validates config and opens a configured runtime:

```go
type Provider interface {
	Type() string
	Capabilities() Capabilities
	ValidateConfig(context.Context, DestinationConfig) error
	NormalizeAssociationConfig(context.Context, DestinationConfig, AssociationConfig) (AssociationConfig, error)
	OpenDestination(context.Context, DestinationConfig) (DestinationRuntime, error)
}
```

`OpenDestination` must not return a nil runtime with a nil error.

The runtime owns provider API calls:

```go
type DestinationRuntime interface {
	Health(context.Context) (*HealthResult, error)
	Plan(context.Context, PlanRequest) (*PlanResult, error)
	Upsert(context.Context, UpsertRequest) (*SyncResult, error)
	Delete(context.Context, DeleteRequest) (*SyncResult, error)
	ReadState(context.Context, ReadStateRequest) (*RemoteState, error)
	Close(context.Context) error
}
```

- `Health` must not mutate remote state. Report an unhealthy destination as a
  `HealthResult` with `ErrorClass` set.
- `Plan` must not mutate remote state. Report expected conflicts, validation
  failures, ownership mismatches, and unavailable dependencies as a
  `PlanResult`, usually with action `conflict` or `blocked` and `ErrorClass`
  set. Return a non-nil error only when no plan can be produced.
- `Upsert`, `Delete`, and `ReadState` return `*providers.Error` for provider-side
  failures. A missing object is `RemoteState.Exists=false`, not an error.
- `Close` must be safe to call more than once.

The backend caches runtimes per destination and config fingerprint. Destination
updates, OpenBao cache invalidation, and backend cleanup close cached runtimes.

### Destination config

```go
type DestinationConfig struct {
	Name   string
	Config map[string]string
}
```

`Config` holds provider-owned fields. The backend stores sensitive fields
separately under a seal-wrapped prefix, redacts them on reads, and merges them
into `Config` only when it calls the provider. Destination and sensitive field
keys are registered per type in `internal/backend/path_destinations.go`.

`ValidateConfig` rejects missing fields, unsupported auth modes, recognized but
unsupported sensitive fields, unsafe endpoints, invalid names, and conflicting
settings before the backend stores the destination.

### Association config

```go
type AssociationConfig struct {
	Config   map[string]string
	Identity string
}
```

Association config holds settings that can differ between remote objects that
share a destination. The backend calls `NormalizeAssociationConfig` on every
association write, plan, and identity-scoped selection. The provider validates
the fields, applies stable defaults, and returns the complete canonical config
that the backend persists.

`Identity` is an opaque, non-sensitive component that the core uses for
association IDs, selection, and remote-name reservations. Only the provider
derives it, and runtimes must read `Config` instead of parsing `Identity`.
Changing the derivation changes association ownership and reservation keys, so
it must stay stable. GitLab derives `Identity` from `environment_scope`.
Providers without association settings reject non-empty config and return an
empty canonical map. Association field keys are registered per type in
`internal/backend/path_associations.go`.

The core renders remote names before it calls the provider. See
[Name remote objects](../configure/associations.md#name-remote-objects) for
templates and reservations.

### Runtime identity

```go
type RuntimeIdentity struct {
	MountUUID    string
	RestoreEpoch string
}
```

Every plan, upsert, delete, and read-state request carries the runtime identity
of the mount that produced it. See
[Runtime identity and restore epoch](../concepts/ownership-and-drift.md#runtime-identity-and-restore-epoch)
for how the restore epoch changes.

### Requests and results

All four request types carry `Runtime`, `Association`, `ResolvedName`,
`SourcePath`, `SourceVersion`, `AssociationID`, and `ObjectID`. Their
`OwnershipIdentity()` method returns the `RequestIdentity` that ownership checks
compare against.

```go
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

type PlanResult struct {
	Action     string
	Message    string
	ErrorClass ErrorClass
}
```

Plan requests describe the object that dispatch would mutate and never carry
payload values. `Action` is one of `create`, `update`, `noop`, `conflict`
(an object exists that this plugin does not own), or `blocked` (the provider
cannot safely produce a mutating operation).

```go
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
```

Upsert must be content-idempotent. Repeating the same request for the same owned
object leaves it unchanged or converges it to the same payload and metadata
without an error. Providers can pass `IdempotencyKey` to APIs that accept
request tokens, but the core does not rely on it for correctness. Providers
with metadata readback reject a mutation when the remote source version is
newer than `SourceVersion`.

```go
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
```

The core sends a delete only when the association uses `delete_mode=delete`
and the provider advertises `SupportsDeleteIfOwned`. Delete removes only owned
objects. If ownership cannot be proven, return the `ownership` class instead of
deleting. A repeated delete after the object is gone or scheduled for deletion
returns a successful `SyncResult`.

```go
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

type RemoteState struct {
	Exists         bool
	OwnershipKnown bool
	Owned          bool
	PayloadSHA256  string
	SourceVersion  int
	RemoteVersion  string
	Verification   string
}

type SyncResult struct {
	RemoteVersion string
}

type HealthResult struct {
	Healthy    bool
	Message    string
	ErrorClass ErrorClass
}
```

`OwnershipKnown=false` means the provider cannot prove ownership either way.
Reconcile reports an object as synced only when `PayloadSHA256` equals the
desired hash, so a provider must fill it only from evidence it actually
compared. Set `Verification` to `value` when the provider compared remote
payload bytes and to `metadata` when it compared provider metadata only. See
[Verification values](../reference/status-and-diagnostics.md#verification-values).

### Payload input

The core builds the payload in `internal/payload`, enforces `MaxPayloadBytes`,
and computes `PayloadSHA256` over the exact bytes intended for provider
comparison. Formats are `json` (canonical JSON object), `raw` (one selected key
as a string or bytes), and `data-map` (destination-native data map).

Providers must not reformat `Payload` when they persist or compare its hash. For
`data_mapping=source-keys`, the core sends `DataMap` keyed by rendered
destination data keys. Providers must use that map and must not derive it by
reparsing JSON. See
[Choose a secret shape](../configure/associations.md#choose-a-secret-shape) for
the user-facing shapes.

## Capabilities

```go
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
```

The backend uses capabilities before any provider mutation:

- `SupportsSecretPath` and `SupportsSecretKey` gate association granularity.
- `SupportsDataMap` gates `data_mapping=source-keys`.
- `SupportsDeleteIfOwned` gates `delete_mode=delete`.
- `MaxPayloadBytes` rejects larger payloads before the provider call in plan,
  dispatch, and reconcile. A value of `0` or less disables the check.

Destination reads return the capabilities as `supports_*` and
`max_payload_bytes` fields. Advertise only behavior the provider implements and
tests. Destination config can still decide whether an implemented capability is
used when that capability changes the remote permissions it needs.

## Ownership metadata

Providers write these logical fields to the remote object where the destination
supports them:

```text
managed=true
mount_uuid=<mount-uuid>
restore_epoch=<restore-epoch>
association_id=<association-id>
source_path=<source-path>
source_version=<source-version>
object_id=<object-id>
payload_sha256=<hash>
```

The carrier and exact spelling are provider-specific, such as AWS tags,
Kubernetes labels and annotations, or a GitLab variable description. A provider
that cannot store every field documents its reduced proof on its configure page.

Before an update or delete, the provider treats a remote object as owned only
when the managed marker is present and the association ID, source path, object
ID, mount UUID, and restore epoch match the request's `OwnershipIdentity()`. A
provider that stores a field as a hash compares the hash of the expected value.
GitLab stores the mount UUID and restore epoch as hashes, and hashes the object
ID and source path when needed to fit its description limit.
See [Ownership metadata](../concepts/ownership-and-drift.md#ownership-metadata)
for the operator view.

## Error classification

```go
type ErrorClass string

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
```

Return failures as `*providers.Error` with `Class` set and a message free of
secret material. The core classifies any other error with
`ClassifyTransportError`, which maps deadlines, connection failures, timeouts,
DNS errors, and `*url.Error` values to `unavailable`. Everything else becomes
`internal`.

The core retries only `rate_limit` and `unavailable` automatically, so a
misclassified terminal failure retries and a misclassified transient failure
stops. See
[Error classes and retry](../reference/status-and-diagnostics.md#error-classes-and-retry)
for the class meanings and retry policy.

## Conformance tests

Every provider runs `providertest.Run` from `internal/providers/providertest`
with a `Harness`. The harness checks:

- a non-empty provider type;
- required capability bits and a minimum `MaxPayloadBytes`;
- valid and invalid destination validation with the expected error class;
- normalization of an empty association config;
- health results;
- plan action mapping;
- a create, no-op, update, delete lifecycle with read-state after each step;
- same-request upsert and delete idempotency;
- upsert and delete error classes; and
- the maturity matrix: ownership loss, auth failure, throttling, payload
  limit, partial success, stale remote state, and delete semantics.

The partial success case declares `atomic` when the provider writes payload and
metadata in one call, or `classified_failure` when a post-write failure is
possible and must return a classified error.

Provider-specific tests cover request shape, ownership metadata layout,
stale source-version rejection, naming rules, auth behavior, and API edge
cases. See [Testing](testing.md) for the lanes that run them.

## Registered providers

`productionProviders` in `internal/backend/backend.go` registers the providers
below. The `fake` provider in `internal/providers/fake` supports backend tests
only and is not registered.

| Type | Package | Supported shapes | Configure |
| --- | --- | --- | --- |
| `aws-sm` | `internal/providers/awssecretsmanager` | `secret-path` with `json` | [AWS Secrets Manager](../configure/aws-secrets-manager.md) |
| `k8s` | `internal/providers/kubernetessecrets` | `secret-path` with `json`; `secret-path` with `data_mapping=source-keys` | [Kubernetes Secrets](../configure/kubernetes-secrets.md) |
| `gitlab` | `internal/providers/gitlab` | `secret-path` with `json`; `secret-key` with `json` or `raw` | [GitLab project variables](../configure/gitlab-project-variables.md) |

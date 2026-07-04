# Provider contract

Providers adapt the core sync engine to destination-specific APIs. The core
engine owns OpenBao storage, authorization shape, queueing, status, payload
construction, redaction, and safety policy evaluation. Providers own
destination API validation, client construction, remote state inspection,
idempotent mutation, ownership metadata, and error classification.

Providers are Go packages compiled into the external OpenBao plugin binary.
They are not separate plugin processes.

## Interface

The backend resolves providers through a concrete registry:

```go
func NewRegistry(providerList ...Provider) (*Registry, error)
func MustNewRegistry(providerList ...Provider) *Registry
func (r *Registry) Get(providerType string) (Provider, bool)
func (r *Registry) MustGet(providerType string) (Provider, error)
```

Each provider validates destination configuration and opens a configured
runtime:

```go
type Provider interface {
    Type() string
    Capabilities() Capabilities
    ValidateConfig(context.Context, DestinationConfig) error
    OpenDestination(context.Context, DestinationConfig) (DestinationRuntime, error)
}
```

The configured runtime owns provider API calls:

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

The backend caches destination runtimes by destination identity and config.
Destination updates and backend cleanup invalidate or close cached runtimes.

## Destination config

Resolved destination config is deliberately split into a stable destination
name and a provider-owned string map:

```go
type DestinationConfig struct {
    Name   string
    Config map[string]string
}
```

Persistent destination metadata keeps sensitive fields out of the
non-sensitive storage record. The backend stores sensitive fields under a
seal-wrapped destination secret prefix, redacts reads, and merges sensitive
fields into the resolved provider config only for validation, health, planning,
dispatch, and reconcile.

Provider config validation rejects unsupported auth modes, unsupported
sensitive fields, unsafe endpoints, invalid names, and provider-specific
configuration conflicts before the backend stores the destination.

## Provider rules

Providers:

- receive resolved destination config and prepared payloads;
- never receive OpenBao request objects;
- never read OpenBao storage;
- never perform OpenBao policy decisions;
- classify errors into stable classes;
- avoid logging secret values and credentials;
- support context cancellation and timeouts;
- write ownership metadata where the destination supports it;
- report partial success precisely.

## Capabilities

Each provider declares capabilities before associations are accepted:

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

The backend uses capabilities to validate association granularity, data
mapping, delete mode, and payload size before provider mutation. A provider
must advertise only behavior it implements and tests. Provider-specific
destination config may still decide whether an implemented capability is used
for a given destination when that capability changes remote permissions.

## Runtime identity

Provider mutation and read-state requests include runtime identity:

```go
type RuntimeIdentity struct {
    PluginInstanceID string
    RestoreEpoch     string
}
```

Providers include these values in remote ownership metadata where the
destination supports metadata. On later mutations, populated ownership metadata
must match the current mount identity before the provider updates or deletes a
remote object.

## Plan input

Plan requests describe the same remote object that dispatch would mutate, but
the response must not include secret payload values:

```go
type PlanRequest struct {
    Runtime       RuntimeIdentity
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

Provider plan actions are stable strings:

- `create`
- `update`
- `noop`
- `conflict`
- `blocked`

Plan operations must not mutate remote state.

## Upsert input

Upsert requests receive prepared payload bytes and payload metadata:

```go
type UpsertRequest struct {
    Runtime       RuntimeIdentity
    ResolvedName  string
    Format        string
    Payload       []byte
    PayloadSHA256 string
    DataMap       map[string][]byte
    SourcePath    string
    SourceVersion int
    AssociationID string
    ObjectID      string
}
```

The core engine builds `Payload`, enforces `MaxPayloadBytes`, and computes
`PayloadSHA256` before calling the provider. Providers must not reformat the
payload before writing it when they also persist or compare the payload hash.

Providers with metadata readback reject stale mutations when the remote managed
source version is newer than the request source version.

## Delete input

Delete requests are sent only when the association uses `delete_mode=delete`
and the provider advertises owned delete support:

```go
type DeleteRequest struct {
    Runtime       RuntimeIdentity
    ResolvedName  string
    DataMap       bool
    SourcePath    string
    SourceVersion int
    AssociationID string
    ObjectID      string
}
```

Provider delete implementations delete only owned objects. If ownership cannot
be proven, the provider returns the `ownership` error class instead of
deleting.

## Read-state input

Read-state requests use the same remote identity and payload hash as mutation
requests:

```go
type ReadStateRequest struct {
    Runtime       RuntimeIdentity
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
}
```

`OwnershipKnown=false` means the provider cannot prove ownership either way.
The core does not treat that state as `SYNCED` unless another comparable field,
such as provider payload-hash metadata, proves the desired remote state.

## Ownership metadata

Providers write destination metadata where possible:

```text
openbao-sync=true
openbao-sync-plugin-instance=<plugin-instance-id>
openbao-sync-restore-epoch=<restore-epoch>
openbao-sync-association=<association-id>
openbao-sync-path=<source-path>
openbao-sync-version=<source-version>
openbao-sync-object=<object-id>
openbao-sync-payload-sha256=<hash>
```

Provider-specific docs describe reduced ownership proof when the destination
cannot store all fields.

## Name templates

The template engine is intentionally small and currently performs literal
placeholder replacement only. It does not support functions, filters,
conditionals, loops, escaping, or nested expressions.

Use [Templating](../concepts/templating.md) for the user-facing contract and
provider constraint guidance.

Supported `name_template` placeholders:

```text
{{ path }}
{{ key }}
{{ destination.type }}
{{ destination.name }}
```

`data_key_template` supports only:

```text
{{ key }}
```

Templates are validated at association creation and revalidated during sync.
Rendered templates that still contain `{{` or `}}` are rejected as
unsupported.

The backend maintains a reservation index for resolved or rendered remote names
so two associations do not manage the same remote object for one destination.

Template changes do not silently rename existing remote objects. Operators
must create a new association, review the plan, and delete the old association
when changing the remote-name reservation.

## Payload formats

Supported formats:

- `json`: canonical JSON object;
- `raw`: one selected source key as raw string or bytes;
- `data-map`: canonical internal format used for destination-native data maps.

Supported association shapes:

- `granularity=secret-path` with `format=json`;
- `granularity=secret-key` with `format=json`;
- `granularity=secret-key` with `format=raw`;
- `granularity=secret-path` with `data_mapping=source-keys` when the provider
  advertises data-map support.

For `secret-key` and `json`, each remote payload is canonical JSON containing
only the selected source key. Source keys used as `secret-key` object IDs must
be non-empty, have no surrounding whitespace, and must not contain `/`, `.`, or
`..`.

For `secret-key` and `raw`, the remote payload is the exact string or byte
value of the selected source key. Structured values are rejected before a
provider call. `raw` is invalid for `secret-path` because there is no single
selected key.

For `data_mapping=source-keys`, the core sends providers a
`map[string][]byte` keyed by rendered destination data keys. Providers receive
the destination-native data map and must not derive it by reparsing JSON
payload bytes.

The payload hash is always computed over the exact bytes intended for provider
comparison.

## Error classification

Provider errors map to stable classes:

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

The core automatically retries only `rate_limit` and `unavailable` errors.
Validation, authentication, authorization, ownership, collision, drift,
capacity, and internal failures remain terminal until an operator changes
configuration or retries manually.

## Provider conformance tests

Every registered provider uses the shared provider conformance harness. The
harness locks down the common contract:

- stable non-empty provider type;
- declared capability bits and payload limits;
- destination validation error classes;
- health diagnostics;
- plan action mapping;
- upsert and delete success results where implemented;
- read-state behavior where implemented;
- provider error-class mapping for retry and terminal failures;
- ownership loss, authentication failure, throttling, payload limits,
  partial-success behavior, stale remote state, and delete semantics.

Provider-specific tests cover request shape, ownership metadata layout, stale
source-version rejection, provider naming rules, provider auth behavior, and
destination API edge cases.

## Registered providers

### Fake provider

The fake provider supports backend tests and local contract checks. It provides
deterministic plan actions, mutation responses, error classes, and both
`secret-path` and `secret-key` granularity.

### AWS Secrets Manager

AWS Secrets Manager uses destination type `aws-sm`.

Supported auth modes:

- AWS SDK default credential chain;
- STS assume role.

Supported association shape:

- `granularity=secret-path` with `format=json`.

The provider writes ownership tags, uses metadata readback, rejects ownership
loss, rejects stale remote source versions, classifies AWS and transport
errors, and supports scheduled-delete recovery for owned secrets. By default,
plan, upsert no-op detection, and read-state use tag metadata. With
`value_drift_detection=true`, those explicit operations also use
`GetSecretValue` for owned secrets and compare the live value hash with the
desired payload hash.

Static AWS access keys, secret access keys, and session tokens are recognized
as sensitive fields but are not supported auth material.

### Kubernetes Secrets

Kubernetes Secrets uses destination type `k8s`.

Supported auth modes:

- in-cluster service account;
- kubeconfig;
- bearer token with API server and CA config.

Supported association shapes:

- `granularity=secret-path` with `format=json`;
- `granularity=secret-path` with `data_mapping=source-keys`.

The provider writes `Opaque` Secrets, ownership labels and annotations, payload
hash metadata, and source-version metadata. It supports owned update, owned
delete, value readback, read-state, health checks, and Kubernetes API error
classification. Plan, upsert no-op detection, and read-state compute payload
hashes from live Secret data rather than trusting stored payload-hash
annotations first.

The provider does not advertise `secret-key` fan-out.

### GitLab project variables

GitLab project variables use destination type `gitlab`.

Supported auth mode:

- GitLab API token.

Supported association shapes:

- `granularity=secret-key` with `format=raw`;
- `granularity=secret-key` with `format=json`;
- `granularity=secret-path` with `format=json`.

The provider writes project CI/CD variables, stores ownership metadata in the
variable description, validates variable attributes and masked payloads,
repairs value and attribute drift, supports owned update, owned delete, value
readback, read-state, health checks, and HTTP error classification.

Non-local `http://` GitLab base URLs are rejected by default and require
`allow_insecure_http=true`, which is intended for local Docker or private test
networks rather than production destinations.

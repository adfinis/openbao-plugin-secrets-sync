# Provider Contract


## Purpose

Providers adapt the core sync engine to destination-specific APIs. The core
engine owns OpenBao storage, authorization shape, queueing, status, payload
construction, and safety policy evaluation. Providers own destination API
validation, remote state inspection, idempotent mutation, and error
classification.

Providers must be deliberately constrained. They are not plugins inside the
plugin in the MVP; they are Go packages compiled into the external OpenBao
plugin binary.

## Interface

```go
type Registry interface {
    Get(providerType string) (Provider, bool)
    MustGet(providerType string) (Provider, error)
}

type Provider interface {
    Type() string
    Capabilities() Capabilities
    Validate(ctx context.Context, cfg DestinationConfig) error
    Plan(ctx context.Context, req PlanRequest) (*PlanResult, error)
    Upsert(ctx context.Context, req UpsertRequest) (*SyncResult, error)
    Delete(ctx context.Context, req DeleteRequest) (*SyncResult, error)
    ReadState(ctx context.Context, req ReadStateRequest) (*RemoteState, error)
    Health(ctx context.Context, cfg DestinationConfig) (*HealthResult, error)
}
```

Resolved destination config is deliberately split into a stable name and a
provider-owned string map:

```go
type DestinationConfig struct {
    Name   string
    Config map[string]string
}
```

Persistent destination metadata must keep sensitive fields out of the
non-sensitive storage record. The backend stores sensitive fields under a
seal-wrapped destination secret prefix, redacts reads, and merges them into the
resolved provider config only for validation, health, planning, and dispatch.

The core engine resolves provider implementations through the registry. Route
handlers, association validation, and the dispatcher must use the same registry
so capability checks and remote mutation cannot drift.

Provider rules:

- Providers receive resolved destination config and prepared payloads.
- Providers never receive OpenBao request objects.
- Providers never perform OpenBao policy decisions.
- Providers must classify errors into stable classes.
- Providers must avoid logging secret values.
- Providers must support context cancellation and timeouts.
- Providers must implement ownership metadata where the target supports it.
- Providers must report partial success precisely.

## Capabilities

Each provider must declare capabilities before associations are accepted.

```go
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
```

The core engine uses capabilities to decide whether a requested association is
valid. For example, `overwrite_owned_only` requires some combination of
metadata readback and provider-specific conditional update semantics. If a
provider cannot prove ownership, the association must either be rejected or
forced into a weaker explicitly acknowledged safety mode.

Future capability fields such as explicit name requirements, binary payload
support, soft-delete semantics, and provider rate-limit models should be added
when the first provider needs them and the core engine can enforce them.

## Safety Modes

Required collision policies:

- `fail_if_exists`: create only when the remote object does not exist.
- `overwrite_owned_only`: update only when remote ownership metadata matches
  the plugin association.
- `overwrite_any`: update regardless of existing remote ownership; operator
  only and never the default.

Required delete modes:

- `retain`: stop local management and leave remote object intact.
- `delete`: delete only when ownership can be proven.
- `orphan`: remove association and stop managing remote object.

Providers must return `ownership` or `collision` error classes when they cannot
honor the requested safety mode.

The core engine owns delete-mode selection. Providers only receive delete
requests when the selected mode requires remote deletion and provider
capabilities declare owned delete support.

## Plan And Diagnostics

Plan requests use the same resolved name and canonical payload metadata that
dispatch uses, but never include secret values in the response:

```go
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

type PlanResult struct {
    Action     string
    Message    string
    ErrorClass ErrorClass
}
```

Provider actions are stable strings: `create`, `update`, `noop`, `conflict`,
and `blocked`.

Destination validation and health endpoints are diagnostic surfaces. Provider
validation or health failures should be returned as structured response fields
with an error class, not as leaked raw provider responses.

## Upsert Input

Providers receive prepared payload bytes, not source secret maps:

```go
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
```

The core engine builds `Payload`, enforces `MaxPayloadBytes`, and computes
`PayloadSHA256` before the provider is called. Providers must not reformat the
payload before writing if they also persist or compare the payload hash.
`SourcePath`, `SourceVersion`, `AssociationID`, and `ObjectID` let providers
persist ownership metadata without reaching back into OpenBao storage. Providers
with metadata readback should reject stale mutations when the remote managed
source version is newer than the request source version.

## Delete Input

Providers receive resolved delete requests after the core engine has validated
association policy and destination capability:

```go
type DeleteRequest struct {
    Destination   DestinationConfig
    ResolvedName  string
    SourcePath    string
    SourceVersion int
    AssociationID string
    ObjectID      string
}
```

Provider delete implementations must only delete owned objects. If ownership
cannot be proven, return `ownership` rather than deleting. Delete requests carry
the same ownership identity as upsert requests so providers can prove that the
remote object belongs to the association that requested deletion.

## Read-State Input

Remote state reads receive the same destination config as mutating calls:

```go
type ReadStateRequest struct {
    Destination   DestinationConfig
    ResolvedName  string
    PayloadSHA256 string
    SourcePath    string
    SourceVersion int
    AssociationID string
    ObjectID      string
}

type RemoteState struct {
    Exists          bool
    OwnershipKnown  bool
    Owned           bool
    PayloadSHA256   string
    SourceVersion   int
    RemoteVersion   string
}
```

`OwnershipKnown=false` means the provider could not prove ownership either
way. The core must not treat that as `SYNCED` unless another comparable field,
such as the provider payload hash metadata, matches the desired state.

## Ownership Metadata

Providers should write destination metadata where possible:

```text
openbao-sync=true
openbao-sync-plugin-instance=<plugin-instance-id>
openbao-sync-restore-epoch=<restore-epoch>
openbao-sync-mount=<mount-accessor-or-name>
openbao-sync-association=<association-id>
openbao-sync-path=<source-path>
openbao-sync-version=<source-version>
openbao-sync-object=<object-id>
openbao-sync-payload-sha256=<hash>
```

If a provider cannot store all fields, it must document the reduced ownership
proof. The core engine should surface reduced guarantees in plan and status
responses.

## Name Templates

The template engine should be intentionally small. Supported values:

```text
mount
mount_accessor
path
path_segments
basename
dirname
version
key
destination_type
destination_name
metadata.<name>
```

Supported functions:

```text
lower
upper
replace
truncate
sha256
dns1123
```

Templates must be validated at association creation and revalidated during
sync. The core engine must maintain a reservation index for resolved names so
two associations do not accidentally manage the same remote object after
normalization, truncation, or provider-specific character conversion.

Template changes must not silently rename existing remote objects. Required
behavior:

- plan shows old and new resolved names;
- operator chooses retain, delete-owned, or orphan old remote objects;
- status tracks old objects until cleanup is complete.

## Payload Formats

MVP formats:

- `json`: full secret data as a canonical JSON object.
- `raw`: one selected key as raw string or bytes.

Later format:

- `env`: postponed because escaping and multiline handling are easy to misuse.

Granularity:

- `secret-path`: one destination secret per OpenBao secret path.
- `secret-key`: one destination secret per top-level key in OpenBao secret
  data.

Core dispatch supports both granularities when the destination provider
advertises the matching capability. For `secret-key` and `json` format, each
remote payload is canonical JSON containing only that source key. Source keys
used as `secret-key` object identifiers must be non-empty, have no surrounding
whitespace, and must not contain `/`, `.`, or `..`.

For `secret-key` and `raw` format, the remote payload is the exact string or
byte value of the selected source key. Structured values are rejected before a
provider call. `raw` is intentionally invalid for `secret-path` because there
is no single selected key.

Canonical JSON requirements:

- stable key ordering;
- UTF-8 output;
- no provider-specific whitespace changes in the payload hash;
- explicit behavior for non-string values;
- explicit behavior for null values;
- deterministic hash input shared by plan, upsert, and drift detection.

The payload hash must be over the exact bytes intended for the destination
payload, not over the original Go map representation.

## Error Classification

Provider errors must map to:

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

Automatically retry only `rate_limit` and `unavailable` errors. A later
provider-contract extension may add an explicit retryable-internal marker, but
plain `internal` errors are terminal for now. Validation, authentication,
authorization, ownership, collision, and provider policy errors should remain
terminal until configuration changes.

## Provider Conformance Tests

Use [Provider implementation guide](provider-implementation.md) for the
practical provider development workflow. This section defines the shared
contract expectations.

Every provider package should use the shared provider conformance harness before
it is registered in the backend. The harness is not a replacement for
provider-specific tests, but it locks down the common contract:

- stable non-empty provider type;
- declared capability bits and payload limits;
- destination validation error classes;
- health diagnostics;
- plan action mapping;
- upsert and delete success results where implemented;
- read-state behavior where implemented;
- provider error-class mapping for retry and terminal failures;
- maturity matrix coverage for ownership loss, authentication failure,
  throttling, payload limits, partial-success behavior, stale remote state, and
  delete semantics.

New providers may start with only type, capability, validation, health, and
plan checks. Backend registration should wait until upsert, owned delete, and
error classification are implemented for the provider.

The maturity matrix treats partial success as an explicit provider property.
Providers whose remote API writes payload and ownership metadata atomically
should declare the atomic mode and keep lifecycle coverage for that mutation.
Providers with multi-step writes must include a case where a later metadata or
cleanup step fails after an earlier remote mutation and the provider returns a
stable error class with no successful `SyncResult`.

## Provider MVP Choices

### Fake Provider

The fake provider is required in Phase 0. It should support all capability
combinations needed by unit and integration tests, including:

- success;
- deterministic plan actions for create, update, noop, conflict, and blocked;
- validation, authentication, authorization, rate-limit, unavailable,
  ownership, collision, and validation error classes;
- unhealthy destination diagnostics;
- `secret-path` and `secret-key` granularity;
- delayed read-after-write consistency in a later slice.

### AWS Secrets Manager

AWS Secrets Manager is a strong MVP provider because it has common customer
demand and a useful ownership model through tags and version metadata.

Current status: package has provider type `aws-sm`, conservative capabilities,
backend registration, destination config for SDK default auth and STS
assume-role auth, seal-wrapped external ID handling, explicit custom endpoint
policies, an SDK-backed client boundary, LocalStack e2e coverage, and mocked
behavior tests for health, plan, upsert, owned delete, read-state, ownership
checks, AWS error classification, and the shared provider maturity matrix.

Current granularity support: `secret-path` only. The provider advertises
`SupportsSecretKey: false` until AWS naming, collision, and cleanup semantics
are implemented and covered by tests.

Required implementation behavior:

- support workload identity or role assumption before static keys;
- validate auth mode, assume-role fields, sensitive static credential fields,
  endpoint URL shape, and endpoint policy;
- write ownership tags for association id, source path, source version, object
  id, and payload hash;
- enforce max payload size before remote calls;
- classify AWS throttling and service errors;
- reject stale update or delete attempts when the remote managed source version
  is newer than the request source version;
- detect ownership loss before update or delete;
- avoid logging AWS responses that may include secret material.

Static access keys are intentionally not supported yet. The backend now has the
seal-wrapped storage and redaction surface needed for them, but static auth
still requires explicit credential construction, rotation semantics, and opt-in
tests before it should be enabled.

### Kubernetes Secrets

Kubernetes Secrets are a strong MVP provider because OpenBao users commonly run
Kubernetes and local integration testing is practical.

Required implementation behavior:

- support in-cluster, kubeconfig, and service-account auth where appropriate;
- validate namespace and name using Kubernetes rules;
- write labels and annotations for ownership metadata;
- support `Opaque` secrets first;
- handle per-key partial failures clearly;
- test with envtest or kind.

Current status: package has provider type `k8s`, conservative capabilities,
backend registration, destination config for namespace, in-cluster auth, and
kubeconfig auth, a client-go-backed client boundary, Opaque Secret
create/update/delete/read-state/health behavior, ownership labels and
annotations, payload hash metadata, Kubernetes API error classification, and a
provider conformance lifecycle and maturity test using the client-go fake
client.

Current granularity support: `secret-path` only. The core engine now expands
`secret-key` associations for providers that opt in, but Kubernetes Secret
`secret-key` support remains later work because it needs a clear provider-level
model for Secret name, data key, ownership metadata, and cleanup semantics.

### GitLab Project Variables

GitLab project variables are a useful third provider because they exercise a
non-secret-manager destination shape: one CI/CD variable key/value per remote
object.

Current status: package has provider type `gitlab`, backend registration,
project-level destination config, seal-wrapped API token storage, standard HTTP
client boundary, provider conformance coverage, project variable plan/upsert,
owned update, owned delete, read-state, health, and HTTP error classification.
The provider passes the shared maturity matrix. Self-contained Docker GitLab
e2e coverage is opt-in because a full GitLab CE container is heavy. Real GitLab
e2e coverage remains a later opt-in/manual fixture because it requires an
external project and token.

Current granularity support: `secret-path` and `secret-key`. For CI/CD
variables, `secret-key` with `format=raw` is the recommended shape. The
provider validates rendered variable names against GitLab's variable key rules.
Ownership and payload hash metadata are stored in the variable description;
the provider does not need to read variable values back for drift status.
Non-local `http://` GitLab base URLs are rejected by default and require
`allow_insecure_http=true`, which is intended for local Docker or private test
networks rather than production destinations.

## Provider Test Expectations

Every provider must have tests for:

- config validation;
- redaction of sensitive fields;
- name validation and normalization;
- payload size limits;
- create/update/delete success;
- fail-if-exists behavior;
- overwrite-owned-only behavior;
- ownership loss;
- rate limit and transient retry mapping;
- terminal authn/authz/policy failures;
- no secret values in logs, errors, or status fixtures.

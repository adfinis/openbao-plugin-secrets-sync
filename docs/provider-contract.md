# Provider Contract

Status: draft
Date: 2026-06-30

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
    MaxPayloadBytes             int
    SupportsTags                bool
    SupportsLabels              bool
    SupportsAnnotations         bool
    SupportsValueReadback       bool
    SupportsMetadataReadback    bool
    SupportsVersionCompare      bool
    SupportsPayloadHashMetadata bool
    SupportsCreateIfAbsent      bool
    SupportsUpdateIfOwned       bool
    SupportsDeleteIfOwned       bool
    SupportsSoftDelete          bool
    SupportsBinaryPayload       bool
    SupportsSecretPath          bool
    SupportsSecretKey           bool
    NameRequirements            NameRequirements
    RateLimitModel              RateLimitModel
}
```

The core engine uses capabilities to decide whether a requested association is
valid. For example, `overwrite_owned_only` requires some combination of
metadata readback and provider-specific conditional update semantics. If a
provider cannot prove ownership, the association must either be rejected or
forced into a weaker explicitly acknowledged safety mode.

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

## Upsert Input

Providers receive prepared payload bytes, not source secret maps:

```go
type UpsertRequest struct {
    Destination   DestinationConfig
    ResolvedName  string
    Format        string
    Payload       []byte
    PayloadSHA256 string
}
```

The core engine builds `Payload`, enforces `MaxPayloadBytes`, and computes
`PayloadSHA256` before the provider is called. Providers must not reformat the
payload before writing if they also persist or compare the payload hash.

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
- `secret-key`: one destination secret per key in OpenBao secret data.

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

Retry only `rate_limit`, `unavailable`, and explicitly retryable internal
errors. Validation, authentication, authorization, ownership, collision, and
provider policy errors should remain terminal until configuration changes.

## Provider MVP Choices

### Fake Provider

The fake provider is required in Phase 0. It should support all capability
combinations needed by unit and integration tests, including:

- success;
- transient failure;
- rate limit;
- terminal validation failure;
- ownership conflict;
- partial success for `secret-key`;
- delayed read-after-write consistency.

### AWS Secrets Manager

AWS Secrets Manager is a strong MVP provider because it has common customer
demand and a useful ownership model through tags and version metadata.

Required implementation behavior:

- support workload identity or role assumption before static keys;
- validate region and endpoint controls;
- write ownership tags;
- enforce max payload size;
- classify AWS throttling and service errors;
- detect ownership loss;
- avoid logging AWS responses that may include secret material.

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

---
verifiedBy:
  - internal/providers/kubernetessecrets/provider.go
  - internal/backend/path_destinations.go
  - internal/backend/dispatcher.go
  - test/e2e/kind
---

# Sync to Kubernetes Secrets

The `k8s` provider writes one `Opaque` Secret for each association in the
destination namespace. It records ownership in a label and annotations.

## Choose an auth mode

Every destination needs `namespace`, a valid DNS-1123 label. Set `auth_mode`
to `in_cluster`, `kubeconfig`, or `token`. When you omit it, the provider
selects `token` if `api_server` or `token` is set, `kubeconfig` if
`kubeconfig_path` is set, and `in_cluster` otherwise. Each mode rejects the
other modes' fields.

Use `in_cluster` when OpenBao runs in the target cluster and its service
account holds the permissions:

```sh
bao write secret-sync/destinations/k8s/apps namespace=apps auth_mode=in_cluster
```

Use `kubeconfig` for an external cluster. `kubeconfig_path` is a file on the
OpenBao server that the plugin process must be able to read. `context` is
optional:

```sh
bao write secret-sync/destinations/k8s/apps \
  namespace=apps \
  auth_mode=kubeconfig \
  kubeconfig_path=/etc/openbao/kubeconfig \
  context=<context>
```

Use `token` to reach an API server with a bearer token. `api_server` must be an
HTTPS URL. `ca_cert_pem` is optional when the server certificate chains to the
runtime trust store and must contain at least one PEM certificate when set.
Set `tls_server_name` when the certificate name differs from the URL host:

```sh
bao write secret-sync/destinations/k8s/apps \
  namespace=apps \
  auth_mode=token \
  api_server=https://kubernetes.example.com \
  ca_cert_pem=@cluster-ca.pem \
  token="$KUBERNETES_BEARER_TOKEN"
```

Token auth rejects localhost and restricted address ranges for `api_server`
unless you set `allow_private_api_server=true`. See
[Endpoint address policy](../security/threat-model.md#endpoint-address-policy).
In-cluster and kubeconfig auth do not apply this policy.

The backend seal-wraps `token` and redacts it on reads. It stores only
`kubeconfig_path`, never the file contents. A caller who can write
`destinations/*` chooses which file the plugin reads, so keep that capability
with trusted platform operators. Requests time out after 30 seconds unless a
kubeconfig sets a positive timeout.

## Grant permissions

Bind a namespace-scoped Role only in namespaces approved as sync destinations:

```yaml
apiGroups: [""]
resources: ["secrets"]
verbs: ["get", "create", "update", "delete"]
```

The provider uses `get` for health, plan, and read-state, `create` and `update`
for upserts, and `delete` for owned deletes. Health reads the fixed name
`openbao-secret-sync-health-check` and treats not-found as healthy, so the
identity needs no `list` access.

## Create an association

Kubernetes supports `secret-path` with `format=json`, using either
`data_mapping=payload` or `data_mapping=source-keys`; see
[Choose a secret shape](associations.md#choose-a-secret-shape). The payload
limit is 1 MiB (1,048,576 bytes) of encoded payload.

The resolved name must be a DNS-1123 subdomain, so the default `{{ path }}`
name `app/db` fails at plan and dispatch. Set `resolved_name`:

```sh
bao write secret-sync/associations/app/db \
  destination=k8s/apps \
  resolved_name=app-db \
  delete_mode=delete
```

With `data_mapping=payload`, the Secret holds the canonical JSON payload under
one data key, `payload`.

With `data_mapping=source-keys`, each top-level source key becomes one Secret
data key through `data_key_template`. Source values must be strings or bytes,
and rendered keys must be valid Secret data keys:

```sh
bao write secret-sync/associations/app/db \
  destination=k8s/apps \
  resolved_name=app-db \
  data_mapping=source-keys \
  data_key_template='{{ key }}'
```

Plan with the same fields before each write.

## Ownership metadata

| Carrier | Key | Value |
| --- | --- | --- |
| Label | `openbao.org/secrets-sync-managed` | `true` |
| Annotation | `openbao.org/secrets-sync-association-id` | Association ID |
| Annotation | `openbao.org/secrets-sync-source-path` | Source path |
| Annotation | `openbao.org/secrets-sync-source-version` | Source version |
| Annotation | `openbao.org/secrets-sync-object-id` | Object ID |
| Annotation | `openbao.org/secrets-sync-payload-sha256` | Payload hash |
| Annotation | `openbao.org/secrets-sync-format` | `json` or `data-map` |
| Annotation | `openbao.org/secrets-sync-data-keys` | JSON array of managed data keys, for `source-keys` only |
| Annotation | `openbao.org/secrets-sync-mount-uuid` | Mount UUID |
| Annotation | `openbao.org/secrets-sync-restore-epoch` | Restore epoch |

The provider updates or deletes a Secret only when the label is `true` and the
association, path, object, mount UUID, and restore epoch annotations match.
Labels and annotations are plaintext and visible to anyone who can read the
Secret. See
[Ownership metadata](../concepts/ownership-and-drift.md#ownership-metadata) for
the general model.

## Drift and delete behavior

Plan, upsert, and read-state hash the live Secret data and report
`verification=value`. Manual data edits are detected even when the annotations
still hold the old hash. The next manual sync or background repair restores
owned data.

With `data_mapping=payload`, an owned update replaces all Secret data with the
single `payload` key. Keys that someone added to the Secret are removed.

With `data_mapping=source-keys`, the provider manages only the keys listed in
the data-keys annotation and preserves other keys. When a desired key already
exists but is not managed, plan reports a conflict and upsert fails with an
ownership error instead of overwriting it. Switching an association from
`payload` to `source-keys` removes the old `payload` key.

An immutable Secret blocks any change to its data. Plan reports
`action=blocked` and upsert fails with a `validation` error.

With `delete_mode=delete`:

- in `payload` mode, the provider deletes the Secret with UID and
  resource-version preconditions;
- in `source-keys` mode, it removes the managed keys and deletes the Secret
  only when no other keys remain. Otherwise it keeps the Secret and removes the
  ownership label and annotations.

A missing Secret counts as deleted. After you resolve an ownership conflict,
run the `manual_sync` action from status or reconcile. See
[Recover ownership loss](../operate/troubleshoot.md#recover-ownership-loss).

## Check the destination

`check` runs validation and, when the destination is enabled and valid, health.
`validate` checks configuration only. `health` reads the health-check Secret
name. Reads redact `token`:

```sh
bao read secret-sync/destinations/k8s/apps
bao read secret-sync/destinations/k8s/apps/check
bao read secret-sync/destinations/k8s/apps/validate
bao read secret-sync/destinations/k8s/apps/health
```

End-to-end tests: [kind](../../test/e2e/kind/README.md).

# Kubernetes Secrets configuration

## Supported auth modes

`auth_mode` may be omitted when the config shape is unambiguous. The provider
selects token auth when `api_server` or `token` is set, kubeconfig auth when
`kubeconfig_path` is set, and in-cluster auth otherwise.

Use in-cluster auth when OpenBao runs in the target Kubernetes cluster:

```sh
bao write secret-sync/destinations/k8s/apps \
  namespace=apps \
  auth_mode=in_cluster
```

Use kubeconfig auth for local development or external cluster access:

```sh
bao write secret-sync/destinations/k8s/apps \
  namespace=apps \
  auth_mode=kubeconfig \
  kubeconfig_path="$HOME/.kube/config" \
  context=kind-openbao
```

Use token auth when OpenBao reaches a Kubernetes API server directly with a
bearer token:

```sh
bao write secret-sync/destinations/k8s/apps \
  namespace=apps \
  auth_mode=token \
  api_server=https://kubernetes.example.com \
  ca_cert_pem=@cluster-ca.pem \
  token="$KUBERNETES_BEARER_TOKEN"
```

`ca_cert_pem` is optional when the API server certificate chains to the runtime
trust store. Set `tls_server_name` when the API endpoint name and certificate
name differ.

Token-auth `api_server` values that target localhost, private addresses,
link-local addresses, multicast, unspecified addresses, or DNS names that
resolve to those ranges are rejected by default. Set
`allow_private_api_server=true` only for an approved internal Kubernetes API
endpoint. In-cluster and kubeconfig auth do not use this field.

For public token-auth endpoints, the provider resolves the API server name
again for every new connection and dials an approved address directly. Ambient
proxy configuration is disabled on this guarded path so proxy-side DNS cannot
bypass the address policy. Provider-created Kubernetes clients use a 30-second
request timeout unless a kubeconfig supplies an explicit positive timeout.

Validation is strict about mixing auth fields:

- `namespace` must be a valid DNS-1123 Kubernetes namespace label.
- In-cluster auth rejects kubeconfig and token fields.
- Kubeconfig auth requires `kubeconfig_path` and rejects token fields.
- Token auth requires `api_server` and `token`, and rejects kubeconfig fields.
- Token auth requires an HTTPS `api_server`.
- Token auth requires `allow_private_api_server=true` for private or local
  `api_server` hosts.
- `ca_cert_pem`, when set, must contain at least one PEM certificate.

## Sensitive fields

The backend stores `token` under the seal-wrapped destination secret prefix and
redacts it on destination reads.

`ca_cert_pem` is certificate material and is not a bearer credential.
`kubeconfig_path` points to a file that must be readable by the OpenBao plugin
process when the provider opens the destination. A caller with
`destinations/*` write access controls this file path; keep that capability
limited to trusted platform operators. The backend stores only the path and
does not store or echo kubeconfig file contents.

## Validation and check commands

Read destination config. Sensitive fields are redacted:

```sh
bao read secret-sync/destinations/k8s/apps
```

Check destination readiness:

```sh
bao read secret-sync/destinations/k8s/apps/check
```

Use `validate` and `health` when you need separate configuration and runtime
diagnostics:

```sh
bao read secret-sync/destinations/k8s/apps/validate
bao read secret-sync/destinations/k8s/apps/health
```

# GitLab E2E Tests

This directory contains the opt-in self-contained OpenBao plus GitLab e2e test
for the GitLab project variable provider.

The workflow builds the Linux plugin binary, starts a disposable GitLab CE
container and OpenBao dev mode, bootstraps a root-owned project and API token
inside GitLab, registers and mounts the plugin, then verifies create, update,
delete, reconcile/read-state, ownership metadata, payload hash metadata, queue
drain, and status transitions against the real GitLab API.

The GitLab container is deliberately not part of default CI. It is large, slow
to boot, and intended for local provider confidence checks.

The default GitLab image is pinned to `gitlab/gitlab-ce:18.7.1-ce.0` for
repeatability. Override `E2E_GITLAB_IMAGE` when testing GitLab upgrades.

## Run

```sh
E2E_GITLAB_CONFIRM=1 make test-e2e-gitlab
```

The default host ports are:

- OpenBao: `127.0.0.1:18203`
- GitLab: `127.0.0.1:18080`

If either port is already in use:

```sh
E2E_GITLAB_CONFIRM=1 \
E2E_GITLAB_OPENBAO_PORT=18213 \
E2E_GITLAB_OPENBAO_ADDR=http://127.0.0.1:18213 \
E2E_GITLAB_PORT=18090 \
E2E_GITLAB_URL=http://127.0.0.1:18090 \
make test-e2e-gitlab
```

## Useful Targets

```sh
make e2e-build-plugin
E2E_GITLAB_CONFIRM=1 make e2e-gitlab-up
make e2e-gitlab-down
E2E_GITLAB_CONFIRM=1 make test-e2e-gitlab
```

`make test-e2e-gitlab` tears the stack down automatically. Use
`E2E_GITLAB_CONFIRM=1 make e2e-gitlab-up` and `make e2e-gitlab-down` when
debugging the running OpenBao or GitLab services.

## Bootstrap

The local fixture uses `gitlab-rails runner` to create:

- project `root/openbao-secret-sync-e2e`;
- root personal access token
  `glpat-openbao-secret-sync-e2e-token-000000`.

The default root password is intentionally random-looking because GitLab
rejects common word combinations during first boot. If you override
`E2E_GITLAB_ROOT_PASSWORD`, choose a password that satisfies GitLab's password
quality checks.

The OpenBao container reaches GitLab over the Docker network at
`http://gitlab`. The e2e destination sets `allow_insecure_http=true` explicitly
for that local Docker-only URL. Production and default GitLab destinations
should use HTTPS.

## OpenTofu

OpenTofu is intentionally not used for this local fixture. A new GitLab
container needs an initial admin token before the GitLab provider can
authenticate, so the Rails bootstrap is the simpler self-contained path.

For a later opt-in real GitLab e2e fixture, OpenTofu can manage a disposable
project, project access token, and cleanup flow, similar to the manual AWS
fixture.

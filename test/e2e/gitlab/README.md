# GitLab E2E Tests

This directory contains the opt-in self-contained OpenBao plus GitLab e2e test
for the GitLab project variable provider.

The workflow builds the Linux plugin binary, starts a disposable GitLab CE
container and OpenBao dev mode, bootstraps a root-owned project and API token
inside GitLab, registers and mounts the plugin, then verifies create, update,
delete, reconcile/read-state, ownership metadata, payload hash metadata, queue
drain, and status transitions against the real GitLab API.

The GitLab fixture is opt-in for ordinary PRs and required for preview
qualification. Record the candidate commit, resolved images, and server
versions with the test results.

The default GitLab image is pinned to `gitlab/gitlab-ce:19.5.1-ce.0` with a
manifest digest for repeatability. Override `E2E_GITLAB_IMAGE` when testing
GitLab upgrades.

## Qualification coverage

The Make target runs these sequential subtests. Each scenario mounts a fresh
plugin and cleans up its test variables. Do not run the scenarios in parallel
because they share one OpenBao mount.

| Scenario | Required observations |
| --- | --- |
| Basic lifecycle | Create, source rotation, owned delete, protected and masked attributes, drift repair, metadata, reservations, status, and queue drain pass. |
| File variables | Multiline values and `variable_raw=false` survive create and rotation; owned delete removes the variable. |
| Hidden variables | Fixture-side hashes verify create and rotation. Stable plans use no-op and reconcile reports metadata verification. External value changes that preserve metadata remain undetected. Owned delete removes the variable. |
| Visible-to-hidden change | Plan reports validation failure, queued upsert fails, and all remote fields remain unchanged. |
| Independent scopes | Two associations manage the same key in different scopes. Updating or deleting one leaves the other unchanged. |
| Same-source scopes | One source and destination use two scoped associations. Scoped updates preserve the other variable, unscoped lifecycle selection fails as ambiguous, and removal by association ID retains the other association and both remote variables. Later source changes affect only the remaining association. |
| Ownership loss and foreign collision | Plan reports a collision. Upsert and delete fail with ownership errors and preserve the foreign value, description, and attributes. |
| Retain and orphan | Source deletion queues no remote operation. The variable and ownership description remain unchanged. |
| Credential refresh | Invalid replacement credentials block sync. Restoring valid credentials and manually syncing repairs the current source version. |

The tests verify variable attributes through the
[GitLab project variables API](https://docs.gitlab.com/api/project_level_variables/).
Hidden values are unavailable through that API. The hidden scenario uses the
local fixture administrator to compare stored value hashes through Rails. Only
boolean results leave GitLab; this fixture check does not add administrator
access or independent hidden-value readback to the plugin. It requires one
running GitLab Compose service whose working-directory label matches this test
directory.

They do not start CI jobs or qualify runner-side masking, variable expansion,
or file delivery. GitLab permits hidden variables only when creating them;
see [Hide a CI/CD variable](https://docs.gitlab.com/ci/variables/#hide-a-cicd-variable).

Compiling the tests does not qualify a provider version. Retain a successful
real API run with its candidate commit and server versions before recording
preview qualification.

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

- project `root/openbao-plugin-secrets-sync-e2e`;
- root personal access token
  `glpat-openbao-plugin-secrets-sync-e2e-token-000000`.

The default root password is intentionally random-looking because GitLab
rejects common word combinations during first boot. If you override
`E2E_GITLAB_ROOT_PASSWORD`, choose a password that satisfies GitLab's password
quality checks.

The OpenBao container reaches GitLab over the Docker network at
`http://gitlab`. The e2e destination sets `allow_insecure_http=true` and
`allow_private_network=true` explicitly for that local Docker-only URL.
Production and default GitLab destinations should use HTTPS.

## OpenTofu

OpenTofu is intentionally not used for this local fixture. A new GitLab
container needs an initial admin token before the GitLab provider can
authenticate, so the Rails bootstrap is the simpler self-contained path.

For a later opt-in real GitLab e2e fixture, OpenTofu can manage a disposable
project, project access token, and cleanup flow, similar to the manual AWS
fixture.

# Contributing

`openbao-plugin-secrets-sync` is an early-stage OpenBao secret engine plugin.
Keep changes small, reviewable, and covered by the closest relevant tests.

## Commit messages

Use conventional commits with a scope:

```text
feat(provider): add example capability
fix(queue): preserve retry metadata
docs(security): clarify ownership checks
```

Sign off every commit with the Developer Certificate of Origin:

```sh
git commit -s
```

## Local checks

Run the smallest useful check while developing:

```sh
make ci-fast
```

Before opening or merging a non-trivial change, run the full local gate:

```sh
make ci-core
```

Pull request CI runs the self-contained LocalStack e2e test. Use additional
provider-specific e2e targets when a change touches that provider or its
fixture:

```sh
make test-e2e
make test-e2e-kind
E2E_GITLAB_CONFIRM=1 make test-e2e-gitlab
```

Manual real-provider tests are opt-in and must not require credentials to run
normal CI.

Optional Git hooks are available:

```sh
make install-git-hooks
```

Set `OPENBAO_SECRET_SYNC_SKIP_HOOKS=1` to bypass local hooks for a one-off
operation. CI remains the source of truth.

## Security-sensitive changes

Do not commit plaintext secrets, OpenBao tokens, API tokens, full ciphertexts,
or production endpoints. Redact command output before adding it to issues, pull
requests, docs, or tests.

When changing provider behavior, keep the provider contract and conformance
tests in sync.

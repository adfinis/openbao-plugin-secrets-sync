##@ CI

.PHONY: ci
ci: ci-core ## Run the standard local CI gate.

.PHONY: ci-core
ci-core: verify-tidy lint security-ci test test-race fuzz build release-artifacts ## Run the local core quality gate.

.PHONY: ci-fast
ci-fast: verify-tidy docs-check versions-check verify-fmt vet test build ## Run a fast local gate suitable for pre-push checks.

.PHONY: ci-heavy
ci-heavy: test-race fuzz release-artifacts ## Run heavier runtime and release-artifact checks.

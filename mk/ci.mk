##@ CI

.PHONY: ci
ci: ci-core ## Run the standard local CI gate.

.PHONY: ci-core
ci-core: verify-tidy lint security-ci test test-race fuzz build release-artifacts ## Run the local core quality gate.

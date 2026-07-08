##@ Development

.PHONY: fmt
fmt: ## Format Go sources.
	@if [ -n "$(GO_SOURCE_DIRS)" ] && find $(GO_SOURCE_DIRS) -name '*.go' | grep -q .; then \
		gofmt -w $$(find $(GO_SOURCE_DIRS) -name '*.go'); \
		if command -v "$(GOFUMPT)" >/dev/null 2>&1; then "$(GOFUMPT)" -w $(GO_SOURCE_DIRS); fi; \
	else \
		printf '%s\n' 'No Go files yet; skipping Go formatting.'; \
	fi

.PHONY: verify-fmt
verify-fmt: ## Verify Go formatting without modifying files.
	@if [ -n "$(GO_SOURCE_DIRS)" ] && find $(GO_SOURCE_DIRS) -name '*.go' | grep -q .; then \
		unformatted="$$(gofmt -l $$(find $(GO_SOURCE_DIRS) -name '*.go'))"; \
		if [ -n "$$unformatted" ]; then printf '%s\n' "$$unformatted"; exit 1; fi; \
		if command -v "$(GOFUMPT)" >/dev/null 2>&1; then \
			unformatted="$$("$(GOFUMPT)" -l $(GO_SOURCE_DIRS))"; \
			if [ -n "$$unformatted" ]; then printf '%s\n' "$$unformatted"; exit 1; fi; \
		elif [ "$(LINT_STRICT)" = "1" ]; then \
			printf '%s\n' 'gofumpt not installed; run make install-go-tools or set LINT_STRICT=0 to skip.'; \
			exit 1; \
		else \
			printf '%s\n' 'gofumpt not installed; skipping gofumpt verification.'; \
		fi; \
	else \
		printf '%s\n' 'No Go files yet; skipping Go formatting verification.'; \
	fi

.PHONY: vet
vet: ## Run go vet.
	@"$(GO)" vet ./...

.PHONY: workflow-lint
workflow-lint: ## Validate GitHub Actions workflows when actionlint is installed.
	@if command -v "$(ACTIONLINT)" >/dev/null 2>&1; then \
		"$(ACTIONLINT)" .github/workflows/*.yml; \
	elif [ "$(LINT_STRICT)" = "1" ]; then \
		printf '%s\n' 'actionlint not installed; run make install-go-tools or set LINT_STRICT=0 to skip.'; \
		exit 1; \
	else \
		printf '%s\n' 'actionlint not installed; skipping workflow lint.'; \
	fi

.PHONY: lint
lint: LINT_STRICT = 1
lint: docs-check versions-check verify-fmt workflow-lint semgrep-ci vet ## Run lint checks.
	@if command -v "$(GOLANGCI_LINT)" >/dev/null 2>&1; then \
		"$(GOLANGCI_LINT)" run; \
	else \
		printf '%s\n' 'golangci-lint not installed; run make install-go-tools.'; \
		exit 1; \
	fi

.PHONY: lint-ci
lint-ci: lint vulncheck ## Run lint plus vulnerability checks.

.PHONY: test
test: ## Run Go tests.
	@"$(GO)" test ./...

.PHONY: test-race
test-race: ## Run race-enabled Go tests.
	@"$(GO)" test -race ./...

.PHONY: tidy
tidy: ## Run go mod tidy.
	@GOFLAGS="-mod=mod" "$(GO)" mod tidy

.PHONY: vendor
vendor: ## Refresh vendor/.
	@GOFLAGS="-mod=mod" "$(GO)" mod vendor

.PHONY: verify-tidy
verify-tidy: ## Verify go.mod and go.sum are tidy.
	@tmp="$$(mktemp -d)"; \
	trap 'rm -rf "$$tmp"' EXIT; \
	cp go.mod "$$tmp/go.mod"; \
	cp go.sum "$$tmp/go.sum"; \
	GOFLAGS="-mod=mod" "$(GO)" mod tidy; \
	cmp -s go.mod "$$tmp/go.mod" && cmp -s go.sum "$$tmp/go.sum"

.PHONY: verify-vendor
verify-vendor: ## Verify vendor/ is synchronized with go.mod and go.sum when vendor exists.
	@if [ ! -d vendor ]; then \
		printf '%s\n' 'vendor/ not present; skipping vendor verification.'; \
		exit 0; \
	fi
	@tmp="$$(mktemp -d)"; \
	trap 'rm -rf "$$tmp"' EXIT; \
	cp go.mod "$$tmp/go.mod"; \
	cp go.sum "$$tmp/go.sum"; \
	cp -R vendor "$$tmp/vendor"; \
	GOFLAGS="-mod=mod" "$(GO)" mod vendor; \
	cmp -s go.mod "$$tmp/go.mod" && cmp -s go.sum "$$tmp/go.sum" && diff -qr "$$tmp/vendor" vendor >/dev/null

.PHONY: fuzz
fuzz: ## Run curated fuzz smoke targets.
	@set -eu; \
	for target in $(FUZZ_TARGETS); do \
		pkg="$${target%%:*}"; \
		fuzz="$${target#*:}"; \
		printf '==> fuzz %s %s\n' "$$pkg" "$$fuzz"; \
		"$(GO)" test "$$pkg" -run='^$$' -fuzz="$$fuzz" -fuzztime="$(FUZZTIME)"; \
	done

.PHONY: versions-check
versions-check: ## Check central version policy exists and contains no floating latest.
	@test -f .ci/versions.yaml
	@! grep -R -n 'latest' .ci/versions.yaml

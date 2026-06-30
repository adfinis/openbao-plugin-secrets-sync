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
		else \
			printf '%s\n' 'gofumpt not installed; skipping gofumpt verification.'; \
		fi; \
	else \
		printf '%s\n' 'No Go files yet; skipping Go formatting verification.'; \
	fi

.PHONY: lint
lint: docs-check versions-check verify-fmt semgrep-ci ## Run lightweight lint checks.
	@"$(GO)" vet ./...
	@if command -v "$(STATICCHECK)" >/dev/null 2>&1; then "$(STATICCHECK)" ./...; else printf '%s\n' 'staticcheck not installed; skipping staticcheck.'; fi
	@if command -v "$(GOLANGCI_LINT)" >/dev/null 2>&1; then "$(GOLANGCI_LINT)" run; else printf '%s\n' 'golangci-lint not installed; skipping golangci-lint.'; fi

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
	@printf '%s\n' 'No fuzz targets yet; skipping fuzz.'

.PHONY: versions-check
versions-check: ## Check central version policy exists and contains no floating latest.
	@test -f .ci/versions.yaml
	@! grep -R -n 'latest' .ci/versions.yaml

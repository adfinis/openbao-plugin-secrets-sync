##@ Tooling

.PHONY: bootstrap
bootstrap: ## Prepare local development prerequisites.
	@printf 'Go toolchain: %s\n' '$(GO_VERSION)'
	@$(MAKE) install-go-tools

.PHONY: install-go-tools
install-go-tools: ## Install pinned optional Go quality tools into bin/.
	@mkdir -p "$(GOBIN)"
	@env -u GOFLAGS GOBIN="$(GOBIN)" "$(GO)" install mvdan.cc/gofumpt@$(GOFUMPT_VERSION)
	@env -u GOFLAGS GOBIN="$(GOBIN)" "$(GO)" install honnef.co/go/tools/cmd/staticcheck@$(STATICCHECK_VERSION)
	@env -u GOFLAGS GOBIN="$(GOBIN)" "$(GO)" install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)
	@env -u GOFLAGS GOBIN="$(GOBIN)" "$(GO)" install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	@env -u GOFLAGS GOBIN="$(GOBIN)" "$(GO)" install github.com/google/go-licenses/v2@$(GO_LICENSES_VERSION)
	@env -u GOFLAGS GOBIN="$(GOBIN)" "$(GO)" install github.com/rhysd/actionlint/cmd/actionlint@$(ACTIONLINT_VERSION)

.PHONY: install-git-hooks
install-git-hooks: ## Install opt-in repo-local Git hooks.
	@test -d .git || { printf '%s\n' 'not a Git worktree'; exit 1; }
	@mkdir -p .git/hooks
	@cp hack/git-hooks/pre-commit .git/hooks/pre-commit
	@cp hack/git-hooks/pre-push .git/hooks/pre-push
	@chmod 0755 .git/hooks/pre-commit .git/hooks/pre-push
	@printf '%s\n' 'Installed pre-commit and pre-push hooks.'

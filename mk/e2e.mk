##@ E2E

DOCKER_COMPOSE ?= docker compose
E2E_COMPOSE_FILE ?= test/e2e/localstack/compose.yaml
E2E_OPENBAO_PORT ?= 18200
E2E_LOCALSTACK_PORT ?= 4566
E2E_OPENBAO_ADDR ?= http://127.0.0.1:$(E2E_OPENBAO_PORT)
E2E_LOCALSTACK_ENDPOINT ?= http://127.0.0.1:$(E2E_LOCALSTACK_PORT)
E2E_PLUGIN_DIR ?= $(CURDIR)/bin/e2e
E2E_PLUGIN_BIN ?= $(E2E_PLUGIN_DIR)/$(BINARY_NAME)
E2E_PLUGIN_VERSION ?= v0.0.0-dev
E2E_GOARCH ?= $(shell "$(GO)" env GOHOSTARCH)
E2E_LDFLAGS := -s -w -X $(VERSION_PKG).version=$(E2E_PLUGIN_VERSION) -X $(VERSION_PKG).commit=$(COMMIT) -X $(VERSION_PKG).buildDate=$(BUILD_DATE) -X $(VERSION_PKG).dirty=$(DIRTY)

.PHONY: e2e-build-plugin
e2e-build-plugin: ## Build the Linux plugin binary used by the OpenBao e2e container.
	@mkdir -p "$(E2E_PLUGIN_DIR)"
	@CGO_ENABLED=0 GOOS=linux GOARCH="$(E2E_GOARCH)" "$(GO)" build $(GO_BUILD_FLAGS) -ldflags "$(E2E_LDFLAGS)" -o "$(E2E_PLUGIN_BIN)" ./cmd/openbao-plugin-secrets-sync
	@chmod 0755 "$(E2E_PLUGIN_BIN)"

.PHONY: e2e-up
e2e-up: e2e-build-plugin ## Start the self-contained OpenBao plus LocalStack e2e stack.
	@E2E_OPENBAO_PORT="$(E2E_OPENBAO_PORT)" E2E_LOCALSTACK_PORT="$(E2E_LOCALSTACK_PORT)" $(DOCKER_COMPOSE) -f "$(E2E_COMPOSE_FILE)" up -d --wait

.PHONY: e2e-down
e2e-down: ## Stop the self-contained OpenBao plus LocalStack e2e stack.
	@E2E_OPENBAO_PORT="$(E2E_OPENBAO_PORT)" E2E_LOCALSTACK_PORT="$(E2E_LOCALSTACK_PORT)" $(DOCKER_COMPOSE) -f "$(E2E_COMPOSE_FILE)" down -v --remove-orphans

.PHONY: test-e2e
test-e2e: e2e-build-plugin ## Run self-contained OpenBao plus LocalStack e2e tests.
	@set -eu; \
	E2E_OPENBAO_PORT="$(E2E_OPENBAO_PORT)" E2E_LOCALSTACK_PORT="$(E2E_LOCALSTACK_PORT)" $(DOCKER_COMPOSE) -f "$(E2E_COMPOSE_FILE)" up -d --wait; \
	trap 'E2E_OPENBAO_PORT="$(E2E_OPENBAO_PORT)" E2E_LOCALSTACK_PORT="$(E2E_LOCALSTACK_PORT)" $(DOCKER_COMPOSE) -f "$(E2E_COMPOSE_FILE)" down -v --remove-orphans' EXIT; \
	AWS_ACCESS_KEY_ID=test \
	AWS_SECRET_ACCESS_KEY=test \
	AWS_REGION=us-east-1 \
	AWS_DEFAULT_REGION=us-east-1 \
	E2E_OPENBAO_ADDR="$(E2E_OPENBAO_ADDR)" \
	E2E_LOCALSTACK_ENDPOINT="$(E2E_LOCALSTACK_ENDPOINT)" \
	E2E_PLUGIN_PATH="$(E2E_PLUGIN_BIN)" \
	"$(GO)" test -tags=e2e ./test/e2e/localstack -count=1 -v

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
E2E_AWS_COMPOSE_FILE ?= test/e2e/aws/compose.yaml
E2E_AWS_OPENBAO_PORT ?= 18201
E2E_AWS_OPENBAO_ADDR ?= http://127.0.0.1:$(E2E_AWS_OPENBAO_PORT)
E2E_AWS_REGION ?= us-east-1
E2E_AWS_SECRET_PREFIX ?= openbao-secret-sync-manual/
E2E_AWS_CONFIRM ?=
E2E_AWS_CLEAN_CONFIRM ?=

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

.PHONY: e2e-aws-up
e2e-aws-up: e2e-build-plugin ## Start the manual real-AWS e2e OpenBao stack.
	@if [ "$(E2E_AWS_CONFIRM)" != "1" ]; then echo "set E2E_AWS_CONFIRM=1 to start the manual AWS e2e stack"; exit 2; fi
	@if [ -z "$$AWS_ACCESS_KEY_ID" ] || [ -z "$$AWS_SECRET_ACCESS_KEY" ]; then echo "run this target under aws-vault or provide AWS credentials"; exit 2; fi
	@E2E_AWS_OPENBAO_PORT="$(E2E_AWS_OPENBAO_PORT)" AWS_REGION="$(E2E_AWS_REGION)" AWS_DEFAULT_REGION="$(E2E_AWS_REGION)" $(DOCKER_COMPOSE) -f "$(E2E_AWS_COMPOSE_FILE)" up -d --wait

.PHONY: e2e-aws-down
e2e-aws-down: ## Stop the manual real-AWS e2e OpenBao stack.
	@E2E_AWS_OPENBAO_PORT="$(E2E_AWS_OPENBAO_PORT)" $(DOCKER_COMPOSE) -f "$(E2E_AWS_COMPOSE_FILE)" down -v --remove-orphans

.PHONY: test-e2e-aws
test-e2e-aws: e2e-build-plugin ## Run the opt-in manual e2e test against real AWS Secrets Manager.
	@if [ "$(E2E_AWS_CONFIRM)" != "1" ]; then echo "set E2E_AWS_CONFIRM=1 to run the manual AWS e2e test"; exit 2; fi
	@if [ -z "$(E2E_AWS_ROLE_ARN)" ]; then echo "set E2E_AWS_ROLE_ARN from the OpenTofu output"; exit 2; fi
	@if [ -z "$(E2E_AWS_EXTERNAL_ID)" ]; then echo "set E2E_AWS_EXTERNAL_ID from the OpenTofu output"; exit 2; fi
	@if [ -z "$$AWS_ACCESS_KEY_ID" ] || [ -z "$$AWS_SECRET_ACCESS_KEY" ]; then echo "run this target under aws-vault or provide AWS credentials"; exit 2; fi
	@set -eu; \
	E2E_AWS_OPENBAO_PORT="$(E2E_AWS_OPENBAO_PORT)" AWS_REGION="$(E2E_AWS_REGION)" AWS_DEFAULT_REGION="$(E2E_AWS_REGION)" $(DOCKER_COMPOSE) -f "$(E2E_AWS_COMPOSE_FILE)" up -d --wait; \
	trap 'E2E_AWS_OPENBAO_PORT="$(E2E_AWS_OPENBAO_PORT)" $(DOCKER_COMPOSE) -f "$(E2E_AWS_COMPOSE_FILE)" down -v --remove-orphans' EXIT; \
	AWS_REGION="$(E2E_AWS_REGION)" \
	AWS_DEFAULT_REGION="$(E2E_AWS_REGION)" \
	E2E_AWS_CONFIRM="$(E2E_AWS_CONFIRM)" \
	E2E_AWS_OPENBAO_ADDR="$(E2E_AWS_OPENBAO_ADDR)" \
	E2E_AWS_ROLE_ARN="$(E2E_AWS_ROLE_ARN)" \
	E2E_AWS_EXTERNAL_ID="$(E2E_AWS_EXTERNAL_ID)" \
	E2E_AWS_REGION="$(E2E_AWS_REGION)" \
	E2E_AWS_SECRET_PREFIX="$(E2E_AWS_SECRET_PREFIX)" \
	E2E_PLUGIN_PATH="$(E2E_PLUGIN_BIN)" \
	"$(GO)" test -tags=e2e ./test/e2e/aws -run TestOpenBaoPluginSyncsToAWSSecretsManager -count=1 -v

.PHONY: test-e2e-aws-clean
test-e2e-aws-clean: ## Force-delete manual AWS e2e secrets under E2E_AWS_SECRET_PREFIX.
	@if [ "$(E2E_AWS_CLEAN_CONFIRM)" != "1" ]; then echo "set E2E_AWS_CLEAN_CONFIRM=1 to force-delete manual AWS e2e secrets"; exit 2; fi
	@if [ -z "$(E2E_AWS_ROLE_ARN)" ]; then echo "set E2E_AWS_ROLE_ARN from the OpenTofu output"; exit 2; fi
	@if [ -z "$(E2E_AWS_EXTERNAL_ID)" ]; then echo "set E2E_AWS_EXTERNAL_ID from the OpenTofu output"; exit 2; fi
	@AWS_REGION="$(E2E_AWS_REGION)" \
	AWS_DEFAULT_REGION="$(E2E_AWS_REGION)" \
	E2E_AWS_CLEAN_CONFIRM="$(E2E_AWS_CLEAN_CONFIRM)" \
	E2E_AWS_ROLE_ARN="$(E2E_AWS_ROLE_ARN)" \
	E2E_AWS_EXTERNAL_ID="$(E2E_AWS_EXTERNAL_ID)" \
	E2E_AWS_REGION="$(E2E_AWS_REGION)" \
	E2E_AWS_SECRET_PREFIX="$(E2E_AWS_SECRET_PREFIX)" \
	"$(GO)" test -tags=e2e ./test/e2e/aws -run TestCleanupAWSSecrets -count=1 -v

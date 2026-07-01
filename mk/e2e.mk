##@ E2E

DOCKER_COMPOSE ?= docker compose
DOCKER ?= docker
KIND ?= kind
KUBECTL ?= kubectl
E2E_COMPOSE_FILE ?= test/e2e/localstack/compose.yaml
E2E_OPENBAO_IMAGE ?= ghcr.io/openbao/openbao:2.5.5@sha256:6150c4a6b62067db6141c8da7a6a6b5763f4f47c315343d0c848b40fecdfd452
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
E2E_KIND_CLUSTER ?= openbao-secret-sync-e2e
E2E_KIND_CONTEXT ?= kind-$(E2E_KIND_CLUSTER)
E2E_KIND_NAMESPACE ?= openbao-secret-sync-e2e
E2E_KIND_IMAGE ?= openbao-secret-sync-e2e:dev
E2E_KIND_DOCKERFILE ?= test/e2e/kind/Dockerfile
E2E_KIND_MANIFEST_DIR ?= test/e2e/kind/manifests
E2E_KIND_OPENBAO_PORT ?= 18202
E2E_KIND_OPENBAO_ADDR ?= http://127.0.0.1:$(E2E_KIND_OPENBAO_PORT)
E2E_GITLAB_COMPOSE_FILE ?= test/e2e/gitlab/compose.yaml
E2E_GITLAB_IMAGE ?= gitlab/gitlab-ce:18.7.1-ce.0
E2E_GITLAB_OPENBAO_PORT ?= 18203
E2E_GITLAB_PORT ?= 18080
E2E_GITLAB_OPENBAO_ADDR ?= http://127.0.0.1:$(E2E_GITLAB_OPENBAO_PORT)
E2E_GITLAB_URL ?= http://127.0.0.1:$(E2E_GITLAB_PORT)
E2E_GITLAB_BASE_URL_IN_BAO ?= http://gitlab
E2E_GITLAB_PROJECT_PATH ?= root/openbao-secret-sync-e2e
E2E_GITLAB_ENVIRONMENT_SCOPE ?= production
E2E_GITLAB_TOKEN ?= glpat-openbao-secret-sync-e2e-token-000000
E2E_GITLAB_ROOT_PASSWORD ?= R8vQ2mT6pL9sX4zC7nY3
E2E_GITLAB_CONFIRM ?=

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

.PHONY: e2e-kind-image
e2e-kind-image: e2e-build-plugin ## Build the OpenBao image used by kind e2e tests.
	@$(DOCKER) build \
		--build-arg OPENBAO_IMAGE="$(E2E_OPENBAO_IMAGE)" \
		-f "$(E2E_KIND_DOCKERFILE)" \
		-t "$(E2E_KIND_IMAGE)" \
		.

.PHONY: e2e-kind-up
e2e-kind-up: e2e-kind-image ## Create the kind e2e cluster and deploy OpenBao with the plugin.
	@command -v "$(KIND)" >/dev/null 2>&1 || { echo "kind is required for e2e-kind-up"; exit 2; }
	@command -v "$(KUBECTL)" >/dev/null 2>&1 || { echo "kubectl is required for e2e-kind-up"; exit 2; }
	@set -eu; \
	if ! "$(KIND)" get clusters | grep -qx "$(E2E_KIND_CLUSTER)"; then \
		"$(KIND)" create cluster --name "$(E2E_KIND_CLUSTER)"; \
	fi; \
	"$(KIND)" load docker-image "$(E2E_KIND_IMAGE)" --name "$(E2E_KIND_CLUSTER)"; \
	"$(KUBECTL)" --context "$(E2E_KIND_CONTEXT)" create namespace "$(E2E_KIND_NAMESPACE)" --dry-run=client -o yaml | \
		"$(KUBECTL)" --context "$(E2E_KIND_CONTEXT)" apply -f -; \
	"$(KUBECTL)" --context "$(E2E_KIND_CONTEXT)" -n "$(E2E_KIND_NAMESPACE)" apply -f "$(E2E_KIND_MANIFEST_DIR)"; \
	"$(KUBECTL)" --context "$(E2E_KIND_CONTEXT)" -n "$(E2E_KIND_NAMESPACE)" set image deployment/openbao openbao="$(E2E_KIND_IMAGE)"; \
	"$(KUBECTL)" --context "$(E2E_KIND_CONTEXT)" -n "$(E2E_KIND_NAMESPACE)" rollout restart deployment/openbao; \
	"$(KUBECTL)" --context "$(E2E_KIND_CONTEXT)" -n "$(E2E_KIND_NAMESPACE)" rollout status deployment/openbao --timeout=120s

.PHONY: e2e-kind-down
e2e-kind-down: ## Delete the kind e2e cluster.
	@command -v "$(KIND)" >/dev/null 2>&1 || exit 0
	@$(KIND) delete cluster --name "$(E2E_KIND_CLUSTER)"

.PHONY: test-e2e-kind
test-e2e-kind: ## Run self-contained OpenBao plus kind e2e tests for Kubernetes Secrets.
	@set -eu; \
	port_forward_pid=""; \
	port_forward_log=""; \
	cleanup() { \
		if [ -n "$$port_forward_pid" ]; then \
			kill "$$port_forward_pid" >/dev/null 2>&1 || true; \
			wait "$$port_forward_pid" >/dev/null 2>&1 || true; \
		fi; \
		if [ -n "$$port_forward_log" ]; then rm -f "$$port_forward_log"; fi; \
		$(MAKE) e2e-kind-down; \
	}; \
	trap cleanup EXIT; \
	$(MAKE) e2e-kind-up; \
	port_forward_log="$$(mktemp)"; \
	"$(KUBECTL)" --context "$(E2E_KIND_CONTEXT)" -n "$(E2E_KIND_NAMESPACE)" \
		port-forward svc/openbao "$(E2E_KIND_OPENBAO_PORT):8200" >"$$port_forward_log" 2>&1 & \
	port_forward_pid="$$!"; \
	for _ in $$(seq 1 30); do \
		if grep -q "Forwarding from" "$$port_forward_log"; then break; fi; \
		if ! kill -0 "$$port_forward_pid" >/dev/null 2>&1; then cat "$$port_forward_log"; exit 1; fi; \
		sleep 1; \
	done; \
	if ! grep -q "Forwarding from" "$$port_forward_log"; then cat "$$port_forward_log"; exit 1; fi; \
	E2E_KIND_OPENBAO_ADDR="$(E2E_KIND_OPENBAO_ADDR)" \
	E2E_KIND_NAMESPACE="$(E2E_KIND_NAMESPACE)" \
	E2E_KIND_CONTEXT="$(E2E_KIND_CONTEXT)" \
	E2E_PLUGIN_PATH="$(E2E_PLUGIN_BIN)" \
	"$(GO)" test -tags=e2e ./test/e2e/kind -count=1 -v

.PHONY: e2e-gitlab-up
e2e-gitlab-up: e2e-build-plugin ## Start the opt-in OpenBao plus GitLab e2e stack and bootstrap GitLab.
	@if [ "$(E2E_GITLAB_CONFIRM)" != "1" ]; then echo "set E2E_GITLAB_CONFIRM=1 to start the GitLab e2e stack"; exit 2; fi
	@set -eu; \
	E2E_OPENBAO_IMAGE="$(E2E_OPENBAO_IMAGE)" \
	E2E_GITLAB_IMAGE="$(E2E_GITLAB_IMAGE)" \
	E2E_GITLAB_OPENBAO_PORT="$(E2E_GITLAB_OPENBAO_PORT)" \
	E2E_GITLAB_PORT="$(E2E_GITLAB_PORT)" \
	E2E_GITLAB_PROJECT_PATH="$(E2E_GITLAB_PROJECT_PATH)" \
	E2E_GITLAB_ENVIRONMENT_SCOPE="$(E2E_GITLAB_ENVIRONMENT_SCOPE)" \
	E2E_GITLAB_TOKEN="$(E2E_GITLAB_TOKEN)" \
	E2E_GITLAB_ROOT_PASSWORD="$(E2E_GITLAB_ROOT_PASSWORD)" \
	$(DOCKER_COMPOSE) -f "$(E2E_GITLAB_COMPOSE_FILE)" up -d --wait; \
	for _ in $$(seq 1 30); do \
		if E2E_GITLAB_PROJECT_PATH="$(E2E_GITLAB_PROJECT_PATH)" \
			E2E_GITLAB_TOKEN="$(E2E_GITLAB_TOKEN)" \
			E2E_GITLAB_ENVIRONMENT_SCOPE="$(E2E_GITLAB_ENVIRONMENT_SCOPE)" \
			$(DOCKER_COMPOSE) -f "$(E2E_GITLAB_COMPOSE_FILE)" exec -T gitlab \
				gitlab-rails runner /openbao-e2e/bootstrap.rb; then \
			exit 0; \
		fi; \
		sleep 10; \
	done; \
	echo "GitLab e2e bootstrap failed"; \
	exit 1

.PHONY: e2e-gitlab-down
e2e-gitlab-down: ## Stop the opt-in OpenBao plus GitLab e2e stack.
	@E2E_GITLAB_OPENBAO_PORT="$(E2E_GITLAB_OPENBAO_PORT)" E2E_GITLAB_PORT="$(E2E_GITLAB_PORT)" \
		E2E_OPENBAO_IMAGE="$(E2E_OPENBAO_IMAGE)" E2E_GITLAB_IMAGE="$(E2E_GITLAB_IMAGE)" \
		$(DOCKER_COMPOSE) -f "$(E2E_GITLAB_COMPOSE_FILE)" down -v --remove-orphans

.PHONY: test-e2e-gitlab
test-e2e-gitlab: e2e-build-plugin ## Run the opt-in self-contained GitLab project variable e2e test.
	@if [ "$(E2E_GITLAB_CONFIRM)" != "1" ]; then echo "set E2E_GITLAB_CONFIRM=1 to run the GitLab e2e test"; exit 2; fi
	@set -eu; \
	cleanup() { $(MAKE) e2e-gitlab-down; }; \
	trap cleanup EXIT; \
	$(MAKE) e2e-gitlab-up; \
	E2E_GITLAB_OPENBAO_ADDR="$(E2E_GITLAB_OPENBAO_ADDR)" \
	E2E_GITLAB_URL="$(E2E_GITLAB_URL)" \
	E2E_GITLAB_BASE_URL_IN_BAO="$(E2E_GITLAB_BASE_URL_IN_BAO)" \
	E2E_GITLAB_PROJECT_PATH="$(E2E_GITLAB_PROJECT_PATH)" \
	E2E_GITLAB_ENVIRONMENT_SCOPE="$(E2E_GITLAB_ENVIRONMENT_SCOPE)" \
	E2E_GITLAB_TOKEN="$(E2E_GITLAB_TOKEN)" \
	E2E_PLUGIN_PATH="$(E2E_PLUGIN_BIN)" \
	"$(GO)" test -tags=e2e ./test/e2e/gitlab -run TestOpenBaoPluginSyncsToGitLabProjectVariables -count=1 -v

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

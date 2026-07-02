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
E2E_GOARCH ?= $(shell "$(GO)" env GOHOSTARCH)
E2E_RELEASE_PLUGIN_BIN ?= $(DIST_DIR)/$(BINARY_NAME)_$(VERSION)_linux_$(E2E_GOARCH)
E2E_PLUGIN_VERSION ?= v0.0.0-dev
E2E_LDFLAGS := -s -w -X $(VERSION_PKG).version=$(E2E_PLUGIN_VERSION) -X $(VERSION_PKG).commit=$(COMMIT) -X $(VERSION_PKG).buildDate=$(BUILD_DATE) -X $(VERSION_PKG).dirty=$(DIRTY)
E2E_AWS_COMPOSE_FILE ?= test/e2e/aws/compose.yaml
E2E_AWS_OPENBAO_PORT ?= 18201
E2E_AWS_OPENBAO_ADDR ?= http://127.0.0.1:$(E2E_AWS_OPENBAO_PORT)
E2E_AWS_REGION ?= us-east-1
E2E_AWS_SECRET_PREFIX ?= openbao-plugin-secrets-sync-manual/
E2E_AWS_CONFIRM ?=
E2E_AWS_CLEAN_CONFIRM ?=
E2E_KIND_CLUSTER ?= openbao-plugin-secrets-sync-e2e
E2E_KIND_CONTEXT ?= kind-$(E2E_KIND_CLUSTER)
E2E_KIND_NAMESPACE ?= openbao-plugin-secrets-sync-e2e
E2E_KIND_IMAGE ?= openbao-plugin-secrets-sync-e2e:dev
E2E_KIND_NODE_IMAGE ?= kindest/node:v1.35.0
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
E2E_GITLAB_PROJECT_PATH ?= root/openbao-plugin-secrets-sync-e2e
E2E_GITLAB_ENVIRONMENT_SCOPE ?= production
E2E_GITLAB_TOKEN ?= glpat-openbao-plugin-secrets-sync-e2e-token-000000
E2E_GITLAB_ROOT_PASSWORD ?= R8vQ2mT6pL9sX4zC7nY3
E2E_GITLAB_CONFIRM ?=
E2E_OCI_COMPOSE_FILE ?= test/e2e/oci/compose.yaml
E2E_OCI_DIR ?= dist/e2e/oci
E2E_OCI_DIST_DIR ?= $(E2E_OCI_DIR)/release
E2E_OCI_VERSION ?= $(patsubst v%,%,$(E2E_PLUGIN_VERSION))
E2E_OCI_RELEASE_PLUGIN_BIN ?= $(E2E_OCI_DIST_DIR)/$(BINARY_NAME)_$(E2E_OCI_VERSION)_linux_$(E2E_GOARCH)
E2E_OCI_ARCHIVE ?= $(E2E_OCI_DIR)/openbao-plugin-secrets-sync.tar
E2E_OCI_LOCAL_IMAGE ?= openbao-plugin-secrets-sync-e2e-oci:$(E2E_PLUGIN_VERSION)
E2E_OCI_CERT_DIR ?= $(E2E_OCI_DIR)/certs
E2E_OCI_CONFIG ?= $(E2E_OCI_DIR)/openbao.hcl
E2E_OCI_OPENBAO_PORT ?= 18204
E2E_OCI_OPENBAO_ADDR ?= http://127.0.0.1:$(E2E_OCI_OPENBAO_PORT)
E2E_OCI_REGISTRY_PORT ?= 15000
E2E_OCI_REPOSITORY ?= openbao-plugin-secrets-sync
E2E_OCI_IMAGE_IN_BAO ?= registry:5000/$(E2E_OCI_REPOSITORY)
E2E_OCI_PUSH_REF ?= 127.0.0.1:$(E2E_OCI_REGISTRY_PORT)/$(E2E_OCI_REPOSITORY):$(E2E_PLUGIN_VERSION)
E2E_RESILIENCE_COMPOSE_FILE ?= test/e2e/resilience/compose.yaml
E2E_RESILIENCE_DIR ?= dist/e2e/resilience
E2E_RESILIENCE_STATIC_SEAL_KEY ?= $(E2E_RESILIENCE_DIR)/static-unseal.key
E2E_RESILIENCE_OPENBAO_PORT ?= 18205
E2E_RESILIENCE_OPENBAO_STANDBY_PORT ?= 18206
E2E_RESILIENCE_OPENBAO_STANDBY_2_PORT ?= 18207
E2E_RESILIENCE_LOCALSTACK_PORT ?= 4567
E2E_RESILIENCE_OPENBAO_ADDR ?= http://127.0.0.1:$(E2E_RESILIENCE_OPENBAO_PORT)
E2E_RESILIENCE_OPENBAO_STANDBY_ADDR ?= http://127.0.0.1:$(E2E_RESILIENCE_OPENBAO_STANDBY_PORT)
E2E_RESILIENCE_OPENBAO_STANDBY_2_ADDR ?= http://127.0.0.1:$(E2E_RESILIENCE_OPENBAO_STANDBY_2_PORT)
E2E_RESILIENCE_LOCALSTACK_ENDPOINT ?= http://127.0.0.1:$(E2E_RESILIENCE_LOCALSTACK_PORT)

.PHONY: e2e-build-plugin
e2e-build-plugin: ## Build the Linux plugin binary used by the OpenBao e2e container.
	@mkdir -p "$(E2E_PLUGIN_DIR)"
	@CGO_ENABLED=0 GOOS=linux GOARCH="$(E2E_GOARCH)" "$(GO)" build $(GO_BUILD_FLAGS) -ldflags "$(E2E_LDFLAGS)" -o "$(E2E_PLUGIN_BIN)" ./cmd/openbao-plugin-secrets-sync
	@chmod 0755 "$(E2E_PLUGIN_BIN)"

.PHONY: e2e-stage-release-plugin
e2e-stage-release-plugin: ## Stage a prebuilt release binary for OpenBao e2e testing.
	@if [ ! -f "$(E2E_RELEASE_PLUGIN_BIN)" ]; then \
		printf 'release plugin binary not found: %s\n' "$(E2E_RELEASE_PLUGIN_BIN)" >&2; \
		exit 1; \
	fi
	@mkdir -p "$(E2E_PLUGIN_DIR)"
	@cp "$(E2E_RELEASE_PLUGIN_BIN)" "$(E2E_PLUGIN_BIN)"
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

.PHONY: test-e2e-release-localstack
test-e2e-release-localstack: e2e-stage-release-plugin ## Run LocalStack e2e against a prebuilt release binary.
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
	E2E_PLUGIN_VERSION="$(E2E_PLUGIN_VERSION)" \
	"$(GO)" test -tags=e2e ./test/e2e/localstack -count=1 -v

.PHONY: e2e-resilience-fixture
e2e-resilience-fixture: e2e-build-plugin ## Generate persistent OpenBao e2e fixture material.
	@mkdir -p "$(E2E_RESILIENCE_DIR)"
	@if [ ! -f "$(E2E_RESILIENCE_STATIC_SEAL_KEY)" ]; then \
		tmp="$(E2E_RESILIENCE_STATIC_SEAL_KEY).tmp"; \
		umask 077; \
		openssl rand 32 > "$$tmp"; \
		mv -f "$$tmp" "$(E2E_RESILIENCE_STATIC_SEAL_KEY)"; \
	fi

.PHONY: e2e-resilience-up
e2e-resilience-up: e2e-resilience-fixture ## Start the persistent OpenBao plus LocalStack resilience stack.
	@E2E_OPENBAO_IMAGE="$(E2E_OPENBAO_IMAGE)" \
	E2E_RESILIENCE_OPENBAO_PORT="$(E2E_RESILIENCE_OPENBAO_PORT)" \
	E2E_RESILIENCE_OPENBAO_STANDBY_PORT="$(E2E_RESILIENCE_OPENBAO_STANDBY_PORT)" \
	E2E_RESILIENCE_OPENBAO_STANDBY_2_PORT="$(E2E_RESILIENCE_OPENBAO_STANDBY_2_PORT)" \
	E2E_RESILIENCE_LOCALSTACK_PORT="$(E2E_RESILIENCE_LOCALSTACK_PORT)" \
	$(DOCKER_COMPOSE) -f "$(E2E_RESILIENCE_COMPOSE_FILE)" up -d --wait

.PHONY: e2e-resilience-down
e2e-resilience-down: ## Stop the persistent OpenBao plus LocalStack resilience stack.
	@E2E_OPENBAO_IMAGE="$(E2E_OPENBAO_IMAGE)" \
	E2E_RESILIENCE_OPENBAO_PORT="$(E2E_RESILIENCE_OPENBAO_PORT)" \
	E2E_RESILIENCE_OPENBAO_STANDBY_PORT="$(E2E_RESILIENCE_OPENBAO_STANDBY_PORT)" \
	E2E_RESILIENCE_OPENBAO_STANDBY_2_PORT="$(E2E_RESILIENCE_OPENBAO_STANDBY_2_PORT)" \
	E2E_RESILIENCE_LOCALSTACK_PORT="$(E2E_RESILIENCE_LOCALSTACK_PORT)" \
	$(DOCKER_COMPOSE) -f "$(E2E_RESILIENCE_COMPOSE_FILE)" down -v --remove-orphans

.PHONY: test-e2e-resilience
test-e2e-resilience: e2e-resilience-fixture ## Run persistent OpenBao lifecycle resilience e2e tests.
	@set -eu; \
	cleanup() { \
		status="$$?"; \
		if [ "$$status" -ne 0 ]; then \
			E2E_OPENBAO_IMAGE="$(E2E_OPENBAO_IMAGE)" \
			E2E_RESILIENCE_OPENBAO_PORT="$(E2E_RESILIENCE_OPENBAO_PORT)" \
			E2E_RESILIENCE_OPENBAO_STANDBY_PORT="$(E2E_RESILIENCE_OPENBAO_STANDBY_PORT)" \
			E2E_RESILIENCE_OPENBAO_STANDBY_2_PORT="$(E2E_RESILIENCE_OPENBAO_STANDBY_2_PORT)" \
			E2E_RESILIENCE_LOCALSTACK_PORT="$(E2E_RESILIENCE_LOCALSTACK_PORT)" \
			$(DOCKER_COMPOSE) -f "$(E2E_RESILIENCE_COMPOSE_FILE)" logs --no-color openbao openbao-standby openbao-standby-2 localstack || true; \
		fi; \
		$(MAKE) e2e-resilience-down >/dev/null 2>&1 || true; \
		exit "$$status"; \
	}; \
	trap cleanup EXIT; \
	$(MAKE) e2e-resilience-up; \
	AWS_ACCESS_KEY_ID=test \
	AWS_SECRET_ACCESS_KEY=test \
	AWS_REGION=us-east-1 \
	AWS_DEFAULT_REGION=us-east-1 \
	E2E_RESILIENCE_OPENBAO_ADDR="$(E2E_RESILIENCE_OPENBAO_ADDR)" \
	E2E_RESILIENCE_OPENBAO_STANDBY_ADDR="$(E2E_RESILIENCE_OPENBAO_STANDBY_ADDR)" \
	E2E_RESILIENCE_OPENBAO_STANDBY_2_ADDR="$(E2E_RESILIENCE_OPENBAO_STANDBY_2_ADDR)" \
	E2E_RESILIENCE_LOCALSTACK_ENDPOINT="$(E2E_RESILIENCE_LOCALSTACK_ENDPOINT)" \
	E2E_RESILIENCE_COMPOSE_FILE="$(CURDIR)/$(E2E_RESILIENCE_COMPOSE_FILE)" \
	E2E_DOCKER_COMPOSE="$(DOCKER_COMPOSE)" \
	E2E_PLUGIN_PATH="$(E2E_PLUGIN_BIN)" \
	E2E_PLUGIN_VERSION="$(E2E_PLUGIN_VERSION)" \
	"$(GO)" test -tags=e2e ./test/e2e/resilience -run TestOpenBaoLifecyclePreservesSecretSyncState -count=1 -v

.PHONY: e2e-oci-build-plugin
e2e-oci-build-plugin: ## Build the Linux plugin binary used by the OCI e2e image.
	@mkdir -p "$(E2E_OCI_DIST_DIR)"
	@CGO_ENABLED=0 GOOS=linux GOARCH="$(E2E_GOARCH)" "$(GO)" build $(GO_BUILD_FLAGS) -ldflags "$(E2E_LDFLAGS)" -o "$(E2E_OCI_RELEASE_PLUGIN_BIN)" ./cmd/openbao-plugin-secrets-sync
	@chmod 0755 "$(E2E_OCI_RELEASE_PLUGIN_BIN)"

.PHONY: e2e-oci-stage-release-plugin
e2e-oci-stage-release-plugin: ## Stage a prebuilt release binary for OCI e2e testing.
	@if [ ! -f "$(E2E_RELEASE_PLUGIN_BIN)" ]; then \
		printf 'release plugin binary not found: %s\n' "$(E2E_RELEASE_PLUGIN_BIN)" >&2; \
		exit 1; \
	fi
	@mkdir -p "$(E2E_OCI_DIST_DIR)"
	@cp "$(E2E_RELEASE_PLUGIN_BIN)" "$(E2E_OCI_RELEASE_PLUGIN_BIN)"
	@chmod 0755 "$(E2E_OCI_RELEASE_PLUGIN_BIN)"

.PHONY: e2e-oci-image-archive
e2e-oci-image-archive: ## Build a Docker archive for the local OCI plugin registry.
	@if [ ! -f "$(E2E_OCI_RELEASE_PLUGIN_BIN)" ]; then \
		printf 'OCI e2e plugin binary not found: %s\n' "$(E2E_OCI_RELEASE_PLUGIN_BIN)" >&2; \
		exit 1; \
	fi
	@mkdir -p "$(E2E_OCI_DIR)"
	@$(DOCKER) build \
		--platform "linux/$(E2E_GOARCH)" \
		--file Dockerfile.oci-plugin \
		--build-arg DIST_DIR="$(E2E_OCI_DIST_DIR)" \
		--build-arg BINARY_NAME="$(BINARY_NAME)" \
		--build-arg VERSION="$(E2E_OCI_VERSION)" \
		--build-arg PLUGIN_VERSION="$(E2E_PLUGIN_VERSION)" \
		--build-arg COMMIT="$(COMMIT)" \
		--build-arg BUILD_DATE="$(BUILD_DATE)" \
		--build-arg SOURCE_URL="$(OCI_IMAGE_SOURCE)" \
		--tag "$(E2E_OCI_LOCAL_IMAGE)" \
		.
	@$(DOCKER) save --output "$(E2E_OCI_ARCHIVE)" "$(E2E_OCI_LOCAL_IMAGE)"

.PHONY: e2e-oci-fixture
e2e-oci-fixture: e2e-oci-image-archive ## Generate OCI e2e registry certificates and OpenBao config.
	@BINARY_PATH="$(E2E_OCI_RELEASE_PLUGIN_BIN)" \
	BINARY_NAME="$(BINARY_NAME)" \
	PLUGIN_VERSION="$(E2E_PLUGIN_VERSION)" \
	E2E_OCI_DIR="$(E2E_OCI_DIR)" \
	E2E_OCI_CERT_DIR="$(E2E_OCI_CERT_DIR)" \
	E2E_OCI_CONFIG="$(E2E_OCI_CONFIG)" \
	E2E_OCI_IMAGE_IN_BAO="$(E2E_OCI_IMAGE_IN_BAO)" \
	./hack/e2e/prepare-oci-fixture.sh

.PHONY: e2e-oci-up-staged
e2e-oci-up-staged: e2e-oci-fixture
	@set -eu; \
	E2E_OPENBAO_IMAGE="$(E2E_OPENBAO_IMAGE)" \
	E2E_OCI_REGISTRY_PORT="$(E2E_OCI_REGISTRY_PORT)" \
	E2E_OCI_OPENBAO_PORT="$(E2E_OCI_OPENBAO_PORT)" \
	E2E_LOCALSTACK_PORT="$(E2E_LOCALSTACK_PORT)" \
	$(DOCKER_COMPOSE) -f "$(E2E_OCI_COMPOSE_FILE)" up -d --wait registry localstack; \
	"$(GO)" run ./hack/tools/oci_archive_push \
		-archive "$(E2E_OCI_ARCHIVE)" \
		-ref "$(E2E_OCI_PUSH_REF)" \
		-ca "$(E2E_OCI_CERT_DIR)/ca.crt"; \
	E2E_OPENBAO_IMAGE="$(E2E_OPENBAO_IMAGE)" \
	E2E_OCI_REGISTRY_PORT="$(E2E_OCI_REGISTRY_PORT)" \
	E2E_OCI_OPENBAO_PORT="$(E2E_OCI_OPENBAO_PORT)" \
	E2E_LOCALSTACK_PORT="$(E2E_LOCALSTACK_PORT)" \
	$(DOCKER_COMPOSE) -f "$(E2E_OCI_COMPOSE_FILE)" up -d --wait openbao

.PHONY: e2e-oci-up
e2e-oci-up: e2e-oci-build-plugin e2e-oci-up-staged ## Start the OCI plugin distribution e2e stack.

.PHONY: e2e-oci-down
e2e-oci-down: ## Stop the OCI plugin distribution e2e stack.
	@E2E_OPENBAO_IMAGE="$(E2E_OPENBAO_IMAGE)" \
	E2E_OCI_REGISTRY_PORT="$(E2E_OCI_REGISTRY_PORT)" \
	E2E_OCI_OPENBAO_PORT="$(E2E_OCI_OPENBAO_PORT)" \
	E2E_LOCALSTACK_PORT="$(E2E_LOCALSTACK_PORT)" \
	$(DOCKER_COMPOSE) -f "$(E2E_OCI_COMPOSE_FILE)" down -v --remove-orphans

.PHONY: test-e2e-oci-localstack
test-e2e-oci-localstack: ## Run self-contained OCI plugin download plus LocalStack e2e tests.
	@set -eu; \
	cleanup() { \
		status="$$?"; \
		if [ "$$status" -ne 0 ]; then \
			E2E_OPENBAO_IMAGE="$(E2E_OPENBAO_IMAGE)" \
			E2E_OCI_REGISTRY_PORT="$(E2E_OCI_REGISTRY_PORT)" \
			E2E_OCI_OPENBAO_PORT="$(E2E_OCI_OPENBAO_PORT)" \
			E2E_LOCALSTACK_PORT="$(E2E_LOCALSTACK_PORT)" \
			$(DOCKER_COMPOSE) -f "$(E2E_OCI_COMPOSE_FILE)" logs --no-color openbao registry localstack || true; \
		fi; \
		$(MAKE) e2e-oci-down >/dev/null 2>&1 || true; \
		exit "$$status"; \
	}; \
	trap cleanup EXIT; \
	$(MAKE) e2e-oci-up; \
	AWS_ACCESS_KEY_ID=test \
	AWS_SECRET_ACCESS_KEY=test \
	AWS_REGION=us-east-1 \
	AWS_DEFAULT_REGION=us-east-1 \
	E2E_OPENBAO_ADDR="$(E2E_OCI_OPENBAO_ADDR)" \
	E2E_LOCALSTACK_ENDPOINT="$(E2E_LOCALSTACK_ENDPOINT)" \
	E2E_PLUGIN_REGISTRATION=oci \
	E2E_PLUGIN_VERSION="$(E2E_PLUGIN_VERSION)" \
	"$(GO)" test -tags=e2e ./test/e2e/localstack -run TestOpenBaoPluginSyncsToLocalStackSecretsManager -count=1 -v

.PHONY: test-e2e-oci-release-localstack
test-e2e-oci-release-localstack: ## Run OCI plugin e2e against a prebuilt release binary.
	@set -eu; \
	cleanup() { \
		status="$$?"; \
		if [ "$$status" -ne 0 ]; then \
			E2E_OPENBAO_IMAGE="$(E2E_OPENBAO_IMAGE)" \
			E2E_OCI_REGISTRY_PORT="$(E2E_OCI_REGISTRY_PORT)" \
			E2E_OCI_OPENBAO_PORT="$(E2E_OCI_OPENBAO_PORT)" \
			E2E_LOCALSTACK_PORT="$(E2E_LOCALSTACK_PORT)" \
			$(DOCKER_COMPOSE) -f "$(E2E_OCI_COMPOSE_FILE)" logs --no-color openbao registry localstack || true; \
		fi; \
		$(MAKE) e2e-oci-down >/dev/null 2>&1 || true; \
		exit "$$status"; \
	}; \
	trap cleanup EXIT; \
	$(MAKE) e2e-oci-stage-release-plugin; \
	$(MAKE) e2e-oci-up-staged; \
	AWS_ACCESS_KEY_ID=test \
	AWS_SECRET_ACCESS_KEY=test \
	AWS_REGION=us-east-1 \
	AWS_DEFAULT_REGION=us-east-1 \
	E2E_OPENBAO_ADDR="$(E2E_OCI_OPENBAO_ADDR)" \
	E2E_LOCALSTACK_ENDPOINT="$(E2E_LOCALSTACK_ENDPOINT)" \
	E2E_PLUGIN_REGISTRATION=oci \
	E2E_PLUGIN_VERSION="$(E2E_PLUGIN_VERSION)" \
	"$(GO)" test -tags=e2e ./test/e2e/localstack -run TestOpenBaoPluginSyncsToLocalStackSecretsManager -count=1 -v

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
		"$(KIND)" create cluster --name "$(E2E_KIND_CLUSTER)" --image "$(E2E_KIND_NODE_IMAGE)"; \
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

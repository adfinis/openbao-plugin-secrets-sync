##@ Build And Release

.PHONY: build
build: ## Build the OpenBao plugin binary with version metadata.
	@mkdir -p "$$(dirname "$(BIN)")"
	@"$(GO)" build $(GO_BUILD_FLAGS) -ldflags "$(LDFLAGS)" -o "$(BIN)" ./cmd/openbao-plugin-secrets-sync

.PHONY: release-artifacts
release-artifacts: clean-dist ## Build Linux release binaries and checksums.
	@set -eu; \
	mkdir -p "$(DIST_DIR)"; \
	for target in $(RELEASE_TARGETS); do \
		goos="$${target%/*}"; \
		goarch="$${target#*/}"; \
		artifact="$(DIST_DIR)/$(BINARY_NAME)_$(VERSION)_$${goos}_$${goarch}"; \
		printf 'building %s\n' "$$artifact"; \
		CGO_ENABLED=0 GOOS="$$goos" GOARCH="$$goarch" "$(GO)" build $(GO_BUILD_FLAGS) -ldflags "$(LDFLAGS)" -o "$$artifact" ./cmd/openbao-plugin-secrets-sync; \
	done
	@$(MAKE) release-sboms
	@$(MAKE) checksums

.PHONY: release-sboms
release-sboms: ## Generate SPDX SBOMs for release binaries.
	@set -eu; \
	for target in $(RELEASE_TARGETS); do \
		goos="$${target%/*}"; \
		goarch="$${target#*/}"; \
		artifact="$(DIST_DIR)/$(BINARY_NAME)_$(VERSION)_$${goos}_$${goarch}"; \
		sbom="$(DIST_DIR)/sbom-$(BINARY_NAME)-$${goos}-$${goarch}.spdx.json"; \
		printf 'generating %s\n' "$$sbom"; \
		BINARY_NAME="$(BINARY_NAME)" \
		BINARY_PATH="$$artifact" \
		OUTPUT_PATH="$$sbom" \
		VERSION="$(VERSION)" \
		GOOS="$$goos" \
		GOARCH="$$goarch" \
		SOURCE_DATE_EPOCH="$${SOURCE_DATE_EPOCH:-$$(git show -s --format=%ct HEAD 2>/dev/null || date +%s)}" \
		"$(SHELL)" hack/ci/generate-go-binary-sbom.sh; \
	done

.PHONY: checksums
checksums: ## Generate release artifact checksums.
	@set -eu; \
	artifacts="$$(find "$(DIST_DIR)" -maxdepth 1 -type f \
		! -name "$$(basename "$(CHECKSUM_FILE)")" \
		! -name '*.bundle' \
		! -name 'provenance-index.json' \
		-exec basename {} \; | sort)"; \
	if [ -z "$$artifacts" ]; then \
		printf '%s\n' 'No release artifacts found for checksum generation.'; \
		exit 1; \
	fi; \
	cd "$(DIST_DIR)" && $(CHECKSUM) $$artifacts > "$$(basename "$(CHECKSUM_FILE)")"

.PHONY: clean-dist
clean-dist: ## Remove release artifacts.
	@rm -rf "$(DIST_DIR)"

##@ Documentation

.PHONY: docs-check
docs-check: ## Check docs for known formatting artifacts.
	@! grep -R -n --exclude-dir=_archive $$(printf '\357\277\274') README.md docs 2>/dev/null
	@! grep -R -n --exclude-dir=_archive '⸻' README.md docs 2>/dev/null
	@! grep -R -n --exclude-dir=_archive '—' README.md docs 2>/dev/null
	@command -v yq >/dev/null 2>&1 || { printf '%s\n' 'yq is required to validate docs/api/openapi.yaml'; exit 1; }
	@yq '.' docs/api/openapi.yaml >/dev/null

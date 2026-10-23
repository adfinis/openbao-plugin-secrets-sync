##@ Documentation

.PHONY: docs-check
docs-check: ## Check docs for formatting artifacts, verifiedBy evidence, index coverage, and links.
	@! grep -R -n --exclude-dir=_archive $$(printf '\357\277\274') README.md docs 2>/dev/null
	@! grep -R -n --exclude-dir=_archive '⸻' README.md docs 2>/dev/null
	@! grep -R -n --exclude-dir=_archive '—' README.md docs 2>/dev/null
	@"$(GO)" run ./hack/tools/docs_check

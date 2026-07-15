##@ Documentation

.PHONY: docs-check
docs-check: ## Check docs for known formatting artifacts.
	@! grep -R -n --exclude-dir=_archive $$(printf '\357\277\274') README.md docs 2>/dev/null
	@! grep -R -n --exclude-dir=_archive '⸻' README.md docs 2>/dev/null
	@! grep -R -n --exclude-dir=_archive '—' README.md docs 2>/dev/null

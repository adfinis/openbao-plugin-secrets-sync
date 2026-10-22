#!/usr/bin/env bash
# Publish only the readiness status; all decisions come from the read-only evaluator.
set -euo pipefail
: "${REPO:?REPO is required}"
: "${PR_NUMBER:?PR_NUMBER is required}"
: "${EXPECTED_HEAD_SHA:?EXPECTED_HEAD_SHA is required}"
[[ "${PR_NUMBER}" =~ ^[1-9][0-9]*$ ]] || exit 1
[[ "${EXPECTED_HEAD_SHA}" =~ ^[0-9a-f]{40}$ ]] || exit 1

publish_status() {
  gh api "repos/${REPO}/statuses/${EXPECTED_HEAD_SHA}" --method POST \
    -f context='Release Readiness' -f state="$1" -f description="$2" \
    -f target_url="${GITHUB_SERVER_URL:-https://github.com}/${REPO}/actions/runs/${GITHUB_RUN_ID:?}" >/dev/null
}

publish_status pending 'Evaluating current PR release readiness'
script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
if bash "${script_dir}/release-readiness.sh"; then
  publish_status success 'Release readiness satisfied or ordinary PR'
else
  publish_status failure 'Release readiness requirements are not satisfied'
  exit 1
fi

#!/usr/bin/env bash
# Evaluate release metadata without executing PR code or changing GitHub state.
set -euo pipefail

: "${REPO:?REPO is required}"
: "${PR_NUMBER:?PR_NUMBER is required}"
: "${EXPECTED_HEAD_SHA:?EXPECTED_HEAD_SHA is required}"
READINESS_MODE="${READINESS_MODE:-merge}"
REQUIRED_LABEL="${REQUIRED_LABEL:-release:ready}"
REQUIRED_APPROVER="${REQUIRED_APPROVER:-}"
RELEASE_BRANCH="release-please--branches--main"

fail() {
  echo "$*" >&2
  exit 1
}

[[ "${PR_NUMBER}" =~ ^[1-9][0-9]*$ ]] || fail "PR number must be a positive integer"
[[ "${EXPECTED_HEAD_SHA}" =~ ^[0-9a-f]{40}$ ]] || fail "Expected head must be a full commit ID"
[[ "${READINESS_MODE}" == merge || "${READINESS_MODE}" == tag ]] || fail "Unknown readiness mode"

# Compact API responses on stdin before storing or passing scalar metadata.
# Patch and review bodies can exceed the platform's single-argument limit.
read_pr() {
  gh api "repos/${REPO}/pulls/${PR_NUMBER}" | jq -ce --arg label "${REQUIRED_LABEL}" '{
    number, state, draft, merged, merged_at, changed_files,
    head: {sha: .head.sha, ref: .head.ref, repo: {full_name: .head.repo.full_name}},
    base: {ref: .base.ref},
    merge_commit_sha: (if .merged then .merge_commit_sha else null end),
    label_present: any(.labels[]; .name == $label)
  }'
}

read_approval() {
  gh api "repos/${REPO}/pulls/${PR_NUMBER}/reviews" --paginate --slurp | jq -ce --arg approver "${REQUIRED_APPROVER}" '
    [add[] | select(((.user.login // "") | ascii_downcase) == ($approver | ascii_downcase))
      | select(.state != "PENDING" and .state != "COMMENTED")
      | {id, state, commit_id, submitted_at}]
    | sort_by(.submitted_at, .id) | last // {}'
}

pr="$(read_pr)"
jq -e --arg head "${EXPECTED_HEAD_SHA}" '.head.sha == $head and .base.ref == "main"' <<<"${pr}" >/dev/null ||
  fail "PR head or base changed; evaluate the current candidate"
files="$(gh api "repos/${REPO}/pulls/${PR_NUMBER}/files" --paginate --slurp |
  jq -ce 'add | map({filename, previous_filename, status})')"
file_count="$(jq -er '.changed_files | numbers' <<<"${pr}")"
[[ "$(jq 'length' <<<"${files}")" == "${file_count}" ]] || fail "PR file list is incomplete"
head_branch="$(jq -er '.head.ref' <<<"${pr}")"
is_release="$(jq -r --arg branch "${RELEASE_BRANCH}" --arg head_branch "${head_branch}" '
  $head_branch == $branch or any(.[];
    .filename == ".release-please-manifest.json" or .previous_filename == ".release-please-manifest.json")' <<<"${files}")"

if [[ "${is_release}" == true ]]; then
  jq -e --arg repo "${REPO}" --arg branch "${RELEASE_BRANCH}" \
    '.head.repo.full_name == $repo and .head.ref == $branch and .draft == false' <<<"${pr}" >/dev/null ||
    fail "Release PR must use the same-repository release branch and must not be draft"
  jq -e 'any(.[]; .filename == "CHANGELOG.md" and .status != "removed") and
    any(.[]; .filename == ".release-please-manifest.json" and .status != "removed")' \
    <<<"${files}" >/dev/null || fail "Release PR must update changelog and manifest"
  [[ -n "${REQUIRED_APPROVER}" ]] || fail "Required release approver is not configured"
  if [[ "${READINESS_MODE}" == tag ]]; then
    [[ "${EXPECTED_MERGE_SHA:-}" =~ ^[0-9a-f]{40}$ ]] || fail "Expected merge must be a full commit ID"
    jq -e --arg merge "${EXPECTED_MERGE_SHA}" \
      '.state == "closed" and .merged == true and .merge_commit_sha == $merge and (.merged_at | type == "string")' \
      <<<"${pr}" >/dev/null || fail "Release PR does not match the selected merge"
  else
    jq -e '.state == "open" and .merged == false' <<<"${pr}" >/dev/null || fail "Release PR is not open"
  fi
  has_label="$(gh api "repos/${REPO}/issues/${PR_NUMBER}/labels" --paginate --slurp |
    jq -r --arg label "${REQUIRED_LABEL}" 'any(add[]; .name == $label)')"
  if [[ "${has_label}" != true ]] || ! jq -e '.label_present' <<<"${pr}" >/dev/null; then
    fail "Required release readiness label is missing"
  fi
  # Comments do not revoke approval. A later dismissal or changes-requested review does.
  approval="$(read_approval)"
  jq -e --arg head "${EXPECTED_HEAD_SHA}" \
    '.state == "APPROVED" and .commit_id == $head and (.submitted_at | type == "string")' \
    <<<"${approval}" >/dev/null || fail "Designated current-head approval is missing or revoked"
  if [[ "${READINESS_MODE}" == tag ]]; then
    jq -en --argjson approval "${approval}" --argjson pr "${pr}" \
      '($approval.submitted_at | fromdateiso8601) < ($pr.merged_at | fromdateiso8601)' >/dev/null ||
      fail "Release approval must predate merge"
  fi
elif [[ "${READINESS_MODE}" == tag ]]; then
  fail "Selected PR is not a release PR"
fi

# Re-read after paginated evaluation. Never publish green for a changed candidate.
current="$(read_pr)"
[[ "${pr}" == "${current}" ]] || fail "PR changed during readiness evaluation"
if [[ "${is_release}" == true ]]; then
  current_approval="$(read_approval)"
  [[ "${approval}" == "${current_approval}" ]] || fail "Reviews changed during readiness evaluation"
fi
jq -cn --argjson release_pr "${is_release}" --argjson pr "${pr}" \
  '{release_pr: $release_pr, pr: $pr.number, head: $pr.head.sha, merge: $pr.merge_commit_sha}'

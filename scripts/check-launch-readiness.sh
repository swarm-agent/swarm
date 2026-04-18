#!/usr/bin/env bash
set -euo pipefail

SCRIPT_PATH="${BASH_SOURCE[0]}"
SCRIPT_DIR="$(cd -- "$(dirname -- "${SCRIPT_PATH}")" && pwd)"
ROOT_DIR="$(cd -- "${SCRIPT_DIR}/.." && pwd)"
SELF_REL="${SCRIPT_PATH#./}"
if [[ "${SELF_REL}" == "${SCRIPT_PATH}" ]]; then
  SELF_REL="$(realpath --relative-to="${ROOT_DIR}" "${SCRIPT_PATH}" 2>/dev/null || printf '%s' "${SCRIPT_PATH}")"
fi
cd "${ROOT_DIR}"

STRICT_BINARIES=0
REQUIRE_CLEAN=0
FORBIDDEN_TOKENS=()

usage() {
  cat <<'USAGE'
Usage: bash scripts/check-launch-readiness.sh [--strict-binaries] [--require-clean] [--forbid-token X ...]

Checks repository launch readiness for public publication.
- Runs existing baseline repo checks.
- Scans tracked files for personal identifiers and local-only env files.
- Detects tracked junk/artifact paths and unexpected untracked files.
- Reports tracked non-text binary blobs for review.

Flags:
  --strict-binaries  fail when tracked non-text binary blobs are present
  --require-clean    fail when git status is not clean
  --forbid-token X   fail if tracked files contain exact token X (repeatable)
USAGE
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --strict-binaries)
      STRICT_BINARIES=1
      ;;
    --require-clean)
      REQUIRE_CLEAN=1
      ;;
    --forbid-token)
      if [[ $# -lt 2 ]]; then
        printf 'missing value for --forbid-token\n' >&2
        exit 2
      fi
      FORBIDDEN_TOKENS+=("$2")
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      printf 'unknown argument: %s\n' "$1" >&2
      usage >&2
      exit 2
      ;;
  esac
  shift
done

need_cmd() {
  local cmd="$1"
  if ! command -v "${cmd}" >/dev/null 2>&1; then
    printf 'missing required command: %s\n' "${cmd}" >&2
    exit 1
  fi
}

need_cmd git
need_cmd rg
need_cmd file
need_cmd mktemp

pass_count=0
warn_count=0
fail_count=0

pass() {
  pass_count=$((pass_count + 1))
  printf '[PASS] %s\n' "$1"
}

warn() {
  warn_count=$((warn_count + 1))
  printf '[WARN] %s\n' "$1"
  if [[ -n "${2:-}" ]]; then
    printf '%s\n' "$2" | sed 's/^/    /'
  fi
}

fail() {
  fail_count=$((fail_count + 1))
  printf '[FAIL] %s\n' "$1"
  if [[ -n "${2:-}" ]]; then
    printf '%s\n' "$2" | sed 's/^/    /'
  fi
}

run_must_pass() {
  local name="$1"
  shift
  local tmp
  tmp="$(mktemp)"
  if "$@" >"${tmp}" 2>&1; then
    pass "${name}"
  else
    fail "${name}" "$(cat "${tmp}")"
  fi
  rm -f "${tmp}"
}

section() {
  printf '\n== %s ==\n' "$1"
}

filter_self_from_lines() {
  local input="${1:-}"
  if [[ -z "${input}" ]]; then
    return 0
  fi
  if [[ -n "${SELF_REL}" ]]; then
    printf '%s\n' "${input}" | grep -vxF "${SELF_REL}" || true
    return 0
  fi
  printf '%s\n' "${input}"
}

scan_tracked_regex() {
  local pattern="$1"
  git grep -n -I -E "${pattern}" -- . ":!${SELF_REL}" || true
}

section "baseline"
run_must_pass "baseline precommit scan" bash "${SCRIPT_DIR}/check-precommit.sh"

if [[ "${REQUIRE_CLEAN}" == "1" ]]; then
  git_status="$(git status --short || true)"
  if [[ -n "${git_status}" ]]; then
    fail "working tree clean" "${git_status}"
  else
    pass "working tree clean"
  fi
fi

section "tracked content"
env_hits="$(git ls-files | rg '(^|/)\.env($|\.)|(^|/)\.swarmenv($|\.)' | rg -v '(^|/)\.env\.example$|(^|/)\.swarmenv\.example$' || true)"
if [[ -n "${env_hits}" ]]; then
  fail "tracked local env files" "${env_hits}"
else
  pass "tracked local env files"
fi

extra_token_hits=""
if [[ "${#FORBIDDEN_TOKENS[@]}" -gt 0 ]]; then
  token_hits_tmp=""
  for forbidden_token in "${FORBIDDEN_TOKENS[@]}"; do
    [[ -n "${forbidden_token}" ]] || continue
    token_hits_tmp+="$(git grep -n -I -F -- "${forbidden_token}" -- . ":!${SELF_REL}" || true)"$'\n'
  done
  extra_token_hits="$(printf '%s' "${token_hits_tmp}" | sed '/^$/d' | sort -u || true)"
  if [[ -n "${extra_token_hits}" ]]; then
    fail "forbidden token matches" "${extra_token_hits}"
  else
    pass "forbidden token matches"
  fi
else
  warn "forbidden token scan skipped" "pass one or more --forbid-token values for owner/device-specific strings"
fi

section "tracked paths"
artifact_paths="$(git ls-files | rg '^(\.agents/|\.cache/|\.runtime/|\.swarm/|tmp/|web/node_modules/|web/dist/)' || true)"
if [[ -n "${artifact_paths}" ]]; then
  fail "tracked local-only artifact paths" "${artifact_paths}"
else
  pass "tracked local-only artifact paths"
fi

launcher_wrappers="$(git ls-files | rg '^(bin/|setup$|rebuild$)' || true)"
if [[ -n "${launcher_wrappers}" ]]; then
  pass "tracked launcher wrapper scripts present"
  printf '%s\n' "${launcher_wrappers}" | sed 's/^/    /'
else
  warn "no tracked launcher wrapper scripts found"
fi

section "workspace hygiene"
untracked_files="$(git ls-files --others --exclude-standard || true)"
untracked_files="$(filter_self_from_lines "${untracked_files}")"
if [[ -n "${untracked_files}" ]]; then
  fail "unexpected untracked files" "${untracked_files}"
else
  pass "unexpected untracked files"
fi

binary_report=""
while IFS= read -r tracked_file; do
  [[ -n "${tracked_file}" ]] || continue
  [[ -f "${tracked_file}" ]] || continue
  mime_type="$(file --brief --mime-type "${tracked_file}")"
  case "${mime_type}" in
    text/*|application/json|application/xml|application/javascript|application/x-empty|inode/x-empty)
      continue
      ;;
  esac
  desc="$(file --brief "${tracked_file}")"
  case "${desc}" in
    *script*|*text*|*JSON*|*XML*|*Unicode*|*empty*)
      continue
      ;;
  esac
  binary_report+="${tracked_file} :: ${mime_type} :: ${desc}"$'\n'
done < <(git ls-files)

binary_report="$(printf '%s' "${binary_report}" | sed '/^$/d' || true)"
if [[ -n "${binary_report}" ]]; then
  if [[ "${STRICT_BINARIES}" == "1" ]]; then
    fail "tracked non-text binary blobs" "${binary_report}"
  else
    warn "tracked non-text binary blobs (manual launch-shape review required)" "${binary_report}"
  fi
else
  pass "tracked non-text binary blobs"
fi

section "summary"
printf 'passes=%d warnings=%d failures=%d\n' "${pass_count}" "${warn_count}" "${fail_count}"
if [[ "${fail_count}" -ne 0 ]]; then
  exit 1
fi

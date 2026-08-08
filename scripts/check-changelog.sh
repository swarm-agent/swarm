#!/usr/bin/env bash
set -euo pipefail

SCRIPT_PATH="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/$(basename -- "${BASH_SOURCE[0]}")"
ROOT_DIR="${SWARM_CHANGELOG_ROOT:-$(cd -- "$(dirname -- "${SCRIPT_PATH}")/.." && pwd)}"

usage() {
  cat <<'USAGE'
Usage: scripts/check-changelog.sh <base-ref> [head-ref]
       scripts/check-changelog.sh --self-test

Require CHANGELOG.md to be updated between the merge base of <base-ref> and
<head-ref> (default: HEAD). The resulting changelog must retain an Unreleased
section with a Docs impact subsection.
USAGE
}

check_range() {
  local base_ref="$1"
  local head_ref="$2"
  local additions

  git cat-file -e "${base_ref}^{commit}" 2>/dev/null || {
    printf 'changelog check: base ref is not a commit: %s\n' "${base_ref}" >&2
    return 1
  }
  git cat-file -e "${head_ref}^{commit}" 2>/dev/null || {
    printf 'changelog check: head ref is not a commit: %s\n' "${head_ref}" >&2
    return 1
  }

  if git diff --quiet "${base_ref}...${head_ref}" -- CHANGELOG.md; then
    printf 'changelog check: CHANGELOG.md must be updated in this PR\n' >&2
    return 1
  fi

  additions="$(git diff --numstat "${base_ref}...${head_ref}" -- CHANGELOG.md | awk '{ additions += $1 } END { print additions + 0 }')"
  if [[ "${additions}" -eq 0 ]]; then
    printf 'changelog check: CHANGELOG.md update must add release information\n' >&2
    return 1
  fi

  if ! git show "${head_ref}:CHANGELOG.md" | awk '
    /^## Unreleased$/ { in_unreleased = 1; found_unreleased = 1; next }
    in_unreleased && /^## / { in_unreleased = 0 }
    in_unreleased && /^### Docs impact$/ { found_docs_impact = 1 }
    END { exit !(found_unreleased && found_docs_impact) }
  '; then
    printf 'changelog check: Unreleased must include a Docs impact subsection\n' >&2
    return 1
  fi

  printf 'changelog check: PASS (%s...%s updates CHANGELOG.md)\n' "${base_ref}" "${head_ref}"
}

self_test() {
  local test_root
  local fixture
  local base_ref

  : "${TMPDIR:?changelog self-test requires TMPDIR}"
  test_root="$(mktemp -d "${TMPDIR%/}/swarm-changelog-check.XXXXXX")"
  trap 'rm -rf -- "${test_root}"' RETURN
  fixture="${test_root}/repo"
  mkdir -p "${fixture}"

  (
    cd "${fixture}"
    git init --quiet
    git config user.name "Swarm Changelog Check"
    git config user.email "changelog-check@example.invalid"
    cat >CHANGELOG.md <<'EOF_CHANGELOG'
# Changelog

## Unreleased

### Docs impact

- Docs impact: none.
EOF_CHANGELOG
    printf 'base\n' >app.txt
    git add CHANGELOG.md app.txt
    git commit --quiet -m base
    base_ref="$(git rev-parse HEAD)"

    printf 'change without changelog\n' >>app.txt
    git add app.txt
    git commit --quiet -m no-changelog
    if SWARM_CHANGELOG_ROOT="${fixture}" "${SCRIPT_PATH}" "${base_ref}" HEAD >/dev/null 2>&1; then
      printf 'changelog self-test: missing changelog update unexpectedly passed\n' >&2
      exit 1
    fi

    cat >>CHANGELOG.md <<'EOF_CHANGELOG'

- Recorded the fixture change.
EOF_CHANGELOG
    git add CHANGELOG.md
    git commit --quiet -m with-changelog
    SWARM_CHANGELOG_ROOT="${fixture}" "${SCRIPT_PATH}" "${base_ref}" HEAD >/dev/null
  )

  printf 'changelog self-test: PASS\n'
}

case "${1:-}" in
  --self-test)
    if [[ $# -ne 1 ]]; then
      usage >&2
      exit 2
    fi
    self_test
    ;;
  -h|--help)
    usage
    ;;
  '')
    usage >&2
    exit 2
    ;;
  *)
    if [[ $# -gt 2 ]]; then
      usage >&2
      exit 2
    fi
    cd "${ROOT_DIR}"
    check_range "$1" "${2:-HEAD}"
    ;;
esac

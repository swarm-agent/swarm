#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd -- "${SCRIPT_DIR}/.." && pwd)"
cd "${ROOT_DIR}"

ZERO_SHA="0000000000000000000000000000000000000000"
remote_name="${1:-unknown}"
remote_url="${2:-unknown}"

if [[ "${SWARM_PREPUSH_CHECKED:-0}" == "1" ]]; then
  echo "[prepush] checks already ran in this process; allowing push"
  exit 0
fi

protected_push=0
saw_ref=0
# The lexical repository checks judge only the lines a protected push adds
# (scripts/lib-check-range.sh). Each protected ref contributes the commit the
# remote already has as its base; any ref without one keeps whole-repo checks.
check_bases=()
check_heads=()
whole_repo_reason=""

while read -r local_ref local_sha remote_ref remote_sha; do
  [[ -z "${local_ref:-}" ]] && continue
  saw_ref=1

  case "${remote_ref}" in
    refs/heads/dev|refs/heads/main)
      protected_push=1
      echo "[prepush] protected target: ${remote_ref#refs/heads/} on ${remote_name} (${remote_url})"

      if [[ "${local_sha}" == "${ZERO_SHA}" ]]; then
        echo "[prepush] FAIL: refusing to delete ${remote_ref#refs/heads/}" >&2
        exit 1
      fi

      if [[ "${remote_sha}" != "${ZERO_SHA}" ]] && git cat-file -e "${remote_sha}^{commit}" 2>/dev/null; then
        if ! git merge-base --is-ancestor "${remote_sha}" "${local_sha}"; then
          echo "[prepush] FAIL: refusing non-fast-forward push to ${remote_ref#refs/heads/}" >&2
          echo "[prepush] Fetch/rebase first, or use an explicit reviewed recovery path." >&2
          exit 1
        fi
        check_bases+=("${remote_sha}")
      else
        tracking_ref="refs/remotes/${remote_name}/${remote_ref#refs/heads/}"
        if merge_base="$(git merge-base "${tracking_ref}" "${local_sha}" 2>/dev/null)"; then
          check_bases+=("${merge_base}")
        else
          whole_repo_reason="no known remote commit or merge-base for ${remote_ref#refs/heads/}"
        fi
      fi
      check_heads+=("${local_sha}")
      ;;
  esac
done

if [[ "${saw_ref}" == "0" ]]; then
  echo "[prepush] no refs on stdin; running guard checks defensively"
  protected_push=1
  whole_repo_reason="no refs on stdin"
fi

if [[ "${protected_push}" != "1" ]]; then
  echo "[prepush] no protected branch target; skipping repository guard checks"
  exit 0
fi

unset SWARM_CHECK_BASE SWARM_CHECK_HEAD
if [[ -z "${whole_repo_reason}" ]]; then
  for head in "${check_heads[@]}"; do
    if [[ "${head}" != "${check_heads[0]}" ]]; then
      whole_repo_reason="protected refs push different commits"
    fi
  done
fi
if [[ -z "${whole_repo_reason}" ]]; then
  if check_base="$(git merge-base --octopus "${check_bases[@]}" 2>/dev/null)"; then
    export SWARM_CHECK_BASE="${check_base}"
    export SWARM_CHECK_HEAD="${check_heads[0]}"
    echo "[prepush] path checks judge only lines added in ${SWARM_CHECK_BASE:0:12}..${SWARM_CHECK_HEAD:0:12}"
  else
    whole_repo_reason="protected ref bases share no commit"
  fi
fi
if [[ -n "${whole_repo_reason}" ]]; then
  echo "[prepush] path checks scan the whole repository: ${whole_repo_reason}"
fi

# The same commits already passed every check below on this machine (keyed by
# their patch IDs, so the key survives a rebase onto commits that landed on the
# remote meanwhile): skip the repeat. Without this a protected push re-runs
# minutes of checks after every rebase, and a busy dev moves on before the
# push is sent. Only the commits being pushed are keyed; the whole-repository
# path never uses the record.
pass_key=""
if [[ -z "${whole_repo_reason}" ]]; then
  pass_key="$(git format-patch --stdout "${SWARM_CHECK_BASE}..${SWARM_CHECK_HEAD}" | git patch-id --stable | cut -d' ' -f1 | sort | sha256sum | cut -d' ' -f1)"
  pass_dir="$(git rev-parse --git-common-dir)/swarm-prepush-passed"
  if [[ -n "${pass_key}" && -f "${pass_dir}/${pass_key}" ]]; then
    echo "[prepush] these commits already passed every check on this machine; not repeating"
    echo "[prepush] PASS"
    exit 0
  fi
fi

echo "[prepush] running repository guard checks before objects are sent"
"${SCRIPT_DIR}/check-precommit.sh"
echo "[prepush] running critical fast tests"
bash "${SCRIPT_DIR}/run-critical-tests.sh" fast
if [[ -n "${pass_key}" ]]; then
  mkdir -p "${pass_dir}"
  : > "${pass_dir}/${pass_key}"
fi
echo "[prepush] PASS"

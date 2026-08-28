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
      fi
      ;;
  esac
done

if [[ "${saw_ref}" == "0" ]]; then
  echo "[prepush] no refs on stdin; running guard checks defensively"
  protected_push=1
fi

if [[ "${protected_push}" != "1" ]]; then
  echo "[prepush] no protected branch target; skipping repository guard checks"
  exit 0
fi

echo "[prepush] running repository guard checks before objects are sent"
"${SCRIPT_DIR}/check-precommit.sh"
echo "[prepush] running atlas-driven critical fast tests"
bash "${SCRIPT_DIR}/run-critical-tests.sh" fast
echo "[prepush] PASS"

#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd -- "${SCRIPT_DIR}/.." && pwd)"
cd "${ROOT_DIR}"

self_test=0
if [[ "${1:-}" == "--self-test" ]]; then
  self_test=1
  shift
fi

scan_paths=(
  "pkg/storagecontract"
  "pkg/startupconfig"
  "internal/launcher"
  "cmd/swarmsetup"
  "swarmd/internal/config"
  "swarmd/internal/appstorage"
  "swarmd/internal/api/tool_storage.go"
  "swarmd/internal/imagegen"
  "swarmd/internal/imagegen.go"
  "swarmd/internal/localcontainers"
  "swarmd/internal/remotedeploy"
  "swarmd/internal/run"
  "swarmd/internal/tool"
  "swarmd/internal/worktree"
  "deploy/container-mvp"
  "install.sh"
  "scripts/lib-lane.sh"
  "swarmd/scripts/dev-up.sh"
)
if (( "$#" > 0 )); then
  scan_paths+=("$@")
fi

rg_common=(
  --line-number
  --no-heading
  --glob '!**/*_test.go'
  --glob '!**/testdata/**'
  --glob '!scripts/check-daemon-storage-paths.sh'
)

filter_allowed() {
  # Allowlist rationale:
  # - storagecontract.go intentionally names XDG/home roots only to reject them.
  # - launcher.go intentionally resolves legacy user/XDG locations only for read-only stop diagnostics.
  # - lib-lane.sh still exposes lane metadata helpers for CLI/harness compatibility; daemon roots below in the same file are system paths.
  # - container entrypoint unsets XDG variables and rejects /root, /home, /workspaces, and /tmp storage paths.
  # - local deploy/remotedeploy workspace strings are mount targets or API route names, not Swarm-owned daemon storage roots.
  grep -Ev \
    -e '^pkg/storagecontract/storagecontract\.go:.*(HOME|XDG_|\.local|\.config|Library|Desktop|Documents|Downloads|forbidden|reject|~|home-relative|WorkspaceRoots)' \
    -e '^internal/launcher/launcher\.go:.*(legacy|Legacy|XDG_STATE_HOME|XDG_DATA_HOME|UserHomeDir|UserConfigDir|\.local|\.config|resolve legacy|stat legacy|startupCWD|Getwd)' \
    -e '^internal/launcher/system_paths\.go:.*os\.CreateTemp\("", "swarmd-config-\*"\)' \
    -e '^scripts/lib-lane\.sh:[0-9]+:.*(swarm_xdg_|XDG_|\.local/state|\.local/share|\.config|migrate_legacy)' \
    -e '^deploy/container-mvp/(entrypoint\.sh|Containerfile\.base):.*(-u XDG_|/root|/home|/workspaces|/tmp|must not be under a user home|~|mkdir -p|VOLUME)' \
    -e '^swarmd/internal/deploy/service\.go:.*(workspaceruntime|/v1/deploy/container/workspaces/bootstrap|/workspaces)' \
    -e '^swarmd/internal/remotedeploy/service\.go:.*(workspaceruntime|startupCWD|/workspaces|resolveRemoteDeployBuildRoot)' \
    -e '^swarmd/internal/localcontainers/service\.go:.*(workspaceruntime|/workspaces)' \
    -e '^swarmd/internal/worktree/service\.go:.*(workspaceruntime|migrateLegacyConfig|MigrateLegacyGlobalConfig)' \
    -e '^swarmd/internal/tool/runtime\.go:.*workspaceruntime' \
    -e '^swarmd/internal/run/service\.go:.*workspaceruntime' \
    -e '^swarmd/internal/store/pebble/(keys|auth_store|auth_vault|worktree_store)\.go:.*(legacy|migrat|Migrate)' \
    -e '^swarmd/internal/store/pebble/swarm_container_profile_store\.go:.*mount\.TargetPath = "/workspace/' \
    -e '^pkg/startupconfig/config\.go:.*migrate startup config' \
    -e '^(internal/launcher/(launcher|update)\.go|swarmd/internal/(imagegen|remotedeploy)/service\.go):.*(os\.Rename|copyDir|copyDir\(|CopyDir)' \
    || true
}

run_scan() {
  local pattern="$1"
  (rg "${rg_common[@]}" "${pattern}" "${scan_paths[@]}" 2>/dev/null || true) | filter_allowed
}

has_failures=0

echo "[storage-path-check] scanning daemon/runtime storage scopes"

forbidden_home_hits="$(run_scan '(\$HOME|\$\{HOME\}|os\.UserHomeDir\(|os\.UserConfigDir\(|os\.UserCacheDir\(|XDG_CONFIG_HOME|XDG_DATA_HOME|XDG_STATE_HOME|XDG_CACHE_HOME|XDG_RUNTIME_DIR|\.local/share|\.local/state|\.config/swarm|~/|/home/|/Users/|/root)')"
if [[ -n "${forbidden_home_hits}" ]]; then
  has_failures=1
  echo "[storage-path-check] FAIL: daemon/runtime storage code references home/XDG/user roots outside the explicit allowlist:"
  echo "${forbidden_home_hits}"
fi

forbidden_workspace_hits="$(run_scan '(/workspaces|/workspace|\./tmp|tmp/remote-deploy|tmp/remote-deploy-ui|tmp/flows|startupCWD|WorkingDirectory=\$|WorkingDirectory=\.|MkdirTemp\(""|CreateTemp\(""|os\.TempDir\(\))')"
if [[ -n "${forbidden_workspace_hits}" ]]; then
  has_failures=1
  echo "[storage-path-check] FAIL: daemon/runtime storage code references workspace, OS temp, or relative temp defaults outside the explicit allowlist:"
  echo "${forbidden_workspace_hits}"
fi

legacy_remote_hits="$(run_scan '(/var/lib/swarm/rd|/var/lib/swarm/remote-deploy|swarm/rd|remoteRootForHome|remote_root/xdg|run-remote-child\.pid.*remote_root|SWARMD_LOCK_PATH=\$swarmd_state_dir)')"
if [[ -n "${legacy_remote_hits}" ]]; then
  has_failures=1
  echo "[storage-path-check] FAIL: remote/container storage code references legacy data-root runtime paths:"
  echo "${legacy_remote_hits}"
fi

migration_hits="$(run_scan '(os\.Rename\(|moveDirContents|CopyDir|copyDir|migration|migrate|Migration|Migrate)')"
if [[ -n "${migration_hits}" ]]; then
  has_failures=1
  echo "[storage-path-check] FAIL: storage migration code is present outside explicit read-only legacy diagnostics:"
  echo "${migration_hits}"
fi

if [[ "${has_failures}" -ne 0 ]]; then
  exit 1
fi

echo "[storage-path-check] PASS"

if [[ "${self_test}" == "1" ]]; then
  tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/swarm-storage-gate.XXXXXX")"
  trap 'rm -rf "${tmp_dir}"' EXIT
  fixture="${tmp_dir}/bad-storage.sh"
  cat >"${fixture}" <<'EOF'
#!/usr/bin/env bash
SWARMD_DATA_DIR="${HOME}/.local/share/swarmd"
SWARMD_LOCK_PATH="${SWARMD_DATA_DIR}/swarmd.lock"
EOF
  if "${BASH_SOURCE[0]}" "${fixture}" >/tmp/swarm-storage-gate-self-test.out 2>&1; then
    cat /tmp/swarm-storage-gate-self-test.out >&2 || true
    echo "[storage-path-check] FAIL: negative fixture unexpectedly passed" >&2
    exit 1
  fi
  rm -f /tmp/swarm-storage-gate-self-test.out
  echo "[storage-path-check] self-test PASS"
fi

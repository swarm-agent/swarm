#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd -- "${SCRIPT_DIR}/.." && pwd)"
cd "${ROOT_DIR}"

DISALLOWED_ABS_PATH_PATTERN='(/home/|/Users/|/tmp/|/var/tmp/|/etc/|/opt/|/root/)'
has_failures=0

# Exact guest-image provisioning statements, not host-path exemptions. Keep
# filename and complete source text bound together; never match a whole script.
filter_guest_image_paths() {
  local hit normalized
  while IFS= read -r hit; do
    normalized="$(printf '%s\n' "${hit#./}" | sed -E 's/^([^:]+):[0-9]+:/\1:/')"
    case "${normalized}" in
      'scripts/testbench-local-cache.sh:    export GOROOT=/opt/go GOTOOLCHAIN=local GOMAXPROCS=2 HOME=/root'|\
      "scripts/testbench-local-image.sh:printf 'local-testbench-base/v1\\n' > \"\$mountpoint/etc/swarm-local-base\""|\
      'scripts/testbench-local-tools.sh:[[ -f $point/etc/swarm-local-base ]] || exit 1'|\
      'scripts/testbench-local-tools.sh:if [[ ! -e $point/opt/go && ! -e $point/opt/pnpm ]]; then'|\
      'scripts/testbench-local-tools.sh:  cp -a -- "$goroot" "$point/opt/go"'|\
      'scripts/testbench-local-tools.sh:  cp -a -- "$pnpmroot" "$point/opt/pnpm"'|\
      'scripts/testbench-local-tools.sh:  chown -R root:root "$point/opt/go" "$point/opt/pnpm"'|\
      'scripts/testbench-local-tools.sh:  ln -s /opt/go/bin/go "$point/usr/local/bin/go"'|\
      'scripts/testbench-local-tools.sh:  ln -s /opt/go/bin/gofmt "$point/usr/local/bin/gofmt"'|\
      'scripts/testbench-local-tools.sh:  ln -s /opt/pnpm/bin/pnpm.mjs "$point/usr/local/bin/pnpm"'|\
      "scripts/testbench-local-tools.sh:  [[ \$(chroot \"\$point\" /usr/bin/env GOROOT=/opt/go /opt/go/bin/go version) == 'go version go1.26.7 linux/amd64' ]] || exit 1"|\
      'scripts/testbench-local-tools.sh:  cmp -- "$pnpmroot/package.json" "$point/opt/pnpm/package.json" || exit 1'|\
      "scripts/testbench-local-tools.sh:printf 'deb http://archive.ubuntu.com/ubuntu resolute main universe\\n' > \"\$point/etc/apt/sources.list\""|\
      'scripts/testbench-local-tools.sh:    export DEBIAN_FRONTEND=noninteractive GOROOT=/opt/go') ;;
      *) printf '%s\n' "$hit" ;;
    esac
  done
}

filter_allowed_runtime_paths() {
  # Security classifier allowlist entries below detect critical shell access; they are not runtime path defaults.
  # Renderer and testbench constants name the fixed Chrome package covered by the
  # reviewed host AppArmor policy. Anchor the complete declaration: no directory,
  # whole-file exemption, alternate binary, or appended shell command is allowed.
  # Fresh-root detection checks the complete fixed installation layout and must
  # not bootstrap over existing state. Allow only this exact presence-check list.
  filter_guest_image_paths | grep -Ev \
    -e '^(\./)?scripts/rebuild\.sh:[0-9]+:  for path in /usr/local/share/swarm /etc/swarmd /var/lib/swarmd /var/cache/swarmd /run/swarmd /var/log/swarmd /etc/systemd/system/swarm\.service; do$' \
    -e '^(\./)?scripts/diagnose-live-workset-full-history\.sh:.*(/etc/swarmd|/var/lib/swarmd|/var/cache/swarmd|/var/log/swarmd|/run/swarmd)' \
    -e '^(\./)?(install\.sh|cmd/swarm/main\.go|internal/launcher/service_lifecycle\.go|scripts/ssh-fast-test\.sh):.*(/etc/swarmd|/etc/systemd/system/swarm\.service|/etc/tmpfiles\.d/swarmd\.conf)' \
    -e '^(\./)?scripts/check-daemon-storage-paths\.sh:.*(/home/|/root|/tmp/swarm-storage-gate-self-test\.out|forbidden_home_hits|negative fixture|run_scan)' \
    -e '^(\./)?swarmd/internal/permission/policy\.go:[0-9]+:[[:space:]]*criticalBashSystemConfigMarker[[:space:]]*=[[:space:]]*"/etc/"[[:space:]]*$' \
    -e '^(\./)?swarmd/internal/permission/policy\.go:[0-9]+:[[:space:]]*criticalBashSystemDataMarker[[:space:]]*=[[:space:]]*"/var/lib/"[[:space:]]*$' \
    -e '^(\./)?swarmd/internal/htmlcapture/renderer\.go:[0-9]+:[[:space:]]*SystemChromePath[[:space:]]*=[[:space:]]*"/opt/google/chrome/chrome"[[:space:]]*$' \
    -e "^(\./)?scripts/run-testbench-runner\.sh:[0-9]+:readonly SYSTEM_CHROME_PATH='/opt/google/chrome/chrome'$" \
    || true
}

# Requirement: the fixed fresh-install presence check is allowed, but changes
# to its targets, appended commands, and other files must remain detectable.
if [[ "${1:-}" == "--self-test" ]]; then
  presence_hit='scripts/rebuild.sh:11:  for path in /usr/local/share/swarm /etc/swarmd /var/lib/swarmd /var/cache/swarmd /run/swarmd /var/log/swarmd /etc/systemd/system/swarm.service; do'
  [[ -z "$(printf '%s\n' "${presence_hit}" | filter_allowed_runtime_paths)" ]] || exit 1
  for rejected_hit in "${presence_hit/swarm.service/other.service}" "${presence_hit}; echo unsafe" "${presence_hit/rebuild.sh/other.sh}"; do
    [[ -n "$(printf '%s\n' "${rejected_hit}" | filter_allowed_runtime_paths)" ]] || exit 1
  done
  # Every currently reviewed guest statement must be allowed, but moving it,
  # appending a command, or changing its fixed destination must be rejected.
  while IFS= read -r guest_hit; do
    [[ -z "$(printf '%s\n' "$guest_hit" | filter_guest_image_paths)" ]] || exit 1
    for rejected_hit in "${guest_hit}; echo unsafe" "other.sh:${guest_hit#*:}" "${guest_hit//\/opt\//\/unsafe\/}" "${guest_hit//\/etc\//\/unsafe\/}"; do
      [[ "$rejected_hit" == "$guest_hit" ]] && continue
      [[ -n "$(printf '%s\n' "$rejected_hit" | filter_guest_image_paths)" ]] || exit 1
    done
  done < <(rg -n "$DISALLOWED_ABS_PATH_PATTERN" scripts/testbench-local-cache.sh scripts/testbench-local-image.sh scripts/testbench-local-tools.sh)
  echo '[path-check] self-test PASS'
fi

echo "[path-check] scanning non-test runtime code and scripts for hardcoded absolute paths"
runtime_hits="$(
  {
    rg -n \
    --glob '!.git/**' \
    --glob '*.go' \
    --glob '*.sh' \
    --glob 'bin/swarm' \
    --glob 'bin/swarmdev' \
    --glob 'bin/swarmsetup' \
    --glob '!**/*_test.go' \
    --glob '!scripts/check-hardcoded-paths.sh' \
    --glob '!scripts/check-precommit.sh' \
    "${DISALLOWED_ABS_PATH_PATTERN}" \
    . || true;
  } | filter_allowed_runtime_paths
)"
if [[ -n "${runtime_hits}" ]]; then
  has_failures=1
  echo "[path-check] FAIL: disallowed absolute path literals found:"
  echo "${runtime_hits}"
fi

echo "[path-check] scanning docs/scripts for legacy repo path tokens"
legacy_hits="$(
  rg -n \
    --glob '!.git/**' \
    --glob '*.md' \
    --glob '*.sh' \
    --glob 'bin/swarm' \
    --glob 'bin/swarmdev' \
    --glob 'bin/swarmsetup' \
    --glob '!scripts/check-hardcoded-paths.sh' \
    --glob '!scripts/check-precommit.sh' \
    'swarm-refactor' \
    . || true
)"
if [[ -n "${legacy_hits}" ]]; then
  has_failures=1
  echo "[path-check] FAIL: legacy repo path token 'swarm-refactor' found:"
  echo "${legacy_hits}"
fi

echo "[path-check] scanning docs for machine-specific home paths"
home_hits="$(
  rg -n \
    --glob '!.git/**' \
    --glob '*.md' \
    '/home/[A-Za-z0-9._-]+/' \
    . || true
)"
if [[ -n "${home_hits}" ]]; then
  has_failures=1
  echo "[path-check] FAIL: machine-specific home paths found in docs:"
  echo "${home_hits}"
fi

echo "[path-check] scanning repository for personal home-directory paths"
repo_home_hits="$(
  rg -n \
    --glob '!.git/**' \
    --glob '!scripts/check-hardcoded-paths.sh' \
    --glob '!scripts/check-precommit.sh' \
    '/home/[A-Za-z0-9._-]+/|/Users/[A-Za-z0-9._-]+/' \
    . || true
)"
if [[ -n "${repo_home_hits}" ]]; then
  has_failures=1
  echo "[path-check] FAIL: personal home-directory paths found:"
  echo "${repo_home_hits}"
fi

if [[ "${has_failures}" -ne 0 ]]; then
  exit 1
fi

echo "[path-check] PASS"

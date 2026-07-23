#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd -- "${SCRIPT_DIR}/.." && pwd)"
cd "${ROOT_DIR}"

assert_fixed() {
  local label="$1"
  local file="$2"
  local pattern="$3"
  if ! grep -Eq -- "${pattern}" "${file}"; then
    printf 'launch-default assertion failed: %s (%s)\n' "${label}" "${file}" >&2
    exit 1
  fi
  printf '[PASS] %s\n' "${label}"
}

assert_absent() {
  local label="$1"
  local file="$2"
  local pattern="$3"
  if grep -Eq -- "${pattern}" "${file}"; then
    printf 'launch-default assertion failed: %s (%s)\n' "${label}" "${file}" >&2
    exit 1
  fi
  printf '[PASS] %s\n' "${label}"
}

assert_fixed "API defaults to loopback" pkg/startupconfig/config.go 'DefaultHost[[:space:]]*=[[:space:]]*"127\.0\.0\.1"'
assert_fixed "permission bypass defaults off" pkg/startupconfig/config.go 'BypassPermissions:[[:space:]]*false,'
assert_fixed "tool-output retention defaults off" pkg/startupconfig/config.go 'RetainToolOutputHistory:[[:space:]]*false,'
assert_fixed "V3 diagnostics default off" pkg/startupconfig/config.go 'V3Diagnostics:[[:space:]]*false,'
assert_fixed "provider diagnostics default off" pkg/startupconfig/config.go 'ProviderAPIDiagnostics:[[:space:]]*false,'
assert_fixed "startup config mode is private" pkg/startupconfig/config.go 'configFileMode[[:space:]]*=[[:space:]]*0o600'
assert_fixed "direct swarmsetup defaults to no service" cmd/swarmsetup/main.go 'installService := false'
assert_fixed "blank install choice is files-only" install.sh '2\|none\|no-service\|no\|n\|N\|""\)'
assert_absent "blank install choice cannot select systemd" install.sh '1\|systemd\|service\|s\|S\|""\)'
assert_fixed "non-loopback startup config fails closed" pkg/startupconfig/config.go 'unsupported non-loopback host'
assert_fixed "non-loopback CLI listen fails closed" swarmd/internal/config/config.go 'unsupported non-loopback --listen'
assert_fixed "default permission output omits details" swarmd/internal/permission/service.go 'detailed output omitted for privacy'
assert_fixed "uninstall preserves canonical config by default" internal/launcher/service_lifecycle.go 'preserve /etc/swarmd'
assert_fixed "uninstall preserves canonical data by default" internal/launcher/service_lifecycle.go 'preserve /var/lib/swarmd'
assert_fixed "failed runtime boot has rollback path" internal/launcher/update.go 'rollbackPendingRuntimeUpdate'

printf 'launch-default assertions passed\n'

#!/usr/bin/env bash
set -euo pipefail

swarm_lane_default() {
  local lane="${SWARM_LANE:-main}"
  lane="$(printf "%s" "${lane}" | tr '[:upper:]' '[:lower:]' | tr -d '[:space:]')"
  case "${lane}" in
  main|dev)
    printf "%s\n" "${lane}"
    ;;
  *)
    printf "main\n"
    ;;
  esac
}

swarm_lane_state_home() {
  if [[ -n "${XDG_STATE_HOME:-}" ]]; then
    printf "%s\n" "${XDG_STATE_HOME}"
    return 0
  fi
  printf "%s/.local/state\n" "${HOME}"
}

swarm_lane_config_home() {
  if [[ -n "${XDG_CONFIG_HOME:-}" ]]; then
    printf "%s\n" "${XDG_CONFIG_HOME}"
    return 0
  fi
  printf "%s/.config\n" "${HOME}"
}

swarm_lane_data_home() {
  if [[ -n "${XDG_DATA_HOME:-}" ]]; then
    printf "%s\n" "${XDG_DATA_HOME}"
    return 0
  fi
  printf "%s/.local/share\n" "${HOME}"
}

swarm_lane_binary_root() {
  printf "%s\n" "/usr/local/share/swarm/bin"
}

swarm_lane_install_root() {
  printf "%s\n" "/usr/local/share/swarm"
}

swarm_lane_tool_bin_dir() {
  printf "%s\n" "/usr/local/share/swarm/libexec"
}

swarm_lane_bin_dir() {
  local lane="${1:-}"
  : "${lane}"
  printf "%s\n" "$(swarm_lane_binary_root)"
}

swarm_lane_desktop_dist_dir() {
  printf "%s\n" "/usr/local/share/swarm/share"
}

swarm_daemon_config_root() {
  printf "%s\n" "/etc"/swarmd
}

swarm_daemon_data_root() {
  local lane="${1:-main}"
  case "${lane}" in
    dev) printf "%s\n" "/var/lib/swarmd/dev" ;;
    *) printf "%s\n" "/var/lib/swarmd" ;;
  esac
}

swarm_daemon_runtime_root() {
  local lane="${1:-main}"
  case "${lane}" in
    dev) printf "%s\n" "/run/swarmd/dev" ;;
    *) printf "%s\n" "/run/swarmd" ;;
  esac
}

swarm_daemon_log_root() {
  local lane="${1:-main}"
  case "${lane}" in
    dev) printf "%s\n" "/var/log/swarmd/dev" ;;
    *) printf "%s\n" "/var/log/swarmd" ;;
  esac
}

swarm_daemon_ports_root() {
  printf "%s\n" "/run/swarmd/ports"
}

swarm_current_owner_spec() {
  local uid="${SUDO_UID:-$(id -u)}"
  local gid="${SUDO_GID:-$(id -g)}"
  if [[ ! "${uid}" =~ ^[0-9]+$ || ! "${gid}" =~ ^[0-9]+$ || "${uid}" == "0" || "${gid}" == "0" ]]; then
    echo "Swarm requires a trusted non-root service owner; refusing uid=${uid} gid=${gid}." >&2
    return 1
  fi
  if command -v getent >/dev/null 2>&1; then
    getent passwd "${uid}" >/dev/null || { echo "unknown service uid: ${uid}" >&2; return 1; }
    getent group "${gid}" >/dev/null || { echo "unknown service gid: ${gid}" >&2; return 1; }
  fi
  printf "%s:%s\n" "${uid}" "${gid}"
}

swarm_file_owner_spec() {
  local path="${1:-}"
  local owner=""
  if owner="$(stat -c '%u:%g' -- "${path}" 2>/dev/null)"; then
    :
  elif owner="$(stat -f '%u:%g' -- "${path}" 2>/dev/null)"; then
    :
  else
    echo "unable to inspect owner/group for ${path}" >&2
    return 1
  fi
  if [[ ! "${owner}" =~ ^[0-9]+:[0-9]+$ ]]; then
    echo "invalid owner/group metadata for ${path}: ${owner}" >&2
    return 1
  fi
  printf '%s\n' "${owner}"
}

swarm_require_safe_target() {
  local kind="${1:-file}"
  local path="${2:-}"
  if [[ -L "${path}" ]]; then
    if [[ "${kind}" != "directory" ]] || ! swarm_is_canonical_runtime_directory_symlink "${path}"; then
      echo "refusing symlink ${kind} target: ${path}" >&2
      return 1
    fi
    return 0
  fi
  if [[ -e "${path}" ]]; then
    case "${kind}" in
      directory) [[ -d "${path}" ]] ;;
      file) [[ -f "${path}" ]] ;;
      *) return 1 ;;
    esac || { echo "refusing non-${kind} target: ${path}" >&2; return 1; }
  fi
}

swarm_is_canonical_runtime_directory_symlink() {
  local path="${1:-}"
  local install_root current_link versions_dir leaf leaf_target current_target version
  install_root="$(swarm_lane_install_root)"
  current_link="${install_root}/current"
  versions_dir="${install_root}/versions"
  leaf="${path##*/}"
  case "${leaf}" in
    bin|libexec|lib|share) ;;
    *) return 1 ;;
  esac
  [[ "${path}" == "${install_root}/${leaf}" ]] || return 1
  [[ -d "${install_root}" && ! -L "${install_root}" ]] || return 1
  [[ -d "${versions_dir}" && ! -L "${versions_dir}" ]] || return 1
  [[ -L "${current_link}" ]] || return 1
  leaf_target="$(readlink -- "${path}")" || return 1
  [[ "${leaf_target}" == "${current_link}/${leaf}" ]] || return 1
  current_target="$(readlink -- "${current_link}")" || return 1
  version="${current_target##*/}"
  [[ -n "${version}" && "${current_target}" == "${versions_dir}/${version}" ]] || return 1
  [[ -d "${current_target}" && ! -L "${current_target}" ]] || return 1
  [[ -d "${current_target}/${leaf}" && ! -L "${current_target}/${leaf}" && -d "${path}" ]]
}

swarm_run_privileged() {
  if [[ "$(id -u)" == "0" ]]; then
    "$@"
    return $?
  fi
  if ! command -v sudo >/dev/null 2>&1; then
    echo "Swarm uses system locations under /usr/local, /etc, /var, and /run." >&2
    echo "Install sudo before running this command, or create/chown those Swarm directories." >&2
    return 1
  fi
  sudo "$@"
}

swarm_dir_writable() {
  local path="${1:-}"
  local probe
  probe="$(mktemp "${path}/.swarm-write-check.XXXXXX" 2>/dev/null)" || return 1
  rm -f "${probe}"
}

swarm_provision_owned_dir() {
  local mode="${1:-0755}"
  local path="${2:-}"
  local owner
  swarm_require_safe_target directory "${path}" || return 1
  if mkdir -p "${path}" 2>/dev/null && chmod "${mode}" "${path}" 2>/dev/null && swarm_dir_writable "${path}"; then
    return 0
  fi
  owner="$(swarm_current_owner_spec)"
  swarm_run_privileged install -d -m "${mode}" -o "${owner%:*}" -g "${owner#*:}" "${path}"
}

swarm_provision_system_dir() {
  local mode="${1:-0755}"
  local path="${2:-}"
  swarm_require_safe_target directory "${path}" || return 1
  if mkdir -p "${path}" 2>/dev/null && [[ -d "${path}" ]]; then
    return 0
  fi
  swarm_run_privileged install -d -m "${mode}" "${path}"
}

swarm_provision_tmpfiles_config() {
  local owner tmp_path target_path
  owner="$(swarm_current_owner_spec)"
  target_path="/etc"/tmpfiles.d/swarmd.conf
  swarm_require_safe_target file "${target_path}" || return 1
  tmp_path="$(mktemp -t swarmd-tmpfiles.XXXXXX)"
  cat >"${tmp_path}" <<EOF
d /run/swarmd 0700 ${owner%:*} ${owner#*:} -
d /run/swarmd/dev 0700 ${owner%:*} ${owner#*:} -
d /run/swarmd/ports 0700 ${owner%:*} ${owner#*:} -
EOF
  if [[ -f "${target_path}" ]] && cmp -s "${tmp_path}" "${target_path}"; then
    rm -f "${tmp_path}"
    return 0
  fi
  if ! swarm_run_privileged install -m 0644 "${tmp_path}" "${target_path}"; then
    rm -f "${tmp_path}"
    return 1
  fi
  rm -f "${tmp_path}"
}

swarm_provision_systemd_service_unit() {
  if [[ "${SWARM_SKIP_SYSTEMD_UNIT:-}" == "1" ]]; then
    return 0
  fi
  if ! command -v systemctl >/dev/null 2>&1; then
    return 0
  fi
  local target_path tmp_path swarm_bin data_root cache_root runtime_root config_root log_root owner
  target_path="/etc"/systemd/system/swarm.service
  swarm_require_safe_target file "${target_path}" || return 1
  tmp_path="$(mktemp -t swarmd-service.XXXXXX)"
  swarm_bin="/usr/local/bin/swarm"
  owner="$(swarm_current_owner_spec)"
  data_root="$(swarm_daemon_data_root main)"
  cache_root="/var/cache/swarmd"
  runtime_root="$(swarm_daemon_runtime_root main)"
  config_root="$(swarm_daemon_config_root)"
  log_root="$(swarm_daemon_log_root main)"
  cat >"${tmp_path}" <<EOF
[Unit]
Description=Swarm daemon
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=${owner%:*}
Group=${owner#*:}
ExecStart=${swarm_bin} main server run
Restart=on-failure
RestartSec=2
StateDirectory=swarmd
CacheDirectory=swarmd
RuntimeDirectory=swarmd
ConfigurationDirectory=swarmd
LogsDirectory=swarmd
Environment=SWARM_SYSTEMD_SCOPE=system
Environment=SWARM_SYSTEMD_UNIT=swarm.service
Environment=SWARMD_DATA_DIR=${data_root}
Environment=SWARMD_CACHE_DIR=${cache_root}
Environment=SWARMD_RUNTIME_DIR=${runtime_root}
Environment=SWARMD_CONFIG_DIR=${config_root}
Environment=SWARMD_LOG_DIR=${log_root}
WorkingDirectory=/

[Install]
WantedBy=multi-user.target
EOF
  if [[ -f "${target_path}" ]] && cmp -s "${tmp_path}" "${target_path}"; then
    rm -f "${tmp_path}"
    return 0
  fi
  if ! swarm_run_privileged install -m 0644 "${tmp_path}" "${target_path}"; then
    rm -f "${tmp_path}"
    return 1
  fi
  rm -f "${tmp_path}"
  swarm_run_privileged systemctl daemon-reload
}

swarm_provision_system_paths() {
  local lane="${1:-main}"
  local data_root runtime_root log_root
  data_root="$(swarm_daemon_data_root "${lane}")"
  runtime_root="$(swarm_daemon_runtime_root "${lane}")"
  log_root="$(swarm_daemon_log_root "${lane}")"

  swarm_provision_system_dir 0755 "/usr/local/bin"
  swarm_provision_system_dir 0755 "/usr/local/share"
  swarm_provision_system_dir 0755 "/etc"/tmpfiles.d
  swarm_provision_system_dir 0755 "/etc"/systemd/system

  swarm_provision_owned_dir 0755 "$(swarm_lane_binary_root)"
  swarm_provision_owned_dir 0755 "$(swarm_lane_tool_bin_dir)"
  swarm_provision_owned_dir 0755 "$(swarm_lane_install_root)"
  swarm_provision_owned_dir 0755 "$(swarm_lane_install_root)/lib"
  swarm_provision_owned_dir 0755 "$(swarm_lane_desktop_dist_dir)"

  swarm_provision_owned_dir 0700 "$(swarm_daemon_config_root)"
  swarm_provision_owned_dir 0700 "${data_root}"
  swarm_provision_owned_dir 0700 "/var/cache/swarmd"
  swarm_provision_owned_dir 0700 "${runtime_root}"
  swarm_provision_owned_dir 0755 "${log_root}"
  swarm_provision_owned_dir 0700 "$(swarm_daemon_ports_root)"
  swarm_provision_tmpfiles_config
  swarm_provision_systemd_service_unit
}

swarm_startup_config_path() {
  printf "%s\n" "$(swarm_daemon_config_root)/swarm.conf"
}

swarm_startup_config_ensure() {
  local config_path owner tmp_path
  config_path="$(swarm_startup_config_path)"
  swarm_require_safe_target file "${config_path}" || return 1
  if [[ -f "${config_path}" ]]; then
    # Preserve the configured service owner/group; only converge the private mode.
    swarm_run_privileged chmod 0600 "${config_path}"
    return 0
  fi

  owner="$(swarm_current_owner_spec)" || return 1
  swarm_provision_system_paths "$(swarm_lane_default)"
  tmp_path="$(mktemp -t swarm-conf.XXXXXX)"
  cat >"${tmp_path}" <<'EOF'
dev_mode = false
dev_root =
host = 127.0.0.1
port = 7781
advertise_host =
advertise_port = 7781
desktop_port = 5555
bypass_permissions = false
retain_tool_output_history = false
v3_diagnostics = false
provider_api_diagnostics = false
# Metadata-only bounded diagnostics for multi-hour daemon/Desktop captures.
# Requires a daemon restart. Adds profiling, CPU, and private local disk overhead;
# see docs/long-session-diagnostics.md and disable it after the capture.
long_session_diagnostics = false
swarm_name =
desktop_onboarding_complete = true
child = false
mode = lan
tailscale_url =
peer_transport_port = 7791
EOF
  swarm_run_privileged install -o "${owner%:*}" -g "${owner#*:}" -m 0600 "${tmp_path}" "${config_path}"
  rm -f "${tmp_path}"
}

swarm_startup_config_remove_obsolete_keys() {
  local config_path tmp_path
  config_path="$(swarm_startup_config_path)"
  swarm_require_safe_target file "${config_path}" || return 1
  tmp_path="$(mktemp -t swarm-conf.XXXXXX)"
  awk '
    function trim(s) {
      sub(/^[[:space:]]+/, "", s)
      sub(/[[:space:]]+$/, "", s)
      return s
    }
    {
      split_pos = index($0, "=")
      if (split_pos == 0) {
        print
        next
      }
      raw_key = trim(substr($0, 1, split_pos - 1))
      if (raw_key == "startup" "_mode" ||
          raw_key == "swarm" "_mode" ||
          raw_key == "swarm_role" ||
          raw_key == "parent_swarm_id" ||
          raw_key == "pairing_state" ||
          raw_key ~ /^managed[_-]?hosts?/ ||
          raw_key ~ /^remote[_-]?deploy/ ||
          raw_key ~ ("^deploy_" "container_")) {
        next
      }
      print
    }
  ' "${config_path}" >"${tmp_path}"
  if ! cmp -s "${config_path}" "${tmp_path}"; then
    local owner
    owner="$(swarm_file_owner_spec "${config_path}")" || return 1
    swarm_run_privileged install -o "${owner%:*}" -g "${owner#*:}" -m 0600 "${tmp_path}" "${config_path}"
  fi
  rm -f "${tmp_path}"
}

swarm_startup_config_has_key() {
  local key="${1:-}"
  local config_path
  config_path="$(swarm_startup_config_path)"

  awk -F= -v wanted="${key}" '
    /^[[:space:]]*#/ || /^[[:space:]]*$/ { next }
    {
      raw_key = $1
      gsub(/^[[:space:]]+|[[:space:]]+$/, "", raw_key)
      if (raw_key == wanted) {
        found = 1
        exit 0
      }
    }
    END {
      if (!found) {
        exit 1
      }
    }
  ' "${config_path}"
}

swarm_startup_config_raw_value() {
  local key="${1:-}"
  local config_path
  config_path="$(swarm_startup_config_path)"

  if [[ -z "${key}" || ! -f "${config_path}" ]]; then
    return 1
  fi

  awk -F= -v wanted="${key}" '
    /^[[:space:]]*#/ || /^[[:space:]]*$/ { next }
    {
      raw_key = $1
      raw_value = substr($0, index($0, "=") + 1)
      gsub(/^[[:space:]]+|[[:space:]]+$/, "", raw_key)
      gsub(/^[[:space:]]+|[[:space:]]+$/, "", raw_value)
      if (raw_key == wanted) {
        print raw_value
        found = 1
        exit 0
      }
    }
    END {
      if (!found) {
        exit 1
      }
    }
  ' "${config_path}"
}

swarm_startup_config_migrate_legacy() {
  local config_path
  local port_value dev_mode_value child_value network_mode_value advertise_host_value tailscale_url_value
  config_path="$(swarm_startup_config_path)"
  swarm_startup_config_ensure
  swarm_startup_config_remove_obsolete_keys

  port_value="$(swarm_startup_config_raw_value port 2>/dev/null || true)"
  if [[ ! "${port_value}" =~ ^[0-9]+$ ]]; then
    port_value="7781"
  fi
  dev_mode_value="$(swarm_startup_config_raw_value dev_mode 2>/dev/null || true)"
  case "${dev_mode_value}" in
    true|false) ;;
    *) dev_mode_value="false" ;;
  esac
  child_value="$(swarm_startup_config_raw_value child 2>/dev/null || true)"
  if [[ -z "${child_value}" ]]; then
    child_value="false"
  fi
  network_mode_value="$(swarm_startup_config_raw_value mode 2>/dev/null || true)"
  if [[ "${network_mode_value}" != "lan" && "${network_mode_value}" != "tailscale" ]]; then
    case "$(swarm_startup_config_raw_value advertise_mode 2>/dev/null || true)" in
      tailscale) network_mode_value="tailscale" ;;
      *) network_mode_value="lan" ;;
    esac
  fi
  advertise_host_value="$(swarm_startup_config_raw_value advertise_host 2>/dev/null || true)"
  tailscale_url_value="$(swarm_startup_config_raw_value tailscale_url 2>/dev/null || true)"
  if [[ -z "${advertise_host_value}" && -z "${tailscale_url_value}" ]]; then
    local legacy_advertise_addr legacy_advertise_mode
    legacy_advertise_addr="$(swarm_startup_config_raw_value advertise_addr 2>/dev/null || true)"
    legacy_advertise_mode="$(swarm_startup_config_raw_value advertise_mode 2>/dev/null || true)"
    if [[ "${legacy_advertise_mode}" == "tailscale" ]]; then
      tailscale_url_value="${legacy_advertise_addr}"
    else
      advertise_host_value="${legacy_advertise_addr}"
    fi
  fi

  if ! swarm_startup_config_has_key dev_mode; then
    cat >>"${config_path}" <<EOF

# Enable source-checkout dev behavior for local child image rebuilds.
# false = runtime-safe behavior only; true = allow dev-only rebuild flow from dev_root.
dev_mode = ${dev_mode_value}
EOF
  fi

  if ! swarm_startup_config_has_key dev_root; then
    cat >>"${config_path}" <<'EOF'

# Source checkout root used for dev-only local child image rebuilds.
# Leave blank until a rebuild from a source checkout records it.
dev_root =
EOF
  fi

  if ! swarm_startup_config_has_key advertise_host; then
    cat >>"${config_path}" <<EOF

# Canonical LAN host or IP that other machines should use to reach this Swarm.
# Leave blank to detect or confirm it in onboarding.
advertise_host = ${advertise_host_value}
EOF
  fi

  if ! swarm_startup_config_has_key advertise_port; then
    cat >>"${config_path}" <<EOF

# Canonical LAN port that other machines should use to reach this Swarm.
# Defaults to the backend API port and changing it requires a restart.
advertise_port = ${port_value}
EOF
  fi

  if ! swarm_startup_config_has_key bypass_permissions; then
    cat >>"${config_path}" <<'EOF'

# Bypass normal tool permission prompts.
# Plan mode still stays plan mode, and exit_plan_mode still requires approval.
bypass_permissions = false
EOF
  fi

  if ! swarm_startup_config_has_key retain_tool_output_history; then
    cat >>"${config_path}" <<'EOF'

# Keep sanitized tool/permission output in persisted history so refresh can show it.
# false keeps the current privacy-preserving placeholder behavior.
retain_tool_output_history = false
EOF
  fi

  if ! swarm_startup_config_has_key v3_diagnostics; then
    cat >>"${config_path}" <<'EOF'

# Persist verbose V3 diagnostic events, including provider request/error diagnostics.
# Enable temporarily while debugging failed sessions; diagnostics may contain request context.
v3_diagnostics = false
EOF
  fi

  if ! swarm_startup_config_has_key provider_api_diagnostics; then
    cat >>"${config_path}" <<'EOF'

# Log sanitized outbound provider API request and response payloads to daemon logs and durable session diagnostics.
# This is separate from v3_diagnostics and omits/redacts API keys and auth headers.
provider_api_diagnostics = false
EOF
  fi

  if ! swarm_startup_config_has_key long_session_diagnostics; then
    cat >>"${config_path}" <<'EOF'

# Record bounded metadata-only diagnostics for investigating long-session memory and lag.
# Artifacts are private local files under the canonical logs root; changing this requires a restart.
long_session_diagnostics = false
EOF
  fi

  if ! swarm_startup_config_has_key swarm_name; then
    cat >>"${config_path}" <<'EOF'

# Human-readable Swarm name shown in onboarding and discovery surfaces.
# Leave blank to set it later.
swarm_name =
EOF
  fi

  if ! swarm_startup_config_has_key desktop_onboarding_complete; then
    cat >>"${config_path}" <<'EOF'

# Explicit Desktop onboarding completion marker.
# false = Desktop must continue onboarding; true = Desktop may open the launcher.
desktop_onboarding_complete = true
EOF
  fi

  if ! swarm_startup_config_has_key child; then
    cat >>"${config_path}" <<EOF

# Whether this Swarm should bootstrap as a child.
# false = master/default, true = child.
child = ${child_value}
EOF
  fi

  if ! swarm_startup_config_has_key mode; then
    cat >>"${config_path}" <<EOF

# Bootstrap network mode.
# lan = connect over the local network.
# tailscale = connect over a Tailscale URL.
mode = ${network_mode_value}
EOF
  fi

  if ! swarm_startup_config_has_key tailscale_url; then
    cat >>"${config_path}" <<EOF

# Canonical persisted Tailscale URL for bootstrap and pairing flows.
# Leave blank when not using a manual Tailscale address.
tailscale_url = ${tailscale_url_value}
EOF
  fi

  if ! swarm_startup_config_has_key local_transport_port; then
    cat >>"${config_path}" <<'EOF'

# Dedicated child/container peer transport port used when the main backend stays on localhost.
# Changing it requires a restart.
local_transport_port = 7790
EOF
  fi

  if ! swarm_startup_config_has_key peer_transport_port; then
    cat >>"${config_path}" <<'EOF'

# Local-only peer transport port for peer forwarding such as Tailscale Serve or SSH tunneling.
# Changing it requires a restart.
peer_transport_port = 7791
EOF
  fi

}

swarm_startup_config_validate() {
  local config_path
  config_path="$(swarm_startup_config_path)"
  swarm_startup_config_ensure
  swarm_startup_config_migrate_legacy

  awk -v config_path="${config_path}" '
    function trim(value) {
      sub(/^[[:space:]]+/, "", value)
      sub(/[[:space:]]+$/, "", value)
      return value
    }
    function fail(message) {
      print message > "/dev/stderr"
      had_error = 1
      exit 1
    }
    BEGIN {
      valid["dev_mode"] = 1
      valid["dev_root"] = 1
      valid["host"] = 1
      valid["port"] = 1
      valid["advertise_host"] = 1
      valid["advertise_port"] = 1
      valid["desktop_port"] = 1
      valid["bypass_permissions"] = 1
      valid["retain_tool_output_history"] = 1
      valid["v3_diagnostics"] = 1
      valid["provider_api_diagnostics"] = 1
      valid["long_session_diagnostics"] = 1
      valid["swarm_name"] = 1
      valid["desktop_onboarding_complete"] = 1
      valid["child"] = 1
      valid["mode"] = 1
      valid["tailscale_url"] = 1
      valid["local_transport_port"] = 1
      valid["peer_transport_port"] = 1
      allow_empty["swarm_name"] = 1
      allow_empty["dev_root"] = 1
      allow_empty["advertise_host"] = 1
      allow_empty["tailscale_url"] = 1
    }
    /^[[:space:]]*#/ || /^[[:space:]]*$/ { next }
    {
      split_pos = index($0, "=")
      if (split_pos == 0) {
        fail(sprintf("invalid startup config %s: line %d: expected key = value", config_path, NR))
      }
      raw_key = trim(substr($0, 1, split_pos - 1))
      raw_value = trim(substr($0, split_pos + 1))
      if (raw_key == "") {
        fail(sprintf("invalid startup config %s: line %d: key must be non-empty", config_path, NR))
      }
      if (raw_key == "webauth_enabled" ||
          raw_key == "swarm_role" ||
          raw_key == "swarm_id" ||
          raw_key == "swarm" "_mode" ||
          raw_key == "advertise_mode" ||
          raw_key == "advertise_addr" ||
          raw_key == "onboarding_state" ||
          raw_key == "network_mode" ||
          raw_key == "tailscale_transport_port" ||
          raw_key ~ ("^deploy_" "container_")) {
        next
      }
      if (raw_key == "mode" && raw_value != "lan" && raw_value != "tailscale") {
        next
      }
      if (!(raw_key in allow_empty) && raw_value == "") {
        fail(sprintf("invalid startup config %s: line %d: value for \"%s\" must be non-empty", config_path, NR, raw_key))
      }
      if (!(raw_key in valid)) {
        fail(sprintf("invalid startup config %s: line %d: unknown key \"%s\"", config_path, NR, raw_key))
      }
      if (raw_key in seen) {
        fail(sprintf("invalid startup config %s: line %d: duplicate key \"%s\"", config_path, NR, raw_key))
      }
      seen[raw_key] = 1
      values[raw_key] = raw_value
    }
    END {
      if (had_error) {
        exit 1
      }
      if (!("dev_mode" in seen)) {
        fail(sprintf("invalid startup config %s: missing dev_mode", config_path))
      }
      if (!("dev_root" in seen)) {
        fail(sprintf("invalid startup config %s: missing dev_root", config_path))
      }
      if (!("host" in seen)) {
        fail(sprintf("invalid startup config %s: missing host", config_path))
      }
      if (!("port" in seen)) {
        fail(sprintf("invalid startup config %s: missing port", config_path))
      }
      if (!("advertise_host" in seen)) {
        fail(sprintf("invalid startup config %s: missing advertise_host", config_path))
      }
      if (!("advertise_port" in seen)) {
        fail(sprintf("invalid startup config %s: missing advertise_port", config_path))
      }
      if (!("desktop_port" in seen)) {
        fail(sprintf("invalid startup config %s: missing desktop_port", config_path))
      }
      if (!("bypass_permissions" in seen)) {
        fail(sprintf("invalid startup config %s: missing bypass_permissions", config_path))
      }
      if (!("retain_tool_output_history" in seen)) {
        fail(sprintf("invalid startup config %s: missing retain_tool_output_history", config_path))
      }
      if (!("v3_diagnostics" in seen)) {
        fail(sprintf("invalid startup config %s: missing v3_diagnostics", config_path))
      }
      if (!("provider_api_diagnostics" in seen)) {
        fail(sprintf("invalid startup config %s: missing provider_api_diagnostics", config_path))
      }
      if (!("long_session_diagnostics" in seen)) {
        fail(sprintf("invalid startup config %s: missing long_session_diagnostics", config_path))
      }
      if (!("swarm_name" in seen)) {
        fail(sprintf("invalid startup config %s: missing swarm_name", config_path))
      }
      if (!("desktop_onboarding_complete" in seen)) {
        fail(sprintf("invalid startup config %s: missing desktop_onboarding_complete", config_path))
      }
      if (!("child" in seen)) {
        fail(sprintf("invalid startup config %s: missing child", config_path))
      }
      if (!("mode" in seen)) {
        fail(sprintf("invalid startup config %s: missing mode", config_path))
      }
      if (!("tailscale_url" in seen)) {
        fail(sprintf("invalid startup config %s: missing tailscale_url", config_path))
      }
      if (!("peer_transport_port" in seen)) {
        fail(sprintf("invalid startup config %s: missing peer_transport_port", config_path))
      }
      if (values["dev_mode"] != "true" && values["dev_mode"] != "false") {
        fail(sprintf("invalid startup config %s: dev_mode must be true or false", config_path))
      }
      if (values["dev_root"] != "" && substr(values["dev_root"], 1, 1) != "/") {
        fail(sprintf("invalid startup config %s: dev_root must be empty or an absolute path", config_path))
      }
      if (values["host"] == "") {
        fail(sprintf("invalid startup config %s: host must not be empty", config_path))
      }
      if (values["port"] !~ /^[0-9]+$/) {
        fail(sprintf("invalid startup config %s: port must be numeric", config_path))
      }
      if (values["advertise_port"] !~ /^[0-9]+$/) {
        fail(sprintf("invalid startup config %s: advertise_port must be numeric", config_path))
      }
      if (values["desktop_port"] !~ /^[0-9]+$/) {
        fail(sprintf("invalid startup config %s: desktop_port must be numeric", config_path))
      }
      if (("local_transport_port" in seen) && values["local_transport_port"] !~ /^[0-9]+$/) {
        fail(sprintf("invalid startup config %s: local_transport_port must be numeric", config_path))
      }
      if (values["peer_transport_port"] !~ /^[0-9]+$/) {
        fail(sprintf("invalid startup config %s: peer_transport_port must be numeric", config_path))
      }
      if (values["bypass_permissions"] != "true" && values["bypass_permissions"] != "false") {
        fail(sprintf("invalid startup config %s: bypass_permissions must be true or false", config_path))
      }
      if (values["retain_tool_output_history"] != "true" && values["retain_tool_output_history"] != "false") {
        fail(sprintf("invalid startup config %s: retain_tool_output_history must be true or false", config_path))
      }
      if (values["v3_diagnostics"] != "true" && values["v3_diagnostics"] != "false") {
        fail(sprintf("invalid startup config %s: v3_diagnostics must be true or false", config_path))
      }
      if (values["provider_api_diagnostics"] != "true" && values["provider_api_diagnostics"] != "false") {
        fail(sprintf("invalid startup config %s: provider_api_diagnostics must be true or false", config_path))
      }
      if (values["long_session_diagnostics"] != "true" && values["long_session_diagnostics"] != "false") {
        fail(sprintf("invalid startup config %s: long_session_diagnostics must be true or false", config_path))
      }
      if (values["desktop_onboarding_complete"] != "true" && values["desktop_onboarding_complete"] != "false") {
        fail(sprintf("invalid startup config %s: desktop_onboarding_complete must be true or false", config_path))
      }
      if (values["child"] != "true" && values["child"] != "false") {
        fail(sprintf("invalid startup config %s: child must be true or false", config_path))
      }
      if (values["mode"] != "lan" && values["mode"] != "tailscale") {
        fail(sprintf("invalid startup config %s: mode must be lan or tailscale", config_path))
      }
      port_num = values["port"] + 0
      if (port_num < 1 || port_num > 65535) {
        fail(sprintf("invalid startup config %s: port must be between 1 and 65535", config_path))
      }
      advertise_port_num = values["advertise_port"] + 0
      if (advertise_port_num < 1 || advertise_port_num > 65535) {
        fail(sprintf("invalid startup config %s: advertise_port must be between 1 and 65535", config_path))
      }
      desktop_port_num = values["desktop_port"] + 0
      if (desktop_port_num < 0 || desktop_port_num > 65535) {
        fail(sprintf("invalid startup config %s: desktop_port must be between 0 and 65535", config_path))
      }
      if ("local_transport_port" in seen) {
        local_transport_port_num = values["local_transport_port"] + 0
        if (local_transport_port_num < 1 || local_transport_port_num > 65535) {
          fail(sprintf("invalid startup config %s: local_transport_port must be between 1 and 65535", config_path))
        }
      }
      peer_transport_port_num = values["peer_transport_port"] + 0
      if (peer_transport_port_num < 1 || peer_transport_port_num > 65535) {
        fail(sprintf("invalid startup config %s: peer_transport_port must be between 1 and 65535", config_path))
      }
    }
  ' "${config_path}"
}

swarm_startup_config_value() {
  local key="${1:-}"
  local config_path
  config_path="$(swarm_startup_config_path)"

  if [[ -z "${key}" ]]; then
    echo "missing startup config key" >&2
    return 1
  fi

  swarm_startup_config_validate || return 1

  awk -F= -v wanted="${key}" '
    /^[[:space:]]*#/ || /^[[:space:]]*$/ { next }
    {
      raw_key = $1
      raw_value = substr($0, index($0, "=") + 1)
      gsub(/^[[:space:]]+|[[:space:]]+$/, "", raw_key)
      gsub(/^[[:space:]]+|[[:space:]]+$/, "", raw_value)
      if (raw_key == wanted) {
        print raw_value
        found = 1
        exit 0
      }
    }
    END {
      if (!found) {
        exit 1
      }
    }
  ' "${config_path}"
}

swarm_startup_host() {
  local host
  host="$(swarm_startup_config_value host)" || return 1
  printf "%s\n" "${host}"
}

swarm_startup_port() {
  local port
  port="$(swarm_startup_config_value port)" || return 1
  printf "%s\n" "${port}"
}

swarm_startup_desktop_port() {
  local port
  port="$(swarm_startup_config_value desktop_port)" || return 1
  printf "%s\n" "${port}"
}

swarm_startup_bypass_permissions() {
  local value
  value="$(swarm_startup_config_value bypass_permissions)" || return 1
  printf "%s\n" "${value}"
}

swarm_startup_v3_diagnostics() {
  local value
  value="$(swarm_startup_config_value v3_diagnostics)" || return 1
  if [[ "${value}" == "true" ]]; then
    printf "1\n"
  else
    printf "0\n"
  fi
}

swarm_startup_provider_api_diagnostics() {
  local value
  value="$(swarm_startup_config_value provider_api_diagnostics)" || return 1
  if [[ "${value}" == "true" ]]; then
    printf "1\n"
  else
    printf "0\n"
  fi
}

swarm_startup_dev_mode() {
  local value
  value="$(swarm_startup_config_value dev_mode)" || return 1
  printf "%s\n" "${value}"
}

swarm_startup_dev_root() {
  local value
  value="$(swarm_startup_config_value dev_root)" || return 1
  printf "%s\n" "${value}"
}

swarm_lane_backend_port() {
  local lane="${1:-}"
  local port
  port="$(swarm_startup_port)" || return 1

  case "${lane}" in
  main)
    printf "%s\n" "${port}"
    ;;
  dev)
    if ((10#${port} >= 65535)); then
      echo "invalid startup config $(swarm_startup_config_path): dev lane backend port would exceed 65535" >&2
      return 1
    fi
    printf "%s\n" "$((10#${port} + 1))"
    ;;
  *)
    echo "unsupported lane: ${lane}" >&2
    return 1
    ;;
  esac
}

swarm_lane_desktop_port() {
  local lane="${1:-}"
  local port
  port="$(swarm_startup_desktop_port)" || return 1

  case "${lane}" in
  main)
    printf "%s\n" "${port}"
    ;;
  dev)
    if ((10#${port} >= 65535)); then
      echo "invalid startup config $(swarm_startup_config_path): dev lane desktop port would exceed 65535" >&2
      return 1
    fi
    printf "%s\n" "$((10#${port} + 1))"
    ;;
  *)
    echo "unsupported lane: ${lane}" >&2
    return 1
    ;;
  esac
}

swarm_lane_listen_addr() {
  local lane="${1:-}"
  local host
  local port
  host="$(swarm_startup_host)" || return 1
  port="$(swarm_lane_backend_port "${lane}")" || return 1
  printf "%s:%s\n" "${host}" "${port}"
}

swarm_lane_port() {
  local listen="${1:-}"
  if [[ "${listen}" =~ :([0-9]+)$ ]]; then
    printf "%s\n" "${BASH_REMATCH[1]}"
    return 0
  fi
  return 1
}

swarm_lane_export_profile() {
  local lane="${1:-}"
  local repo_root="${2:-}"

  if [[ -z "${lane}" || -z "${repo_root}" ]]; then
    echo "usage: swarm_lane_export_profile <main|dev> <repo-root>" >&2
    return 1
  fi

  case "${lane}" in
  main|dev)
    ;;
  *)
    echo "unsupported lane: ${lane}" >&2
    return 1
    ;;
  esac

  swarm_provision_system_paths "${lane}"

  local listen
  listen="$(swarm_lane_listen_addr "${lane}")" || return 1

  local port
  if ! port="$(swarm_lane_port "${listen}")"; then
    echo "invalid listen address from startup config: ${listen} (expected host:port)" >&2
    return 1
  fi

  local dev_mode
  local dev_root
  local bypass_permissions
  local v3_diagnostics
  local provider_api_diagnostics
  local desktop_port

  local daemon_config_root
  daemon_config_root="$(swarm_daemon_config_root)"
  local daemon_state_root
  daemon_state_root="$(swarm_daemon_runtime_root "${lane}")"
  local daemon_data_root
  daemon_data_root="$(swarm_daemon_data_root "${lane}")"
  local daemon_log_root
  daemon_log_root="$(swarm_daemon_log_root "${lane}")"
  local daemon_ports_root
  daemon_ports_root="$(swarm_daemon_ports_root)"

  dev_mode="$(swarm_startup_dev_mode)" || return 1
  dev_root="$(swarm_startup_dev_root)" || return 1
  bypass_permissions="$(swarm_startup_bypass_permissions)" || return 1
  v3_diagnostics="$(swarm_startup_v3_diagnostics)" || return 1
  provider_api_diagnostics="$(swarm_startup_provider_api_diagnostics)" || return 1
  desktop_port="$(swarm_lane_desktop_port "${lane}")" || return 1

  export SWARM_LANE="${lane}"
  export SWARM_LANE_PORT="${port}"
  export SWARM_STATE_HOME="${daemon_state_root}"
  export SWARM_CONFIG_HOME="${daemon_config_root}"
  export SWARM_STARTUP_CONFIG="$(swarm_startup_config_path)"
  export SWARM_DEV_MODE="${dev_mode}"
  export SWARM_DEV_ROOT="${dev_root}"
  export SWARM_BYPASS_PERMISSIONS="${bypass_permissions}"
  export SWARM_V3_DIAGNOSTICS="${v3_diagnostics}"
  export SWARM_PROVIDER_API_DIAGNOSTICS="${provider_api_diagnostics}"

  export SWARMD_LISTEN="${listen}"
  export SWARMD_URL="http://${listen}"
  export SWARM_DESKTOP_PORT="${desktop_port}"

  export STATE_ROOT="${daemon_state_root}"
  export DATA_DIR="${daemon_data_root}"
  export DB_PATH="${daemon_data_root}/swarmd.pebble"
  export LOCK_PATH="${daemon_state_root}/swarmd.lock"
  export PID_FILE="${daemon_state_root}/swarmd.pid"
  export LOG_FILE="${daemon_log_root}/swarmd.log"

  export SWARM_BIN_DIR="$(swarm_lane_bin_dir "${lane}")"
  export SWARM_TOOL_BIN_DIR="$(swarm_lane_tool_bin_dir)"
  export SWARM_WEB_DIR="${repo_root}/web"
  export SWARM_WEB_DIST_DIR="$(swarm_lane_desktop_dist_dir)"

  export SWARM_PORTS_DIR="${daemon_ports_root}"
  export SWARM_PORT_RECORD="${SWARM_PORTS_DIR}/swarmd-${lane}.env"

  # Compatibility env expected by existing swarmd scripts.
  export LISTEN="${SWARMD_LISTEN}"
  export ADDR="${SWARMD_URL}"
}

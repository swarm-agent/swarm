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
  local data_home
  data_home="$(swarm_lane_data_home)"
  printf "%s\n" "${data_home}/swarm/bin"
}

swarm_lane_install_root() {
  local data_home
  data_home="$(swarm_lane_data_home)"
  printf "%s\n" "${data_home}/swarm"
}

swarm_lane_tool_bin_dir() {
  printf "%s\n" "$(swarm_lane_install_root)/libexec"
}

swarm_lane_bin_dir() {
  local lane="${1:-}"
  : "${lane}"
  printf "%s\n" "$(swarm_lane_binary_root)"
}

swarm_lane_desktop_dist_dir() {
  printf "%s\n" "$(swarm_lane_install_root)/share"
}

swarm_startup_config_path() {
  local config_home
  config_home="$(swarm_lane_config_home)"
  printf "%s\n" "${config_home}/swarm/swarm.conf"
}

swarm_startup_config_ensure() {
  local config_path
  config_path="$(swarm_startup_config_path)"
  if [[ -f "${config_path}" ]]; then
    return 0
  fi

  mkdir -p "$(dirname -- "${config_path}")"
  cat >"${config_path}" <<'EOF'
startup_mode = interactive
host = 127.0.0.1
port = 7781
advertise_host =
advertise_port = 7781
desktop_port = 5555
bypass_permissions = false
retain_tool_output_history = false
swarm_name =
swarm_mode = false
child = false
mode = lan
tailscale_url =
peer_transport_port = 7791
parent_swarm_id =
pairing_state =
deploy_container_enabled = false
deploy_container_host_driven = false
deploy_container_sync_enabled = false
deploy_container_sync_mode =
deploy_container_sync_modules =
deploy_container_sync_owner_swarm_id =
deploy_container_sync_credential_url =
deploy_container_sync_agent_url =
deploy_container_deployment_id =
deploy_container_host_api_base_url =
deploy_container_host_desktop_url =
deploy_container_local_transport_socket_path =
deploy_container_bootstrap_secret =
deploy_container_verification_code =
remote_deploy_enabled = false
remote_deploy_session_id =
remote_deploy_session_token =
remote_deploy_host_api_base_url =
remote_deploy_host_desktop_url =
remote_deploy_invite_token =
remote_deploy_sync_enabled = false
remote_deploy_sync_mode =
remote_deploy_sync_owner_swarm_id =
remote_deploy_sync_credential_url =
EOF
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
  local port_value startup_mode_value swarm_mode_value child_value network_mode_value advertise_host_value tailscale_url_value
  config_path="$(swarm_startup_config_path)"
  swarm_startup_config_ensure

  port_value="$(swarm_startup_config_raw_value port 2>/dev/null || true)"
  if [[ ! "${port_value}" =~ ^[0-9]+$ ]]; then
    port_value="7781"
  fi
  startup_mode_value="$(swarm_startup_config_raw_value startup_mode 2>/dev/null || true)"
  if [[ -z "${startup_mode_value}" ]]; then
    startup_mode_value="$(swarm_startup_config_raw_value mode 2>/dev/null || true)"
  fi
  case "${startup_mode_value}" in
    interactive|box) ;;
    *) startup_mode_value="interactive" ;;
  esac
  child_value="$(swarm_startup_config_raw_value child 2>/dev/null || true)"
  if [[ -z "${child_value}" ]]; then
    case "$(swarm_startup_config_raw_value swarm_role 2>/dev/null || true)" in
      child) child_value="true" ;;
      *) child_value="false" ;;
    esac
  fi
  swarm_mode_value="$(swarm_startup_config_raw_value swarm_mode 2>/dev/null || true)"
  case "${swarm_mode_value}" in
    true|false) ;;
    *)
      if [[ -n "$(swarm_startup_config_raw_value swarm_role 2>/dev/null || true)" ]]; then
        swarm_mode_value="true"
      else
        swarm_mode_value="false"
      fi
      ;;
  esac
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

  if ! swarm_startup_config_has_key startup_mode; then
    cat >>"${config_path}" <<EOF

# Swarm startup mode.
# interactive = normal local use; Swarm runs when you launch it.
# box = always-on box mode; Swarm should be treated as an always-running service.
startup_mode = ${startup_mode_value}
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

  if ! swarm_startup_config_has_key swarm_name; then
    cat >>"${config_path}" <<'EOF'

# Human-readable Swarm name shown in onboarding and discovery surfaces.
# Leave blank to set it later.
swarm_name =
EOF
  fi

  if ! swarm_startup_config_has_key swarm_mode; then
    cat >>"${config_path}" <<EOF

# Whether this Swarm should participate in shared swarm networking.
# false = standalone local use, true = enable swarm role/pairing/transport settings.
swarm_mode = ${swarm_mode_value}
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

  if ! swarm_startup_config_has_key parent_swarm_id; then
    cat >>"${config_path}" <<'EOF'

# Parent swarm ID for child bootstrap/attach flows.
parent_swarm_id =
EOF
  fi

  if ! swarm_startup_config_has_key pairing_state; then
    cat >>"${config_path}" <<'EOF'

# Persisted local pairing state.
pairing_state =
EOF
  fi

  if ! swarm_startup_config_has_key deploy_container_enabled; then
    cat >>"${config_path}" <<'EOF'

# Deploy/container child attach bootstrap payload.
deploy_container_enabled = false
deploy_container_host_driven = false
deploy_container_sync_enabled = false
deploy_container_sync_mode =
deploy_container_sync_modules =
deploy_container_sync_owner_swarm_id =
deploy_container_sync_credential_url =
deploy_container_sync_agent_url =
deploy_container_deployment_id =
deploy_container_host_api_base_url =
deploy_container_host_desktop_url =
deploy_container_local_transport_socket_path =
deploy_container_bootstrap_secret =
deploy_container_verification_code =
EOF
  fi

  if ! swarm_startup_config_has_key deploy_container_host_driven; then
    cat >>"${config_path}" <<'EOF'
deploy_container_host_driven = false
EOF
  fi

  if ! swarm_startup_config_has_key deploy_container_sync_enabled; then
    cat >>"${config_path}" <<'EOF'
deploy_container_sync_enabled = false
EOF
  fi

  if ! swarm_startup_config_has_key deploy_container_sync_mode; then
    cat >>"${config_path}" <<'EOF'
deploy_container_sync_mode =
EOF
  fi

  if ! swarm_startup_config_has_key deploy_container_sync_modules; then
    cat >>"${config_path}" <<'EOF'
deploy_container_sync_modules =
EOF
  fi

  if ! swarm_startup_config_has_key deploy_container_sync_owner_swarm_id; then
    cat >>"${config_path}" <<'EOF'
deploy_container_sync_owner_swarm_id =
EOF
  fi

  if ! swarm_startup_config_has_key deploy_container_sync_credential_url; then
    cat >>"${config_path}" <<'EOF'
deploy_container_sync_credential_url =
EOF
  fi

  if ! swarm_startup_config_has_key deploy_container_sync_agent_url; then
    cat >>"${config_path}" <<'EOF'
deploy_container_sync_agent_url =
EOF
  fi

  if ! swarm_startup_config_has_key deploy_container_local_transport_socket_path; then
    cat >>"${config_path}" <<'EOF'
deploy_container_local_transport_socket_path =
EOF
  fi

  if ! swarm_startup_config_has_key remote_deploy_enabled; then
    cat >>"${config_path}" <<'EOF'

# Remote deploy child bootstrap payload.
remote_deploy_enabled = false
remote_deploy_session_id =
remote_deploy_session_token =
remote_deploy_host_api_base_url =
remote_deploy_host_desktop_url =
remote_deploy_invite_token =
remote_deploy_sync_enabled = false
remote_deploy_sync_mode =
remote_deploy_sync_owner_swarm_id =
remote_deploy_sync_credential_url =
EOF
  fi

  if ! swarm_startup_config_has_key remote_deploy_sync_enabled; then
    cat >>"${config_path}" <<'EOF'
remote_deploy_sync_enabled = false
EOF
  fi

  if ! swarm_startup_config_has_key remote_deploy_sync_mode; then
    cat >>"${config_path}" <<'EOF'
remote_deploy_sync_mode =
EOF
  fi

  if ! swarm_startup_config_has_key remote_deploy_sync_owner_swarm_id; then
    cat >>"${config_path}" <<'EOF'
remote_deploy_sync_owner_swarm_id =
EOF
  fi

  if ! swarm_startup_config_has_key remote_deploy_sync_credential_url; then
    cat >>"${config_path}" <<'EOF'
remote_deploy_sync_credential_url =
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
      valid["startup_mode"] = 1
      valid["host"] = 1
      valid["port"] = 1
      valid["advertise_host"] = 1
      valid["advertise_port"] = 1
      valid["desktop_port"] = 1
      valid["bypass_permissions"] = 1
      valid["retain_tool_output_history"] = 1
      valid["swarm_name"] = 1
      valid["swarm_mode"] = 1
      valid["child"] = 1
      valid["mode"] = 1
      valid["tailscale_url"] = 1
      valid["local_transport_port"] = 1
      valid["peer_transport_port"] = 1
      valid["parent_swarm_id"] = 1
      valid["pairing_state"] = 1
      valid["deploy_container_enabled"] = 1
      valid["deploy_container_host_driven"] = 1
      valid["deploy_container_sync_enabled"] = 1
      valid["deploy_container_sync_mode"] = 1
      valid["deploy_container_sync_modules"] = 1
      valid["deploy_container_sync_owner_swarm_id"] = 1
      valid["deploy_container_sync_credential_url"] = 1
      valid["deploy_container_sync_agent_url"] = 1
      valid["deploy_container_deployment_id"] = 1
      valid["deploy_container_host_api_base_url"] = 1
      valid["deploy_container_host_desktop_url"] = 1
      valid["deploy_container_local_transport_socket_path"] = 1
      valid["deploy_container_bootstrap_secret"] = 1
      valid["deploy_container_verification_code"] = 1
      valid["remote_deploy_enabled"] = 1
      valid["remote_deploy_session_id"] = 1
      valid["remote_deploy_session_token"] = 1
      valid["remote_deploy_host_api_base_url"] = 1
      valid["remote_deploy_host_desktop_url"] = 1
      valid["remote_deploy_invite_token"] = 1
      valid["remote_deploy_sync_enabled"] = 1
      valid["remote_deploy_sync_mode"] = 1
      valid["remote_deploy_sync_owner_swarm_id"] = 1
      valid["remote_deploy_sync_credential_url"] = 1
      allow_empty["swarm_name"] = 1
      allow_empty["advertise_host"] = 1
      allow_empty["tailscale_url"] = 1
      allow_empty["parent_swarm_id"] = 1
      allow_empty["pairing_state"] = 1
      allow_empty["deploy_container_sync_mode"] = 1
      allow_empty["deploy_container_sync_modules"] = 1
      allow_empty["deploy_container_sync_owner_swarm_id"] = 1
      allow_empty["deploy_container_sync_credential_url"] = 1
      allow_empty["deploy_container_sync_agent_url"] = 1
      allow_empty["deploy_container_deployment_id"] = 1
      allow_empty["deploy_container_host_api_base_url"] = 1
      allow_empty["deploy_container_host_desktop_url"] = 1
      allow_empty["deploy_container_local_transport_socket_path"] = 1
      allow_empty["deploy_container_bootstrap_secret"] = 1
      allow_empty["deploy_container_verification_code"] = 1
      allow_empty["remote_deploy_session_id"] = 1
      allow_empty["remote_deploy_session_token"] = 1
      allow_empty["remote_deploy_host_api_base_url"] = 1
      allow_empty["remote_deploy_host_desktop_url"] = 1
      allow_empty["remote_deploy_invite_token"] = 1
      allow_empty["remote_deploy_sync_mode"] = 1
      allow_empty["remote_deploy_sync_owner_swarm_id"] = 1
      allow_empty["remote_deploy_sync_credential_url"] = 1
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
      if (raw_key == "webauth_enabled" || raw_key == "swarm_role" || raw_key == "swarm_id" || raw_key == "advertise_mode" || raw_key == "advertise_addr" || raw_key == "onboarding_state" || raw_key == "network_mode" || raw_key == "tailscale_transport_port") {
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
      if (!("startup_mode" in seen)) {
        fail(sprintf("invalid startup config %s: missing startup_mode", config_path))
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
      if (!("swarm_name" in seen)) {
        fail(sprintf("invalid startup config %s: missing swarm_name", config_path))
      }
      if (!("swarm_mode" in seen)) {
        fail(sprintf("invalid startup config %s: missing swarm_mode", config_path))
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
      if (!("parent_swarm_id" in seen)) {
        fail(sprintf("invalid startup config %s: missing parent_swarm_id", config_path))
      }
      if (!("pairing_state" in seen)) {
        fail(sprintf("invalid startup config %s: missing pairing_state", config_path))
      }
      if (!("deploy_container_enabled" in seen)) {
        fail(sprintf("invalid startup config %s: missing deploy_container_enabled", config_path))
      }
      if (!("deploy_container_sync_enabled" in seen)) {
        fail(sprintf("invalid startup config %s: missing deploy_container_sync_enabled", config_path))
      }
      if (!("deploy_container_sync_mode" in seen)) {
        fail(sprintf("invalid startup config %s: missing deploy_container_sync_mode", config_path))
      }
      if (!("deploy_container_sync_modules" in seen)) {
        fail(sprintf("invalid startup config %s: missing deploy_container_sync_modules", config_path))
      }
      if (!("deploy_container_sync_owner_swarm_id" in seen)) {
        fail(sprintf("invalid startup config %s: missing deploy_container_sync_owner_swarm_id", config_path))
      }
      if (!("deploy_container_sync_credential_url" in seen)) {
        fail(sprintf("invalid startup config %s: missing deploy_container_sync_credential_url", config_path))
      }
      if (!("deploy_container_sync_agent_url" in seen)) {
        fail(sprintf("invalid startup config %s: missing deploy_container_sync_agent_url", config_path))
      }
      if (!("deploy_container_deployment_id" in seen)) {
        fail(sprintf("invalid startup config %s: missing deploy_container_deployment_id", config_path))
      }
      if (!("deploy_container_host_api_base_url" in seen)) {
        fail(sprintf("invalid startup config %s: missing deploy_container_host_api_base_url", config_path))
      }
      if (!("deploy_container_host_desktop_url" in seen)) {
        fail(sprintf("invalid startup config %s: missing deploy_container_host_desktop_url", config_path))
      }
      if (!("deploy_container_local_transport_socket_path" in seen)) {
        fail(sprintf("invalid startup config %s: missing deploy_container_local_transport_socket_path", config_path))
      }
      if (!("deploy_container_bootstrap_secret" in seen)) {
        fail(sprintf("invalid startup config %s: missing deploy_container_bootstrap_secret", config_path))
      }
      if (!("deploy_container_verification_code" in seen)) {
        fail(sprintf("invalid startup config %s: missing deploy_container_verification_code", config_path))
      }
      if (!("remote_deploy_enabled" in seen)) {
        fail(sprintf("invalid startup config %s: missing remote_deploy_enabled", config_path))
      }
      if (!("remote_deploy_session_id" in seen)) {
        fail(sprintf("invalid startup config %s: missing remote_deploy_session_id", config_path))
      }
      if (!("remote_deploy_session_token" in seen)) {
        fail(sprintf("invalid startup config %s: missing remote_deploy_session_token", config_path))
      }
      if (!("remote_deploy_host_api_base_url" in seen)) {
        fail(sprintf("invalid startup config %s: missing remote_deploy_host_api_base_url", config_path))
      }
      if (!("remote_deploy_host_desktop_url" in seen)) {
        fail(sprintf("invalid startup config %s: missing remote_deploy_host_desktop_url", config_path))
      }
      if (!("remote_deploy_invite_token" in seen)) {
        fail(sprintf("invalid startup config %s: missing remote_deploy_invite_token", config_path))
      }
      if (!("remote_deploy_sync_enabled" in seen)) {
        fail(sprintf("invalid startup config %s: missing remote_deploy_sync_enabled", config_path))
      }
      if (!("remote_deploy_sync_mode" in seen)) {
        fail(sprintf("invalid startup config %s: missing remote_deploy_sync_mode", config_path))
      }
      if (!("remote_deploy_sync_owner_swarm_id" in seen)) {
        fail(sprintf("invalid startup config %s: missing remote_deploy_sync_owner_swarm_id", config_path))
      }
      if (!("remote_deploy_sync_credential_url" in seen)) {
        fail(sprintf("invalid startup config %s: missing remote_deploy_sync_credential_url", config_path))
      }
      if (values["startup_mode"] != "interactive" && values["startup_mode"] != "box") {
        fail(sprintf("invalid startup config %s: invalid startup_mode \"%s\"", config_path, values["startup_mode"]))
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
      if (values["swarm_mode"] != "true" && values["swarm_mode"] != "false") {
        fail(sprintf("invalid startup config %s: swarm_mode must be true or false", config_path))
      }
      if (values["child"] != "true" && values["child"] != "false") {
        fail(sprintf("invalid startup config %s: child must be true or false", config_path))
      }
      if (values["mode"] != "lan" && values["mode"] != "tailscale") {
        fail(sprintf("invalid startup config %s: mode must be lan or tailscale", config_path))
      }
      if (values["pairing_state"] != "" &&
          values["pairing_state"] != "unpaired" &&
          values["pairing_state"] != "bootstrap_configured" &&
          values["pairing_state"] != "pending_approval" &&
          values["pairing_state"] != "paired" &&
          values["pairing_state"] != "rejected") {
        fail(sprintf("invalid startup config %s: pairing_state must be empty or a known state", config_path))
      }
      if (values["deploy_container_enabled"] != "true" && values["deploy_container_enabled"] != "false") {
        fail(sprintf("invalid startup config %s: deploy_container_enabled must be true or false", config_path))
      }
      if ("deploy_container_host_driven" in seen &&
          values["deploy_container_host_driven"] != "true" &&
          values["deploy_container_host_driven"] != "false") {
        fail(sprintf("invalid startup config %s: deploy_container_host_driven must be true or false", config_path))
      }
      if (values["deploy_container_sync_enabled"] != "true" && values["deploy_container_sync_enabled"] != "false") {
        fail(sprintf("invalid startup config %s: deploy_container_sync_enabled must be true or false", config_path))
      }
      if (values["remote_deploy_enabled"] != "true" && values["remote_deploy_enabled"] != "false") {
        fail(sprintf("invalid startup config %s: remote_deploy_enabled must be true or false", config_path))
      }
      if (values["remote_deploy_sync_enabled"] != "true" && values["remote_deploy_sync_enabled"] != "false") {
        fail(sprintf("invalid startup config %s: remote_deploy_sync_enabled must be true or false", config_path))
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

swarm_startup_mode() {
  local mode
  mode="$(swarm_startup_config_value startup_mode)" || return 1
  printf "%s\n" "${mode}"
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

  local listen
  listen="$(swarm_lane_listen_addr "${lane}")" || return 1

  local port
  if ! port="$(swarm_lane_port "${listen}")"; then
    echo "invalid listen address from startup config: ${listen} (expected host:port)" >&2
    return 1
  fi

  local state_home
  state_home="$(swarm_lane_state_home)"
  local config_home
  config_home="$(swarm_lane_config_home)"
  local data_home
  data_home="$(swarm_lane_data_home)"
  local startup_mode
  local bypass_permissions
  local desktop_port

  local swarm_state_home="${state_home}/swarm"
  local daemon_state_root="${swarm_state_home}/swarmd/${lane}"
  local daemon_data_root="${data_home}/swarmd/${lane}"

  startup_mode="$(swarm_startup_mode)" || return 1
  bypass_permissions="$(swarm_startup_bypass_permissions)" || return 1
  desktop_port="$(swarm_lane_desktop_port "${lane}")" || return 1

  export SWARM_LANE="${lane}"
  export SWARM_LANE_PORT="${port}"
  export SWARM_STATE_HOME="${swarm_state_home}"
  export SWARM_CONFIG_HOME="${config_home}/swarm"
  export SWARM_STARTUP_CONFIG="$(swarm_startup_config_path)"
  export SWARM_STARTUP_MODE="${startup_mode}"
  export SWARM_BYPASS_PERMISSIONS="${bypass_permissions}"

  export SWARMD_LISTEN="${listen}"
  export SWARMD_URL="http://${listen}"
  export SWARM_DESKTOP_PORT="${desktop_port}"

  export STATE_ROOT="${daemon_state_root}"
  export DATA_DIR="${daemon_data_root}"
  export DB_PATH="${daemon_data_root}/swarmd.pebble"
  export LOCK_PATH="${daemon_state_root}/swarmd.lock"
  export PID_FILE="${daemon_state_root}/swarmd.pid"
  export LOG_FILE="${daemon_state_root}/swarmd.log"

  export SWARM_BIN_DIR="$(swarm_lane_bin_dir "${lane}")"
  export SWARM_TOOL_BIN_DIR="$(swarm_lane_tool_bin_dir)"
  export SWARM_WEB_DIR="${repo_root}/web"
  export SWARM_WEB_DIST_DIR="$(swarm_lane_desktop_dist_dir)"

  export SWARM_PORTS_DIR="${swarm_state_home}/ports"
  export SWARM_PORT_RECORD="${SWARM_PORTS_DIR}/swarmd-${lane}.env"

  # Compatibility env expected by existing swarmd scripts.
  export LISTEN="${SWARMD_LISTEN}"
  export ADDR="${SWARMD_URL}"
}

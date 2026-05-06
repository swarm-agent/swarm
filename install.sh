#!/bin/sh
set -eu

REPO="swarm-agent/swarm"
DEFAULT_VERSION=""
INSTALL_VERSION=""
ARTIFACT_ROOT=""
INSTALL_SYSTEM_SERVICE=0

usage() {
  cat <<'EOF'
Usage:
  sh install.sh [--version <tag>] [--artifact-root <path>] [--install-systemd-service]

Options:
  --install-systemd-service  install the runtime under system paths and enable swarmd.service

EOF
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --version)
      INSTALL_VERSION="${2:-}"
      shift 2
      ;;
    --artifact-root)
      ARTIFACT_ROOT="${2:-}"
      shift 2
      ;;
    --install-systemd-service)
      INSTALL_SYSTEM_SERVICE=1
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "unsupported argument: $1" >&2
      exit 2
      ;;
  esac
done

need_cmd() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "missing required command: $1" >&2
    exit 1
  }
}

resolve_script_dir() {
  if [ -z "${0:-}" ] || [ ! -f "$0" ]; then
    return 1
  fi
  CDPATH= cd -- "$(dirname -- "$0")" && pwd
}

read_build_info_version() {
  build_info="$1/build-info.txt"
  if [ ! -f "$build_info" ]; then
    return 1
  fi
  sed -n 's/^version=//p' "$build_info" | head -n 1
}

parse_first_tag_name() {
  sed -n 's/^[[:space:]]*"tag_name":[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1
}

is_stable_release_tag() {
  tag="$1"
  printf '%s\n' "$tag" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+$'
}

resolve_release_version() {
  latest_api="https://api.github.com/repos/${REPO}/releases/latest"

  version="$(curl -fsSL "$latest_api" 2>/dev/null | parse_first_tag_name || true)"
  if [ -z "$version" ]; then
    return 1
  fi
  if ! is_stable_release_tag "$version"; then
    echo "latest stable release tag is not a stable semver tag: $version" >&2
    return 1
  fi
  printf '%s\n' "$version"
  return 0
}

print_installing() {
  version="$1"
  if [ -n "$version" ]; then
    printf 'installing swarm (%s)\n' "$version"
  else
    printf 'installing swarm\n'
  fi
}

print_ok() {
  printf 'ok\n'
}

system_bin_home() {
  printf '%s\n' "/usr/local/bin"
}

system_data_home() {
  printf '%s\n' "/usr/local/share"
}

bin_home() {
  if [ "$INSTALL_SYSTEM_SERVICE" = "1" ]; then
    system_bin_home
  elif [ -n "${XDG_BIN_HOME:-}" ]; then
    printf '%s\n' "$XDG_BIN_HOME"
  elif [ -n "${HOME:-}" ]; then
    printf '%s/.local/bin\n' "$HOME"
  else
    echo "HOME is required unless XDG_BIN_HOME is set" >&2
    return 1
  fi
}

data_home() {
  if [ "$INSTALL_SYSTEM_SERVICE" = "1" ]; then
    system_data_home
  elif [ -n "${XDG_DATA_HOME:-}" ]; then
    printf '%s\n' "$XDG_DATA_HOME"
  elif [ -n "${HOME:-}" ]; then
    printf '%s/.local/share\n' "$HOME"
  else
    echo "HOME is required unless XDG_DATA_HOME is set" >&2
    return 1
  fi
}

install_root() {
  printf '%s/swarm\n' "$(data_home)"
}

verify_installed_runtime() {
  root="$(install_root)"
  bin_dir="$(bin_home)"

  for name in swarm swarmdev rebuild swarmsetup; do
    if [ ! -x "$bin_dir/$name" ]; then
      echo "installed launcher is missing or not executable: $bin_dir/$name" >&2
      return 1
    fi
  done

  for rel in \
    libexec/swarm \
    libexec/swarmdev \
    libexec/rebuild \
    libexec/swarmsetup \
    bin/swarmtui \
    bin/swarmd \
    bin/swarmctl \
    bin/swarm-fff-search
  do
    if [ ! -x "$root/$rel" ]; then
      echo "installed runtime executable is missing: $root/$rel" >&2
      return 1
    fi
  done

  for rel in lib/libfff_c.so share/index.html build-info.txt; do
    if [ ! -f "$root/$rel" ]; then
      echo "installed runtime file is missing: $root/$rel" >&2
      return 1
    fi
  done
}

bin_home_on_path() {
  target="$(bin_home)"
  case ":${PATH:-}:" in
    *":$target:"*)
      return 0
      ;;
    *)
      return 1
      ;;
  esac
}

current_shell_name() {
  shell_path="${SHELL:-}"
  shell_name="${shell_path##*/}"
  if [ -n "$shell_name" ]; then
    printf '%s\n' "$shell_name"
  else
    printf 'sh\n'
  fi
}

print_path_refresh_instructions() {
  target="$(bin_home)"

  if [ "$INSTALL_SYSTEM_SERVICE" = "1" ]; then
    printf '\nSwarm system service installed.\n'
    printf 'Daemon data roots are managed by systemd under /var/lib/swarmd, /var/cache/swarmd, /run/swarmd, /var/log/swarmd, and /etc/swarmd.\n'
    printf '\nCheck service status:\n'
    printf '  systemctl status swarmd.service\n'
    return 0
  fi

  if bin_home_on_path; then
    printf '\nStart Swarm:\n  swarm\n'
    return 0
  fi

  printf '\nSwarm installed.\n'
  printf '\nThis shell does not have %s on PATH yet.\n' "$target"

  if [ "$(current_shell_name)" = "fish" ]; then
    printf '\nIf you are using fish, copy/paste this now:\n'
    printf '  set -gx PATH "%s" $PATH\n' "$target"
    printf '\nIf new fish shells still cannot find swarm, copy/paste this once:\n'
    printf '  fish_add_path "%s"\n' "$target"
  else
    printf '\nCopy/paste this now:\n'
    printf '  export PATH="%s:$PATH"\n' "$target"
    printf '\nOr reload your shell:\n'
    printf '  exec "$SHELL" -l\n'
  fi

  printf '\nThen start Swarm:\n'
  printf '  swarm\n'
  printf '\nIf that still fails, run it directly:\n'
  printf '  %s\n' "$target/swarm"
}

warn_legacy_daemon_data() {
  found=""
  append_legacy_path() {
    candidate="$1"
    if [ -n "$candidate" ] && [ -e "$candidate" ]; then
      if [ -n "$found" ]; then
        found="$(printf '%s\n%s' "$found" "$candidate")"
      else
        found="$candidate"
      fi
    fi
  }

  if [ -n "${HOME:-}" ]; then
    append_legacy_path "$HOME/.local/share/swarmd"
    append_legacy_path "$HOME/.local/state/swarmd"
    append_legacy_path "$HOME/.cache/swarmd"
    append_legacy_path "$HOME/.config/swarmd"
  fi
  if [ -n "${XDG_DATA_HOME:-}" ]; then
    append_legacy_path "$XDG_DATA_HOME/swarmd"
  fi
  if [ -n "${XDG_STATE_HOME:-}" ]; then
    append_legacy_path "$XDG_STATE_HOME/swarmd"
  fi
  if [ -n "${XDG_CACHE_HOME:-}" ]; then
    append_legacy_path "$XDG_CACHE_HOME/swarmd"
  fi
  if [ -n "${XDG_CONFIG_HOME:-}" ]; then
    append_legacy_path "$XDG_CONFIG_HOME/swarmd"
  fi

  if [ -n "$found" ]; then
    printf '\nwarning: legacy swarmd daemon data was found under HOME/XDG paths:\n' >&2
    printf '%s' "$found" | while IFS= read -r legacy_path; do
      [ -n "$legacy_path" ] && printf '  %s\n' "$legacy_path" >&2
    done
    printf 'warning: this installer will not reuse or migrate those paths automatically.\n' >&2
    if [ "$INSTALL_SYSTEM_SERVICE" = "1" ]; then
      printf 'warning: systemd installs use StateDirectory=swarmd, CacheDirectory=swarmd, RuntimeDirectory=swarmd, LogsDirectory=swarmd, and ConfigurationDirectory=swarmd.\n' >&2
      printf 'warning: review and migrate any legacy data manually before relying on the new service.\n' >&2
    else
      printf 'warning: daemon defaults now use /var/lib/swarmd, /var/cache/swarmd, /run/swarmd, /var/log/swarmd, and /etc/swarmd.\n' >&2
    fi
  fi
}

nologin_shell() {
  if [ -x /usr/sbin/nologin ]; then
    printf '%s\n' /usr/sbin/nologin
  elif [ -x /sbin/nologin ]; then
    printf '%s\n' /sbin/nologin
  else
    printf '%s\n' /bin/false
  fi
}

ensure_swarmd_user() {
  need_cmd getent
  if ! getent group swarmd >/dev/null 2>&1; then
    need_cmd groupadd
    groupadd --system swarmd
  fi
  if ! id -u swarmd >/dev/null 2>&1; then
    need_cmd useradd
    useradd --system --gid swarmd --home-dir /nonexistent --shell "$(nologin_shell)" swarmd
  fi
}

write_systemd_unit() {
  root="$1"
  cat <<EOF
[Unit]
Description=Swarm daemon
Wants=network-online.target
After=network-online.target

[Service]
Type=exec
User=swarmd
Group=swarmd
WorkingDirectory=/var/lib/swarmd
Environment=LD_LIBRARY_PATH=$root/lib
Environment=SWARM_BIN_DIR=$root/bin
Environment=SWARM_TOOL_BIN_DIR=$root/libexec
Environment=SWARM_WEB_DIST_DIR=$root/share
Environment=SWARM_STARTUP_CONFIG=/etc/swarmd/swarm.conf
Environment=HOME=/nonexistent
ExecStart=$root/bin/swarmd --bypass-permissions --cwd /var/lib/swarmd
Restart=on-failure
RestartSec=5s
StateDirectory=swarmd
CacheDirectory=swarmd
RuntimeDirectory=swarmd
LogsDirectory=swarmd
ConfigurationDirectory=swarmd

[Install]
WantedBy=multi-user.target
EOF
}

install_systemd_service() {
  if [ "$INSTALL_SYSTEM_SERVICE" != "1" ]; then
    return 0
  fi
  if [ "$(id -u)" != "0" ]; then
    echo "--install-systemd-service must be run as root so it can create swarmd:swarmd and write the system unit" >&2
    return 1
  fi
  if [ ! -d /run/systemd/system ]; then
    echo "--install-systemd-service requires systemd on this Linux host" >&2
    return 1
  fi
  need_cmd systemctl
  need_cmd install
  ensure_swarmd_user
  install -d -o swarmd -g swarmd -m 0750 /var/lib/swarmd /var/cache/swarmd /var/log/swarmd /etc/swarmd
  install -d -o swarmd -g swarmd -m 0755 /run/swarmd
  root="$(install_root)"
  unit_path="/etc/systemd/system/swarmd.service"
  tmp_unit="$(mktemp)"
  write_systemd_unit "$root" >"$tmp_unit"
  install -m 0644 "$tmp_unit" "$unit_path"
  rm -f "$tmp_unit"
  systemctl daemon-reload
  systemctl enable --now swarmd.service
  printf 'installed systemd service: %s\n' "$unit_path"
}

finish_install() {
  warn_legacy_daemon_data
  install_systemd_service
  print_path_refresh_instructions
}

run_bundle_install() {
  artifact_root="$1"
  platform_dir="$(printf '%s/%s\n' "$artifact_root" "linux-amd64")"
  installer="$(printf '%s/%s\n' "$platform_dir" "root/swarmsetup")"
  log_path="$2"
  if [ "$INSTALL_SYSTEM_SERVICE" = "1" ]; then
    XDG_DATA_HOME="$(system_data_home)" XDG_BIN_HOME="$(system_bin_home)" "$installer" --artifact-root "$artifact_root" >"$log_path" 2>&1
    need_cmd chown
    need_cmd find
    need_cmd chmod
    root="$(install_root)"
    chown -R root:root "$root"
    find "$root" -type d -exec chmod 0755 {} +
    find "$root" -type f -exec chmod 0644 {} +
    chmod 0755 "$root/current/bin/swarmtui" \
      "$root/current/bin/swarmd" \
      "$root/current/bin/swarmctl" \
      "$root/current/bin/swarm-fff-search"
    chmod 0755 "$root/current/libexec/swarm" \
      "$root/current/libexec/swarmdev" \
      "$root/current/libexec/rebuild" \
      "$root/current/libexec/swarmsetup"
  else
    "$installer" --artifact-root "$artifact_root" >"$log_path" 2>&1
  fi
}

need_cmd uname
need_cmd curl
need_cmd tar
need_cmd sed
need_cmd grep
need_cmd mktemp

script_dir=""
if script_dir_candidate="$(resolve_script_dir 2>/dev/null)"; then
  script_dir="$script_dir_candidate"
fi

if [ -n "$ARTIFACT_ROOT" ]; then
  script_dir="$ARTIFACT_ROOT"
fi

platform_dir="$(printf '%s/%s\n' "$script_dir" "linux-amd64")"
bundle_installer="$(printf '%s/%s\n' "$platform_dir" "root")/swarmsetup"
bundle_index="$(printf '%s/%s\n' "$script_dir" "web")/index.html"
if [ -n "$script_dir" ] && [ -x "$bundle_installer" ] && [ -f "$bundle_index" ]; then
  version="$(read_build_info_version "$script_dir" 2>/dev/null || true)"
  print_installing "$version"
  tmp_dir="$(mktemp -d)"
  trap 'rm -rf "$tmp_dir"' EXIT INT TERM
  printf 'installing runtime... '
  if ! run_bundle_install "$script_dir" "$tmp_dir/swarmsetup.log"; then
    cat "$tmp_dir/swarmsetup.log" >&2
    exit 1
  fi
  print_ok
  printf 'linking launcher... '
  if ! verify_installed_runtime; then
    exit 1
  fi
  print_ok
  finish_install
  exit 0
fi

if [ -n "$ARTIFACT_ROOT" ]; then
  echo "invalid artifact root: $ARTIFACT_ROOT" >&2
  exit 1
fi

os_name="$(uname -s)"
arch_name="$(uname -m)"
if [ "$os_name" != "Linux" ] || { [ "$arch_name" != "x86_64" ] && [ "$arch_name" != "amd64" ]; }; then
  echo "unsupported platform: ${os_name}-${arch_name} (current installer supports Linux x86_64 only)" >&2
  exit 1
fi

release_version="$INSTALL_VERSION"
if [ -z "$release_version" ]; then
  release_version="$DEFAULT_VERSION"
fi
if [ -z "$release_version" ]; then
  release_version="$(resolve_release_version || true)"
fi
if [ -z "$release_version" ]; then
  echo "failed to resolve a GitHub release for ${REPO}" >&2
  exit 1
fi

asset_name="swarm-${release_version}-linux-amd64.tar.gz"
asset_url="https://github.com/${REPO}/releases/download/${release_version}/${asset_name}"
tmp_dir="$(mktemp -d)"
archive_path="$tmp_dir/$asset_name"
extract_dir="$tmp_dir/extract"
trap 'rm -rf "$tmp_dir"' EXIT INT TERM

print_installing "$release_version"
printf 'downloading release... '
curl -fsSL "$asset_url" -o "$archive_path"
print_ok

mkdir -p "$extract_dir"
tar -xzf "$archive_path" -C "$extract_dir"
artifact_root="$extract_dir/swarm-${release_version}-linux-amd64"
if [ ! -d "$artifact_root" ]; then
  echo "downloaded archive missing expected root $artifact_root" >&2
  exit 1
fi

printf 'installing runtime... '
if ! run_bundle_install "$artifact_root" "$tmp_dir/swarmsetup.log"; then
  cat "$tmp_dir/swarmsetup.log" >&2
  exit 1
fi
print_ok
printf 'linking launcher... '
if ! verify_installed_runtime; then
  exit 1
fi
print_ok
finish_install

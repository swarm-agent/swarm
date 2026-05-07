#!/bin/sh
set -eu

REPO="swarm-agent/swarm"
DEFAULT_VERSION=""
INSTALL_VERSION=""
ARTIFACT_ROOT=""

usage() {
  cat <<'EOF'
Usage:
  sh install.sh [--version <tag>] [--artifact-root <path>]

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

current_owner_uid() {
  if [ -n "${SUDO_UID:-}" ]; then
    printf '%s\n' "$SUDO_UID"
  else
    id -u
  fi
}

current_owner_gid() {
  if [ -n "${SUDO_GID:-}" ]; then
    printf '%s\n' "$SUDO_GID"
  else
    id -g
  fi
}

run_privileged() {
  if [ "$(id -u)" -eq 0 ]; then
    "$@"
    return $?
  fi
  if ! command -v sudo >/dev/null 2>&1; then
    echo "Swarm installs to /usr/local and stores daemon state under /etc, /var, and /run." >&2
    echo "Install sudo or pre-create the Swarm-owned system directories before running install.sh." >&2
    return 1
  fi
  sudo "$@"
}

dir_writable() {
  path="$1"
  probe="$(mktemp "$path/.swarm-write-check.XXXXXX" 2>/dev/null)" || return 1
  rm -f "$probe"
}

provision_owned_dir() {
  mode="$1"
  path="$2"
  if mkdir -p "$path" 2>/dev/null && chmod "$mode" "$path" 2>/dev/null && dir_writable "$path"; then
    return 0
  fi
  run_privileged install -d -m "$mode" -o "$(current_owner_uid)" -g "$(current_owner_gid)" "$path"
}

provision_system_dir() {
  mode="$1"
  path="$2"
  if mkdir -p "$path" 2>/dev/null && [ -d "$path" ]; then
    return 0
  fi
  run_privileged install -d -m "$mode" "$path"
}

provision_tmpfiles_config() {
  uid="$(current_owner_uid)"
  gid="$(current_owner_gid)"
  tmp_path="$(mktemp "${TMPDIR:-/tmp}/swarmd-tmpfiles.XXXXXX")"
  cat >"$tmp_path" <<EOF
d /run/swarmd 0700 ${uid} ${gid} -
d /run/swarmd/dev 0700 ${uid} ${gid} -
d /run/swarmd/ports 0700 ${uid} ${gid} -
EOF
  if ! run_privileged install -m 0644 "$tmp_path" "/etc"/tmpfiles.d/swarmd.conf; then
    rm -f "$tmp_path"
    return 1
  fi
  rm -f "$tmp_path"
}

provision_system_paths() {
  provision_system_dir 0755 /usr/local/bin
  provision_system_dir 0755 /usr/local/share
  provision_system_dir 0755 "/etc"/tmpfiles.d
  provision_system_dir 0755 "/etc"/systemd/system

  provision_owned_dir 0755 /usr/local/share/swarm/bin
  provision_owned_dir 0755 /usr/local/share/swarm/libexec
  provision_owned_dir 0755 /usr/local/share/swarm
  provision_owned_dir 0755 /usr/local/share/swarm/share
  provision_owned_dir 0755 /usr/local/share/swarm/lib

  provision_owned_dir 0700 "/etc"/swarmd
  provision_owned_dir 0700 /var/lib/swarmd
  provision_owned_dir 0700 /var/lib/swarmd/dev
  provision_owned_dir 0700 /var/cache/swarmd
  provision_owned_dir 0700 /run/swarmd
  provision_owned_dir 0700 /run/swarmd/dev
  provision_owned_dir 0700 /run/swarmd/ports
  provision_owned_dir 0755 /var/log/swarmd
  provision_owned_dir 0755 /var/log/swarmd/dev
  provision_tmpfiles_config
}

bin_home() {
  printf '%s\n' "/usr/local/bin"
}

data_home() {
  printf '%s\n' "/usr/local/share"
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

finish_install() {
  print_path_refresh_instructions
}

run_bundle_install() {
  artifact_root="$1"
  platform_dir="$(printf '%s/%s\n' "$artifact_root" "linux-amd64")"
  installer="$(printf '%s/%s\n' "$platform_dir" "root/swarmsetup")"
  log_path="$2"
  "$installer" --artifact-root "$artifact_root" >"$log_path" 2>&1
}

need_cmd uname
need_cmd curl
need_cmd tar
need_cmd sed
need_cmd grep
need_cmd mktemp
need_cmd id
need_cmd install

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
  printf 'provisioning system paths... '
  if ! provision_system_paths; then
    exit 1
  fi
  print_ok
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

printf 'provisioning system paths... '
if ! provision_system_paths; then
  exit 1
fi
print_ok
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

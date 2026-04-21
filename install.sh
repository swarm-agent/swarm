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

resolve_release_version() {
  latest_api="https://api.github.com/repos/${REPO}/releases/latest"
  releases_api="https://api.github.com/repos/${REPO}/releases"

  version="$(curl -fsSL "$latest_api" 2>/dev/null | parse_first_tag_name || true)"
  if [ -n "$version" ]; then
    printf '%s\n' "$version"
    return 0
  fi

  version="$(curl -fsSL "$releases_api" 2>/dev/null | parse_first_tag_name || true)"
  if [ -n "$version" ]; then
    printf '%s\n' "$version"
    return 0
  fi

  return 1
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

bin_home() {
  if [ -n "${XDG_BIN_HOME:-}" ]; then
    printf '%s\n' "$XDG_BIN_HOME"
  else
    printf '%s/.local/bin\n' "$HOME"
  fi
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
print_ok
finish_install

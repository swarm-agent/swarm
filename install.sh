#!/bin/sh
set -eu

REPO="swarm-agent/swarm"
DEFAULT_VERSION=""
INSTALL_VERSION=""
ARTIFACT_ROOT=""
ASSUME_YES=0
SERVICE_MODE=""
VERIFY_ONLY=0
ACCOUNT_MODE=""
ACCOUNT_NAME=""
# none, stdin (stdin is a terminal) or tty (stdin is a pipe, /dev/tty opens).
TERMINAL_MODE=""
TTY_DEVICE="/dev/tty"
SYSTEMD_RUN_DIR="/run/systemd/system"
PUBLIC_INSTALL_COMMAND="curl -fsSL https://swarmagent.dev/install-launcher"

usage() {
  cat <<'EOF'
Usage:
  sh install.sh [--yes] [--service|--no-service] [--verify-only] [--version <tag>] [--artifact-root <path>]
  curl -fsSL https://swarmagent.dev/install-launcher | sh -s -- --yes

Options:
  --yes, -y              Run without the confirmation prompt. Without a terminal and
                         without --service/--no-service it picks --service when systemd
                         is running and --no-service otherwise. Account: the invoking
                         user; under sudo, the person who ran sudo (SUDO_USER); direct
                         root without sudo, the locked service account. An existing
                         install always keeps its owner.
  --install-user NAME    Select an existing non-root OS account; never reassign an existing install.
  --create-user NAME     Root explicitly creates a human account; OS passwd requires a terminal.
  --create-service-account  Explicitly choose the locked non-login service account.
  --service, --systemd   Install, enable, and start swarm.service with systemd.
  --no-service           Install files only; do not install/start a service.
  --version <tag>        Install a specific release tag.
  --artifact-root <path> Install from an extracted release artifact.
  --verify-only          Validate an artifact root without changing the host or provisioning prerequisites.

Without a terminal (an AI agent, CI, cron), pass --yes; a non-root run then also
needs passwordless sudo, otherwise rerun it as root:
  curl -fsSL https://swarmagent.dev/install-launcher | sudo sh -s -- --yes

EOF
}

parse_args() {
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
    --install-user|--create-user)
      [ -z "$ACCOUNT_MODE" ] || { echo 'choose only one account mode' >&2; exit 2; }
      [ -n "${2:-}" ] || { echo "missing value for $1" >&2; exit 2; }
      ACCOUNT_MODE="$1"
      ACCOUNT_NAME="$2"
      shift 2
      ;;
    --create-service-account)
      [ -z "$ACCOUNT_MODE" ] || { echo 'choose only one account mode' >&2; exit 2; }
      ACCOUNT_MODE="$1"
      shift
      ;;
    --yes|-y)
      ASSUME_YES=1
      shift
      ;;
    --verify-only)
      VERIFY_ONLY=1
      SERVICE_MODE="none"
      ASSUME_YES=1
      shift
      ;;
    --service|--systemd)
      if [ -n "$SERVICE_MODE" ] && [ "$SERVICE_MODE" != "systemd" ]; then
        echo "choose only one of --service or --no-service" >&2
        exit 2
      fi
      SERVICE_MODE="systemd"
      shift
      ;;
    --no-service|--no-systemd|--files-only)
      if [ -n "$SERVICE_MODE" ] && [ "$SERVICE_MODE" != "none" ]; then
        echo "choose only one of --service or --no-service" >&2
        exit 2
      fi
      SERVICE_MODE="none"
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
}

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

  printf 'resolving latest release...\n' >&2
  metadata="$(curl -q --fail --location --silent --show-error --connect-timeout 10 --max-time 30 "$latest_api")" || return 1
  version="$(printf '%s\n' "$metadata" | parse_first_tag_name)"
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

# Ignore ambient curlrc settings: they can silently add retries, rate limits,
# or suppress output. Keep proxy/CA environment support and TLS verification.
download_release_file() (
  label="$1"
  url="$2"
  destination="$3"
  deadline="$4"
  printf 'downloading %s\n  %s\n' "$label" "$url" >&2
  printf 'Connection limit: 10s; stalled transfer: 20s below 1 KiB/s; total limit: %ss.\n' "$deadline" >&2
  if curl -q --fail --location --show-error --connect-timeout 10 \
    --speed-limit 1024 --speed-time 20 --max-time "$deadline" \
    --output "$destination" \
    --write-out '\nDownloaded %{size_download} bytes in %{time_total}s (%{speed_download} bytes/s).\n' \
    "$url"; then
    printf '%s download complete.\n' "$label" >&2
  else
    status=$?
    printf '\nFailed to download %s (curl exit %s).\nURL: %s\n' "$label" "$status" "$url" >&2
    case "$status" in
      28) echo 'Connection, stalled-transfer, or total download time limit reached.' >&2 ;;
      22) echo 'The release server returned an HTTP error; check that this release asset exists.' >&2 ;;
      5|6|7) echo 'Could not resolve or connect to the release server; check DNS, proxy, and firewall access.' >&2 ;;
      60) echo 'TLS certificate verification failed; check the system clock and trusted CA certificates.' >&2 ;;
    esac
    echo 'Installation stopped before archive extraction or Swarm path provisioning. Check the error above and network access to GitHub and its release CDN, then rerun the installer.' >&2
    return "$status"
  fi
)

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

require_safe_target() {
  kind="$1"
  path="$2"
  if [ -L "$path" ]; then
    if [ "$kind" != "directory" ] || ! is_canonical_runtime_directory_symlink "$path"; then
      echo "refusing symlink $kind target: $path" >&2
      return 1
    fi
    return 0
  fi
  if [ -e "$path" ]; then
    case "$kind" in
      directory) [ -d "$path" ] ;;
      file) [ -f "$path" ] ;;
      *) return 1 ;;
    esac || { echo "refusing non-$kind target: $path" >&2; return 1; }
  fi
}

is_canonical_runtime_directory_symlink() {
  path="$1"
  install_root="/usr/local/share/swarm"
  current_link="$install_root/current"
  versions_dir="$install_root/versions"
  leaf="${path##*/}"
  case "$leaf" in
    bin|libexec|lib|share) ;;
    *) return 1 ;;
  esac
  [ "$path" = "$install_root/$leaf" ] || return 1
  [ -d "$install_root" ] && [ ! -L "$install_root" ] || return 1
  [ -d "$versions_dir" ] && [ ! -L "$versions_dir" ] || return 1
  [ -L "$current_link" ] || return 1
  leaf_target="$(readlink "$path")" || return 1
  [ "$leaf_target" = "$current_link/$leaf" ] || return 1
  current_target="$(readlink "$current_link")" || return 1
  version="${current_target##*/}"
  [ -n "$version" ] && [ "$current_target" = "$versions_dir/$version" ] || return 1
  [ -d "$current_target" ] && [ ! -L "$current_target" ] || return 1
  [ -d "$current_target/$leaf" ] && [ ! -L "$current_target/$leaf" ] && [ -d "$path" ]
}

run_privileged() {
  if [ "$(id -u)" -eq 0 ]; then
    "$@"
    return $?
  fi
  if ! command -v sudo >/dev/null 2>&1; then
    echo "Swarm installs to /usr/local and stores daemon state under /etc, /var, and /run." >&2
    echo "Install sudo before running install.sh, or pre-create the Swarm-owned system directories." >&2
    return 1
  fi
  if [ "$TERMINAL_MODE" = "none" ]; then
    # No terminal can answer a password prompt; never wait for one.
    sudo -n "$@"
    return $?
  fi
  sudo "$@"
}

# A terminal exists only if stdin is one or /dev/tty can actually be opened.
# Permission bits on /dev/tty say nothing: without a controlling terminal the
# open fails (ENXIO) although the node is world read/write.
detect_terminal() {
  # shellcheck disable=SC2094 # opening the device both ways is the probe
  if [ -t 0 ]; then
    TERMINAL_MODE="stdin"
  elif ( : <"$TTY_DEVICE" >"$TTY_DEVICE" ) 2>/dev/null; then
    TERMINAL_MODE="tty"
  else
    TERMINAL_MODE="none"
  fi
}

# Same test as require_systemd and swarmsetup: systemctl is installed and
# systemd runs as the system manager (PID 1), i.e. /run/systemd/system exists.
systemd_is_running() {
  command -v systemctl >/dev/null 2>&1 && [ -d "$SYSTEMD_RUN_DIR" ]
}

# Root reached through sudo by a person: SUDO_USER/SUDO_UID name a non-root user.
invoked_via_sudo() {
  [ "$(id -u)" -eq 0 ] || return 1
  [ -n "${SUDO_USER:-}" ] && [ "$SUDO_USER" != "root" ] || return 1
  [ -n "${SUDO_UID:-}" ] && [ "$SUDO_UID" != "0" ] && [ -n "${SUDO_GID:-}" ]
}

print_no_terminal_rerun() {
  cat >&2 <<EOF
No terminal is available to answer the install questions, so nothing was changed.
Rerun without questions (systemd service where systemd runs, files only elsewhere):
  ${PUBLIC_INSTALL_COMMAND} | sh -s -- --yes
Or choose the mode explicitly:
  ${PUBLIC_INSTALL_COMMAND} | sh -s -- --yes --service
  ${PUBLIC_INSTALL_COMMAND} | sh -s -- --yes --no-service
EOF
}

require_noninteractive_sudo() {
  who="$(id -un 2>/dev/null || id -u)"
  if command -v sudo >/dev/null 2>&1 && sudo -n true </dev/null >/dev/null 2>&1; then
    return 0
  fi
  if command -v sudo >/dev/null 2>&1; then
    reason="'sudo -n true' failed: sudo needs a password for ${who} and there is no terminal to type it"
  else
    reason="sudo is not installed"
  fi
  cat >&2 <<EOF
Swarm installs system-wide (/usr/local, /etc, /var and the systemd unit), which needs administrator rights.
This run is not root, ${reason}.
Nothing was changed.
Ask the person to run this in their own terminal (sudo asks for their password there):
  ${PUBLIC_INSTALL_COMMAND} | sudo sh -s -- --yes
It installs Swarm for them (the sudo user), not a separate service account.
Alternatively grant ${who} passwordless sudo, or rerun from a terminal.
EOF
  exit 1
}

# Decide everything a missing terminal affects before any host change.
plan_unattended_install() {
  if [ "$TERMINAL_MODE" != "none" ]; then
    return 0
  fi
  if [ "$ASSUME_YES" -ne 1 ]; then
    print_no_terminal_rerun
    exit 2
  fi
  if [ -z "$SERVICE_MODE" ]; then
    if systemd_is_running; then
      SERVICE_MODE="systemd"
      echo 'No terminal and --yes: chose --service because systemd is running (pass --no-service for files only).'
    else
      SERVICE_MODE="none"
      echo 'No terminal and --yes: chose --no-service because systemd is not running; Swarm files are installed but not started.'
    fi
  fi
  if [ "$(id -u)" -ne 0 ]; then
    require_noninteractive_sudo
  fi
}

install_runtime_prerequisite_packages() {
  packages="$1"
  PREREQUISITE_INSTALLER_FOUND=1
  if command -v apt-get >/dev/null 2>&1; then
    run_privileged apt-get update || return 1
    # shellcheck disable=SC2086
    run_privileged env DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends $packages || return 1
  elif command -v dnf >/dev/null 2>&1; then
    # shellcheck disable=SC2086
    run_privileged dnf install -y $packages || return 1
  elif command -v yum >/dev/null 2>&1; then
    # shellcheck disable=SC2086
    run_privileged yum install -y $packages || return 1
  elif command -v pacman >/dev/null 2>&1; then
    # shellcheck disable=SC2086
    run_privileged pacman -S --noconfirm --needed $packages || return 1
  elif command -v zypper >/dev/null 2>&1; then
    # shellcheck disable=SC2086
    run_privileged zypper --non-interactive install $packages || return 1
  elif command -v apk >/dev/null 2>&1; then
    # shellcheck disable=SC2086
    run_privileged apk add --no-cache $packages || return 1
  else
    PREREQUISITE_INSTALLER_FOUND=0
    return 1
  fi
}

ensure_runtime_prerequisites() {
  missing_packages=""
  missing_commands=""
  for command_name in git bash; do
    if command -v "$command_name" >/dev/null 2>&1; then
      continue
    fi
    missing_commands="${missing_commands}${missing_commands:+, }${command_name}"
    missing_packages="${missing_packages}${missing_packages:+ }${command_name}"
  done
  if [ -z "$missing_packages" ]; then
    return 0
  fi

  if [ "$ASSUME_YES" -ne 1 ]; then
    if ! read_prompt_answer "Install missing mandatory packages (${missing_commands}) with administrator privileges before continuing? [y/N] "; then
      echo 'prerequisite installation requires explicit consent; no packages changed' >&2
      return 1
    fi
    case "$PROMPT_ANSWER" in
      y|Y|yes|YES|Yes) ;;
      *) echo 'install cancelled; no packages or Swarm runtime changed'; return 1 ;;
    esac
  fi
  echo "Installing missing mandatory Swarm runtime prerequisites: ${missing_commands}"
  package_status=0
  PREREQUISITE_INSTALLER_FOUND=0
  install_runtime_prerequisite_packages "$missing_packages" || package_status=$?
  if [ "$package_status" -ne 0 ]; then
    if [ "$PREREQUISITE_INSTALLER_FOUND" -eq 0 ]; then
      echo "Swarm requires ${missing_commands} at runtime, but no supported package manager was found." >&2
      echo "Install the missing commands, then rerun install.sh." >&2
    else
      echo "Failed to install mandatory Swarm runtime prerequisites: ${missing_commands}." >&2
      echo "Fix the package-manager error or install the missing commands, then rerun install.sh." >&2
    fi
    echo "No Swarm files, service definitions, or state paths were changed." >&2
    echo "Swarm installation has not started." >&2
    return 1
  fi

  for command_name in git bash; do
    if ! command -v "$command_name" >/dev/null 2>&1; then
      echo "Runtime prerequisite provisioning did not provide required command: $command_name" >&2
      echo "No Swarm files, service definitions, or state paths were changed." >&2
      echo "Swarm installation has not started." >&2
      return 1
    fi
  done
}

service_plan_label() {
  case "$SERVICE_MODE" in
    systemd)
      printf '%s\n' '/etc/systemd/system/swarm.service, enabled and started with systemd'
      ;;
    none)
      printf '%s\n' 'none; install runtime/files only and exit without starting Swarm'
      ;;
    *)
      printf '%s\n' 'choose at prompt: systemd service or no service/install files only'
      ;;
  esac
}

print_install_plan() {
  version="$1"
  source_label="$2"
  cat <<EOF
Swarm install plan
  Source: ${source_label}
  Version: ${version:-unknown}
  Runtime: /usr/local/share/swarm
  Launchers: /usr/local/bin/swarm, /usr/local/bin/swarmdev, /usr/local/bin/rebuild, /usr/local/bin/swarmsetup
  Daemon config: /etc/swarmd
  Daemon data: /var/lib/swarmd
  Daemon runtime: /run/swarmd
  Daemon cache/logs: /var/cache/swarmd, /var/log/swarmd
  Account: $(account_plan_label)
  Direct root installs: choose an existing user, create a human user with OS prompts,
  or explicitly use the locked non-root swarm account with home /var/lib/swarm.
  Fresh sudo/non-root installs: use the invoking user's identity and home.
  Reinstall: preserve consistent existing install ownership, regardless of caller.
  Account selection occurs before runtime provisioning. With --yes, direct root
  without sudo keeps the legacy locked-service-account default and root through
  sudo installs for the sudo user, unless --install-user, --create-user or
  --create-service-account is supplied.
  No service-account password, sudo, interactive login, or SSH changes.
  Human account creation uses useradd and passwd on the OS terminal; no password
  enters Swarm logs. Optional sudo access is a separate administrator decision.
  OS account selection does not log you into Swarm; complete Swarm setup/sign-in.
  Service: $(service_plan_label)
  Mandatory runtime prerequisites: Git and Bash; missing packages are installed with a detected supported package manager before Swarm paths are changed, and existing installations are left unchanged.

Reinstall preserves /etc/swarmd and /var/lib/swarmd by default.
EOF
}

account_plan_label() {
  if [ -n "$ACCOUNT_MODE" ]; then
    printf '%s\n' "${ACCOUNT_MODE} ${ACCOUNT_NAME}"
  elif [ "$(id -u)" -ne 0 ]; then
    printf '%s\n' "this user ($(id -un 2>/dev/null || id -u)) for a fresh install"
  elif [ "$ASSUME_YES" -ne 1 ]; then
    printf '%s\n' 'interactive choice'
  elif invoked_via_sudo; then
    printf '%s\n' "sudo user ${SUDO_USER} for a fresh install (--yes under sudo)"
  else
    printf '%s\n' 'locked service account swarm for a fresh install (--yes as direct root)'
  fi
}

# Returns non-zero when no answer can be read (no terminal, EOF, read error);
# callers treat that as "no answer", never as an empty answer.
read_prompt_answer() {
  prompt="$1"
  PROMPT_ANSWER=""
  case "$TERMINAL_MODE" in
    stdin)
      printf '%s' "$prompt"
      read -r PROMPT_ANSWER || return 1
      ;;
    tty)
      printf '%s' "$prompt" >"$TTY_DEVICE" || return 1
      read -r PROMPT_ANSWER <"$TTY_DEVICE" || return 1
      ;;
    *)
      return 1
      ;;
  esac
}

choose_service_mode() {
  if [ -n "$SERVICE_MODE" ]; then
    return 0
  fi
  if ! read_prompt_answer 'Choose install type: [1] systemd service, [2] no service/files only, [3] cancel: '; then
    echo "install requires a service choice; rerun with --service or --no-service" >&2
    return 1
  fi
  answer="$PROMPT_ANSWER"
  case "$answer" in
    1|systemd|service|s|S)
      SERVICE_MODE="systemd"
      ;;
    2|none|no-service|no|n|N|"")
      SERVICE_MODE="none"
      ;;
    3|cancel|c|C|q|Q)
      echo "install cancelled"
      return 1
      ;;
    *)
      echo "invalid install choice: $answer" >&2
      return 1
      ;;
  esac
}

confirm_install_plan() {
  choose_service_mode
  if [ "$ASSUME_YES" -eq 1 ]; then
    return 0
  fi
  if ! read_prompt_answer 'Continue with this install? [y/N] '; then
    echo "install requires confirmation; rerun with --yes for non-interactive install" >&2
    return 1
  fi
  answer="$PROMPT_ANSWER"
  case "$answer" in
    y|Y|yes|YES|Yes)
      return 0
      ;;
    *)
      echo "install cancelled"
      return 1
      ;;
  esac
}

require_systemd() {
  if ! command -v systemctl >/dev/null 2>&1; then
    echo "systemctl not found; explicit --service installation requires systemd." >&2
    echo "Install systemd/systemctl support or rerun with --no-service." >&2
    return 1
  fi
  if [ ! -d "$SYSTEMD_RUN_DIR" ]; then
    echo "systemd is not running as the system manager; cannot enable/start swarm.service." >&2
    echo "Run install.sh on a systemd host or provision /etc/systemd/system/swarm.service manually, then run: systemctl enable --now swarm.service" >&2
    return 1
  fi
}

enable_start_service() {
  require_systemd
  printf 'enabling and starting swarm.service... '
  if ! run_privileged systemctl daemon-reload; then
    echo "failed to reload systemd after installing swarm.service" >&2
    return 1
  fi
  if ! run_privileged systemctl enable --now swarm.service; then
    echo "failed to enable/start swarm.service" >&2
    echo "Remediation: inspect with 'systemctl status swarm.service' and rerun install.sh after fixing the reported systemd error." >&2
    return 1
  fi
  print_ok
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

  for rel in current/build-info.txt current/share/index.html current/lib/libfff_c.so; do
    if [ ! -f "$root/$rel" ]; then
      echo "installed runtime file is missing: $root/$rel" >&2
      return 1
    fi
  done

  if [ "$SERVICE_MODE" = "systemd" ] && [ ! -f /etc/systemd/system/swarm.service ]; then
    echo "installed systemd service unit is missing: /etc/systemd/system/swarm.service" >&2
    return 1
  fi
  if [ "$SERVICE_MODE" = "systemd" ]; then
    attempts=0
    while [ "$attempts" -lt 30 ]; do
      status_output="$($bin_dir/swarm status 2>/dev/null || true)"
      if printf '%s\n' "$status_output" | grep -Fxq 'active=active' && printf '%s\n' "$status_output" | grep -Fxq 'daemon_health=healthy'; then
        return 0
      fi
      attempts=$((attempts + 1))
      sleep 1
    done
    echo "Swarm was installed, but swarm.service did not reach daemon readiness." >&2
    printf '%s\n' "$status_output" >&2
    echo "Inspect the startup failure with: sudo systemctl status swarm.service" >&2
    return 1
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

  if [ "$SERVICE_MODE" = "none" ]; then
    if bin_home_on_path; then
      printf '\nSwarm runtime and launchers installed. No daemon service was installed or started.\n'
      print_no_service_commands
      return 0
    fi
    printf '\nSwarm runtime and launchers installed. No daemon service was installed or started.\n'
  elif bin_home_on_path; then
    printf '\nSwarm installed and daemon service started.\n'
    print_service_commands
    return 0
  else
    printf '\nSwarm installed and daemon service started.\n'
  fi
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

  if [ "$SERVICE_MODE" = "none" ]; then
    print_no_service_commands
  else
    printf '\nThen manage or attach to Swarm with:\n'
    printf '  swarm status\n'
    printf '  swarm open\n'
    printf '  swarm session\n'
    printf '  swarm stop\n'
    printf '  swarm restart\n'
    printf '  swarm uninstall\n'
    printf '\nIf PATH still fails, run it directly:\n'
    printf '  %s status\n' "$target/swarm"
  fi
}

print_no_service_commands() {
  printf '\nNo service manager was configured. To run Swarm, configure your supervisor to execute:\n'
  printf '  /usr/local/bin/swarm main server run (as the non-root install owner, never root)\n'
  printf '\nOr install/start the systemd service later with:\n'
  printf '  swarm install --service\n'
  printf '\nIf PATH still fails, run it directly:\n'
  printf '  /usr/local/bin/swarm main server run (as the non-root install owner, never root)\n'
}

print_service_commands() {
  printf '\nNext commands:\n'
  printf '  swarm status\n'
  printf '  swarm open\n'
  printf '  swarm session\n'
  printf '  swarm stop\n'
  printf '  swarm restart\n'
  printf '  swarm uninstall\n'
}

install_owner_name() {
  owner="$(stat -c %U "$(install_root)" 2>/dev/null || true)"
  case "$owner" in
    ''|UNKNOWN|root) return 1 ;;
  esac
  printf '%s\n' "$owner"
}

# Same lines as README "Connect your AI"; printed in every successful mode.
print_connect_lines() {
  printf '\nConnect your AI:\n'
  printf '  Claude Code (Swarm tools plus setup skills):\n'
  printf '    claude plugin marketplace add swarm-agent/swarm && claude plugin install swarm@swarm\n'
  printf '  Claude Code, tools only (any MCP client runs the same command):\n'
  printf '    claude mcp add swarm -- swarm mcp\n'
  printf '  Codex CLI (add to your Codex config.toml):\n'
  printf '    [mcp_servers.swarm]\n'
  printf '    command = "swarm"\n'
  printf '    args = ["mcp"]\n'
  if owner="$(install_owner_name)"; then
    printf '  swarm mcp must run as %s, the user that runs Swarm.\n' "$owner"
  else
    printf '  swarm mcp must run as the user that runs Swarm.\n'
  fi
  if [ "$SERVICE_MODE" = "none" ]; then
    printf '  Start Swarm first (no service was started); swarm mcp never starts it.\n'
  fi
  printf '  A new Swarm refuses swarm mcp until it has an owner: finish setup in swarm open, or run\n'
  printf '    swarm ctl setup identity --username NAME --name SWARM_NAME\n'
}

finish_install() {
  echo 'Installation identity is separate from Swarm authentication: open Swarm and complete account setup or sign in. Reinstall retains completed setup.'
  print_path_refresh_instructions
  print_connect_lines
}

run_bundle_install() {
  artifact_root="$1"
  platform_dir="$(printf '%s/%s\n' "$artifact_root" "linux-amd64")"
  installer="$(printf '%s/%s\n' "$platform_dir" "root/swarmsetup")"
  log_path="$2"
  set -- --artifact-root "$artifact_root"
  case "$ACCOUNT_MODE" in
    --install-user|--create-user) set -- "$@" "$ACCOUNT_MODE" "$ACCOUNT_NAME" ;;
    --create-service-account) set -- "$@" --create-service-account ;;
    '')
      if [ "$ASSUME_YES" -eq 1 ]; then
        # No account flag: swarmsetup installs a fresh install for the invoking
        # user (SUDO_UID under sudo) and keeps an existing install's owner. Only
        # direct root, with no person behind it, gets the locked service account.
        if [ "$(id -u)" -eq 0 ] && ! invoked_via_sudo; then
          set -- "$@" --create-service-account
        fi
      else
        set -- "$@" --choose-account
      fi
      ;;
  esac
  if [ "$SERVICE_MODE" = "none" ]; then
    set -- "$@" --no-service
  else
    set -- "$@" --service
  fi
  # Account selection and passwd use /dev/tty, never this diagnostic log.
  "$installer" "$@" >"$log_path" 2>&1
}

validate_artifact_root() {
  artifact_root="$1"
  platform_dir="$(printf '%s/%s\n' "$artifact_root" "linux-amd64")"
  for rel in \
    root/swarm \
    root/swarmdev \
    root/rebuild \
    root/swarmsetup \
    root/swarmtui \
    swarmd/swarmd \
    swarmd/swarmctl \
    swarmd/swarm-fff-search
  do
    if [ ! -x "$platform_dir/$rel" ]; then
      echo "artifact root is missing executable: $platform_dir/$rel" >&2
      return 1
    fi
  done
  for rel in \
    swarmd/libfff_c.so \
    ../web/index.html \
    ../build-info.txt \
    ../LICENSE \
    ../THIRD_PARTY_NOTICES.md
  do
    if [ ! -f "$platform_dir/$rel" ]; then
      echo "artifact root is missing file: $platform_dir/$rel" >&2
      return 1
    fi
  done
}

# Everything runs from main, called on the last line: under `curl | sh` the
# shell has read the whole script before acting, so a truncated download runs
# nothing and a child reading stdin cannot consume the rest of the script.
main() {
parse_args "$@"

need_cmd uname
need_cmd tar
need_cmd sed
need_cmd grep
need_cmd awk
need_cmd head
need_cmd dirname
need_cmd pwd
need_cmd readlink
need_cmd mkdir
need_cmd chmod
need_cmd sleep
need_cmd mktemp
need_cmd id
need_cmd install
need_cmd env

script_dir=""
if script_dir_candidate="$(resolve_script_dir 2>/dev/null)"; then
  script_dir="$script_dir_candidate"
fi

if [ -n "$ARTIFACT_ROOT" ]; then
  script_dir="$ARTIFACT_ROOT"
fi
if [ "$VERIFY_ONLY" -eq 1 ] && [ -z "$ARTIFACT_ROOT" ]; then
  echo "--verify-only requires --artifact-root" >&2
  exit 2
fi

detect_terminal
if [ "$VERIFY_ONLY" -ne 1 ]; then
  plan_unattended_install
  ensure_runtime_prerequisites
fi

platform_dir="$(printf '%s/%s\n' "$script_dir" "linux-amd64")"
bundle_installer="$(printf '%s/%s\n' "$platform_dir" "root")/swarmsetup"
bundle_index="$(printf '%s/%s\n' "$script_dir" "web")/index.html"
if [ -n "$script_dir" ] && [ -x "$bundle_installer" ] && [ -f "$bundle_index" ]; then
  validate_artifact_root "$script_dir"
  version="$(read_build_info_version "$script_dir" 2>/dev/null || true)"
  if [ "$VERIFY_ONLY" -eq 1 ]; then
    if [ -z "$version" ]; then
      echo "artifact root build-info.txt is missing a version" >&2
      exit 1
    fi
    echo "verified artifact root: $script_dir ($version)"
    exit 0
  fi
  print_install_plan "$version" "artifact root: $script_dir"
  confirm_install_plan
  if [ "$SERVICE_MODE" = "systemd" ]; then
    require_systemd
  fi
  print_installing "$version"
  tmp_dir="$(mktemp -d)"
  trap 'rm -rf "$tmp_dir"' EXIT INT TERM
  # swarmsetup owns account and path provisioning; do not pre-create root-owned state.
  if [ "$SERVICE_MODE" = "none" ]; then
    printf 'installing runtime and launchers... '
  else
    printf 'installing runtime, service unit, and starting service... '
  fi
  if ! run_bundle_install "$script_dir" "$tmp_dir/swarmsetup.log"; then
    cat "$tmp_dir/swarmsetup.log" >&2
    exit 1
  fi
  print_ok
  if [ "$SERVICE_MODE" = "none" ]; then
    printf 'verifying launcher and runtime... '
  else
    printf 'verifying launcher and service unit... '
  fi
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

need_cmd curl
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

print_install_plan "$release_version" "GitHub release: $asset_name"
confirm_install_plan
if [ "$SERVICE_MODE" = "systemd" ]; then
  require_systemd
fi
print_installing "$release_version"
checksum_name="${asset_name}.sha256"
checksum_url="${asset_url}.sha256"
checksum_path="$tmp_dir/$checksum_name"
need_cmd sha256sum
download_release_file 'release archive' "$asset_url" "$archive_path" 600
download_release_file 'release checksum' "$checksum_url" "$checksum_path" 30
printf 'verifying release checksum... '
checksum_line="$(awk -v name="$asset_name" '
  NF >= 2 {
    file = $NF
    sub(/^\*/, "", file)
    if (length($1) == 64 && $1 ~ /^[[:xdigit:]]+$/ && file == name) {
      print $1 "  " name
      exit
    }
  }
' "$checksum_path")"
if [ -z "$checksum_line" ]; then
  echo "checksum asset does not contain an exact entry for $asset_name" >&2
  exit 1
fi
(
  cd "$tmp_dir"
  printf '%s\n' "$checksum_line" | sha256sum -c -
)
print_ok

mkdir -p "$extract_dir"
tar -xzf "$archive_path" -C "$extract_dir"
artifact_root="$extract_dir/swarm-${release_version}-linux-amd64"
if [ ! -d "$artifact_root" ]; then
  echo "downloaded archive missing expected root $artifact_root" >&2
  exit 1
fi
validate_artifact_root "$artifact_root"

# swarmsetup owns account and path provisioning; errors are printed from its log.
if [ "$SERVICE_MODE" = "none" ]; then
  printf 'installing runtime and launchers... '
else
  printf 'installing runtime, service unit, and starting service... '
fi
if ! run_bundle_install "$artifact_root" "$tmp_dir/swarmsetup.log"; then
  cat "$tmp_dir/swarmsetup.log" >&2
  exit 1
fi
print_ok
if [ "$SERVICE_MODE" = "none" ]; then
  printf 'verifying launcher and runtime... '
else
  printf 'verifying launcher and service unit... '
fi
if ! verify_installed_runtime; then
  exit 1
fi
print_ok
finish_install
}

main "$@"

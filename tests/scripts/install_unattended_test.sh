#!/usr/bin/env bash
# shellcheck disable=SC2016 # single-quoted snippets run in their own shell
# Purpose. Requirement: an AI agent (no terminal) can install and start Swarm
# with `curl -fsSL https://raw.githubusercontent.com/swarm-agent/swarm/main/install.sh | sh -s -- --yes`, and every
# decision a missing terminal affects is made before any host change:
#   - terminal detection opens /dev/tty instead of trusting its permission bits;
#   - no terminal + --yes picks --service when systemd runs, --no-service
#     otherwise, and says so; no terminal without --yes exits 2 with the rerun line;
#   - non-root without a terminal proves `sudo -n true` before changing anything,
#     else stops and prints the sudo rerun line;
#   - root through sudo with --yes installs for the sudo user (no service-account
#     flag); direct root keeps the service account; explicit account flags win;
#   - every successful mode prints the README connect lines;
#   - a real terminal keeps the interactive prompts.
# Threats: the earlier installer read an empty answer when /dev/tty was not
# openable and silently installed files only (never starting Swarm), cancelled
# instead of explaining the rerun, failed late inside swarmsetup on a sudo
# password prompt, and gave `curl | sudo sh -s -- --yes` the locked service
# account so `swarm mcp` refused the person.
# Owner: install.sh (detect_terminal, read_prompt_answer, plan_unattended_install,
# require_noninteractive_sudo, run_bundle_install, print_connect_lines).
# Layer: the installer's own functions run unchanged (install.sh minus its final
# `main "$@"` line) in a session without a controlling terminal (setsid, stdin
# /dev/null) or on a pseudo-terminal (script), with fake id/sudo/systemctl/
# apt-get and a fake artifact whose swarmsetup only records its arguments. This
# is the narrowest layer that observes the terminal, sudo and argument decisions
# together; it never touches /usr/local, /etc, systemd or real sudo. The live
# install is proven by scripts/test-install-distro.sh inside a disposable VM/container.
set -euo pipefail

ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)"
INSTALLER="${ROOT}/install.sh"
: "${TMPDIR:?TMPDIR must be set to disposable command scratch}"
for tool in setsid script timeout; do
  command -v "$tool" >/dev/null 2>&1 || { echo "missing test tool: $tool" >&2; exit 1; }
done
SETSID="$(command -v setsid)"
SCRIPT="$(command -v script)"
TIMEOUT="$(command -v timeout)"

work="$(mktemp -d "${TMPDIR%/}/install-unattended-test.XXXXXX")"
trap 'rm -rf -- "${work:?}"' EXIT

pass=0
fail=0
check() {
  local label="$1"
  shift
  if "$@"; then
    pass=$((pass + 1))
    printf 'ok   %s\n' "$label"
  else
    fail=$((fail + 1))
    printf 'FAIL %s\n' "$label"
    if [[ -f "${case_dir:-}/out" ]]; then sed 's/^/     | /' "${case_dir}/out"; fi
  fi
}
has() { grep -Fq -- "$2" "$1"; }
lacks() { ! grep -Fq -- "$2" "$1"; }
absent() { [[ ! -e "$1" ]]; }
equals() { [[ "$(cat "$1" 2>/dev/null)" == "$2" ]]; }

# Functions only: the installer must end with exactly `main "$@"`.
[[ "$(tail -n 1 "$INSTALLER")" == 'main "$@"' ]] || { echo 'install.sh must end with: main "$@"' >&2; exit 1; }
lib="${work}/install-lib.sh"
sed '$d' "$INSTALLER" >"$lib"

artifact="${work}/artifact"
for rel in linux-amd64/root/swarm linux-amd64/root/swarmdev linux-amd64/root/rebuild \
  linux-amd64/root/swarmsetup linux-amd64/root/swarmtui linux-amd64/swarmd/swarmd \
  linux-amd64/swarmd/swarmctl linux-amd64/swarmd/swarm-fff-search linux-amd64/swarmd/libfff_c.so \
  web/index.html build-info.txt LICENSE THIRD_PARTY_NOTICES.md; do
  mkdir -p "$(dirname -- "${artifact}/${rel}")"
  printf 'fixture\n' >"${artifact}/${rel}"
  chmod 0755 "${artifact}/${rel}"
done
printf 'version=v0.0.0-test\n' >"${artifact}/build-info.txt"
cat >"${artifact}/linux-amd64/root/swarmsetup" <<'EOF'
#!/bin/sh
printf '%s\n' "$@" >"$CASE_DIR/setup.args"
printf 'SUDO_UID=%s\n' "${SUDO_UID:-}" >"$CASE_DIR/setup.env"
EOF

host_cmds=(sh cat uname tar sed grep awk head dirname pwd readlink mkdir chmod sleep mktemp install env rm ln stat tail true)

# new_case NAME [git=missing] [systemd=yes|no]
new_case() {
  local name="$1" git=present systemd=yes arg
  shift
  for arg in "$@"; do
    case "$arg" in
      git=*) git="${arg#git=}" ;;
      systemd=*) systemd="${arg#systemd=}" ;;
    esac
  done
  case_dir="${work}/${name}"
  mkdir -p "${case_dir}/bin" "${case_dir}/home" "${case_dir}/tmp" "${case_dir}/install-root"
  local c
  for c in "${host_cmds[@]}" bash; do ln -s "$(type -P "$c")" "${case_dir}/bin/${c}"; done
  if [[ "$git" == present ]]; then ln -s "$(type -P git)" "${case_dir}/bin/git"; fi
  cat >"${case_dir}/bin/id" <<'EOF'
#!/bin/sh
case "$*" in
  -u) printf '%s\n' "$FAKE_UID" ;;
  -un) printf '%s\n' "$FAKE_USER" ;;
  *) exit 1 ;;
esac
EOF
  cat >"${case_dir}/bin/sudo" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" >>"$CASE_DIR/sudo.calls"
if [ "${1:-}" = -n ]; then
  shift
  [ "${FAKE_SUDO_PASSWORDLESS:-0}" = 1 ] || { echo 'sudo: a password is required' >&2; exit 1; }
  exec "$@"
fi
echo 'sudo: a terminal is required to read the password' >&2
exit 1
EOF
  cat >"${case_dir}/bin/apt-get" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" >>"$CASE_DIR/apt.calls"
EOF
  if [[ "$systemd" == yes ]]; then
    printf '#!/bin/sh\nexit 0\n' >"${case_dir}/bin/systemctl"
    mkdir -p "${case_dir}/run-systemd"
  fi
  chmod 0755 "${case_dir}/bin/id" "${case_dir}/bin/sudo" "${case_dir}/bin/apt-get"
  if [[ "$systemd" == yes ]]; then chmod 0755 "${case_dir}/bin/systemctl"; fi
}

# The wrapper sources the unchanged functions, points the systemd probe and the
# install root at the case, and stubs only the post-install live verification.
wrapper='. "$1"; shift
SYSTEMD_RUN_DIR="$CASE_DIR/run-systemd"
install_root() { printf "%s\n" "$CASE_DIR/install-root"; }
verify_installed_runtime() { return 0; }
main "$@"'

case_env() {
  printf '%s\n' "PATH=${case_dir}/bin" "HOME=${case_dir}/home" "TMPDIR=${case_dir}/tmp" \
    "CASE_DIR=${case_dir}" "FAKE_UID=${FAKE_UID:-1000}" "FAKE_USER=${FAKE_USER:-alice}" \
    "FAKE_SUDO_PASSWORDLESS=${FAKE_SUDO_PASSWORDLESS:-0}" "SHELL=/bin/sh"
  # shellcheck disable=SC2086 # SUDO_ENV is a space-separated list of NAME=VALUE words
  if [[ -n "${SUDO_ENV:-}" ]]; then printf '%s\n' $SUDO_ENV; fi
}

# No controlling terminal: new session, stdin /dev/null (an agent's shell).
run_notty() {
  local -a env_args
  mapfile -t env_args < <(case_env)
  set +e
  "$TIMEOUT" 20 "$SETSID" -w env -i "${env_args[@]}" sh -c "$wrapper" sh "$lib" "$@" </dev/null >"${case_dir}/out" 2>&1
  echo "$?" >"${case_dir}/rc"
  set -e
}

# A real pseudo-terminal with typed answers (a person in a terminal).
run_pty() {
  local answers="$1" quoted="" arg
  shift
  local -a env_args
  mapfile -t env_args < <(case_env)
  for arg in env -i "${env_args[@]}" sh -c "$wrapper" sh "$lib" "$@"; do
    quoted+=" $(printf '%q' "$arg")"
  done
  set +e
  printf '%b' "$answers" | SHELL="$(type -P bash)" "$TIMEOUT" 20 "$SCRIPT" -qec "$quoted" /dev/null >"${case_dir}/out" 2>&1
  echo "$?" >"${case_dir}/rc"
  set -e
}

connect_lines() {
  has "$1" 'claude plugin marketplace add swarm-agent/swarm && claude plugin install swarm@swarm' &&
    has "$1" 'claude mcp add swarm -- swarm mcp' &&
    has "$1" '[mcp_servers.swarm]' && has "$1" 'args = ["mcp"]' && has "$1" 'swarm ctl setup identity'
}

echo '# environment: setsid really removes the controlling terminal'
new_case probe
"$SETSID" -w sh -c '( : </dev/tty ) 2>/dev/null && echo tty || echo none; [ -r /dev/tty ] && [ -w /dev/tty ] && echo perms-rw' </dev/null >"${case_dir}/out" 2>&1 || true
check 'no controlling terminal under setsid' has "${case_dir}/out" none
check '/dev/tty permission bits still say read/write (the old check passed)' has "${case_dir}/out" perms-rw

echo '# B1: no terminal never yields an empty answer'
new_case read-answer
"$SETSID" -w env -i PATH="${case_dir}/bin" sh -c '. "$1"; detect_terminal; printf "mode=%s\n" "$TERMINAL_MODE"; if read_prompt_answer "q? "; then echo "rc=0 answer=[$PROMPT_ANSWER]"; else echo rc=1; fi' sh "$lib" </dev/null >"${case_dir}/out" 2>&1 || true
check 'detect_terminal reports none' has "${case_dir}/out" 'mode=none'
check 'read_prompt_answer fails instead of returning an empty answer' has "${case_dir}/out" 'rc=1'

echo '# no terminal + --yes + systemd running, non-root with passwordless sudo'
new_case yes-systemd systemd=yes
FAKE_SUDO_PASSWORDLESS=1 run_notty --artifact-root "$artifact" --yes
check 'exit 0' equals "${case_dir}/rc" 0
check 'says it chose --service' has "${case_dir}/out" 'chose --service because systemd is running'
check 'swarmsetup gets --service and no account flag' equals "${case_dir}/setup.args" "--artifact-root
${artifact}
--service"
check 'sudo was checked with sudo -n true' has "${case_dir}/sudo.calls" '-n true'
check 'every sudo call is non-interactive' bash -c '! grep -v "^-n " "$1"' _ "${case_dir}/sudo.calls"
check 'connect lines printed (service mode)' connect_lines "${case_dir}/out"
check 'connect lines name the owner' has "${case_dir}/out" "swarm mcp must run as $(id -un)"

echo '# no terminal + --yes, no systemd'
new_case yes-nosystemd systemd=no
FAKE_SUDO_PASSWORDLESS=1 run_notty --artifact-root "$artifact" --yes
check 'exit 0' equals "${case_dir}/rc" 0
check 'says it chose --no-service' has "${case_dir}/out" 'chose --no-service because systemd is not running'
check 'swarmsetup gets --no-service' equals "${case_dir}/setup.args" "--artifact-root
${artifact}
--no-service"
check 'connect lines printed (files-only mode)' connect_lines "${case_dir}/out"
check 'files-only says to start Swarm first' has "${case_dir}/out" 'Start Swarm first'

echo '# no terminal + --yes --no-service on a systemd host keeps the explicit choice'
new_case yes-explicit-none systemd=yes
FAKE_SUDO_PASSWORDLESS=1 run_notty --artifact-root "$artifact" --yes --no-service
check 'exit 0' equals "${case_dir}/rc" 0
check 'explicit --no-service wins' has "${case_dir}/setup.args" '--no-service'
check 'no default announced' lacks "${case_dir}/out" 'chose --'

echo '# B2: no terminal without --yes fails fast with the rerun line'
new_case no-yes systemd=yes
FAKE_SUDO_PASSWORDLESS=1 run_notty --artifact-root "$artifact"
check 'exit 2' equals "${case_dir}/rc" 2
check 'prints the exact rerun line' has "${case_dir}/out" 'curl -fsSL https://raw.githubusercontent.com/swarm-agent/swarm/main/install.sh | sh -s -- --yes'
check 'prints the --service option' has "${case_dir}/out" 'sh -s -- --yes --service'
check 'prints the --no-service option' has "${case_dir}/out" 'sh -s -- --yes --no-service'
check 'not reported as a cancellation' lacks "${case_dir}/out" 'install cancelled'
check 'swarmsetup never ran' absent "${case_dir}/setup.args"
check 'sudo never ran' absent "${case_dir}/sudo.calls"

echo '# B3: non-root, no terminal, sudo needs a password, Git missing'
new_case sudo-password git=missing systemd=yes
run_notty --artifact-root "$artifact" --yes
check 'exit 1' equals "${case_dir}/rc" 1
check 'prints the sudo rerun line' has "${case_dir}/out" 'curl -fsSL https://raw.githubusercontent.com/swarm-agent/swarm/main/install.sh | sudo sh -s -- --yes'
check 'explains why' has "${case_dir}/out" "'sudo -n true' failed"
check 'says nothing changed' has "${case_dir}/out" 'Nothing was changed.'
check 'only sudo -n true was attempted' equals "${case_dir}/sudo.calls" '-n true'
check 'no package manager call' absent "${case_dir}/apt.calls"
check 'swarmsetup never ran' absent "${case_dir}/setup.args"

echo '# B3: passwordless sudo provisions Git non-interactively'
new_case sudo-git git=missing systemd=yes
FAKE_SUDO_PASSWORDLESS=1 run_notty --artifact-root "$artifact" --yes
check 'package manager ran through sudo -n' has "${case_dir}/sudo.calls" '-n apt-get update'
check 'every sudo call is non-interactive' bash -c '! grep -v "^-n " "$1"' _ "${case_dir}/sudo.calls"

echo '# B4: root through sudo with --yes installs for the sudo user'
new_case via-sudo systemd=yes
FAKE_UID=0 FAKE_USER=root SUDO_ENV='SUDO_USER=alice SUDO_UID=1000 SUDO_GID=1000' run_notty --artifact-root "$artifact" --yes
check 'exit 0' equals "${case_dir}/rc" 0
check 'no --create-service-account' lacks "${case_dir}/setup.args" '--create-service-account'
check 'no --choose-account' lacks "${case_dir}/setup.args" '--choose-account'
check 'swarmsetup sees SUDO_UID' equals "${case_dir}/setup.env" 'SUDO_UID=1000'
check 'plan names the sudo user' has "${case_dir}/out" 'sudo user alice for a fresh install'
check 'root never calls sudo' absent "${case_dir}/sudo.calls"

echo '# direct root (no SUDO_USER) keeps the locked service account'
new_case direct-root systemd=yes
FAKE_UID=0 FAKE_USER=root run_notty --artifact-root "$artifact" --yes
check 'exit 0' equals "${case_dir}/rc" 0
check 'passes --create-service-account' has "${case_dir}/setup.args" '--create-service-account'

echo '# sudo from root (SUDO_USER=root) is direct root'
new_case sudo-root systemd=yes
FAKE_UID=0 FAKE_USER=root SUDO_ENV='SUDO_USER=root SUDO_UID=0 SUDO_GID=0' run_notty --artifact-root "$artifact" --yes
check 'passes --create-service-account' has "${case_dir}/setup.args" '--create-service-account'

echo '# explicit account flags win under sudo'
new_case explicit-service-account systemd=yes
FAKE_UID=0 FAKE_USER=root SUDO_ENV='SUDO_USER=alice SUDO_UID=1000 SUDO_GID=1000' run_notty --artifact-root "$artifact" --yes --create-service-account
check '--create-service-account kept' has "${case_dir}/setup.args" '--create-service-account'
new_case explicit-install-user systemd=yes
FAKE_UID=0 FAKE_USER=root SUDO_ENV='SUDO_USER=alice SUDO_UID=1000 SUDO_GID=1000' run_notty --artifact-root "$artifact" --yes --install-user bob
check '--install-user bob kept' equals "${case_dir}/setup.args" "--artifact-root
${artifact}
--install-user
bob
--service"

echo '# --verify-only never needs a terminal or sudo'
new_case verify-only systemd=yes
run_notty --artifact-root "$artifact" --verify-only
check 'exit 0' equals "${case_dir}/rc" 0
check 'verified' has "${case_dir}/out" 'verified artifact root'
check 'sudo never ran' absent "${case_dir}/sudo.calls"

echo '# real terminal: interactive prompts unchanged'
new_case pty-interactive systemd=yes
run_pty '1\ny\n' --artifact-root "$artifact"
check 'exit 0' equals "${case_dir}/rc" 0
check 'service prompt shown' has "${case_dir}/out" 'Choose install type'
check 'confirmation prompt shown' has "${case_dir}/out" 'Continue with this install?'
check 'answers drive swarmsetup (--choose-account, --service)' equals "${case_dir}/setup.args" "--artifact-root
${artifact}
--choose-account
--service"
check 'no unattended default announced' lacks "${case_dir}/out" 'No terminal'
check 'no sudo preflight with a terminal' absent "${case_dir}/sudo.calls"
check 'connect lines printed (interactive)' connect_lines "${case_dir}/out"

new_case pty-yes systemd=yes
run_pty '2\n' --artifact-root "$artifact" --yes
check '--yes on a terminal still asks for the service mode' has "${case_dir}/out" 'Choose install type'
check 'answer 2 means --no-service' has "${case_dir}/setup.args" '--no-service'

new_case pty-blank systemd=yes
run_pty '\ny\n' --artifact-root "$artifact"
check 'blank answer on a real terminal stays files-only' has "${case_dir}/setup.args" '--no-service'

printf '\n%d passed, %d failed\n' "$pass" "$fail"
[[ "$fail" -eq 0 ]]

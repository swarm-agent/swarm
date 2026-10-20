#!/usr/bin/env bash
# Purpose: `install.sh claude-code` sets up the Claude Code CLI for Swarm's
# service user so the claudecode provider can run on a headless install. It
# must find a CLI that user can run (or install one with Anthropic's
# installer into that user's home), point the daemon at it through the
# root-only daemon settings file (SWARM_CLAUDE_CODE_CLI_PATH, other settings
# kept), start `claude auth login` as that user only when a person is at a
# terminal, fail clearly (not look successful) when sign-in is still missing,
# and be safe to run again: no second install, restart or sign-in once set up.
# The real claude_code_setup and daemon_env_apply run here; only the claude
# binary, the installer download (curl), runuser and systemd are stubbed. No
# network, no root.
# Run: bash install-claude-code.test.sh
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
work=$(mktemp -d "${TMPDIR:?}/claude-code.XXXXXX")
trap 'rm -rf "$work"' EXIT
fail() { echo "FAIL: $*" >&2; exit 1; }

# The installer's functions, without running it.
# shellcheck disable=SC1090
source <(sed '$d' "$here/install.sh")

STATE=$work/state CONF=$work/state/install.env DAEMON_ENV=$work/state/daemon.env
UNITS=$work/units SERVICE_HOME=$work/home SERVICE_USER=swarm
INSTALLER=/installed/install.sh
mkdir -p "$STATE" "$UNITS" "$SERVICE_HOME" "$work/bin"
printf 'SWARM_REF=dev\n' >"$CONF"
printf '[Service]\nEnvironmentFile=-%s\n' "$DAEMON_ENV" >"$UNITS/$DAEMON_UNIT"
printf 'OTHER_SETTING=keep-me\n' >"$DAEMON_ENV"

# The fake claude: version, sign-in state and login, kept in its user's HOME.
cat >"$work/fake-claude" <<'EOF'
#!/usr/bin/env bash
state=$HOME/.fake-claude
echo "HOME=$HOME $*" >>"$state.calls"
case "$*" in
  --version) cat "$state.version" 2>/dev/null || echo "2.1.300 (Claude Code)" ;;
  "auth status")
    if [[ -f $state.signed-in ]]; then echo '{"loggedIn": true, "authMethod": "claude.ai"}'
    else echo '{"loggedIn": false, "authMethod": "none"}'; exit 1; fi ;;
  "auth login") touch "$state.signed-in" ;;
  update) echo "2.1.300 (Claude Code)" >"$state.version" ;;
  *) exit 2 ;;
esac
EOF
chmod +x "$work/fake-claude"
export FAKE_CLAUDE=$work/fake-claude CURL_LOG=$work/curl.log

# Anthropic's installer download: only its URL is accepted, and the script it
# returns installs the fake claude into ~/.local/bin, as the real one does.
cat >"$work/bin/curl" <<'EOF'
#!/usr/bin/env bash
echo "$*" >>"$CURL_LOG"
[[ ${*: -1} == https://claude.ai/install.sh ]] || exit 22
printf 'mkdir -p "$HOME/.local/bin" && cp "%s" "$HOME/.local/bin/claude"\n' "$FAKE_CLAUDE"
EOF
chmod +x "$work/bin/curl"
PATH=$work/bin:$PATH

# Swarm's user exists; runuser must be asked for it, then runs the command as
# this test's user.
id() { if [[ $* == "-u $SERVICE_USER" ]]; then echo 990; else command id "$@"; fi; }
runuser() {
  [[ $1 == -u && $2 == "$SERVICE_USER" && $3 == -- ]] || fail "runuser called as: $*"
  shift 3
  "$@"
}
# Ownership needs root; everything else about the file is kept.
install() {
  local args=()
  while (($#)); do case $1 in -o | -g) shift 2 ;; *) args+=("$1"); shift ;; esac; done
  command install "${args[@]}"
}
systemctl() { :; }
restart_daemon_when_idle() { echo restart >>"$work/restarts"; }
wait_ready() { :; }
install_units() { fail "install_units ran although the unit reads the settings file"; }
terminal=no
stdin_is_terminal() { [[ $terminal == yes ]]; }

run() { rc=0; (claude_code_setup "$@") >"$work/out" 2>&1 || rc=$?; }
count() { [[ -f $1 ]] && wc -l <"$1" | tr -d ' ' || echo 0; }
cli=$SERVICE_HOME/.local/bin/claude

# 1. Fresh machine, no terminal: install for Swarm's user, point Swarm at it,
# then fail with the terminal command because no one can sign in.
run
[[ $rc != 0 ]] || { cat "$work/out" >&2; fail "succeeded without a sign-in"; }
grep -q "sudo bash $INSTALLER claude-code" "$work/out" || { cat "$work/out" >&2; fail "no terminal command for the person"; }
[[ -x $cli ]] || fail "claude was not installed into Swarm's user's ~/.local/bin"
[[ $(count "$CURL_LOG") == 1 ]] || fail "the installer was not downloaded exactly once"
grep -qx "SWARM_CLAUDE_CODE_CLI_PATH=$cli" "$DAEMON_ENV" || fail "SWARM_CLAUDE_CODE_CLI_PATH not set"
grep -qx 'OTHER_SETTING=keep-me' "$DAEMON_ENV" || fail "other daemon settings were dropped"
[[ $(stat -c %a "$DAEMON_ENV") == 600 ]] || fail "daemon settings file is not 0600"
[[ $(count "$work/restarts") == 1 ]] || fail "Swarm was not restarted once to apply the path"
[[ ! -e $SERVICE_HOME/.fake-claude.signed-in ]] || fail "signed in without a person"
! grep -q 'auth login' "$SERVICE_HOME/.fake-claude.calls" || fail "auth login ran without a terminal"
! grep -qv "^HOME=$SERVICE_HOME " "$SERVICE_HOME/.fake-claude.calls" || fail "claude ran with another HOME"

# 2. Again, in a terminal: no new install or restart; sign-in runs as Swarm's user.
terminal=yes
run
[[ $rc == 0 ]] || { cat "$work/out" >&2; fail "setup with sign-in failed ($rc)"; }
grep -q 'signed in' "$work/out" || fail "sign-in not reported"
grep -q 'swarm_connect_claude_code' "$work/out" || fail "the next step (activate) is not named"
[[ $(count "$CURL_LOG") == 1 ]] || fail "re-run downloaded the installer again"
[[ $(count "$work/restarts") == 1 ]] || fail "re-run restarted Swarm with an unchanged path"
[[ $(grep -c 'auth login' "$SERVICE_HOME/.fake-claude.calls") == 1 ]] || fail "auth login did not run once"

# 3. Again, no terminal: it reports the state and changes nothing.
terminal=no
run
[[ $rc == 0 ]] || { cat "$work/out" >&2; fail "re-run of a set-up machine failed ($rc)"; }
grep -q 'Swarm already uses it' "$work/out" || fail "re-run did not report the current path"
[[ $(grep -c 'auth login' "$SERVICE_HOME/.fake-claude.calls") == 1 ]] || fail "re-run signed in again"
[[ $(count "$work/restarts") == 1 && $(count "$CURL_LOG") == 1 ]] || fail "re-run changed the machine"

# 4. A claude Swarm's user already has on its PATH is used, not reinstalled;
# one older than Swarm needs is updated first.
rm -rf "$SERVICE_HOME" "$work/restarts" "$CURL_LOG"
mkdir -p "$SERVICE_HOME" "$work/system/bin"
printf 'OTHER_SETTING=keep-me\n' >"$DAEMON_ENV"
cp "$work/fake-claude" "$work/system/bin/claude"
echo "2.0.1 (Claude Code)" >"$SERVICE_HOME/.fake-claude.version"
touch "$SERVICE_HOME/.fake-claude.signed-in"
PATH=$work/system/bin:$PATH run
[[ $rc == 0 ]] || { cat "$work/out" >&2; fail "setup with an existing claude failed ($rc)"; }
[[ ! -e $CURL_LOG ]] || fail "installed although Swarm's user already has claude"
grep -qx "SWARM_CLAUDE_CODE_CLI_PATH=$work/system/bin/claude" "$DAEMON_ENV" || fail "the existing claude was not used"
grep -qx 'update' <(sed 's/^HOME=[^ ]* //' "$SERVICE_HOME/.fake-claude.calls") || fail "an old claude was not updated"
grep -q '2.1.300' "$work/out" || fail "the updated version is not reported"

# 5. Arguments are refused.
run extra
[[ $rc != 0 ]] || fail "accepted an unknown argument"

echo 'claude-code installer tests passed'

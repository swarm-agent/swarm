#!/usr/bin/env bash
set -u
set -o pipefail

pass_count=0
fail_count=0
warn_count=0

pass() {
  pass_count=$((pass_count + 1))
  printf '[ok] %s\n' "$1"
}

fail() {
  fail_count=$((fail_count + 1))
  printf '[fail] %s\n' "$1"
}

warn() {
  warn_count=$((warn_count + 1))
  printf '[warn] %s\n' "$1"
}

run_check() {
  local label="$1"
  shift
  local out rc
  out="$("$@" 2>&1)"
  rc=$?
  if [ "$rc" -eq 0 ]; then
    pass "$label"
  else
    fail "$label"
    if [ -n "$out" ]; then
      printf '       %s\n' "$out"
    fi
  fi
}

run_warn_check() {
  local label="$1"
  shift
  local out rc
  out="$("$@" 2>&1)"
  rc=$?
  if [ "$rc" -eq 0 ]; then
    pass "$label"
  else
    warn "$label"
    if [ -n "$out" ]; then
      printf '       %s\n' "$out"
    fi
  fi
}

show_sysctl_value() {
  local key="$1"
  if command -v sysctl >/dev/null 2>&1; then
    local line
    line="$(sysctl "$key" 2>/dev/null || true)"
    if [ -n "$line" ]; then
      printf '       %s\n' "$line"
    else
      printf '       %s = <unavailable>\n' "$key"
    fi
  else
    printf '       sysctl command not available\n'
  fi
}

probe_unshare_userns() {
  unshare -U /bin/true
}

probe_unshare_netns() {
  unshare -n /bin/true
}

probe_bwrap_pid_only() {
  bwrap --new-session --die-with-parent --unshare-pid --ro-bind / / --proc /proc --dev /dev -- /bin/sh -lc true
}

probe_bwrap_strict_net() {
  bwrap --new-session --die-with-parent --unshare-pid --unshare-net --ro-bind / / --proc /proc --dev /dev -- /bin/sh -lc true
}

printf 'Sandbox stack test (strict mode)\n'
printf 'Date: %s\n' "$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
printf '\n'

if command -v bwrap >/dev/null 2>&1; then
  pass "bubblewrap binary present ($(command -v bwrap))"
else
  fail "bubblewrap binary present"
fi

if command -v bwrap >/dev/null 2>&1; then
  if [ -u "$(command -v bwrap)" ]; then
    pass "bubblewrap binary has setuid bit"
  else
    warn "bubblewrap binary missing setuid bit"
  fi
fi

printf '[info] kernel capability knobs:\n'
show_sysctl_value kernel.unprivileged_userns_clone
show_sysctl_value user.max_user_namespaces
show_sysctl_value user.max_net_namespaces
show_sysctl_value kernel.apparmor_restrict_unprivileged_userns
show_sysctl_value kernel.apparmor_restrict_unprivileged_unconfined
printf '\n'

run_check "unshare user namespace works" probe_unshare_userns
run_check "unshare network namespace works" probe_unshare_netns
run_check "bwrap pid namespace probe works" probe_bwrap_pid_only
run_check "bwrap strict network probe works (--unshare-net)" probe_bwrap_strict_net

printf '\nSummary: %d ok, %d warn, %d fail\n' "$pass_count" "$warn_count" "$fail_count"
if [ "$fail_count" -eq 0 ]; then
  printf 'RESULT: STRICT_SANDBOX_READY=true\n'
  exit 0
fi

printf 'RESULT: STRICT_SANDBOX_READY=false\n'
printf 'Re-run this exact test with: ./scripts/sandbox-stack-test.sh\n'
exit 1

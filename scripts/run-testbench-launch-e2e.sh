#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'USAGE'
Usage: scripts/run-testbench-launch-e2e.sh [--workspace <name-or-path>] [--desktop-timeout-ms <ms>] [--tui-timeout-seconds <n>] [--with-onboarding] [--headful]

Optionally runs the existing isolated product onboarding gate, then runs the
exact container candidate setup, canonical Desktop launch suite, and
canonical TUI launch suite. Each stage is sequential and stops on its first
failure so evidence stays bounded and attributable.
USAGE
}

fail() {
  printf 'run-testbench-launch-e2e: %s\n' "$*" >&2
  exit 1
}

ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
WORKSPACE=""
DESKTOP_TIMEOUT_MS="900000"
TUI_TIMEOUT_SECONDS="180"
HEADFUL="false"
WITH_ONBOARDING="false"
while [[ $# -gt 0 ]]; do
  case "$1" in
    --workspace) [[ $# -ge 2 ]] || fail "--workspace requires a value"; WORKSPACE="$2"; shift 2 ;;
    --desktop-timeout-ms) [[ $# -ge 2 ]] || fail "--desktop-timeout-ms requires a value"; DESKTOP_TIMEOUT_MS="$2"; shift 2 ;;
    --tui-timeout-seconds) [[ $# -ge 2 ]] || fail "--tui-timeout-seconds requires a value"; TUI_TIMEOUT_SECONDS="$2"; shift 2 ;;
    --with-onboarding) WITH_ONBOARDING="true"; shift ;;
    --headful) HEADFUL="true"; shift ;;
    -h|--help) usage; exit 0 ;;
    *) fail "unknown argument: $1" ;;
  esac
done

if [[ "${WITH_ONBOARDING}" == "true" ]]; then
  printf '== Optional stage: isolated product onboarding gate ==\n'
  "${ROOT_DIR}/tests/swarmd/identity_bootstrap_e2e.sh"
  printf '\n'
fi

printf '== Testbench stage 1/3: ensure exact isolated-container candidate ==\n'
"${ROOT_DIR}/scripts/testbench-container-deploy.sh" ensure

printf '\n== Testbench stage 2/3: Desktop launch suite ==\n'
desktop_args=(--timeout-ms "${DESKTOP_TIMEOUT_MS}")
[[ -n "${WORKSPACE}" ]] && desktop_args+=(--workspace "${WORKSPACE}")
[[ "${HEADFUL}" == "true" ]] && desktop_args+=(--headful)
"${ROOT_DIR}/scripts/run-testbench-desktop-e2e.sh" "${desktop_args[@]}"

printf '\n== Testbench stage 3/3: TUI launch suite ==\n'
tui_args=(--timeout-seconds "${TUI_TIMEOUT_SECONDS}")
[[ -n "${WORKSPACE}" ]] && tui_args+=(--workspace "${WORKSPACE}")
"${ROOT_DIR}/scripts/run-testbench-tui-launch-e2e.sh" "${tui_args[@]}"

printf '\ncanonical testbench launch gate PASS: connectivity + Desktop + TUI\n'

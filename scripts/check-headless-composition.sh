#!/usr/bin/env bash
set -euo pipefail

# Requirement: the dedicated Headless executable must exclude Desktop/TUI
# composition while the existing daemon retains it. Threat: shared exported
# methods or imports accidentally pull UI back into the linked executable.
# Owners: cmd/swarm-headless, runtime.NewHeadless, api.NewCoreServer and the
# Desktop composition adapters. The compiler's dependency graph and real
# unstripped symbol tables are the narrowest proof of this build boundary;
# HTTP behavior is tested separately in core_composition_test.go.
usage() {
  echo 'Usage: bash scripts/check-headless-composition.sh HEADLESS_BINARY DESKTOP_DAEMON_BINARY'
  echo 'Pass unstripped binaries built from swarmd/cmd/swarm-headless and swarmd/cmd/swarmd.'
  echo 'Requires TMPDIR; reads binaries and source dependencies, does not start either daemon.'
}
if [[ "${1:-}" == '--help' || "${1:-}" == '-h' ]]; then
  usage
  exit 0
fi
if [[ $# != 2 ]]; then
  usage >&2
  exit 2
fi
: "${TMPDIR:?set TMPDIR to an isolated temporary directory}"
headless_binary="$1"
desktop_binary="$2"
for binary in "$headless_binary" "$desktop_binary"; do
  [[ -f "$binary" ]] || { echo "missing binary: $binary" >&2; exit 1; }
done
script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "${script_dir}/.." && pwd)"
# shellcheck source=lib-go.sh
source "${script_dir}/lib-go.sh"
swarm_require_go "$repo_root"
headless_symbols="$(mktemp "${TMPDIR}/headless-symbols.XXXXXX")"
desktop_symbols="$(mktemp "${TMPDIR}/desktop-symbols.XXXXXX")"
imports="$(mktemp "${TMPDIR}/headless-imports.XXXXXX")"
trap 'rm -f -- "$headless_symbols" "$desktop_symbols" "$imports"' EXIT
"$GO_BIN" tool nm "$headless_binary" > "$headless_symbols"
"$GO_BIN" tool nm "$desktop_binary" > "$desktop_symbols"
(cd "${repo_root}/swarmd" && "$GO_BIN" list -mod=readonly -deps ./cmd/swarm-headless) > "$imports"
module_path="$(awk '/^module / { print $2; exit }' "${repo_root}/go.mod")"
ui_imports="${module_path}"'/internal/(app|launcher|ui)(/|$)|github.com/(gdamore/tcell|rivo/tview|charmbracelet/bubbletea)(/|$)'
if grep -E "$ui_imports" "$imports"; then
  echo 'FAIL: Headless imports UI/launcher packages' >&2
  exit 1
fi
ui_symbols='internal/api\.ConfigureDesktop|internal/api\.\(\*Server\)\.(desktopHandler|withDesktopAssets|withDesktopLocalSession|issueDesktopLocalSession|handleDesktopLocalSessionBootstrap|handleDesktopStream|handleTailscaleOnboardingApproval|handleDesktopFeedback|handleUpdate(Status|Apply|Run))|internal/runtime\.New$|internal/tailscale\.NewDetector|'"${module_path}"'/internal/(app|launcher|ui)\.'
if grep -E "$ui_symbols" "$headless_symbols"; then
  echo 'FAIL: Headless binary links Desktop composition' >&2
  exit 1
fi
for symbol in 'internal/runtime.newDaemon' 'internal/api.NewCoreServer'; do
  grep -Fq "$symbol" "$headless_symbols" || { echo "FAIL: Headless missing canonical backend $symbol" >&2; exit 1; }
done
for symbol in 'internal/runtime.New' 'internal/api.ConfigureDesktop' 'internal/api.(*Server).withDesktopAssets'; do
  grep -Fq "$symbol" "$desktop_symbols" || { echo "FAIL: Desktop positive control missing $symbol" >&2; exit 1; }
done
echo 'PASS: Headless dependency graph excludes launcher/TUI imports.'
echo 'PASS: Headless executable excludes Desktop composition and retains the canonical backend.'
echo 'PASS: existing Desktop daemon retains its composition and asset handler.'

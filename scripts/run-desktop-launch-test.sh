#!/usr/bin/env bash
set -euo pipefail

# Retain the local entrypoint so existing callers receive an explicit failure,
# never a successful empty suite or a dangling browser-test filename.
if [[ $# -eq 1 && ( "$1" == "-h" || "$1" == "--help" ) ]]; then
  cat <<'USAGE'
Usage: scripts/run-desktop-launch-test.sh [legacy arguments]

Retired: the old Desktop /new and /task browser/provider journeys do not
qualify Orchestrator or + New Task. No replacement browser suite exists yet.
See web/README.md for the separate deterministic test:orchestrator entrypoint.
USAGE
  exit 0
fi
printf '%s\n' 'run-desktop-launch-test: retired legacy chat journeys; Orchestrator browser/provider replacement required (web/README.md).' >&2
exit 1

#!/usr/bin/env bash
set -euo pipefail
# Purpose: execute the critical gate's cache-resolution assignments in isolation;
# explicit overrides beat inherited warmed caches, which beat checkout defaults.
# This avoids invoking Go or creating caches merely to prove shell precedence.
root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
assignments="$(sed -n '/^CACHE_ROOT=/,/^GOPATH_DIR=/p' "${root}/scripts/run-critical-tests.sh")"
for mode in default inherited explicit; do
 (
  unset GOCACHE GOMODCACHE GOCACHE_DIR GOMODCACHE_DIR GOPATH_DIR GO_CACHE_ROOT
  ROOT_DIR=/fixture
  case "$mode" in
   inherited) GOCACHE=/warm/build; GOMODCACHE=/warm/mod ;;
   explicit) GOCACHE=/warm/build; GOMODCACHE=/warm/mod; GOCACHE_DIR=/override/build; GOMODCACHE_DIR=/override/mod ;;
  esac
  eval "$assignments"
  case "$mode" in
   default) [[ "$GOCACHE_DIR" == /fixture/.cache/go/build && "$GOMODCACHE_DIR" == /fixture/.cache/go/mod ]] ;;
   inherited) [[ "$GOCACHE_DIR" == /warm/build && "$GOMODCACHE_DIR" == /warm/mod ]] ;;
   explicit) [[ "$GOCACHE_DIR" == /override/build && "$GOMODCACHE_DIR" == /override/mod ]] ;;
  esac
  [[ "$GOPATH_DIR" == /fixture/.cache/go/path ]]
 )
done
echo 'critical cache precedence: PASS'

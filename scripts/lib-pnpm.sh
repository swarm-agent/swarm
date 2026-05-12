#!/usr/bin/env bash

swarm_pnpm() {
  if command -v pnpm > /dev/null 2>&1; then
    pnpm "$@"
    return
  fi
  if command -v corepack > /dev/null 2>&1; then
    corepack pnpm "$@"
    return
  fi
  echo "required command not found: pnpm or corepack" >&2
  return 1
}

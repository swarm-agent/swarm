#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat >&2 <<'USAGE'
Usage: scripts/copy-local-swarm-db.sh [--db-path <path>] [--copy-path <path>]

Creates a private sibling copy of the local Swarm Pebble database. By default:

  /var/lib/swarmd/swarmd.pebble
    -> /var/lib/swarmd/swarmd.pebble.copy-<UTC timestamp>-<pid>

The copy is always created in the source database's existing directory. This
script never uses TMPDIR and never deletes a copy.
USAGE
}

fail() {
  printf 'copy-local-swarm-db: %s\n' "$*" >&2
  exit 1
}

DB_PATH="/var/lib/swarmd/swarmd.pebble"
COPY_PATH=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --db-path)
      [[ $# -ge 2 ]] || fail "--db-path requires a value"
      DB_PATH="$2"
      shift 2
      ;;
    --copy-path)
      [[ $# -ge 2 ]] || fail "--copy-path requires a value"
      COPY_PATH="$2"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      fail "unknown argument: $1"
      ;;
  esac
done

[[ "${DB_PATH}" = /* ]] || fail "database path must be absolute"
[[ -d "${DB_PATH}" ]] || fail "database path is not a directory: ${DB_PATH}"

DB_PARENT="$(cd -- "$(dirname -- "${DB_PATH}")" && pwd -P)"
DB_NAME="$(basename -- "${DB_PATH}")"
DB_PATH="${DB_PARENT}/${DB_NAME}"

if [[ -z "${COPY_PATH}" ]]; then
  COPY_PATH="${DB_PATH}.copy-$(date -u +%Y%m%dT%H%M%SZ)-$$"
fi
[[ "${COPY_PATH}" = /* ]] || fail "copy path must be absolute"
[[ "$(dirname -- "${COPY_PATH}")" == "${DB_PARENT}" ]] || fail "copy must be beside the source database in ${DB_PARENT}"
[[ "${COPY_PATH}" != "${DB_PATH}" ]] || fail "copy path must differ from the source database"
[[ ! -e "${COPY_PATH}" ]] || fail "copy path already exists: ${COPY_PATH}"

umask 077
PARTIAL_PATH="${COPY_PATH}.partial"
[[ ! -e "${PARTIAL_PATH}" ]] || fail "partial copy path already exists: ${PARTIAL_PATH}"

cleanup_partial() {
  if [[ -d "${PARTIAL_PATH}" ]]; then
    rm -rf -- "${PARTIAL_PATH}"
  fi
}
trap cleanup_partial EXIT

mkdir -- "${PARTIAL_PATH}"
cp -a --reflink=auto -- "${DB_PATH}/." "${PARTIAL_PATH}/"
mv -- "${PARTIAL_PATH}" "${COPY_PATH}"
trap - EXIT

printf '%s\n' "${COPY_PATH}"

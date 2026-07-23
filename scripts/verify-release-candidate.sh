#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 2 ]]; then
  echo "usage: $0 <archive.tar.gz> <archive.tar.gz.sha256>" >&2
  exit 2
fi

ARCHIVE_PATH="$1"
CHECKSUM_PATH="$2"

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "missing required command: $1" >&2
    exit 1
  }
}

require_cmd sha256sum
require_cmd tar
require_cmd grep
require_cmd mktemp

if [[ ! -f "${ARCHIVE_PATH}" ]]; then
  echo "release archive not found: ${ARCHIVE_PATH}" >&2
  exit 1
fi
if [[ ! -f "${CHECKSUM_PATH}" ]]; then
  echo "release checksum not found: ${CHECKSUM_PATH}" >&2
  exit 1
fi

archive_dir="$(cd -- "$(dirname -- "${ARCHIVE_PATH}")" && pwd)"
archive_name="$(basename -- "${ARCHIVE_PATH}")"
checksum_dir="$(cd -- "$(dirname -- "${CHECKSUM_PATH}")" && pwd)"

if [[ "${archive_dir}" != "${checksum_dir}" ]]; then
  echo "release archive and checksum must be in the same directory" >&2
  exit 1
fi

checksum_line="$(awk -v name="${archive_name}" '
  NF >= 2 {
    file = $NF
    sub(/^\*/, "", file)
    if (length($1) == 64 && $1 ~ /^[[:xdigit:]]+$/ && file == name) {
      print $1 "  " name
      exit
    }
  }
' "${CHECKSUM_PATH}")"
if [[ -z "${checksum_line}" ]]; then
  echo "checksum file does not contain an exact entry for ${archive_name}" >&2
  exit 1
fi
(
  cd "${archive_dir}"
  printf '%s\n' "${checksum_line}" | sha256sum -c -
)

root_name="${archive_name%.tar.gz}"
required_entries=(
  "${root_name}/install.sh"
  "${root_name}/build-info.txt"
  "${root_name}/web/index.html"
  "${root_name}/linux-amd64/root/swarm"
  "${root_name}/linux-amd64/root/swarmdev"
  "${root_name}/linux-amd64/root/rebuild"
  "${root_name}/linux-amd64/root/swarmsetup"
  "${root_name}/linux-amd64/root/swarmtui"
  "${root_name}/linux-amd64/swarmd/swarmd"
  "${root_name}/linux-amd64/swarmd/swarmctl"
  "${root_name}/linux-amd64/swarmd/swarm-fff-search"
  "${root_name}/linux-amd64/swarmd/libfff_c.so"
)
archive_listing="$(tar -tzf "${ARCHIVE_PATH}")"
for entry in "${required_entries[@]}"; do
  if ! grep -Fxq "${entry}" <<<"${archive_listing}"; then
    echo "release archive is missing required entry: ${entry}" >&2
    exit 1
  fi
done
for entry in "${root_name}/install.sh" "${root_name}/linux-amd64/root/swarmsetup"; do
  if ! tar -tvzf "${ARCHIVE_PATH}" | awk -v required="${entry}" '$NF == required && substr($1, 4, 1) == "x" { found = 1 } END { exit found ? 0 : 1 }'; then
    echo "release archive service/install input is not executable: ${entry}" >&2
    exit 1
  fi
done

extract_dir="$(mktemp -d)"
trap 'rm -rf "${extract_dir}"' EXIT
tar -xzf "${ARCHIVE_PATH}" -C "${extract_dir}"
artifact_root="${extract_dir}/${root_name}"
if [[ ! -d "${artifact_root}" ]]; then
  echo "release archive is missing expected root directory: ${root_name}" >&2
  exit 1
fi
"${artifact_root}/install.sh" --artifact-root "${artifact_root}" --no-service --yes --verify-only

echo "verified release candidate: ${archive_name}"

#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"

usage() {
  cat <<'EOF'
Usage:
  ./scripts/smoke-release-archive.sh <archive.tar.gz> <archive.tar.gz.sha256> [--evidence <path>]

Verify a Linux release archive, extract it, and run its embedded swarmsetup
artifact install against disposable system and storage roots. TMPDIR must be set.
The smoke never installs a service or writes to host /etc, /var, /run, or
/usr/local paths.
EOF
}

if [[ $# -lt 2 ]]; then
  usage >&2
  exit 2
fi

ARCHIVE_PATH="$1"
CHECKSUM_PATH="$2"
shift 2
EVIDENCE_PATH=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --evidence)
      if [[ $# -lt 2 || -z "${2:-}" ]]; then
        echo "--evidence requires a path" >&2
        exit 2
      fi
      EVIDENCE_PATH="$2"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "unsupported argument: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

: "${TMPDIR:?TMPDIR must be set to a disposable command-scratch directory}"
if [[ ! -d "${TMPDIR}" || ! -w "${TMPDIR}" ]]; then
  echo "TMPDIR is not a writable directory: ${TMPDIR}" >&2
  exit 1
fi

ARCHIVE_PATH="$(cd -- "$(dirname -- "${ARCHIVE_PATH}")" && pwd)/$(basename -- "${ARCHIVE_PATH}")"
CHECKSUM_PATH="$(cd -- "$(dirname -- "${CHECKSUM_PATH}")" && pwd)/$(basename -- "${CHECKSUM_PATH}")"
if [[ -n "${EVIDENCE_PATH}" ]]; then
  mkdir -p "$(dirname -- "${EVIDENCE_PATH}")"
  EVIDENCE_PATH="$(cd -- "$(dirname -- "${EVIDENCE_PATH}")" && pwd)/$(basename -- "${EVIDENCE_PATH}")"
fi

smoke_root="$(mktemp -d "${TMPDIR%/}/swarm-release-smoke.XXXXXX")"
trap 'rm -rf "${smoke_root}"' EXIT
extract_root="${smoke_root}/extract"
system_root="${smoke_root}/system"
home_root="${smoke_root}/home"
command_tmp="${smoke_root}/tmp"
mkdir -p "${extract_root}" "${system_root}" "${home_root}" "${command_tmp}"

"${SCRIPT_DIR}/verify-release-candidate.sh" "${ARCHIVE_PATH}" "${CHECKSUM_PATH}"

tar -xzf "${ARCHIVE_PATH}" -C "${extract_root}"
archive_name="$(basename -- "${ARCHIVE_PATH}")"
artifact_root="${extract_root}/${archive_name%.tar.gz}"
installer="${artifact_root}/linux-amd64/root/swarmsetup"
service_input="${artifact_root}/install.sh"
if [[ ! -x "${installer}" ]]; then
  echo "release archive is missing executable installer: ${installer}" >&2
  exit 1
fi
if [[ ! -x "${service_input}" ]]; then
  echo "release archive is missing executable service/install input: ${service_input}" >&2
  exit 1
fi

install_root="${system_root}/usr-local-share-swarm"
system_bin="${system_root}/usr-local-bin"
state_root="${system_root}/var-lib-swarmd"
cache_root="${system_root}/var-cache-swarmd"
runtime_root="${system_root}/run-swarmd"
config_root="${system_root}/etc-swarmd"
logs_root="${system_root}/var-log-swarmd"

HOME="${home_root}" \
TMPDIR="${command_tmp}" \
PATH="${PATH}" \
SWARM_SYSTEM_INSTALL_ROOT="${install_root}" \
SWARM_SYSTEM_BIN_DIR="${system_bin}" \
SWARM_SYSTEM_BINARY_DIR="${install_root}/bin" \
SWARM_SYSTEM_LIBEXEC_DIR="${install_root}/libexec" \
SWARM_SYSTEM_LIB_DIR="${install_root}/lib" \
SWARM_SYSTEM_SHARE_DIR="${install_root}/share" \
STATE_DIRECTORY="${state_root}" \
CACHE_DIRECTORY="${cache_root}" \
RUNTIME_DIRECTORY="${runtime_root}" \
CONFIGURATION_DIRECTORY="${config_root}" \
LOGS_DIRECTORY="${logs_root}" \
SWARM_SKIP_SYSTEMD_UNIT=1 \
"${installer}" --artifact-root "${artifact_root}" --no-service

required_installed=(
  "current/build-info.txt"
  "current/share/index.html"
  "current/lib/libfff_c.so"
  "current/bin/swarmtui"
  "current/bin/swarmd"
  "current/bin/swarmctl"
  "current/bin/swarm-fff-search"
  "current/libexec/swarm"
  "current/libexec/swarmdev"
  "current/libexec/rebuild"
  "current/libexec/swarmsetup"
)
for rel in "${required_installed[@]}"; do
  if [[ ! -e "${install_root}/${rel}" ]]; then
    echo "artifact install is missing required output: ${install_root}/${rel}" >&2
    exit 1
  fi
done
for name in swarm swarmdev rebuild swarmsetup; do
  link_path="${system_bin}/${name}"
  if [[ ! -L "${link_path}" || ! -e "${link_path}" ]]; then
    echo "artifact install is missing launcher symlink: ${link_path}" >&2
    exit 1
  fi
done

version="$(sed -n 's/^version=//p' "${artifact_root}/build-info.txt" | head -n 1)"
digest="$(awk -v name="${archive_name}" '$NF == name || $NF == "*" name { print $1; exit }' "${CHECKSUM_PATH}")"
{
  printf 'release_archive=%s\n' "${archive_name}"
  printf 'release_sha256=%s\n' "${digest}"
  printf 'release_version=%s\n' "${version}"
  printf 'archive_contents=passed\n'
  printf 'artifact_root_install=passed\n'
  printf 'host_system_paths=not_used\n'
  printf 'service_mode=not_installed\n'
  printf 'systemd_clean_machine_lifecycle=external_required\n'
} | if [[ -n "${EVIDENCE_PATH}" ]]; then tee "${EVIDENCE_PATH}"; else cat; fi

echo "smoked release candidate without host system writes: ${archive_name}"

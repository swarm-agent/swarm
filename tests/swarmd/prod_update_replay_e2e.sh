#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd -- "${SCRIPT_DIR}/../.." && pwd)"

# shellcheck disable=SC1091
source "${ROOT_DIR}/scripts/lib-lane.sh"

usage() {
  cat <<'EOF'
Usage: ./tests/swarmd/prod_update_replay_e2e.sh [options]

Build a repeatable production-style update replay:
  1. install an isolated host from a stable baseline release (default v0.1.17)
  2. create a local child container via the production local-replicate harness
  3. serve a local fake GitHub releases API plus candidate archive/metadata
  4. run `swarm main update apply` inside the isolated host
  5. capture update duration and local container update-job results

This is intentionally local/offline for candidate artifacts. It avoids publishing a
real release just to measure the local-container production update path.

Options:
  --baseline-version <tag>       Stable baseline to install first. Default: v0.1.17
  --candidate-version <tag>      Candidate version to build/update to. Default: v0.1.18-test
  --runtime <podman|docker>      Container runtime for child image/container. Default: harness recommendation
  --host-root <path>             Reuse this isolated host root instead of mktemp
  --work-dir <path>              Candidate build/fake-release work dir. Default: mktemp
  --skip-candidate-build         Reuse an existing --work-dir candidate dist
  --skip-web                     Skip web build for candidate dist
  --keep-server-log              Do not remove fake release server log on success
  -h, --help                     Show this help text

Environment:
  SWARM_PROD_UPDATE_BASELINE_ARTIFACT_ROOT can point to an already-unpacked baseline dist tree.
EOF
}

log() { printf '%s\n' "$*"; }
fail() { printf 'error: %s\n' "$*" >&2; exit 1; }
require_cmd() { command -v "$1" >/dev/null 2>&1 || fail "required command not found: $1"; }
trim() {
  local value="${1-}"
  value="${value#"${value%%[![:space:]]*}"}"
  value="${value%"${value##*[![:space:]]}"}"
  printf '%s' "${value}"
}
abs_existing_dir() { (cd "$1" && pwd); }

BASELINE_VERSION="v0.1.17"
CANDIDATE_VERSION="v0.1.18-test"
RUNTIME="${RUNTIME:-}"
HOST_ROOT="${HOST_ROOT:-}"
WORK_DIR=""
SKIP_CANDIDATE_BUILD="false"
SKIP_WEB="false"
KEEP_SERVER_LOG="false"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --baseline-version)
      BASELINE_VERSION="${2:-}"
      shift 2
      ;;
    --candidate-version)
      CANDIDATE_VERSION="${2:-}"
      shift 2
      ;;
    --runtime)
      RUNTIME="${2:-}"
      shift 2
      ;;
    --host-root)
      HOST_ROOT="${2:-}"
      shift 2
      ;;
    --work-dir)
      WORK_DIR="${2:-}"
      shift 2
      ;;
    --skip-candidate-build)
      SKIP_CANDIDATE_BUILD="true"
      shift
      ;;
    --skip-web)
      SKIP_WEB="true"
      shift
      ;;
    --keep-server-log)
      KEEP_SERVER_LOG="true"
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      fail "unsupported argument: $1"
      ;;
  esac
done

[[ -n "$(trim "${BASELINE_VERSION}")" ]] || fail "--baseline-version is required"
[[ -n "$(trim "${CANDIDATE_VERSION}")" ]] || fail "--candidate-version is required"

require_cmd curl
require_cmd jq
require_cmd tar
require_cmd sha256sum
require_cmd awk
require_cmd sed
require_cmd find
require_cmd go

WORK_DIR="${WORK_DIR:-$(mktemp -d "${TMPDIR:-/tmp}/swarm-prod-update-replay-XXXXXX")}" 
mkdir -p "${WORK_DIR}"
WORK_DIR="$(abs_existing_dir "${WORK_DIR}")"
ARTIFACT_DIR="${WORK_DIR}/artifacts"
FAKE_RELEASE_DIR="${WORK_DIR}/fake-release"
mkdir -p "${ARTIFACT_DIR}" "${FAKE_RELEASE_DIR}"

write_artifact() {
  local name="${1:?name is required}"
  local content="${2-}"
  printf '%s' "${content}" >"${ARTIFACT_DIR}/${name}"
}

fetch_baseline_artifact() {
  if [[ -n "${SWARM_PROD_UPDATE_BASELINE_ARTIFACT_ROOT:-}" ]]; then
    BASELINE_ARTIFACT_ROOT="$(abs_existing_dir "${SWARM_PROD_UPDATE_BASELINE_ARTIFACT_ROOT}")"
    return 0
  fi
  local archive="${WORK_DIR}/swarm-${BASELINE_VERSION}-linux-amd64.tar.gz"
  local url="https://github.com/swarm-agent/swarm/releases/download/${BASELINE_VERSION}/swarm-${BASELINE_VERSION}-linux-amd64.tar.gz"
  local extract_dir="${WORK_DIR}/baseline-extract"
  if [[ ! -f "${archive}" ]]; then
    log "Downloading baseline ${BASELINE_VERSION}"
    curl -fL --retry 3 --connect-timeout 5 -o "${archive}" "${url}"
  fi
  rm -rf "${extract_dir}"
  mkdir -p "${extract_dir}"
  tar -xzf "${archive}" -C "${extract_dir}"
  BASELINE_ARTIFACT_ROOT="$(find "${extract_dir}" -mindepth 1 -maxdepth 1 -type d | head -n 1)"
  [[ -n "${BASELINE_ARTIFACT_ROOT}" ]] || fail "baseline archive did not contain an artifact root"
}

build_candidate_artifacts() {
  CANDIDATE_DIST="${WORK_DIR}/candidate-dist"
  if [[ "${SKIP_CANDIDATE_BUILD}" != "true" ]]; then
    local web_args=()
    if [[ "${SKIP_WEB}" == "true" ]]; then
      web_args+=(--skip-web)
    fi
    log "Building candidate dist ${CANDIDATE_VERSION} at ${CANDIDATE_DIST}"
    "${ROOT_DIR}/scripts/build-main-dist.sh" --output-dir "${CANDIDATE_DIST}" --version "${CANDIDATE_VERSION}" "${web_args[@]}"
  fi
  [[ -f "${CANDIDATE_DIST}/swarm-${CANDIDATE_VERSION}-linux-amd64.tar.gz" ]] || fail "missing candidate archive under ${CANDIDATE_DIST}"
  if [[ ! -f "${CANDIDATE_DIST}/container/container-image-info.txt" ]]; then
    local runtime_for_build="${RUNTIME:-podman}"
    log "Building candidate container metadata with ${runtime_for_build}"
    "${ROOT_DIR}/scripts/build-container-artifact.sh" \
      --dist-dir "${CANDIDATE_DIST}" \
      --output-dir "${CANDIDATE_DIST}/container" \
      --runtime "${runtime_for_build}" \
      --image-name "ghcr.io/swarm-agent/swarm:${CANDIDATE_VERSION}" \
      --base-image-name "localhost/swarm-base:ubuntu24.04-tailscale-stable-v1"
  fi
  [[ -f "${CANDIDATE_DIST}/container/container-image-info.txt" ]] || fail "missing candidate container metadata"
}

prepare_fake_release_tree() {
  local archive_name="swarm-${CANDIDATE_VERSION}-linux-amd64.tar.gz"
  local archive_path="${CANDIDATE_DIST}/${archive_name}"
  local digest
  digest="$(sha256sum "${archive_path}" | awk '{print $1}')"
  cp "${archive_path}" "${FAKE_RELEASE_DIR}/${archive_name}"
  cp "${CANDIDATE_DIST}/container/container-image-info.txt" "${FAKE_RELEASE_DIR}/container-image-info.txt"
  printf '%s  %s\n' "${digest}" "${archive_name}" >"${FAKE_RELEASE_DIR}/${archive_name}.sha256"
  cat >"${FAKE_RELEASE_DIR}/releases.json" <<EOF
[
  {
    "tag_name": "${CANDIDATE_VERSION}",
    "html_url": "http://127.0.0.1:0/${CANDIDATE_VERSION}",
    "draft": false,
    "prerelease": true,
    "published_at": "2099-01-01T00:00:00Z",
    "assets": [
      {"name": "${archive_name}", "browser_download_url": "__BASE_URL__/${archive_name}", "digest": "sha256:${digest}"},
      {"name": "${archive_name}.sha256", "browser_download_url": "__BASE_URL__/${archive_name}.sha256"}
    ]
  },
  {
    "tag_name": "${BASELINE_VERSION}",
    "html_url": "https://github.com/swarm-agent/swarm/releases/tag/${BASELINE_VERSION}",
    "draft": false,
    "prerelease": false,
    "published_at": "2000-01-01T00:00:00Z",
    "assets": []
  }
]
EOF
}

start_fake_release_server() {
  SERVER_LOG="${WORK_DIR}/fake-release-server.log"
  SERVER_PORT_FILE="${WORK_DIR}/fake-release-port.txt"
  SERVER_GO_FILE="${WORK_DIR}/fake-release-server.go"
  rm -f "${SERVER_LOG}" "${SERVER_PORT_FILE}" "${WORK_DIR}/fake-release-server.pid"
  cat >"${SERVER_GO_FILE}" <<'EOF'
package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
)

func main() {
	dir := flag.String("dir", ".", "directory to serve")
	portFile := flag.String("port-file", "", "path to write selected URL")
	flag.Parse()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatal(err)
	}
	url := "http://" + listener.Addr().String()
	if *portFile != "" {
		if err := os.WriteFile(*portFile, []byte(url+"\n"), 0o644); err != nil {
			log.Fatal(err)
		}
	}
	fmt.Println(url)
	log.Fatal(http.Serve(listener, http.FileServer(http.Dir(*dir))))
}
EOF
  go run "${SERVER_GO_FILE}" -dir "${FAKE_RELEASE_DIR}" -port-file "${SERVER_PORT_FILE}" >"${SERVER_LOG}" 2>&1 &
  echo $! >"${WORK_DIR}/fake-release-server.pid"
  for _ in $(seq 1 100); do
    if [[ -s "${SERVER_PORT_FILE}" ]]; then
      SERVER_BASE_URL="$(head -n 1 "${SERVER_PORT_FILE}")"
      break
    fi
    sleep 0.1
  done
  [[ -n "${SERVER_BASE_URL:-}" ]] || fail "fake release server did not start; see ${SERVER_LOG}"
  sed -i "s#__BASE_URL__#${SERVER_BASE_URL}#g" "${FAKE_RELEASE_DIR}/releases.json"
  log "Fake release server: ${SERVER_BASE_URL}"
}

stop_fake_release_server() {
  if [[ -f "${WORK_DIR}/fake-release-server.pid" ]]; then
    kill "$(cat "${WORK_DIR}/fake-release-server.pid")" >/dev/null 2>&1 || true
  fi
  if [[ "${KEEP_SERVER_LOG}" != "true" ]]; then
    :
  fi
}
trap stop_fake_release_server EXIT

run_baseline_replicate() {
  local args=(--host-install-artifact-root "${BASELINE_ARTIFACT_ROOT}" --skip-host-rebuild --skip-image-rebuild --poll-timeout 180)
  if [[ -n "${RUNTIME}" ]]; then
    args+=(--runtime "${RUNTIME}")
  fi
  if [[ -n "${HOST_ROOT}" ]]; then
    args+=(--host-root "${HOST_ROOT}")
  fi
  log "Creating baseline local child from ${BASELINE_VERSION}"
  "${ROOT_DIR}/tests/swarmd/local_replicate_e2e.sh" "${args[@]}" | tee "${ARTIFACT_DIR}/baseline-local-replicate.log"
  local baseline_artifacts summary_path
  baseline_artifacts="$(grep -E '^Artifacts: ' "${ARTIFACT_DIR}/baseline-local-replicate.log" | tail -n 1 | sed 's/^Artifacts: //')"
  summary_path="${baseline_artifacts}/summary.json"
  [[ -f "${summary_path}" ]] || fail "could not locate baseline summary.json at ${summary_path}"
  cp "${summary_path}" "${ARTIFACT_DIR}/baseline-summary.json"
  HOST_ROOT="$(jq -r '.host_root' "${ARTIFACT_DIR}/baseline-summary.json")"
  HOST_API_URL="$(jq -r '.host_api_url' "${ARTIFACT_DIR}/baseline-summary.json")"
  HOST_LOG_FILE="$(jq -r '.host_log_file' "${ARTIFACT_DIR}/baseline-summary.json")"
  [[ -d "${HOST_ROOT}" ]] || fail "baseline host root missing: ${HOST_ROOT}"
}

run_update_apply() {
  local releases_url_template="${SERVER_BASE_URL}/releases.json?owner=%s&repo=%s"
  local metadata_url_template="${SERVER_BASE_URL}/container-image-info.txt?version=%s"
  local update_log="${ARTIFACT_DIR}/update-apply.log"
  log "Running production update apply to ${CANDIDATE_VERSION}"
  local start_ms end_ms status
  start_ms="$(date +%s%3N)"
  set +e
  XDG_BIN_HOME="${HOST_ROOT}/xdg/bin" \
  XDG_CONFIG_HOME="${HOST_ROOT}/xdg/config" \
  XDG_DATA_HOME="${HOST_ROOT}/xdg/data" \
  XDG_STATE_HOME="${HOST_ROOT}/xdg/state" \
  XDG_CACHE_HOME="${HOST_ROOT}/xdg/cache" \
  SWARM_UPDATE_RELEASES_URL_TEMPLATE="${releases_url_template}" \
  SWARM_UPDATE_INCLUDE_UNSTABLE_RELEASES=true \
  SWARM_PRODUCTION_IMAGE_METADATA_URL_TEMPLATE="${metadata_url_template}" \
  "${HOST_ROOT}/xdg/bin/swarm" main update apply >"${update_log}" 2>&1
  status=$?
  set -e
  end_ms="$(date +%s%3N)"
  write_artifact "update-duration-ms.txt" "$((end_ms - start_ms))"
  if [[ "${status}" != "0" ]]; then
    tail -n 200 "${update_log}" >&2 || true
    fail "update apply failed with status ${status}; log: ${update_log}"
  fi
  jq -nc \
    --arg candidate_version "${CANDIDATE_VERSION}" \
    --arg baseline_version "${BASELINE_VERSION}" \
    --arg host_root "${HOST_ROOT}" \
    --arg host_api_url "${HOST_API_URL}" \
    --arg host_log_file "${HOST_LOG_FILE}" \
    --arg update_log "${update_log}" \
    --argjson duration_ms "$(cat "${ARTIFACT_DIR}/update-duration-ms.txt")" \
    '{baseline_version:$baseline_version,candidate_version:$candidate_version,host_root:$host_root,host_api_url:$host_api_url,host_log_file:$host_log_file,update_log:$update_log,duration_ms:$duration_ms}' \
    >"${ARTIFACT_DIR}/update-summary.json"
}

capture_post_update_state() {
  curl -fsS "${HOST_API_URL%/}/v1/update/run" >"${ARTIFACT_DIR}/desktop-update-job.json" || true
  curl -fsS "${HOST_API_URL%/}/v1/update/local-containers?dev_mode=false&target_version=${CANDIDATE_VERSION}" >"${ARTIFACT_DIR}/local-container-update-plan-after.json" || true
  if [[ -f "${HOST_LOG_FILE}" ]]; then
    tail -n 300 "${HOST_LOG_FILE}" >"${ARTIFACT_DIR}/host-log-tail-after-update.txt" || true
  fi
}

fetch_baseline_artifact
build_candidate_artifacts
prepare_fake_release_tree
start_fake_release_server
run_baseline_replicate
run_update_apply
capture_post_update_state

log ""
log "Production update replay summary"
jq . "${ARTIFACT_DIR}/update-summary.json"
log "Artifacts: ${ARTIFACT_DIR}"

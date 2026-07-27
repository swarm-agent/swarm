#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat >&2 <<'USAGE'
usage: verify-release-evidence.sh ARCHIVE CHECKSUM SIGSTORE_BUNDLE PROVENANCE_BUNDLE \
  --repository OWNER/REPOSITORY \
  --source-sha FULL_GIT_SHA \
  --source-ref REFS_REF \
  --workflow-ref OWNER/REPOSITORY/.github/workflows/FILE@REF \
  --workflow-sha FULL_GIT_SHA \
  --workflow-identity HTTPS_GITHUB_WORKFLOW_IDENTITY \
  --workflow-name NAME \
  --event-name EVENT
USAGE
  exit 2
}

if [[ $# -lt 4 ]]; then
  usage
fi

archive_path="$1"
checksum_path="$2"
sigstore_bundle_path="$3"
provenance_bundle_path="$4"
shift 4

repository=""
source_sha=""
source_ref=""
workflow_ref=""
workflow_sha=""
workflow_identity=""
workflow_name=""
event_name=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --repository)
      [[ $# -ge 2 ]] || usage
      repository="$2"
      shift 2
      ;;
    --source-sha)
      [[ $# -ge 2 ]] || usage
      source_sha="$2"
      shift 2
      ;;
    --source-ref)
      [[ $# -ge 2 ]] || usage
      source_ref="$2"
      shift 2
      ;;
    --workflow-ref)
      [[ $# -ge 2 ]] || usage
      workflow_ref="$2"
      shift 2
      ;;
    --workflow-sha)
      [[ $# -ge 2 ]] || usage
      workflow_sha="$2"
      shift 2
      ;;
    --workflow-identity)
      [[ $# -ge 2 ]] || usage
      workflow_identity="$2"
      shift 2
      ;;
    --workflow-name)
      [[ $# -ge 2 ]] || usage
      workflow_name="$2"
      shift 2
      ;;
    --event-name)
      [[ $# -ge 2 ]] || usage
      event_name="$2"
      shift 2
      ;;
    *)
      usage
      ;;
  esac
done

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "missing required command: $1" >&2
    exit 1
  }
}

require_cmd awk
require_cmd cosign
require_cmd gh
require_cmd sha256sum

for required_file in "${archive_path}" "${checksum_path}" "${sigstore_bundle_path}" "${provenance_bundle_path}"; do
  if [[ ! -f "${required_file}" ]]; then
    echo "required release evidence not found: ${required_file}" >&2
    exit 1
  fi
done

if [[ ! "${repository}" =~ ^[^/[:space:]]+/[^/[:space:]]+$ ]]; then
  echo "repository must be OWNER/REPOSITORY" >&2
  exit 1
fi
if [[ ! "${source_sha}" =~ ^[0-9a-f]{40}$ ]]; then
  echo "source SHA must be a full lowercase 40-character Git commit SHA" >&2
  exit 1
fi
if [[ ! "${source_ref}" =~ ^refs/(heads|tags|pull)/ ]]; then
  echo "source ref must be an explicit refs/heads, refs/tags, or refs/pull ref" >&2
  exit 1
fi
if [[ ! "${workflow_sha}" =~ ^[0-9a-f]{40}$ ]]; then
  echo "workflow SHA must be a full lowercase 40-character Git commit SHA" >&2
  exit 1
fi
if [[ -z "${workflow_name}" || -z "${event_name}" ]]; then
  echo "workflow name and event name must be non-empty" >&2
  exit 1
fi

expected_workflow="${repository}/.github/workflows/build-main.yml"
case "${workflow_ref}" in
  "${expected_workflow}@refs/heads/"*|"${expected_workflow}@refs/tags/"*|"${expected_workflow}@refs/pull/"*) ;;
  *)
    echo "workflow ref does not match repository and exact workflow path" >&2
    exit 1
    ;;
esac
expected_identity="https://github.com/${workflow_ref}"
if [[ "${workflow_identity}" != "${expected_identity}" ]]; then
  echo "workflow identity does not match the exact workflow ref" >&2
  exit 1
fi

archive_dir="$(cd -- "$(dirname -- "${archive_path}")" && pwd)"
archive_name="$(basename -- "${archive_path}")"
checksum_dir="$(cd -- "$(dirname -- "${checksum_path}")" && pwd)"
if [[ "${archive_dir}" != "${checksum_dir}" ]]; then
  echo "release archive and checksum must be in the same directory" >&2
  exit 1
fi

checksum_line="$(awk -v name="${archive_name}" '
  NF != 2 { exit 2 }
  {
    file = $2
    sub(/^\*/, "", file)
    if ($1 !~ /^[0-9a-f]{64}$/ || file != name || seen++) { exit 2 }
    line = $1 "  " name
  }
  END {
    if (NR != 1 || seen != 1) { exit 2 }
    print line
  }
' "${checksum_path}")" || {
  echo "checksum must contain exactly one lowercase SHA-256 entry for ${archive_name}" >&2
  exit 1
}
(
  cd -- "${archive_dir}"
  printf '%s\n' "${checksum_line}" | sha256sum -c --strict -
)

cosign verify-blob \
  --bundle "${sigstore_bundle_path}" \
  --certificate-identity "${workflow_identity}" \
  --certificate-oidc-issuer "https://token.actions.githubusercontent.com" \
  --certificate-github-workflow-repository "${repository}" \
  --certificate-github-workflow-ref "${source_ref}" \
  --certificate-github-workflow-sha "${source_sha}" \
  --certificate-github-workflow-name "${workflow_name}" \
  --certificate-github-workflow-trigger "${event_name}" \
  "${archive_path}"

gh attestation verify "${archive_path}" \
  --bundle "${provenance_bundle_path}" \
  --repo "${repository}" \
  --cert-identity "${workflow_identity}" \
  --cert-oidc-issuer "https://token.actions.githubusercontent.com" \
  --signer-workflow "${expected_workflow}" \
  --signer-digest "${workflow_sha}" \
  --source-digest "${source_sha}" \
  --source-ref "${source_ref}" \
  --deny-self-hosted-runners

echo "verified release evidence for ${archive_name} from ${workflow_identity} at ${source_sha}"

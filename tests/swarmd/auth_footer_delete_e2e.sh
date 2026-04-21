#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
GO_BIN="${ROOT_DIR}/.tools/go/bin/go"
IMAGE_NAME="auth-footer-delete-e2e:latest"
TEST_BIN="${ROOT_DIR}/.tmp/auth-footer-delete-e2e.test"

cleanup() {
  rm -f "${TEST_BIN}"
}
trap cleanup EXIT

mkdir -p "${ROOT_DIR}/.tmp"
cd "${ROOT_DIR}"

"${GO_BIN}" test -c -o "${TEST_BIN}" ./tests/internal/app
podman build -f deploy/container-mvp/Containerfile -t "${IMAGE_NAME}" . >/dev/null
podman run --rm \
  -v "${ROOT_DIR}:/workspaces/swarm-go:Z" \
  -w /workspaces/swarm-go \
  "${IMAGE_NAME}" \
  /workspaces/swarm-go/.tmp/auth-footer-delete-e2e.test -test.v -test.run 'Test(ChatFooterClearsModelStateWhenSetModelStateReceivesEmptyValues|AuthDeleteFlowClearsOpenChatFooterAfterReload|CredentialDeleteCleanupClearsSessionPreferencesForDeletedProvider)$'

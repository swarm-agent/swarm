#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd -- "${SCRIPT_DIR}/.." && pwd)"
cd "${ROOT_DIR}"

echo "[secret-check] scanning tracked and untracked non-ignored files for credential/key and private tailnet patterns"
hits="$(
  rg -n -I --hidden \
    --glob '!.git/**' \
    --glob '!**/node_modules/**' \
    --glob '!**/dist/**' \
    --glob '!**/.cache/**' \
    --glob '!**/.tools/**' \
    --glob '!**/.swarm/**' \
    --glob '!**/.bin/**' \
    --glob '!**/*.svg' \
    --glob '!scripts/check-secrets.sh' \
    '(-----BEGIN (RSA |EC |OPENSSH |DSA |PGP )?PRIVATE KEY-----|(^|[^A-Za-z0-9_])(AKIA[0-9A-Z]{16}|ASIA[0-9A-Z]{16}|gh[pousr]_[A-Za-z0-9]{30,}|github_pat_[A-Za-z0-9_]{20,}|glpat-[A-Za-z0-9_-]{20,}|AIza[0-9A-Za-z_-]{35}|AQ\.[A-Za-z0-9_-]{20,}|xox[baprs]-[A-Za-z0-9-]{20,}|sk-ant-[A-Za-z0-9_-]{20,}|sk-or-v1-[A-Za-z0-9_-]{20,}|sk-or-[A-Za-z0-9_-]{20,}|sk-(proj-)?[A-Za-z0-9_-]{20,}|fw_[A-Za-z0-9]{20,}|gsk_[A-Za-z0-9]{20,}|xai-[A-Za-z0-9_-]{20,}|exa_[A-Za-z0-9_-]{20,}|eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9._-]{10,}\.[A-Za-z0-9._-]{10,}))' \
    "${ROOT_DIR}" || true
)"

if [[ -n "${hits}" ]]; then
  echo "[secret-check] FAIL: possible secret material found:"
  echo "${hits}"
  exit 1
fi

private_tailnet_hits="$(
  rg -n -I --hidden \
    --glob '!.git/**' \
    --glob '!**/node_modules/**' \
    --glob '!**/dist/**' \
    --glob '!**/.cache/**' \
    --glob '!**/.tools/**' \
    --glob '!**/.swarm/**' \
    --glob '!**/.bin/**' \
    --glob '!**/*.svg' \
    --glob '!scripts/check-secrets.sh' \
    '([A-Za-z0-9-]+\.)?tail[0-9a-z]{6,}\.ts\.net' \
    "${ROOT_DIR}" || true
)"

if [[ -n "${private_tailnet_hits}" ]]; then
  echo "[secret-check] FAIL: possible private Tailscale tailnet URL found:"
  echo "${private_tailnet_hits}"
  exit 1
fi

echo "[secret-check] PASS"

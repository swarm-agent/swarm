#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd -- "${SCRIPT_DIR}/.." && pwd)"
cd "${ROOT_DIR}"

if [[ ! -f "${ROOT_DIR}/scripts/lib-go.sh" ]]; then
  echo "missing go resolver script at ${ROOT_DIR}/scripts/lib-go.sh" >&2
  exit 1
fi
# shellcheck disable=SC1091
source "${ROOT_DIR}/scripts/lib-go.sh"
swarm_require_go "${ROOT_DIR}"

mkdir -p ".bin" ".cache/gomod" ".cache/go-build" ".cache/gopath" ".cache/code-nav-home"

export PATH="${ROOT_DIR}/.bin:${PATH}"
export GOBIN="${ROOT_DIR}/.bin"
export GOMODCACHE="${ROOT_DIR}/.cache/gomod"
export GOCACHE="${ROOT_DIR}/.cache/go-build"
export GOPATH="${ROOT_DIR}/.cache/gopath"
export HOME="${ROOT_DIR}/.cache/code-nav-home"
export XDG_CACHE_HOME="${ROOT_DIR}/.cache"

packages=(
  "golang.org/x/tools/gopls@latest"
)

for pkg in "${packages[@]}"; do
  echo "[install] ${pkg}"
  "${GO_BIN}" install "${pkg}"
done

echo "[install] completed go-based toolchain in .bin"
if command -v ctags >/dev/null 2>&1; then
  echo "[install] detected ctags: $(command -v ctags)"
else
  echo "[install] ctags not found (optional): install universal-ctags via system package manager for polyglot symbol extraction."
fi

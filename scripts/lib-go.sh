#!/usr/bin/env bash
set -euo pipefail

swarm_find_go_bin() {
  local root="${1:-}"
  if [[ -z "${root}" ]]; then
    echo "swarm_find_go_bin requires a repository root" >&2
    return 1
  fi

  if [[ -n "${GO_BIN:-}" ]] && [[ -x "${GO_BIN}" ]]; then
    if [[ "$(cd -- "$(dirname -- "${GO_BIN}")/.." && pwd)" == "${root}/.tools/go" ]]; then
      printf "%s\n" "${GO_BIN}"
      return 0
    fi
  fi

  local candidate parent_root
  parent_root="$(cd -- "${root}/.." && pwd)"
  for candidate in \
    "${root}/.tools/go/bin/go" \
    "${parent_root}/.tools/go/bin/go"
  do
    if [[ -x "${candidate}" ]]; then
      printf "%s\n" "${candidate}"
      return 0
    fi
  done

  if command -v go >/dev/null 2>&1; then
    command -v go
    return 0
  fi

  return 1
}

swarm_require_go() {
  local root="${1:-}"
  local go_dir gofmt_bin goroot_dir

  if ! GO_BIN="$(swarm_find_go_bin "${root}")"; then
    echo "missing Go toolchain (go 1.25+ required)." >&2
    echo "set GO_BIN, install go in PATH, or install local go at ${root}/.tools/go/bin/go" >&2
    return 1
  fi

  goroot_dir="${GOROOT:-}"
  if [[ -z "${goroot_dir}" ]]; then
    goroot_dir="$("${GO_BIN}" env GOROOT 2>/dev/null || true)"
  fi
  if [[ -z "${goroot_dir}" ]]; then
    go_dir="$(cd -- "$(dirname -- "${GO_BIN}")" && pwd)"
    goroot_dir="$(cd -- "${go_dir}/.." && pwd)"
  else
    goroot_dir="$(cd -- "${goroot_dir}" && pwd)"
  fi

  go_dir="${goroot_dir}/bin"
  gofmt_bin="${GOFMT_BIN:-${go_dir}/gofmt}"
  if [[ ! -x "${gofmt_bin}" ]]; then
    echo "missing gofmt alongside ${GO_BIN}" >&2
    return 1
  fi

  if [[ -z "${GOROOT:-}" ]]; then
    export GOROOT="${goroot_dir}"
  fi
  if [[ -z "${GOTOOLCHAIN:-}" ]]; then
    export GOTOOLCHAIN="local"
  fi
  case ":${PATH:-}:" in
    *":${go_dir}:"*) ;;
    *) export PATH="${go_dir}:${PATH:-}" ;;
  esac

  export GO_BIN
  export GOFMT_BIN="${gofmt_bin}"
}

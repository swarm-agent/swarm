#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd -- "${SCRIPT_DIR}/.." && pwd)"
cd "${ROOT_DIR}"

if ! command -v rg >/dev/null 2>&1; then
  echo "error: ripgrep (rg) is required" >&2
  exit 1
fi

if ! command -v jq >/dev/null 2>&1; then
  echo "error: jq is required" >&2
  exit 1
fi

export PATH="${ROOT_DIR}/.bin:${PATH}"

source_globs=(
  "*.go"
  "*.py"
  "*.js"
  "*.jsx"
  "*.ts"
  "*.tsx"
  "*.java"
  "*.kt"
  "*.kts"
  "*.rb"
  "*.rs"
  "*.c"
  "*.h"
  "*.cc"
  "*.hh"
  "*.cpp"
  "*.hpp"
  "*.cxx"
  "*.cs"
  "*.php"
  "*.swift"
  "*.scala"
  "*.lua"
  "*.sh"
  "*.bash"
  "*.zsh"
)

rg_args=(--files)
for pattern in "${source_globs[@]}"; do
  rg_args+=(-g "${pattern}")
done

largest_file=""
largest_size=0

while IFS= read -r path; do
  if [[ ! -f "${path}" ]]; then
    continue
  fi
  size="$(wc -c < "${path}")"
  if (( size > largest_size )); then
    largest_size="${size}"
    largest_file="${path}"
  fi
done < <(rg "${rg_args[@]}")

if [[ -z "${largest_file}" ]]; then
  echo "error: no source files found for configured globs" >&2
  exit 1
fi

base_name="$(basename -- "${largest_file}")"
ext="${base_name##*.}"
if [[ "${ext}" == "${base_name}" ]]; then
  ext=""
fi
ext="$(printf '%s' "${ext}" | tr '[:upper:]' '[:lower:]')"

symbol_engine="${CODE_NAV_SYMBOL_ENGINE:-}"
if [[ -z "${symbol_engine}" && -f ".cache/code-nav-fastest.env" ]]; then
  # shellcheck disable=SC1091
  . ".cache/code-nav-fastest.env"
  symbol_engine="${CODE_NAV_SYMBOL_ENGINE:-}"
fi

if [[ -z "${symbol_engine}" ]]; then
  if [[ "${ext}" == "go" ]] && command -v gopls >/dev/null 2>&1; then
    symbol_engine="gopls"
  elif command -v ctags >/dev/null 2>&1; then
    symbol_engine="ctags"
  fi
fi

if [[ -z "${symbol_engine}" ]]; then
  echo "error: no non-regex symbol engine available (need gopls for Go files, or ctags for polyglot files)" >&2
  exit 1
fi

echo "largest_source_file=${largest_file}"
echo "size_bytes=${largest_size}"
echo "symbol_engine=${symbol_engine}"
echo "symbols(line<TAB>name):"

if [[ "${symbol_engine}" == "gopls" ]]; then
  if [[ "${ext}" != "go" ]]; then
    echo "error: gopls engine supports only .go files (got .${ext})" >&2
    exit 1
  fi
  mkdir -p ".cache/code-nav-home" ".cache/go-build" ".cache/gomod" ".cache/goimports"
  XDG_CACHE_HOME="${ROOT_DIR}/.cache" \
  HOME="${ROOT_DIR}/.cache/code-nav-home" \
  GOCACHE="${ROOT_DIR}/.cache/go-build" \
  GOMODCACHE="${ROOT_DIR}/.cache/gomod" \
  GOPATH="${ROOT_DIR}/.cache/gopath" \
  gopls symbols "${largest_file}" \
    | awk '
      BEGIN { OFS="\t" }
      /^[^[:space:]]/ {
        name = $1
        kind = $2
        rng = $3
        if (kind == "Function" || kind == "Method") {
          split(rng, parts, ":")
          print parts[1], name
        }
      }
    ' \
    | awk '!seen[$0]++'
  exit 0
fi

if [[ "${symbol_engine}" == "ctags" ]]; then
  if ! command -v ctags >/dev/null 2>&1; then
    echo "error: symbol_engine=ctags but ctags is not installed" >&2
    exit 1
  fi
  ctags --output-format=json --fields=+n --sort=no -f - "${largest_file}" \
    | jq -r '
        select(._type == "tag")
        | select(.line != null and .name != null)
        | select(.kind == "function" or .kind == "method" or .kind == "procedure")
        | "\(.line)\t\(.name)"
      ' \
    | awk '!seen[$0]++'
  exit 0
fi

echo "error: unsupported symbol engine '${symbol_engine}'" >&2
exit 1

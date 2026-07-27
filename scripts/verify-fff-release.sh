#!/usr/bin/env bash
set -euo pipefail

if [ $# -eq 3 ] && [ "$1" = "--require-tag" ]; then
  require_tag_only=1
  manifest="$2"
  tag="$3"
elif [ $# -eq 4 ]; then
  require_tag_only=0
  manifest="$1"
  tag="$2"
  header_file="$3"
  library_file="$4"
else
  echo "usage: $0 [--require-tag] <manifest> <tag> [<header-file> <library-file>]" >&2
  exit 1
fi

for tool in awk sha256sum; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "missing required tool: ${tool}" >&2
    exit 1
  fi
done

manifest_line="$(awk -v tag="$tag" '$1 == tag { if (found) exit 2; print; found=1 } END { if (!found) exit 1 }' "$manifest")" || {
  echo "FFF release tag ${tag} is not uniquely pinned in ${manifest}" >&2
  exit 1
}
read -r manifest_tag header_sha256 library_sha256 extra <<EOF
${manifest_line}
EOF
if [ "$manifest_tag" != "$tag" ] || [ -n "${extra:-}" ]; then
  echo "invalid manifest entry for FFF release tag ${tag}" >&2
  exit 1
fi
is_sha256() {
  [ "${#1}" -eq 64 ] && case "$1" in *[!0-9a-f]*) return 1 ;; *) return 0 ;; esac
}
if ! is_sha256 "$header_sha256" || ! is_sha256 "$library_sha256"; then
  echo "invalid SHA-256 digest in manifest entry for ${tag}" >&2
  exit 1
fi
if [ "$require_tag_only" -eq 1 ]; then
  printf 'FFF release %s is pinned in reviewed manifest\n' "$tag"
  exit 0
fi

actual_header="$(sha256sum "$header_file" | awk '{print $1}')"
actual_library="$(sha256sum "$library_file" | awk '{print $1}')"
if [ "$actual_header" != "$header_sha256" ]; then
  echo "FFF header digest mismatch for ${tag}" >&2
  exit 1
fi
if [ "$actual_library" != "$library_sha256" ]; then
  echo "FFF library digest mismatch for ${tag}" >&2
  exit 1
fi
printf 'verified FFF release %s header and library against reviewed manifest\n' "$tag"

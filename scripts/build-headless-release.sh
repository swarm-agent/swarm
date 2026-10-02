#!/usr/bin/env bash
# Producer only: building is not qualification. The protected controller owns receipts.
set -euo pipefail
[[ $# == 6 ]] || { echo 'usage: bash scripts/build-headless-release.sh VERSION SOURCE_SHA BUILT_AT BUILD_IMAGE@sha256:DIGEST RUNTIME_IMAGE@sha256:DIGEST OUTPUT_DIR' >&2; exit 2; }
version=$1 source=$2 built_at=$3 build_image=$4 runtime_image=$5 output=$6
[[ "$version" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]
[[ "$source" =~ ^[0-9a-f]{40}$ && "$(git rev-parse HEAD)" == "$source" ]]
[[ -z "$(git status --porcelain --untracked-files=all)" ]]
[[ "$build_image" =~ ^docker.io/library/golang:1.26.7-trixie@sha256:[0-9a-f]{64}$ ]]
[[ "$runtime_image" =~ ^docker.io/library/debian:trixie-slim@sha256:[0-9a-f]{64}$ ]]
mkdir -p "$output"
docker buildx build --platform linux/amd64 --provenance=false --sbom=false \
  --build-arg "BUILD_IMAGE=$build_image" --build-arg "RUNTIME_IMAGE=$runtime_image" \
  --build-arg "VERSION=$version" --build-arg "COMMIT=$source" --build-arg "BUILT_AT=$built_at" \
  --output "type=oci,dest=$output/swarm-headless.oci.tar" .
python3 -B scripts/headless-release.py inspect --archive "$output/swarm-headless.oci.tar" \
  --version "$version" --source "$source" > "$output/headless-image.json"

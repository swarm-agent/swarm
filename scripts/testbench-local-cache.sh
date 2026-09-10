#!/usr/bin/env bash
# Download only dependency manifests into base caches; never run candidate scripts.
set -euo pipefail
[[ ${1:-} != --help ]] || { echo 'Usage: bash scripts/testbench-local-cache.sh IMAGE WORKTREE'; exit 0; }
[[ $# == 2 && $EUID == 0 ]] || exit 2
image=$1; source=$2
[[ $image == /* && $source == /* && $(realpath -e "$image") == "$image" && $(realpath -e "$source") == "$source" ]] || exit 2
: "${TMPDIR:?}"
scratch=$(mktemp -d "$TMPDIR/swarm-local-cache.XXXXXXXX")
cleanup() { rm -f -- "$scratch/go.mod" "$scratch/go.sum" "$scratch/pnpm-lock.yaml" "$scratch/root.mod" "$scratch/root.sum" "$scratch/package.json" "$scratch/pnpm-workspace.yaml"; rmdir -- "$scratch"; }
trap cleanup EXIT
# Only explicit public dependency manifests cross this provisioning boundary.
install -m 0644 "$source/go.mod" "$scratch/root.mod"
install -m 0644 "$source/go.sum" "$scratch/root.sum"
install -m 0644 "$source/swarmd/go.mod" "$scratch/go.mod"
install -m 0644 "$source/swarmd/go.sum" "$scratch/go.sum"
install -m 0644 "$source/web/package.json" "$scratch/package.json"
install -m 0644 "$source/web/pnpm-workspace.yaml" "$scratch/pnpm-workspace.yaml"
install -m 0644 "$source/web/pnpm-lock.yaml" "$scratch/pnpm-lock.yaml"
timeout --signal=TERM --kill-after=10s 600s systemd-nspawn --quiet --settings=no --register=no \
  --image="$image" --timezone=off --link-journal=no --as-pid2 \
  --bind-ro="$scratch:/manifests" /bin/bash -ec '
    export GOROOT=/opt/go GOTOOLCHAIN=local GOMAXPROCS=2 HOME=/root
    mkdir -p /cache-manifests/go /cache-manifests/web
    cp /manifests/root.mod /cache-manifests/go.mod
    cp /manifests/root.sum /cache-manifests/go.sum
    cp /manifests/go.mod /manifests/go.sum /cache-manifests/go/
    cp /manifests/pnpm-lock.yaml /manifests/package.json /manifests/pnpm-workspace.yaml /cache-manifests/web/
    cd /cache-manifests
    go mod download
    cd /cache-manifests/go
    go mod download
    cd /cache-manifests/web
    pnpm install --frozen-lockfile --ignore-scripts --network-concurrency=4 --reporter=append-only
    printf "offline-caches-ready\n"
  '
sha256sum -- "$image"

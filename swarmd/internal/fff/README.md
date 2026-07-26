# FFF Go bindings

This repo vendors the upstream FFF C header and shared library so Swarm can use FFF without requiring Rust at runtime.

Canonical locations:
- `internal/fff/`
- `swarmd/internal/fff/`

Contents:
- `include/fff.h` — upstream generated C header from `dmtrKovalenko/fff`
- `lib/linux-amd64-gnu/libfff_c.so` — vendored Linux x86_64 glibc C library release asset
- `fff.go` — Go cgo wrapper used by Swarm and by `cmd/fffprobe`

## Current scope

- Vendored runtime target in this repo: Linux amd64 glibc (`c-lib-x86_64-unknown-linux-gnu.so` upstream asset)
- Wrapper exposes:
  - create/destroy/wait for scan
  - file search and glob-only file search
  - grep
  - multi-grep
  - scan progress / rescan / restart index
  - base path, scan state, watcher readiness wait
  - git refresh
  - query tracking / historical query lookup
  - health check

## Update procedure

Use the checked-in helper:

```bash
./scripts/update-fff.sh <reviewed-release-tag>
```

Updates are explicit review events. First inspect the immutable tagged source and release
asset through an independent trusted channel, calculate SHA-256 for both imported files,
and add the tag and digests to `scripts/fff-release-manifest.txt`. The helper then:
1. Rejects omitted, malformed, duplicate, or unreviewed tags.
2. Downloads the tagged `crates/fff-c/include/fff.h` and release asset
   `c-lib-x86_64-unknown-linux-gnu.so`.
3. Verifies both files against the checked-in reviewed manifest. It does not trust a
   checksum downloaded from the same mutable release channel.
4. Replaces both vendored copies under `internal/fff/` and `swarmd/internal/fff/` only
   after all verification succeeds.
5. Warns if the two Go wrappers diverged.

## Manual verification after update

We intentionally do this with manual checks first.

### 1. Smoke test the Swarm daemon wrapper

```bash
cd swarmd
GO111MODULE=on go run ./cmd/fffprobe /path/to/repo search runtime
GO111MODULE=on go run ./cmd/fffprobe /path/to/repo grep 'executeSearch'
```

### 2. Optionally smoke test the root-module wrapper

```bash
cd /path/to/swarm-go
GO111MODULE=on go run ./cmd/fffprobe /path/to/repo search runtime
```

### 3. Confirm exported symbols if needed

```bash
nm -D swarmd/internal/fff/lib/linux-amd64-gnu/libfff_c.so | awk '/ T fff_/ {print $3}' | sort
```

### 4. Review packaging references

The daemon-side library is packaged by `scripts/build-main-dist.sh` and installed by the launcher/runtime artifact flow.

## Notes

- The header may already match upstream while the shared library is older. Check both.
- Prefer using upstream release assets for reproducibility.
- If upstream adds new C API functions we want, update `fff.go` in both vendored directories together.
- Keep `internal/fff/fff.go` and `swarmd/internal/fff/fff.go` in sync unless there is a deliberate reason not to.
- If tests/validation were not requested, do not run them automatically.

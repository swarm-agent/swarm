# FFF Go bindings

This repo vendors the upstream FFF C header and shared library so Swarm can use FFF without requiring Rust at runtime.

Canonical locations:
- `internal/fff/`
- `swarmd/internal/fff/`

Contents:
- `include/fff.h` — upstream generated C header from `dmtrKovalenko/fff.nvim`
- `lib/linux-amd64-gnu/libfff_c.so` — vendored Linux x86_64 glibc C library release asset
- `fff.go` — Go cgo wrapper used by Swarm and by `cmd/fffprobe`

## Current scope

- Vendored runtime target in this repo: Linux amd64 glibc (`c-lib-x86_64-unknown-linux-gnu.so` upstream asset)
- Wrapper exposes:
  - create/destroy/wait for scan
  - file search
  - grep
  - multi-grep
  - scan progress / rescan / restart index
  - git refresh
  - query tracking / historical query lookup
  - health check

## Update procedure

Use the checked-in helper:

```bash
./scripts/update-fff.sh            # latest upstream release
./scripts/update-fff.sh v0.5.2     # pin a specific release tag
```

What it does:
1. Resolves the requested or latest GitHub release tag from `dmtrKovalenko/fff.nvim`
2. Downloads the raw upstream header from `crates/fff-c/include/fff.h`
3. Downloads the release asset `c-lib-x86_64-unknown-linux-gnu.so`
4. Verifies the upstream `.sha256`
5. Replaces both vendored copies under:
   - `internal/fff/`
   - `swarmd/internal/fff/`
6. Prints resulting hashes and warns if the two Go wrappers diverged

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

Current repo-specific place that explicitly packages the daemon-side library:
- `scripts/rebuild-container-remote.sh`

## Notes

- The header may already match upstream while the shared library is older. Check both.
- Prefer using upstream release assets for reproducibility.
- If upstream adds new C API functions we want, update `fff.go` in both vendored directories together.
- Keep `internal/fff/fff.go` and `swarmd/internal/fff/fff.go` in sync unless there is a deliberate reason not to.
- If tests/validation were not requested, do not run them automatically.

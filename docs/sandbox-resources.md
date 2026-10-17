# Sandbox project caches

The sandbox manager mounts one Docker named volume per registered project at
`/opt/swarm-cache`, shared by its jobs, reviewers, and custom-environment
containers. Different project roots have different volumes. No daemon storage
or credentials are mounted. Removing a job does not remove the project cache.

`GOCACHE` and `GOMODCACHE` use `go-build` and `go-mod`; `PNPM_HOME` uses
`pnpm-home`, and `npm_config_store_dir` selects `pnpm`. This shares downloaded
packages, not writable `node_modules` trees. Go's build cache and pnpm's
content-addressable store support concurrent consumers. Only first-use seeding
is locked; jobs are never serialized on a cache lock.

An empty volume is seeded from the configured default image's `/opt/go/cache`
and declared `GOMODCACHE`, plus the warm store resolved by `pnpm store path`
(preserving its version directory), even when the job uses a custom environment. Existing
volumes are not reseeded when images change. The default image must include
`flock`, `find`, `cp`, `chown`, and `du`. Initialization failure rejects container
creation rather than silently falling back to a private cache.

Sandbox `GOFLAGS` is `-buildvcs=false -trimpath`. Identical code across worktrees
can reuse compiled packages and eligible test results. Go still invalidates
results for changed inputs, environment, flags, and tests that inspect paths or
files; this is not a promise that every test is cacheable across worktrees.

The durable account-scoped box limits record accepts `project_cache_bytes` via
`swarm_set_limits` and reports it via `swarm_get_limits`; zero selects 10 GiB
per project. Trimming and usage reporting read the current record each time;
read failures are surfaced rather than using stale startup settings.
At container creation, usage above this soft limit triggers `go clean -cache`.
Active writers may exceed the limit. Module/pnpm stores are not deleted beneath
running consumers, so dependency content alone can exceed it. This is not a
hard disk quota. Custom images without Go cannot perform Go-cache trimming.
`swarm_get_environment` reports `cache.bytes`, `cache.limit_bytes`, and the volume
name; `cache_error` means the size could not be measured, not a measured zero.

Real Docker cache tests skip explicitly when Docker or the local test image is
missing. They do not pull images. No speedup or timing is claimed without running
the actual requested package in both containers.

## Runner landing sandboxes

The landing runner sizes each check sandbox from host RAM (`MemTotal` in
`/proc/meminfo`), its configured `--slots` count (concurrent landings; default 1,
allowed 1–16), and that landing's check count. In
`swarmd/internal/runner/engine.go`, the per-check memory limit in bytes is
`max(2 GiB, (RAM / 4 * 3) / (max(1, slots) * max(1, checks)))`, using integer
arithmetic: roughly 75% of host RAM divided among those workers, with a 2 GiB
floor. That floor can make the combined limits exceed the 75% budget. Failure
to read valid host memory information rejects execution rather than using a
fallback.

Each check's CPU limit is `runtime.NumCPU()`, the whole-machine CPU count visible
to the runner, not a fraction divided by slots or checks. After sizing and before
creating the sandbox manager, the runner logs one line per landing to stderr
(with a timestamp): `runner landing <id> sandbox memory=<bytes> cpus=<count>`.

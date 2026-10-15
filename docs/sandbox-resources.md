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
and declared `GOMODCACHE`, even when the job uses a custom environment. Existing
volumes are not reseeded when images change. The default image must include
`flock`, `find`, `cp`, `chown`, and `du`. Initialization failure rejects container
creation rather than silently falling back to a private cache.

Sandbox `GOFLAGS` is `-buildvcs=false -trimpath`. Identical code across worktrees
can reuse compiled packages and eligible test results. Go still invalidates
results for changed inputs, environment, flags, and tests that inspect paths or
files; this is not a promise that every test is cacheable across worktrees.

The box's startup configuration accepts `sandbox_project_cache_bytes` (or the
daemon flag `--sandbox-project-cache-bytes`); zero selects 10 GiB per project.
At container creation, usage above this soft limit triggers `go clean -cache`.
Active writers may exceed the limit. Module/pnpm stores are not deleted beneath
running consumers, so dependency content alone can exceed it. This is not a
hard disk quota. Custom images without Go cannot perform Go-cache trimming.
`swarm_get_environment` reports `cache.bytes`, `cache.limit_bytes`, and the volume
name; `cache_error` means the size could not be measured, not a measured zero.

Real Docker cache tests skip explicitly when Docker or the local test image is
missing. They do not pull images. No speedup or timing is claimed without running
the actual requested package in both containers.

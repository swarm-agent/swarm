# Worker sandbox resources

The agent sandbox is off by default on a headless install: agents run in Swarm
on the machine. With the Docker sandbox on, each project has one sandbox, shared
by all its jobs (never one container per job); it mounts the project and its
worktrees. Under bubblewrap a command sees only its own checkout. Reviewer
containers retain their separate writable cache and read-only project/Git
mounts. The umask guard and in-flight lifecycle leases remain unchanged.

There are no default caps. Without owner settings the box admits every worker
run, and a sandbox has no memory, swap or process cap.

An owner who wants caps sets them in the install config (`swarm.conf`):
`sandbox_memory` (Docker size syntax; empty means no cap) and
`worker_max_concurrent_runs` (0 means no limit). Daemon overrides are
`--sandbox-memory` and `--worker-max-concurrent-runs`. Restart the daemon after
changing these settings.

When set, the box-wide limit spans accounts. Preparation counts against the limit before
worktree allocation/provider dispatch. Overflow receipts persist `status: queued`
and `queue_reason`; the existing worker reconciliation sweep admits them as
capacity becomes available. Queue order follows reconciliation order, not strict
FIFO. Admission state survives daemon restart. A durable active-reservation index is
updated in the same batch as each receipt, so terminal history never enters the
capacity count. Follow-up messages retain the original job outcome and expose
`followup_status: queued` with `queue_reason` until the executor reserves a slot;
`followup_status: running` holds it until the intent ends. V3 pending intents
provide restart-safe queue replay, and stale completion cannot free a newer slot.

Terminal runs remove only their disposable job container. Startup inventories
containers by the sandbox label, removes terminal/unknown job containers and
stops surviving idle containers before admitting execution. Idle job boxes are
stopped, never removed, by the reaper. Terminal removal is fenced against newer
acquisitions and waits for command/Git leases. Project gateway registration stays
valid until the last container reference is removed. Follow-up reservations are
released atomically with terminal intents, including restart interruption.
Shared named caches and bind-mounted checkout files are never removed. Home initialization repairs the exact same volume that the
container mounts; reviewers still use their separate home.

Container OOM/stopped/restart errors are retryable interruptions, not repository
identity failures. Bind-mounted work and caches are preserved. Observational Git
queries through the worktree identity helper recover/restart the container and
retry once; mutations and agent commands that may already have run are not
replayed. Continue the same job with a follow-up after an interruption. A terminal
worker interruption also emits a warning `job.sandbox_interrupted` signal. Exit 137 is reported as a
possible OOM rather than asserting a cause Docker has not confirmed.

## Job clone memory

Job and review clones use `boundedCloneArgs` in
`swarmd/internal/worktree/worker_clone.go` (also called by `review.go`). They keep
`git clone --no-local` isolation: objects pass through upload-pack without
hardlinks or alternates. Because gVisor counts the whole upload-pack/pack-objects
process tree against the sandbox memory limit, upload-pack uses `pack.threads=1`,
`pack.windowMemory=64m`, `pack.deltaCacheSize=16m`, `core.bigFileThreshold=1m`, and
`pack.window=10` to bound pack-building work and exclude large blobs from delta
search. The clone client also uses `core.bigFileThreshold=1m`; these settings are
command-scoped, not persisted in either repository. Repositories with many large
loose objects should still be packed with `git gc` for faster clones.

Tests: sandbox resource/classification/retry units, durable Pebble queue tests,
and `TestWorkerDockerOOMIsolation` (skips without Docker; requires the installed
sandbox image with Python 3). The Docker test allocates real memory past one
container's limit while a second command is running; it is not a benchmark.

# Worker sandbox resources

Managed worker checkouts use separate containers (approach **a**): one OOM
cannot kill another checkout's gVisor sentry. Containers share the project's
image, project/worktree mounts and home cache volume. This is resource isolation,
not a new filesystem authorization boundary. Reviewer containers retain their
separate writable cache and read-only project/Git mounts. The umask guard and
in-flight lifecycle leases remain unchanged. Tool execution pins the checkout's
container even when a command changes its working directory.

Defaults reserve 25% of physical host RAM for the OS/daemon. Worker concurrency
is the smaller of half the logical CPUs and the number of 3 GiB budgets in the
remaining RAM (minimum one). Each container gets that remaining RAM divided by
concurrency, with no swap. On **8 vCPU / 31 GiB** this is **4 workers**, each
limited to **6,241,124,352 bytes (5.8125 GiB)**. This is a capacity budget, not a
reservation or measured workload guarantee. Other sessions/reviewers and idle
containers still consume memory; operators should reduce concurrency when other
services or large caches share the box.

The install config (`swarm.conf`) accepts `sandbox_memory` (Docker size syntax;
empty means automatic) and `worker_max_concurrent_runs` (0 means automatic).
Daemon overrides are `--sandbox-memory` and `--worker-max-concurrent-runs`.
Restart the daemon after changing these settings. Linux host memory is read from
`/proc/meminfo`; when unavailable, configure both values explicitly.

The box-wide limit spans accounts. Preparation counts against the limit before
worktree allocation/provider dispatch. Overflow receipts persist `status: queued`
and `queue_reason`; the existing worker reconciliation sweep admits them as
capacity becomes available. Queue order follows reconciliation order, not strict
FIFO. Admission state survives daemon restart.

Container OOM/stopped/restart errors are retryable interruptions, not repository
identity failures. Bind-mounted work and caches are preserved. Observational Git
queries through the worktree identity helper recover/restart the container and
retry once; mutations and agent commands that may already have run are not
replayed. Continue the same job with a follow-up after an interruption. A terminal
worker interruption also emits a warning `job.sandbox_interrupted` signal. Exit 137 is reported as a
possible OOM rather than asserting a cause Docker has not confirmed.

Tests: sandbox resource/classification/retry units, durable Pebble queue tests,
and `TestWorkerDockerOOMIsolation` (skips without Docker; requires the installed
sandbox image with Python 3). The Docker test allocates real memory past one
container's limit while a second command is running; it is not a benchmark.

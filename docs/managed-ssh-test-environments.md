# Managed SSH exact-result test environments

One generic host path: existing SSH authentication + Linux Docker. GCP, AWS and
other SSH hosts are ordinary operator-provided hosts, not Swarm cloud providers.
No cloud API, broker integration, credentials acquisition or SSH config mutation.

## Prerequisites and one preflight

The operator supplies a reachable SSH host, trusted host key in the daemon user's
known-hosts/config, noninteractive external agent/config authentication and Docker
access. The host needs GNU `timeout` and sufficient bounded build storage. The
test image needs `sh`, `setsid`, `kill`, `/proc` and a writable operations directory
(default `/run/swarm/operations`). Install dependencies in the committed recipe;
build RUN networking is disabled. Use an approved base image already available
on the host. Never forward credentials or mount a Docker socket into tests.

Create an account/workspace `manage_connections` SSH definition with host/user/port
only (optional existing identity/known-hosts file references, never key contents).
Run `check` once. The ten-second probe reports ready or network/auth/runtime
failure; stop and ask the operator to restore access/runtime. Do not retry in a
loop or fall back locally. `capabilities` fails on unavailable access too; stored
capability flags are not proof that a host is reachable.

## Definition and exact source

Save `environment.build` with exact catalog product and recipe workspace IDs,
current generations and full lowercase commit OIDs, plus committed
`recipe_directory` and `recipe_file`. Use `container.image=managed-build`, matching
`registry_image.image=managed-build` and `pull_policy=never`. For SSH omit
`container.rootless_systemd`; that setting remains exclusively local Podman.
Set `reuse=true`, `release_behavior=none`, `idle_timeout_seconds=0`, and a bounded
`max_instances` and resource limits. No privileged container or extra host mounts.
The committed recipe must COPY the exported product into the test working directory
(for example `/workspace`) and provide a persistent container command.

A generic image plus mutable source staging was considered: it would require new
attachment/source authority. This path instead retains existing immutable build
and attachment receipts, with the selected source baked into one image. It does
not build a second source-sync architecture.

Obtain the exact `project_result` reference from authorized task-result inspection,
not current dev, a caller path, branch/tag, or arbitrary remote checkout. Build
re-resolves that result before effects and after building; only the product commit
is substituted, never the authorized recipe. Git exports sanitize the context:
512 MiB/30,000 entries, no traversal, links or special files; credential-shaped
files excluded. A private daemon temporary context is streamed directly to Docker;
there is no remote source directory or key copy. Local scratch is removed on exit.

## Managed sequence

1. `build(environment_id, connection_id, project_result, timeout_ms)`; retain its
   operation ID and wait for actual success, not queued admission. Output is capped
   at 4 MiB; execution is bounded by the operation deadline (ten-minute maximum)
   and the remote Docker client timeout uses the remaining operation deadline. Receipt records exact
   sources, context/definition digests, server-resolved result binding, saved
   connection digest, and remote-observed immutable SHA-256 image ID/labels.
2. `ensure` (or `deploy`) with the exact successful `build_operation_id` and same
   result/connection. No image substitution, implicit rebuild, sync or fallback.
   Keep the returned deployment and **your own** environment deployment lease.
   Reuse requires the same receipt/connection and observed image/ownership labels.
3. `exec` with deployment ID, own lease ID, same result, bounded command/timeout
   and `max_output`. Existing container supervision handles process groups and
   cancellation; uncertain termination remains explicit. No shell testing outside
   the manager. Managed SSH port publications are remote loopback-only; direct HTTP health/frontend endpoints are not supported on this path; they are not
   directly reachable browser URLs and no automatic public tunnel is created.
4. `release` your lease on every terminal path. Release preserves the reusable
   image/container; it is not deletion or cloud access revocation. Separate
   authorized `destroy` removes only an observed owned container by immutable ID.
   No host-wide prune or shared cache deletion. Images/cache remain operator-owned
   retained Docker resources; inspect exact operation tags before explicit cleanup.
   Managed image deployments retain the existing finite review-retention deadline;
   after expiry the canonical cleanup stops an idle unleased deployment.

Task preparation/attachment/acquisition stays on the existing canonical routes:
attach the ready deployment's immutable provenance, explicitly assign the current
attempt, release the preparer's receipt, then consumers `list_attachments` and
`acquire_attachment` for independent receipts. An attachment is evidence, not a
scope grant. Unauthorized/expired/stale task, generation, commit, connection,
consumer or missing environment blocks before access. Do not lend parent receipts.

## Stop conditions and limits

Stop on unavailable authentication/host/runtime/supervisor, wrong/missing receipt,
result or connection drift, resource limits, timeout, cancellation, or cleanup
uncertainty. Killing SSH does not prove Docker's daemon-side build stopped. Failed
transfer/build or restart therefore yields **unconfirmed cleanup**, no usable image
receipt and no replay. The operator must reconcile the exact `swarm-managed:<digest>`
operation tag on the authorized connection and restore access before a fresh build.
A successful image with stale admission is likewise not accepted or silently used.

Saved SSH connection edits invalidate receipts. Externally mutable SSH aliases,
agent/config and host replacement cannot be cryptographically attested by Swarm;
operator-managed host keys/access and stable host identity remain prerequisites.
Do not advertise restart-proof remote build termination or immutable cloud identity.

A cloud IAM lease authorizes temporary cloud access; a Swarm deployment lease
only authorizes one consumer's environment access. An external temporary-access
system may supply the host and short-lived SSH authentication to the operator.
The parent ensures that access lasts through execution and cleanup and revokes it
after reconciliation. That external approval/acquisition is not implemented here.
No live cloud readiness or test execution is implied by this implementation.

## Exec deadline/output smoke test

Use the rebuilt daemon at the validated commit and the existing operator-approved
SSH connection. Check access once, then build/ensure as above. Retain the exact
result, deployment and own lease receipts; run these managed `exec` commands with
those same references (no direct SSH test shell):

1. `command=["sh","-c","printf 'SMOKE ASSERTION OK\\n'; printf 'stderr detail\\n' >&2"], timeout_ms=10000, max_output=4096`.
   Inspect `get_operation(operation_id)` after completion: `succeeded`, exit 0,
   `result.stdout` includes the assertion and `result.stderr` the detail.
2. `command=["sh","-c","printf 'failure assertion\\n'; printf 'failure detail\\n' >&2; exit 7"]`
   with the same bounds: expect `failed`, exit 7 and both streams retained.
3. `command=["sh","-c","printf 'before deadline\\n'; sleep 60"], timeout_ms=1000`.
   Expect `timed_out`/124 within the deadline plus the 15-second cleanup window,
   with partial output. Repeat with `timeout_ms=10000`, explicitly `cancel` its
   operation ID after start: expect `cancelled`/130 within 15 seconds of cancel.
4. Run `command=["sh","-c","printf '%0100d\\n' 0"], max_output=32`.
   Completion must include at most 32 bytes per stream and `result.truncated=true`.
   A smaller `get_operation(..., max_output=16)` further narrows the receipt only;
   fetching again does not change the originally stored output.
5. Release the own deployment lease on every settled outcome. Record exact daemon
   revision, receipts and observed statuses privately; these checks are not proof
   of provider-backed frontend suites until those actual suites have run.

The deadline includes queue waiting and is capped at ten minutes. Managed exec
passes its remaining deadline to the provider. Cleanup waits are independently
bounded; an SSH disconnect or cleanup deadline with no termination confirmation
settles as `unknown` (or a confirmed cleanup failure as `cleanup_failed`), **not**
proof of remote termination. These unresolved states block deployment reuse.
Retain the operation/deployment IDs and cleanup error; restore authorized access
and reconcile that exact operation's container/process metadata before reuse.
Confirmed cleanup releases admission capacity even if an output reader remains
hung; unconfirmed hung work conservatively retains capacity until it returns.
Queued cancellation/shutdown never claims remote termination: no provider was
started. Manager shutdown closes admission and joins registered queued/running
supervisors, allowing cleanup plus the bounded probe-timeout persistence/join
window; an incomplete join returns an error rather than a successful shutdown.

Exec results retain separate bounded stdout/stderr, actual command exit status,
and an explicit truncation flag for success, failure and partial timeout/cancel
output. `max_output` is a per-stream byte cap; nonpositive/oversized values use the
4 MiB cap. Summary remains short. Output is credential-pattern redacted (including
provided exec environment values) before the requested cap; do not print secrets
or assume arbitrary application data can always be recognized as credentials.

Missing/expired external SSH or cloud access is a blocker: stop, report the failed
preflight/cleanup and ask the operator to renew access through the full execution
and cleanup window. Do not acquire credentials, provision a VM, install frontend
dependencies, repeatedly probe the host or silently substitute a local provider.

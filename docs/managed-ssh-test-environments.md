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

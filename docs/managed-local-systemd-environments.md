# Managed local systemd test environments

This is an explicit `local_podman` adapter to the existing environment lifecycle,
not a workspace execution profile. Docker/SSH defaults are unchanged. The
running daemon must contain these changes before these actions are available.
No connection default, socket, host policy, install or onboarding is changed by
saving a definition.

## Admission prerequisites

Use `manage_connections create` with `kind: local_podman`, a name and the owning
workspace, without Docker/SSH transport configuration. Then use `check` and
`capabilities` on that connection. Checks inspect metadata only and require:

- Linux, local rootless Podman with a reported version; no remote/rootful fallback;
- crun and slirp4netns;
- cgroup v2, systemd cgroup manager, delegated cpu/memory/pids controllers;
- a reachable user systemd manager. Managed builds also invoke `systemd-run`.

A failed check does **not** prove any particular missing prerequisite unless its
receipt says so. Checks neither install packages nor change delegation, user
services, credentials, sockets or host policy. Engine compatibility and actual
systemd boot still require separately authorized runtime qualification.

## Exact committed build inputs

Save an environment with the following shape (replace placeholders with
account-authorized catalog identities and full lowercase commit OIDs). The
product commit must be the selected original candidate, **not current dev**.
The recipe is a separately authorized committed repository input; do not copy
its source into the product repository or substitute the latest recipe.

```json
{
  "name": "Manual onboarding test",
  "mode": "deployable",
  "role": "testing",
  "preferred_connection_id": "<local-podman-connection>",
  "build": {
    "product": {"workspace_id": "<product-workspace>", "workspace_generation": 1, "commit": "<exact-product-oid>"},
    "recipe": {"workspace_id": "<recipe-workspace>", "workspace_generation": 1, "commit": "<exact-recipe-oid>"},
    "recipe_directory": "<committed-recipe-directory>",
    "recipe_file": "<committed-recipe-directory>/Containerfile"
  },
  "container": {
    "image": "managed-build",
    "command": ["/sbin/init"],
    "rootless_systemd": {"cgroup_namespace": "private", "network": "slirp4netns", "pids_limit": 1024},
    "exposed_ports": [{"container_port": 15555, "host_port": 15555, "protocol": "tcp"}]
  },
  "provisioning": {"strategy": {"kind": "registry_image", "registry_image": {"image": "managed-build", "pull_policy": "never"}}},
  "deployment_policy": {"reuse": true, "max_instances": 1, "release_behavior": "none", "idle_timeout_seconds": 0}
}
```

Use `manage_environments create` with the nested `environment` object (or the
same top-level definition fields). Nested values are objects, not JSON strings.
Unknown fields, raw source host paths, mutable refs and runtime flag bags are
rejected. Build sources are resolved from the account catalog with generation
checks before and after execution. A saved definition is not a task-result
attestation: the selecting parent must verify the task receipt's exact commit
and recipe metadata before saving it. A task reference itself is not a source
path. Builds do not use `project_result` (that field is for leased validation).

The product tree is exported at context root and the selected recipe directory
under `.swarm-recipe/<recipe_directory>`. `COPY` in the recipe must use these
paths. Git archive exports committed content only; working-tree files and Git
metadata are not copied. Symlinks, hardlinks, special files and unsafe paths are
rejected. Credential-shaped files are excluded. Review committed sources for
secrets: filename exclusion is not a secret scanner. The managed exporter, not
the product's unrelated `.dockerignore`, defines the clean context. Archives
are capped at 512 MiB and 30,000 entries across both inputs.

## Action flow after reviewed integration/rebuild

1. `build` with `environment_id`, explicit `connection_id`, `timeout_ms` (at most
   600000), and a stable `idempotency_key`. Do not supply source workspace paths,
   runtime targets, commands, environment overrides or a previous build ID.
2. Retain the returned `operation_id`. Inspect `get_operation` or durable updates
   without busy polling. Only `status: succeeded` with `result.build` is usable.
   `cancel_operation` uses the ordinary supervised cancellation path.
3. `ensure` (or `deploy`) with the same environment/connection and
   `build_operation_id` equal to that exact successful operation. No tag/image
   override is accepted. An old source/connection receipt is rejected. The
   deployment retains the build provenance and inspects the actual image ID.
4. Use the returned deployment and lease with `exec` only for authorized setup.
   Onboarding remains a deliberate human operation, never an automatic build or
   deployment side effect. Release the exact lease with `release` when done.
   Release retains the container and user's state; explicit `destroy` is separate.

Runtime ports, including dynamically assigned ports, publish only to
`127.0.0.1`. The adapter emits `--systemd=always`, private cgroups,
`slirp4netns:allow_host_loopback=false`, a PID limit, `--pull=never` and no host
binds. No host HOME, provider credential, Docker socket or host network is
mounted. Recipe image installation and runtime setup remain separately reviewed.

## Build ownership and evidence

Builds run one job in an operation-owned systemd user unit with a 600-second
maximum, control-group termination, 1024-task/CPU/memory limits and a per-file
size limit. Image export is capped at 8 GiB. Recipe output is discarded rather
than persisting potential secrets. Build storage, auth/config files, HOME,
hooks and temporary directories are private to that operation under the daemon
cache root. Empty registry auth, no proxy inheritance, OCI isolation and private
rootless networking are explicit. The account's local engine imports only the
resulting image by immutable ID; labels bind it to operation, inputs and product
commit. Success returns source identities, definition/context digests and image
ID, not claims about install/onboarding health.

Imported-image admission compares only validated full lowercase SHA-256 IDs;
Podman's bare and `sha256:`-prefixed spellings are equivalent, but short IDs,
different digests and mismatched operation/input/revision labels are rejected.

Cancellation/recovery stops and verifies only the operation unit, unmounts its
private store and removes populated VFS storage through scoped `podman unshare`
with the same sanitized HOME/runtime/config/store overrides as the build.
Namespace removal traverses only this store, adds owner traversal/write permission
to its directories (including read-only/mapped-owner VFS layers), and refuses
filesystem crossings. It never uses global reset/prune, sudo or host-wide
chmod/chown. A separate operation-owned cleanup unit has control-group termination,
a runtime/task limit and explicit stopped-state verification, including restart
recovery and cancellation; killing only the command launcher is insufficient.
Both directory-bound ownership receipts survive unconfirmed/partial storage
removal so cleanup can retry after restart; foreign/missing evidence fails closed.
Cleanup commands share a bounded deadline and withhold arbitrary output, exposing
only phase/exit classification and fixed untrusted troubleshooting hints.
For private operator investigation, retain the exact operation's receipts and
review the authorized unit/engine diagnostics privately; raw recipe/layer output
may contain secrets and must not enter task reports or durable operation output.
Do not manually delete retained paths based on their names: verify both receipts,
operation/build-root binding and directory identities through the reviewed cleanup
path before recovery. This does not enable a new raw-log endpoint or automatic
retry, and unit tests do not establish real mapped-owner kernel cleanup.
Failed/cancelled imported images
are selected by the operation label and removed without force; unrelated images
are never pruned. Unconfirmed cleanup is `cleanup_failed`, not success. A
successful image remains available to the accepted build receipt. Operators
must separately monitor disk capacity: per-file/context/export limits are not
a filesystem-wide quota. Rootless container isolation is not a sandbox for an
unreviewed hostile recipe; only explicitly reviewed committed recipes belong
in this workflow.

Focused unit/contract tests prove decoding, source/receipt admission, bounded
archive handling, argv isolation, cancellation and cleanup with injected
engines. They do not establish actual host capability, image build/install,
systemd boot or user onboarding. Those need exact-revision live receipts after
reviewed integration and daemon rebuild.

## Task-linked handoff

The task attachment interface uses `manage_environments` or authenticated
`POST /v1/task-environments`. Supply explicit `project_id`, `task_id`, and
catalog `workspace_id`; attachment operations deliberately do not accept
`workspace_path` or `project_result` as alternate source authorities. Orchestrator
may prepare before assignment. A task-linked Swarm may prepare only its current
task's clean committed source. Coding tasks require an owned isolated worktree;
a general Swarm coordinator may select its exact catalog source read-only while
its delegated Coder receives an independent worktree. Product workspace/generation must match
the task source; product commit must match its current clean checkout. Recipe
source must also resolve to an authorized project workspace generation.

1. Create the committed build definition, then `build` with task references and
   `environment_id`. Inspect the exact returned `operation_id` with
   `get_operation`; use durable updates rather than polling. `ensure`/`deploy`
   selects its successful `build_operation_id` explicitly.
2. The **originating preparer** calls `release_preparation` with task references,
   `workspace_id`, and the successful ensure/deploy `operation_id`. Wait for the
   release operation to finish through durable updates. This releases only that
   consumer's exclusive preparation receipt and retains the managed deployment;
   it never copies that receipt to the task or takes another consumer's lease.
   A generic non-task preparer uses its ordinary own `release` receipt instead.
3. `attach_task` supplies `attachment_id`, `expected_task_revision`,
   `expected_attachment_revision` (zero for creation), `deployment_id`, and an
   epoch-millisecond `expires_at` within 24 hours. Alternatively select a build
   `operation_id` to record preparing/building evidence. Completed build evidence
   is not automatically switched to a deployment: explicitly CAS replace it.
4. Before assignment, omit `attempt_id`. After task deployment/reopen, list with
   `list_attachments` and CAS replace the attachment with the current exact
   `attempt_id`. Attach never starts a task, approves scope, or changes its code.
5. Each authorized consumer calls `acquire_attachment` with explicit
   `attachment_id`, `expected_attachment_revision`, `attempt_id`, and optional
   `ttl_millis` (maximum one hour). Each gets its own receipt. `exec`,
   `get_deployment`, `get_operation`, `cancel_operation`, and `release` include
   the task/attachment identity, `workspace_id`, and that consumer's `lease_id`.
   Preparation status/cancellation uses its originating operation without an
   attachment. Exec supports `env`, `working_dir`, `timeout_ms`, and `max_output`.

The same steps support Swarm-created environments after execution starts.
Current task discovery is available on every turn and through `list_attachments`;
no originating conversation or parent's lease is needed. Reopen, source edits,
workspace-generation change, runtime replacement, expiry and detach invalidate
execution. Commit changes, rebuild the explicit source, select the exact new
runtime, and CAS reassign; never substitute current dev. Detached attachment IDs
are permanently retired for that task; create a fresh ID. Mutations are bounded
at 16 live attachments and 4096 retired identities per task.

### Ordinary delegated Coder execution

Task administration may use `acquire_attachment` with `execution_session_id` to
select the current parent task session. Ordinary Task launch then binds a direct
Coder child to its own shared receipt before starting it. Durable current lineage,
account/user, task attempt, source and worktree ownership must match. The child's
receipt records the parent receipt; each Bash dispatch and provider boundary
rechecks the current parent selection, lease and attachment. Approval remains in
the parent's permission scope while execution uses the child's session identity.
Coder receives no environment-management authority.

A missing or invalid managed selection never falls back to local execution,
including with `--sandbox=off`. Binding failures retain a blocked selector and
release the consumer receipt. This slice supports direct Coder delegation and
local Podman execution with the bounded delegated file/Git handoff described
below. Nested delegation and context-rotation rebinding remain unsupported.
Provider-backed repair-cycle claims require exact-candidate live receipts. Other agent roles are refused while the parent has
a managed execution selection. The returned source commit is the prepared
baseline, not an attestation of the current remote filesystem.

### Lifetime and realtime

Managed deployments retain state on individual release and task completion, but
have an immutable review deadline (24 hours from creation for existing/default
records). Attachment and lease expiry cannot extend it. Automatic cleanup runs
on daemon startup and traverses at most 64 durable deployment records per minute,
with a 15-second sweep budget. Live leases or unresolved operations postpone
stopping; failures remain retryable and visible. Large catalogs can delay cleanup.
Explicit `cleanup_review` performs one bounded workspace sweep. Stopping does
not delete retained state/images; explicit authorized destruction remains separate.
Do not treat an attachment expiry as a promise that resources have been removed.

Task mutations emit canonical project invalidations. Environment/deployment,
operation and lease mutations atomically emit `environment.updated` with bounded
`task_environment_targets` and `task_environment_workspace_invalidated` for
workspace rehydration, including upgrade repair. Clients must subscribe to the
relevant environment workspace scopes as well as project events; no recurring
card-status polling. Projections report preparing/building/ready/failed/stopped/
stale, and never serialize lease receipts into task cards or seed context.

These are source contracts, not evidence of a live container or browser-ready
frontend. Focused Go contract tests are authored separately; execution receipts
and manual container/browser qualification are required before runtime claims.

### Task-card browser access

Task cards show each attachment and its runtime/preparation status, including
bounded failure guidance and a link to the exact workspace/deployment in the
existing Deployments view. Missing catalog identity fails closed. Scoped durable
`environment.updated` events invalidate cards and coalesce reloads; there is no
recurring status polling. Attachment expiry disables browser controls locally.

Declare `frontend_endpoints` in the environment definition **before deploying**.
Each endpoint requires a unique `id`, a `name`, a declared TCP `container_port`,
`scheme` (`http` or `https`), and a safe absolute `health_path`; `path` defaults to
`/`. At most eight endpoints are accepted. Paths cannot contain credentials,
query strings, fragments, encoded escapes, traversal or scheme-relative URLs.
Nested `environment` creation and JSON import accept these typed definitions;
the HTTP environment replacement route can update definitions. Deployment
creation snapshots the endpoint intent and declared ports. Editing a definition
does not alter an existing deployment's browser intent: explicitly deploy again
and reattach. Legacy deployments without the snapshot offer no browser access.

**Check browser access** sends an authenticated `POST /v1/task-environments` with
`action: browser_endpoints`, exact project/task/workspace/attachment IDs and
`expected_attachment_revision`. This is a Desktop HTTP action, not a new model
tool action. It does not acquire or copy a lease. Source, attempt, expiry and
runtime generation are checked before and after probes. Only local engine
mappings to literal `127.0.0.1` or `::1` qualify; SSH and container-only addresses
do not. Each configured health path must return HTTP 2xx without redirects,
ambient proxy use or disabled TLS verification. Up to eight probes run together
with one-second request timeouts and a five-second overall budget. Response
bodies are not read or disclosed. Mere container existence/health is insufficient.

Multiple endpoints require explicit selection. **Open in browser** reserves a
blank browser tab during the click, severs its opener, then rechecks readiness
and validates the fresh loopback URL/mapped port before navigation. Failure,
cancellation, expiry, detach or component teardown closes the pending blank tab;
popup blocking produces an actionable error. The browser's normal new-tab path
is used; no separate native external-browser bridge is present in this client.
Backend-only deployments return no browser endpoints and retain agent exec use.

Frontend URL, popup-controller, card-rendering, route-identity, realtime-wire,
workspace-demand and invalidation regressions are authored **but not run** in
this implementation task. Go endpoint/snapshot tests are also authored; their
execution belongs to the separate focused verification checkpoint. No frontend
build, screenshot, browser run, container run or live-readiness claim accompanies
this source implementation. Manual browser/container qualification remains a
separately authorized follow-up.

## SSH exact-result capability boundary

SSH Docker now has a separate exact committed-context build path; it does not
emulate local rootless systemd. See [managed SSH test environments](managed-ssh-test-environments.md)
for prerequisites, receipts, stop conditions and uncertain cancellation cleanup.
Local Podman contracts remain unchanged. Cloud authority acquisition and host
provisioning remain external operator responsibilities, not Swarm broker APIs.

## Delegated Coder checkout and Git handoff

An admitted direct Coder receives a distinct receipt-derived checkout inside the
selected deployment. Preparation transfers the exact allocated base commit and
its tree as a shallow Git repository. It does not copy host Git configuration,
hooks, credentials, unrelated history or the host working directory. The recipe
must provide Bash, Git and GNU coreutils, plus the project's test dependencies.
Configure the intended commit identity/signing inside that environment; host
signing keys and remotes are never copied automatically.

Bash, `read`, `write`, `edit`, `git_status`, `git_diff`, `git_add` and `git_commit`
use that checkout. File operations reject path traversal, Git metadata and
symlinks; edits use content-hash compare-and-swap. Other host-bound tools stay
refused. File staging is limited to 1 MiB per file. Committed source packs are
limited to 8 MiB, with the existing 4 MiB encoded operation-output limit also
bounding returned packs. Oversized transfers are rejected explicitly. Symlinks,
submodules, special files and protected credential-shaped paths are unsupported.
These limits are admission constraints, not a general repository compatibility
claim. No local fallback is allowed.

On successful Coder completion, trusted orchestration obtains the remote commit
and pack through the lease, checks clean source/ancestry and the still-current
receipt, then imports only into the allocated child worktree. Each of at most
256 linear commits must stay within owned scope. A dirty, moved or renamed
local destination is refused; the committed remote checkout is retained for
inspection. The exact commit ID is preserved. The normal Coder handoff then
inspects the returned local Git state independently. The parent source branch is
not integrated, pushed or deployed. A failed handoff must not be reported as a
successful implementation.

Every operation revalidates the receipt at admission and provider dispatch.
While a managed command is active, receipt/task validation repeats at roughly
one-second intervals; revocation, replacement or expiration requests supervised
cancellation. Command deadlines and explicit stop use the same provider process
cleanup. A failed cleanup remains an error. Completion or failure releases the
child receipt; deployment retention/destruction remains the owner's policy.
Context rotation and other providers require separate qualification.

## Opt-in Headless service prerequisites

The default Headless installation remains Bubblewrap and does not install or
activate Podman. For an explicitly selected local Podman managed backend on a
systemd host, an administrator must provision Podman, crun, slirp4netns, uidmap
and a user session bus; assign a non-overlapping subordinate UID/GID range to
the daemon account; and enable its persistent user systemd manager. Do not copy
another host's numeric range without checking allocations.

For the packaged `swarm-headless.service`, the administrator also installs an
explicit service drop-in before activation:

```ini
[Service]
NoNewPrivileges=no
RestrictSUIDSGID=no
ProtectControlGroups=no
```

These exceptions allow rootless UID mapping and user-manager cgroup operations;
they do not select sandbox-off, change loopback listeners, grant permission
bypass or mount host directories into managed containers. Keep the installed
Bubblewrap `ExecStart`. Reload systemd and restart only the intended stopped or
drained daemon as an authorized installation action. An existing live service
must not be changed as an incidental environment capability check. Verify the
connection's actual capabilities and a disposable managed build/exec before
using it for work. Missing prerequisites remain an actionable refusal; no
rootful or host execution fallback is provided.

## One definition, independent job instances

A definition and its committed build are reusable recipes, not a running job's
private environment. Use `deploy` with the same successful `build_operation_id`
to create a distinct deployment for each independent job, up to the definition's
`deployment_policy.max_instances`. Attach each job to its own deployment and
acquire that attachment's receipt. Its delegated Coder borrows that assignment
with a separate child receipt and checkout; it does not provision another box.
Acquiring another receipt for one existing deployment does not clone it.

Use `resources.cpu_limit`, `resources.memory_limit` and the rootless PID limit
for per-instance ceilings appropriate to the workload. These do not reserve a
fixed amount of RAM per agent. Capacity exhaustion is an explicit refusal, not
an implicit host fallback or a promise to queue/provision another cloud host.

Managed builds retain their deployments on receipt release. For a fresh job
instance, explicitly destroy an unleased deployment and deploy again from the
same build. `ensure` can reuse an unleased compatible deployment and is not a
clean-reset guarantee. Active receipts prevent destructive lifecycle operations;
release the owning parent receipt after its delegated work ends, then destroy
the instance when retention is no longer wanted. Keep each job's exact source,
deployment and lease association in its durable task attachment.

A managed selection keeps the Swarm owner's receipt controls available through
`manage_environments`: release, operation inspection/cancellation, deployment
inspection and attachment listing. These still cross the existing durable
role/task/receipt checks; Coder does not gain environment management authority.
Provisioning, reassignment and alternate execution remain blocked while that
selection is active. Task receipts must be released through the owning task's
control path with explicit project, task, attachment, workspace and lease
references; the generic environment endpoint cannot impersonate that consumer.

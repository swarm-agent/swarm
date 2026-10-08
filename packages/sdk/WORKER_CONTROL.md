# Headless worker control (configuration preview)

`client.workers.control` uses authenticated `/v3/workers/{worker_id}` APIs.
It extends canonical worker identity, run receipts and V3 worker realtime
invalidation. It is not a second executor or a direct SSH/cloud deployment helper.
Use a trusted backend/private full API connection, not the restricted container
session listener. Never give a browser the privileged daemon socket.

## Supported operations

| SDK operation | Route suffix | Effect |
| --- | --- | --- |
| `registerSSHTarget` | POST `ssh-targets` | Register an existing-host configuration in the canonical workspace/account connection catalog; idempotent, no network contact. |
| `resolveTarget` | POST `target-reference` | Authorize an existing SSH connection or GCP topology reference and compute its configuration digest. |
| `getContext`, `updateContext` | GET/PUT `context` | Read current/exact revision; publish explicit bounded knowledge with CAS and provenance. |
| `proposeDeployment` | POST `deployments` | Persist pending intent pinned to worker/context/target and explicit lifecycle. |
| `listDeployments`, `getDeployment` | GET `deployments[/{id}]` | Inspect persisted intent, not runtime health. |
| `approveDeployment` | POST `deployments/{id}/approve` | Explicit user approves exact revision/digest; stale worker/context/target rejects. |
| `queueJob` | POST `deployments/{id}/jobs` | Admit a canonical run with resolved account model, context, target, deployment and pending-attempt pins. Does not execute it. |
| `command`, `commands` | POST/GET `deployments/{id}/commands` | Stop is durable pending intent; start returns 503 unavailable. |

Use existing `workers.listRuns`, `getRun`, and `cancelRun` for job receipts.
Cancellation of a still-unstarted remote intent releases reservations atomically
and records `cancelled_before_dispatch`. It is not remote runtime cleanup.

## Safety and semantics

- SDK transport/user identity is authenticated; request JSON cannot supply an
  account or agent role. Scopes remain `automations:read`/`automations:write`.
  Trigger/worker-scoped credentials cannot use control-plane operations.
- Ordinary chat/subagents cannot manage workers. Canonical Orchestrator agent
  callers can propose intent through the shared service; approvals and explicit
  context publication require a user. This preview adds no AI tool schema.
- Existing workers keep IDs and local behavior. Queued remote intent reserves
  one job per deployment and one context writer per worker. Local admission is
  also blocked while a remote context writer owns the worker. The first target
  capacity policy is shared across deployments; a later proposal cannot silently
  enlarge it. Reservations survive restart.
- `persistent` and `on_demand` are explicit for SSH and GCP. Approval sets desired
  state `running`, while observed state remains `unavailable`. A saved intent is
  never readiness. A stop command closes further admission, stays `pending`,
  and does not fabricate acknowledgement. Cancel pending jobs separately.
- Registered existing targets authorize only `owned_runtime` cleanup. No host
  delete/shutdown capability exists. Future GCP provisioning must establish exact
  owned resource identity before adding compute deletion.
- Context is at most 64 KiB plus 32 bounded opaque artifact/checkpoint references;
  references grant no access and must be reauthorized before use. Updates reject
  while jobs own context. No automatic transcript ingestion or vector store.
- Worker changes require new reviewed deployment intent. The preview requires a
  currently approved active worker and its existing primary workspace binding to
  admit a job; it does not invent a different worker approval engine.

## Durability and replay

Deployment, context, admission and command mutations use the canonical durable
worker transaction/outbox. Subscribe to existing V3 `worker.updated` account
invalidation and rehydrate these APIs; retain opaque scoped endpoint cursors.
Do not parse cursor numbers or poll for fabricated runtime progress. Worker runs
retain session identity, but a queued remote attempt has no materialized V3
session until an adapter actually prepares execution. Do not call a session
watcher on that reserved ID as if it were a live session.

## Explicitly unavailable

No SSH process launch, GCP provisioning, remote readiness/heartbeat, execution,
remote acknowledgement, remote context publication, result manifest or cleanup
is implemented by this preview. Legacy direct dispatch is not a remote fallback.
Remote outcome writes through the local writer fail closed. Generation is pinned
at one until a qualified adapter supports replacement/retry fencing. Schedule
occurrence deduplication is retained at canonical storage; selecting remote
placement in the live scheduler is deferred, not silently enabled by a proposal.
GCP references must already exist in canonical account topology with transport
`gcp`; this API does not enroll cloud credentials or create machines.

`examples/headless-worker-control.ts` is an SDK-only configuration example. It
registers up to ten explicit SSH configurations and proposes placement. Approval
is a separate user decision; running the example is not live execution proof.

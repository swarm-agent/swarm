# Durable workers: local lifecycle, SDK and canonical hub

Status: steps 1–3 implemented the bounded foundation with focused deterministic verification, not the complete product journey. Step 4 now closes the remaining lifecycle, Orchestrator, hub and Tasks integration gaps. Step 5 proves the completed journey on the real local testbench. All live E01–E20 acceptance remains unverified. Step 4 has not started; this revision refines scope only.

## Goal and scope lock

A worker is a stable, durable, account-owned object. It is not a session, plan or schedule. Users and the Swarm Orchestrator manage it in `/swarm/workers`; the SDK uses the same backend contract. Attach executable plans as automations to a worker; each execution is a distinct run. Also support workers with no attached automation, receiving direct requests.

Ship the smallest complete local lifecycle and prove it on the existing local testbench. External triggers mean authenticated signals arriving from outside Swarm, not cloud execution. Defer S3, cloud provisioning, marketplaces, remote runners, generalized packaging frameworks, new memory engines and multi-target deployment orchestration. Do not turn this into a general custom-agent product.

This checklist governs this effort; older cloud-worker roadmaps are background, not added scope. Keep unchecked items unchecked until their postconditions are observed. Update this document with each implementation checkpoint.

### The user outcome, in plain English

Tell Orchestrator what specialist you want, define and approve it, and keep it as a stable worker even when it has no jobs. Give that same worker one task, test it safely, or attach ongoing work later. Orchestrator must be able to explain its configuration, inspect authorized prior run sessions and outputs, help diagnose a failed attempt, revise it and test again. The hub is the everyday control panel: on/off, current work, activity by day and results. Work needing a human decision must appear in Tasks with a usable deliverable and a route back to the worker/run.

Here, “agent” means this durable worker specialist, not a new custom system-agent registry or a permanently running model process. Deploying makes the approved worker ready to receive work; zero jobs must be a normal ready/idle state, not an error and not a reason to manufacture an automation. The urgent target is a usable local end-to-end workflow; urgency does not justify claiming launch readiness before Step 5 passes. Host installation/promotion remains a separately authorized operation after candidate acceptance.

## Inspected starting points (not live proof)

- `swarmd/internal/session/automation_v2.go`: proposals accept `SessionPlanDocument`; worker identity needs an independent durable authority rather than an authoring-session dependency.
- `swarmd/internal/store/pebble/automation_v2*.go`, `swarmd/internal/run/automation_v2_execution.go`, `swarmd/internal/api/automations_v2.go`: existing persistence, scheduler/execution and API paths to reuse, not a second execution engine.
- `swarmd/internal/agent/system_agent_registry.go`: dedicated Orchestrator profile enables `manage_workers`; ordinary Swarm disables it. Verify backend enforcement, not just tool visibility.
- `swarmd/internal/run/automation_v2_tools.go`, `swarmd/internal/tool/automation_v2_schema.go`: current AI worker authoring contract to align with stable worker IDs.
- `web/src/features/desktop/orchestrate/OrchestrateView.tsx`: canonical middle-panel hub and adjacent Orchestrator chat.
- `packages/sdk/src/automations.ts`: existing client mixes worker and automation terminology and addresses controls by session/generation.
- `packages/sdk/src/storage/types.ts`: existing `WorkerActorSpec` mixes instructions, cloud configuration, schedule and mutable task state. Do not introduce another competing worker definition.
- `packages/sdk/src/deploy/worker-cloud-deployer.ts`: existing cloud helper has hardcoded environment/model defaults. It is not authority for the local worker design; do not reuse those defaults or expand cloud work in this scope.
- `scripts/testbench-local-deploy.sh`, `scripts/local-testbench-pool.md`: maintained local testbench workflow. Existing spec filenames or historical results are not evidence for this change.

## Minimum data contract

Use three durable concepts, reusing existing plan/run infrastructure:

| Object | Required responsibility |
| --- | --- |
| Worker | Server-owned immutable ID and account ownership; name, purpose, standing instructions, revision, lifecycle state, requested capabilities, workspace role requirements and approved local bindings. Exists without a plan or authoring chat. |
| Attached automation | Own ID and worker ID; executable plan revision, requested deliverables, activation mode (manual, interval, cron, external trigger), trigger/input contract and enabled state. Zero or many per worker. |
| Run | Own ID; worker ID/revision, optional automation ID/revision, request source, accepted input, resolved context provenance, execution session IDs, status/timestamps/errors and deliverable references. |

A direct request creates a run without manufacturing a persistent automation. A test run uses the same admission/execution path and is explicitly labelled; it does not enable schedules. Reuse the existing plan document for executable steps, not as the worker record. A worker may simultaneously support schedules, triggers and direct prompts; these are not separate agent classes.

One local deployment operation validates and activates the stored worker with approved bindings. Do not add a generic deployment orchestration subsystem now. Creation/import can leave it inactive until activation is approved. The same worker ID survives edits, automation changes, pause/resume, authoring-chat archival and daemon restart. Every run pins the accepted revisions; later edits cannot rewrite active or historical runs.

### Portable file and SDK contract

- [ ] Define one versioned, strictly validated JSON worker document and matching backend/SDK types. Keep instructions as a Markdown string inside this file for now; a separate `WORKER.md` loader is unnecessary.
- [ ] The document contains the complete reusable definition: identity provenance, metadata, instructions, capability requests, named workspace requirements and attached automation definitions/plans with input and deliverable requirements.
- [ ] Account authority, credentials, host paths, approvals, active leases and mutable run history are not portable definition fields. Export/import must not imply transferring those privileges or live processes.
- [ ] Reject unsupported schema versions, unknown structural fields, oversized inputs and invalid plans before any partial mutation. Do not silently discard data or execute imports.
- [ ] Export/import round-trips the supported definition without semantic loss. Import-as-new allocates a new local identity and records provenance. Updating an existing worker requires explicit target ID, authority and expected revision; a file cannot overwrite by claiming an ID.
- [ ] Activation resolves workspace roles and secret references locally, obtains required approvals and returns the durable worker ID. A file cannot grant tools or permissions.
- [ ] SDK supplies typed create/get/list/update, import/export/validate, local deploy/activate, attach/update/remove automation, direct request, test run, authenticated trigger, history/deliverables and lifecycle controls over canonical daemon APIs. Preserve pagination, explicit errors, idempotency and revision guards.
- [ ] Reconcile the existing `WorkerActorSpec` and automation client explicitly. Convert supported legacy fields once or reject them clearly; no independent SDK scheduler/store/worker authority and no silent lossy adapter.

### Context and multiple workspaces

- [ ] Standing worker instructions are explicit stored data; they are not inherited implicitly from the authoring conversation.
- [ ] Model configuration resolves through canonical account settings; do not embed a hardcoded fallback or export credentials.
- [ ] Named workspace roles bind to authorized local workspace IDs. Required missing bindings fail clearly; optional unavailable context is disclosed, never silently skipped.
- [ ] Each writable repository receives isolated execution ownership. Workspace instructions remain scoped to their workspace. Reject unsupported unsafe combinations rather than writing into a shared checkout.
- [ ] Effective context inspection shows worker revision, attached plan/request, workspace instruction sources, approved capabilities and explicit attachments, with secrets redacted. Trigger input remains untrusted data.
- [ ] Do not add automatic persistent conversational memory in this milestone. Use explicit context and existing authorized state facilities only.

## Ownership, lifecycle and migration

- [ ] Only the Swarm Orchestrator may perform AI worker deployment/management. Ordinary chat and delegated agents are denied by backend identity checks even with crafted calls; hiding a tool is insufficient.
- [ ] Authenticated user UI and SDK clients may use authorized management APIs. An external trigger credential can dispatch only its scoped worker/automation; it cannot edit, deploy, delete or enumerate unrelated objects.
- [ ] All callers share one lifecycle service, authorization, revision checks, idempotency, durable events and observable errors. Session creation/mutations still cross the canonical V3 boundary.
- [ ] Worker pause first closes admission for schedules, external signals and direct requests, invalidates queued work, and requests cancellation of active runs. Show `stopping` until cancellation is acknowledged; show failure truthfully if it cannot complete. Pause here is not merely schedule pause.
- [ ] Automation-only disable stops that automation's future/queued/active work without stopping unrelated worker jobs. Label worker-wide and automation-specific controls distinctly.
- [ ] Archive/delete use the same stop barrier and cannot report success with uncontrolled active work. Preserve owned history/deliverable references under existing retention policy; use a tombstone rather than cascading history destruction. No purge feature in this scope.
- [ ] Resume does not replay cancelled work or missed ticks unexpectedly. A paused/archived/deleted worker stays stopped across restart. Completed external side effects cannot be undone by cancellation; disclose this limit.
- [ ] Migrate existing records idempotently, preserving established worker IDs where valid, automation ownership, sessions, histories and outputs. Resolve collisions explicitly; never silently combine distinct workers. Failed migration must not partially activate schedules.

## Canonical hub and Orchestrator interaction

- [ ] `/swarm/workers` remains the canonical middle-panel hub with Orchestrator chat beside it; do not create a parallel management page.
- [ ] Cards show state, current work, next scheduled execution, daily run counts and failures; include edit, pause/resume, archive/delete and direct-request controls with correct scope/confirmation.
- [ ] Clicking opens worker details: instructions/effective context, workspace bindings, attached plans/automations, trigger configuration, current/previous runs, linked sessions and deliverables.
- [ ] Daily counts use an explicit timezone and durable run data. Empty, loading, stale and error states are distinguishable; use canonical event-driven hydration/reconnect repair.
- [ ] `Add to chat` adds a removable worker reference to the next Orchestrator message only. It does not dispatch work. Clear after successful submission; retain on failed send. Re-resolve authorization and current state server-side; reject stale/deleted references clearly.
- [ ] `Send task` dispatches an explicit direct request. The Orchestrator can inspect, attach plans, test, dispatch and manage the selected worker; ordinary chat directs deployment requests to Orchestrator.
- [ ] Each accepted request, automation and deliverable is traceable to the same worker identity across UI, SDK and Orchestrator.

## Implementation order: five steps, no cloud detour

Steps 1–3 below retain their historical implementation evidence. Their checked boxes do not certify the expanded end-to-end outcome above. The unchecked contract sections remain milestone acceptance requirements; reconcile them against Step 4 implementation and Step 5 evidence before closing the milestone.

### 1. Stable object and shared SDK contract

- [x] Implement worker record/revisions, automation ownership, run linkage, migration and portable JSON validation/import/export.
- [x] Expose canonical worker definition APIs and SDK methods together; distinguish legacy SDK actor types.
- [x] Add focused requirements-first persistence, round-trip, ownership, stale-update and migration tests; review test evidence.
- Exit: an idle worker is independently durable; SDK reads/edits the same object after restart and round-trips its definition without enabling work.

#### Checkpoint 1 implementation and verification

`store/pebble/worker_store.go` owns account-scoped worker identities, revision snapshots, automation indexes and run links; `session/worker.go`, `api/workers.go` and `packages/sdk/src/workers.ts` share this authority. Schema version 1 imports create idle identities; updates require explicit target/revision. Create/import-new require idempotency keys at HTTP/SDK ingress. Portable provenance excludes local migration/approval authority. Legacy `WorkerActorSpec` remains explicitly incompatible, not silently converted.

Definition routes: collection GET/POST, worker GET/PUT, validate/import/export, automation attach/update/remove, revision/run inspection and explicit migration. SDK implements definition CRUD/import/export/validation/attachments; run controls, deployment and Orchestrator tool cutover remain checkpoint 2. Worker DELETE is deliberately not exposed before a safe stop barrier. Local bindings are rejected before approved activation. Migrated legacy records are read-only snapshots while the old executor remains authoritative; migration does not activate anything or modify legacy records. Re-run/cutover freshness must be reconciled in checkpoint 2. Only actually retained historical definitions are migrated—missing revisions are not invented.

Observed deterministic checks (not live acceptance): focused Go persistence/service/API tests pass twice; SDK transport tests pass (11); SDK TypeScript check passes. Real temporary Pebble reopen and loopback realtime replay are exercised, not provider execution. Reproducible commands, from repository root with Go/tsx/tsc available:

```sh
(cd swarmd && GOMAXPROCS=2 go test -count=2 -p 1 -timeout 60s ./internal/store/pebble ./internal/session ./internal/api -run '(Worker|Portable|LegacyMigration|LegacyAutomationV2Migration|SessionArtifactLineage_ModelAndSettings)')
tsx --test --test-concurrency=1 packages/sdk/src/__tests__/workers.spec.ts
tsc --noEmit -p packages/sdk/tsconfig.json
```

An inherited media test compilation typo was corrected to call the existing `equalArtifactLineage` helper without changing its assertions. No live testbench, host deployment, cloud work, prompt/schema changes, push or promotion is included. End-to-end SDK against a restarted candidate daemon remains E06/E10; component tests do not establish that live result.

### 2. Execution, safe controls and Orchestrator authority

- [x] Wire attached plans, schedules, external signals, direct requests and labelled test runs into the existing execution engine.
- [x] Implement shared safe stop/disable/archive/delete, restart reconciliation and idempotent admission.
- [x] Align the Orchestrator tools/instructions with the new object; restrict prompt/schema edits to this explicitly requested worker lifecycle contract. Enforce ordinary-chat denial server-side.
- [x] Complete SDK local activation, triggers, run inspection and lifecycle controls against the same service.
- Exit: focused tests prove ownership, cancellation/admission races, recovery and authority; no new executor or cloud target.

#### Step 2 prerequisite review (implementation incomplete)

- Confirmed checkpoint 1 was committed and clean before review.
- Found and guarded a run-history defect in `WorkerStore.RecordWorkerRun`: terminal outcomes could be overwritten or reopened, and running receipts could regress to admitted. The guard runs under the existing worker mutation mutex. Added a 25-transition regression matrix asserting rejected writes leave the persisted receipt unchanged; execution is still pending.
- Execution wiring still needs independent worker occurrence admission (without an authoring session), activation approval/bindings, coordinated dispatch/stop barriers and explicit migration cutover. Existing `provider_tool_invoker.go` already gates worker proposals to Orchestrator; inner management dispatch needs review rather than claiming the outer gate is absent.
- Implementation delegation failed before editing on both staged and single-Coder launch paths: parent lineage publication could not obtain fresh worktree recovery evidence. Exact-worktree discovery also returned no fresh recovery identity, despite clean Git status. Do not bypass ownership checks or mark step 2 complete.
- The configured managed test environment is Docker, not the required existing local nspawn pool; it was inspected but not deployed. No new test execution or live acceptance is claimed for this repair.

#### Step 2 direct recovery progress

Recovered the preserved core as parent-owned source edits, without claiming child Git integration. The daemon now composes `WorkerExecutionService`; HTTP and Orchestrator execution/lifecycle controls use it instead of raw worker writes, fabricated realtime notifications or receipt-only dispatch. Admission pins revisions/input and atomically persists idempotency; stops retain a durable target and require terminal acknowledgement. Ordinary chat and missing agent identity are denied. Existing V3 plan/intent execution and isolated Git worktrees are reused.

Observed focused deterministic checks: admission/reopen, automation isolation, run monotonicity, active/undispatched stop, Orchestrator denial and HTTP unavailable-executor nonmutation pass twice. A real temporary Git/Pebble dispatch test creates the canonical V3 plan/intent and retries a failed wake without duplicate admission. This is not provider execution or live acceptance.

Historical recovery snapshot (superseded by the verification below): the inherited HTTP lifecycle fixtures had no execution service and failed; an older provider-tool fixture expected worker tools on ordinary Swarm. The subsequent parent-owned work repaired those fixtures and execution gaps without bypassing worktree verification or launching more agents.

#### Step 2 completed local execution boundary

HTTP, SDK and Orchestrator now share the daemon-composed service. Real temporary Git/Pebble fixtures cover activation, direct/test/trigger dispatch, interval/cron slot deduplication, stop/resume, automation-scoped disable, archive/delete and retained history. Multi-checkpoint completion waits for all checkpoints; cancellation follows the current execution intent and persists an occurrence-wide cancellation fence. Terminal receipts wait for executor acknowledgement. Run updates commit durable worker realtime events.

Authenticated activation accepts `activate:false` to approve bindings while keeping a worker idle; a labelled test can then execute without enabling schedules. Disabled attached plans can be tested without replacement by a direct prompt. Interrupted session preparation resumes through existing V3/worktree checks; unsafe allocation recovery becomes an explicit failed receipt, preserving evidence rather than bypassing ownership. Failed wakes retain their original intent for retry.

Validation: focused Go worker/portable/migration/authority tests passed twice across storage, session, run and API packages; 15 SDK wire tests and SDK TypeScript check passed. Commands: the checkpoint-one Go command with `./internal/run` added and `-run '(Worker|Portable|LegacyMigration|LegacyAutomationV2Migration|OrdinaryChatAndSubagentsCannotManageWorkers|ScopedAutomationDisable)'`, plus the SDK commands above. No provider workload or live testbench result is claimed.

Remaining milestone gaps: activation intentionally supports one required primary workspace and no capability grants; unsupported combinations fail closed. Migrated legacy records remain read-only snapshots; safe live executor cutover is not implemented. The pre-session Git allocation crash window can fail safely rather than auto-reclaim a checkout. Hub approval UX, multiple-workspace support and live E01–E15 remain unverified. No cloud/S3 work, deployment, push or promotion.

### 3. Functional hub and one-message context

- [x] Wire hub cards, worker detail, daily history, sessions, deliverables, lifecycle controls and one-time chat selection to canonical state/actions.
- [x] Keep Orchestrator chat available; reconcile old worker views without maintaining a second authority.
- [x] Add focused behavior tests including failed stop, failed send, stale selection and refresh/reconnect.
- Exit: a user can oversee and control the full lifecycle from the hub, not merely view cards.

#### Step 3 implementation and verification

`worker-hub.tsx` now renders the canonical middle-panel hub through `desktop-workers` cache/runtime/actions. Worker updates and reconnect repair demanded pages without polling. Cards/detail expose durable lifecycle, definition revisions, approved primary bindings, attached plans and scoped controls, direct/test requests, receipt history and source-session links for deliverables. Idle binding approval is separate from activation; retrying an unchanged failed direct/test/create request retains its admission key. Stop requests remain visibly pending until acknowledged. The unreachable cloud/legacy fleet branch was removed; existing legacy detail routes and proposal review remain explicitly separate, not migrated execution.

`GET /v3/workers/{id}/summary?timezone=IANA&date=YYYY-MM-DD` computes daily admission/status counts and current work from durable receipts independently of history pagination. Desktop uses explicit UTC. Reads cap at 10,000 receipts/32 MiB with explicit partial-count reporting; active links cap at 100. Next scheduled time is an eligible slot from the existing scheduler calculation, not a dispatch guarantee.

The composer sends only `metadata.selected_worker: {worker_id, expected_revision}`. V3 message ingress resolves account authorization, compiled Orchestrator identity and current worker revision; foreign/stale/deleted/malformed references reject before append/run-intent mutation. Resolved context belongs to that message, not session instructions or worker dispatch. Failed sends retain selection; successful sends consume only the submitted reference. SDK history now consumes the backend `revisions` envelope while preserving its public `history` return value.

Observed focused checks: four backend regressions pass twice (selected-worker rejection/nonmutation/replay, receipt aggregation across pages and DST boundaries, scheduler eligibility); 19 Desktop checks pass including actual rendered composer/detail interactions, failed stops, stable retry keys, stale state and event/reconnect repair; 15 SDK worker transport tests pass. Desktop and SDK TypeScript checks pass. Styled component fixture screenshots were inspected, not the installed full routed application. No provider-backed run or live E01–E15 result is claimed. Representative commands:

```sh
(cd swarmd && GOMAXPROCS=2 go test -count=2 -p 1 -timeout 90s ./internal/api ./internal/run -run '^(TestSelectedWorkerMessageIngressAuthorizationAndReplay|TestWorkerAPI_SummaryReceiptsAndAuthorization|TestWorkerRunSummaryFallBackDay|TestNextWorkerScheduledAtEligibility)$')
(cd web && node --import tsx --test --test-concurrency=1 --test-timeout=30000 src/features/desktop/runtime/desktop-workers.spec.ts src/features/desktop/state/desktop-workers-api.spec.ts src/features/desktop/orchestrate/worker-message-context.spec.ts src/features/desktop/orchestrate/worker-hub.browser.spec.ts src/features/desktop/orchestrate/orchestrator-worker-composer.browser.spec.ts src/features/desktop/orchestrate/orchestrate-workers-dogfood.spec.ts)
```

At Step 3 closeout, full routed UI/live provider acceptance, capability/multiple-workspace support and legacy executor cutover remained gaps. Under the refined sequence, implementation gaps belong to Step 4 and live proof to Step 5. Deliverable links currently open retained source sessions; this is not proof of usable artifact presentation or Tasks escalation. External trigger credential provisioning and Orchestrator approval-path parity require Step 4 reconciliation, not an assumed complete contract. No push, host deployment or cloud work.

### 4. Finish the usable worker lifecycle — implementation, not a test-only checkpoint

**Meaning:** remove the known gaps so a user can complete the entire journey through Orchestrator, the hub or the SDK without switching to a legacy worker authority, pasting internal IDs, hand-authoring raw plan JSON, or losing outputs that need attention. Reuse what steps 1–3 built; do not rebuild the worker engine.

#### 4A. One worker from definition through approval and deployment

- [ ] Connect Orchestrator creation, proposal/review, binding approval and activation to the same durable worker ID and exact definition revision. Reconcile the legacy Automation V2 proposal path; do not approve an unrelated legacy object and present the new worker as deployed.
- [ ] Clearly separate create draft, approve bindings for an idle test, activate for normal work and attach/enable automation. A deployed worker with zero jobs stays available for direct tasks without a schedule or an authoring session.
- [ ] Show the human exactly what is being approved (instructions, workspaces, capabilities and enabled work). AI may prepare/propose but may not self-approve. Definition/binding changes that invalidate approval require fresh approval before affected execution.
- [ ] Keep identity through edits, authoring-chat archival, pause/resume and restart. Expose readiness, missing setup and lifecycle errors truthfully.
- Done when: the same worker is defined, reviewed and made ready in the hub and SDK, with no unsolicited run and no legacy review detour. Live proof: E01, E06, E10, E16.

#### 4B. Orchestrator can understand and manage the whole worker

- [ ] Complete a concrete tool/API/SDK/hub parity inventory: definition create/read/edit, import/export, approval proposal, activation status, automation attach/edit/remove, direct/test dispatch, trigger setup, runs, context, daily history, deliverables and stop/resume/archive/delete. Repair missing supported paths; remove misleading guidance rather than advertising unavailable actions.
- [ ] Let Orchestrator inspect effective worker context: standing instructions, pinned/current revisions, approved workspace roles, scoped workspace instruction sources, capabilities, attached plans and explicit attachments. Explain differences between a historical run and today's worker definition.
- [ ] Let it list related runs and read bounded, authorized prior execution sessions and deliverables via existing V3/session/artifact authorities. A source-session link alone is not sufficient for AI inspection. Missing/deleted/inaccessible records must be explicit; never enumerate unrelated or cross-account sessions or inject all history automatically.
- [ ] Preserve one-message selected-worker context, revision checks and ordinary-chat denial. Prior session/tool text is untrusted evidence, not authority to alter instructions, expand permission or deploy.
- Done when: in a fresh Orchestrator conversation, “what is this worker configured to do, what happened last time, and help me improve it” has an authorized inspection/action path without relying on the original authoring transcript. Live proof: E12, E13, E17.

#### 4C. A real dogfooding loop for manual and automated work

- [ ] Support the same-worker loop: define → approve → labelled test → inspect actual session/output/error → propose correction → approve if needed → test again → activate. Keep past revisions and receipts intact; do not replace the worker to retry.
- [ ] Manual workers need no trigger: “test this worker” and “send it this task” must work in-app and through Orchestrator/SDK. Testing an attached automation must exercise its actual plan/input contract without enabling its schedule.
- [ ] Finish authenticated external-trigger setup through maintained authorized APIs and SDK examples, including credential scope/revocation, safe handling and a clear local caller path. Do not place credentials in chat, portable definitions or tracked examples.
- [ ] Expose run progress, failure reasons and inspectable results; retry is an explicit new attempt linked to the prior run, while transport retries retain idempotency and cannot duplicate work.
- Done when: a user can diagnose and improve a non-trigger worker without leaving the app, and SDK callers exercise the same deployment/dispatch contract. Live proof: E02–E07, E18.

#### 4D. Make the hub an everyday control panel

- [ ] Replace internal workspace-ID entry with authorized workspace selection. Offer understandable worker/automation editing and Orchestrator-assisted plan authoring; raw JSON may remain an advanced option, not a prerequisite.
- [ ] Clearly distinguish worker-wide on/off from automation enable/disable, idle from paused, testing from normal runs and stopping from stopped. Keep pending/error states and confirmations; never show success before the stop barrier acknowledges it.
- [ ] Add date navigation and explicit timezone selection for daily activity, with running work, counts, failures, tests and paginated runs linked to sessions/results. Clearly label partial counts. A worker with no jobs has a useful empty state and direct/test actions.
- [ ] Use the canonical event-driven state and reconnect repair for cards, details and day views. Preserve readable loading/stale/error states rather than using polling or optimistic false success.
- Done when: users can find what ran on a chosen day, inspect what is running now and turn the correct scope off/on without internal IDs or hidden state. Live proof: E08, E09, E14.

#### 4E. Surface actionable outputs in Tasks

- [ ] Define the attention contract using existing task/review authorities: a requested human-review deliverable, approval/decision request or blocked/failed run that requires intervention produces an actionable Tasks item. Routine successful background work stays in history unless review was requested; do not flood Tasks with every run.
- [ ] Link each item to the exact worker, run, execution session and retained deliverable reference, with a readable reason and supported next action. Render/open the actual artifact or file through existing authorized surfaces; raw reference JSON is not the finished experience.
- [ ] Make escalation durable and idempotent across retries, refresh and restart. Project from canonical run/review state rather than introducing a second task database or inferring “needs attention” from prose alone.
- [ ] Acknowledgement, review/approval, resolution and retry must update the linked state through its existing authority. Acknowledging an item must not falsely mark a failed run successful. Preserve historical links after worker archival/deletion, subject to retention and authorization.
- Done when: a worker requiring a human decision appears once in Tasks, opens usable evidence and lets the user take the correct next action, with consistent state in the hub and Orchestrator. Live proof: E19, E20.

#### 4F. Close the remaining scope promises and safety gaps

- [ ] Implement the required/optional named workspace bindings already promised by E11, with isolated writable repositories and scoped instructions. Do not leave “one primary workspace only” as a footnote while declaring the existing multiple-workspace milestone complete.
- [ ] Resolve capability requests through the existing permission system for the supported local worker workflow, with visible grants/denials and least privilege. Define the supported set explicitly; unsupported capabilities must fail clearly. This is not authorization to invent a new grant system or give workers blanket access.
- [ ] Finish safe legacy executor cutover for supported migrated workers: preserve identity/history, prevent dual schedulers and reconcile retries/restarts. Unconvertible records remain explicitly inactive/read-only with a recovery explanation; never label a snapshot as a usable migrated worker.
- [ ] Review and repair lifecycle acknowledgement, failed dispatch/preparation, queued work and restart edges across all clients. Safe failure is acceptable where recovery cannot be proven; fabricated completion or unsafe worktree reclamation is not.
- Done when: the known unsupported milestone requirements are implemented or explicitly re-scoped by the user—not silently carried into a “complete” label. Live proof: E05, E08–E11, E13, E15.

#### Step 4 execution order and exit gate

Implement 4A first; 4B and 4C build on that single approval/identity path. Build 4D and 4E against the established context/run/review contract. Close 4F before Step 5. These are bounded work packages within Step 4, not permission to expand into cloud, custom system-agent infrastructure or a new executor.

- [ ] Add requirement-first focused regression tests as each gap is closed, including authorization, stale approvals, duplicate escalation, failed stop, unavailable history and safe retry. Keep atlas and test inventory synchronized. Moving live testing to Step 5 does not defer normal implementation checks.
- [ ] Reconcile every unchecked requirement above with concrete implementation and narrow test evidence. Publish a concise parity/gap table; no known missing part of the agreed journey may be described as “only live validation remains.”
- [ ] Commit a reviewed candidate with the checklist updated. Step 4 completion means implemented and ready for live acceptance, **not proven ready for daily use**. Do not deploy the host or push as part of this step.

### 5. Prove the complete journey on the real local testbench, then repair failures

- [ ] Build/deploy the reviewed committed candidate through the existing local two-slot systemd-nspawn pool only, after verifying live lane ownership. Preserve host Swarm, unrelated lanes, existing clips and credentials. Do not reset/rebuild the pool merely to test.
- [ ] Read maintained script help and current operational runner guidance; keep pool settings separate from provider credentials. Never substitute Docker, QEMU, remote/cloud or another testbench. If ownership/access is unavailable, report the exact blocker before alternatives.
- [ ] Verify actual test settings: Gemini 3.8 Flash, low thinking, without altering host settings or silently substituting a model.
- [ ] Run the acceptance matrix below with real daemon/provider sessions, the full routed Desktop app and an external SDK caller. Start with the manual-worker golden path: fresh Orchestrator → define zero-job worker → human approval → idle test → inspect actual output/prior session → revise/retest → activate/direct task → review in Tasks → pause/resume. Then exercise schedules, triggers, restart and migration. Fix failures and rerun affected scenarios on the exact final candidate.
- Exit: every required case has revision-bound evidence, or is explicitly failed/blocked. No synthetic workload, prewritten provider output or fabricated telemetry counts as proof.

## End-to-end acceptance matrix

Start the authoring cases in fresh Orchestrator conversations using normal user requests, not prebuilt tool arguments. Use bounded timeouts and a small explicit concurrency limit. Low-level tests supplement but do not replace these live cases.

| ID | Scenario | Required observable pass condition |
| --- | --- | --- |
| E01 | Create an idle specialist from zero | Orchestrator asks only necessary questions, creates an independent worker with no automation, and hub/SDK show the same ID and instructions. No unsolicited run. |
| E02 | Direct user and Orchestrator requests | User sends one task; Orchestrator sends another to the same worker. Distinct real runs/sessions and requested outputs link back to that worker. |
| E03 | Attached automation plans | Attach two plans with separate automation IDs; exercise interval and cron scheduling with real bounded occurrences. Editing/removing one does not change worker identity or the other plan; runs pin revisions. |
| E04 | External trigger from zero | Orchestrator guides creation and scoped credential setup. A process outside the daemon uses the SDK to send an authenticated event; one real execution yields the requested deliverable. No public listener required. |
| E05 | Trigger rejection and deduplication | Invalid credential, wrong worker/account, malformed payload and repeated idempotency key cause no unauthorized or duplicate run/state change; SDK exposes accurate errors/receipt. |
| E06 | SDK deployment and file reuse | SDK creates/activates locally using the canonical document, reads it in hub, exports/imports as a new idle identity, and explicitly updates an authorized existing identity. Unknown version/stale revision/collision reject without partial state. |
| E07 | Test run | An idle and a scheduled worker can each run one labelled test through the real path. No schedule is enabled or modified; outcomes, sessions and deliverables are inspectable. |
| E08 | Pause while active and queued | With actual active work plus queued work, pause from hub; admission closes, queue is invalidated and active work acknowledges cancellation. A racing trigger cannot start new work; failure to stop is not success. Repeat through Orchestrator/SDK. |
| E09 | Disable, archive, delete and resume | Disable one automation while another remains available. Archive/delete worker during active work obey stop barrier. Resume an eligible paused worker explicitly without catch-up surprises. No orphaned execution or false success. |
| E10 | Restart and identity durability | Restart only the owned candidate daemon with active/paused/idle cases. Same IDs, revisions, automation links and history survive; recovery neither duplicates admitted work nor resurrects stopped work. Archive the authoring chat and verify the worker remains usable. |
| E11 | Multiple workspaces and context | Required and optional role bindings behave as specified; missing required role fails before execution. Inspect actual context provenance and isolated writable repositories; no unauthorized workspace mutation or context leakage. |
| E12 | One-time selected-worker context | Select A, send an Orchestrator request about A, then send an unrelated message. Selection applies only to the first successful send; failed send retains chip, removal cancels it, stale/deleted selection fails visibly. |
| E13 | Ordinary chat cannot deploy | Ask ordinary chat to create/deploy/manage a worker; it directs to Orchestrator. Attempt a crafted disallowed tool call and verify backend denial and no worker/automation/run mutation. Authorized SDK access still works. |
| E14 | Hub, history and deliverables | Observe running/succeeded/failed/cancelled cases, daily counts in selected timezone, attached plans, trigger state, sessions and usable requested deliverables. Reload/reconnect preserves truth; failed reads/stops are visible. |
| E15 | Migration | Seed representative existing idle/scheduled/trigger records through supported fixtures, migrate and restart twice. Identity/history mappings remain stable; no duplicate workers or scheduled runs; unconvertible records fail visibly without activation. |
| E16 | One-object approval and zero-job deployment | From normal Orchestrator conversation, propose and approve the exact durable worker revision and bindings. Hub and SDK show the same ready worker with zero jobs and no unsolicited execution. A stale/changed proposal cannot activate; denial leaves the worker inactive. No legacy parallel worker is created. |
| E17 | Orchestrator context and prior-session inspection | A fresh Orchestrator conversation reads the selected worker's effective context and an authorized prior run's actual session/output, explains a failure and proposes a correction. Current and historical revisions stay distinguishable. Foreign/inaccessible history is rejected without disclosure; untrusted historical text cannot grant authority. |
| E18 | In-app dogfooding without a trigger | Test a manual worker, inspect the real result, revise its instructions, reapprove if necessary and retest the same ID. Both pinned attempts remain inspectable. A transport retry cannot duplicate a run; deliberate retry makes a distinct linked attempt. Repeat with an attached disabled automation without enabling its schedule. |
| E19 | Deliverable and attention escalation | A real run requests human review of its output; another requires intervention on failure/block. Each produces one actionable Tasks item with correct worker/run/session links, usable artifact and next action. Routine no-review success does not produce unnecessary Tasks. Hub/Tasks/Orchestrator agree on state. |
| E20 | Attention recovery and resolution | Reload/reconnect and restart the owned candidate around escalation; no lost or duplicate Tasks item. Resolve/acknowledge through the authorized existing review/task path and verify consistent linked state without rewriting the failed run as success. Worker archival retains authorized evidence; foreign-account access and stale/duplicate resolution cause no unauthorized mutation. |

### Evidence and completion rules

For each case record: candidate commit, daemon binary identity/PID, lane ownership, effective model, scenario/input, exact worker/automation/run/session links, observed postconditions, provider receipts where execution occurs, deliverable references and pass/fail/block reason. Keep private IDs, paths, logs and credentials outside public tracked documentation. Commit only sanitized result summaries and exact reproducible test commands.

Every new or changed test needs a written invariant/threat/production-boundary purpose, narrow assertions and negative cases. Report affected boundaries and validation evidence in the change handoff. UI screenshots must be inspected for actual rendering defects before making visual claims. No source-string assertion or HTTP 200 alone proves this lifecycle.

Final acceptance is all E01–E20 passing against the final candidate, with no unresolved agreed lifecycle feature or safety/authority/data-loss defect. Step 4 implementation completion and Step 5 live acceptance must be reported separately. If provider or infrastructure access blocks a scenario, label it blocked—not passed. No S3 or cloud deployment is needed to finish this milestone.

# V3-native plan definition and execution runtime contract

## Status and purpose

This document freezes the architecture contract for the new V3-native plan runtime before implementation. It is a design gate for a new keyspace and new domain model, not a refactor of the existing `SessionPlanDocument` execution path.

The runtime has two authorities:

1. **`PlanDefinition`**: immutable, revisioned intent and ordering.
2. **`PlanExecutionEvent` plus derived projections**: mutable execution history and current state.

The existing V3 execution-epoch subsystem remains the authority for bounded provider context and provider lineage. Plan execution events may carry an epoch-link request when a transition genuinely starts a fresh run, but the plan runtime does not replace, copy, or reinterpret epoch state.

## Confirmed execution-epoch invariants

### Durable segment identity and boundaries

The current implementation already divides a durable V3 session into explicitly named execution segments:

- `ExecutionEpoch` is defined as a bounded segment while root event and message sequences remain session-global (`swarmd/internal/store/pebble/execution_epoch.go:21-38`).
- Each epoch records stable `epoch_id`, ordinal, parent epoch, inclusive root-sequence bounds, lifecycle status, plan/checkpoint/attempt/run linkage, and provider policy (`execution_epoch.go:23-50`).
- Provider lineage state is fixed-size and deliberately excludes transcript text and ephemeral provider response IDs (`execution_epoch.go:69-95`).
- Boundary lookup is explicit and keyed by stable plan/checkpoint/attempt/reason identity, with run/source disambiguation only for the boundary kinds that need it (`execution_epoch.go:163-179`). No transcript parsing determines an epoch boundary.

### `BeginExecutionEpoch`

`BeginExecutionEpoch` has the following invariants:

1. It requires session ID, client request ID, and payload hash, runs under the per-session mutation lock, and checks operation-scoped idempotency before creating state (`execution_epoch.go:423-505`). Reuse of a request ID with a different hash conflicts.
2. A fresh begin reads the root high-water sequence and the active/latest epoch directly. It seals the predecessor at that exact high water and creates the successor at the next sequence (`execution_epoch.go:508-565`).
3. If an old session has no epoch record, the one-time `ExecutionEpochLegacyPrefix` captures only fixed-point metadata—last root sequence, projection high water, active legacy plan ID, and lifecycle generation—without scanning transcript history (`execution_epoch.go:53-59, 537-546, 798-817`).
4. Boundary identity and ordinal collisions fail; they do not silently reuse or overwrite a different epoch (`execution_epoch.go:566-597`).
5. The predecessor, successor, boundary event, session projection/high water, realtime outbox record and indexes, idempotency result, root sequence, optional trigger message/event/outbox, and linked run intent are written in one Pebble batch (`execution_epoch.go:598-775`). The batch commits with `pebble.Sync` (`execution_epoch.go:781-795`).
6. When a run intent is linked, its epoch, plan, checkpoint, attempt, run-session, parent-session, resume flag, event sequence, point row, status index, and active run state are updated in that same durable batch (`execution_epoch.go:735-775`). The durable `V3SessionRunIntent` fields that preserve this linkage are declared in `session_event_store.go:325-346`.
7. Idempotent replay validates that the stored epoch, event, projection, predecessor, outbox, and optional trigger still agree before returning the compact prior result (`execution_epoch.go:438-503`).

The store’s `NewBatch` is Pebble `DB.NewBatch`, which is a write-only/unindexed batch (`swarmd/internal/store/pebble/store.go:104-105`; Pebble v1.1.5 `db.go`, `NewBatch`). Pebble documents `NewIndexedBatch` as slower and necessary only for reads from both batch and DB. This matches the required mutation shape: perform validation reads first, then construct blind writes. Pebble `Batch.Commit` applies the batch to its parent DB; `pebble.Sync` requests synchronization through the OS cache to disk. Therefore the compact execution event, affected projections, summary, idempotency row, outbox record, and genuine epoch/run linkage must share one unindexed `Sync` batch.

### `SealExecutionEpoch`

`SealExecutionEpoch`:

- names both session and epoch, preventing a delayed worker from sealing a newer active epoch (`execution_epoch.go:131-136`);
- holds the per-session mutation lock and requires the named epoch to be the active index (`execution_epoch.go:341-349`);
- records the current durable root high water as the inclusive `LastRootSeq` and never infers a boundary from message content (`execution_epoch.go:330-364`); and
- atomically writes the sealed epoch/latest index and removes the active index using `pebble.Sync` (`execution_epoch.go:365-376`).

The number of keys and encoded bytes changed by sealing is independent of session history and plan size.

### Named-range recovery and provider-context construction

`ListExecutionEpochMessages` reads the explicitly named epoch and its inclusive message range from one Pebble snapshot. The shared snapshot prevents a delayed sealed-epoch reader from seeing later root writes (`execution_epoch.go:299-327`). Invalid or missing durable bounds fail explicitly.

Provider context is rooted in that named epoch:

- the executor resolves `epoch_id` from the job or its durable run intent and fails if no epoch exists (`swarmd/internal/api/sessions_v3_executor.go:2891-2908`);
- it reads only that epoch range; resume may explicitly use the immediately preceding named epoch when the current epoch has no usable provider input (`sessions_v3_executor.go:2908-2951`);
- final provider input is compacted to at most 500 message records after the latest explicit compaction checkpoint (`sessions_v3_executor.go:2936`; `swarmd/internal/run/service.go:2779-2827`); and
- lineage conversion does not infer a second boundary from transcript text or metadata because messages are already epoch-bounded (`sessions_v3_executor.go:3433-3472`).

Provider request construction also requires a declared, valid `ExecutionEpochLifecycleRunner`, derives context branch/cache/affinity lineage from `(session, epoch, provider configuration)`, and only permits native continuation when the stored configuration and lineage still match (`sessions_v3_executor.go:1568-1641`).

### Precise caveats

“Epoch-bounded” is not a claim of literal infinite storage or constant provider-recovery work:

- Durable session events, messages, and sealed epochs are retained; total disk use still grows with retained history.
- `ListExecutionEpochMessages(..., limit=0)` currently materializes the entire named epoch before provider compaction keeps at most 500 records. Recovery cost is therefore `O(messages in selected epoch)`, not `O(all session history)`, and a long active epoch can still be large.
- Resume may read one explicitly named parent epoch, so its read bound is the current/parent epoch content, never an unbounded scan over every ancestor.
- The 500-record cap bounds record count, not encoded bytes or tokens; individual message size remains relevant. Reactive compaction and model context limits remain necessary.
- Epoch creation persists immutable history and realtime outbox records. The mutation work is bounded, but retained storage is intentionally cumulative.
- Existing provider-context boundary helpers can synthesize summaries from the legacy active plan snapshot (`sessions_v3_executor.go:2918-2934`). The new runtime must replace that dependency at cutover with a compact execution-summary read; it must not put a complete definition/projection into provider history.

These caveats do not justify changing the epoch subsystem. The plan runtime defect is the routine execution update’s dependence on complete plan documents, revisions, and provider tool-history payloads; epoch boundaries already isolate provider context from unrelated earlier session history.

## New domain model

The names below describe new types in a dedicated plan-runtime package. They must not embed, alias, accept, return, or serialize `SessionPlanDocument`, `SessionPlanSnapshot`, `PlanDocumentPatch`, or any legacy execution-bearing type. They must not treat a positional `Tasks []string` value as identity or execution state.

### Immutable definition types

```text
PlanDefinition {
  schema_version
  session_id, plan_id
  definition_revision
  parent_definition_revision
  content_hash
  title, goal, scope
  continuation_default
  checkpoint_order[]CheckpointID
  created_at, created_by
}

CheckpointDefinition {
  session_id, plan_id, definition_revision, checkpoint_id
  order, title, objective, notes
  acceptance_criteria[]Criterion
  subtask_order[]SubtaskID
  next_checkpoint_id
}

SubtaskDefinition {
  session_id, plan_id, definition_revision, checkpoint_id, subtask_id
  order, title, notes
  next_subtask_id
}
```

`CheckpointID` and `SubtaskID` are non-empty stable opaque strings. Order is presentation/next-selection metadata, never identity. `Criterion` is definition content with its own stable ID and text. There is no definition status, active checkpoint, attempt, run, epoch, report, review, timestamped progress, or execution sequence.

A definition revision is immutable after commit. The logical `PlanDefinition` is physically normalized into a revision header, checkpoint point rows, subtask point rows, and immutable order indexes. A routine execution command reads only its revision header and directly addressed target rows; it never decodes a complete definition blob. Definition creation/revision may scale with definition size because it is not a routine execution mutation.

### Execution authority types

```text
PlanExecutionEvent {
  schema_version
  session_id, plan_id
  execution_seq
  event_id, event_type
  definition_revision
  client_request_id, payload_hash
  checkpoint_id?
  subtask_ids[]?
  result_delta
  actor_id
  occurred_at
}

PlanExecutionSummary {
  schema_version
  session_id, plan_id
  definition_revision
  execution_seq
  status                       // idle|in_progress|paused|waiting_review|blocked|failed|completed
  active_checkpoint_id?
  next_checkpoint_id?
  active_attempt_id?
  continuation_mode            // automatic|review_each_checkpoint
  pause_after_current
  completed_checkpoint_count
  blocked_reason_code?
  updated_at
}

CheckpointExecution {
  schema_version
  session_id, plan_id, checkpoint_id
  execution_seq
  status                       // pending|in_progress|paused|needs_review|completed|blocked|failed
  attempt_number
  active_attempt_id?
  active_subtask_id?
  next_subtask_id?
  run_id?, epoch_id?
  run_session_id?, parent_session_id?
  started_at?, terminal_at?
  outcome_code?
  evidence_ref?
  review_state?                // none|pending|approved|rejected
}

SubtaskExecution {
  schema_version
  session_id, plan_id, checkpoint_id, subtask_id
  execution_seq
  status                       // pending|in_progress|completed
  attempt_id?
  started_at?, completed_at?
}

PlanExecutionSnapshot {
  schema_version, projection_schema_version
  session_id, plan_id, definition_revision
  execution_seq
  summary
  checkpoint_executions[]
  subtask_executions[]
  created_at, content_hash
}
```

Events are the immutable execution authority. Summary/checkpoint/subtask rows and snapshots are deterministic, rebuildable materializations. Missing checkpoint/subtask execution rows mean `pending` under the referenced immutable definition; the store does not prewrite a pending row for every definition item. A snapshot contains execution projection only and never embeds a plan definition.

`result_delta` is an event-specific compact value, not an untyped full projection. Evidence larger than the event budget is stored once under an immutable content-addressed `evidence_ref`; event, realtime, tool, and provider-history records carry only the reference and a bounded summary.

### Idempotent command and result contract

Every mutating command has this common envelope:

```text
PlanExecutionCommand {
  schema_version
  session_id, plan_id
  expected_execution_seq
  client_request_id
  payload_hash                 // hash of canonical command body excluding transport metadata
  actor_id
  definition_revision
  command_type
  command_payload              // event-specific stable target IDs and bounded evidence
}

PlanExecutionMutationResult {
  session_id, plan_id
  execution_seq
  event_id, event_type
  replayed
  checkpoint_change?
  subtask_changes[]
  summary_change
  run_epoch_link?
  next_action                  // none|start_checkpoint|await_review|resume|resolve_block|retry
}
```

Contract:

1. `expected_execution_seq` is mandatory (`0` only for activation/import). Under the per-session mutation lock, the store first checks the operation-scoped idempotency row, then reads the current fixed-size summary and directly named definition/projection rows.
2. If `(account_scope, session, plan, client_request_id)` exists with the same payload hash, return its stored compact result without appending an event. A different hash is an idempotency conflict.
3. For a new request, current sequence must equal `expected_execution_seq`; otherwise return an explicit conflict containing only current sequence and compact summary status.
4. One command appends exactly one allowed `PlanExecutionEvent`. A command may change several explicitly named subtask rows (maximum 64) and at most one checkpoint row because those records are one bounded delta. It never smuggles a definition replacement or unrelated target update into the same event.
5. Event, affected projection rows, summary/high water, idempotency result, compact realtime outbox event/indexes, and any genuine run/epoch linkage are one unindexed Pebble `Sync` batch. Reads happen before batch construction; no read-your-writes is required.
6. The result schema is the same for first execution and replay. It never contains `PlanDefinition`, `PlanExecutionSnapshot`, all checkpoint rows, or all subtask rows.

## Command, event, and deterministic transition contract

### Allowed event types

No arbitrary event type is accepted. The initial schema permits only:

| Command | One appended event | Stable targets |
| --- | --- | --- |
| `ActivatePlan` | `plan.execution_activated` | plan ID, definition revision |
| `StartCheckpoint` | `plan.checkpoint_started` | checkpoint ID, attempt ID, optional run/epoch IDs |
| `RecordCheckpointOutcome` | `plan.checkpoint_outcome_recorded` | checkpoint ID, attempt ID, outcome enum |
| `RestartCheckpoint` | `plan.checkpoint_restarted` | checkpoint ID, prior/new attempt IDs, optional run/epoch IDs |
| `FocusSubtask` | `plan.subtask_focused` | checkpoint ID, subtask ID |
| `CompleteSubtasks` | `plan.subtasks_completed` | checkpoint ID, 1-64 subtask IDs, optional next focus and optional checkpoint outcome |
| `PauseExecution` | `plan.execution_paused` | plan ID and optional active checkpoint ID |
| `ResumeExecution` | `plan.execution_resumed` | plan ID |
| `RequestCheckpointReview` | `plan.checkpoint_review_requested` | checkpoint ID, attempt ID |
| `RecordCheckpointReview` | `plan.checkpoint_review_recorded` | checkpoint ID, review decision |
| `ImportLegacyActivePlan` | `plan.legacy_execution_imported` | plan ID, immutable import manifest hash |

Definition creation/revision events belong to the definition service and are not execution events. A requirement-changing restart must first commit an immutable definition revision through that service, then issue `RestartCheckpoint` referencing that exact revision and stable checkpoint ID. There is no execution command that accepts a document patch.

`CompleteSubtasks` supports the existing atomic “complete final subtasks and checkpoint” behavior: the single event lists the changed subtask IDs and an optional terminal checkpoint outcome. This remains delta-sized; it does not imply completion of omitted subtasks. The command validates that every required subtask is already complete or named in the event.

### Plan-level transition table

| Command/event | Required source | Resulting summary | Forbidden conditions |
| --- | --- | --- | --- |
| activate/import | no new-runtime summary, expected seq `0` | `idle`, seq `1`, next first checkpoint | existing summary; incompatible definition/import |
| checkpoint started/restarted | `idle` or `paused` with named eligible checkpoint | `in_progress`, active checkpoint/attempt set | another active checkpoint/run; target not pending/restartable |
| checkpoint completed | `in_progress`, matching active attempt | `completed` if no next checkpoint; otherwise `idle` or `paused` when pause/review policy applies, with next ID | stale attempt; incomplete required subtasks |
| checkpoint review requested | `in_progress`, matching active attempt | `waiting_review`, active checkpoint retained | no active checkpoint; stale attempt |
| checkpoint blocked | `in_progress`, matching active attempt | `blocked`, active checkpoint retained | empty reason code; stale attempt |
| checkpoint failed | `in_progress`, matching active attempt | `failed`, active checkpoint retained | empty failure code; stale attempt |
| checkpoint paused/stopped | `in_progress`, matching active attempt | `paused`, active checkpoint retained and restartable | stale attempt |
| pause execution | `idle` or `in_progress` | `paused` immediately when idle; while running keep `in_progress` and set `pause_after_current=true` | completed/blocked/failed/waiting review |
| resume execution | `paused` with no active run | `idle`, pause flag cleared, next ID retained | active run; blocked/failed/waiting review/completed |
| review approved | `waiting_review`, target `needs_review` | target completed; summary `completed`, `idle`, or policy-paused | stale/missing review |
| review rejected | `waiting_review`, target `needs_review` | target `paused`, summary `paused`; explicit restart required | stale/missing review |

Automatic continuation is orchestration, not an implicit projection mutation. A terminal event leaves the next checkpoint identified in an `idle` summary; the dispatcher subsequently issues an explicit `StartCheckpoint`, producing its own event and genuine epoch/run linkage. A crash between those commits is recoverable by redispatching from `idle + next_checkpoint_id`; no target is half-started.

### Checkpoint transition table

| Event | Source checkpoint | Result checkpoint |
| --- | --- | --- |
| activated/imported (implicit absence) | no row | pending (derived; no row required) |
| `checkpoint_started` | pending | in_progress, attempt 1 |
| `checkpoint_restarted` | paused, blocked, failed, or needs_review/rejected | in_progress, attempt incremented, old attempt immutable |
| `checkpoint_outcome_recorded(completed)` | in_progress, matching attempt | completed |
| `checkpoint_outcome_recorded(paused)` | in_progress, matching attempt | paused |
| `checkpoint_outcome_recorded(blocked)` | in_progress, matching attempt | blocked |
| `checkpoint_outcome_recorded(failed)` | in_progress, matching attempt | failed |
| `checkpoint_review_requested` | in_progress, matching attempt | needs_review, review pending |
| `checkpoint_review_recorded(approved)` | needs_review | completed, review approved |
| `checkpoint_review_recorded(rejected)` | needs_review | paused, review rejected |

Completed checkpoints are immutable terminal records. Rewinding a completed checkpoint is not a routine transition: it requires a new definition revision or explicit future versioned `execution.rewound` command and is outside the initial event set. Block resolution without retry is represented by a new terminal outcome only if the checkpoint is still in progress; a blocked checkpoint otherwise restarts with a new attempt. No command overwrites the prior attempt.

### Subtask transition table

| Event | Required source | Result |
| --- | --- | --- |
| `subtask_focused` | checkpoint in progress; target pending or already active | target in progress; prior active, if any, returns to pending |
| `subtasks_completed` | checkpoint in progress; every target pending or in progress | named targets completed; optional named next target in progress |
| `checkpoint_restarted` | checkpoint restartable | completed subtasks remain completed by default; pending/in-progress subtasks become pending; explicit restart mode may reset named stable IDs only |

A completed subtask cannot return to pending through focus or completion. Batch completion rejects duplicate IDs, unknown IDs, cross-checkpoint IDs, more than 64 IDs, and any ID not named directly in the immutable definition revision. Selection of “next” uses `CheckpointExecution.next_subtask_id` plus immutable `next_subtask_id` links; it never scans unrelated definition rows or synthesizes IDs from position.

## One-time cutover/import policy

The selected policy is **eager, idempotent import at the atomic product cutover**. Existing active legacy plans do not finish on the old execution path, and the product does not choose per-session fallback.

1. Before enabling new-runtime writes, the cutover migrator enumerates each legacy active plan under the same exclusive maintenance/startup gate that prevents plan mutations. It validates the complete legacy record once.
2. It writes a new immutable `PlanDefinition` revision with freshly allocated stable checkpoint, criterion, and subtask IDs. Legacy task-array positions are used only as one-time source ordering during import; generated IDs are recorded in an immutable import manifest keyed by `(session, legacy_plan_id, legacy_revision_hash)` and are never recomputed at runtime.
3. It converts current legacy execution state into one `ImportLegacyActivePlan` command/event at execution sequence `1`, writes only the non-pending checkpoint/subtask projection rows, the summary, the import idempotency record, and compact realtime state. The import manifest records the source hash and target definition/execution IDs.
4. Import and pointer switch for a session are one `pebble.Sync` batch. A session’s writable plan-runtime authority becomes the new pointer only if definition, import event, projections, summary, manifest, and pointer all commit. A crash before commit leaves only the legacy authority; rerun uses source hash/idempotency and is deterministic.
5. After every active plan imports successfully, the release flips the global runtime schema/capability marker and enables new commands. The same release removes the legacy execution-update route and client dispatch. Legacy records remain read-only historical data; no old execution mutation API remains reachable.
6. If any active plan is invalid or cannot be represented, cutover fails loudly and leaves the global marker unchanged. Operators repair or explicitly archive that plan and rerun. There is no per-session “try new, fall back old,” no dual write, and no background period with competing authorities.
7. Stale clients receive an explicit unsupported-schema/conflict response instructing refresh. They do not invoke `PatchPlan`, `SavePlanWithMetadata`, `session.plan.saved`, or positional task synthesis.

The migrator is temporary product code. After the supported upgrade window and evidence that no pre-cutover active records remain, remove it in a separate explicit schema retirement; do not keep it as a runtime fallback.

## Immutable event retention and snapshot policy

The selected policy is **retain execution events immutably for the lifetime of the plan; snapshots accelerate reads but never authorize pruning**.

- Every accepted execution event remains under its `(session, plan, execution_seq)` key until the user deletes the plan/session through the canonical deletion flow or a future, separately approved archival product contract moves the complete immutable stream.
- Materialized summary/checkpoint/subtask rows and `PlanExecutionSnapshot` records are rebuildable caches. Corruption or incompatible snapshot schema causes a rebuild from the latest compatible snapshot plus event tail, or from sequence 1 when none exists. It never causes fallback to legacy execution fields.
- Snapshot creation is asynchronous, content-hashed, and published by atomically updating a compatible-snapshot pointer only after the complete snapshot is durable. Routine mutation commits do not encode or rewrite snapshots.
- Keep the newest two verified compatible snapshots plus one previous-schema snapshot during upgrades; older snapshot blobs may be deleted because they are caches. Execution events are not deleted by snapshot cleanup.
- Tool history and provider transcript are not the audit store. They receive only bounded mutation receipts; immutable events remain in Pebble and are fetched through dedicated plan-runtime reads.
- Session/plan deletion must remove definition, execution-event, projection, snapshot, idempotency, import-manifest, and outbox indexes through the canonical durable deletion contract. There is no TTL that silently discards an active plan’s event history.

This policy intentionally permits cumulative disk growth; it avoids an audit-destroying compaction policy before product requirements exist. The complexity contract below bounds hot-path and recovery work, not total retained bytes.

## Complexity and payload budgets

### Symbols

- `D`: complete plan-definition size (all checkpoints/subtasks/criteria).
- `H`: total session message/event history.
- `E`: prior plan execution-event count.
- `k`: explicitly changed targets in one command (`1 <= k <= 64`; normally `1`).
- `e`: bounded event-specific evidence bytes after normalization.
- `T`: events after the selected snapshot watermark.
- `P`: number of materialized non-default checkpoint/subtask rows returned by full hydrate.

IDs, titles/summaries, reason codes, and inline evidence have limits: ID 128 bytes, title/summary 512 bytes, reason code 64 bytes, inline evidence 4 KiB. Larger evidence must use a content-addressed reference. Arrays have explicit maxima; `CompleteSubtasks` is capped at 64 target IDs.

### Routine mutation budgets

For every execution command after activation/import:

| Measure | Required bound |
| --- | --- |
| Validation reads | at most 1 idempotency row + 1 summary + 1 checkpoint definition/projection + `k` subtask definition/projection point reads + optional 1 run/epoch point read; no prefix/history scan |
| CPU and allocations | `O(k + e)`, independent of `D`, `H`, and `E` |
| Keys written | base <= 12 keys plus <= 2 keys per changed target and documented run/epoch linkage keys; hard maximum 144 at `k=64` |
| Logical values serialized | one event, one summary, one idempotency result, one compact outbox envelope, one checkpoint row, `k` subtask rows, optional run/epoch values |
| Total routine plan-runtime batch bytes | <= 32 KiB for ordinary `k=1`; <= 128 KiB hard limit for `k=64`; reject before commit if exceeded |
| Event bytes | <= 8 KiB ordinary; <= 48 KiB hard limit for batched IDs/evidence reference |
| Realtime outbox envelope | <= 12 KiB ordinary; <= 64 KiB hard limit |
| Tool/API mutation result | <= 8 KiB ordinary; <= 32 KiB hard limit |
| Provider tool-history representation | <= 4 KiB summary and never a definition/snapshot; execution-only internal calls should be omitted from provider transcript where protocol permits |
| Commits/fsyncs | exactly one unindexed Pebble batch committed with `pebble.Sync` per accepted non-replay command; zero writes for an idempotent replay |

A routine mutation must report instrumentation for keys, event/projection/outbox/result bytes, commit duration, conflict/replay, and changed-target count. Focused benchmarks compare a fixed delta across `D`, `H`, and `E` scale factors. The acceptance threshold is: identical key counts; no serialized full-definition/history bytes; and p50 bytes/allocations within 10% plus fixed allocator noise between smallest and largest unrelated-data fixtures.

### Read and recovery budgets

| Path | Required bound |
| --- | --- |
| Current summary | one point read; `O(1)` bytes |
| One checkpoint/subtask state | definition and projection point reads; `O(1)` per named target |
| Incremental execution read | ordered range from caller-supplied sequence, explicit page max 256 events or 1 MiB, whichever comes first; opaque continuation cursor |
| Realtime replay | compact outbox range with explicit page/cursor limits; no embedded full projection/definition |
| Current hydrate | definition header/order metadata plus paged definition rows and materialized execution rows; `O(D + P)` only when caller explicitly asks for full plan view, with 512-record/1 MiB pages |
| Recovery | latest compatible snapshot point lookup/read plus ordered tail; `O(snapshot bytes + T)`, never `O(H)` and never a scan of legacy plan revisions |
| Snapshot cadence | create asynchronously when `T >= 256` or tail encoded bytes >= 1 MiB; never on the routine mutation critical path |
| Provider context | unchanged named epoch range plus explicit 500-record post-read cap; no plan execution stream scan and no full plan in tool output |

Recovery must expose tail length, tail bytes, snapshot sequence/schema/age, and rebuild reason. A missing/incompatible snapshot is an observable slow-path rebuild from immutable execution events, not a successful legacy fallback. Full hydrate is allowed to scale with the plan view requested; routine mutation, compact receipts, realtime deltas, summary reads, and provider history are not.

## Explicit non-goals and prohibitions

- Do not add methods to `SessionPlanDocument`, `SessionPlanSnapshot`, or `PlanDocumentPatch` for the new execution stream.
- Do not alias legacy structs to new names or convert back to a complete legacy document on every command.
- Do not publish `session.plan.saved` for execution progress or create definition/history revisions from progress.
- Do not make legacy `Tasks []string` or array indexes authoritative IDs.
- Do not dual-write, shadow-write, compare-and-fallback, or keep a compatibility execution route after cutover.
- Do not put complete definitions, complete projections, snapshots, or unbounded reports in events, outbox, mutation results, provider tool messages, or durable provider transcript.
- Do not create or seal an execution epoch for passive progress. Only transitions that genuinely create a provider run call the existing epoch/run-intent subsystem, and that linkage must be in the same atomic mutation flow.

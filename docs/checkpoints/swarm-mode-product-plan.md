# Swarm mode: product and implementation plan

Status: proposed

Starting evidence: `docs/checkpoints/swarm-burst-architecture.md`

## 1. Decision

Build **Swarm mode** as a dedicated bulk-artifact generation flow, separate from Plan mode and the `task` tool.

**One-sentence product promise:** Swarm mode turns one creative brief into many independent, reviewable artifacts in parallel, then saves the validated results as an organized collection for the user to compare, combine, or promote.

The launch MVP is **Designer-only** in product language: it generates design-oriented text, structured objects, and isolated code/artifact bundles. It does not launch hundreds of Designer child sessions. Internally it uses bounded, tool-free generation workers and one trusted materializer.

The user-facing mode and backend primitive should both be called **Swarm**, not Burst. “Burst” may remain an internal scheduler term if useful, but it should not appear as a competing product name.

## 2. What users can ask Swarm mode to do

Swarm mode is appropriate when the work can be expressed as one brief, one typed output contract, and many mostly independent results.

Good launch examples:

- “Use Swarm mode to create 100 low-poly objects for a 3D alien landscape.”
- “Generate 60 icon concepts, each as its own SVG.”
- “Create 40 procedural tree definitions using this JSON schema.”
- “Give me 80 independent card-layout variants under a dedicated variants folder.”
- “Make 200 prop descriptions with tags, dimensions, palettes, and assembly anchors.”
- “Generate 25 isolated Three.js object modules that implement this fixed interface.”

Later adapter examples:

- “Generate 100 product-photo directions, then render the best 20.”
- “Create 50 texture images with a shared art direction.”
- “Produce a set of sound-effect or video candidates.”

Inappropriate requests:

- “Implement authentication across the backend and frontend.” This is dependent engineering work for Auto/Plan mode and possibly a small Coder wave.
- “Investigate five unrelated production incidents.” This is autonomous research for Finder tasks, not homogeneous artifact generation.
- “Have 500 Coders modify the same application.” Shared integration and hundreds of worktrees are explicitly unsupported.
- “Make a step-by-step migration plan.” Plan mode owns ordered reasoning and checkpoints; Swarm items are not plan steps.
- “Pick one design and integrate it into production.” Swarm can generate and shortlist candidates; canonical integration is a later parent or Coder operation.
- “Generate arbitrary files anywhere in my repository.” Swarm writes only beneath one approved job output root.

If a request mixes generation and integration, Swarm mode completes the generation collection first. Promotion or integration is a separate normal workflow with its own review boundary.

## 3. Plain-language distinction between existing concepts

| Product concept | Promise | Unit of work | Durability | Write model |
| --- | --- | --- | --- | --- |
| **Auto mode** | Let Swarm carry out one cohesive task | One parent run | V3 session/run | Normal permissioned tools |
| **Plan mode** | Design and execute ordered dependent checkpoints | Checkpoint | Durable V3 plan state | Per-checkpoint normal tools |
| **`task` delegation** | Give a small number of autonomous specialists distinct assignments | Finder/Coder/Designer child session | Durable V3 child session and lineage | Finder read-only; Coder isolated worktree; Designer distinct shared-checkout target |
| **Swarm mode** | Produce many independent variants or objects from one brief | Swarm item inside one Swarm job | One durable job plus item pages | Tool-free workers; one trusted materializer under one output root |

Required terminology:

- **Swarm mode**: the user-selected bulk-generation flow.
- **Swarm job**: one durable approved generation run.
- **Brief**: immutable creative direction and constraints for the job.
- **Item**: one requested output. This is what the progress UI counts.
- **Generator**: a typed adapter such as `structured-3d-object` or `code-bundle`.
- **Worker**: an internal provider request. Do not present workers as agents.
- **Collection**: the materialized manifest and output directory.
- **Shortlist**: user-selected items for later composition or promotion.

Do not say “500 agents” unless 500 real durable child sessions exist. The honest phrase is “Swarm is generating 500 items” or “one creative director coordinating 500 generations.”

## 4. Boundary and cap for the existing `task` tool

### Launch rule

Adopt a server-enforced product hard cap of **25 child launches in one `task` call**. This is an upper bound, not a recommended team size: ordinary delegation remains easiest to understand as a small specialist team, while calls approaching 25 create substantial durable session, transcript, synthesis, and worktree overhead.

Keep these controls independent:

- **25 launches per call** is the product/API maximum and must be checked before any reservation;
- the account’s automatic-wave budget controls how many approval-free `task` calls a parent run may make;
- the account’s active-child limit controls concurrent durable children and may reject or defer a call below 25;
- the existing `MaxSubagentWaveSize = 256` remains a backend validation safety ceiling, not a user-facing limit or product promise (`swarmd/internal/permission/policy.go`);
- durable reservation remains atomic: validate the 25-call maximum first, then reserve the exact accepted wave against current account limits without partial launches.

The task schema and server handler must expose the same 25-launch maximum. Account policy may lower effective capacity but may never raise one call above 25, and clients must not reinterpret the 256 safety ceiling as available capacity.

### Redirect rule

Before launching `task`, classify the request:

1. If it requests **26 or more** autonomous children, reject the call before reservation. Explain that 25 is the hard per-call maximum and identify the appropriate alternative.
2. If the assignments are homogeneous variants sharing one output schema, recommend Swarm mode at any count; for a large homogeneous request, redirect rather than creating heavyweight child sessions.
3. If the work needs separate transcripts, tools, independent research, code integration, or child-specific judgment, keep it on `task` with at most 25 launches in the call.
4. For **9–25 launches**, show a non-blocking warning before approval/launch: this is a large autonomous-agent wave, review and synthesis become increasingly unwieldy, and Swarm mode is usually better when the requested outputs are homogeneous.
5. Do not silently split a request above 25 into several `task` calls. Splitting evades the product boundary and still creates the heavyweight state the cap is intended to prevent.
6. Preserve the normal atomic reservation path after classification. A call accepted by the 25-launch product check may still be rejected or constrained by the current automatic-wave budget or active-child limit; never partially launch it while presenting the full wave as accepted.
7. If the request is high-fanout but no Swarm generator supports its output kind, say that it is not yet supported and ask the user to narrow it; do not misroute it to Designer generation.

Suggested error copy:

> `task` supports at most 25 autonomous agents in one call. Large task waves are difficult to review and synthesize. If these are many independent outputs from one shared brief, use Swarm mode to generate and review them as a collection instead.

Settings should describe the two independent controls honestly:

- **Automatic task waves per parent run**: approval-free task calls.
- **Active child agents**: simultaneous durable task children across calls, independently bounded by account policy and backend safety limits.

Do not call the first value “launches”; the current field name may remain for compatibility while UI/API documentation is corrected.

## 5. How it works, step by step

First choose the right mechanism:

- Use ordinary **`task` delegation** when each worker must behave as an autonomous specialist: it needs its own instructions, durable child session and transcript, possibly tools or a Coder worktree, and an individual report for the parent to synthesize. One call may contain at most 25 launches, and 9–25 should carry the large-wave warning.
- Use **Swarm mode** when one creative direction should produce many independent items with the same typed contract. Items are lightweight generations inside one durable job—not child agents, chats, or worktrees—so the system can schedule, validate, review, and recover hundreds of them efficiently.

The end-to-end Swarm mode flow is:

1. **Ask for a collection.** The user selects Swarm mode or says, for example, “Use Swarm mode to generate 100 alien-landscape objects.” Natural-language detection may suggest the mode, but must not start costly generation automatically.
2. **Create the brief.** The main Swarm model acts as creative director and converts the request into an immutable typed `CreativeBrief`: output kind, shared invariants, diversity axes, references, forbidden patterns, evaluation rubric, count, and approved output root. This preserves the main agent’s taste without writing a step-by-step plan.
3. **Create item specifications.** A few bounded, tool-free expander calls turn the brief into distinct `SwarmVariantSpec` records. Each record defines one item’s seed, variation choices, generation prompt, and output contract. The server rejects duplicate IDs, duplicate destinations, malformed specs, and count overflow before generation.
4. **Estimate and approve.** The server checks generator and provider support, model availability, count tier, concurrency, rate and retry budgets, expected calls/cost, destination, and overwrite policy. The user approves the exact brief digest and limits.
5. **Generate the items.** A bounded scheduler sends the item specifications to tool-free generation workers. Workers return typed data or owned asset references only; they cannot read the repository, invoke tools, use Git, or write files. Provider concurrency adapts to throttling while preserving capacity for interactive chat.
6. **Validate every result.** Adapter-specific validators check schema, paths, extensions, bytes, content limits, duplicates, and other safety rules. Invalid items fail or retry independently; they never become writable merely because a model or judge rated them highly.
7. **Materialize safely.** One trusted materializer writes only validated items beneath the approved job root, each in an isolated destination. It enforces containment and collision rules, uses atomic publication where possible, records hashes, and advances a durable collection manifest. Generation workers never perform parallel shared-file writes.
8. **Review the gallery.** The UI presents durable progress plus a virtualized gallery or table with previews, failures, duplicates, diversity coverage, clusters, and explicit partial-success counts. The parent model receives bounded summaries rather than every full item.
9. **Shortlist.** The user compares candidates and saves selected item IDs in a durable shortlist. Shortlisting does not mutate canonical application files.
10. **Promote separately.** A later Auto, Plan, or Coder workflow consumes the shortlist and deliberately composes or integrates selected outputs into the product. This is a separate review and permission boundary; Swarm mode itself produces the collection, not the final shared-file integration.

For the 3D-landscape case, the first job produces independently loadable object definitions or modules plus metadata. A deterministic composition stage can place objects by declared bounds, anchors, tags, biome, seed, and density rules. The main application is not modified by 500 concurrent writers.

## 6. Launch scope and non-goals

### MVP scope

- Designer-only request domain.
- Text, strict JSON, structured 3D object, and isolated text/code bundle outputs.
- One job-owned workspace output root.
- One provider-neutral scheduler with one or more text-capable provider adapters.
- Preview, estimate, approval, progress, cancel, resume, item retry, collection manifest, gallery/table, and shortlist.
- Initial supported count: 1–100 without elevated count approval; 101–500 requires explicit approval. Hard launch maximum: 500 items per job.
- Bounded concurrency default: 5, independently configurable by provider within a server maximum. Item count never equals concurrency.

### Deferred

- Image, video, audio, and other binary generation adapters.
- Finder or Coder Swarm jobs.
- Arbitrary tool use by workers.
- Child sessions or per-item chat.
- Per-item Git branches/worktrees or automatic integration.
- Workers reading the repository beyond immutable, server-selected inputs included in the brief.
- Arbitrary executable code validation or running generated code.
- Cross-job autonomous composition.
- Hosted control-plane scheduling or non-local runner expansion.
- Claims that every item is a distinct agent.

## 7. Core contracts

All names below are provisional Go/domain names. The boundaries are required even if implementation naming changes.

### 7.1 `SwarmCreativeBrief`

Immutable after approval:

```text
brief_id
schema_version
user_intent
audience
output_kind
generator_id + generator_version
requested_count
invariants[]
diversity_axes[]
forbidden_patterns[]
reference_asset_ids[]
anchor_examples[]
evaluation_rubric[]
output_root
materialization_mode
created_by_run_id
content_digest
```

The brief includes only immutable workspace-relative references or owned session asset IDs. Workers receive bounded normalized content, not credentials, full transcripts, tool definitions, or mutable repository access.

### 7.2 `SwarmVariantSpec`

Produced in batches by a tool-free expander and validated before scheduling:

```text
item_id
ordinal
seed
axis_values{}
generation_prompt
negative_constraints[]
output_contract
expected_files[]
spec_digest
```

IDs derive from `(job_id, ordinal, spec_digest)` or an equivalent stable construction. The server rejects duplicate IDs, duplicate destinations, schema mismatch, missing diversity coverage, and count overflow.

Expansion should use 4–16 bounded requests returning multiple specs each, not one enrichment request per item. The configured Router model may be reused temporarily as a model assignment only after the generic one-shot invocation is extracted. Router’s naming schema and authority must remain unchanged.

### 7.3 `SwarmGeneratorAdapter`

Every output kind implements a provider-neutral typed adapter:

```text
ID() -> stable identifier
Version() -> immutable schema version
Capabilities() -> media kinds, limits, provider requirements
BuildRequest(brief, spec, modelSnapshot) -> tool-free provider request
Decode(response) -> candidate payload or typed adapter error
Validate(candidate, limits) -> validated output + diagnostics
DescribeMaterialization(validated) -> isolated relative artifacts
Preview(validated) -> bounded preview metadata
CostEstimate(brief, count, modelSnapshot) -> estimate or explicitly unknown
```

Adapters cannot write files, invoke tools, access Git, or mutate the job. They return data or durable asset references only.

Initial adapters:

1. **`text-v1`** — bounded UTF-8 document plus structured metadata.
2. **`json-v1`** — caller-selected strict JSON Schema with depth, byte, and key limits.
3. **`structured-3d-object-v1`** — declarative geometry/material/transform/anchors/bounds/tags schema. No scripts, external URLs, shaders, or executable callbacks.
4. **`code-bundle-v1`** — bounded map of relative text files implementing a fixed user-supplied interface. Files are never executed; extensions, count, and bytes are allowlisted. Each item stays in its own directory.

Future adapters:

- `image-v1` integrates the existing `swarmd/internal/imagegen` service and persists owned asset references;
- future video/audio adapters use the same job/item/scheduler/materializer contracts while adding media-specific capability, cost, and preview fields.

### 7.4 `SwarmJob`

One durable parent-session-owned resource:

```text
job_id
parent_session_id
workspace_binding_id
brief + digest
state
limits_snapshot
provider_model_snapshot
output_root
materialization_policy
counts {requested, specified, queued, running, valid, materialized, failed, cancelled}
usage {requests, input_tokens, output_tokens, media_units, cost_known, estimated_cost, actual_cost}
created_at, approved_at, started_at, updated_at, completed_at
last_error_summary
resume_cursor
revision
```

Lifecycle:

```text
draft -> estimated -> awaiting_approval -> approved -> expanding -> generating
      -> validating -> materializing -> completed

running states -> pausing -> paused -> generating
running states -> cancelling -> cancelled
running states -> failed
completed may be partial_success when terminal item failures remain
```

`completed` needs an explicit outcome: `success` or `partial_success`. A job must never report success when requested items are missing.

### 7.5 `SwarmItem`

Store items as bounded records or chunked pages, not 500 child sessions:

```text
job_id, item_id, ordinal
spec + digest
state + attempt_count
provider_request_id when safe to retain
provider/model snapshot
started_at, finished_at
usage/cost
validation diagnostics
candidate payload ref
artifact refs[]
preview metadata
error {class, retryable, safe_message}
source_item_id for retry lineage
```

Item lifecycle:

```text
pending_spec -> specified -> queued -> generating -> validating
             -> ready -> materializing -> materialized
             -> retry_wait -> queued
             -> failed | cancelled
```

Large candidate payloads and media bytes use the repository’s canonical resource/asset storage; events and projections contain references and bounded summaries.

### 7.6 Scheduler

Use a bounded scheduler keyed by provider, credential/account, model, generator/media kind, and interactive-priority class.

Required behavior:

- global, per-provider, per-model, and per-job semaphores;
- fair scheduling that reserves capacity for interactive chat;
- hard request, token, media, item, elapsed-time, and cost budgets;
- jittered exponential backoff and adapter-normalized `Retry-After`;
- adaptive concurrency reduction on throttling/resource exhaustion, with slow recovery;
- maximum attempts and deterministic terminal failure classification;
- cancellation propagation to queued and in-flight requests where supported;
- durable retry schedule and restart recovery;
- no goroutine per waiting item and no unbounded channel or response aggregation;
- bounded samples/counters in realtime rather than one live transcript per item.

A worker is one tool-free provider invocation. It has no `read`, `write`, `edit`, Bash, Git, session, task, or plan tools. It receives only the normalized brief fragment, variant spec, output schema, and adapter instructions.

### 7.7 Validator

Validation occurs before any workspace write:

- verify schema/version and content type;
- enforce item/file/byte/depth limits;
- reject traversal, absolute paths, reserved names, empty paths, duplicate normalized paths, and disallowed extensions;
- reject external URLs or executable fields where the adapter forbids them;
- compute content hashes and exact duplicate groups;
- record warnings separately from hard errors;
- permit an item-level retry without replaying successful items;
- produce deterministic preview metadata and materialization instructions.

Semantic judging and ranking are not validation authority. A judge score cannot make an invalid item writable.

### 7.8 Trusted materializer

Generation workers never write to the checkout. A single trusted service materializes validated results beneath one approved root, recommended shape:

```text
<workspace>/.swarm-output/<job-slug>-<job-id>/
  manifest.json
  items/
    <item-id>/
      item.json
      <adapter-owned files>
  previews/
  shortlist.json
```

The exact root should be chosen through a storage/product contract before implementation. Do not assume `.swarm-output` is canonical merely because it appears in this proposal. It must be workspace-relative, intentionally user-visible or explicitly ignored, and approved before the first write.

Materializer invariants:

- resolve every destination against the workspace binding and job root;
- reject absolute paths, `..`, symlink/reparse-point escape, device paths, and case-fold collisions;
- create the job root with no overwrite by default;
- serialize manifest revision changes and use atomic file replacement;
- write each item to a staging sibling under the job root, validate final paths, then atomically publish when supported;
- never follow generated symlinks and never materialize symlink entries;
- use stable item directories so resume is idempotent;
- compare stored hashes before treating an existing file as complete;
- on interruption, recover from the durable job/item records and manifest revision;
- require a new approval to overwrite or materialize outside the original approved contract;
- report partial materialization exactly, retaining validated candidates for retry.

### 7.9 Aggregation and review

Never put every full item into the parent model context.

1. Deterministically hash and deduplicate exact matches.
2. Group by declared diversity axes and adapter-specific inexpensive similarity features.
3. Score bounded batches against the brief rubric using optional tool-free judge adapters.
4. Preserve score provenance; do not present subjective model scores as validation.
5. Show representative winners, outliers, failures, duplicates, and coverage gaps.
6. Let the user inspect every original item on demand and create a durable shortlist.
7. Send only bounded summaries and selected previews to the main Swarm model for synthesis.

For 3D collections, preview metadata includes bounds, triangles/primitive count where derivable, materials, tags, anchors, and a deterministic thumbnail/scene preview only after a safe renderer exists. Initial review may be schema/table based.

## 8. Durability and API shape

Swarm jobs are V3 session resources. Every create, approval, lifecycle, item-page, retry, cancellation, shortlist, and materialization mutation must cross `ApplySessionMutation` / `ApplyV3SessionMutation` so events, projection state, idempotency, and realtime outbox remain atomic.

Do not create a second in-memory job authority or put correctness state in Desktop local storage. Scheduler queues may accelerate execution, but restart recovery comes from durable approved jobs and item states.

Proposed endpoints, subject to API contract review:

```text
POST   /v3/sessions/{session_id}/swarm-jobs:estimate
POST   /v3/sessions/{session_id}/swarm-jobs
GET    /v3/sessions/{session_id}/swarm-jobs/{job_id}
GET    /v3/sessions/{session_id}/swarm-jobs/{job_id}/items?cursor=...
POST   /v3/sessions/{session_id}/swarm-jobs/{job_id}:approve
POST   /v3/sessions/{session_id}/swarm-jobs/{job_id}:pause
POST   /v3/sessions/{session_id}/swarm-jobs/{job_id}:resume
POST   /v3/sessions/{session_id}/swarm-jobs/{job_id}:cancel
POST   /v3/sessions/{session_id}/swarm-jobs/{job_id}/items/{item_id}:retry
PUT    /v3/sessions/{session_id}/swarm-jobs/{job_id}/shortlist
POST   /v3/sessions/{session_id}/swarm-jobs/{job_id}:materialize
```

Mutation requests require account/session/workspace identity, idempotency key, expected job revision where relevant, and canonical payload hashing. Item list cursors are opaque and scoped to the job/filter. No endpoint returns unbounded item payloads.

The model-facing tool should be a dedicated, single-purpose tool such as `swarm_generate`, not an extension of `task`:

- `estimate` validates a brief and returns approval-safe facts;
- `create` creates the durable job from the exact estimate/brief digest;
- lifecycle operations act on one owned job;
- the tool never accepts shell commands or arbitrary absolute destinations;
- mode instructions direct the parent to use it for supported bulk generation and to avoid writing a step-by-step plan.

The Desktop can invoke equivalent typed APIs from approval/review controls. Definition management and execution must remain separate from Workspace Actions.

## 9. Permissions, privacy, cost, and limits

### Approval boundaries

Always require approval before the first provider generation or workspace materialization in the launch MVP. The approval card includes:

- count and generator;
- provider/model snapshot;
- known estimate or “cost unavailable” with hard non-currency budgets;
- global and provider concurrency;
- requested output root and expected file count/bytes;
- reference assets sent to providers;
- overwrite policy;
- cancellation and partial-success behavior.

Reapproval is required when count, generator, model/provider, budget, references, output root, materialization contract, or overwrite behavior changes.

### Defaults and hard ceilings

- default requested count suggested by the parent, never silently inflated;
- 100-item approval tier; elevated explicit approval for 101–500;
- 500 hard items/job at launch;
- configurable lower account limits;
- bounded per-item and total bytes;
- bounded requests and retries independent of requested item count;
- stop before exceeding a hard budget; never silently downgrade, omit, or claim completion;
- no public listener or new network exposure;
- provider calls use existing account-scoped credentials without placing secrets in prompts, events, logs, or manifests;
- diagnostics remain privacy-redacted by default.

### Partial success

A job can complete with `partial_success` when at least one item materialized and terminal failures remain. The UI and manifest show requested, succeeded, duplicate, failed, and cancelled counts. The user may retry failed items within remaining budgets. Successful items are never regenerated merely to make counters appear complete.

### Cancellation and resume

Cancel stops new scheduling immediately, requests cancellation of in-flight work where supported, retains completed results, and marks never-started items cancelled. Pause retains resumability without converting queued items to terminal cancellation. Daemon restart reconstructs approved nonterminal jobs from durable state and applies an explicit recovery policy; it does not assume in-flight provider requests succeeded.

## 10. Desktop experience

### Composer

Extend the current Auto/Plan mode affordance with a clear **Swarm** choice and short description:

> Generate many independent design artifacts from one brief.

Do not overload the existing plan toggle icon without a labeled picker. “Swarm model” settings already use Swarm as the primary-agent brand, so mode copy must say **Swarm mode** or **Swarm generation**, never just “Swarm model.”

When the user submits an unsupported request in Swarm mode, explain why it is dependent/autonomous work and offer Auto or Plan mode; do not run a fake swarm.

### Estimate/approval card

Show count, type, destination, model/provider, concurrency, estimated requests/tokens/cost, references, and approval threshold. Unknown cost must be visible. Let the user reduce count or change destination before approval through a new estimate, not an in-place mutation of the approved digest.

### Progress

Show real item states: specified, queued, generating, validating, ready, materializing, failed. Counters come from the durable projection. A particle/card visualization is optional, but every visual unit represents a real item. Use bounded representative previews and virtualized lists.

### Review

Provide:

- gallery/table switch based on adapter preview capability;
- filters for state, axis, tag, score, model, duplicate group, and validation warning;
- compare view and shortlist;
- explicit missing/failure accounting;
- inspectable prompt/spec/model lineage with safe redactions;
- collection path and manifest revision;
- “continue in Auto mode with shortlist” as a normal future user action, not automatic integration.

Backend-derived Swarm state belongs in `web/src/features/desktop/state/`, runtime ownership in `runtime/`, and realtime coordination in `realtime/`. Components consume selectors/actions and do not parse transport frames into a second cache.

## 11. Ordered implementation plan

Each phase is independently reviewable. Do not begin image support or high-count rollout until the preceding durability and recovery gates pass.

### Phase 0 — Freeze product contract and task boundary

Work:

- adopt the promise, terminology, supported/unsupported intent matrix, and 25-child task-call cap;
- distinguish task call count, active-child concurrency, and automatic wave budget in API errors/settings copy;
- add request classification guidance and deterministic redirect errors;
- select and document the canonical user-visible output-root contract rather than inventing a fallback path;
- define launch limits: 100 normal approval tier, 500 elevated tier/hard maximum.

Likely files:

- `swarmd/internal/tool/runtime.go`
- `swarmd/internal/run/service_task_launch.go`
- `swarmd/internal/permission/policy.go`
- `swarmd/internal/run/service_prompt.go`
- `web/src/features/desktop/settings/permissions/components/permissions-settings-page.tsx`
- product documentation under `docs/`

Acceptance:

- one task call with 25 launches may proceed through normal atomic reservation, while 26 launches fails before reservation with a stable redirect message;
- homogeneous generation requests are described as Swarm mode, not child agents;
- settings and help text explain the 25-child cap, the large-wave warning, and the existing wave/concurrency controls without conflating them;
- the output-root decision is portable, workspace-relative, and consistent with storage contracts.

Validation expectations:

- focused parser/policy/service tests for 25 accepted, 26 rejected, 9–25 warning behavior, lower account limits, atomic reservation, and no reservation on product-cap rejection;
- focused Desktop copy/settings tests.

### Phase 1 — Provider-neutral one-shot inference and generator registry

Work:

- extract the generic configured-model, one-shot, tool-free invocation from `swarmd/internal/api/session_router.go` into an internal provider-neutral service;
- keep `swarmd/internal/router/router.go` naming behavior unchanged;
- add immutable generator registry, versioned schemas, capability preflight, normalized errors, and output limits;
- implement `text-v1` and `json-v1` adapters;
- temporarily allow an explicit compatibility mapping to the Router model assignment, while planning a dedicated Swarm generator/expander model assignment.

Likely files:

- `swarmd/internal/api/session_router.go`
- `swarmd/internal/router/router.go`
- `swarmd/internal/agentmodel/resolver.go`
- new `swarmd/internal/swarmgen/` package
- `swarmd/internal/agent/system_agent_registry.go` only if a compiled tool-free utility identity is needed

Acceptance:

- callers can invoke a configured model once with tools forced off and a strict bounded response schema;
- Router naming tests remain unchanged and pass;
- generator IDs/versions are immutable and unsupported provider/media combinations fail at preflight;
- malformed, oversized, or schema-invalid outputs never reach materialization.

Validation expectations:

- focused unit tests for tool prohibition, schema decoding, bounds, provider error normalization, and Router regression.

### Phase 2 — Durable Swarm job and item model

Work:

- define job/item/event/projection records and mutation kinds;
- implement estimate, create, approve, get, and paginated item APIs with idempotency and optimistic revision checks;
- persist immutable brief, model/provider/limits snapshot, item pages, counters, usage, and errors;
- emit bounded realtime patches through the existing V3 outbox;
- add restart scanning/recovery ownership without starting generation yet.

Likely files:

- `swarmd/internal/store/pebble/` V3 event/projection records and indexes
- `swarmd/internal/session/` mutation contracts
- `swarmd/internal/api/` V3 routes and authorization
- `swarmd/internal/api/sessions_v3_realtime_contract.go`
- new `swarmd/internal/swarmjob/` service/domain package

Acceptance:

- every mutation crosses canonical V3 mutation/outbox authority atomically;
- replaying an idempotency key does not duplicate a job or items;
- item listing is bounded, cursor-scoped, and restart-safe;
- realtime publishes counters/samples without embedding full collection payloads;
- no child session is created for any job item.

Validation expectations:

- focused store/service/API tests for atomicity, replay, revision conflict, authorization, pagination, restart hydration, and outbox ordering.

### Phase 3 — Expander and bounded scheduler

Work:

- compile immutable creative briefs and batched variant specs;
- validate IDs, count, diversity matrix coverage, and duplicate specs;
- implement global/provider/model/job queues, fair interactive priority, adaptive limits, budgets, retries, pause/resume/cancel, and restart reconciliation;
- execute `text-v1` and `json-v1` workers without tools;
- persist every item transition and normalized usage/error evidence.

Likely files:

- new `swarmd/internal/swarmscheduler/`
- `swarmd/internal/swarmgen/`
- `swarmd/internal/swarmjob/`
- provider adapter packages for throttle/usage normalization
- daemon startup wiring in `swarmd/internal/runtime/daemon.go`

Acceptance:

- 100 items complete under bounded memory/concurrency without child sessions;
- throttling reduces provider concurrency and honors retry hints;
- chat retains reserved interactive capacity;
- hard budgets stop scheduling before overrun;
- pause, cancel, item retry, daemon restart, and partial success preserve completed work and honest counts;
- no waiting-item goroutine explosion or unbounded response aggregation occurs.

Validation expectations:

- deterministic fake-provider tests for fairness, throttling, retry, hard budgets, cancellation races, restart, and partial success;
- bounded 100-item integration benchmark with event/memory evidence, not a broad repository suite.

### Phase 4 — Validator and trusted materializer

Work:

- finalize the approved workspace output-root contract;
- implement path normalization, symlink/case/collision defense, staging, atomic publish, hashes, manifest revisions, resume, and overwrite approval;
- add `structured-3d-object-v1` and `code-bundle-v1` adapters;
- generate collection manifests and shortlist documents;
- keep all outputs isolated per item and prohibit execution.

Likely files:

- new `swarmd/internal/swarmmaterialize/`
- `swarmd/internal/swarmgen/`
- workspace/path authority integrations near `pkg/storagecontract/storagecontract.go` and canonical workspace binding services
- V3 job mutation/service code for artifact refs and manifest revisions

Acceptance:

- traversal, absolute paths, symlink escape, duplicate destinations, case-fold collisions, and unapproved overwrite all fail closed;
- interruption and retry produce the same manifest and hashes without duplicating files;
- every item is isolated and independently loadable/inspectable;
- structured 3D outputs have schema-valid geometry/material/anchor/bounds metadata;
- code bundles implement a fixed textual contract, stay within allowlisted extensions/limits, and are never executed;
- partial writes are reported and recoverable without claiming collection completion.

Validation expectations:

- focused cross-platform path tests where supported;
- fault-injection tests at staging, item publish, and manifest publish boundaries;
- malicious generated-path fixtures and idempotent resume tests.

### Phase 5 — Swarm mode tool and Desktop MVP

Work:

- register a dedicated `swarm_generate`-style tool with estimate/create/lifecycle actions and strict path/count schemas;
- add Swarm mode to composer execution choices and mode instructions;
- build estimate/approval, progress, virtualized item gallery/table, item details, cancel/pause/resume/retry, shortlist, and collection-location surfaces;
- hydrate state through canonical V3 state and targeted item queries;
- show honest labels, unknown cost, partial success, and real item counts.

Likely files:

- `swarmd/internal/tool/runtime.go`
- `swarmd/internal/run/service_tools.go` or a dedicated single-purpose Swarm tool handler
- `swarmd/internal/run/service_prompt.go`
- `web/src/features/desktop/chat/components/mode-picker.tsx`
- `web/src/features/desktop/chat/components/desktop-v3-agentic-composer.tsx`
- new `web/src/features/desktop/swarm/`
- `web/src/features/desktop/state/`
- `web/src/features/desktop/runtime/`
- `web/src/features/desktop/realtime/`
- `web/src/features/desktop/session-v3/`

Acceptance:

- a user can request, estimate, approve, monitor, cancel/resume, review, shortlist, and materialize a 100-item collection without using `task`;
- unsupported requests produce clear Auto/Plan/task guidance;
- all displayed units map to durable jobs/items and survive Desktop or daemon restart;
- item virtualization and bounded realtime keep UI frame/render cost stable at 500 items;
- parent chat receives only bounded status/summary evidence, not all full outputs.

Validation expectations:

- focused API/tool contract tests;
- Desktop component/state/realtime tests for mode selection, approval digest changes, progress gaps/refetch, virtualized 500-item views, cancellation, partial success, and restart hydration;
- manual 3D-object collection review using safe declarative outputs.

### Phase 6 — Controlled 500-item rollout and measurement

Work:

- instrument privacy-safe operational counters;
- run staged count tiers (10, 25, 100, then 500) with approved test providers/budgets;
- tune chunk sizes, event cadence, scheduler concurrency, and gallery virtualization;
- add account controls for item/cost/concurrency limits and kill switch;
- document operator recovery and user expectations.

Required measurements:

- time to estimate and approval;
- time to first valid/materialized item;
- p50/p95 item latency and total wall time;
- provider throttles, retries, terminal failures, and partial-success rate;
- token/media/currency usage where known;
- scheduler queue depth and interactive chat latency impact;
- Pebble record/event/outbox volume and restart recovery time;
- daemon peak memory/goroutines;
- realtime bytes/patch rate;
- Desktop hydration time, frame time, and memory;
- cancellation latency and post-cancel provider completion count;
- exact-duplicate rate, diversity-axis coverage, shortlist rate, and promotion rate.

Acceptance:

- 100-item jobs satisfy reliability/performance budgets before 500 is enabled;
- a 500-item job remains within explicit daemon, event, realtime, UI, and cost budgets;
- cancellation and restart evidence is retained for the exact build under test;
- metrics count jobs/items/provider requests accurately and never label items as agents;
- rollout can be disabled without corrupting existing durable jobs or collections.

Validation expectations:

- checked-in bounded load harness using fake providers for routine regression;
- separately approved real-provider evidence with hard spend limits and no private payload retention;
- focused launch-readiness review before enabling the feature by default.

### Phase 7 — Media adapter expansion (deferred until gates pass)

Work:

- add `image-v1` by integrating `swarmd/internal/imagegen`, not duplicating provider clients;
- store immutable owned media refs and bounded previews;
- add media-specific count/cost/size/content validation and review UI;
- evaluate a dedicated Swarm worker model assignment separately from Router;
- later add other media only through the generator capability registry.

Acceptance:

- image generation uses the same durable job, scheduler, cancellation, budget, review, and partial-success contracts;
- raw media bytes do not enter V3 event payloads or parent context;
- provider-specific behavior remains inside adapters;
- text/code/3D jobs require no migration or alternate authority.

Validation expectations:

- focused image service integration tests, media ownership/auth tests, cancellation/retry tests, and gallery performance tests.

## 12. Risks and likely attack points

1. **Product confusion with the Swarm agent/model.** Use “Swarm mode,” “Swarm job,” and “items”; update model-setting copy only where ambiguity exists.
2. **Turning `task` into an escape hatch.** Enforce the 25-child product cap server-side before reservation, warn for 9–25 launches, redirect homogeneous bulk generation to Swarm mode, and prohibit silent wave splitting.
3. **Claiming tool-free workers are Designer agents.** The MVP is Designer-only as a capability domain, but the UI counts generated items, not agents or sessions.
4. **Duplicate durability authority.** Keep jobs/items on V3 mutation/projection/outbox paths; in-memory queues are accelerators only.
5. **Event and projection amplification.** Chunk item pages, coalesce counters, cap samples, and avoid per-token/per-item transcript events.
6. **Path and collision attacks.** Generated paths are untrusted input; only the materializer writes, beneath an approved root, after containment and collision checks.
7. **Generated-code execution.** MVP validates and writes isolated text only; it does not install dependencies, run previews, or execute generated modules.
8. **Cost runaway and provider starvation.** Hard budgets, approval tiers, fair interactive priority, adaptive queues, and kill switches are launch requirements.
9. **False completion under partial failure.** Requested/succeeded/failed/cancelled/duplicate counts and terminal outcome are explicit in the job and manifest.
10. **Router authority creep.** Reuse only an extracted one-shot invocation/model assignment compatibility path; do not add generation semantics to Router naming.
11. **Premature media generalization.** Keep adapter interfaces modular, but prove text/JSON/3D/code durability and safety before image rollout.
12. **500-item UI theater.** Every progress unit must map to a real durable item; simulated demos must be explicitly labeled and excluded from performance claims.

## 13. Decisions to freeze before implementation

These are implementation-phase product decisions, not reasons to weaken the architecture:

- canonical user-visible output-root location and ignore/tracking behavior;
- whether Swarm mode is stored as a new session submission mode or as a typed composer intent that invokes the dedicated tool while the session remains Auto at rest;
- first dedicated Swarm expander/generator model assignment timing versus temporary Router assignment reuse;
- exact default bytes/files/retries/concurrency and account-admin control surface;
- first declarative 3D schema and preview strategy;
- currency estimate behavior when provider pricing is absent or stale.

Recommended decision for session semantics: store **Swarm generation as a typed run intent/job**, not as a new long-lived session authority. The composer may visually expose “Swarm mode,” but the durable object is the job attached to the V3 session. This avoids making every chat message inherit bulk-generation behavior and keeps Auto/Plan lifecycle semantics intact.

## 14. Relevant existing filepaths

- `docs/checkpoints/swarm-burst-architecture.md` — source architecture evidence and current high-fanout analysis.
- `swarmd/internal/tool/runtime.go` — current `task` schema and compiled eligible agents.
- `swarmd/internal/run/service_task_launch.go` — task parsing, manifests, scope validation, and launch boundary.
- `swarmd/internal/run/service_tools.go` — durable child creation, execution, aggregation, and task tool handling.
- `swarmd/internal/permission/policy.go` — account task-wave policy, default active-child limit, and current 256 safety ceiling.
- `swarmd/internal/permission/subagent_reservation.go` — durable atomic task-wave/child reservations.
- `swarmd/internal/run/service_prompt.go` — mode and tool-selection instructions.
- `swarmd/internal/agent/system_agent_registry.go` — immutable Finder/Coder/Designer/Router identities and tool contracts.
- `swarmd/internal/agentmodel/resolver.go` — canonical account-scoped system-agent model resolution.
- `swarmd/internal/api/session_router.go` — current strict tool-free configured-model invocation shape.
- `swarmd/internal/router/router.go` — Router’s narrow naming authority, which must not expand.
- `swarmd/internal/imagegen/` — existing provider-neutral image-generation service for the deferred adapter.
- `swarmd/internal/store/pebble/session_event_store.go` — canonical V3 mutation/event/projection/outbox boundary implementation.
- `swarmd/internal/api/sessions_v3_realtime_contract.go` — V3 realtime contract and bounded patch expectations.
- `swarmd/internal/runtime/daemon.go` — daemon service/scheduler startup wiring.
- `pkg/storagecontract/storagecontract.go` — portable storage authority relevant to choosing an output-root contract.
- `web/src/features/desktop/chat/components/mode-picker.tsx` — current Auto/Plan composer affordance.
- `web/src/features/desktop/chat/components/desktop-v3-agentic-composer.tsx` — composer execution surface.
- `web/src/features/desktop/chat/components/desktop-plan-subagent-list.tsx` — existing child-agent UI that must remain distinct from Swarm item review.
- `web/src/features/desktop/settings/permissions/components/permissions-settings-page.tsx` — current task-wave/active-child policy controls.
- `web/src/features/desktop/state/` — canonical backend-derived Desktop state.
- `web/src/features/desktop/runtime/` — Desktop runtime ownership.
- `web/src/features/desktop/realtime/` — V3 realtime coordination and gap recovery.
- `web/src/features/desktop/session-v3/` — typed Desktop V3 transport/API contracts.

## 15. Launch definition of done

Swarm mode is launch-ready when a user can ask for 100 independently useful Designer-domain artifacts, understand and approve the exact count/cost/destination contract, watch honest durable progress, cancel or resume safely, receive an explicit complete or partial-success collection, review and shortlist results after restart, and later promote selected outputs without any generation worker receiving tools or writing outside the single approved job root.

The 500-item claim is enabled only after the same contract passes measured durability, scheduler, cost, event-volume, materialization, cancellation, recovery, and Desktop performance gates at that scale.

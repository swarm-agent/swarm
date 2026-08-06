# Swarm Burst: high-fanout ideation without heavyweight agents

## Recommendation

Build **Swarm Burst** as a separate bulk-generation capability, not as a larger `task` wave.

`task` should remain the durable autonomous-agent path for Finder, Coder, and Designer work. A burst should represent many real generated **ideas or objects**, but it should not claim that every item is a full agent. This distinction is what makes 100–500 outputs practical without turning the product into a cosmetic agent-count demo.

The core model is:

> A strong main model acts as creative director, a cheap tool-free model expands its direction into diverse one-shot specifications, bounded provider pools generate the items, and one trusted materializer writes approved outputs.

## What exists today

### The task tool is deliberately heavyweight

The task schema accepts only Finder, Coder, and Designer launches. Every launch carries its own assignment, deliverable, scope, and dependency evidence (`swarmd/internal/tool/runtime.go:1223-1296`; parsing and validation in `swarmd/internal/run/service_task_launch.go:180-308`).

For each launch, the runtime:

1. Resolves a compiled agent and account-scoped model assignment.
2. Creates a canonical durable V3 child session through `ApplySessionMutation` (`swarmd/internal/run/service_tools.go:484-659`).
3. For Coder, resolves a clean Git base and allocates a distinct sibling worktree before execution (`swarmd/internal/run/service_tools.go:3319-3335`, `570-601`).
4. Builds a large delegated prompt containing sanitized parent/session metadata, the active plan, recent transcript, and a generic autonomous completion contract (`swarmd/internal/run/service_tools.go:3929-3997`).
5. Runs a complete provider turn for every child and waits for the whole wave (`swarmd/internal/run/service_tools.go:3164-3182`, `3497-3594`).
6. Persists child reports and lineage, validates Coder commits, and aggregates bounded report excerpts back into the parent (`swarmd/internal/run/service_tools.go:3635-3901`).

This is appropriate for autonomous research and isolated code changes, but most of that machinery is unnecessary for “show me 500 logo directions.”

### Concurrency is policy-bounded

The account policy separately limits accepted waves and active children. Reservations are durable and atomic; delegated children cannot delegate further (`swarmd/internal/permission/subagent_reservation.go:35-114`). The checked-in default is five active children, and the validation ceiling is 256 (`swarmd/internal/permission/policy.go:72-75`, `151-184`). The active runtime policy can differ, but a 500-child `task` wave is invalid under the current contract.

Write-capable Coder launches also require worktree isolation and a committed clean handoff. Designer can write in the shared checkout only to concrete, non-overlapping targets and cannot use Git (`swarmd/internal/agent/system_agent_registry.go:419-453`). Those rules prevent corrupt parallel writes, but they make “hundreds of writers” the wrong primitive.

### Router is already a useful one-shot inference lane

Router is a compiled hidden, read-only, tool-free system agent (`swarmd/internal/agent/system_agent_registry.go:363-369`, `604-614`). Its provider, model, thinking level, and service tier come from canonical account-scoped system-agent settings (`swarmd/internal/agentmodel/resolver.go:14-68`, `71-84`).

The existing API bridge resolves that configured model, supplies arbitrary caller-owned instructions and input, forces `Tools: []` and `ToolChoice: "none"`, invokes exactly one provider response, bounds output size, and validates the result (`swarmd/internal/api/session_router.go:28-30`, `99-174`). The current Router product contract itself is only session/worktree naming with strict JSON (`swarmd/internal/router/router.go:21-69`).

Therefore, the configured Router **model assignment** is technically suitable for cheap prompt expansion, but the naming Router contract should not be overloaded. Extract the one-shot provider invocation into a provider-neutral internal utility service, then give Swarm Burst its own compiled prompt/schema. Longer term, expose a distinct “Burst worker” model assignment so users can tune it independently from session naming.

## Why 25 full agents feel slow

The wave is parallel only after preparation. Child profile resolution and V3 session creation happen sequentially, and Coder worktree allocations happen sequentially as well (`swarmd/internal/run/service_tools.go:3347-3375`). Each child then incurs:

- a full durable session/run lifecycle rather than a small inference request;
- a duplicated parent/plan/transcript prompt, increasing input tokens and provider prefill time;
- tool discovery and potentially many tool round trips;
- provider-side queuing, account rate limits, and tail latency;
- parent blocking until the slowest launch completes;
- per-child events, projections, realtime patches, reports, and UI state;
- final report aggregation whose inline context is explicitly capped (`swarmd/internal/run/service_tools.go:3792-3811`).

Launching more goroutines does not remove these costs. At 100–500 it increases provider throttling, memory/event pressure, UI load, and context aggregation pressure. The previous 25-Finder wave illustrates this: all children returned, but the wave performed 222 child tool attempts and overflowed the parent’s inline report budget.

## Proposed architecture

### 1. Creative direction: one strong main-model turn

The main Swarm agent produces an immutable `CreativeBrief`:

- intent and audience;
- visual or object constraints;
- invariants that every result must preserve;
- diversity axes and forbidden repetitions;
- references/examples supplied by the user;
- evaluation rubric;
- requested count and output kind.

This is where the main agent’s taste lives. Downstream workers are not asked to reinterpret the user from scratch.

### 2. Prompt expansion: a few batched lightweight calls

Use the configured lightweight utility model to expand the brief into strict `VariantSpec[]` objects. Do **not** make 500 enrichment calls. Make perhaps 4–16 calls, each responsible for a disjoint region of a deterministic diversity matrix and returning 16–64 specifications.

Each specification contains a stable ID, seed, creative axes, generation prompt, negative constraints, expected output type, and lineage back to the immutable brief. The server validates count, schema, IDs, and axis coverage. Duplicate hashes are rejected before generation.

This produces meaningful parallel variation while preserving the creative director’s constraints. It also lets the main model author explicit anchor examples that every expander sees.

### 3. Generation: adaptive bounded provider pools

Run one-shot, tool-free generation requests from the validated specifications. Use a scheduler keyed by provider, model, credential/account, and media kind rather than one unbounded all-at-once goroutine wave.

The scheduler should support:

- configurable global and per-provider concurrency;
- token/request/image budgets;
- jittered exponential backoff for throttling;
- `Retry-After` when exposed by an adapter;
- adaptive concurrency reduction after 429/resource-exhausted responses and slow recovery;
- cancellation and pause;
- item-level retry without replaying successful work;
- fair sharing with interactive chat so a burst cannot make normal Swarm use unusable.

For text/JSON objects, require a strict result schema. For images, route through the existing provider media/image-generation surfaces and persist the returned asset references; do not pretend a text agent “wrote” an image.

### 4. Durability: one bulk job, not 500 child sessions

Add a durable V3 `SwarmBurstJob` resource owned by the parent session, with item records or chunked item pages. All mutations still cross the canonical V3 session mutation/outbox boundary.

Suggested lifecycle:

`draft -> estimated -> approved -> expanding -> generating -> materializing -> completed | paused | cancelled | failed`

Persist counters, per-item status, prompt/spec digest, provider/model snapshot, usage/cost, errors, asset/artifact refs, and retry lineage. Realtime should publish bounded counter/sample patches, not replay 500 complete transcripts into the parent chat.

A burst item is inspectable and reproducible, but it is not a chat session unless the user explicitly promotes it into one.

### 5. Writes: workers never race on the checkout

Generation workers return data or asset references only. They do not receive `write`, `edit`, Bash, or Git.

A single trusted materializer writes results after validation:

- target one dedicated workspace-relative burst directory;
- use stable collision-free item IDs and a manifest;
- write each item atomically;
- reject traversal, symlink escape, duplicate destinations, and overwrite unless explicitly approved;
- resume from the manifest after interruption;
- let the main agent or user promote selected outputs into canonical product files.

For source-code variants, either emit patch proposals or isolate every variant under its own subdirectory. Do not allocate hundreds of Git worktrees. Real integration remains a later Coder/main-agent operation with normal review boundaries.

### 6. Aggregation: reduce before asking the main model to judge

Do not feed 500 full outputs back into the parent context.

1. Deterministically validate, hash, and deduplicate.
2. Group by declared diversity axes and inexpensive similarity signals.
3. Use lightweight judges to score bounded batches against the main agent’s rubric.
4. Surface representative winners, outliers, failures, and coverage gaps.
5. Ask the main model to review only the reduced gallery plus summaries, while keeping every original item durable and inspectable.

This preserves the main agent’s taste at the beginning and end without paying for 500 heavyweight main-model conversations.

## Product safeguards

Before starting, show an estimate with output count, expected provider calls, selected models, concurrency, approximate token/image spend when knowable, destination, and whether writes will occur. Require explicit approval above account-configured count/cost/write thresholds.

Recommended controls:

- **Provider health:** preflight credentials/catalog support; per-provider queues and adaptive limits.
- **Cost:** hard max calls/tokens/images/currency where adapters expose pricing; stop rather than silently exceed.
- **Interactive priority:** reserve capacity for chat and user-triggered actions.
- **Privacy:** keep prompts/results local except for the chosen provider calls; redact diagnostics by default.
- **Writes:** default to preview/gallery; require a selected destination and materialization approval for workspace output.
- **Quality:** schema validation, dedupe, missing-item accounting, retry caps, and explicit partial-success state.
- **Review:** filters by score/axis/model/status, contact sheet/gallery, compare view, shortlist, and promote/export actions.
- **Cancellation:** immediate scheduler stop, durable completed items retained, queued items marked cancelled.

## Honest 100–500 visualization

A compelling demo can show 100 or 500 live **burst items** as particles/cards moving through `specified`, `queued`, `generating`, `validating`, and `ready`. Every visual unit must correspond to a durable real item or real queued request.

Do not label batched specs, UI particles, or simulated timers as “500 agents.” If marketing needs agent language, use wording such as “one creative director coordinating 500 parallel generations.” Reserve the agent count for actual V3 child sessions.

Offer an explicit demo/simulation mode only for UI rehearsal. It must be visibly marked simulated and must not be mixed with product performance claims.

## Concrete MVP

1. Extract the generic configured-Router one-shot invocation from the API layer into an internal provider-neutral utility service.
2. Add a compiled tool-free Burst Expander contract with strict `CreativeBrief -> VariantSpec[]` schemas; initially reuse the Router model assignment behind a clearly named compatibility choice.
3. Add a durable burst job and bounded provider scheduler for text/JSON outputs only.
4. Add a preview-only gallery with progress counters, item inspection, cancellation, retry, shortlist, and export manifest.
5. Add the single materializer for a dedicated output directory.
6. Add image generation and richer judging after the scheduler, accounting, and recovery contracts are proven.

The first performance target should be **100 validated text/JSON variants with bounded memory and no child sessions**, then 500. Measure time-to-first-item, p50/p95 item latency, total wall time, provider throttles, cost, event volume, UI frame time, cancellation latency, and recovery after restart.

## Likely attack points

- Reusing Router’s model assignment without accidentally expanding Router’s immutable naming authority.
- Keeping bulk job changes on the V3 mutation/outbox path rather than adding a side store or in-memory authority.
- Provider-specific throttle metadata leaking into generic orchestration.
- Unbounded item/event payloads overwhelming Pebble, realtime hydration, parent context, or the gallery.
- Workspace materialization path containment, symlink handling, collision behavior, and partial writes.
- Misleading UI language that conflates items, provider calls, workers, and durable agents.

## Relevant filepaths

- `swarmd/internal/tool/runtime.go` — current task schema and eligible agent types.
- `swarmd/internal/run/service_task_launch.go` — task argument parsing, manifests, and launch validation.
- `swarmd/internal/run/service_tools.go` — child preparation, parallel turns, prompt inheritance, worktrees, lineage, and aggregation.
- `swarmd/internal/permission/subagent_reservation.go` — durable wave budget and active-child reservation.
- `swarmd/internal/permission/policy.go` — default policy and maximum wave size.
- `swarmd/internal/agent/system_agent_registry.go` — immutable Router/Finder/Coder/Designer identities and tool contracts.
- `swarmd/internal/agentmodel/resolver.go` — canonical account-scoped system-agent model resolution.
- `swarmd/internal/api/session_router.go` — reusable shape of a strict tool-free configured-model invocation.
- `swarmd/internal/router/router.go` — Router’s current narrow naming schema and authority boundary.
- `swarmd/internal/api/image_generation.go` — existing image-generation API surface to integrate rather than duplicate.
- `swarmd/internal/api/sessions_v3_realtime_contract.go` — realtime contract that bulk progress must preserve.
- `web/src/features/desktop/state/` — canonical Desktop backend-derived state.
- `web/src/features/desktop/realtime/` — bounded live progress coordination.
- `web/src/features/desktop/chat/components/desktop-plan-subagent-list.tsx` — existing child-agent presentation, which should remain distinct from a burst gallery.

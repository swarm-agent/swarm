# Provider tool-construction adapter contract

This contract defines how a conversational provider adapter reports a model-authored tool call before Swarm executes it. Codex/OpenAI Responses is the reference adapter. Other providers must map their native stream into the same `provideriface.StreamEvent` lifecycle; Desktop presentation must not branch on provider identity.

## Authority and lifecycle boundary

Provider construction and Swarm tool execution are distinct lifecycles:

1. The provider adapter emits construction events while the model is assembling a call.
2. The V3 executor persists them as ordered `session.provider_tool_call.*` mutations on the same durable event timeline as preceding reasoning.
3. Swarm runtime execution emits `session.tool.started`, `session.tool.delta`, and a terminal `session.tool.completed`, `session.tool.failed`, or `session.tool.cancelled` event.
4. Desktop reconciles both lifecycles into one provider-neutral activity by stable identity. Durable V3 events, projections, replay, and reconnect hydration remain authoritative; provider tool construction must not use an unsequenced live-patch bypass.

Construction completion means the provider finished constructing arguments. It does **not** mean the tool executed successfully. Failure and cancellation are runtime terminal states unless a future provider-neutral construction-failure event is explicitly added.

## Required event mapping

Every adapter maps its native stream to these types from `swarmd/internal/provider/interfaces/runtime.go`:

| Provider-neutral event | Required meaning | Required content |
| --- | --- | --- |
| `response.tool_call.started` | Earliest point at which one logical call can be tracked without conflating it with another call | Stable `ToolCallID` when known; otherwise `ToolCallIndex`; `ToolName` when known; provider/model context; `StartedAtUnixMs`; `RecordedAtUnixMs`; status `started` |
| `response.tool_call.arguments.delta` | Append-only argument bytes/chunks | Same logical identity, `ArgumentsDelta`, stable start timestamp, current record timestamp, status `building` |
| `response.tool_call.arguments.snapshot` | Complete replacement snapshot of arguments | Same logical identity, `ArgumentsSnapshot`, stable start timestamp, current record timestamp, status `building` |
| `response.tool_call.completed` | Provider finished constructing the call | Stable call identity and tool name, final `Arguments`, stable start timestamp, current record timestamp, status `completed` |

An adapter may use deltas, snapshots, or both. Deltas are append-only. Snapshots replace the accumulated argument state. Completion carries the canonical final argument string.

## Ordering, identity, and repair guarantees

- Emit exactly one logical start before any argument or completion event reaches the provider-neutral callback.
- Emit at most one logical completion. Repeated native terminal/output snapshots must be deduplicated.
- Preserve native ordering among distinct calls and argument events.
- `ToolCallID` is the primary cross-lifecycle identity and must be the provider's stable call ID, not a newly generated UI ID.
- `ToolCallIndex` is the bounded provisional identity when a provider reveals an output slot before its call ID. It must remain stable for that response and must not be reused to merge unrelated calls.
- `ToolName` may arrive late. Repair the tracked call as metadata becomes available; do not emit a second start merely because identity or name became more complete.
- Provider-native item IDs belong in `Metadata` (for Codex/OpenAI, `provider_item_id`) and may be used internally to repair early argument events. They do not replace `ToolCallID` as the runtime reconciliation identity.
- Parallel calls require distinct stable IDs or distinct output indexes until IDs are known. An ambiguous same-step fallback must produce separate activities rather than merge calls.
- `ProviderID` and `Model` describe the backend-authoritative adapter context. React components must never inspect them to select presentation.

## Timing guarantees

- `StartedAtUnixMs` is captured once for the logical call at the first provider-neutral construction event and remains unchanged through arguments and completion.
- `RecordedAtUnixMs` is the observation time for each normalized event and must be monotonic for that call.
- If a native argument or terminal event arrives before full identity, buffer it until stable identity can be repaired; retain its relative ordering. Do not publish an identity-less event and later duplicate it.
- V3 persistence carries provider timestamps through replay. Realtime arrival time is not a substitute for the recorded provider-normalization timestamp.

## Durable V3 and Desktop requirements

- Persist construction through the canonical V3 mutation path as `session.provider_tool_call.started`, `.arguments.delta`, `.arguments.snapshot`, and `.completed`.
- Include run ID, step, event index, call ID, optional output index, tool name when known, arguments, provider/model, status, timestamps, and adapter metadata.
- Use deterministic idempotency identity so replay or retry cannot create duplicate durable construction records.
- Deliver provider construction only through ordered durable realtime events. Before the first construction start in a provider step, complete any active reasoning record so the durable sequence naturally places the tool below that thinking.
- Reconnect and cursor-gap repair replay or hydrate those same durable records; they must not reopen completed reasoning or re-anchor an existing tool row.
- Reconcile `session.tool.*` runtime events into the same Desktop activity using call ID/tool instance identity first and bounded run/step/output fallback only when unambiguous.
- Terminal runtime phases are monotonic. Late or stale construction replay must enrich identity/arguments but must never return a completed, failed, or cancelled card to a pulsing phase.
- Desktop semantic presentation (`edit`, `plan_manage`, `task`, or generic) is derived after provider-neutral normalization. Adding a provider adapter requires no React rendering change.

## Adapter conformance checklist

A new adapter is conformant when focused fixtures prove:

- start appears after preceding reasoning and before execution output for edit, plan, task/subagent, and generic calls;
- argument-first and terminal-first native shapes repair to ordered start/arguments/completion events;
- repeated native snapshots do not duplicate start or completion;
- parallel calls remain distinct;
- provider/model, stable call identity, output index, metadata, and timestamps survive durable replay;
- runtime success, failure, and cancellation resolve the same activity;
- reconnect/reordered replay leaves one terminal, non-animated card per call; and
- no provider-specific Desktop branch, retired runner route, legacy stream authority, or in-memory recovery fallback is introduced.

Reference implementation and focused fixtures:

- `swarmd/internal/provider/interfaces/runtime.go`
- `swarmd/internal/provider/codex/client.go`
- `swarmd/internal/provider/codex/runner.go`
- `swarmd/internal/provider/codex/provider_tool_construction_stream_test.go`
- `swarmd/internal/api/sessions_v3_provider_stream.go`
- `swarmd/internal/api/sessions_v3_provider_stream_test.go`
- `web/src/features/desktop/state/desktop-tool-activity.spec.ts`

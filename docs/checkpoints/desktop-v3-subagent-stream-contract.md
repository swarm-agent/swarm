# Desktop V3 subagent stream contract

This note locks the CP-6 migration contract for carrying delegated subagent progress through the V3 Desktop path without depending on legacy `/v1` or `/v2` streams.

## Backend emission points

- `swarmd/internal/run/service_tools.go`
  - `prepareDelegatedSubagentLaunch` creates a canonical child session before execution and records lineage metadata (`parent_session_id`, `lineage_kind=delegated_subagent`, `launch_source`, `requested_subagent`, `subagent`, `assignment_label`, model/provider fields).
  - `executeTaskToolWithParsed` launches subagents with `RunTurnStreaming` and emits parent task progress via `emitTaskStreamDelta` as the child reports step/tool/reasoning/assistant events.
  - `buildTaskStreamPayload` is the canonical parent task progress payload shape for live deltas (`path_id=tool.task.stream.v1`). Final task output uses `path_id=tool.task.v1` and includes all launch rows.
- `swarmd/internal/run/service.go`
  - Targeted `@subagent` runs use the same `prepareDelegatedSubagentLaunch` and `emitTaskStreamDelta` contract, plus forward selected child stream events to the parent live stream.
- `swarmd/internal/api/sessions_v3_executor.go`
  - `emitSessionV3ProviderToolEvent` and `recordProviderToolEvent` persist provider tool started/delta events as V3 session events (`session.tool.started`, `session.tool.delta`) on the executing session.
  - For child-under-parent multiplexing, these child events must remain canonical child-session events (`event.session_id` is the child ID) while being delivered to subscribers of the parent stream.

## Parent task stream payload schema

Live task deltas are `StreamEventToolDelta` frames whose `Output` is JSON:

- Top-level required/stable fields:
  - `tool: "task"`
  - `path_id: "tool.task.stream.v1"`
  - `action`, `status`, `phase`, `launch_count`
  - `description` and `goal`
  - `parent_session_id`
  - `summary`
  - `details_truncated`
  - `launches[]`
- Per-launch required/stable fields for Desktop rows:
  - identity: `launch_index`, `child_session_id` for stream deltas; final payloads may use `session_id`
  - agent labels: `requested_subagent`, `subagent`, `agent_type`, `meta_prompt`, `assignment_label`
  - model labels: `subagent_provider`, `subagent_model`
  - session/workspace: `child_mode`, `workspace_path`, `workspace_name`, `worktree_enabled`, `worktree_root_path`, `worktree_branch`
  - progress: `status`, `phase`, `launch_started_at_ms`, `current_tool`, `current_tool_started_at_ms`, `current_tool_ms`, `elapsed_ms`
  - previews/counters: `current_preview_kind`, `current_preview_text`, `reasoning_summary`, `tool_started`, `tool_completed`, `tool_failed`, `tool_order`
- Privacy/display rule:
  - Tool preview text may be surfaced.
  - Assistant/reasoning preview text is redacted from the stream payload; only kind/summary metadata may be exposed.

## Desktop parser/render expectations

- `web/src/features/desktop/chat/services/tool-message.ts`
  - Parses task payloads only by JSON shape and `tool === "task"`; it does not need legacy transport details.
  - `buildTaskToolRows` maps `launches[]` to task rows.
  - `buildTaskToolRow` accepts either `session_id` or `child_session_id` for child navigation identity.
  - Agent label fallback order includes `resolved_agent_name`, `requested_subagent_type`, `agent_type`, `subagent`, and `requested_subagent`.
  - Running rows keep timer anchors (`launch_started_at_ms`, `current_tool_started_at_ms`) and avoid formatting stale stream durations as final display time.
- `web/src/features/desktop/chat/components/chat-markdown.tsx`
  - Renders task rows as a subagent stream.
  - Uses dense/swarm rendering at `TASK_SWARM_THRESHOLD = 10` rows, which is the UI baseline for large fanout.

## Regression coverage added/required

- Backend:
  - `service_tools_stream_contract_test.go` locks live task stream top-level fields, per-launch fields, child session ID field, timing anchors, and assistant/reasoning preview redaction.
- Frontend:
  - `tool-message.spec.ts` locks parsing of canonical stream fields, model/assignment labels, timer anchors, `child_session_id`, and final-payload `session_id` alias.

## Migration implications for CP-8/CP-9

- V3 multiplexing should deliver child session V3 events under the parent stream without rewriting the event's canonical `session_id`.
- Parent task deltas can remain as compatibility summaries while child V3 event multiplexing is introduced.
- No Desktop work should introduce child stream fanout or `/v1`/`/v2` bootstrap/stream calls.

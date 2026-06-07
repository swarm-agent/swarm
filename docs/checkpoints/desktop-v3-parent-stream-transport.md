# Desktop V3 parent stream transport design

This note completes CP-8 for Desktop V3 delegated subagent streaming. It chooses the transport seam and locks the event/cursor/backpressure semantics for CP-9 implementation.

## Decision

Multiplex delegated child session progress at the V3 per-session stream hub seam in `swarmd/internal/api/sessions_v3_stream_ws.go`.

A Desktop route that is already watching the parent session should keep one parent session stream. The backend will route canonical child V3 events to subscribers of that parent stream. Do not use legacy `/v1` or `/v2` run streaming, and do not make Desktop open one websocket or snapshot request per child.

Rationale:

- CP-7 made delegated children real V3 sessions created through `ApplySessionMutation(SessionMutationCreateSession)`.
- The committed V3 event/outbox path is already canonical; child events must remain persisted on the child session.
- `sessions_v3_stream_ws.go` is the planned hub-level seam for parent route stream delivery. It can route committed child events to existing parent stream subscribers without changing child event identity.
- `/v3/realtime/stream` remains the broader native realtime outbox API, but current Desktop route-stream constraints and existing guards keep the focused parent stream separate from that API.

## Frame semantics

Child-under-parent delivery must not rewrite canonical event identity.

For a parent stream subscriber:

- Parent events keep current frame semantics.
- Child events are delivered as event frames with additional routing context.
- `event.session_id` remains the child session ID.
- `event.seq` remains the child session-local sequence.
- The frame should include enough context for Desktop routing, for example:
  - `session_id`: the canonical event session ID for event frames (child ID for child frames);
  - `parent_session_id`: the subscribed parent that caused delivery;
  - `relation`: `self` or `child`;
  - `lineage_kind`: `delegated_subagent` for delegated child frames.

Desktop must treat `parent_session_id` as routing context only. It must not mutate the child event's canonical session ID or derive child URLs from shaped IDs.

## Parent and child cursors

The parent stream's `after_seq`, `last_seq`, and high-watermark remain the parent session cursor. Child frames must not advance the parent cursor.

Maintain separate child cursors inside the stream loop:

- `lastParentSeq` for parent events and replay.
- `lastChildSeqBySessionID` for child events delivered under this parent subscription.

Gap behavior:

- A parent sequence gap is fatal for the parent stream and emits the existing parent `cursor.error` / refetch-required behavior.
- A child sequence gap emits a child-scoped `cursor.error` that includes the child `session_id`, `parent_session_id`, expected/actual child seq, and refetch-required reason.
- A child gap must drop or pause only that child cursor. It must not poison the parent cursor or force a full parent refetch.

Replay behavior:

- Initial parent replay remains `ReplaySessionEvents(parentID, afterSeq, limit)`.
- Child replay should be bounded. CP-9 can seed currently-known children and replay a capped child tail only when it is cheap; otherwise it must emit an explicit child refetch-required frame rather than silently dropping history.
- Live child events after subscription are delivered through the hub without DB lookups.

## Lineage index

Avoid per-event Pebble lookups.

Add an in-memory lineage index owned by the API stream/hub layer:

- `childID -> parentID/accountScopeID/userID/lineageKind`
- `parentID -> childIDs`

Register lineage when a committed `session.created` event creates a delegated child:

- `parent_session_id` is non-empty and not equal to child ID;
- `lineage_kind == delegated_subagent`;
- account/user scope is captured from the committed session/event payload.

On parent stream subscribe, the server may seed the lineage index once from already-persisted session metadata for that parent. This is subscription-bound catch-up, not publish-time lookup. Publish-time child routing must use only the in-memory index.

## Backpressure and batching

Use the existing parent stream subscriber queue as the hard safety boundary:

- If a subscriber queue fills, send the existing slow-consumer/reconnect-required cursor error and disconnect.
- Do not silently drop child progress.
- Do not fan out one socket/subscriber per child.

For large fanout (100-500 children), CP-9 may coalesce non-terminal high-frequency child deltas before enqueueing to the parent subscriber, but only if terminal events and cursor ordering remain exact. Coalescing must preserve:

- `session.created` and lineage-registration events;
- lifecycle/run intent terminal events;
- tool completed/failed events;
- final assistant/message events;
- explicit cursor/refetch errors.

## Account and authorization rules

Child delivery under a parent stream is allowed only when:

- the principal is authorized to see the parent session;
- the child lineage entry account/user scope matches the committed child event/outbox row;
- the child lineage points to that parent.

Unrelated child sessions, flow sessions, and cross-account events must never be multiplexed under the parent stream.

## CP-9 implementation targets

Likely files:

- `swarmd/internal/api/sessions_v3_stream_ws.go`
  - extend the hub/subscriber payload from bare `SessionEvent` to a routed event with parent context;
  - add the in-memory lineage index;
  - route child events to parent subscribers while preserving canonical child event IDs and child seqs;
  - update stream-loop cursor handling for separate parent and child cursors.
- `swarmd/internal/api/sessions_v3_outbox.go`
  - register delegated child lineage when committed V3 `session.created` events pass through the publish seam.
- `swarmd/internal/run/service_tools.go`
  - keep CP-7 canonical child creation metadata stable.
- Desktop state/stream code
  - consume parent stream child frames without child stream or snapshot fanout.

## Non-goals

- Do not reintroduce `/v1` or `/v2` run streaming.
- Do not make Desktop open child streams or child snapshot fanout for live progress.
- Do not rewrite child events as parent events.
- Do not add DB lookups on every published child event.

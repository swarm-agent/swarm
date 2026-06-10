# Session V3 workset contract

Status: CP-Redo-1 contract source of truth.

This document defines the load-time V3 workset API contract for Desktop. Backend storage/API is the source of truth for session selection, stable ordering, pagination, explicit omissions, and history manifests. Desktop must load a bounded workset page into a normalized cache and switch locally for sessions present in that cache.

## Endpoint

`POST /v3/sessions:workset`

The endpoint returns a normalized workset response. It is not a single-session snapshot API and must not be implemented by Desktop fanout across `GET /v3/sessions/{id}`.

## Request contract

```json
{
  "session_ids": ["session_123"],
  "workspace": {
    "workspace_path": "/workspace/path"
  },
  "recent": {
    "limit": 50,
    "before_updated_at": 1730000000000
  },
  "history": {
    "mode": "full",
    "max_messages_per_session": 200,
    "max_events_per_session": 500,
    "manifest_policy": "manifest"
  }
}
```

### Selectors

At least one selector must be present:

- `session_ids`: explicit canonical V3 session ids to include.
- `workspace.workspace_path`: canonical workspace path scope.
- `recent.limit`: request the most recently updated sessions visible to the caller.

Selector rules:

- `session_ids` preserves explicit inclusion semantics. The backend may still omit history for those sessions only through an explicit omission/manifest entry.
- `workspace.workspace_path` restricts selected sessions to the workspace path when used with `recent`.
- `recent.limit` is a positive page size. Backend may clamp it to a documented service maximum but must return pagination metadata for the actual page.
- `recent.before_updated_at` is the canonical page cursor. When present, the page includes sessions strictly before that cursor in the stable recent ordering.

### Stable recent ordering

Recent pages must be ordered by:

1. `updated_at` descending;
2. `session_id` descending as the deterministic tie-breaker.

A page cursor based only on timestamp can be ambiguous when multiple sessions share the same `updated_at`. The implementation may therefore encode a compound cursor internally or return an opaque cursor later, but CP-Redo requires `pagination.next_before_updated_at` as the public cursor field. If duplicate timestamps require tie preservation, the backend must also return enough stable cursor state to avoid duplicates/skips; it must not silently rely on nondeterministic store iteration.

## History request contract

`history.mode` controls how much per-session history the caller asks for:

- `none`: return session metadata/projections only.
- `tail`: return the newest bounded message/event tail.
- `full`: return complete history unless bounded by explicit history limits.

`history.max_messages_per_session` and `history.max_events_per_session` are hard per-session caps for inline history. If a session has more messages/events than these caps, the backend must not silently truncate. It must either fail or return explicit omissions/manifests according to `history.manifest_policy`.

`history.manifest_policy` values:

- `error`: if requested history cannot be returned fully inline within the per-session caps, the endpoint fails clearly.
- `omit`: return available session metadata and explicit omission entries for missing history.
- `manifest`: return manifest descriptors for history chunks that are not fully included inline.

`full` means complete unless a declared cap forces an explicit `error`, `omit`, or `manifest` result. Hidden tail caps are forbidden.

## Response contract

```json
{
  "sessions_by_id": {
    "session_123": {
      "id": "session_123",
      "session_api": "v3",
      "workspace_path": "/workspace/path",
      "updated_at": 1730000000000,
      "last_event_seq": 42,
      "projection_high_watermark_seq": 42
    }
  },
  "messages_by_session": {
    "session_123": []
  },
  "events_by_session": {
    "session_123": []
  },
  "plans_by_session": {},
  "plan_revisions_by_session": {},
  "permissions_by_session": {},
  "usage_by_session": {},
  "preferences_by_session": {},
  "agent_model_policy_by_session": {},
  "run_intents_by_session": {},
  "history_manifests_by_session": {
    "session_123": []
  },
  "history_chunks_by_id": {},
  "omissions": [],
  "pagination": {
    "next_before_updated_at": 1729999999999,
    "has_more": true
  },
  "watermarks": {
    "loaded_at": 1730000000100,
    "max_updated_at": 1730000000000
  }
}
```

The response must be normalized by session id/resource map. Backend/client/Desktop code must preserve this shape and must not flatten it back into one snapshot per selected session as the authoritative Desktop cache model.

### Pagination response

`pagination.has_more` indicates whether another recent page is available for the same selector scope.

`pagination.next_before_updated_at` is the cursor Desktop sends as `recent.before_updated_at` for the next page. It must be absent or null when `has_more` is false.

If a page boundary prevents inclusion of a session or history segment that Desktop asked for, the response must include an omission with `reason: "page_boundary"` and a `next_cursor` when there is a follow-up request that can retrieve it.

### History manifests

`history_manifests_by_session[session_id]` contains ordered chunk descriptors for history not fully included inline:

```json
{
  "chunk_id": "session_123:messages:1-200",
  "resource": "messages",
  "from_seq": 1,
  "to_seq": 200,
  "message_count": 200,
  "event_count": 0,
  "complete": true
}
```

Descriptor fields:

- `chunk_id`: stable id for the chunk inside this response/workset scope.
- `resource`: `messages`, `events`, or a future explicit resource name.
- `from_seq` / `to_seq`: inclusive primary session sequence bounds covered by the chunk.
- `message_count`: number of messages covered by the chunk.
- `event_count`: number of events covered by the chunk.
- `complete`: true only when the descriptor fully covers the stated sequence range.

`history_chunks_by_id[chunk_id]` contains chunks included in the response. A manifest may describe chunks not included inline; those missing chunks must be fetchable later through an explicit workset/chunk follow-up contract, not through ad hoc per-session snapshot hydrate fanout.

### Omissions

Every omitted selected resource must be represented explicitly:

```json
{
  "session_id": "session_123",
  "resource": "messages",
  "reason": "requires_manifest",
  "next_cursor": "session_123:messages:201",
  "manifest_ref": "session_123:messages"
}
```

Allowed `reason` values for CP-Redo:

- `requires_manifest`: omitted because the request requires manifest handling but manifests were not allowed or not requested.
- `page_boundary`: omitted because the resource belongs to a later page of the selected scope.

Rules:

- Omissions must identify `session_id` when session-scoped.
- Omissions must identify `resource`.
- `next_cursor` is required when the backend can provide a cursor for follow-up loading.
- `manifest_ref` is required when a manifest describes the omitted history.
- No hidden tail caps: if data is not complete, the response must say so.

## Desktop load and switch rules

Desktop initial load must call `POST /v3/sessions:workset` with an explicit selector and history policy, then seed a normalized client-side cache from the response.

For sessions already present in the loaded workset and not explicitly omitted for the requested render path:

- sidebar selection is local lookup plus navigation only;
- route switching is local lookup plus render only;
- hover behavior must not hydrate that session snapshot;
- store recovery must not call per-session authoritative hydration.

Desktop may fetch during switch only when one of these is true:

1. the selected session is outside the loaded workset;
2. the selected session/resource is explicitly omitted;
3. realtime cursor/gap recovery requires a scoped workset refetch using returned pagination/watermark/cursor information.

Those follow-up fetches must use the workset/page/chunk contract. They must not restore the previous authoritative path of `GET /v3/sessions/{id}` per sidebar click for sessions already listed in the workset.

## Implementation attack points for later checkpoints

- Backend API: add `POST /v3/sessions:workset` route/handler with typed request and normalized response structs.
- Pebble/store: add stable recent selection, pagination metadata, history manifest/chunk construction, and explicit omissions.
- Go client: add typed request/response structs preserving normalized maps, pagination, manifests, chunks, and omissions.
- Desktop cache: replace snapshot-per-session authoritative switching with one load-time normalized workset seed and local selectors.
- Desktop layout/store: remove `routeSessionSnapshotQuery`, switch-time `hydrateDesktopV3SessionSnapshot`, hover prefetch hydration, and store-triggered `requestAuthoritativeSessionSnapshot` from the cached-session switch path.

## Validation targets

Checkpoint 1 is complete when this contract is present and later implementation checkpoints can test against it. Later tests must prove:

- first page and next page behavior with stable ordering;
- budget exceeded without manifest fails or explicitly omits according to policy;
- budget exceeded with manifest returns descriptors and included chunks consistently;
- per-session history caps produce explicit omissions/manifests, not hidden truncation;
- Desktop performs one load-time workset request and local-only switches for cached sessions.

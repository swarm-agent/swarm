# V3 activity stream (implementation preview)

`GET /v3/activity/stream?cursor=0&project=...&worker=...` projects metadata from the existing durable V3 realtime outbox. It requires the same principal and `sessions:read` scope as session reads; each session is checked again before inclusion. The stream is capped at 32 concurrent connections, with the existing 256-record bounded outbox subscription queue. Writes have a five-second deadline; a disconnected or permanently blocked transport cannot be guaranteed to receive its gap notification.

SSE frames are `activity`, `cursor`, or `gap`. Persist cursor strings, not SSE arrival counts. `gap` contains `from`, `to`, and a recovery `cursor`; reconnect with that cursor. Heartbeat comments are scheduled every 15 seconds. A cursor ahead of the log or before retained history produces a gap. The cursor space is the V3 outbox sequence, not the legacy signals sequence.

`GET /v3/activity?cursor=...` provides one bounded replay page. MCP `swarm_activity` accepts `since_cursor` for this page; its existing `after` option remains the separate legacy signal feed.

The SDK exposes `client.activity.stream({cursor, project, worker, signal})`, yielding metadata events and explicit gap records, reconnecting after clean stream closure. This initial implementation requires HTTP, not Unix socket transport. Network-exception retries and Unix socket streaming remain outstanding.

## Current limitations / not ready for release

This implementation is not yet verified. Tests were attempted but sandbox execution failed or timed out; no latency numbers are claimed. Lifecycle projection currently covers session creation, assistant/run transitions and tool start/completion/failure. Review, delivery and landing producer integration is incomplete: projection cases alone do not establish that canonical producers emit those events. Worker/job identity mapping needs verification against canonical worker bindings. Dedicated permission-scope, expired-retention, heartbeat, transport-overflow and SDK reconnect tests remain necessary before release.

The latency test measures actual in-process durable session creation through SSE receipt, including connection setup; it is not a provider/AI benchmark.

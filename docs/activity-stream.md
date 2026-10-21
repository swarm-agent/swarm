# V3 activity stream

`GET /v3/activity/stream?cursor=0&project=...&worker=...` is a metadata-only SSE feed over the existing durable V3 realtime outbox. It uses the same authenticated principal and `sessions:read` scope as session reads, checks account/user ownership, and checks session read access for session-bound records. No second event store is created.

Each `activity` frame contains `seq`, `at` (Unix milliseconds), `kind`, `session_id`, `job_id`, `worker_id`, `worker_name`, `project_id`, `state`, and `summary`. Unavailable identity fields are empty strings. `seq` increases in the global outbox sequence; filtered activity need not have consecutive sequence numbers. Summaries are fixed event labels, never provider text. Tool events add `tool_name` and, for read/write/edit, `file_path`; arguments, output, file contents, prompts, messages, review reasons and publish output are excluded.

## Delivery and recovery

- At most 32 simultaneous streams per daemon, with a 256-record bounded subscription queue each. Excess streams receive HTTP 429.
- An initial `: connected` comment flushes the subscription immediately; idle connections receive `: heartbeat` every 15 seconds.
- `cursor` frames advance the replay position across filtered-out records. Persist their decimal-string `cursor` value and reconnect with `?cursor=...`.
- Overflow emits `gap` with `from`, `to`, and the last delivered `cursor`, then closes. Reconnecting replays durable events. Expired/ahead cursors also emit `gap` and close, with a recovery cursor at the current head; historical loss must be displayed, not hidden.
- Writes have a five-second deadline. A disconnected or permanently blocked network cannot be guaranteed to receive a gap frame; reconnect with the last received cursor. Producers never wait for clients.
- Project and worker filters are exact IDs. Filtering is not an authorization mechanism.

`GET /v3/activity?cursor=...` returns one bounded replay page: `events`, `cursor`, and `gap`. MCP `swarm_activity({since_cursor:"0"})` uses this page and requires `sessions:read`. Its older `after` option remains the separate signal feed; do not mix those cursor spaces.

## SDK

`client.activity.stream({cursor, project, worker, signal})` is an async iterator yielding activity events and explicit `{kind:"gap", ...}` records. It reconnects after EOF or network failures, resumes the last cursor, and stops on authorization/protocol errors or cancellation. Reconnects are delayed one second, not activity polling. This SSE implementation currently requires HTTP; Unix-socket SDK transport is not supported.

## Producers and limits

Session/tool/run events use their existing session envelopes. Worker run changes and recorded reviews carry metadata in the same durable batch as their existing worker/review records, with the existing worker realtime publisher. Delivery and pull-request transitions append idempotent receipts to the owning session's event log/outbox. External Git effects and their receipt are not one atomic transaction; receipt errors are surfaced and operations may require reconciliation after a crash. This feed is not proof that Git delivery happened unless the underlying delivery authority recorded that outcome.

Focused tests exercise metadata exclusion, account/scope checks, stream caps, cursor replay/expiration, overflow plus close, heartbeat timing, durable worker/review receipts, delivery idempotency, MCP and SDK resume. The latency test commits real in-process session mutations after opening an SSE subscription and measures through client receipt; it is not an AI/provider benchmark. Measured results belong in the validation handoff, not a permanent performance guarantee.

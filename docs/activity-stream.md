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

## The fleet's activity

`GET /v3/fleet/activity/stream?cursors=home:12,box:40` on a home is one SSE feed that merges the home's own activity (the same in-process subscription and outbox, no second store) with each linked machine's `/v3/activity/stream`, read through the link as that machine's home. Same scope (`sessions:read`, owner sessions and keys); gateway tokens are refused. `cursors` holds one `machine:cursor` pair per machine (the home's own name is `name` from `GET /v3/fleet`); a machine left out starts at 0, as above, unless `start=now` is given: then each machine without a cursor starts at its current head (no history replayed, no gap), which is what a live view wants.

- Only machines whose `linked.{name}.work` is `ask` or `allowed` are read. Under `off` a machine is not contacted at all and is announced once: `{"kind":"machine","machine":"box","state":"not_shown","reason":"linked work rule is off"}`. Links and rules are read again at each heartbeat, so a rule turned off stops that machine's reading within 15 seconds and a new link joins.
- `activity` frames are the per-machine record plus `machine`: `{"machine":"box","seq":41,"at":1793000000000,"kind":"session.tool.started","session_id":"…","job_id":"","worker_id":"","worker_name":"","project_id":"…","state":"started","summary":"session.tool.started","tool_name":"read","file_path":"main.go"}`. A machine's frames are re-encoded from the known metadata fields only, with strings clipped to 1024 bytes; nothing it sends is acted on.
- `cursor` frames: `{"kind":"cursor","machine":"box","cursor":"41"}`. Persist the latest per machine and reconnect with all of them.
- `gap` frames: `{"kind":"gap","machine":"box","from":12,"to":40,"cursor":"40"}`. A gap does not close the fleet stream: that machine resumes from the gap's cursor about a second later (followed by its `cursor` frame), and the loss is still shown.
- `machine` frames: `online` when a machine's stream opens (the home is announced `online` at the start), `offline` with a short `error` (an HTTP status or this home's own transport error, never the machine's text) when it cannot be reached, drops, or sends nothing for 45 seconds. The home retries with backoff from 2 to 60 seconds without holding up the others, and says `online` again when it is back.
- It counts as one stream against this daemon's 32; each upstream is one stream on its machine (a machine at its limit shows `offline` with `HTTP 429`). Heartbeats are the same `: heartbeat` comment every 15 seconds. The fleet stream closes only when the client goes, a write fails, or the home's own feed fails.

`GET /v3/fleet/activity?cursors=...` is the same merge as one bounded replay page per machine, read in parallel (10 seconds each): `{"events":[…tagged, ordered by at…],"cursors":{"home":"12","box":"41"},"gaps":[{"machine":"box","cursor":"40"}],"machines":{"home":"online","box":"online","lab":"not_shown"}}`. A machine that does not answer is `offline` and keeps the cursor it was asked with.

Limits: a machine is shown under the name this home linked it by, not the name it gives itself; a linked machine with the home's own name is `not_shown`. The page and stream read each machine's own feed only (not that machine's linked machines). A machine that is unlinked while the stream is open is announced `not_shown` with reason `unlinked`.

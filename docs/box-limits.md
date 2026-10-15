# Box limits

`GET /v3/limits` reads the current account's box-local limits and revision.
`PUT /v3/limits` replaces them with `{ "expected_revision": 0, "limits": { ... } }`.
Read first, retain every field, and use the returned revision. Stale writes return
409; invalid values return 400 without changing the record. Account identity is
always taken from authentication. Scoped keys need `usage:read` (or `usage:write`)
to read and `usage:write` to change limits; a session grant alone is insufficient.

MCP exposes `swarm_get_limits` and `swarm_set_limits`. The SDK exposes
`client.limits.get()` and `client.limits.set(revision, limits)`.

| Field | Default | Hard bounds |
| --- | ---: | --- |
| worker_job_concurrency | 100 | 1–10000 |
| job_tokens | 0 (unset) | 0–1000000000 |
| job_cost_usd | 0 (unset) | 0–1000000 |
| delegation_waves | 5 | 0–256 |
| active_children | 5 | 1–256 |
| swarm_children | 5 | 1–256 |

Worker concurrency counts admitted and running jobs across this account's workers.
An excess admission is refused, not queued; retry after capacity is released.
Idempotent replay returns the original receipt even at capacity. Lowering a limit
does not terminate jobs already admitted. Limits are read at the next admission or
billable operation without restarting the daemon.

Job budgets use canonical worker-run usage including attributed descendant usage.
They stop the next billable operation, not an invoice: operations already in flight
can overshoot before receipts arrive. Unknown pricing refuses operations under a
cost limit. Existing account/day and per-worker/day budgets remain additive and
are not reset by editing this record. Shared execution capacity and target placement
limits remain separate, additional admission constraints.

Before the first limits write, existing delegation policy remains authoritative;
a limits write makes the three delegation numbers authoritative. Delegation mode,
over-budget approval behavior, isolation, and other permission settings are not
changed. Delegation caps remain approval-free allowances, not permission bypasses.

Worker concurrency and job-budget refusals emit `limit.reached` to the machine
signal feed, with `attrs.limit` and `attrs.value`. Bounded delegation refusals or
approval holds emit the same kind. Refusals also preserve the limit name/value in
the error text. Signal readers use the existing account-filtered signal cursor API.

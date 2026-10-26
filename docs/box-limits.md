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
signal feed, with `attrs.limit` and `attrs.value`. Repeated refusals emit only one signal per
crossing (per job for budgets); observing usage below the limit re-arms it.
Crossing state survives restart, and signal publication is idempotent. Bounded delegation refusals or
approval holds emit the same kind. Refusals also preserve the limit name/value in
the error text. Signal readers use the existing account-filtered signal cursor API.

### Reserved helper budgets

The limits record also stores `helper_concurrency` (default 4, range 1–256),
`helpers_per_job` (20, 1–10,000), `helper_timeout_seconds` (300, 1–3,600), and
`helper_tokens` (32,000, 1–1,000,000,000). Existing records inherit these defaults.
Worker-Coder task launches accept Finder helpers only in the job checkout.
Helper admission counts reservations across turns; completion releases active
capacity, not the per-job total. Helpers cannot delegate or use mutation tools.
Timeout cancels the child; token stopping uses provider receipts and therefore
is not a hard pre-dispatch token ceiling. Finder/helper bash requires an active
sandbox and accepts only fixed system readers with literal arguments (no shell
syntax, executables from the worktree, or arbitrary flags). Three-helper live
provider fan-out has not yet been validated.

Helper `helper_tokens` is a cumulative budget enforced after the provider's
usage receipt. An over-budget helper is stopped and reported at the next step;
the in-flight response may exceed the budget. No pre-dispatch token estimate is
used.

### Worker daily spending limits

MCP `swarm_deploy_worker` accepts `budget: {"daily_usd": 10, "daily_tokens": 100000}` (tokens optional); omission preserves the policy and `budget: null` clears its limits. Zero means unset, not a zero-spend cap. `automations:write` is sufficient. `swarm_list_workers` includes the worker's policy, today's indexed UTC spend, and blocked status/reason. Account limits still apply; changing a policy does not erase usage, reservations, or existing budget holds. Limits stop subsequent model calls, not an already-running call's final invoice. Native `manage_workers` accepts the same budget on create/propose/update; the budget is part of the worker proposal and takes effect only when the person accepts it. Existing-worker budget updates also require acceptance. Acceptance commits the policy and worker together through the canonical budget service's shared persistence boundary.

Claude Code and Codex model usage counts as $0 subscription cost toward worker USD budgets; media generation and priced API usage still count at their price, tokens still count for every provider, and unknown pricing on other providers still fails closed.

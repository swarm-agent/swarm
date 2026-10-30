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
| stall_after_minutes | 20 | 1–1440 (0 = default) |
| run_silent_minutes | 30 | 1–1440 (0 = default) |

`stall_after_minutes` and `run_silent_minutes` set when an unfinished task
counts as stalled (docs/task-progress.md, "Stalled tasks").

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

Claude Code and Codex model usage counts as $0 subscription cost toward worker USD budgets; media generation and priced API usage still count at their price, tokens still count for every provider, and a new model call whose provider pricing is unknown is still refused under a USD limit. A new media request whose price is unknown is admitted at its unpriced ceiling instead (below).

### Unpriced receipts under daily USD limits

A receipt Swarm could not price is not free, and it does not stop all work either. The account daily limit (`/v3/usage/limits`) and each worker's daily USD limit count every unpriced receipt at a conservative estimate, recorded with the receipt, and stop new work only when today's known cost plus those estimates reaches the limit. The estimate is:

- a model turn: its billed tokens at the highest input and output token rates known for the model's family (catalog models of the same provider whose id starts with the same name, such as `gemini`), else for its provider, else for any provider;
- a media receipt: a fixed ceiling per kind, $0.50 for an image, $1.00 for audio and $10.00 for a video (or any other kind);
- a receipt recorded before estimates existed: $1.00.

Admission uses the same rules before generation. A worker media request (image, video, sound, speech) is priced from the catalog before a job is created; token-priced speech (Gemini TTS) uses upper bounds from its request: prompt tokens of one per three UTF-8 bytes of the script, style and language plus 64, and audio tokens of 25 per second at a slow 10 bytes of script per second. Its reservation is that estimate, and the real receipt settles it. A media request Swarm still cannot price is admitted and reserved at the media receipt ceiling of its kind, once per requested output, and refused when today's known cost plus unpriced estimates, open reservations and that ceiling would exceed the account or worker daily USD limit. A per-job `job_cost_usd` limit still refuses unknown pricing.

The estimate does not create a day hold: raising the limit, or a receipt being priced later, admits work again. `GET /v3/usage/limits` (and the `limits` of `GET /v3/usage`, shown by `swarm_get_usage`) reports `today_unpriced_receipts` and `today_unpriced_estimate_usd`; worker usage and worker budget status report `unpriced_estimate_usd`. `today_cost_usd` and `limit_exceeded` stay known cost only.

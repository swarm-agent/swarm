# Automation HTTP integration contract

`/v3/automations` uses the authenticated daemon listener and trusted request principal, never JSON attribution. `handleAutomations` binds explicit user origin and rejects a pre-bound agent/system identity. `PolicyApproval` rechecks current account membership and workspace ownership. Missing services fail closed.

## Reads (GET)

All reads require `workspace_id`. Limit defaults to 20, maximum 50.

- `list`: definition heads, optional automation `id`, query and opaque cursor.
- `search`: optional automation `id`, kind (definition/occurrence/context/audit), query and cursor. `{records,next_cursor}`; empty pages may continue. Search is not chronological; clients may group only the loaded page by date.
- `get`: automation `id`, optional kind/record_id, optional exact positive revision. Returns `{records,next_before}` with at most one row.
- `history`: exclusive `before` revision, kind and record_id; newest first. Zero next_before ends pagination.
- `context`: `{context:{Trust,Revision,UserInstructions,Summaries}}`. Agent summaries are untrusted evidence, never authorization.
- `policy`: current definition `record`, computed `policy_sha256`, and nullable linked `approval`. `CurrentGrant` checks workspace ownership and exact automation/scope linkage. Grant revision is independent of definition revision; revoked grants remain readable for status. Reads grant nothing.

## Mutations (POST)

Strict single JSON object, maximum 64 KiB; unknown fields rejected. Common fields: action, workspace_id, id, mutation_id, expected_revision.

- `save`: full definition; zero revision creates, otherwise CAS. Canonical approved plans and immutable document pins are checked. Enabling checks live policy.
- `enable`/`pause`: full definition CAS after reading the current head.
- `context`: complete user_instructions replacement; CAS refers to context revision. HTTP users cannot author agent summaries.
- `run`: positive `scheduled_at` UTC epoch milliseconds fixed once per gesture; mutation_id identifies that trigger. Expected revision is the definition revision. Reuse the exact body on retry. HTTP 202 means durable admission, not dispatch or success.
- `event`: exact source, identity and scheduled_at; authenticated local approved-policy event authority checks approving user, current definition and configured source. No public webhook or source-string-only authentication.
- `cancel`: occurrence_id and exact occurrence revision. `ExecutionService.Cancel` durably fences dispatch before canonical runtime cancellation; stop failure stays nonterminal and retains serialize reservations. Retry the original mutation identity and revision.
- `/approve`: explicit user gesture with definition revision and reviewed policy digest. Returns a grant; it does not enable or link the definition. Link approval_reference and approved_policy mode using a subsequent definition CAS. Partial failure must remain visible.
- `/revoke`: explicit user gesture with approval_reference and **grant** revision, not definition revision. Revocation blocks future authorization; it is not cancellation of an existing execution.

Save/context return `{record,fresh}`; approval routes return `{approval}`. Cancellation returns `{record,fresh:false}` (not replay evidence). Errors are redacted: 400 invalid, 401 no principal, 403 denied, 404 absent reference, 409 conflict, 503 unavailable, 500 unexpected failure.

## Integrated execution and Desktop

`runtime/daemon.go` composes policy, domain, V3 execution host, event authority, API/tool adapters and durable publication. `runtime/automation.go` owns bounded scheduling/recovery and independent outcome/notification reconciliation. Session creation/checkpoints cross canonical V3 mutation authority with managed worktree isolation and pinned plan documents. Automation persistence and metadata-free account-workset `automation.updated` outbox entries commit atomically; Desktop hydrates bounded reads after invalidation/reconnect, not polling.

`manage_automation` retains agent identity. User management operations return non-applied proposals; dispatch is limited to already-admitted occurrences. Context writes attribute exact occurrence/session evidence and cannot replace user instructions.

Desktop configuration reviews saved policy and performs approve→link CAS; expiry is editable. Linked grants can be revoked after reload. Main workspace navigation opens the updates-first automation route. Linked plan conversations provide management chat without a new session authority. Audit outcome occurrence_id is resolved through scoped get before opening its session; facts.session_id is not trusted. Canonical conversation artifact galleries own typed artifact inspection; no duplicate automation artifact cache is introduced.

## Explicit gaps and validation

No delete/tombstone API. No complete daily digest, dedicated management-session creation, automatic application of chat proposals, or rich deliverable index on outcome cards. Approval and link are two operations: failed linking can leave an unlinked expiring grant; no background retry or false success. Browser usability, full UI workflow, live schedule/restart, socket authentication, provider execution and deployment have not been validated by this source audit. Tests/builds/browser trials not run; parent validation required. Source assertions are not execution evidence.

## Bounded schedule/progress read

`GET /v3/automations?action=progress&workspace_id=…&id=…&display_timezone=UTC`
returns `{progress: …}` from `automation.Service.ScheduleProgress`; agents call
that same service with their bound principal. `display_timezone` is an explicit
IANA civil-day timezone (not `Local`); it does not change interval execution or
cron policy. All numeric times are Unix milliseconds. `definition_revision`,
`schedule`, and optional `interval_anchor` identify the current immutable policy.
`day_start`/`day_end` are UTC instants bracketing the local day, not a 24-hour quota.

`planned_slots` enumerates enabled definition validity windows (at most 50
revisions), retaining earlier pinned revisions across edits. Every slot remains
a forecast, not an admission promise. `upcoming_slots` enumerates the current
policy over 48 hours, capped at 100; `forecast_horizon_end` and
`upcoming_complete` expose that bound. `forecast_complete` applies to today's
revision-window enumeration only. `next_eligible` is conditional on current
checks, expiry and observed overlap; future approval/revocation, missed-run,
shared-target and daemon availability decisions still belong to admission.
`no_next_reason` distinguishes pause, approval, expiry, manual/event schedules,
unknown overlap/history, unavailable plan/authorization and horizon exhaustion.

`counts` and `manual_counts` count unique occurrence IDs by their scheduled-day
cohort and current durable state; only `completed` is confirmed completion.
Retries/revisions do not add to the count. Legacy scheduled trigger hashes are
verifiable; other legacy triggers remain `unknown_trigger_count`, never silently
manual. New admissions persist immutable `trigger_kind`. Occurrences retain their
pinned definition revision. At most eight 50-result/512-key pages are scanned,
including empty continuation pages. `history_complete=false` means all counts and
`latest_recorded` are partial observations, **not totals or globally latest**.

`as_of` is the read-start clock; `freshness=non_atomic_read` explicitly disclaims a
transaction snapshot. Consume canonical automation durable invalidations and
reconnect replay to refetch, plus initial hydration/explicit refresh. No network
poll loop or frontend cron evaluator is part of this contract. Local clock-only
presentation may mark an old-day projection stale until a canonical refetch.

`recorded_at` is the occurrence state write, not execution start or completion.
Actual start/completion timing and unrecorded skipped/missed totals are explicitly
unavailable. Audit summary/details are not scanned by this projection; use the
existing paginated audit history. Absence of an occurrence never proves a skip,
success or scheduler health. Daemon-loop composition remains a separate concern.

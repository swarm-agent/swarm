# Automation HTTP integration contract

`/v3/automations` uses the existing authenticated daemon listener and trusted request principal. Account and user attribution are never JSON inputs. Domain Access must resolve current workspace ownership for every operation. Configure once at startup with `Server.ConfigureAutomations(domain, execution, cancellation, eventVerifier)`. Missing domain returns 503; missing execution/cancellation/event adapters fail closed.

## Reads (GET)

All reads require `workspace_id`. `limit` defaults to 20, maximum 50.

- `action=list`: definition heads; optional `query`, opaque `cursor`.
- `action=search`: optional `id` (automation), `kind` (definition/occurrence/context/audit), `query`, opaque `cursor`. Response `{records,next_cursor}`. Empty pages can have a continuation. Never synthesize cursors or assume chronological search order.
- `action=get&id=...`: optional `kind`, `record_id`, exact positive `revision`; absent revision returns head. Response `{records,next_before}`; records contains at most one record.
- `action=history&id=...`: optional `kind` (default definition), `record_id` (default automation ID), `before` exclusive revision. Response `{records,next_before}` newest first. Zero next_before ends pagination.
- `action=context&id=...`: `{context: {Trust,Revision,UserInstructions,Summaries}}`. Summaries are untrusted attributed evidence, not permission grants.

## Mutations (POST)

Strict single JSON object, 64 KiB maximum; unknown fields rejected. Common fields: `action`, `workspace_id`, `id`, `mutation_id`, `expected_revision`.

- `save`: full `definition` in canonical AutomationDefinition shape. Expected revision zero creates; positive revision edits. Enable/disable and 1–16 ordered plan associations are full revision-guarded definition edits. Save checks canonical approved plan revisions and document pins; enabling also checks live approval policy. No hard deletion exists in the foundation; disable preserves audit history.
- `context`: complete replacement `user_instructions` map; expected revision refers to context. HTTP users cannot write agent summaries or attribution.
- `run`: `scheduled_at` fixed UTC epoch milliseconds chosen once per user gesture; mutation_id is the stable manual trigger identity. Expected revision is the definition revision. Reuse the exact body on retry.
- `event`: `source`, `identity`, fixed `scheduled_at`; expected revision is the definition revision. Requires both authenticated local principal and injected payload-bound EventVerifier, followed by domain TriggerAuthority. No unauthenticated public webhook. Credential format belongs to the configured adapter, not this API.
- `cancel`: `occurrence_id`; expected revision is the exact occurrence revision. Injected cancellation adapter must authorize and durably stop canonical V3 execution before recording cancellation. No adapter returns 503.

Save/context return `{record,fresh}` with immutable attribution; admission returns HTTP 202 `{record}` (durable pending, NOT execution success). Cancellation returns `{record,fresh:false}`; clients must not infer replay state from that field. Errors are redacted: 400 invalid, 401 no principal, 403 denied, 404 absent exact reference, 409 stale/changed replay, 503 missing adapter, 500 unexpected failure.

## Remaining composition responsibilities

The integrated foundation has no delete/tombstone, cancellation implementation, scheduler loop, V3 runtime adapter, or durable automation outbox. This transport deliberately does not fabricate these authorities or publish ephemeral notifications. Daemon integration must supply live Access/TriggerAuthority, payload-bound event verification, cancellation, recovery and durable V3 publication/hydration. HTTP admission does not dispatch inline. Consumers must not add polling to hide the missing outbox integration. Automation history is hydrated through the bounded reads above; execution sessions remain canonical V3 resources.

Parent must reconcile docs/swarm-atlas.md and docs/testing/test-audit-ledger.tsv (outside this job's ownership). New request tests verify ownership/attribution rejection, stale no-write, forged-event no-occurrence, verified replay uniqueness and strict body bounds using real Pebble with fake external authority. They do not establish real authentication middleware, approval adapters, cancellation, outbox or V3 worktree safety. Tests/build/formatter not run; parent validation required.

## Consumer wiring update (partial; cancellation dependency blocked)

HTTP now binds the verified request principal using BindRuntimeIdentity and rejects
pre-bound non-user origins. ConfigureAutomationApproval(policy) is startup-only
and independent of ConfigureAutomations order. Daemon must call that setter with
its concrete PolicyApproval. GET action=policy returns the current definition
record and computed policy_sha256 for review. POST /v3/automations/approve uses
expected_revision (definition) and that digest; POST /v3/automations/revoke uses
approval_reference and expected_revision (grant). Both require the common envelope
fields and explicit authenticated user origin. Approval returns a grant, not an
enabled definition; save its reference and approved_policy mode via a subsequent
CAS. enable/pause read the current definition then perform SaveDefinition CAS;
enable retains live execution approval checks. Manual admission remains HTTP 202.

Exact cancellation cannot safely be wired to current ExecutionService.Cancel:
that method accepts no expected revision and re-reads the head before external
stop side effects. A transport pre-read would be a TOCTOU check, not exact CAS.
The existing injected cancellation interface remains fail-closed (503 when nil).
Parent must add an exact-revision execution cancellation boundary in
internal/automation/execution.go and its runtime fence, then wire the API. Those
files are outside this job's mutation scope. No implementation of that missing
contract is claimed here. CI event verification also remains a separate adapter.

Tests/formatter not run; parent validation required. Proposed focused command
from swarmd/ with Go and repository FFF prerequisites:
`go test ./internal/api -run '^TestAutomationHTTP' -count=1 -timeout=60s`.
Parent must update atlas route/boundary evidence and test inventory outside scope.

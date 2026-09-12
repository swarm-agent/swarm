# Typed automation proposal boundary

Structured `SessionPlanDocument.automation` contains `scope`, `automation_id`,
`definition_revision`, `existing`, and the complete paused `definition` (including
schedule/timezone, expiry/tools/targets, session identity and executable plan pins).
Create/save the paused definition first using the existing definition CAS. Pins
refer to separately approved executable plans, not the proposal itself. Ordinary
plan acceptance rejects this typed intent instead of starting a one-time run.

Explicit authenticated user acceptance uses `POST /v3/automations/approve` with
existing `workspace_id`, `id`, `expected_revision`, `policy_sha256`, plus `proposal`:
`{session_id, plan_id, revision, document_sha256}`. The grant, permanent session
binding and approved proposal revision commit together through V3. This does not
enable execution: use the existing explicit approved-policy enable CAS afterward.
Historical plan revisions and artifacts remain retained. Retry the exact proposal
reference; a changed reference is not an idempotent retry.

Agent consumers bind the actual agent session using `BindRuntimeIdentity` and call
`Service.EditParentDefinition`. Only a canonical Plan sidechat can edit its own
parent's stored definition. This capability cannot create, approve, enable, change
parent identity or repin executable instructions. It forces paused/fresh approval
and preserves old occurrence pins. It is a domain capability, not a public HTTP
route and not permission to relax existing sidechat tool allowlists.

Parent integration still needs tool/run and Desktop consumption. Parent-owned
atlas and test audit ledger updates are outside this child scope. All new tests
are authored only: not run; parent validation required. Format changed Go files
and run focused tests before integrating consumers.

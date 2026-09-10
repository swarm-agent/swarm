# Automation composition handoff

This change is partial integration, not a runnable automation product.

- Runtime registers `manage_automation` schema/dispatch/normalization. Principal derives from authenticated workspace scope; writer identity is the calling session with role agent. Bounded reads use domain policy. update_context delegates occurrence ownership and immutable user locks to the domain.
- Primary compiled agent receives capability; restricted contract denies it. Permission identities separate read/change/run/cancel. These identities are not approval records.
- Daemon composes one domain service against its existing Pebble and canonical SessionStore, shared by API and tools. Current Access deliberately permits only live catalog-owned reads. All mutations remain denied. No goroutine, alternate shutdown path or polling was introduced.
- save/pause/enable/run/cancel are explicit unavailable tool operations, not fake successes. Definition bodies/user grants are not accepted by this partial schema. The existing domain requires user role for definition edits/manual admission; impersonating a user after generic tool approval would violate that boundary.
- No notification delivery was added: the prerequisite contains no durable automation outbox. Calling notification delivery inline would lose restart durability. Consequently notification cannot rewrite an execution outcome in this integration.

## Required next implementation boundaries

1. Canonical user-approved automation proposal/application adapter (not a tool boolean), bound to exact definition/plan/target revision; occurrence owner adapter for agent summaries. Replace read-only Access only after these exist.
2. Idempotent session-owned managed-worktree V3 ExecutionRuntime.Ensure, pinned canonical checkpoint installation, cancellation and durable schedule/recovery cursor. API dependency explicitly reports these missing; interface comments are not implementations.
3. Atomic automation mutation/outbox and independently acknowledged notification delivery, with injected delivery failure/restart tests. Do not infer execution completion from delivery.
4. Parent updates atlas and test audit ledger outside this child's scope. No critical runner promotion. Check broader tool catalogs/run-layer capability allowlists outside tool/agent ownership before claiming provider availability.

## Parent inspection / validation

Inspect runtime_manage_automation.go ingress, WorkspaceScope principal resolution, automationAccess read-only guard, daemon shared-service lifetime and permission buildPolicyEvalContext identities. New tests assert strict schema/unknown authority and bounds, separated action identities, compiled primary capability, and missing adapter/cross-account no-side-effect rejection.

Working directory: repository swarmd module. Prerequisites: repository Go toolchain and native FFF dependencies. Proposed focused command (only after user permits execution):

`go test ./internal/tool ./internal/agent ./internal/permission ./internal/runtime -run 'TestAutomation(ToolIngress|PrimaryCapability|PolicyIsolation|CompositionFailClosed)$' -count=1 -timeout=120s`

Formatting also not run. Status: not run; parent validation required. Explicit user request prohibits running tests now.

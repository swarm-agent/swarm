# Automation daemon integration

## Implemented composition

`runtime.New` constructs `PolicyApproval` with live account-user membership,
workspace catalog lookup, and canonical session/grant ownership, then constructs
`Service`, `run.NewAutomationExecutionHost`, `NewV3Runtime`, and
`NewExecutionService`. Session mutations use `session.Service.ApplySessionMutation`
and wake `api.Server.PublishCommittedV3RealtimeOutbox` after durable commit. Wake
failure is reported without replaying execution. No notification adapter is added.

`ConfigureAutomations(domain, execution, nil, nil)` now receives execution;
`SetManageAutomationService(domain)` receives the policy-backed domain.
`(*Daemon).AutomationServices() (*automation.PolicyApproval, *automation.ExecutionService)`
exposes both concrete authorities for the next consumer wiring job.

## Trusted identity consumer requirements

Before every API/tool call, use:

`automation.BindRuntimeIdentity(ctx context.Context, verified identity.Principal, origin, sessionID string) (context.Context, error)`

Only authenticated adapters may bind this private typed context. API uses the
verified request principal, origin `user`, empty session ID. Tool uses its verified
WorkspaceScope principal, origin `agent`, canonical execution session ID. Never
bind from request JSON principal/role fields. Agent subjects remain session IDs;
ownership resolves their stored user and current active account membership.
The API/tool calls currently lack this binding and therefore fail closed until the
consumer job adds it. Approval must remain exclusive to explicit user routes.

Extend API startup wiring with a `*automation.PolicyApproval` setter and route
`ApproveUser`/`RevokeUser` explicitly. Extend the tool setter to receive execution
only when implementing authorized mutations. The existing API cancellation
interface has a different signature than `ExecutionService.Cancel`; the consumer
must preserve exact-revision semantics, not cast it away. Event verification is
unavailable: `RuntimeTriggers` rejects event names without a credential adapter.
Manual user and daemon schedule triggers are implemented.

## Dormant bounded lifecycle

`(*Daemon).StartAutomationScheduling(context.Context) error` is explicit and is
**not called** by construction or Run. No daemon, scheduler, provider, or test was
started during this change. The caller must wait for consumer integration and
validation before activation. Close cancels and joins its sole worker before DB
shutdown; duplicate/after-close starts are rejected.

Sweeps use canonical `IdentityStore.ListAccountScopes` and
`WorkspaceStore.ListForAccount`, capped at 128 each (129 detects overflow), 20
pages of 50 definitions and 20 pages of 50 occurrences per definition, with a
30-second context budget and minute wakeup. There is no recurring Git refresh.
Opaque record cursors are forwarded unchanged; durable schedule cursor CAS and
trigger receipts remain owned by Tick/RecoverPage and Pebble. Approval owner is
resolved from the durable grant and ownership/revocation is checked again on work.
Per-definition failures do not prevent other definitions from being attempted.

Limits are explicit errors, not silent truncation. Catalogs above these bounds
need canonical paginated account/workspace APIs before scale expansion; historical
occurrences beyond 1000 need durable recovery-scan continuation to avoid starvation.
No new storage authority or legacy global workspace list is introduced.

## Parent validation and documentation

Tests authored, **not run; parent validation required**. From repository root,
format the six owned Go/source files as appropriate with gofmt (Markdown excluded).
From `swarmd/`, with the repository's Go/FFF build prerequisites:

- `go test ./internal/automation -run '^TestRuntimeIdentityOrigins$' -count=1 -timeout=30s`
- `go test ./internal/runtime -run '^TestAutomation(CompositionOwnership|RecoveryBounds|LoopShutdown)$' -count=1 -timeout=60s`

Assertions cover live revocation before catalog effects, cross-account rejection,
agent occurrence confinement, origin/trigger substitution rejection, opaque cursor
forwarding, recovery failure bounds and cancel/join ordering. These are not live
provider, worktree crash-recovery or end-to-end API proofs. Parent owns atlas and
test-audit-ledger updates outside this child's scope, source formatting, and any
requested test execution. Do not claim launch readiness from this handoff.

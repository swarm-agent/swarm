# Execution composition handoff

The concrete `V3Runtime` allocates through the account-scoped worktree service,
creates via the supplied V3 mutation publisher, and installs a reset, digest-checked
composite checkpoint document via `CommitV3PlanAcceptance`. Binding order is
conservatively serialized, including bindings without dependency edges.

Compose `NewV3Runtime` with the existing session/worktree services, canonical V3
publisher, `ExecutionApproval`, and `V3ExecutionHost`; then pass it to
`NewExecutionService`. The repository must implement `ClaimAutomationDispatch`
and `ScheduleRepository`. `Tick` admits at most one due occurrence per definition;
`RecoverPage` dispatches at most fifty pending records per page. No daemon loop
is installed by this package.

## Remaining integration requirements (not implemented here)

- `V3ExecutionHost.Prepare` must resolve workspace ownership and generation from
  canonical workspace ID, compile the account agent/model/tool policy, and return
  canonical V3 placement metadata and primary workspace grants.
- `Start` and `Cancel` still require a concrete canonical executor adapter with a
  durable cancellation fence shared by both operations. The contract alone is
  not evidence of cancellation race safety or successful provider execution.
- `ExecutionApproval.Request/Verify` require the user approval service adapter;
  reference strings must not become permission grants. No adapter is installed.
- Dispatch reservations now claim account-scoped target IDs atomically across
  definitions and workspaces, including independent occurrences. Claims survive
  restart and are reclaimed only after terminal occurrence state. Safe cancellation
  and truthful terminal reconciliation remain prerequisites for releasing targets.
- Recovery after worktree allocation but before session creation relies on the
  existing deterministic allocator's recovery behavior and requires an injected
  failure integration test. Concurrent Ensure calls likewise require review.
- Explicit failed-run retries, durable notification outbox, immutable checkpoint
  policy/task-program rebinding, and blocked-run resume remain unfinished.
- Parent must update the atlas/test audit ledger outside this child's ownership.

## Authored evidence

`TestPinnedExecutionDocumentResetsOnlyProgress` checks fresh progress, preserved
objectives/criteria, untouched source and rejection of changed source bytes.
`TestAutomationDispatchClaimAndCursorRestart` checks durable reservation recovery,
competing claims and stale cursor rejection without advancing the winner.
`TestV3RuntimeRejectsMissingAuthorities` checks fail-closed construction.

Validation: **not run; parent validation required**. User requested no execution.
Suggested commands, only after execution is authorized, from `swarmd/` with Go
and the repository's FFF/native build prerequisites installed:

```
go test ./internal/automation -run 'Test(PinnedExecution|V3Runtime|Execution)' -count=1 -timeout=60s
go test ./internal/store/pebble -run '^TestAutomationDispatchClaimAndCursorRestart$' -count=1 -timeout=60s
```

Formatting has not been run; parent must gofmt the changed Go files before final
integration. This is a partial implementation handoff, not completed execution.

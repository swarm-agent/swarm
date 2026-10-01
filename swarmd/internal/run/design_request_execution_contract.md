# Queued Designer request execution boundary

`manage_design` accepts briefs through `ApplySessionMutation(design.accept)`. Submission returns durable request/group identity, not proof of execution. Acceptance and its bounded pending index survive parent completion; no parent-owned goroutine is the scheduling authority.

## Allocation adapter

`Service.AllocateDesignChild` is a trusted scheduler API, not a tool argument surface. It loads the authenticated account/principal request, uses permission-owned `AdmitExecution`, resolves the configured Designer with `agentmodel.ResolveSystemAgent`, and calls the canonical session mutation boundary. If Designer resolution fails or is unavailable in the catalog, only the resolved account default may be used, with a durable `RouterAlert` in attempt/status and child metadata. If that default is unavailable, allocation fails without creating a child; no model/provider is hardcoded.

`DesignAllocation` participates in the same Pebble batch as child session, run intent, events, outbox and attempt binding. Request revision CAS and deterministic account/request/candidate/attempt identity prevent racing allocations and orphan children. Parent ownership and run existence are checked under the account/session locks; parent completion does not invalidate accepted work. The child has no inherited workspace grants or writable checkout. Its durable metadata and run intent retain parent and attempt lineage.

The caller owns the returned admission lease and must release it or pass it to execution with `executioncapacity.WithLease`. Recovery of an already bound attempt returns the verified child and **no lease**: it is not permission to replay a provider call. Recovery must inspect canonical run intent and acquire execution ownership before dispatch. Interrupted work needs an explicit fresh attempt. No scheduling loop or provider execution is enabled here.

`Service.ReconcileDesignCancellation` verifies persisted child ownership/lineage. Pending runs are cancelled through the V3 mutation boundary with an event-sequence precondition. Running cancellation dispatches `StopSessionRun`; later reconciliation confirms history only after the canonical intent says cancelled. An unavailable executor is an error, not a fabricated acknowledgement. Persisted `cancel_requested` remains discoverable after restart.

## Deferred execution work

The next checkpoint must hydrate authorized selected source files, own scheduler wake/restart processing, acquire/retain execution ownership, invoke the configured Designer, reconcile terminal states, validate standalone output, and publish through independent design history. It must not treat the allocation return value as provider success, register work in parent cleanup maps, or accept child IDs from models. `RecordDesignAttempt` remains a low-level observation API, not an allocation authority.

No Artifact V3 author grants, Git, Parts, publication or dual writes participate. Browser validation, UI, and provider dispatch remain deferred.

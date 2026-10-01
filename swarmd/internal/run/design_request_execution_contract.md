# Queued Designer request execution boundary

`manage_design` accepts briefs through `ApplySessionMutation(design.accept)`. It does not create a child, resolve a model, invoke a provider, or reserve an execution slot. The returned request ID is the durable job/group reference, not proof of execution. At most 50 pending requests per account/principal, each with at most eight candidates, are admitted under the same store lock and batch as acceptance. Replays do not consume additional capacity. Parent run completion/cancellation does not remove the independent pending index.

## Next-checkpoint adapter requirements

- Enumerate `ListPendingDesignRequests` in bounded account/principal pages. Never infer ownership from request arguments or scan arbitrary accounts.
- Resolve the configured Designer model only when binding execution, using the canonical system-agent resolver and admission service. Resolve selected source files through authorized workspace/path boundaries before provider dispatch. Neither is implemented by this queued foundation.
- Allocate a canonical V3 child session/run with durable delegation lineage through the session mutation boundary. Use a deterministic allocation idempotency key derived from request/candidate/attempt; recover that exact child after a crash rather than allocating another. Atomic allocation/binding support must be implemented before enabling dispatch; `RecordDesignAttempt` alone is not atomic child allocation.
- Bind attempt provenance using request-revision CAS only after proving the canonical child owner, lineage and run. Do not treat caller-provided child strings as authority. Observe canonical terminal states; interrupted work requires an explicit fresh attempt, not automatic provider replay.
- Reconciliation owns `cancel_requested` dispatch against canonical children and confirmation. The tool immediately cancels queued candidates; it only persists cancellation intent for running candidates. Pending cancellation must survive daemon restart.
- Do not register accepted work in parent-owned goroutine cleanup or cancellation maps. The pending index remains scheduling authority until terminal observation. No independent design session lifecycle exists.
- Publication belongs only to a validated Designer completion adapter, never the orchestrator tool. Exact historical bases remain immutable; failed/cancelled attempts cannot publish. Plan output is text history, never an implementation trigger.

No Artifact V3 author grants, Git, Parts, publication, or dual writes participate. Existing artifact operations remain untouched. Provider execution, canonical child allocation/binding, source hydration and browser validation are intentionally not enabled by this foundation.

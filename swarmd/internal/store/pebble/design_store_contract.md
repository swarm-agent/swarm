# Independent design persistence boundary

`design_store.go` uses the existing Pebble database and a Store-owned mutation mutex. It neither reads nor writes Artifact V3 records, filesystem artifacts, grants, Git, Parts, session lifecycle records, or provider credentials.

## Exported integration API

All methods are on `*Store`. All take `DesignPrincipal` first. Construct this from authenticated identity, never model-supplied account/principal strings. Account and creator jointly partition records; sharing/transfer is not implemented.

- `SubmitDesignRequest(principal, DesignSubmit) (DesignRequest, error)`: 1–8 explicit candidates; immutable input context; creates distinct artifacts for generation. An edit names an existing artifact and exact historical base of the same kind. Candidate artifact IDs must be distinct within a batch. The request ID is the batch group; artifacts retain their original creation group and each revision names the editing request.
- `GetDesignRequest(principal, requestID) (DesignRequest, error)`: bounded status, candidate definitions and attempt provenance; no output bytes or source snapshots.
- `ReadDesignContext(principal, requestID) ([]DesignContextSnapshot, error)`: at most 32 snapshots / 256 KiB source bytes. Hydration must enforce workspace permissions, path and symlink safety, and secret exclusion before submission. Paths are inert labels here; the store never opens them.
- `RecordDesignAttempt(principal, requestID, DesignAttemptMutation) (DesignRequest, error)`: request-revision CAS plus idempotency. `running` starts an attempt from queued/failed/interrupted, with fresh child identity. Observe canonical outcomes as failed/interrupted/cancelled, with bounded machine reason codes rather than raw provider errors. Successful completion is only recorded by publication.
- `PublishDesignRevision(principal, requestID, DesignPublication) (DesignRequest, error)`: request-revision CAS, exact active child/run match, kind check, bounded UTF-8 bytes. Atomically stores output, monotonically numbered revision, successful attempt, artifact counter and idempotency receipt. Does not select automatically. Obtain the exact ref from the candidate's last attempt result.
- `GetDesignArtifact(principal, artifactID) (DesignArtifact, error)`: metadata, revision count, selected ref and monotonic selection version.
- `ReadDesignRevision(principal, DesignRef) (DesignRevision, error)`: exact revision/hash match and byte digest verification, maximum 2 MiB output.
- `DesignHistory(principal, artifactID, afterRevision, limit) ([]DesignRevision, error)`: ascending immutable metadata, excludes content; at most 50 records. Continue using the last revision number. History reads may include a newly appended revision on a later page but never rewrite prior pages.
- `SelectDesignRevision(principal, DesignSelection) (DesignArtifact, error)`: exact expected current ref plus selection-version CAS, preventing ABA. No cross-artifact selection. Version starts at zero, current starts nil. Selection increments version even when selecting the same ref.
- `DesignSelectionHistory(principal, artifactID, afterVersion, limit) ([]DesignArtifact, error)`: bounded immutable selection receipts, including prior selections.

Mutations require idempotency keys. Submit keys are scoped to the principal; attempt/publication keys are scoped to request and operation; selection keys are scoped to artifact. A replay returns its original result, not refreshed status. A changed payload under the same key fails with `ErrDesignConflict`. Failed storage writes leave no receipt, permitting retry. Invalid inputs, absent/foreign records and conflicts expose `ErrDesignInvalid`, `ErrDesignNotFound`, and `ErrDesignConflict` respectively.

## Runtime responsibilities and states

This is persistence foundation, not an executor or a new session authority. The caller must verify parent and child session/run ownership, durable delegation lineage, configured Designer identity/model, admission, and canonical run state. It must not expose publication as an orchestrator-authored content tool. `DesignSubmit` has no generated-output field. HTML/plan bytes arrive only through the delegated completion adapter. Validate standalone HTML at that boundary and sandbox all preview rendering; the store deliberately does not sanitize or rewrite immutable output. Plan documents never trigger implementation here.

Queued cancellation becomes cancelled immediately because no child has started. Running cancellation becomes cancel_requested, blocking publication; after canonical cancellation, record cancelled. Reopen preserves state verbatim: runtime reconciliation explicitly records interrupted when canonical run evidence warrants it. Failed/interrupted retries require a fresh child session; prior attempts remain, with a maximum of 16 per candidate. Cancellation is terminal; a new request is needed to retry it. Request status is derived: cancel_requested, running, queued, then succeeded if all succeed, partial_success if some succeed, otherwise interrupted, failed, or cancelled. Partial success therefore describes settled mixed outcomes, not an in-progress batch.

Acceptance/durable scheduling, cancellation dispatch, source hydration, HTML validation, realtime notification and authorization against actual session records must be integrated by runtime. This store alone does not guarantee background execution survives parent termination. No global request listing or unbounded account scan API is introduced.

## Validation handoff

Tests are requirement-first real-Pebble persistence tests, not benchmarks. They cover immutable bytes, exact hashes, branching, reopen, context snapshots, selection ABA/history, idempotency/conflicts, cross-account/principal rejection, failure preservation, batch partial outcomes, cancellation, interrupted reconciliation, fresh retries, bounded inputs/history, concurrent CAS and real read-only database write failure.

Not run; parent validation required. From the repository root, format the two new Go files with gofmt. From `swarmd`, run `go test ./internal/store/pebble -run '^TestDesignStore' -count=1 -timeout=120s`, then the same focused selection with `-race -count=3 -timeout=180s`. Requires the repository Go toolchain/dependencies. No provider, credentials, daemon or live environment is needed. Formatting and execution evidence must be attached to the integrated revision; no passing result is claimed here.

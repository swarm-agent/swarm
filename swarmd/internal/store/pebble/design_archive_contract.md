# Independent design archive

Archive identity is the artifact, not the session, request, candidate index, or
revision. All requests referring to that artifact inherit its current visibility.
The immutable revision history, exact references, selection, and execution state
are untouched. Publication and selection preserve archive metadata. Restore is
an explicit inverse mutation.

## HTTP contract

`POST /v3/sessions/{origin_session_id}/designs/artifacts/{artifact_id}` accepts:

```json
{"action":"archive","ref":{"artifact_id":"design","revision":1,"sha256":"exact-retained-digest"},"expected_version":0,"idempotency_key":"client-generated-stable-key"}
```

Use `action: restore` to restore. `expected_version` is **archive_version**, not
selection_version or request revision. A valid historical exact ref is allowed;
a forged/missing ref is not. Responses are `200 {"artifact": DesignArtifact}`
with `archived` and monotonic `archive_version`. False `archived` may be omitted.
Exact retries return the original receipt; key reuse with different input and
stale archive versions return 409. Malformed input is 400; wrong owner/session or
missing artifacts are 404. Session write scope is required. Existing session and
project catalog authorization remains authoritative; the endpoint grants no
project membership and cannot archive a session or task.

Session and project design GET catalogs accept `view=active` (default) or
`view=archived`. Other values are 400. Rows without any matching candidate are
excluded. **Candidate indexes are never compacted**: mixed requests appear in
both views, so clients must filter candidates by `candidate.archived` while
preserving original indexes. Every catalog candidate includes `archive_version`.
Follow `next_cursor` even on an empty page: filtering does not expand the bounded
scan budget. Store listing methods retain all rows for non-UI history callers.

The artifact's origin session is the original request's parent session. Existing
exact artifact history GET remains available after archive. Archive commits use
`commitDesignChange` / `ApplyV3SessionMutation`, retaining `design.updated` and
project `project.updated` resource=`designs` invalidations atomically with state
and receipt. Consumers should update loaded artifact candidates immediately from
the response and coalesce targeted invalidations, not reload every project.

## Task archive and catalog refresh

`ArchiveProjectTaskIfRevision` and its existing HTTP route retain their CAS and
live-execution guards unchanged. They return the updated task directly. No
measured task-mutation bottleneck justified weakening those checks. Project design
catalog refresh formerly performed a synchronous membership write for every
visited position under `projectsMu`; `hydrateDesignMembership` now reads and
validates existing locators without rewriting/fsyncing them. Only missing legacy
locators are hydrated. The lock, bounded scan, and membership revalidation remain.
No latency or throughput measurement is claimed.

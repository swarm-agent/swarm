# Independent delegated design HTTP contract

This API does not adapt designs to Artifact V3. Existing artifact routes remain unchanged. All routes require the existing authenticated V3 session access check plus exact account/principal ownership. Errors: 400 invalid, 404 absent/foreign, 409 stale exact ref/CAS. Responses are no-store. No client account, run, child or output bytes are accepted.

- `GET /v3/sessions/{session}/designs?after={opaque_cursor}&limit=20`: `{requests, next_cursor}`. Limit 1–50. Immutable input briefs/context are excluded. Each candidate retains state, errors, attempts, exact result and validation/preview refs. Requests are newest-admission-first, using a durable monotonic per-session counter rather than request IDs or wall time. Echo next_cursor unchanged until empty; a full final page has an empty cursor. Cursors are account/principal/session scoped. Refresh from the first page to see newer admissions. This is a bounded durable per-session index, not a global scan.
- `GET /v3/sessions/{session}/designs/artifacts/{artifact}?after=0`: `{artifact,revisions}` with at most 50 metadata-only revisions strictly after the numeric revision. Continue from the last revision while a full page is returned. Artifact contains `selected`, `selection_version`, `revision_count`; revisions contain `ref`, `base`, `plan_source`, attempt/validation provenance. Selection is never inferred from latest.
- `POST` to that artifact URL with `{action,ref,...}`. `ref` is the complete `{artifact_id,revision,sha256}` from history. Actions:
  - `read`: exact UTF-8 text/plain bytes (HTML or plan).
  - `download`: exact bytes, attachment disposition (`design.html` for HTML, `design.txt` for plans). This does not integrate source code.
  - `preview_png`: additionally `preview` equal to revision `attempt.validation.preview`; returns verified stored PNG.
  - `preview_html`: a trusted PNG-only HTML wrapper, **not executable authored HTML**. Opaque-origin `sandbox` HTTP CSP plus restrictive embedded meta CSP for srcdoc, scripts/network/forms/frames denied. The iframe must retain sandbox=""; meta CSP cannot impose sandbox. For Desktop fetch this response and use a sandbox-empty iframe; alternatively display the PNG. This endpoint remains the lightweight thumbnail and explicit screenshot fallback.
  - `live_source`: exact HTML revision bytes, text/plain with attachment disposition, nosniff and script-denying HTTP CSP (never executable on the daemon origin). Same session/principal and full ref checks as read; plans use read. Desktop's open viewer alone passes the bytes to `DeliverablePreview`: an opaque sandboxed outer document with a CSP before all content, containing an independently sandboxed srcdoc frame. No allow-same-origin, forms, popups, top-navigation or downloads. The outer frame's frame-src denial constrains inner URL navigation; inherited and leading inner CSP deny network, workers, nested frames, objects and authored base URLs. No preview message bridge is registered. Only inline scripts/styles and embedded data images/fonts/media are supported; external/package dependencies are explicitly blocked, not silently fetched. The original bytes remain available through read/download and a source disclosure. Markdown plans use the existing safe Markdown renderer. Reduced-motion CSS is applied, and authored JS can observe the browser preference; arbitrary authored JS is not forcibly paused. Browser execution tests must pass on the supported Desktop engine before shipping this path.
  - `select`: additionally `idempotency_key`, `expected_version`, `expected_current` (null for first selection). Returns `{artifact}`. Exact current ref plus monotonic version rejects ABA; never rewrite old bytes.
  - `edit`: additionally nonempty `brief` (at most 65536 UTF-8 bytes), `idempotency_key`. Queues a canonical parent **user message**, returning the existing V3 messages endpoint receipt, not a design acceptance receipt. Message contains `manage_design submit` arguments with one edit candidate and the exact selected historical base. Parent execution must call that tool; only its trusted run may accept/allocate. UI says “edit requested”, then hydrates design progress after acceptance. This is not guaranteed direct admission or immediate provider execution. Reuse the idempotency key for retries; use a new key for another edit.

Realtime: `design.accepted` plus `design.updated` in the canonical parent session event/outbox scope. Updated payload has `request_id` and either `revision` or `artifact_id,selection_version`. Allocation emits its parent invalidation in the same batch as child creation, run and request state. Other progress/selection commits participate in ApplyV3SessionMutation. Rehydrate the catalog/history on these events, initial open and durable reconnect repair; coalesce in-flight requests, no recurring polling. Do not compare or parse endpoint cursors.

`media_inspect` accepts `design_preview_reference:{session_id,preview}` as a mutually exclusive selector; preview is the complete `DesignPreviewRef` from ready history (including output request/candidate/attempt/child/run/hash and PNG hash). Backend authenticates parent ownership and validates publication plus PNG bytes. No conversion/import to Artifact V3.

The admission counter/index is populated atomically for new acceptances. Earlier pre-index development records are outside this unreleased catalog contract; no migration, inferred history or silent full-store scan is provided. Existing Artifact V3/legacy artifact discovery is unaffected.

Validation: focused tests are authored but not run; parent validation required. Browser pixel/provider acceptance is separate and not implied by hermetic tests.

## Project Media Center discovery

`GET /v3/projects/{project}/designs?limit=20&after=<opaque>` returns
`{designs: [{project_id, task_id?, attempt_id?, title, request}], next_cursor}`.
Limit is 1–50. Authenticated user/account and projects:read or sessions:read are
required. The request is the existing independent design request (briefs removed),
including stable candidate array positions, request revision, states, failure and
router alerts, attempt results/validation preview references and exact base lineage.
The bounded title is the first 80 Unicode characters of normalized retained input.
It is display text, not HTML. No output bytes or revision history are copied.
Use parent_session_id and the unchanged per-session exact-reference APIs to open
or load revision turns. Do not convert these references into Artifact V3.

Pagination is membership-first, then newest admission within each session; it is
not global timestamp sorting. Continue even on an empty page until next_cursor is
empty. Merge by request ID/candidate index, not title/time. Each call visits at most
limit membership/catalog positions, including foreign/deleted sessions. Cursors
are account/principal/project scoped. They are opaque continuation positions, not
snapshot guarantees or authority. On membership invalidation discard traversal
cursors and rehydrate from the first page. Concurrent admission is reconciled by
restarting from the first page on the durable invalidation.

Hydration walks only the project's primary session and its task records/current
and historical attempt sessions. It uses the already-atomic session admission
index, so completed requests predating this endpoint are included. It idempotently
persists membership locators per visited owned session; continuation survives
restart. New project/task bindings persist these locators in their canonical batch.
Locators never authorize reads: current project/task binding and session ownership
are revalidated. Project removal/rebinding invalidates previously discovered rows.
There is no global session scan or second progress/history store.

Design acceptance/update/allocation retains its original design.accepted or
design.updated event and atomically appends a reference-free project.updated
invalidation (`resource: designs`) for every distinct validated project membership
in the same V3 batch. Duplicate task/primary bindings do not duplicate wakeups;
revoked bindings are ignored. Membership lookup is bounded to 50 locators and
fails closed on overflow. Existing project realtime
publication/replay delivers this while chat is unmounted. Listen to project.updated
and rehydrate the matching project, rather than polling. Pre-index projects acquire
locators through bounded initial hydration; legacy metadata is only a locator and
must match the canonical project/task/attempt binding.

Edit remains canonical parent user-message acceptance, not design admission.
The message has `metadata.design_edit_request: {client_request_id, base, state:
"requested"}`. Persist/reload the canonical message, not a frontend pending store.
A subsequent accepted request exposes `source_message_id` from its canonical run
and `client_request_id` from design acceptance. Reconcile using message identity
and exact candidate base (never timestamps/titles); message metadata by itself
cannot establish accepted/running/ready state. Legacy requests lacking message
identity must not be heuristically matched. Selection CAS and exact-ref checks
remain unchanged.

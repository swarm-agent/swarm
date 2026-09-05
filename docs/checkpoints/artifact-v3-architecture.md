# Artifact V3: whole-project Git and authoring-turn contract

Status: **frozen implementation contract and red acceptance journey**
Date: 2026-09-03
Scope: initial multipart creation, complete-artifact preview, targeted follow-up turns, whole-project repair, candidate selection, and revision history. **Storyboards, Video Studio, MP4, and downstream media workflows are excluded.**

## 1. Product rule

A managed artifact is a private, runnable project. One artifact owns one private bare Git repository. Every user-visible revision is one immutable Git commit whose tree contains all real project files required to build and preview that revision. Git owns bytes, trees, blob identity, ancestry, and the revision identifier. Durable V3 events own authorization, idempotency, turn and candidate status, current-head selection, session projection, and realtime delivery.

A Part is only a stable user-facing navigation and iteration target inside a whole revision. It is not a blob container, independent revision chain, publication unit, lock that forbids necessary shared repairs, or second source of truth. A targeted prompt communicates intent. The author may change shared CSS, code, assets, configuration, the manifest, or another unlocked target when necessary to keep the complete project correct. The complete candidate is built and previewed as one unit; Swarm never stitches independently repaired Parts into a composition.

## 2. Required user loop

1. The user prompts for an artifact. Swarm allocates an Artifact and Authoring Turn before starting its context-bound author.
2. The author receives a private worktree at the exact base commit (empty only for the initial turn) and normal project operations: list, read, create, edit, rename, delete, diff, build, preview, and finish. It does not receive `declare_parts`, `write_part`, independent Part publication, repository IDs, refs, output paths, or policy internals as caller arguments.
3. During the same turn the author may build and preview repeatedly. A compile error, runtime error, unresolved target locator, overflow, unreadable pixels, or interaction failure returns bounded diagnostics and inspectable output to that same context. The author repairs the project in place and retries without allocating a replacement Artifact or Turn.
4. A successful initial turn creates the root commit. The complete preview becomes the primary Artifact Studio surface; Part navigation and history are secondary.
5. A follow-up can target one or more stable Parts at an exact base commit. The author still receives the complete tree and may make necessary cross-target changes. Alternative candidates are complete child commits from the same base.
6. Artifact Studio previews each candidate as its own complete tree. Choosing one performs a head CAS to that exact commit; it never copies selected blobs between candidates.
7. History shows whole commits, ancestry, changed files, affected Parts, diagnostics, and prior previews. Opening a prior commit never moves the current head. A new iteration always names an exact base commit, normally the current head.

## 3. Canonical repository

One private bare repository exists at the daemon-owned artifact repository root under an opaque server-generated Artifact ID. It has no remotes, inherited Git config, credentials, or hooks. It is never placed in a user repository and its host path is never sent to clients or models.

A normal browser artifact tree may be:

```text
swarm-artifact.json
index.html
styles/theme.css
src/app.js
src/plans.js
assets/logo.svg
```

The Git tree contains those paths directly. There is no mandatory `parts/` directory, synthetic `content` file, generated construction table, or per-blob repository. Empty directories and ignored/cache/build output are not revision content. Symlinks, submodules, `.git*`, absolute/escaping paths, case-fold collisions, devices, FIFOs, sockets, and other non-regular entries are rejected. Executable files are permitted only when the policy explicitly allows them; otherwise committed mode is `100644`.

Limits are repository-level and explicit: maximum file bytes, total tree bytes, file count, path length/depth, build time/output, and diagnostic volume. They are not a product-level Part count. Reads, diffs, trees, histories, and Part lists paginate or virtualize. The protocol and manifest shape are identical for one Part and hundreds of Parts within quotas.

### 3.1 Minimal manifest

`swarm-artifact.json` is the only canonical Artifact metadata file in the tree:

```json
{
  "schema_version": "swarm.artifact/v3",
  "entrypoint": "index.html",
  "parts": [
    {
      "id": "pricing",
      "label": "Pricing",
      "locator": { "kind": "selector", "path": "index.html", "value": "#pricing" }
    }
  ]
}
```

Required fields are `schema_version`, one safe repository-relative `entrypoint`, and `parts`. Each Part requires a stable unique `id`, a display `label`, and exactly one locator. Initial locator kinds are:

- `file`: a real repository-relative path;
- `selector`: a selector resolved in a rendered HTML entrypoint plus an optional owning source `path`;
- `state`: a stable application state ID plus an optional owning source `path`;
- `semantic`: a stable source/navigation label plus at least one real owning `path`.

A locator is navigation and validation metadata only. It does not contain bytes, object IDs, independent revisions, output policy, dimensions, runtime graphs, build steps, candidates, locks, lineage, or external URLs. Paths resolve against the same commit tree. Rendered locators must resolve uniquely in every preview state for which they are declared. Duplicate IDs, missing paths, ambiguous selectors, unsafe values, or unresolved state IDs fail the complete candidate gate.

A later turn may update locator paths after a legitimate rename while preserving the stable Part ID. Removing or reassigning a Part ID is a manifest-level change shown in the diff and requires policy authorization; it cannot happen implicitly because a target was not selected.

## 4. Exact references

All external references are authenticated opaque encodings. Their decoded canonical records contain:

```text
ArtifactRef  = account + owner user + owner session + artifact_id
RevisionRef  = ArtifactRef + full commit_oid + tree_oid + manifest_blob_oid
TurnRef      = ArtifactRef + turn_id + exact base_commit_oid + producer session/run + grant
CandidateRef = TurnRef + candidate_id + full commit_oid + build_id + validation_id
HeadRef      = ArtifactRef + head_generation + full commit_oid + selecting event_seq
PreviewRef   = RevisionRef + build_id + validation_id + expiring GET-only ticket
```

Human labels, abbreviated hashes, filenames, Part IDs, candidate indexes, or event sequence alone never resolve authority. A Turn and every Candidate bind one exact base commit. Stale base/head generation, foreign owner, mismatched tree/manifest/build evidence, reused idempotency key with another payload, or missing commit rejects with no visible state movement.

## 5. Authoring Turn and private worktree

The server allocates a context-bound grant with owner, Artifact ID, Turn ID, producer run, exact base commit, expiry, file/tree/build quotas, permitted operations, initial versus follow-up mode, targeted Part IDs, and immutable policy snapshot. Those values are injected by trusted orchestration, not executable model fields.

For each Turn the daemon creates one private checkout from the Artifact's bare repository under the canonical daemon cache/runtime contract. The checkout:

- starts at `base_commit_oid`; an initial turn starts from an empty index with no parent;
- has no remote, credential helper, hooks, ambient user Git configuration, or access outside its contained root;
- permits normal regular-file operations and a bounded diff;
- cannot modify refs or commit directly; the service owns tree write, commit creation, and ref CAS;
- remains available through repeated build/preview/repair attempts in the same Turn;
- is removed after terminal success/cancellation under retention policy, while the candidate commit, diagnostics, and Turn record remain recoverable.

`finish_turn` snapshots the whole worktree, validates the manifest and quotas, writes one candidate tree/commit, then launches the whole-project gate. An empty or unchanged candidate is rejected unless the user explicitly requested a no-op verification. The author may continue after a failed gate; failure does not end the Turn or allocate another destination. Cancellation stops processes, records cancellation, retains bounded diagnostics, and cannot move head.

## 6. Whole-project build and preview gate

A Candidate can become `ready` only when all checks bind to its exact commit/tree and policy revision:

1. repository containment and quota validation;
2. manifest parse, safe path validation, and stable Part resolution;
3. server-owned build from the complete tree in an isolated, network-disabled environment;
4. preview server admission of build output only, never arbitrary source paths or URLs;
5. browser load/readiness, console/runtime error, and interaction checks;
6. requested target and all declared locator resolution;
7. pixel inspection for clipping, overflow, sizing, legibility, overlap, scrollbars, capture chrome, and prompt fidelity;
8. deterministic evidence digests tied to the exact Candidate.

Build products and screenshots are immutable evidence, not Artifact source. A failed build, browser timeout, corrupt/missing object, bad locator, broken interaction, or poor pixels creates typed diagnostics and leaves the last working head unchanged. A successful commit without matching build and preview evidence is not selectable. A prior validated Revision stays previewable by exact commit even while another Turn is working or invalid.

## 7. Branches, candidates, and history

Canonical refs are implementation-private accelerators, not durable product authority:

```text
refs/heads/artifact                    current selected head cache
refs/swarm/turns/<turn-id>/candidate/<candidate-id>
refs/swarm/transactions/<transaction-id>
```

The root Revision has no parent. Every follow-up Candidate has exactly the Turn base commit as its first parent. Candidates in one round are siblings. Selection advances `refs/heads/artifact` by expected-old CAS to the selected complete commit. V3 projections call this the Artifact head; the UI never exposes a Git branch-selection metaphor as authority.

No per-Part merge is allowed. If combining ideas from candidates is desired, open a new Turn from an exact commit and let the author edit/build/preview a new complete child commit. Unchanged files naturally retain identical Git blob OIDs, which the acceptance journey checks. Changed Parts are derived for display by intersecting the commit diff with manifest source paths and rendered locator evidence; the derived set cannot authorize or reject filesystem edits.

## 8. Event-to-Git transaction and crash recovery

Git cannot participate in the Pebble batch, so the protocol is a deterministic two-authority reconciliation rather than pretending to have one database transaction.

### 8.1 Candidate creation

1. A V3 mutation creates or replays the Turn and Candidate intent keyed by `(artifact_id, turn_id, candidate_id, request_id)` with exact base and payload hash.
2. The service writes the complete Git tree and commit without moving the Artifact head.
3. It atomically creates `refs/swarm/transactions/<transaction-id>` and the candidate ref, both naming that exact commit.
4. A V3 mutation records the candidate commit/tree/manifest, build/validation IDs, status, and transaction ID. Only this event makes the Candidate visible/selectable.

Crash before step 2 leaves an intent to resume. Crash after the commit but before transaction ref permits the same deterministic retry or garbage collection of unreachable objects. Crash after the transaction ref but before V3 commit is reconciled by reading the exact transaction ref and completing the idempotent event; it never invents a second commit or reports success from Git alone.

### 8.2 Head selection

1. A V3 mutation records a pending selection transaction bound to expected current head generation/commit and one ready Candidate.
2. Git atomically CAS-updates `refs/heads/artifact` and creates the immutable transaction ref.
3. A V3 mutation finalizes the selected head and realtime outbox.

Recovery compares the pending V3 transaction, immutable transaction ref, and Git head. If Git did not move, retry the CAS. If Git moved to the exact selected commit, finalize the event. Any other Git head or reused transaction ref is corruption/conflict: freeze writes, keep the prior durable head projection, and require integrity repair. The head is not announced as successful until the final V3 event commits. Realtime is published only from the durable outbox.

Startup and periodic reconciliation scan bounded pending transactions and verify referenced commits with `git cat-file`/`fsck`. Missing/corrupt exact commits make the affected revision unavailable with a typed integrity diagnostic; they never fall back to legacy bytes or another commit.

## 9. HTTP, realtime, and Desktop contract

The V3 family is separate from `/artifact-v2` and legacy `/artifacts`:

```text
GET  /v3/sessions/{session}/artifacts-v3
GET  /v3/sessions/{session}/artifacts-v3/{artifact}
GET  /v3/sessions/{session}/artifacts-v3/{artifact}/revisions?cursor=...
GET  /v3/sessions/{session}/artifacts-v3/{artifact}/revisions/{commit}
GET  /v3/sessions/{session}/artifacts-v3/{artifact}/preview?revision=<opaque>
POST /v3/sessions/{session}/artifacts-v3/{artifact}/turns
POST /v3/sessions/{session}/artifacts-v3/{artifact}/turns/{turn}/select
```

Write requests use client idempotency keys and exact expected refs/generations. Responses and normal V3 sync/realtime projections carry Artifact, Turn, Candidate, and head records; clients do not parse commit order, infer a head from status labels, or use transport cursor order as revision authority.

Artifact Studio is preview-first:

- the complete current preview occupies the primary pane;
- a virtualized Part navigator focuses declared locators without changing revision;
- a Turn timeline and commit graph show ancestry, base, selected head, and terminal status;
- candidate cards open complete candidate previews and show changed files/derived affected Parts;
- diagnostics link to file/line or rendered locator when safe;
- prior Revision rows open exact historical previews without moving head;
- `Iterate` starts from an exact current Revision and carries selected Part IDs as intent only.

The UI must never claim “independently stored parts,” require unchanged non-target Parts, group by V2 composition IDs, or show metadata lists in place of a complete preview.

## 10. Current implementation failure evidence

The current Artifact V2 implementation is useful evidence of what V3 must replace, not a base to rename:

- `swarmd/internal/artifactv2/author.go:21,34-35,458,798-809` fixes authoring to at most 64 Parts. `swarmd/internal/tool/runtime_artifact_v2_author.go:49-56,152-155` embeds the same `declare_parts`/`write_part` schema and 1-to-64 parser bound. `swarmd/internal/run/artifact_v2_designer.go:23-35,110-120` injects `MaxParts: 64` and exact declared target keys.
- `swarmd/internal/artifactv2/author.go:496-536` writes one Part Revision at a time. `advanceFromLatest` at `750-795` reconstructs a composition from independently latest Part revisions. This makes a Part a persistence authority rather than a navigation target.
- Focused iteration forbids required cross-project repairs. `artifactv2/author.go:330-410` imports only target Part revisions and rejects a Candidate that changes a preserved Part. `artifactv2/service.go:465-487` repeats this rule. `web/.../artifact-v2-api.ts:313-319` tells the model to preserve every non-target and locked Part byte-for-byte, while `artifact-v2-studio-model.ts:83-90` treats that constraint as candidate correctness.
- `artifactv2/service.go:867-904` derives a repository ID from Artifact, Part, and digest, then creates a fresh genesis repository containing only `content`. One managed Artifact therefore spans per-blob Git repositories instead of one inspectable project history.
- `artifactv2/author.go:884-904` implements the baseline compiler by sorting Parts and concatenating bytes with newlines. `909-913` validates only that compiled bytes and an output receipt are non-empty. Neither proves a runnable multipart project or rendered result.
- `artifactgit/types.go:46-115` and `artifactgit/repository.go:131-313` persist a synthetic manifest of Part blob OIDs plus `concat-v1`/`package-v1` construction. `repository.go:197-212` writes only `swarm-artifact.json`, `content`, or flat `parts/<id>` entries; `675-699` materializes the same synthetic layout. Candidate/Merge APIs at `390-539` operate by Part replacement and can stitch selections.
- `/v3/sessions/{session}/artifact-v2/{artifact}` loads Parts, all per-Part revisions, compositions, builds, validations, derivatives, iterations, and published heads into one Studio response (`sessions_v3_artifact_v2.go:198-235`). Desktop mirrors that metadata-heavy shape (`artifact-v2-api.ts:186-198,277-280`).
- The Desktop Studio labels the artifact as `independently stored parts`, renders metadata cards before a preview, previews only motion/storyboard compiler outputs, and compares candidate preservation by target Part (`desktop-v3-artifact-v2-studio.tsx:24-36`). It cannot open an arbitrary prior whole-project commit preview.
- Daemon wiring constructs V2 over `NewGitBlobStore`, then layers V2 compiler/validator and `/artifact-v2` routes (`swarmd/internal/runtime/daemon.go:286-310,571-577`). V3 must replace that new-write wiring rather than import `artifactv2`, write `ArtifactV2*` records, or translate V2 events.

## 11. V3 cutover boundary

Artifact V3 packages, tools, routes, events, records, projections, prompts, and Desktop models must not import, embed, alias, call, translate, or write:

- `swarmd/internal/artifactv2`;
- `ArtifactV2*` Pebble records or `artifact.v2.*` mutations;
- `artifact_v2_author`, `declare_parts`, `write_part`, V2 composition/Part selection, or V2 preview routes;
- V1 collection/variant/Part/chain records, `manage_artifact` write actions, or V1 `artifact.Authority` writes.

The audited native Git command adapter, canonical storage roots, V3 mutation boundary, authenticated identity, bounded authoring workspace primitives, build isolation, and trusted browser renderer may be retained behind V3-owned interfaces. Existing V1/V2 artifacts remain explicitly labeled read-only history. Their references are not valid V3 bases, parents, imports, or write authority.

Cutover is one reviewed registration change after the red journey passes: task orchestration allocates V3 Turns/grants; the model sees the V3 workspace capability; `/artifacts-v3` and V3 sync/realtime drive Desktop; new managed writes cannot reach V1/V2; historical V1/V2 routes are GET/read-only. There is no dual write, shadow projection, fallback, or “success” copied from legacy status.

## 12. Red acceptance journey

`scripts/runners/artifact-v3-multipart-e2e.mjs` is intentionally red until the implementation checkpoints provide the contract above. Its substantive gates require:

- a real auto session and one managed authoring task, not direct fixture upload;
- V3 HTTP and V3 realtime Artifact events;
- a native Artifact repository with conventional files, one root commit, and strict `git fsck`;
- real browser screenshots of the root complete Artifact, a complete candidate, and the old root Revision;
- a follow-up targeting `pricing` in `scripts/fixtures/artifact-v3-multipart/` that must also update shared CSS and the Hero-to-plan interaction;
- successful complete-project build/interaction/pixel evidence before selection;
- a head CAS to the exact Candidate commit, with the old commit still previewable and unchanged files retaining blob OIDs;
- a generated 96-Part Artifact using the same manifest and authoring protocol, with no truncation of Part or commit history;
- absence of V1/V2 writes or legacy records in the journey projection.

The runner refuses to pass on source-string assertions, status labels, V2 records, mocked previews, or caller-uploaded final bytes. `--preflight` validates only the fixture and runner structure and must exit with the distinct red code until a live V3 route exists.

## 13. Implementation and proof obligations

Before cutover, deterministic tests must cover path containment/symlinks, quotas, exact owner authorization, stale base/head CAS, idempotency reuse, concurrent selection, cancellation/process cleanup, commit corruption, Git-before-event and event-before-Git crash points, restart reconciliation, complete-build failure with unchanged head, same-Turn repair, realtime gap/hydration, and historical preview. Every rejection asserts unchanged refs, projection, outbox, and destination.

Final proof runs the bounded journey twice, restarting the daemon and running strict `git fsck` between stages, then runs one bounded provider-backed initial creation and targeted follow-up. Pixel inspection covers current, candidate, selected, and prior Revision views. Validation is failed—not skipped—if any stage writes a V1/V2 record, independently publishes a Part, uses mocked pixels, accepts only a status label, truncates the 96-Part fixture, or cannot reopen the exact old commit.

## Relevant filepaths

- `docs/checkpoints/artifact-v3-architecture.md`
- `scripts/runners/artifact-v3-multipart-e2e.mjs`
- `scripts/fixtures/artifact-v3-multipart/`
- `swarmd/internal/artifactv2/author.go`
- `swarmd/internal/artifactv2/service.go`
- `swarmd/internal/artifactgit/types.go`
- `swarmd/internal/artifactgit/repository.go`
- `swarmd/internal/tool/runtime_artifact_v2_author.go`
- `swarmd/internal/run/artifact_v2_designer.go`
- `swarmd/internal/api/sessions_v3_artifact_v2.go`
- `web/src/features/desktop/session-v3/artifact-v2-api.ts`
- `web/src/features/desktop/session-v3/artifact-v2-studio-model.ts`
- `web/src/features/desktop/chat/components/desktop-v3-artifact-v2-studio.tsx`
- `swarmd/internal/runtime/daemon.go`

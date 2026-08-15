# Progressive AI-assisted video editing and iteration plan

Status: execution-ready product and architecture design. This document changes no production behavior.

## Decision

Build **progressive AI-assisted editing**, not a one-shot “generate video” command.

The product loop should be:

1. collect and understand source clips;
2. let AI propose an ordered, trimmed edit against an immutable revision;
3. show the proposal as a timeline diff and playable preview;
4. require the user to accept, revise, or branch it;
5. create alternate edit branches when requested;
6. render reviewed revisions into managed, seekable video artifacts;
7. let the user select a render or revision as the durable reference for the next AI turn.

An AI proposal must never silently replace the user’s selected edit. An accepted timeline revision and a rendered video are different durable objects: the revision is the editing authority; the artifact is a reproducible output of that exact revision.

For launch, use ordinary browser-seekable MP4 proxies and authenticated byte-range delivery. Do not load a large video into a JavaScript `Blob`, embed it in a session event, or make HLS the first implementation. Add adaptive streaming later only if measured local/remote use requires it.

## Current state

Swarm has useful pieces, but not this end-to-end flow:

- The Desktop Video tool can create DB-backed video threads, add local clips, reorder or hide them, persist a simple `timelineSegments` metadata array, and play a client-composited timeline.
- A Video tool thread can launch a child AI session, but the child receives only loose session metadata such as thread ID and clip order. There is no authenticated, immutable edit-revision reference, no editing tool contract, and no compare/apply workflow.
- Video transcription produces durable source fingerprints, transcript references, content digests, section indexes, evidence ranges, and conservative splice manifests.
- Current splice boundaries remain transcript-derived. Frame anchors are `not_requested`, low-confidence cuts require verification, and no renderer consumes the manifest.
- Managed artifacts already provide collections, variants, lineage, durable event sequences, selected variants, and opaque references that can be attached back to chat.
- The artifact gallery already groups and navigates iterations, but it does not render `video/*` media inline and currently downloads a complete artifact into a browser `Blob` before previewing it.
- Managed artifact defaults are unsuitable for real video: a single stored artifact defaults to 64 MiB, a session to 512 MiB, and the generic inline artifact route rejects previews over 32 MiB.
- Clip media already uses authenticated `http.ServeContent` byte-range delivery. This is the correct starting pattern for video playback.
- Media settings still labels managed video generation “Coming soon.”

Therefore, Swarm has a basic manual timeline, durable video understanding, and reusable artifact lineage, but it lacks the canonical revision graph, AI edit operations, review workflow, verified renderer, large-media artifact path, and video viewer required for progressive creation.

## Product model

### 1. Video project

Evolve the current video thread into a typed `video_project.v1` aggregate. Do not continue placing editing authority in an unvalidated metadata map.

A project contains:

- account and workspace ownership;
- opaque project ID and monotonic project version;
- source assets and their immutable fingerprints;
- one or more edit variants;
- immutable edit revisions and their ancestry;
- the currently selected variant and revision;
- render jobs and managed artifact references;
- review decisions and bounded AI rationale references;
- a canonical assistant session reference when chat is attached.

The video-project service is the authority for editing state. V3 remains the authority for chat/session messages and managed-artifact metadata. A V3 message may carry an opaque video-project or edit-revision reference, but must not copy the complete project into message metadata or create a second timeline authority.

### 2. Source asset

A source asset reference must be stable even if its display name changes. It should contain:

- opaque `source_asset_id`;
- source fingerprint and content digest;
- bounded media probe result: duration, dimensions, rotation, frame rate/time base, codecs, audio streams, keyframes, and size;
- source kind: imported local clip, managed generated clip, or rendered derivative;
- durable transcript/index references when available;
- origin/provenance reference without exposing a private filesystem path to the model;
- availability and validation state.

Original user media stays untouched. Rendering and AI tools receive resolved, authenticated handles, not arbitrary paths.

### 3. Edit revision

Every meaningful edit creates an immutable `video_edit_revision.v1` record with:

- `project_id`, `variant_id`, `revision_id`, parent revision ID, project version, and content digest;
- an ordered timeline of typed segments;
- exact source in/out times, timeline position, track, visibility, and transition/audio settings;
- source fingerprints and transcript/evidence references used by the edit;
- author kind (`user`, `ai_proposal`, or `system_migration`), rationale, and schema/policy version;
- validation and review state;
- render references produced from this revision.

A segment references a source asset and range. It must not persist a browser URL or filesystem path. The first implementation can support one video track plus source audio, hard cuts, reorder, include/exclude, and exact trims. Transitions, overlays, captions, music, and generated media are later typed operations, not arbitrary FFmpeg text.

Updates use optimistic concurrency: a proposal names its expected parent revision. If the user or another AI turn has advanced the variant, applying the stale proposal fails with a visible conflict instead of overwriting newer work.

### 4. Variants and progression

A `video_edit_variant.v1` is a named branch whose head points to one revision. Examples are “Concise,” “Product first,” and “Story led.”

- Forking creates another variant from an existing immutable revision.
- Iterating advances only that variant’s head.
- Selecting a variant does not delete alternatives.
- A render always records the exact revision ID and digest it used.
- The user may select either a timeline revision or a ready render as the reference for the next instruction.
- Unselected variants remain available until the user explicitly deletes them or retention policy safely removes unpinned derived renders.

This gives the AI a real reference point: “make the selected concise cut 10 seconds shorter” resolves to one exact revision, while “try three different openings” creates three branches from the same parent.

## Durable reference contract

Use small opaque references in chat and tools:

```json
{
  "project_id": "opaque",
  "variant_id": "opaque",
  "revision_id": "opaque",
  "revision_digest": "sha256:opaque",
  "event_seq": 123
}
```

Managed renders keep the existing artifact identity shape:

```json
{
  "session_id": "opaque",
  "collection_id": "opaque",
  "variant_id": "opaque",
  "event_seq": 456
}
```

The resolver validates ownership, event sequence, revision digest, source fingerprints, and readiness before hydrating bounded context. The AI normally receives:

- project title and current selected revision;
- compact timeline and variant summaries;
- transcript section/evidence references relevant to the requested change;
- render descriptors, posters, and selected review state;
- explicit omissions and truncation indicators.

It does not receive video bytes, private paths, an unbounded transcript, or all historical revisions. The AI can request a bounded revision diff or transcript range when needed.

## AI editing contract

Add a dedicated editing tool rather than turning transcription actions into an unrelated renderer. A future `manage_video_edit` surface should support bounded actions such as:

- `get_project` and `read_revision`;
- `read_revision_diff` and `list_variants`;
- `propose_revision` from typed timeline operations;
- `propose_variants` from one immutable base revision;
- `request_frame_verification`;
- `request_draft_render` and `render_status`;
- `select_revision` or `select_render` only after an explicit user action/review contract.

The model submits typed operations such as reorder, trim, include, exclude, split, and replace. The backend compiles and validates the resulting full revision. The model never submits a shell command, FFmpeg filter graph, storage path, artifact destination, or ownership metadata.

`propose_revision` creates a reviewable proposal; it does not mutate the selected head. Applying a proposal is an explicit user action or a separately authorized instruction with an exact expected parent revision.

## Progressive Desktop experience

### Main editing surface

Keep the existing source list, player, and timeline, but make their authority explicit:

- **Sources/evidence:** clips, transcript readiness, source availability, and section markers.
- **Player:** virtual timeline preview for instant reorder/trim feedback; selected rendered proxy when reviewing output fidelity.
- **Timeline:** typed segments with in/out handles, included/hidden state, and revision identity.
- **AI sidecar:** suggestions tied to the current revision, each showing rationale, duration change, and affected segments.
- **History:** a progression rail of immutable revisions with author, timestamp, review state, and render state.
- **Variants:** named branch cards with duration, segment count, poster, rationale, and selected state.

### Review behavior

An AI edit appears as a diff card:

- moved, added, removed, and trimmed segments;
- old/new duration;
- boundary verification status;
- warnings for missing media, low confidence, audio discontinuity, or stale source;
- actions: preview, compare, accept, revise, or fork.

For alternatives, provide synchronized A/B review: select two branches, use one shared playhead where practical, and jump between their rendered proxies or virtual timelines. The user can choose one, ask for another iteration from either, or retain both.

The UI should use precise language:

- **AI-assisted edit** for ordered/trimmed existing clips;
- **render** for producing a playable file;
- **generated clip** for newly synthesized media;
- **video generation** only when the workflow can actually create source media and compose it end to end.

### AI continuation

Starting chat from the Video tool must attach an authenticated project/revision resource, not only `parent_video_thread_id` metadata. Returning from chat should reopen the same project and highlight proposals or renders created by that session. Attaching a ready render or revision to a later message gives the AI the exact durable reference to continue from.

## Frame verification and fail-closed rendering

### Frame verification first

Before a cut can render, extract and inspect bounded frames around every proposed boundary, normally before/at/after plus the nearest decodable keyframe. Record:

- exact requested and decoded timestamps;
- source fingerprint;
- frame digests and dimensions;
- decode status;
- visual/OCR evidence and confidence where used;
- human approval/rejection when policy requires it.

The current transcript-derived splice manifest must remain non-automatic until this exists. Missing, stale, undecodable, or source-mismatched frames keep the boundary in `verification_required` and block rendering.

### Canonical render manifest

Compile an accepted revision into `video_render_manifest.v1`. It contains only validated source handles and typed operations. The executor:

1. revalidates ownership, revision digest, source fingerprints, media probes, bounds, and boundary reviews;
2. preflights available disk, estimated output size, duration, and resource limits;
3. compiles deterministic FFmpeg argv without a shell;
4. writes to private same-filesystem staging storage;
5. supports cancellation, timeout, bounded logs, and crash-safe job state;
6. re-encodes exact cuts and normalizes the first launch codec profile;
7. validates output with `ffprobe`, expected duration tolerance, required streams, dimensions, and first/middle/last frame decodes;
8. hashes the completed file and atomically publishes it as a managed video artifact;
9. records terminal failure instead of publishing partial or unverified bytes.

Never execute model-authored FFmpeg commands. Never fall back from a failed exact render to an approximate concat. Never publish the output before validation and atomic finalization.

The initial output profile should be broadly playable H.264/AAC MP4 with `faststart`, a fixed pixel format, and explicit landscape/portrait dimensions. Preserve a future codec-profile field so additional formats do not redefine old manifests.

## Managed video artifacts and large files

### Extend the artifact lifecycle, not the browser Blob path

Rendered videos should appear in the existing managed artifact catalog and collection lineage, but large media needs a dedicated backing mode inside that lifecycle:

- artifact metadata remains in durable V3 state;
- bytes live in private app-managed workspace storage;
- public/session records expose no storage path;
- a video artifact descriptor records size, digest, duration, dimensions, codecs, poster reference, rendition references, and source edit revision;
- authenticated GET/HEAD supports byte ranges and `Content-Length`;
- the Desktop `<video>` element points directly at the range endpoint;
- download remains available without first buffering the complete file in JavaScript.

The current 32 MiB generic preview cap, 64 MiB artifact cap, 512 MiB session cap, and full-Blob gallery fetch cannot be reused unchanged for real renders.

### Rendition strategy

Publish a render set atomically at the metadata level:

- **review proxy:** 540p or 720p H.264/AAC, moderate bitrate, `faststart`, optimized for immediate in-app review;
- **master:** requested resolution/quality, generated only when requested or policy permits;
- **poster/contact sheet:** small image artifact for gallery and timeline cards;
- optional manifest/report: bounded JSON or text with revision and validation evidence.

The UI streams the proxy by default and lets the user open/download the master. A failed master must not invalidate an already validated proxy, but the collection must show partial failure clearly.

For launch, progressive MP4 plus byte ranges is enough. Consider HLS/DASH only after evidence shows that remote access, multi-gigabyte masters, or unstable links make range-served MP4 inadequate.

### Quotas, retention, and pressure

Introduce explicit media quotas rather than silently raising generic artifact limits:

- preflight source duration, estimated output bytes, and free disk;
- configurable per-render, per-project, and per-workspace derived-media limits;
- fail before rendering when reservation cannot be made;
- keep originals by reference rather than copying them;
- pin the selected revision’s proxy/master and any artifact attached to a message or handoff;
- allow cleanup of abandoned staging files and unpinned derived renders after a visible retention window;
- never delete source media or the edit revision needed to reproduce a retained render;
- surface “proxy available, master not retained” and similar states honestly.

Multi-gigabyte media should remain playable because seeking reads ranges from disk. It should not consume equivalent frontend memory. If the source or output exceeds configured local limits, offer proxy-only rendering, an explicit export destination, or a clear refusal—never an implicit truncation.

## Ordered implementation plan

### Phase 1 — Typed project and revision authority

1. Define `video_project.v1`, source asset, edit revision, variant, review, render-job, and opaque reference schemas.
2. Migrate the current thread/order/timeline projection without retaining `timelineSegments` metadata as a second authority.
3. Add optimistic concurrency, revision diffs, branch/fork semantics, and authenticated project references in V3 messages.
4. Add provider-free validation for segment bounds, fingerprints, chronology, duplicate IDs, stale parents, replay, and migration.

Gate: a user can create, edit, reload, fork, compare, and select immutable revisions without AI or rendering, and a stale update fails visibly.

### Phase 2 — AI proposal and progressive review

1. Add the bounded `manage_video_edit` read/proposal contract.
2. Hydrate compact project, revision, and transcript evidence by reference.
3. Show AI proposals as unapplied timeline diffs with accept/revise/fork actions.
4. Add progression history and variant comparison UI.

Gate: AI can propose clip order, inclusion, and exact trims from one immutable base; no proposal changes the selected edit until reviewed.

### Phase 3 — Frame verification

1. Add deterministic frame extraction around every boundary.
2. Persist verification records keyed by source fingerprint and timestamp.
3. Show boundary frames and warnings in proposal review.
4. Keep every unresolved boundary fail-closed.

Gate: changed sources, undecodable frames, out-of-range timestamps, and unreviewed cuts cannot reach rendering.

### Phase 4 — Fail-closed render and in-app video viewing

1. Implement the typed manifest compiler and cancellable FFmpeg executor.
2. Add private staging, reservation, validation, hashing, atomic publication, and restart reconciliation.
3. Extend managed artifacts with video descriptors, proxy/master renditions, posters, and authenticated range delivery.
4. Add native `<video>` review to the Video tool and artifact gallery without full-Blob buffering.

Gate: one reviewed real project renders successfully, survives seek/reload, appears in its artifact collection, retains exact revision lineage, and is attachable back to chat by opaque reference.

### Phase 5 — Failure evidence and guarded pilot

After the first successful real render:

1. add end-to-end failures for missing/changed sources, bad timestamps, decode failure, FFmpeg absence, disk exhaustion, cancellation, daemon restart, partial output, invalid codecs, artifact-finalization failure, range requests, and large proxy playback;
2. rerun the real 184-second pipeline and a materially larger fixture;
3. measure render time, output size, seek latency, frontend memory, cleanup, and restart behavior;
4. run a human-reviewed pilot with unattended cuts disabled.

Gate: injected failures never publish a false-ready artifact, and a large render is viewable without loading the complete file into frontend memory.

### Phase 6 — Rich composition and generated media

Only after the guarded pilot:

- add captions, audio levels, transitions, overlays, music, and richer multi-track operations one typed capability at a time;
- add AI-generated clips as source assets with provider/model/prompt/artifact provenance;
- let users create multiple composition variants from the same selected revision;
- add cost/time estimates and explicit approval before provider-billed generation;
- retain the same revision, review, render, artifact, and durable-reference contracts.

This is the point where “AI video creation” can become accurate product language.

## Acceptance and validation matrix

Required contract tests:

- immutable revision replay and deterministic digest;
- optimistic concurrency and stale proposal rejection;
- branch isolation and selected-variant persistence;
- source fingerprint and ownership enforcement;
- bounded AI context hydration and stale reference rejection;
- exact edit-to-render-manifest compilation;
- frame-verification requirements and review enforcement;
- render cancellation/restart/idempotency;
- artifact proxy/master partial-failure semantics;
- HTTP range, seek, HEAD, MIME, auth, and no-path-leak behavior;
- Desktop playback without whole-file `Blob` allocation;
- quota reservation, cleanup, pinning, and source preservation.

Required real fixtures:

- the existing 184-second multimodal source;
- clips with mismatched codecs, frame rates, dimensions, rotation, and audio presence;
- variable-frame-rate and corrupted clips;
- an output larger than the current 32/64 MiB preview/artifact limits;
- a long source proving bounded memory and seek behavior;
- three alternatives from the same parent revision followed by one additional iteration from a non-selected branch.

Do not call the flow end to end until the user can start from sources, review an AI proposal, branch alternatives, render one exact revision, view it in the app, attach the durable reference to a later AI message, and continue editing from that same revision after restart.

## Best next move

Implement Phases 1–4 as one staged program with separate ownership for the revision contract, AI proposal surface, frame verification, render executor, managed-media delivery, and Desktop review. Integrate frame verification before allowing the renderer to publish anything. Then complete one real reviewed render, add the failure matrix, and begin a guarded human-reviewed pilot.

Do not start with generative composition UX, arbitrary FFmpeg control, HLS, or unattended cuts. The highest-leverage foundation is the immutable edit revision plus durable reference: it makes manual edits, AI proposals, alternatives, rendering, review, and later generated clips all converge on one reproducible workflow.

## Likely attack points

- `swarmd/internal/store/pebble/video_thread_store.go` — current thread snapshot has clip/order fields and untyped metadata, but no revision graph or concurrency contract.
- `swarmd/internal/api/video_threads.go` — current project CRUD and clip byte-range serving; useful range pattern, but timeline updates overwrite snapshots.
- `web/src/features/desktop/tools/pages/video-tool-page.tsx` — current client-composited player, metadata timeline, reordering, hiding, and weak chat handoff.
- `swarmd/internal/videotranscription/section_index.go` — durable evidence and splice proposal foundation; frame anchors are not yet verified.
- `swarmd/internal/tool/runtime_manage_video.go` — transcription-oriented tool that should remain focused while a dedicated edit tool is added.
- `swarmd/internal/store/pebble/session_artifact_store.go` — reusable collection, variant, lineage, selection, and event-sequence contracts.
- `swarmd/internal/artifact/service.go` — private managed bytes and atomic staging, currently bounded for smaller artifacts.
- `swarmd/internal/api/sessions_v3_artifacts.go` — authenticated range-capable serving, but generic preview size limits block large video.
- `web/src/features/desktop/session-v3/artifact-api.ts` — durable opaque artifact selections and catalog lineage.
- `web/src/features/desktop/chat/components/desktop-v3-artifact-gallery.tsx` — iteration UI and selection flow, but full-Blob fetching and no video player.
- `web/src/features/desktop/settings/media/components/media-settings-page.tsx` — current understanding controls and explicit “Coming soon” video-generation state.

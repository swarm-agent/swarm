# Artifact V3 restoration gap audit

Status: **RED — restoration gates are unverified**

Audit date: 2026-09-04

Scope: ordinary Swarm creation, regular managed Designer creation and iteration, Designer Iteration Swarms, native Git/project authoring, build and preview, durable events and realtime, API and Desktop Artifact Studio, exact prior/candidate revisions, storyboards, HTML animation, Video Studio, and MP4 conversion.

## Executive verdict

Artifact V3 has substantial native implementation, and preserved live evidence reached isolated root Git revisions and browser previews. That is not an end-to-end acceptance result. Every preserved Artifact V3 journey summary is RED, the first required product gate was never isolated and proven, and no native Artifact V3 adapter to the storyboard/Video Studio/MP4 pipeline was confirmed.

The restoration order is mandatory and now starts below the Designer boundary:

1. Prove the complete deterministic no-Designer product flow in bounded sub-gates: static HTML root, manifest Parts, exact-base turn revisions, continued iteration or alternate candidate selection, Part animation, and native storyboard/HTML-animation-to-Video-Studio/MP4 conversion.
2. Commit the reviewed deterministic foundation, deploy that exact clean commit to the isolated testbench, and prove the smallest ordinary primary-Swarm request creates exactly one usable static HTML Artifact V3.
3. Expand the ordinary-Swarm journey through targeted turns, continued edits, alternate iteration selection, animation, and media conversion on the already-proven native contracts.
4. Only after those no-Designer and ordinary-Swarm gates are green, prove regular managed Designer creation/iteration and then Designer Iteration Swarm sibling candidates.

**The first deterministic static-HTML sub-gate and the ordinary-Swarm gate are currently unverified.** The earlier work began with a richer managed-Designer multipart journey and then attempted sidebar follow-up and multi-candidate behavior. That was the wrong foundational proof. A root revision produced during a later RED journey does not retroactively prove either smaller contract.

This document began as an audit-only checkpoint. The subsequent approved restoration checkpoint adopted it as the executable gate contract. Existing implementation changes and ignored evidence remain preserved and unaccepted until the focused deterministic tests and exact-commit live gates below establish them.

## Evidence standard

The classifications below distinguish three different statements:

- **Implemented:** a production path and focused tests exist in the current worktree.
- **Observed in an isolated RED run:** preserved live evidence reached the stated boundary, but the enclosing acceptance journey failed.
- **Validated:** the purpose-built gate passed with exact durable, Git, API, realtime, Desktop, and pixel evidence. Nothing in this audit is classified as validated end to end.

Source shape, a passing unit test, a `ready` label, a build receipt, or one screenshot cannot substitute for the gate-specific evidence. Later gates cannot be used to waive an earlier gate.

## Preserved live evidence

Ignored evidence remains under these workspace-relative locations:

- `.tmp/artifact-v3-three-part-proof-off/summary.json` — RED; the initial request produced zero Artifact V3 projects.
- `.tmp/artifact-v3-three-part-proof/summary.json` — RED; the initial run timed out.
- `.tmp/artifact-v3-three-part-proof-fixed/summary.json` — RED before browser inspection because Playwright was unavailable from the selected package boundary.
- `.tmp/artifact-v3-three-part-proof-fixed-2/summary.json` — RED after a root revision and screenshots; the runner found no running Hero animation.
- `.tmp/artifact-v3-three-part-proof-async-2/summary.json` and `.tmp/artifact-v3-three-part-proof-async-3/summary.json` — RED after one root revision and complete-preview evidence; Desktop could not find the Artifact V3 Studio card for the sidebar iteration.
- `.tmp/artifact-v3-three-part-proof-async-4/summary.json` — RED after a root revision and four screenshots; the targeted sidebar follow-up timed out.
- `.tmp/artifact-v3-followup-resume/summary.json` — RED; the resumed follow-up timed out.
- `.tmp/artifact-v3-dump-proof/` and the `session-dump.json` files in the evidence directories — proof that bounded dev-mode session capture became available for diagnosis, not proof of Artifact V3 correctness.
- `.tmp/artifact-v3-orb-proof/`, `.tmp/artifact-v3-token-proof/`, and `.tmp/artifact-v3-studio-diagnosis*/` — isolated preview/authentication/Desktop evidence only.

These files contain private diagnostic identifiers and must remain ignored. Public documentation and test ledgers should summarize their behavior without copying durable session IDs.

## Current boundary inventory

### Seven V3-native boundaries

| Boundary | Implemented evidence | Current acceptance state |
| --- | --- | --- |
| Context-bound whole-project author capability | `swarmd/internal/tool/runtime_artifact_v3_author.go` provides contained list/read/create/edit/rename/delete/diff, repeatable `build_preview`, and `finish_turn`, bound to an injected grant and producer run. | Implemented; not directly proven through ordinary Swarm. |
| Managed Designer turn allocation | `swarmd/internal/run/artifact_v3_designer.go` allocates initial or exact-base follow-up turns without allocating V1/V2 collections. | Implemented; only observed inside RED runs. |
| Git-backed revision authority | `swarmd/internal/store/pebble/artifact_git_v3_repository.go` owns complete project trees, commits, refs, materialization, history, and selection mechanics. | Implemented; strict end-to-end Git ancestry, fsck, and restart proof remains open. |
| Durable V3 service and projections | `artifact_v3_service.go` and `artifact_v3_projection_store.go` bind Artifact, turn, candidate, revision, and head state to Pebble records. | Implemented; complete crash/reconciliation journey is unverified. |
| V3 runtime build and preview bridge | `swarmd/internal/runtime/artifact_v3_runtime.go` bridges authoring, project validation, trusted browser capture, evidence, exact revision reads, and preview resources without importing Artifact V1/V2. | Implemented; isolated browser output exists, but the current validator/renderer contract is not accepted end to end. |
| Native HTTP and session event/realtime shape | `swarmd/internal/api/sessions_v3_artifact_v3.go` exposes `/artifacts-v3`, exact revisions, preview access, turns, selection, and `artifact.v3.*` projection events through the V3 mutation/outbox path. | Implemented; no complete passing replay plus live-cursor journey exists. |
| Desktop Artifact V3 API and Studio | `artifact-v3-api.ts` and `desktop-v3-artifact-v3-studio.tsx` normalize the native catalog, revisions, turns, previews, iteration prompt, and selection CAS. | Implemented; RED evidence includes Studio discovery/follow-up failures. |

### Three neutral shared-infrastructure boundaries

These can be retained only behind V3-owned interfaces and cannot establish V3 correctness by themselves:

1. authenticated account/user/session identity and the canonical V3 session mutation/outbox mechanism;
2. canonical private storage roots, audited Git command execution, and bounded process/workspace containment;
3. trusted browser capture, preview-ticket cryptography, and video-render primitives where their input/output identity is rebound to exact Artifact V3 revisions.

### Six legacy-entangled or legacy-shaped boundaries

| Boundary | Evidence and risk | Required restoration decision |
| --- | --- | --- |
| Generic task parsing and target handling | `service_task_launch.go` carries V1 `source_artifact`, V2 source, V3 source, section locators, regular launches, and swarm parsing in one surface. The interrupted worktree changes alter V3 target-hint semantics after provider failures. | Freeze a minimal V3 source/target contract, prove it at Gate 2, then apply it consistently to swarm parsing. Do not treat parser acceptance as authoring success. |
| Shared launch/provider context | `service_tools.go`, `service.go`, `service_background.go`, and `provider_tool_invoker.go` carry V1, V2, and V3 contexts side by side. | Prove that a V3 launch cannot acquire or fall back to V1/V2 write authority and that retries retain the exact V3 grant/base. |
| Legacy-shaped task handoff fields | A finished V3 candidate is placed into the generic `taskArtifactReference` using `CollectionID=ArtifactID`, `VariantID=CommitOID`, and `EventSeq=projection sequence`. | Introduce or strictly validate a native V3 handoff identity. Downstream code must not infer that these are V1 collection/variant semantics. |
| Reused preview-token plumbing | Artifact V3 reuses the existing encrypted preview capability and Desktop boundary. This is reasonable shared cryptography but remains legacy-shaped routing/auth plumbing. | Keep the cryptographic primitive only if exact session, artifact, revision, expiry, method, and asset-path rejection are proven with no query/cookie fallback. |
| Side-by-side authority registration | Runtime and tools still contain V1/V2 readers and retired V2 author branches while V3 is registered for new managed Designer writes. | Prove new V3 writes cannot reach V1/V2 records. Label historical paths read-only; remove dead write branches only in a separately reviewed implementation checkpoint. |
| Mixed Desktop catalog/state handling | Desktop still supports historical V1/V2 artifacts alongside V3, and the native V3 normalizer includes compatibility fallbacks for multiple field shapes. RED runs failed to surface the V3 Studio card reliably. | Give native V3 catalog/realtime state an explicit selector and identity contract; historical readers must not become a second current-state authority. |

## Gap map by product path

### Ordinary Swarm to one Artifact V3 — RED / unverified

The current model-visible `artifact_v3_author` capability is enabled for managed Designer output, while primary Swarm reaches it through generic task orchestration. The intended ordinary user journey therefore crosses Swarm reasoning, task argument construction, one child allocation, context propagation, authoring, Git/Pebble commit, projection publication, and Desktop discovery.

What exists:

- primary prompt guidance instructs Swarm to use managed Designer task delegation for managed creative output;
- the task parser accepts managed Designer launches and the coordinator creates a V3 grant;
- the Designer profile enables `artifact_v3_author` and disables V1/V2 managed writers;
- the runtime can persist and expose a root revision.

What is not proven:

- a purpose-built minimal user prompt to ordinary Swarm produces exactly one V3 artifact without direct fixture upload, preseeded V3 source, direct child invocation, or legacy managed write;
- the parent waits for and reports the exact V3 result rather than a legacy-shaped success placeholder;
- the artifact appears through durable V3 state and Desktop after reload;
- its preview pixels are inspected and the exact Git revision remains readable after daemon restart;
- a failed first authoring attempt is repaired or fails honestly without producing a partial artifact.

### Regular Designer creation and focused iteration — RED

Preserved runs demonstrate that one managed Designer can sometimes create a root commit and browser preview. Other runs exposed manifest-schema drift, empty-repository recovery failure, preview subresource authentication/ORB failure, Desktop packaging/discovery issues, section-target schema drift, and repeated follow-up timeouts.

The current uncommitted patch changes V3 `section_target` from an authoritative locator payload to an optional ID-matching display hint and validates target IDs against the exact head manifest during turn preparation. That direction may remove redundant model-authored locator authority, but it is implementation-under-review, not accepted behavior. It has not completed a passing focused iteration with an unchanged old head, one ready child commit, exact prior preview, and explicit selection CAS.

### Designer Iteration Swarms — RED / not attempted at the correct dependency point

The task swarm parser and hydration structs carry `artifact_v3_source`, and expanded swarm launch specs flow through the same V3 allocator. This is code-path availability, not a proven Iteration Round contract.

Open gaps include:

- the swarm parser still requires stricter duplicated `section_target(s)` matching than the interrupted regular-path patch, so regular and swarm V3 target semantics have drifted;
- every initial managed Designer launch currently receives an independently derived Artifact ID from its launch/task index unless orchestration deliberately binds a shared artifact/base; the required one-artifact sibling-candidate semantics need proof, not inference;
- candidate count, sibling ancestry, exact shared base, partial-wave failure preservation, selection, and rejected-candidate retention have no passing V3 swarm journey;
- generic swarm/task metadata still uses legacy artifact-reference fields and must not become candidate authority.

No Designer Swarm should be launched until Gates 1 and 2 pass.

### Git, authoring, build, preview, and revision recovery — partially implemented, not accepted

High-value unresolved points:

- `Build` currently validates the manifest and complete HTML document but otherwise copies the project tree as build output; it is not yet proof of a general server-owned project build contract.
- `Preview` captures one injected `default` state and records one PNG digest. It does not itself perform the complete interaction, console/runtime, multi-state, animation, responsive, or model-independent pixel-quality obligations described by the architecture contract.
- in-memory maps retain grants, builds, and preview results. Durable Git/Pebble revisions survive, but author-turn resume, same-turn repair across daemon restart, and exact evidence recovery require explicit proof.
- API revision lookup trims a `revision-` prefix rather than authenticating a closed opaque revision reference. Exact ownership and commit checks exist below it, but reference shape and stale/foreign negative behavior need one canonical contract.
- catalog enumeration begins from repository directories and joins them to Pebble projections. Corrupt, missing, or orphaned sides need bounded reconciliation and explicit unavailable state rather than silent omission.
- the current live runner proves many obligations only after a multipart animated Designer request, so it cannot serve as the minimal Gate 1 proof and has never reached PASS.

### Durable event/realtime and Desktop Studio — implemented path, RED proof

The API publishes `artifact.v3.*` payloads through the session mutation and realtime outbox boundary. Desktop has native HTTP models and Studio UI. Nevertheless, preserved runs timed out finding the V3 Studio card or completing the sidebar follow-up, and no full run proved all of:

- bootstrap/hydrate contains the exact artifact and head;
- a cursor-bearing realtime event updates the same native entity;
- reload/reconnect does not duplicate, lose, or regress the head;
- candidate generation does not move the selected head;
- a prior exact revision remains previewable after selection;
- historical V1/V2 catalog entries cannot shadow the V3 entity.

### Storyboard, HTML animation, Video Studio, and MP4 — no confirmed native V3 adapter

The downstream implementation remains centered on Artifact V2:

- `swarmd/internal/artifactv2/storyboard.go` and `storyboard_service.go` define V2 storyboard parts, heads, stills, and capture-state lineage;
- `swarmd/internal/artifactv2/video_conversion.go` explicitly accepts an exact **Artifact V2** published head, resolves V2 derivatives, builds `ArtifactV2VideoReference` values, and creates an Artifact-V2 conversion proposal;
- `swarmd/internal/tool/runtime_manage_video.go` exposes `convert_artifact_v2`;
- `runtime_manage_artifact_animation.go` and legacy `runtime_manage_video_storyboard.go` operate on V1 collection/variant references and hand-authored capture/storyboard inputs;
- Video Studio plan records and render promotion understand V1 managed references and V2 references, not native Artifact V3 revision/preview/build identities.

No audited production boundary currently converts one exact Artifact V3 commit into:

1. stable storyboard sections with filming requirements and production state;
2. exact capture-state stills bound to that commit/tree/manifest and validation evidence;
3. compatible live HTML animation candidates plus a render-ready fallback;
4. one pending Video Studio proposal preserving V3 source identity;
5. a selected exact-lineage MP4 derivative suitable for final rendering.

A V3 adapter must not translate a V3 commit into a fake V1 collection/variant or V2 published head. It needs native typed identity all the way through proposal, selection, derivative, and render materialization.

## Dependency-ordered restoration program

A gate is green only when its purpose-built acceptance journey passes against the exact reviewed source it names. Deterministic no-Designer sub-gates run locally first. Provider-backed and Desktop journeys run against one clean committed checkout deployed to the isolated container; their repeatability gate passes twice on the same commit, including one daemon restart where specified. Stop at the first RED gate and repair only that layer.

### Gate 0 — deterministic native Artifact V3 without Designers

Objective: prove the complete Artifact V3 and media contracts directly through their server-owned services before any model or Designer can obscure a missing boundary.

#### Gate 0A — static HTML root and Parts

1. Create one Artifact V3 author turn through the native coordinator, not by writing Git/Pebble fixtures directly.
2. Use the context-bound author service to create a conventional `swarm-artifact.json`, `index.html`, and shared assets.
3. Run the real builder and previewer, finish one root commit, and assert exactly one Artifact, one parentless commit, valid build/preview evidence, and zero V1/V2 writes.
4. Read the native API projection and revision-bound preview; verify every declared stable Part resolves in the complete rendered document.

#### Gate 0B — targeted turn and continued turn history

1. Start from Gate 0A's exact selected head and target one manifest Part by ID.
2. Edit the complete project, including shared files when coherence requires it; build/preview and finish one direct-child candidate.
3. Prove candidate creation does not move head. Select it using exact expected-head and projection CAS.
4. Start another user turn from the selected candidate and prove the base is the exact chosen commit, not the original root or an independently stored Part revision.
5. Reopen root, first candidate, and continued candidate as complete previews. Stale/unknown target and selection requests must leave Git, projection, and outbox unchanged.

#### Gate 0C — alternate iterations and choice

1. From one exact head, create at least two complete sibling candidates in one turn without a Designer or task swarm.
2. Prove both candidates share one base, remain independently previewable, and do not move head.
3. Select either candidate by CAS; retain the alternate and prior revision for later comparison and branching.
4. Continue a new targeted turn from the selected alternative and prove lineage has no part stitching or legacy identity translation.

#### Gate 0D — animation inside Parts

1. Add CSS/WAAPI/SVG animation to declared Parts in a complete candidate while preserving stable IDs.
2. Validate runtime/console health, clipping/overflow, reduced-motion/lifecycle behavior, deterministic seek or representative frame sampling, and visibly changing pixels.
3. Repeat a targeted Part turn and alternate selection while preserving animation in unrelated Parts and retaining exact prior animated previews.
4. Failed animation validation or export must leave the selected head unchanged and the prior preview usable.

#### Gate 0E — native V3 storyboard and HTML-animation-to-video

1. Bind storyboard sections, filming requirements, production state, capture states, and animation profile/duration to one exact selected V3 commit and build.
2. Produce exact state stills plus a render-ready animation fallback with native V3 source lineage.
3. Convert that exact head through a new server-owned V3 adapter into one pending Video Studio proposal; never impersonate a V1 collection/variant or V2 published head, and never self-accept the proposal.
4. After explicit candidate selection, export/promote an exact-lineage MP4 and prove Video Studio can materialize it while stale source, mismatched profile/duration, missing fallback, failed export, and cancellation make no partial mutation.

Gate 0 evidence requirements:

- focused service/handler/runtime tests at each sub-gate, including rejection plus unchanged-state assertions;
- exact Git tree/ancestry/fsck and durable projection/replay evidence;
- revision-bound HTML and pixel/frame inspection for every claimed visual state;
- a checked-in bounded runner that can resume at the first failing sub-gate;
- zero V1/V2 write events, records, references, or identity translation in the V3 journey.

Current state: **Gate 0A RED / UNVERIFIED. Gate 0B–0E BLOCKED by dependency.**

### Gate 1 — one ordinary Swarm-created Artifact V3

Prerequisite: Gate 0A green on the exact commit under test.

Objective: prove the smallest user-visible primary-Swarm path before any explicit iteration or Designer swarm behavior.

Required journey:

1. Start one fresh ordinary auto session and send one minimal request to primary Swarm for one static HTML artifact.
2. Do not invoke a Designer endpoint directly, upload fixture bytes, preseed an artifact, or name internal V3 IDs in the user prompt.
3. Permit only the canonical internal authoring path selected by Swarm; assert exactly one child author if delegation is the current product contract.
4. Assert exactly one native Artifact V3 repository, one root commit with no parent, a conventional project tree, a valid minimal manifest, matching build and preview evidence, and zero V1/V2 writes.
5. Inspect the rendered pixels for clipping, overflow, sizing, legibility, overlap, scrollbars, and prompt fidelity.
6. Reload Desktop and restart the daemon; rehydrate the same exact head and preview without another authority.

Acceptance evidence:

- bounded runner summary marked PASS;
- exact Git commit/tree/manifest and strict `git fsck` evidence without publishing private IDs;
- V3 mutation/replay/realtime records showing genesis only and no legacy write events;
- one inspected screenshot before restart and the exact same revision after restart;
- negative run proving author failure creates no partial ready artifact.

Current state: **GREEN on exact commit `46fa0d95` for the bounded Gate 1 journey.** Primary Swarm created one ready three-Part native V3 artifact; server build/preview and direct pixel inspection agreed on an exact 1440×900 non-overflowing page; Desktop showed the Artifact and all Parts; durable replay contained two native genesis events; the already-subscribed Desktop socket recorded two cursor-bearing native events; zero Designer children and no V1/V2 identity were present. Earlier RED runs and their repaired boundaries remain recorded in the Atlas revision ledger and test-audit reinventory notes. Restart/fsck and negative failure evidence remain wider Gate 0/2 obligations rather than claims of this bounded pass.

### Gate 2 — ordinary Swarm turn-based iteration and alternate choice

Gate 1 first passed on exact isolated-container commit `46fa0d95`: primary Swarm created one native Artifact with three stable selector Parts, valid 1440×900 pixels, visible Studio, durable replay, cursor-bearing realtime, zero Designer children, and zero legacy identity. The targeted-Part prerequisite then exposed a missing ordinary-Swarm authoring boundary: the Studio draft carried exact native identity, but only Designer task orchestration could consume it. Source now adds bounded authenticated `manage_artifact read_v3` and exact-base `revise_v3`; the latter preserves all stable Part IDs, publishes one complete build/preview-valid child candidate, and deliberately leaves head selection unchanged. Focused tests pass. The first exact targeted-Part run reached valid root pixels and zero Designers, then exposed a harness-only literal sibling-ID assertion; target identity now comes from the exact manifest label/ID. The next exact run durably created and pixel-inspected the no-Designer candidate, but the harness raced detail projection visibility immediately after terminal run intent; it now polls that canonical route for at most 30 seconds for `awaiting_selection`. A later exact run exposed a legitimate prior visual-repair turn, so the harness now records prior history and requires exactly one added targeted turn rather than two total. The next exact run reached valid root/candidate pixels, unchanged head, exact ancestry, and Studio candidate visibility; it exposed only a false harness expectation that the still-current root should show a historical warning after viewing an unselected candidate. The runner now asserts exact root preview identity instead. The next exact retry failed earlier because the provider exhausted its bounded root turn after two overflow attempts and produced zero Artifact records; the runner stopped correctly. One permitted same-source retry then reached every targeted product, pixel, ancestry, and Studio gate but its final replay assertion reused a bounded hydrate event slice. That check now uses canonical paginated replay and distinct root/candidate screenshot files. Exact isolated-container commit `28d0ef97` passed the no-Designer targeted-Part stage: primary Swarm read the exact root revision, created one build/preview-valid Pricing candidate, inspected its exact pixels, preserved all three Parts, recorded direct-child ancestry, left head unchanged, showed root/candidate/history in Studio, and delivered five durable plus five cursor-bearing realtime native events with no legacy identity. Gate 2 now advances to explicit selection, continued turns from the selected revision, and alternate whole-project choice. The runner's next bounded `selected-continuation` stage selects the exact candidate through Studio, then requires a new primary-Swarm Hero candidate to parent that selected commit and preserve the Pricing change; its first exact run stopped because implicit Part derivation accepted an extra semantic wrapper. Direct create now fails closed above three implicit Parts and tells primary Swarm to pass the exact three requested Parts; focused tests pass and exact execution remains pending.

Prerequisites: Gate 0A–0D and Gate 1 green on the same reviewed base.

Required journey:

1. Use Artifact Studio to target one Part on the exact current head and send the generated request through the normal primary-Swarm conversation.
2. Prove one complete child candidate is created without moving head, inspect base/candidate pixels, and select by exact CAS.
3. Continue another user-requested change from the selected candidate and prove the new turn uses that exact commit as base.
4. Request at least two alternate iterations from one base, compare complete candidates, choose either, retain the other, and continue again from the chosen branch.
5. Repeat with animated Parts and prove unrelated animations, old previews, durable replay, and native identity remain correct.

Acceptance evidence:

- normal Desktop message requests and native V3 turn/candidate/revision events;
- exact commit ancestry for targeted, continued, and alternate branches;
- base, candidate, alternate, selected, and prior-revision pixel/frame inspection;
- stale target/head/selection failures with unchanged Git, projection, and outbox;
- restart/reload/realtime proof and zero V1/V2 writes.

### Gate 3 — regular managed Designer creation and focused iteration

Prerequisite: Gates 0 through 2 green on the same reviewed base.

Required journey:

1. Create one artifact through the already-proven Gate 1 path.
2. Start one regular managed Designer follow-up from the exact current V3 head and target one manifest Part by stable ID.
3. Allow coherent shared-file changes, run repeated build/preview repair in the same turn, and finish one complete child commit.
4. Assert the candidate is a direct child of the exact base, the selected head remains unchanged before user selection, and the old revision remains previewable.
5. Select with exact expected head and turn revision; prove stale selection and unknown target rejection leave Git, projection, and outbox unchanged.
6. Inspect base, candidate, and selected pixels and verify unrelated required behavior remains correct.

Acceptance evidence:

- one initial and one follow-up Designer child only;
- durable turn/candidate/revision evidence and exact ancestry;
- before/after Desktop Studio screenshots and prior-revision preview;
- target-ID authority proof with no caller-reconstructed locator requirement;
- restart/replay and no-legacy-write evidence.

### Gate 4 — Designer Iteration Swarm sibling candidates

Prerequisite: Gates 0 through 3 green.

Required journey:

1. From one exact selected V3 head, request a bounded Designer Iteration Swarm.
2. Allocate one Artifact and one iteration turn with the requested number of complete sibling candidates, all sharing the exact same base commit.
3. Preserve every ready candidate and bounded diagnostics for failed slots without moving head or fabricating legacy placeholders.
4. Compare complete previews in Artifact Studio, select one exact candidate by CAS, and retain prior and unselected revisions according to policy.
5. Repeat for a targeted Part set and prove regular/swarm target semantics are identical.

Acceptance evidence:

- exact candidate cardinality and sibling ancestry;
- independent ready build/preview evidence and pixel inspection for every claimed candidate;
- partial-wave failure test with unchanged head;
- stale/foreign selection negative tests;
- reload/realtime proof and zero V1/V2 writes.

### Gate 5 — Designer-backed storyboard, animation, Video Studio, and MP4 confirmation

Prerequisite: Gate 0E and Gates 1 through 4 green. The native adapters are implemented and deterministically proven before Designers enter this path; this gate confirms Designers can author valid inputs and traverse those same server-owned boundaries. Implement and test it in bounded sub-gates.

#### Gate 5A — Artifact V3 storyboard confirmation

Define storyboard metadata as stable manifest/project data or a V3-owned typed projection bound to one exact commit. Produce ordered capture states and exact PNG stills whose lineage names the V3 artifact, commit, tree, manifest, validation, and state. Reject stale, foreign, incomplete, or duplicate states without proposal mutation.

#### Gate 5B — Artifact V3 HTML animation confirmation

Bind a reviewed animation profile and duration to the exact V3 revision and build. Prove deterministic seek, live playback, lifecycle cleanup, representative frames, and a render-ready fallback. Failed export must not invalidate or move the source head.

#### Gate 5C — Video Studio proposal confirmation

Add one server-owned conversion action accepting only an exact published/selected V3 head and exact video-project base. Assemble storyboard parts or animation candidates server-side. The pending proposal must retain native V3 references; no V1 collection/variant or V2 published-head impersonation is allowed. AI cannot accept the proposal.

#### Gate 5D — exact-lineage MP4 derivative and final render

After explicit candidate selection, export/promote the selected V3 animation to MP4 with source commit/profile/duration lineage. Prove stale selection, mismatched profile/duration, missing fallback, failed export, and render cancellation cannot partially mutate the accepted cut or source artifact.

Acceptance evidence for Gate 5:

- focused deterministic negative tests at each adapter boundary;
- one pending storyboard proposal and one pending animation proposal built from exact V3 heads;
- inspected still/fallback/animation frames;
- one selected exact-lineage MP4 derivative and final Video Studio materialization;
- user acceptance remains an external explicit action;
- no V1/V2 write records or identity translation in the V3 journey.

## Likely attack and failure points

- Confusing generic `collection_id`/`variant_id` fields with native Artifact V3 Artifact/commit identity.
- Letting parent or child model-authored locator data override the exact head manifest.
- Reusing an expired, foreign, or wrong-revision preview capability for CSS/JavaScript assets.
- Treating Git refs, directory enumeration, or a `ready` label as authority when Pebble projection/evidence is missing or stale.
- Moving head before candidate validation or user selection, or replaying a stale selection after another head advance.
- Losing a repairable turn because grants/build/preview state is memory-only across daemon restart.
- Creating one Artifact per swarm worker instead of sibling candidate commits from one exact base.
- Dropping successful candidates when another swarm slot fails.
- Allowing Desktop V1/V2 fallback state to hide a missing V3 catalog/realtime update.
- Translating a V3 revision into V1/V2 identity to reuse storyboard, fallback, proposal, derivative, or render code.
- Reusing validation, still, fallback, or MP4 evidence after source bytes, tree, profile, duration, or policy change.
- Claiming visual success from renderer readiness or hashes without inspecting every rendered state covered by the claim.

## Preserved dirty worktree state

At audit capture, tracked uncommitted changes were present in:

- `docs/swarm-atlas.md`
- `docs/testing/test-audit-ledger.tsv`
- `swarmd/internal/run/artifact_v3_designer_test.go`
- `swarmd/internal/run/service_task_launch.go`
- `swarmd/internal/runtime/artifact_v3_runtime.go`
- `swarmd/internal/runtime/artifact_v3_runtime_test.go`

This audit document is an additional tracked file. The existing implementation diff changes V3 target-hint and exact-manifest validation behavior and updates its tests/ledger/atlas note. It is preserved for later review; it is not validated or accepted by this audit. Ignored `.tmp/artifact-v3-*` evidence is also preserved. No cleanup, commit, additional provider journey, or implementation was performed while producing this report.

## Relevant filepaths

- `docs/checkpoints/artifact-v3-architecture.md`
- `docs/checkpoints/artifact-v3-restoration-gap-audit.md`
- `scripts/runners/artifact-v3-multipart-e2e.mjs`
- `swarmd/internal/tool/runtime_artifact_v3_author.go`
- `swarmd/internal/run/artifact_v3_designer.go`
- `swarmd/internal/run/service_task_launch.go`
- `swarmd/internal/run/service_task_swarm.go`
- `swarmd/internal/run/service_tools.go`
- `swarmd/internal/run/provider_tool_invoker.go`
- `swarmd/internal/runtime/artifact_v3_runtime.go`
- `swarmd/internal/store/pebble/artifact_git_v3_repository.go`
- `swarmd/internal/store/pebble/artifact_v3_service.go`
- `swarmd/internal/store/pebble/artifact_v3_projection_store.go`
- `swarmd/internal/api/sessions_v3_artifact_v3.go`
- `web/src/features/desktop/session-v3/artifact-v3-api.ts`
- `web/src/features/desktop/chat/components/desktop-v3-artifact-v3-studio.tsx`
- `swarmd/internal/artifactv2/storyboard.go`
- `swarmd/internal/artifactv2/storyboard_service.go`
- `swarmd/internal/artifactv2/video_conversion.go`
- `swarmd/internal/tool/runtime_manage_video_storyboard.go`
- `swarmd/internal/tool/runtime_manage_video.go`
- `swarmd/internal/tool/runtime_manage_artifact_animation.go`
- `docs/testing/test-audit-ledger.tsv`
- `docs/swarm-atlas.md`

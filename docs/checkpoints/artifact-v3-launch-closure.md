# Artifact V3 launch closure

Assessment date: 2026-09-05 UTC. Source reviewed: `d2ee6d63` (clean isolated implementation checkout before this document).

## Verdict

**NOT READY for the complete video-making feature.** Recorded individual HTML and revision journeys are useful evidence, not a passing release journey. L1 now verifies native conversion and decoded beginning/middle/end frames; pixel inspection found layout defects, so visual quality and the complete journey remain RED. Earlier test/pixel claims remain inherited records, not fresh verification.

The required outcome is: a user creates an artifact with animated HTML Parts, revises it turn by turn, compares and explicitly selects alternatives (including Designer swarm candidates), combines selected animation with stills and recorded video in Video Studio, and explicitly accepts and renders a usable final video. Earlier revisions remain usable after later edits and restart.

This document is the current closure checklist. The restoration audit remains historical evidence; its conflicting opening status and older inventory must not be treated as current acceptance. The structured approved session plan owns execution. Reconcile the historical audit and Atlas during closure rather than accumulating contradictory status notes.

## What is known, and what is not

| Capability | Current evidence | Release status |
| --- | --- | --- |
| Primary Swarm creates HTML with three visible Parts | Fresh L2 journey on runtime `3b05ea65`: stable animated Hero/Pricing/Footer, inspected Studio and native events | L2 passed; repeat on release candidate |
| Targeted revision, selection, continued turn | Fresh L2 Pricing selection, Hero continuation and responsive continuation; exact ancestry, unchanged head before selection, inspected pixels | L2 passed; repeat on release candidate |
| Alternate candidates | Fresh L2 two-sibling compare/select/continue journey; both alternatives retained, Option Two explicitly selected | L2 passed; Designer alternatives remain unproven |
| CSS/WAAPI animation in Parts | Fresh L2 revision journey retains all three running animations; paired repair frames and narrow Desktop pixels inspected | L2 preservation passed; not arbitrary animation or repaired final export |
| HTML-to-MP4 | Native four-second conversion and three decoded 1920×1080 frames verified; motion retained, source unchanged | Encoding proved; visual layout RED |
| Video Studio native V3 display | Native authenticated media/canvas mapping implemented; retained and fresh pending proposals decode and play on `bc6a1f27`, with inspected Studio pixels | L3 native playback passed; final-video quality remains separate |
| Multiple storyboard sections / exact state stills | Ordered temporal entry/state contract, bounded validation, pending acceptance rejection and stable replacement pass focused real-store/renderer-boundary tests | Source contract implemented; provider-backed multi-shot filming journey not claimed |
| Regular Designer creation and iteration | Earlier roots and failures; no current complete passing journey supplied | Unproven |
| Designer swarm sibling candidates | Code path exists; cardinality, common base, failure retention and subsequent turn unproven | Unproven |
| Animated HTML + still + recorded video final cut | No accepted-cut/final-playback evidence supplied | Unproven; required |
| Restart, stale references, partial failure, installed-runtime parity | Focused tests and partial history are not a complete candidate-bound proof | Required before release |

## Source-confirmed gaps and concrete next actions

1. **Encoder prerequisite.** `htmlcapture.animationEncoderArgs` requires PNG image2pipe, libx264, yuv420p and MP4 faststart. The last recorded isolated runtime used a minimal Playwright encoder. Provision a verified, checksum-pinned compatible encoder through the existing fixed testbench contract, preserving root ownership, read-only mounts, process bounds and same-lane private state. Check the actual codec/container pipeline before another provider-backed conversion. Separately establish how the shipped application obtains the same capability; a testbench-only mount is not product readiness. Privileged helper installation retains its separate permission boundary.
2. **Frontend consumer.** `video-studio-surface.tsx` derives visuals and candidate readiness from legacy `visual`/`selected_source` collection/variant fields. A bounded search of frontend source found no consumers for the backend `artifact_v3_source`, `artifact_v3_visual`, or `artifact_v3_selected_source` fields. Trace the API-to-player path, add native typed references and authenticated media delivery as needed, and prove a real pending V3 proposal displays and plays. Never disguise V3 identity as V1/V2.
3. **Revision lifetime.** `artifactv3video.Service.read` authenticates derivatives using `ReadSelectedHead`; that runtime method rejects commits that are no longer the current head. Test the user journey of accepting/exporting revision A, selecting revision B, then reopening/rendering A. Keep current-head checks for new conversion/selection where appropriate, but immutable owned derivative reads must not depend on a mutable current-head pointer. Preserve authorization and digest checks.
4. **Storyboard/Part semantics.** `assemble` returns exactly one ready animation part with generic filming requirements. The runtime render request does not consume `PartID` or `CaptureStateID`. Implement and prove explicit ordered temporal sections/state renders; distinguish spatial HTML Parts from timeline sections. Do not imply each DOM Part automatically becomes a shot. Preserve stable section identity, exact source lineage, pending filming requirements and replacement semantics.
5. **Proof is too shallow.** The current `video-conversion` runner requires one pending part and verifies reference metadata plus PNG/MP4 prefixes and sizes. It does not prove decoded MP4 frames, visible Video Studio playback, acceptance, mixed-media composition or the final exported cut. Extend the maintained runner in bounded stages; headers alone cannot pass visual/video acceptance.
6. **Designer contract parity.** Audit the actual model-visible tool schema, injected grants, regular/swarm parsing, source targets and returned native references. Primary success is not Designer success. Prove one regular author plus follow-up before a small two-candidate swarm. Preserve all successful candidates when another slot fails; selection must be explicit and later turns must use the selected commit.
7. **Evidence reconciliation.** Inspect actual test assertions and retained bounded reports before preserving any GREEN label. Reconcile new/changed test inventory with the audit ledger; no automatic promotion into curated security suites. Remove stale current-status prose from the restoration audit and update affected Atlas authorities and gaps.

## Ordered execution gates

Each gate reports source commit, runtime/model identity, observable result, inspected outputs, exact failure and next action. No downstream PASS waives a failed prerequisite.

### L1 — Compatible runtime and one actual encoded clip
- Verify safe encoder provisioning and a real deterministic PNG-to-H.264/MP4 smoke pipeline inside the isolated runtime.
- Preserve same-lane state; do not require repeated login or reset onboarding.
- Run one fresh native conversion after the material fix; decode and inspect beginning/middle/end frames and duration, not only a container prefix.
- Gate passes only with valid output and unchanged source. This is not whole-feature completion.

**2026-09-05 recovery — encoder and native decoded-clip proof complete; visual layout remains RED.** Same-lane rebuilds preserved source and account state. The resumed account-backed Luna inference reached native conversion on `0ac9296f`, but deterministic preflight rejected initial pixels despite paused CSS timelines. No source artifact or onboarding reset was performed.

The approved checksum-pinned compatible encoder and matching root-owned helper are installed. The installer proved PNG image2pipe → libx264/yuv420p/MP4 faststart in a bounded private namespace: 24 decoded 64×64 frames over one second, with changing frame content. An initial execution-filesystem denial stopped safely before installation; using suitable private scratch resolved it without weakening mount protections. Installation did not restart or reset the lane; subsequent persistent rebuilds activated the compatible runtime.

The earlier eleven focused helper contract tests cover rejection of missing, symlinked, wrongly owned and modified encoders; they were not rerun during installation. Fixed-broker host checks passed afterward and arbitrary sudo remains denied. Operational commands, authentication and permission evidence remain outside public source. Subsequent native conversion and decoded inspection are recorded below; no overall visual acceptance is claimed.

Subsequent same-lane persistent rebuilds preserved authenticated account and source state. The exact `animationEncoderArgs` pipeline also passed as the application UID inside the running isolated runtime. Native conversion then exposed two distinct renderer defects: compositor-lagged CSS frames and an omitted audited fallback PNG. Corrections `e2ad54ae` and `0ac9296f` retain strict pixel stability and return the audited frame-zero PNG with MP4. The focused real-browser CSS stability/negative-mutation and encode/fallback pixel-equivalence tests passed twice; Atlas synchronization passed.

The earlier HTTP 429 capacity failure is superseded by successful Luna inference after user-managed account recovery. A local exact-source comparison reproduced the remaining first-paint race with identical paused times/computed transforms; awaiting animation readiness and two paint callbacks stabilized samples. The renderer now crosses that bounded presentation boundary before its first screenshot, retaining the independent exact-pixel audit. Three focused multi-animation/backwards-seek/negative-mutation repetitions passed. Prior settings were restored; no proposal was accepted or final render initiated. Native conversion on `ab3999c7` produced a four-second silent MP4 and fallback PNG. Inspection on `3b05ea65` reused those exact bytes after persistent rebuild; the native candidate consumer now uses canonical V3 plan validation rather than legacy source identity. The maintained runner passed native timing/reference checks, unchanged selected head/Part/turn/replay, no delegation and model restoration. `inspect_frames` decoded 1920×1080 PNGs at 0, 2000 and 3966 ms; every frame was pixel-inspected. Orb, pulse and footer motion are visible, but the hero buttons/proof overlap the pricing heading and the footer line crosses card buttons; the pulse is positioned at the viewport edge. Text outside those overlaps is legible, and no browser chrome or scrollbar appears. **Visual quality remains RED.** The source was deliberately not revised during this exact-source encoding proof; L2 must correct and inspect responsive layout before preserving any visual PASS. A restart-interrupted diagnostic required canonical run cancellation; this is not a general restart/recovery pass. **Shipped-runtime parity remains RED:** `NewChromedpRendererWithConcurrency` resolves `ffmpeg` from PATH and animation capture validates it as a system executable; the inspected launcher and distribution builder do not establish compatible encoder provisioning. A repaired testbench alone cannot close that release requirement.

### L2 — Confirm the artifact foundation without Designers
- Fresh primary request creates one artifact with three animated stable Parts.
- Target one Part; compare old/candidate pixels; confirm head unchanged until explicit selection.
- Continue from selection, request two sibling alternatives, select one and continue again.
- Verify other Parts retain requested behavior; inspect every claimed preview/frame; reload and replay exact identity.
- Report provider/handoff errors separately from artifact state: a valid candidate does not excuse a broken user journey.

**2026-09-05 L2 evidence:** A fresh primary-Swarm/Luna journey on runtime `3b05ea65` created one animated artifact, revised Pricing, explicitly selected it in Desktop, continued Hero from that exact commit, created two direct sibling alternatives, selected the reviewed Option Two and continued from it. Stable `hero`/`pricing`/`footer` IDs, native ancestry and replay were retained. Twenty-five native events cover the final history; creation and mutation stages observed cursor-bearing realtime, while read-only inspection used reconnect replay completion. Both siblings and the original preview remain usable. No Designers were launched.

The last continuation clipped Footer in the narrower Desktop pane and was left unselected. A separate exact-base responsive repair retained all three animations and passes 840×844 geometry checks (Footer ends at 830px) as well as the existing 1440×900 runner. **L2 visual review completed in fresh media context:** exactly the two retained repair frames and actual Desktop Studio capture were pixel-inspected. Hero, Pricing and Footer fit; headings, prices, buttons and continuation labels are legible; the footer marker is separated from text; no content clipping, scrollbar or obstructing capture overlay is visible. The paired frames show changed Hero orb position/rotation, Pricing pulse width/brightness and Footer marker rotation/color, agreeing with advancing browser timelines. This closes the interrupted review for this exact repair and these sampled viewports, not arbitrary viewport or final-video acceptance. Option Two remains selected; both continuation candidates are preserved and unselected.

The maintained runner now supports bounded animated creation/targeting/selection/sibling/continuation/inspection stages with revision-keyed screenshots. Evidence fixes open collapsed historical turns, wait for preview replacement, distinguish reconnect from mutation events, and resolve sibling labels independently of response order. New-turn selection uses a before/after turn-ID set; explicit inspection and head selection require an exact reviewed reference. Provider authoring succeeded; intermediate runner failures (including the retained continuation RED) and the parent media-input interruption remain separate from durable artifact results. Fresh-context inspection resolved that interruption without new provider authoring, candidate regeneration, selection, rebuild or reset. Retained exact-reference inspection proves 25 native replay events, cursor-bearing reconnect completion and no legacy identity; the finish stage verifies reload retains all three Parts and the selected head. The corrected generating stages have not been replayed end to end, and independent test P1/P2 review remains pending. L2 is complete as a recovered user journey, not a clean repeated release-gate pass.

A separate operational continuity gap surfaced: persistent rebuild retains encrypted account state but drops the device label. Restoring only the existing self-runtime label through the canonical API restored Desktop without OAuth, identity, workspace or completion-state reset. Future rebuilds require the owning helper repair; current readiness is not proof of permanent configuration continuity.

### L3 — Complete native video integration
- Fix revision lifetime and native frontend media handling with focused negative tests.
- Prove ordered storyboard/state stills and animation are bound to exact V3 revisions, with no silently ignored target/state inputs.
- Show actual pending proposal media in Video Studio, correct sizing/timing and stable section replacement.
- Verify stale/foreign requests, failed render, cancellation and proposal failure do not mutate selected head or accepted cut. Check derivative/proposal publication ordering rather than assuming whole-operation atomicity.

### L4 — Designers on the proven foundation
- Regular managed Designer creates a valid multipart artifact and completes one targeted revision with inspected pixels.
- Two Designer swarm candidates share one artifact/base and remain individually previewable; explicit selection and subsequent turn work.
- Negative/partial-wave proof preserves good candidates without moving head or inventing success.
- A Designer-authored animated revision traverses the same V3 video boundary. No legacy fallback.

### L5 — The actual mixed-media video journey
- Use at least one HTML-animated section, one still and one registered recorded-video clip in one ordered project.
- Edit one animated section after conversion; retain the prior accepted media until the user accepts its replacement. Reopen the prior source and cut successfully.
- User explicitly accepts the proposal and starts final render through the product UI; AI must not self-accept or start final rendering.
- Inspect decoded frames for each section and every transition boundary, plus duration, dimensions, clipping, legibility, media timing and intended audio/mute behavior. Verify downloadable output and playback, not just a successful job label.

### L6 — Freeze and release decision
- Freeze a clean candidate. Run the complete bounded journey twice on that same source and runtime, including one prescribed restart/reconnect recovery.
- Verify shipped-runtime encoder/browser availability, not only the testbench helper, and run required focused/security/build/publication gates for the intended release operation.
- Any correction changes the candidate and invalidates affected evidence. No evidence borrowing from an older head.
- Reconcile this checklist, historical audit, Atlas and test ledger. Report GO only when every required gate passes; otherwise list the exact remaining RED items.
- Integration, push, PR and release remain separately authorized operations. A testbench GO is not a release or a promotion to the main checkout.

## Today / tomorrow window

Use UTC to avoid an assumed local timezone. These are decision targets, not guaranteed delivery times.

- **September 5, 06:00:** target L1 encoder/decoded-clip result and a fresh L2 foundation status. If provisioning lacks an approved installation path, identify it immediately rather than consuming provider retries.
- **September 5, 12:00:** target L2 complete and L3 native UI/revision/storyboard gap assessment with implemented fixes where bounded. Reforecast if material architecture work remains.
- **September 5, 18:00:** first full-scope GO/NO-GO decision. Same-day release is possible only if L1–L6 and required release gates have genuinely passed. Otherwise do not advertise the full feature as ready.
- **September 6, 12:00:** target remaining Designer/mixed-media fixes complete and candidate freeze.
- **September 6, 18:00:** second GO/NO-GO decision after repeated exact-candidate proof. Still RED means defer this feature, not weaken its checks or silently drop Designers/mixed media.

The credible planning target is **tomorrow**, with a same-day result as an upside. Multiple known implementation gaps make a same-day commitment unjustified. Deadlines may slip if permissions or substantial defects remain; report that at the decision point. Scope reduction is a user decision, not an AI shortcut.

## Execution discipline

- Fix the first concrete failure, run its narrow proof, then advance. No equivalent retry without new evidence; at most two materially distinct safe recovery approaches per genuine blocker.
- Live stages are at most ten minutes with progress at least every fifteen seconds. Preserve resumable state and bounded private evidence outside tracked source.
- No status from filenames, readiness flags, hashes or header checks alone. Use observable postconditions, real rendered pixels and exact revision lineage.
- No unrelated cleanup, model churn, onboarding resets, custom preview UI, expanded privileges or host-production test deployments.
- Routine implementation gaps remain work, not excuses to stop. Required unavailable permissions/inputs are named explicitly.

## Relevant filepaths

- `scripts/runners/artifact-v3-multipart-e2e.mjs`
- `swarmd/internal/htmlcapture/animation.go`
- `swarmd/internal/artifactv3video/service.go`
- `swarmd/internal/runtime/artifact_v3_runtime.go`
- `swarmd/internal/tool/runtime_manage_video.go`
- `swarmd/internal/run/artifact_v3_designer.go`
- `swarmd/internal/run/service_task_launch.go`
- `swarmd/internal/run/service_task_swarm.go`
- `swarmd/internal/store/pebble/artifact_v3_service.go`
- `swarmd/internal/store/pebble/session_video_project_store.go`
- `swarmd/internal/videoproject/service.go`
- `swarmd/internal/videorender/service.go`
- `web/src/features/desktop/chat/components/desktop-v3-artifact-v3-studio.tsx`
- `web/src/features/desktop/tools/video-studio/video-studio-surface.tsx`
- `docs/checkpoints/artifact-v3-restoration-gap-audit.md`
- `docs/swarm-atlas.md`
- `docs/testing/test-audit-ledger.tsv`

The fixed testbench provisioning helper belongs to its separate operational workspace; its private deployment procedure and evidence must not be copied into this public source document.

### L3 source closure in progress

Native media GET/HEAD/range delivery and Desktop MP4 canvas routing are implemented without legacy identity aliases. Reads authenticate immutable source evidence plus exact persisted derivative receipts; source selections/conversions still require current head. An explicit startup migration imports receipts only from durable video revision references and verified pre-receipt bytes, preserving historical media without rewriting source or video identities.

Ordered temporal sections use `swarm-storyboard.json` with exact state HTML entries, stable section IDs, bounded duration and filming requirements. Spatial Part requests and undeclared states reject before rendering. Pending filming blocks acceptance, and ready revisions replace the same shot while retaining other sections and prior stills. Complete derivative sets publish before proposal CAS; a later cancellation may retain private derivatives but cannot create/accept a proposal or alter the confirmed cut.

Focused native backend tests passed twice, including real Git/Pebble restart, receipt migration, exact state routing, malformed unselected states, partial-render failure, pending replacement and post-publication cancellation. TypeScript and 62 focused frontend tests passed; the required fast pre-build security gate passed. The maintained runner now checks exact PNG/MP4 digests, three decoded frames and advancing real Video Studio canvas. Deployment and live pixel review of this source remain pending; L3 and overall launch are not complete.

The first L3 deployed playback trial decoded the retained four-second MP4 at three timestamps and preserved the old pending project through receipt migration. It correctly failed because the Studio canvas stayed black after media loaded behind its loading branch. Ordinary Play painted immediately, isolating a missing mount-time redraw rather than corrupt media or auth failure. A callback-ref redraw and non-overlapping player badges are the focused correction; its fresh live proof is pending. The initial runner invocation also failed at model preflight before provider work because the no-Designer model override was omitted; corrected invocation retains that RED evidence separately.

The mount-only correction did not close the initial-paint failure. A bounded browser trace showed `drawImage` running at readyState 4 with correct dimensions/alpha but zero lit pixels; a one-shot `requestVideoFrameCallback` then delivered the actual decoded frame without Play. The correction now redraws on first frame presentation and cancels its callback on replacement/unmount, rather than polling or treating loaded-data as presentation evidence. Prior RED runs remain retained.

On `fdcf1672`, the retained native proposal and a fresh Codex conversion of the already-selected L2 Option Two both passed the maintained browser-decode and actual Studio-playback gates. The fresh 1920×1080 four-second media was inspected at 0/2000/3966ms: main text and pricing fit with preserved motion and selected-turn labels. The retained L1 media still has its documented overlaps; Option Two's footer marker crowds text in some samples. These source-quality limits are not player/identity failures and do not constitute final-video approval. Pixel review also found the redundant pending badge covered the small Studio preview; it was removed while retaining the separate confirmation warning below the player. No source selection, proposal acceptance or final render occurred.

**L3 scoped result:** final resumed native journey passed on `bc6a1f27` after the same-lane rebuild. Exact pending proposal, PNG/MP4 digests, three browser-decoded motion samples, first-frame painting, advancing Studio canvas, unchanged source replay and confirmed base, no delegation/legacy identity, and settings restoration were verified. Final Studio overview plus before/playing canvas captures were pixel-inspected: native content is visible before Play, advances during playback, and the redundant pending overlay no longer covers the headline; the explicit confirmation warning remains outside the player. At the narrow approximately 440px player, fine body/footer text is necessarily small, so full-resolution decoded inspection remains required. The three final decoded images have the same digests as the already inspected fresh conversion samples. L3 integration is complete at this scope, not final-video or launch approval. Multi-shot source/state and pending-replacement behavior has deterministic renderer-boundary/Pebble proof, not a claimed provider-backed filming journey. Designer, mixed-media user acceptance/final render, shipped runtime parity and twice-on-one-candidate release proof remain open.

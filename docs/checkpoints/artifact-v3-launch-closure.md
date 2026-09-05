# Artifact V3 launch closure

Assessment date: 2026-09-05 UTC. Source reviewed: `d2ee6d63` (clean isolated implementation checkout before this document).

## Verdict

**NOT READY for the complete video-making feature.** Recorded individual HTML and revision journeys are useful evidence, not a passing release journey. The L1 inspection below verifies encoder installation and an isolated codec smoke test; native conversion and visual acceptance remain pending. Earlier test/pixel claims remain inherited records, not fresh verification.

The required outcome is: a user creates an artifact with animated HTML Parts, revises it turn by turn, compares and explicitly selects alternatives (including Designer swarm candidates), combines selected animation with stills and recorded video in Video Studio, and explicitly accepts and renders a usable final video. Earlier revisions remain usable after later edits and restart.

This document is the current closure checklist. The restoration audit remains historical evidence; its conflicting opening status and older inventory must not be treated as current acceptance. The structured approved session plan owns execution. Reconcile the historical audit and Atlas during closure rather than accumulating contradictory status notes.

## What is known, and what is not

| Capability | Current evidence | Release status |
| --- | --- | --- |
| Primary Swarm creates HTML with three visible Parts | Prior recorded live passes on earlier commits, including Studio, pixels and native events | Reconfirm on release candidate |
| Targeted revision, selection, continued turn | Prior recorded individual passes; exact-base candidate and unchanged-head checks exist | Reconfirm together, including animated content |
| Alternate candidates | Atomic alternatives implementation and recorded candidate creation; historical narrative mixes RED attempts with broader GREEN claims | Require explicit fresh compare/select/continue proof |
| CSS/WAAPI animation in Parts | Recorded three-Part motion and two captured frames | Does not prove arbitrary animation, animated revision preservation, or final export |
| HTML-to-MP4 | Native backend implementation; last live attempt failed `animation_encode_failed` | RED |
| Video Studio native V3 display | Backend V3 references exist; inspected frontend visual/candidate code expects collection/variant references | Integration gap |
| Multiple storyboard sections / exact state stills | Conversion currently assembles one ready animation section | Not implemented as a demonstrated multi-section journey |
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

**2026-09-05 recovery — encoder installed; persistent rebuild and native proof pending.** The existing persistent lane is active on candidate `707107142b31ed50005f353bbc5ab5f1f860651f`; the source checkout is `d2ee6d63236e0ee9b7134929edcc93fdc4d72e78`. Redacted provider discovery reports Codex runnable and the previously used Luna model selectable. Existing account model assignments were not changed. No login, onboarding, source session, artifact, or runtime reset was performed.

The approved checksum-pinned compatible encoder and matching root-owned helper are installed. The installer proved PNG image2pipe → libx264/yuv420p/MP4 faststart in a bounded private namespace: 24 decoded 64×64 frames over one second, with changing frame content. An initial execution-filesystem denial stopped safely before installation; using suitable private scratch resolved it without weakening mount protections. The running lane was not restarted or reset by installation and still needs its persistent runtime rebuild.

The earlier eleven focused helper contract tests cover rejection of missing, symlinked, wrongly owned and modified encoders; they were not rerun during installation. Fixed-broker host checks passed afterward and arbitrary sudo remains denied. Operational commands, authentication and permission evidence remain outside public source. No native conversion or decoded-frame pixel acceptance is claimed yet.

After installation, rebuild this same lane in persistent mode, compare candidate identity and redacted provider readiness, run the exact `animationEncoderArgs` PNG image2pipe pipeline inside the isolated runtime, then perform one fresh exact-source conversion and inspect decoded beginning/middle/end frames. **Shipped-runtime parity remains RED:** `NewChromedpRendererWithConcurrency` resolves `ffmpeg` from PATH and animation capture validates it as a system executable; the inspected launcher and distribution builder do not establish compatible encoder provisioning. A repaired testbench alone cannot close that release requirement.

### L2 — Confirm the artifact foundation without Designers
- Fresh primary request creates one artifact with three animated stable Parts.
- Target one Part; compare old/candidate pixels; confirm head unchanged until explicit selection.
- Continue from selection, request two sibling alternatives, select one and continue again.
- Verify other Parts retain requested behavior; inspect every claimed preview/frame; reload and replay exact identity.
- Report provider/handoff errors separately from artifact state: a valid candidate does not excuse a broken user journey.

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

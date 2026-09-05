# Historical V1 Audit — Superseded by Artifact V2

Date: 2026-08-31

## Executive verdict

The pipeline is not failing for one reason. It currently combines four different failure classes behind one model-authored `manage_artifact create` call:

1. **A confirmed backend/tool-contract defect** rejects `animation_profile` when a managed Designer echoes the exact profile that the same tool schema and animation guidance expose.
2. **A brittle authoring protocol** asks a language model to produce a complete HTML application, two manifests, a parser-time runtime binder, deterministic seeking, an independent live scheduler, a player bridge, exact temporal parts, fixed-viewport layout, and visual-quality compliance in one irreversible publication call.
3. **Lossy validation and recovery** reduce structured renderer diagnostics to broad error strings and allow at most one model re-authoring attempt, without a reusable validated source or a draft-edit loop.
4. **A multi-authority handoff** requires compatible ready HTML candidates, inspection images, a fallback, a pending Video Studio proposal, optional MP4 export, derivative promotion, and later user acceptance. A failure in any earlier authority prevents the video handoff.

The immediate `animation_profile` failure is **not an HTML or renderer failure**. It occurs before profile injection, parsing, reservation, preflight, or publication. It is a contextual API contradiction. The system tells the model that the profile exists, exposes the field in the generic tool schema, and also tells it to omit the field; runtime then rejects mere field presence even when it matches the trusted profile exactly.

This should be treated as a **launch blocker in the managed animated-HTML write path**, but not as evidence that deterministic HTML capture or Video Studio rendering must be discarded. Those lower layers have working, focused tests and successful live candidates. The broken boundary is the model-facing authoring/publication protocol and its recovery semantics.

## Exact `animation_profile` rejection

### Authority flow

1. The parent chooses `animation_profile` on a Designer task launch. `service_task_launch.go` parses it through the closed animation-profile registry.
2. `service_tools.go` creates the parent-owned collection and staging placeholder with immutable `OutputRequirements` and `AnimationProfile`.
3. `delegatedSubagentRunStartMeta` copies both snapshots into the child `ArtifactRunContext`; `providerManagedArtifactRunContext` carries that context into tool execution.
4. The Designer prompt explicitly says that the backend supplied an immutable animation profile and that managed publication must omit `animation_profile` because orchestration injects it (`service_tools.go:5766-5811`; swarm specialization repeats this in `service_task_swarm.go:530-548`).
5. The child still receives the generic `manage_artifact` definition. Its schema exposes `animation_profile` as an optional property and describes it as valid for `create` and `create_package` (`runtime_manage_artifact.go:239-300`). Common animation guidance also says declared animation HTML must carry the reviewed profile (`designer_animation_guidance.go:13-24`).
6. During managed `create`, runtime checks only whether the argument key was supplied. If present, it returns `manage_artifact managed create must omit animation_profile; trusted orchestration injects the immutable target` (`runtime_manage_artifact.go:453-460`). This happens before `parseArtifactCreate`, trusted profile injection at line 497, media validation, durable reservation, or renderer preflight.
7. The error has no `animation_*` failure code. `managedDesignerRefinementCandidate` therefore cannot classify it for the one bounded correction round, and `designerToolFailureState` terminates the child immediately (`service.go:2978-3053`).

### Classification

**Implementation defect / contradictory contract.** The model can be perfectly correct about the selected profile and still fail because the contextual managed-call contract is represented only in prose, while the executable schema remains generic. The runtime rejects an equivalent redundant value instead of ignoring it or comparing it with the trusted snapshot.

### Why prior fixes did not address it

Recent changes repaired other layers: workspace routing, partial-wave aggregation, renderer concurrency, start-frame quality, parser-time binding guidance, model reasoning duration, and export-profile guidance. None changed the managed-create argument gate or supplied a context-specific tool schema. More prompt text cannot make a contradictory executable contract reliable.

## Confirmed root-cause map

| Layer | Confirmed behavior | Class | Why it breaks end to end |
| --- | --- | --- | --- |
| Parent task contract | Parent correctly resolves and snapshots `animation_profile` and output requirements. | Working authority | The child sees those values in both context and generic tool schema, creating duplicate ownership. |
| Managed tool schema | `animation_profile`, destination metadata, requirements, and many unrelated artifact operations remain visible in one broad schema. | Implementation/design defect | The schema does not encode the contextual managed-Designer call shape. Prose is asked to override executable affordances. |
| Managed create gate | Any supplied `animation_profile` key is rejected before trusted injection, even if exact. | Critical implementation defect | Produces the reported immediate failure and bypasses authored correction. |
| HTML authoring | Live evidence includes missing animation manifests, invalid parser-time scripts, a bound runtime whose `ready` method remained a placeholder, fixed-viewport overflow, clipped exit text, blank/weak opening frames, and timing-edge invisibility. | Model-authoring failures | The one-shot brief is too large for reliable freeform generation; defects vary every run. |
| Runtime contract | Current prompt requires a parser-time `__SWARM_ANIMATION_BIND__`, final `ready/seek/stop` functions on the first bound object, deterministic seek, and a separate live RAF scheduler. | Valid but over-complex authoring contract | The model must implement host integration, deterministic rendering, and live playback simultaneously. |
| Checked-in docs | `docs/checkpoints/html-video-animation-capture-contract.md:19-38` and `video-maker-html-animation-audio-gap-report.md:27-37` still describe direct `__SWARM_ANIMATION_V1__` assignment and omit the current binder/stop/live-scheduler contract. | Confirmed contradictory documentation | There are multiple written definitions of “correct” HTML. Prompt hardening has outpaced canonical docs. |
| Renderer preflight | Trusted Chrome correctly rejects missing binding, readiness, seek, instability, overflow, network, blocking UI, and invalid frames. | Working fail-closed boundary | It is being used as the first meaningful compiler/test pass after an irreversible model publication attempt. |
| Diagnostic propagation | `htmlcapture` produces bounded lifecycle/diagnostic outcomes, but `normalizeAnimationRendererError` maps them to a small safe message set. | Implementation limitation | The model often receives “animation did not become ready” rather than the specific safe outcome needed for a precise repair. |
| Correction loop | One fresh destination may be allocated for selected `animation_*` errors, with a prose request to preserve valid source. | Fragile orchestration | The failed source is not exposed as an editable draft contract; the model commonly reconstructs or broadly re-authors it, introducing a different defect. Non-coded errors such as the profile rejection get no correction. |
| Partial-wave aggregation | Recent logic preserves ready references and can recommend one replacement wave only for fully evidenced `animation_inspection_failed` partial waves. | Narrowly repaired | Other author-correctable publication failures and the profile contract defect still terminate the requested wave. |
| Provider latency | Live evidence showed high-reasoning Designer turns taking many minutes; testbench `off` reasoning improved bounded completion. | Provider/configuration reliability issue | It amplifies every retry. It is not the cause of the profile rejection and does not repair publication semantics. |
| Artifact state model | The first create call both supplies bytes and attempts terminal publication, renderer validation, ready-state transition, and inspection-frame creation. | Architectural coupling | Draft authoring, validation, publication, and evidence generation cannot be independently retried or resumed. |
| Video handoff | Video Studio requires 2–16 compatible ready HTML candidates plus a render-ready fallback; candidate requirements/profile/duration must match. Selected HTML is later exported and promoted as an MP4 derivative. | Valid but downstream-sensitive | No fallback or proposal can be assembled until enough HTML candidates survive the brittle upstream gate. |
| Export contract | Export correctly inherits the source profile and rejects caller-supplied profile overrides. | Working authority with prior prompt misuse | Earlier parents redundantly passed `animation_profile`, wasting attempts; schema text was recently clarified, but this is separate from managed create. |

## Why the accumulated fixes produced little launch progress

The work repeatedly optimized the failure that happened in the latest run rather than changing the unstable boundary:

- a clipped frame led to inspection/replacement-wave logic;
- responsive layout overflow led to fixed-stage prompt rules;
- parser failures led to more binder and syntax guidance;
- blank frame zero led to stronger opening-frame prose;
- slow generation led to lower test reasoning;
- caller-supplied export profiles led to clearer export guidance.

Each correction is locally reasonable, but the child still must synthesize a protocol-heavy application in one tool payload and publish it through a generic context-insensitive API. The next run can therefore fail at a different layer. The system has become **prompt-patched rather than compiler-driven**.

The deterministic renderer is not “corrupt”; it is exposing invalid authored programs and contradictory call contracts. The corruption is in the composition of authoring, policy, validation, publication, recovery, and video assembly into one probabilistic transaction.

## Immediate launch-unblock path

Do these in order. Do not start another blind three-Designer live wave before steps 1–4 exist.

### 1. Remove the fatal redundant-profile trap

For a managed Designer context:

- If `animation_profile` is omitted, inject the trusted snapshot as today.
- If it is supplied and resolves exactly to the trusted profile, ignore the redundant caller value and continue with the trusted snapshot.
- If it differs, reject with a coded contextual contract error and no mutation.
- Apply the same exact-match/ignore rule to redundant `output_requirements`; never allow either caller value to override trusted state.

This is safer and more reliable than testing for field presence. Trusted orchestration remains authoritative.

### 2. Give managed Designers a context-bound publication schema

When a run has a managed `ArtifactRunContext`, expose only a narrow publication action such as `publish_candidate` (or a contextual `create` schema) with:

- `filename`, `media_type`, `content` or package `entries`;
- optional presentation and locator-only review targets;
- exact source-lineage fields only when a source is already authenticated.

Do not expose destination IDs, output requirements, animation profile, collection metadata, export controls, delete/select/promote actions, or unrelated image operations. The executable schema—not prose—must express ownership.

### 3. Add a server-owned canonical animation starter

Stop asking every Designer to reinvent the host protocol. Supply a reviewed, versioned `motion_ui` shell that already owns:

- both manifests;
- parser-time binder and final `ready/seek/stop` methods;
- one shared `renderAt(timeMs)` timeline;
- live RAF lifecycle and reduced-motion behavior;
- `swarm-player/v1` bridge;
- fixed 1920×1080 containment CSS;
- deterministic section/part wiring.

The model should author scene data, styles, and `renderAt` behavior inside bounded extension points. Validate the generated classic script before Chrome.

### 4. Separate “bytes accepted” from “candidate published”

Persist the exact authored bytes as a private immutable draft before renderer validation. Validation failure should produce a structured report against that draft, not terminally destroy the only repairable source. A correction creates a derived draft from exact prior bytes and bounded edits. Only a validated draft becomes a ready candidate.

### 5. Prove one golden path before restoring waves

Use one checked-in deterministic fixture and one managed Designer candidate:

1. contextual schema rejects conflicting policy but accepts redundant exact policy;
2. exact bytes become a draft;
3. static syntax/manifest validation passes;
4. trusted preflight returns three inspection frames;
5. candidate becomes ready;
6. fallback derives from the same exact candidate;
7. two byte-preserving variants are produced from a known-good base;
8. one pending `propose_html_iteration` with one stable part is created;
9. selected candidate export produces an exact-lineage MP4 derivative.

Only after this passes twice should a parallel three-Designer test be re-enabled.

### 6. Launch contingency

If launch content is needed before V2 lands, bypass freeform managed Designer generation for the critical animation:

- author or select one known-good workspace HTML template;
- publish its exact bytes with `publish_workspace` and the reviewed profile;
- create alternatives with `derive_text` bounded replacements so the runtime shell remains byte-identical;
- export a fallback from one validated candidate;
- submit the pending Video Studio HTML iteration;
- export only the selected candidate when the MP4 derivative is required.

This uses existing authorities while avoiding another probabilistic rewrite of the runtime shell.

## Artifact V2 boundary

Artifact V2 is now the canonical managed creative protocol. This document is retained only as historical V1 deletion and primitive-audit evidence; its coexistence/migration sequencing and any patch-oriented V1 instructions are superseded by `artifact-v2-constitution.md` and current code.

### Separate first-class objects

1. **Artifact Draft** — immutable exact bytes plus source lineage; private to authoring until validated.
2. **Publication Policy** — server-owned destination, output requirements, animation profile, limits, and allowed derivative types. Never model-authored.
3. **Validation Report** — versioned structured results from syntax, manifest, runtime, viewport, stability, and representative-frame checks; contains bounded repair locators and safe diagnostics.
4. **Published Candidate** — a validated draft promoted into the existing durable artifact chain with ready status and exact evidence references.
5. **Derivative** — still, fallback, MP4, or package generated from one exact published candidate with server-owned settings and lineage.
6. **Video Attachment** — a small adapter referencing compatible published candidates/fallback/derivative; Video Studio does not reinterpret HTML publication policy.

### V2 state machine

`draft_created → validating → valid | invalid → published → derivative_staging → derivative_ready | derivative_failed`

Key rules:

- `invalid` is repairable and retains exact bytes; it is not a failed publication slot.
- publication is impossible without a matching valid report for the exact draft digest and policy revision;
- a validation report cannot be reused after bytes or policy change;
- every repair derives from an exact prior draft and records bounded edits;
- candidate selection never changes bytes or validation evidence;
- derivative failures do not invalidate the source candidate.

### V2 tool surface

Use separate, context-bound actions:

- `create_draft(content|entries)` — no destination/profile/requirements arguments;
- `validate_draft(draft_ref)` — returns structured diagnostics and inspection evidence;
- `derive_draft(draft_ref, exact_edits)` — byte-preserving repair path;
- `publish_candidate(draft_ref, validation_ref)` — server verifies digest/policy and promotes atomically;
- `create_derivative(candidate_ref, kind)` — fallback/still/MP4 through allowlisted server settings;
- `attach_video_candidates(candidate_refs, fallback_ref, stable_part)` — validates compatibility and creates the pending Video Studio proposal.

Managed Designer runs should receive only the actions allowed by their injected capability. Parent agents retain discovery, selection, export, and video actions.

### Structured diagnostics

Preserve safe renderer outcomes such as:

- lifecycle phase and exact failed contract (`bind`, `ready`, `seek`, `live_playback`);
- manifest field/path;
- static script syntax location;
- viewport selector/pseudo-element and bounded coordinates;
- inspection slot/timestamp;
- asset readiness category;
- whether a prior stage passed and must be preserved.

Do not expose browser output, private paths, source snippets, network URLs, provider payloads, or secrets.

### Migration strategy

1. Existing V1 ready artifacts remain readable only through the bounded `artifactlegacy.Reader`; there is no bulk data migration and no legacy write bridge.
2. Introduce V2 draft/report records beside existing artifact collections.
3. Implement `publish_candidate` by calling the existing artifact authority only after V2 validation succeeds.
4. Keep current exact references for published candidates and derivatives so Video Studio compatibility is preserved.
5. Route managed animated-HTML Designer writes to V2 behind a server feature gate; leave ordinary direct artifact creation unchanged initially.
6. Add the video attachment adapter, then retire model-authored manual assembly of candidate/fallback arrays.
7. The V1 managed create/create_package path is retired from registration and direct execution fails closed; Artifact V2 is the only managed Designer writer.

### What not to rewrite

Retain unless focused evidence disproves them:

- `pebblestore` artifact durability and exact-reference identity;
- artifact authority lineage checks;
- trusted Chrome deterministic capture and MP4 renderer;
- animation-profile registry and server-owned budgets;
- Video Studio proposal/selection/user-acceptance boundaries;
- existing ready-artifact read compatibility.

The highest-value redesign is the **write protocol**, not the durable read model or renderer.

## Likely attack points

- Context-bound schemas must not let a child recover forbidden destination or policy fields through aliases or package metadata.
- Exact-match compatibility must compare canonical resolved snapshots, not raw caller JSON.
- Draft validation must bind report, bytes digest, policy revision, producer principal, and account scope.
- Repair locators must not expose source text or permit edits outside the exact draft.
- Validation retries need bounded CPU/browser capacity and idempotent request IDs.
- A failed derivative must not mutate or demote the source candidate.
- Video attachment must reject mixed duration/profile/output requirements before proposal mutation.
- V1/V2 write coexistence is prohibited. V1 is historical ready-read compatibility only; V2 owns composition and published heads.

## Relevant filepaths

- `swarmd/internal/run/service_task_launch.go` — parses and resolves task animation profiles.
- `swarmd/internal/run/service_tools.go` — allocates managed destinations/placeholders, propagates trusted context, builds Designer prompts, and aggregates handoffs.
- `swarmd/internal/run/provider_tool_invoker.go` — carries `ArtifactRunContext` into provider tool execution.
- `swarmd/internal/run/service_task_swarm.go` — repeats the managed publication contract for Designer swarms.
- `swarmd/internal/run/service.go` — child tool-failure stopping and one-round refinement classification.
- `swarmd/internal/run/designer_animation_guidance.go` — current large model-authored runtime and inspection protocol.
- `swarmd/internal/tool/runtime_manage_artifact.go` — generic schema, contradictory managed-create key rejection, trusted injection, publication, and inspection-frame handoff.
- `swarmd/internal/tool/runtime_manage_artifact_animation.go` — manifest parsing, publication preflight, export, diagnostic normalization, and derivatives.
- `swarmd/internal/htmlcapture/animation.go` — trusted runtime binding, readiness, seeking, stable pixels, containment, and rendering.
- `swarmd/internal/artifact/animation_profile.go` — closed canonical animation-profile registry.
- `swarmd/internal/artifact/authority.go` — durable publication, lineage, idempotency, and ready-state authority.
- `swarmd/internal/videoproject/service.go` — compatible HTML candidate/profile/duration validation.
- `swarmd/internal/tool/runtime_manage_video.go` — Video Studio HTML iteration, selection, and derivative tool contract.
- `swarmd/internal/videorender/service.go` — final exact artifact materialization and render validation.
- `docs/checkpoints/html-video-animation-capture-contract.md` — stale authoring contract that must be reconciled with current binder/live-playback behavior.
- `docs/checkpoints/video-maker-html-animation-audio-gap-report.md` — stale summary of the runtime bootstrap contract.
- `scripts/runners/designer-artifact-flow.mjs` — provider-backed pipeline proof and current failure evidence surface.

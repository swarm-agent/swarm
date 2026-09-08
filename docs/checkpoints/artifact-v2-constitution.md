# Artifact V2 Constitution

Status: **frozen implementation contract**
Date: 2026-08-31
Authority: approved plan `Rebuild the Artifact Creation Pipeline`, checkpoint 1
Implementation state at freeze: **not implemented**

## 1. Constitutional rule

Artifact V2 is a from-first-principles replacement for the managed creative-write product. It is **not** a copy, fork, rename, wrapper, extension, compatibility write layer, schema alias, route alias, incremental revision, feature-gated branch, dual write, shadow write, or fallback around the V1 artifact system.

V1 managed-write code may be inspected only to:

1. enumerate paths that must be deleted or quarantined;
2. prove the structural read-only boundary for historical ready artifacts; and
3. audit a lower-level primitive before that primitive is placed behind a V2-owned interface.

No later checkpoint may begin from a V1 handler, action schema, prompt, mutation, record, projection, state machine, placeholder, retry classifier, accepted-head transition, renderer choreography, storyboard handoff, or video candidate array. If implementation discovery makes V1 adaptation appear easier, the required response is to design the missing V2 boundary. There is no exception process for reusing V1 managed-write authority.

Copied, wrapped, renamed, aliased, extended, translated, delegated-to, or fallback V1 behavior fails review even when observable happy-path output appears correct.

## 2. Product invariant

One server-owned Artifact V2 authority governs every new managed creative write from allocation through publication and downstream conversion. A Designer authors creative part content; it never chooses durable destination identity, output policy, runtime/compiler shell, validator, renderer, lineage, publication state, or downstream proposal structure.

A working result appears in the session artifact sidebar as soon as the server allocates its durable Working Artifact. Every later state is projected from durable V3 events. Artifact Studio reads that projection to display the whole composition, real independently revisioned parts, immutable candidate revisions, validation/build evidence, iteration rounds, locks, the current composition head, and the published head.

Publication is a reversible pointer decision over an already validated exact composition. It is not the same operation as accepting bytes, building, validating, previewing, or generating a derivative.

## 3. Names, namespaces, and references

All V2 implementation symbols, records, event kinds, storage keys, APIs, and tool contracts use an explicit V2 namespace. Planned implementation packages use `artifactv2` or a more narrowly named V2-owned child package. They must not be added to package `artifact` as another mode.

Canonical planned boundaries:

- Go domain root: `swarmd/internal/artifactv2/`
- V3 records: `ArtifactV2*`
- V3 events/mutations: `artifact.v2.*`
- session API family: `/v3/sessions/{session_id}/artifact-v2/...`
- context-bound Designer tool: `artifact_v2_author`
- parent/server application service: `artifactv2.Service`
- immutable bytes interface: `artifactv2.BlobStore`
- trusted compiler/renderer interfaces: `artifactv2.Compiler`, `artifactv2.Validator`, `artifactv2.PreviewRenderer`
- legacy reader: `artifactv2.LegacyReadyReader`
- Video Studio adapter: `artifactv2.VideoConversionService`

These names are contract reservations, not claims that routes or packages already exist.

### 3.1 Exact V2 reference

An internal exact reference is:

```text
ArtifactV2ExactRef {
  artifact_id
  object_kind
  object_id
  revision
  event_seq
  content_digest            // required for byte-bearing or composition objects
  policy_revision           // required for validation/publication/conversion objects
}
```

Clients and models receive an opaque authenticated encoding plus bounded display metadata, not account IDs, storage paths, repository refs, browser settings, provider payloads, or mutable policy. Resolution always re-derives account, user, and session authority from trusted context and verifies every field against durable state. Human labels never identify an object.

Exact lineage is an ordered set of typed references, not free-form metadata. A lineage edge is accepted only when the server resolves both endpoints in the same account/user scope and the edge kind is allowed by this contract.

## 4. Domain objects

Every object below is first-class and durable. No object is reconstructed from transcript text, a model-authored manifest, a V1 collection/variant, a Desktop cache, or a renderer response.

### 4.1 Working Artifact

**Purpose.** Durable authoring workspace visible immediately, before any part bytes exist.

**Identity.** Server-generated `artv2_*`; unique inside account scope and never supplied by a model.

**Owner.** Account + user + parent V3 session. A delegated Designer is a producer principal, never the owner.

**Mutation authority.** `artifactv2.Service` through `ApplyV3SessionMutation` using only `artifact.v2.*` mutations. The context-bound Designer capability can request permitted part operations; it cannot mutate the record directly.

**Required immutable fields.** Owner scope, parent session, artifact kind, server-resolved output-policy revision, capability class, creation request/idempotency key, and initial user intent reference.

**Allowed lifecycle states.**

```text
allocated -> authoring
allocated|authoring|invalid|ready -> building
building -> validating | invalid
validating -> invalid | ready
ready -> iterating | building | published_view
iterating -> building | ready | published_view
published_view -> iterating | building | ready
any nonterminal state -> cancelled
```

`published_view` is a projection convenience meaning that a Published Head currently points into this Working Artifact. It is not a terminal write state and cannot replace the Published Head object. `invalid` is repairable. Build/validation failure never deletes part revisions or rewinds the composition head. `cancelled` blocks new Designer writes but preserves history.

**Exact lineage.** Root lineage references the trusted run/task/iteration allocation event. It never references a V1 artifact. Every derived object must reference this Working Artifact and its event sequence.

**User-visible projection.** Sidebar row from allocation onward with `Working`, `Building`, `Validating`, `Needs repair`, `Ready`, `Iterating`, or `Published`. Projection includes bounded progress and latest safe diagnostic summary, never placeholders pretending to be artifact variants.

### 4.2 Part

**Purpose.** Stable identity for one real independently revisioned input to a composition. A single-part artifact is permitted, but that part still has immutable revisions and cannot be locator-only metadata.

**Identity.** Server-generated `partv2_*`. The Designer may propose a bounded `part_key`, label, role, media class, and review locator; the server allocates identity and rejects duplicate/unsafe definitions.

**Owner.** The Working Artifact owner. Definition ownership cannot migrate to a child session.

**Mutation authority.** Server creates the definition after validating it against the capability and compiler contract. Definition fields used by compilation are immutable. Display labels/locators may change only through a separate metadata event that cannot change bytes, order, policy, or identity.

**States.** `declared -> populated`; `populated` remains true once at least one revision exists. A part can be `locked` only in a Composition Head, not globally mutated.

**Exact lineage.** References its Working Artifact allocation event and the declaration request. Locator metadata is never lineage and never proves bytes.

**User-visible projection.** Stable part entry in Artifact Studio with label, kind, current exact revision, validation association, candidate history, and lock state from the selected Composition Head.

### 4.3 Part Revision

**Purpose.** Immutable exact bytes for one Part.

**Identity.** Server-generated `prev2_*`, bound to one Part and one immutable blob digest.

**Owner.** Working Artifact owner. Producer session/run is audit lineage only.

**Mutation authority.** Append-only `artifactv2.Service`. No update or overwrite method exists. A repair or alternative creates another Part Revision.

**Allowed transitions.** `accepted_bytes` is terminal. Availability may be projected separately as `available | unavailable`, but unavailability never changes identity or permits replacement under the same revision ID.

**Exact lineage.** Includes Working Artifact, Part, producer capability grant, optional exact parent Part Revision, byte digest, size, canonical media type, and storage receipt. A parent link is required for repair/revision actions and forbidden for the first revision. Cross-account, cross-artifact, cross-part, stale, or missing parents are rejected before storage mutation.

**User-visible projection.** Candidate chip/preview and history entry. The UI may show a safe authored label but selection uses the exact opaque reference.

### 4.4 Composition Head

**Purpose.** The current complete ordered composition of exact Part Revisions plus per-part locks and a server-owned construction specification.

**Identity.** Each immutable composition is `compv2_*`; the mutable Working Artifact head is a CAS pointer `(composition_id, head_revision)`.

**Owner.** Working Artifact owner.

**Mutation authority.** `artifactv2.Service` only. A Designer can submit a candidate composition through its capability; the server validates completeness and performs CAS against the exact expected head. Artifact Studio selection/lock actions use separate user or parent application commands.

**Allowed transitions.** A composition object is immutable. The head pointer may advance from exact expected head to a complete new composition. It never advances partially and never points to an unpersisted revision. Locked slots must retain the exact locked revision unless a user unlock event is part of the same atomic command.

**Exact lineage.** Ordered Part IDs, exact Part Revision refs, lock bits, construction/compiler input version, parent composition ref(s), and content-derived composition digest. All parts must belong to the same Working Artifact and policy revision.

**User-visible projection.** Whole-composition preview target, ordered parts, current/locked badges, ancestor/candidate relation, and whether a matching successful Build and Validation Result exists.

### 4.5 Build Result

**Purpose.** Immutable result of compiling one exact Composition into an executable/viewable artifact using server-owned code and policy.

**Identity.** `buildv2_*`.

**Owner.** Working Artifact owner; produced by server worker principal.

**Mutation authority.** `artifactv2.Compiler` worker through `artifactv2.Service`. Models cannot provide compiler/runtime versions, output paths, generated manifests, browser flags, dimensions, frame rate, encoder settings, or success state.

**States.** `queued -> running -> succeeded | failed | cancelled`. Terminal states are immutable. Retries create a new Build Result with an explicit retry-of edge and the same bound inputs.

**Exact lineage.** Exact Composition ref and digest, output-policy revision, compiler identity/version, template/runtime-shell version, canonical build inputs, generated output blob refs, and bounded deterministic log/diagnostic digest. A success for one composition or policy cannot be reused for another.

**User-visible projection.** `Building`, safe failure code/part locator, build version, and successful preview/output availability. Raw logs, source paths, generated runtime internals, and private environment details are never projected.

### 4.6 Validation Result

**Purpose.** Immutable proof that one exact successful Build satisfies one exact policy revision.

**Identity.** `valv2_*`.

**Owner.** Working Artifact owner; produced by server validator principal.

**Mutation authority.** `artifactv2.Validator` through `artifactv2.Service`.

**States.** `queued -> running -> valid | invalid | cancelled`. Terminal states are immutable. Infrastructure failure is a typed invalid result only when validation actually ran and reached a contract failure; unavailable infrastructure creates `failed_to_run` and cannot be represented as authored invalidity or validity.

**Exact lineage.** Exact Build ref, Composition ref/digest, policy revision, validator suite/version, renderer snapshot, bounded evidence refs, and safe diagnostics. Publication requires `valid` and an exact match on every bound field.

**User-visible projection.** `Validating`, `Ready`, or `Needs repair`; per-part safe diagnostics; inspection still/frame references; and proof timestamp/version. It excludes browser output, URLs, private paths, provider content, secrets, and unbounded source snippets.

### 4.7 Validation/Build diagnostic

Build and Validation Results share a closed diagnostic shape:

```text
ArtifactV2Diagnostic {
  code
  phase                    // schema|compile|bind|ready|seek|layout|pixel|asset|policy
  severity
  part_id?                 // server-resolved
  authored_locator?        // bounded selector/state/time/line-column within authored part
  frame_slot_or_time?      // bounded
  bounds?                  // bounded viewport coordinates
  preservation_proofs[]    // prior stages that passed and must remain unchanged
  retry_class              // repairable|policy|infrastructure|terminal
  safe_message
}
```

The server, not a text classifier, assigns `retry_class`. Diagnostics cannot grant capability, widen editable parts, or contain replacement bytes.

### 4.8 Iteration Round

**Purpose.** Durable grouping of alternatives for one exact base composition and target set.

**Identity.** `roundv2_*`, allocated before child execution.

**Owner.** Working Artifact owner.

**Mutation authority.** Parent/user application service opens the round. Context-bound Designers may append candidate Part Revisions/Compositions only to their allocated candidate slots. User/parent selection and lock decisions use separate commands.

**States.**

```text
open -> generating -> awaiting_selection -> selected | closed_without_selection | cancelled
```

Partial success remains explicit: failed candidate slots retain typed terminal evidence and do not fabricate placeholder variants. Adding a candidate is append-only. Selecting is CAS-bound to the exact round revision and candidate refs. A selected round advances the Composition Head atomically; it does not publish automatically.

**Exact lineage.** Base Composition, targeted Part IDs, preserved locked revisions, candidate slot IDs, task/child lineage, and selected candidate. Candidates cannot change non-target or locked parts.

**User-visible projection.** Named round in Artifact Studio, requested count, per-slot status, candidate previews, exact selected decision, unchanged-part proof, and repair actions. Sidebar shows real working-state progress, not preallocated V1 variants.

### 4.9 Published Head

**Purpose.** Immutable publication decision over one exact validated Composition.

**Identity.** Each decision is `pubv2_*`; the Working Artifact has a CAS current-published pointer. Publication never changes Part Revision or Composition bytes.

**Owner.** Working Artifact owner.

**Mutation authority.** User action, or a parent application command only when the server-resolved authorization captured from the initiating user request permits parent publication. A Designer capability never has publication authority. Authorization mode is not a model argument.

**States.** A Published Head record is immutable and `active | superseded` is derived from the current pointer. Revert creates a new Published Head pointing to a previously valid Composition.

**Exact lineage.** Exact Composition, successful Build, valid Validation Result, policy revision, authorizing principal/action, and prior Published Head. Any stale head, changed policy, mismatched digest, invalid result, or cross-account ref rejects with no pointer movement.

**User-visible projection.** `Published`, publication history, exact active composition, authorizing action, and available server-owned derivative/conversion actions.

### 4.10 Storyboard Composition

**Purpose.** Artifact V2 specialization, not a separate handoff document or model-authored Video Studio plan.

**Identity.** A Working Artifact with `kind=storyboard_v2`; each ordered storyboard Part has stable `partv2_*` identity.

**Owner and mutation authority.** Same as Working Artifact/Part. The server validates storyboard metadata and builds capture outputs.

**Required per-part typed metadata.** Title, positive bounded duration, creative direction, non-empty filming requirements, production state (`pending | ready`), stable server-derived capture state identity, and optional V2 spatial-composition reference. Narration/on-screen text are creative fields, not timeline acceptance.

**Allowed transitions.** Production state can advance `pending -> ready` only through an explicit trusted media assignment or user action defined by the later storyboard implementation; it cannot regress silently. Part order changes create a new Composition. Capture stills are Build/Validation evidence bound to that composition, not model-supplied fallback arrays.

**Exact lineage.** Storyboard Composition -> ordered exact Part Revisions -> server-generated capture Build outputs -> Validation Result. Spatial slots use exact registered media refs and are validated by the server.

**User-visible projection.** Artifact Studio shows ordered shots, requirements, production state, still evidence, unresolved spatial slots, and exact published storyboard head.

### 4.11 Video Conversion

**Purpose.** Server-owned conversion of one exact Published Head into one pending Video Studio proposal.

**Identity.** `convv2_*`.

**Owner.** Account/user/session owning the target Video Studio project, with source ownership revalidated.

**Mutation authority.** `artifactv2.VideoConversionService`. AI can request conversion through a parent action but cannot provide candidate arrays, fallback arrays, plan parts, storyboard exports, derivative mappings, project acceptance, or render commands.

**States.**

```text
requested -> verifying -> building_adapter -> pending_proposal_created
requested|verifying|building_adapter -> failed | cancelled
```

A successful conversion is terminal and points to a pending proposal. It never accepts that proposal and never starts final rendering.

**Exact lineage.** Published Head, Composition, Build, Validation, all generated preview/fallback/derivative outputs, adapter version, target project, exact current base revision, pending proposal ID/revision, and idempotency key. Stale target revision, mixed policy/duration/profile, pending storyboard production, unresolved required slot, or cross-account source rejects before proposal mutation.

**User-visible projection.** Conversion status and link to the pending Video Studio proposal. Video Studio retains user-controlled acceptance.

## 5. Durable journey and event grammar

### 5.1 Golden user journey

1. The parent accepts the user's artifact request. The server resolves owner, artifact kind, output policy, compiler profile, limits, capability class, and publication authorization.
2. In one V3 mutation, the server emits `artifact.v2.working.created`. The sidebar immediately shows a real Working Artifact in `Working`; no child or placeholder variant is required.
3. A Designer run receives a context-bound `artifact_v2_author` capability referring to that Working Artifact through trusted runtime context. Destination and policy are absent from its executable schema.
4. The Designer inspects the authoring context, declares permitted real parts, and writes initial Part Revisions. Each successful write emits an append-only event and becomes visible in Artifact Studio.
5. The server creates/advances an exact Composition Head by CAS. The whole composition and each real part are now navigable even before validation.
6. The Designer requests a build. The server emits build queued/running/terminal events, compiles through the server-owned compiler/runtime shell, and starts validation only for a successful exact build.
7. Validation emits safe structured evidence. If invalid, the Working Artifact remains visible and repairable. A repair must name the exact base Part Revision and expected Composition Head; it creates new immutable bytes and a new composition.
8. When valid, the Working Artifact becomes `Ready`. Nothing is published merely because validation succeeded.
9. For alternatives, the parent/user opens an Iteration Round against an exact base composition and target set. Candidate slots append real candidate revisions/compositions. Artifact Studio compares them and the user/authorized parent selects and optionally locks exact revisions. Selection advances the Composition Head atomically but does not publish.
10. Publication creates a Published Head bound to the exact valid Build/Validation evidence. Re-publication or revert creates another immutable publication decision.
11. A compatible Published Head can be converted by a server-owned adapter. It builds all required stills/fallbacks/derivatives itself and atomically creates one pending Video Studio proposal. AI never reconstructs the proposal arrays. The user reviews and accepts in Video Studio.

### 5.2 Canonical durable events

The initial event set is closed and versioned:

```text
artifact.v2.working.created
artifact.v2.working.cancelled
artifact.v2.part.declared
artifact.v2.part.metadata_changed
artifact.v2.part_revision.appended
artifact.v2.composition.created
artifact.v2.composition_head.advanced
artifact.v2.build.queued
artifact.v2.build.started
artifact.v2.build.succeeded
artifact.v2.build.failed
artifact.v2.build.cancelled
artifact.v2.validation.queued
artifact.v2.validation.started
artifact.v2.validation.valid
artifact.v2.validation.invalid
artifact.v2.validation.failed_to_run
artifact.v2.validation.cancelled
artifact.v2.iteration.opened
artifact.v2.iteration.candidate_appended
artifact.v2.iteration.candidate_failed
artifact.v2.iteration.awaiting_selection
artifact.v2.iteration.selected
artifact.v2.iteration.closed
artifact.v2.part_lock.changed
artifact.v2.published_head.created
artifact.v2.video_conversion.requested
artifact.v2.video_conversion.started
artifact.v2.video_conversion.proposal_created
artifact.v2.video_conversion.failed
artifact.v2.video_conversion.cancelled
```

Every event carries schema version, account/user/session envelope, object ID, object revision, idempotency key, causation ID, correlation ID, actor class, timestamp, and only typed references needed by that event. Payloads never contain raw artifact bytes, paths, browser logs, provider output, or authority-bearing caller metadata.

Event, projection, idempotency, indexes, and realtime outbox update atomically. Realtime/sidebar delivery is an accelerator; reconnect/hydration reconstructs the same Working Artifact from durable events/projections.

### 5.3 Failure atomicity

- Byte storage receipt is committed before a Part Revision event may reference it; an unreferenced receipt is garbage-collectable and invisible.
- Composition Head CAS and event/projection commit are one mutation.
- Build/validation terminal records never update the head.
- Iteration selection, lock changes, and head advance are one mutation.
- Publication pointer movement and Published Head event are one mutation after all exact evidence is revalidated.
- Video proposal creation and conversion terminal event use an idempotent cross-service operation: either the exact pending proposal is discoverable and referenced, or no conversion success is recorded. Retry resolves the same proposal; it never creates a second proposal.

## 6. Capability and action separation

### 6.1 Context-bound Designer surface

Managed Designers receive only `artifact_v2_author`. The trusted runtime binds account, owner session, Working Artifact ID, capability grant ID, allowed part/media classes, byte/count limits, allowed operation set, target set for focused iteration, expected policy revision, and expiry. Those values are not caller fields.

Allowed actions:

| Action | Model arguments | Server behavior |
|---|---|---|
| `inspect_context` | none | Returns bounded creative brief, existing allowed parts/current exact revisions, safe diagnostics, editable target set, and content limits. No destination/policy/storage IDs. |
| `declare_parts` | bounded part keys, labels, roles, media classes, optional review locators, construction ordering hints | Validates against capability/compiler class and allocates stable Part IDs. Cannot set owner, paths, policy, runtime, or durable IDs. At most once for an empty artifact unless the grant explicitly allows additive parts. |
| `write_part` | `part_id`, content or bounded package entries, optional creative filename, exact `expected_base_revision`, exact `expected_composition_head` | Stores immutable bytes, creates one Part Revision, and optionally proposes a complete composition through server construction. First write requires an explicitly empty base; repair requires the exact current/authorized base. |
| `write_parts` | bounded all-or-nothing set of the same write shape | Creates all revisions and one complete composition atomically from the caller's perspective; partial state is not projected. |
| `request_build` | exact `expected_composition_head` | Queues server build/validation. It cannot name compiler, validator, renderer, profile, dimensions, FPS, browser, encoder, destination, or publication. |

A focused Designer grant omits `declare_parts`, exposes only the selected stable Part IDs, and rejects edits to all other or locked parts. A capability can publish at most its allocated candidate count and cannot be reused by another run/session.

Forbidden Designer fields include collection/variant/chain/repository IDs, destination, owner, account/session lineage, output requirements, policy, animation profile, compiler/runtime/template version, manifest version, renderer/browser/encoder flags, arbitrary dimensions/FPS/duration overrides, validation status, failure code, progress, selection, locks outside the grant, publication, derivative kind, video project/proposal data, source paths, preview URLs, storage refs, V1 references, and retry classification.

### 6.2 Parent/server application actions

These actions are not available to managed Designers:

- allocate/cancel Working Artifact;
- open/cancel Iteration Round and candidate grants;
- request build/validation retry;
- select candidate revisions and advance Composition Head;
- lock/unlock exact Part Revisions;
- publish/revert Published Head when server-resolved authorization permits;
- request V2 derivative or Video Conversion;
- inspect/search/list owned V2 objects;
- materialize/download a Published Head through contained read APIs;
- administer cleanup under retention policy.

The parent never supplies server policy or destination internals. It refers to opaque exact V2 refs and expected revisions only.

### 6.3 User-only actions

Artifact Studio owns explicit selection/lock overrides, publication when authorization requires a user gesture, publication reversion, cancellation, and opening the pending Video Studio proposal. Video Studio alone owns acceptance of the pending proposal and final render initiation. AI can recommend but cannot execute those user-owned transitions.

## 7. Anti-derivation matrix

Anything in this matrix is forbidden as a V2 write dependency. “Forbidden” includes direct import/call, copied logic, renamed type/action, wrapper, adapter that invokes it, schema/route alias, feature flag, compatibility mode, fallback, dual/shadow write, event translation, or reconstruction in Desktop/provider code.

| V1 category | Exact current evidence / examples | Forbidden V2 dependency | Required V2 replacement |
|---|---|---|---|
| Broad model tool | `swarmd/internal/tool/runtime_manage_artifact.go`, `manageArtifactDefinition`, action enum | `manage_artifact` for any V2 create/mutate/validate/select/publish/convert path | context-bound `artifact_v2_author` plus separate parent/user services |
| One-shot writes | actions `create`, `create_package`, `publish_workspace`, `generate_image` | accepting bytes and attempting terminal ready publication in one call | Working Artifact + Part Revision + Build + Validation + Published Head |
| V1 derivation writes | `derive_text`, `publish_part`, `publish_parts`, `select_parts`, `promote`, `delete`, `cancel_html_animation_export` | V1 ready reference as a base or any V1 mutation in a V2 journey | V2 exact Part Revision/Composition operations; V1 remains read-only |
| V1 export writes | `export_html_stills`, `export_html_animation`, `export_html_animation_fallback` | model/parent choreography that generates V1 derivatives for V2 | V2 server build/derivative/conversion services bound to Published Head |
| V1 focused protocol | `runtime_manage_artifact_parts.go` read-before-publish state and one-publication guards | copying the per-run in-memory read/publish protocol | durable capability grant + exact expected base/head + append-only revisions |
| V1 initial part schema | `initial_parts`, locator `parts`, `part_choices`, `replacements` in `runtime_manage_artifact.go` | aliasing those payloads as V2 parts/candidates | V2 Part definitions, revisions, complete compositions, and typed selection commands |
| V1 domain authority | `swarmd/internal/artifact/authority*.go`; `Authority.Create`, `CreatePackage`, `Reserve`, `CreateInitialComposition`, `PublishPartReplacements`, `SelectPartRevisions`, `PublishWorkspace`, `MarkFailed`, `Select` | any V2 write call into `artifact.Authority` | new `artifactv2.Service` using only audited primitive interfaces |
| V1 create input | `artifact.CreateInput`, `AutoAccept`, `CollectionID`, `VariantID`, `ArtifactStepID`, `CandidateIndex` | type alias, embedding, translation, or field-for-field clone | V2 commands scoped to one Working Artifact/capability |
| V1 mutations | `artifact.create`, `artifact.update`, `artifact.finalize`, `artifact.fail`, `artifact.unavailable`, `artifact.select`, variant/collection delete in `session_event_store.go` | V2 event translation into or out of these kinds | distinct `artifact.v2.*` mutations and projections |
| V1 records | `SessionArtifactCollection`, `SessionArtifactVariant`, `SessionArtifactStep`, `SessionArtifactChain`, `SessionArtifactPart*`, `SessionArtifactComposition*` | schema alias, embedding, decode/encode bridge, or persistence reuse for new writes | `ArtifactV2*` records with the objects in §4 |
| V1 statuses | `staging`, `ready`, `failed`, `unavailable`; collection counters | mapping V2 lifecycle onto these four states | explicit V2 working/build/validation/iteration/publication states |
| V1 placeholders | `allocateManagedArtifactPlaceholders` and placeholder validation in `run/service_tools.go`; projection-only `Reserve` | preallocating fake variants before authored bytes | one real Working Artifact allocation and real candidate slot records |
| V1 aggregation | parent-owned collections, iteration group/variant/candidate aggregation in `service_tools.go` and `service_task_swarm.go` | using collection counts/readiness as V2 orchestration authority | V2 Iteration Round and candidate slot projection |
| V1 automatic head | `AutoAccept`, `repo.AdvanceOfficial`, `SessionArtifactStep.Accepted`, collection selected variant | automatic accepted/official movement on create/finalize | explicit Composition Head and Published Head CAS commands |
| V1 Git graph semantics | `refs/heads/official`, `refs/swarm/candidates/*`, `refs/swarm/transactions/*`, V1 chain/step projection | treating V1 official/candidate refs as V2 domain state | V2 BlobStore plus V2-owned composition/publication records; storage refs remain private |
| V1 prompts | managed publication prose in `run/service_tools.go`, swarm specialization, `designer_animation_guidance.go`, checkpoint/master prompt text describing one successful create/create_package | prompt patching or retaining publication instructions for V2 Designers | executable contextual schema and compiler-owned runtime |
| V1 correction classifier | `managedDesignerRefinementCandidate`, `designerToolFailureState`, `managedDesignerRefinementFeedback`, one bounded fresh destination in `run/service.go` | text/error-code classification that allocates replacement variants | typed Validation Result retry class and durable repair against exact revisions |
| V1 partial-wave classifier | `animation_inspection_failed` replacement-wave logic in `service_tools.go` | rerunning missing V1 slots or mixing successful refs manually | V2 Iteration Round candidate terminal states and server-issued replacement grants |
| V1 animation authoring ABI | model-authored `swarm.animation/v1`, `swarm.iteration/v1`, binder, RAF scheduler, player bridge in prompts and HTML | asking a Designer to implement host/runtime protocol or treating manifests as V2 domain data | server compiler/template owns executable shell; model writes creative parts only |
| V1 capture/storyboard ABI | model-authored `swarm.capture/v1`, `swarm.storyboard/v1`, state IDs and HTML handoff | treating authored manifests/handoff JSON as Storyboard Composition authority | typed V2 storyboard Parts/metadata and server-generated capture build |
| V1 diagnostic normalization | `normalizeAnimationRendererError` and broad error strings in `runtime_manage_artifact_animation.go` | lossy string round-trip or model classification | closed V2 diagnostic records with safe locators/preservation proofs |
| V1 Desktop wire model | `DesktopV3ArtifactStatus`, V1 collection/variant/step/accepted/acceptedPartHeads normalization in `artifact-api.ts` | projecting V2 through V1 entries or deriving V2 state client-side | dedicated V2 projection types/selectors from V3 sync |
| V1 Artifact Studio model | `artifact-studio-model.ts` rounds/turns inferred from V1 steps, collections, accepted heads, staging/failed placeholders | adapting these inference functions for V2 | direct V2 Working Artifact/Part/Round/Head projection |
| V1 sidebar/gallery | V1 grouping/progress/placeholder labels in `desktop-v3-artifact-sidebar.tsx` and `desktop-v3-artifact-gallery.tsx` | client inference from V1 collection/variant status for new writes | V2 durable sidebar projection and V2 Artifact Studio view model |
| V1 session API | current session artifact handlers under `api/sessions_v3_artifacts*.go` and `/v3/artifacts` catalog | adding V2 write modes, query flags, aliases, or fallback to these handlers | separate `/artifact-v2` route/service family; V1 endpoints read-only after cutover |
| Manual storyboard choreography | `export_html_stills` response `storyboard_handoff` -> caller-created project -> `manage_video import_storyboard` with `storyboard_source` + `exports[]` | AI/manual assembly of state/reference arrays | `VideoConversionService` reads exact Published Head and constructs proposal internally |
| Manual animation video choreography | `propose_html_iteration` plan parts with `animation_candidates`, fallback visual, `select_animation_candidate`, `export_html_animation`, `promote_animation_derivative` | AI-authored candidate/fallback/derivative arrays or state transitions | one V2 conversion request; adapter owns validation, fallback/derivative creation, pending proposal |
| Generic video-plan schema | `manage_video` `plan.parts`, `animation_candidates`, `visual`, `composition_catalog`, storyboard export schema | using model-authored video plan as Artifact V2 conversion output | typed internal adapter input derived from V2 objects only |
| Legacy read-to-write bridge | any proposed import/derive/copy of a V1 ready artifact into V2 | V1 bytes or refs participating as source lineage in a new write | prohibited; historical V1 is view/download/materialize only |
| Launch contingency | known-good template + `publish_workspace` + `derive_text` | treating contingency as architecture or implementation scaffold | explicitly outside V2; may be used only by separately scoped launch operations |

A structural dependency scanner must enforce this table before implementation merges. No test that only exercises a happy path can waive a forbidden edge.

## 8. Retained-primitive allowlist

Everything not listed here is forbidden until this constitution is amended before dependent implementation begins. A retained primitive is infrastructure, not V2 domain authority. V2 owns every calling interface.

| Primitive | Invariant and current owner | V2-owned interface | Exact lineage rule | Trust boundary / likely attack | Required focused negative proof | Proof it cannot invoke V1 writes |
|---|---|---|---|---|---|---|
| Atomic V3 mutation commit | `SessionStore.ApplyV3SessionMutation` atomically commits events/projections/idempotency/outbox | `artifactv2.EventCommitter.Commit(ArtifactV2Mutation)` | event envelope and typed V2 refs share exact account/user/session and causation | caller-crafted event kind/payload, partial commit, replay mismatch | reject non-`artifact.v2.*`, cross-account refs, hash mismatch, duplicate different payload; inject batch failure and assert no state/outbox | adapter accepts a closed V2 mutation type; AST/import test forbids V1 artifact constants/types and generic raw mutation passthrough |
| Private immutable byte storage | `artifactgit.Repository` supplies isolated exact-byte Git storage; storage package has no user authority | `artifactv2.BlobStore.PutImmutable/GetExact` implemented in `artifactv2/storage` | receipt binds digest, size, media type, repository/blob identity, owner scope supplied by service | ref/path injection, symlink/protocol/config escape, overwrite, digest collision | traversal/protocol/config/hook rejection, cross-account read, stale receipt, duplicate idempotency, failed put leaves no V2 event | adapter imports `artifactgit` only, exposes no official/candidate/transaction ref or V1 `artifact.Authority`; import/method-set test enforces this |
| Identity principal | `identity` and authenticated V3 run/session context own account/user/session | `artifactv2.PrincipalResolver` | every command scope is derived, never caller supplied | forged owner/session/child lineage | cross-account/user/session commands reject with zero records/blobs | resolver has no artifact mutation dependency; compile-time interface test |
| Output policy registries | current server-owned output requirements and animation profile logic are policy concepts; direct package ownership must be neutralized | `artifactv2.PolicyResolver` returning immutable `PolicySnapshot` | policy revision is copied into Working Artifact and every build/validation/publication/conversion ref | model override, mutable registry re-resolution mid-run, raw JSON equivalence bugs | conflicting/unknown profile, stale policy, redundant caller fields impossible because schema omits them | V2 imports a neutral policy package only; direct import of V1 `artifact` package is forbidden. Extraction must preserve focused behavior and remove duplicate authority |
| Trusted Chrome still renderer | `htmlcapture.ChromedpRenderer` owns network/file isolation, stable 1920x1080 capture and bounded diagnostics | `artifactv2.PreviewRenderer.RenderStill(CompiledBuild)` | renderer input is compiler output bound to exact Build; output receipt links Build/Composition/policy | arbitrary URL/path/browser flags, network, unstable pixels, overflow, blocking UI | network/file/traversal, unstable/overflow/blocking, timeout/cancel, concurrent capacity; no evidence/event on failure | adapter accepts compiled server bytes only and returns evidence; it cannot import tool runtime or artifact authority |
| Trusted animation renderer | `htmlcapture.AnimationRenderer` owns deterministic seek/frame capture and MP4 encoding | `artifactv2.PreviewRenderer.PreflightAnimation` and `artifactv2.DerivativeRenderer.RenderAnimation` | compiled build + policy determine duration/FPS/profile; every frame/MP4 links exact Build | model-authored runtime ABI, caller FPS/browser/encoder override, resource exhaustion | malformed compiler output, stale Build, network/file, bind/seek/live/layout, capacity/cancel/partial encode; source head unchanged | V2 compiler generates the private renderer ABI. Adapter cannot parse V1 artifact records or call `manage_artifact`; imports restricted to `htmlcapture` |
| V3 sync/realtime transport | durable projection/replay is authority; realtime is acceleration | `ArtifactV2ProjectionReader` + normal sync resource | projection revision/event seq exactly matches committed V2 state | client-derived authority, cursor misuse, missed events | gap/reconnect/hydrate, stale projection, cross-scope cursor, event/projection atomicity | projection schema is `ArtifactV2Projection`; no V1 collection/variant decoder or fallback |
| Authenticated preview tickets | current API preview admission is GET-only, exact, expiry-bound | `artifactv2.PreviewTicketIssuer/Resolver` | ticket resolves exact V2 Published/Build evidence ref only | ticket replay, widened path/method/session | wrong method/path/session, expiry, tamper, cross-account, stale ref | resolver interface is read-only and accepts only V2 exact refs; no mutation service handle |
| Contained materialization | `storagecontract` and artifact materialization containment concepts protect workspace writes | `artifactv2.Materializer.MaterializePublished` | only exact active/historical Published Head can materialize its bound build output | traversal, symlink, ignored/private destination, overwrite | traversal/symlink/cross-workspace/stale head/overwrite failure leaves destination unchanged | interface accepts V2 Published ref and read-only BlobStore; no V1 reader may be passed as write source |
| Video project pending-proposal authority | `videoproject.Service` keeps proposal creation separate from user acceptance/render | `artifactv2.VideoProposalSink.CreatePendingFromConversion` | idempotent sink input is server-generated from one exact V2 conversion and exact target base revision | adapter calling accept/render, stale target, duplicate proposal, mixed source refs | stale/cross-account/unresolved storyboard/mixed policy/duplicate request; no proposal on failure; assert accepted revision unchanged | restricted sink interface exposes only pending creation/status. It has no accept, create revision, restore, or render methods; implementation import tests forbid `manage_video` runtime |
| Registered video-source resolver and spatial validator | `videosource`/`videocomposition` own opaque source and geometry validation | `artifactv2.StoryboardMediaResolver` | each slot assignment binds exact source fingerprint/ranges and storyboard Part | raw paths, stale refs, range/crop overflow, hidden audio policy | unknown/stale/cross-account source, invalid ranges/geometry, partial slot update | resolver cannot write artifacts or proposals; conversion service consumes validated typed results |
| Video Studio user acceptance | existing user-controlled proposal acceptance is a separate authority | no V2 write interface; link-only projection | pending proposal reference remains exact until Video Studio user action | AI silently accepting/rendering | tool/agent contract proves no accept/start-render action; pending remains pending after conversion | V2 service has no acceptance dependency or method; structural interface test |

### 8.1 Internal compiler ABI rule

The server-owned V2 compiler may generate a private executable ABI compatible with a retained trusted renderer. That generated ABI is not a V2 domain schema, not durable author input, and never model-authored. Compatibility with `swarm.capture/v1` or `swarm.animation/v1` inside generated bytes does not permit importing V1 artifact write handlers, manifests as source authority, or old prompts. The compiler version and template digest are bound to the Build Result.

## 9. Trust-boundary test obligations

Every implementation checkpoint must begin its tests from the invariant and attack point, then use the narrowest deterministic layer. Each command below means “eventually provide this class of proof,” not “create these exact filenames.”

| Boundary | Positive proof | Negative/stale/cross-account proof | Idempotency/failure-atomicity proof |
|---|---|---|---|
| Working Artifact allocation | one V3 mutation immediately projects sidebar state | forged owner/policy/destination rejected; no record | same key/same payload replays; different payload conflicts; injected commit failure leaves no artifact/outbox |
| Designer capability | allowed action writes only authorized part | expired/foreign grant, forbidden field/action, non-target/locked part reject | replay returns same revision; storage or event failure exposes neither partial revision nor head |
| Part Revision | exact bytes/digest can be read by owner | stale parent, wrong part/artifact/account, oversized/invalid media reject | duplicate exact request yields one revision; failed blob/event boundary leaves no visible revision |
| Composition Head | complete exact composition advances by CAS | stale head, missing/mixed/locked/cross-account revision rejects | concurrent CAS has one winner; failure leaves old head and locks unchanged |
| Build | exact composition/policy compiles to immutable result | stale composition/policy, caller runtime/path/settings, malformed server template fail closed | retry creates linked result; worker crash/restart cannot claim success or duplicate output |
| Validation | matching build yields safe evidence and `valid` | mismatched digest/policy, renderer unavailable, network/overflow/unstable/blocked output never becomes valid | replay is exact; failure after evidence storage cannot mark ready without terminal result |
| Repair | invalid part creates child revision preserving other exact parts | edit outside diagnostic grant, stale base/head, locked part rejected | failed multi-part repair changes no head or subset; retry is exact |
| Iteration Round | candidates append and user selection advances head | candidate count/slot/target mismatch, changed non-target, stale selection reject | partial candidates remain explicit; selection CAS one winner and never auto-publishes |
| Published Head | exact valid composition publishes | invalid/stale/mismatched evidence, changed policy, unauthorized parent, cross-account reject | duplicate publish returns same decision; pointer/event failure leaves prior publication active |
| Legacy reader | owner can list/read historical V1 ready artifact | staging/failed/unavailable, cross-account, write-like request reject | reads never change event sequence, selected head, V1/V2 counts, Git refs, or outbox |
| Storyboard | ordered typed parts build captures | missing requirements, invalid duration/state/slot/source, model handoff arrays reject | capture/validation partial failure creates no published/conversion head |
| Video conversion | exact Published Head creates one pending proposal | stale target, unresolved pending part/slot, mixed policy/duration/profile, manual arrays, cross-account reject | same request resolves same proposal; sink/event partial failure is reconciled without duplicates; acceptance remains unchanged |
| V2 projection | bootstrap/hydrate/realtime show identical state | stale cursor/scope/gap does not invent state or fall back to V1 | reconnect/restart reproduces exact state; duplicate events do not duplicate parts/rounds |
| Anti-derivation | dependency scan finds zero forbidden imports/calls/strings in V2 write packages | fixture adds each prohibited edge and gate fails | build cannot bypass the gate by aliases, generated adapters, build tags, reflection, or route flags |

All rejection tests assert both the error and absence of unauthorized/partial state change. Every stale-reference test uses two real revisions. Every cross-account test creates both owners. Every background worker test has bounded cancellation/restart evidence.

## 10. Single-authority cutover

### 10.1 Cutover conditions

Cutover occurs only when one reviewed revision proves:

1. all new managed creative writes enter `artifactv2.Service`;
2. the Designer executable schema contains only §6.1;
3. no V2 write package imports/calls/aliases a forbidden V1 dependency;
4. V3 registration exposes one V2 write route family and no V1 write route/action remains reachable;
5. Desktop sidebar and Artifact Studio consume V2 projections for new artifacts and never infer V2 state from V1 collections/variants;
6. every current V1 ready artifact remains readable through the read-only adapter;
7. that adapter cannot write, validate, select, publish, derive, convert, delete, or mutate selection/official refs;
8. Video Conversion creates one pending proposal without model-authored arrays and cannot accept/render it;
9. deterministic golden, provider-backed single-candidate, bounded three-candidate, pixel, reconnect, failure, and structural anti-derivation proofs pass at the exact revision; and
10. Atlas, public tool/agent contracts, prompts, route catalog, critical matrix, and test ledger are synchronized.

There is no dual-write or shadow-write period. Before cutover, V1 is current implementation and V2 routes are unavailable. At cutover, new write registration switches atomically to V2 and V1 write registrations are removed in the same change. Historical V1 reads remain available only through the adapter.

### 10.2 Deletion/quarantine map

| Current V1 surface | Cutover action |
|---|---|
| `manage_artifact` create/mutation/export action enum and handlers | delete from model/runtime registration; replace parent read needs with explicit legacy reader and V2 actions |
| `runtime_manage_artifact.go` write dispatch, parsers, create/package/publish/select/delete branches | delete write branches; do not retain dormant build-tag or feature-flag paths |
| `runtime_manage_artifact_parts.go`, `_derive.go`, `_capture.go`, `_animation.go` write/export paths | delete or move only independently allowlisted renderer-neutral helpers into neutral packages after audit; no runtime write entry remains |
| V1 managed `ArtifactRunContext` destination/publication contract in provider runtime | delete; replace with V2 capability grant context |
| parent collection/variant/step/candidate allocation in `service_task_launch.go`, `service_task_swarm.go`, `service_tools.go` | delete V1 destination/placeholder allocation; allocate Working Artifact/Iteration Round/candidate grants |
| `allocateManagedArtifactPlaceholders` and projection-only `Reserve` | delete; no placeholder compatibility |
| V1 managed Designer prompt instructions and `designer_animation_guidance.go` runtime-authoring protocol | delete from Designer prompts; replace with contextual schema and bounded creative/compiler guidance |
| one-round refinement/correction classifier in `service.go` | delete for V2; typed validation repair drives continuation |
| V1 partial-wave replacement classifier and suggested replacement action | delete for V2; V2 Iteration Round owns explicit candidate states/grants |
| V1 `artifact.Authority` write methods | remove from new-write wiring; after all non-V2 callers migrate, delete write authority. Do not leave V2 adapters calling it |
| V1 artifact mutation registrations and write preparation | remove public/new-write reachability. Historical records remain decodable read-only; mutation kinds reject new requests after cutover |
| V1 collection/variant/step/chain/part/composition write records | freeze as legacy decode structs in a read-only package or migration snapshot; no new record can be emitted |
| V1 official/candidate/transaction ref advancement | freeze historical repos; adapter reads exact ready bytes only and never opens write operations |
| session V1 artifact write handlers and selection endpoints | remove methods/routes or return explicit retired-write error with zero mutation; never forward to V2 |
| Desktop V1 create/select/part-selection/export controls | remove for new artifacts; legacy view offers preview/download/materialize only |
| V1 Artifact Studio inference for new artifacts | keep only in a clearly tagged legacy viewer; V2 has a separate direct projection model |
| `storyboard_handoff`, `manage_video import_storyboard` caller choreography | remove from AI/tool path; V2 conversion owns internal mapping |
| `propose_html_iteration` manual candidate/fallback arrays and selected-derivative choreography for Artifact V2 | remove from Artifact V2 workflow; retain unrelated legacy Video Studio compatibility only until its own migration, never callable by V2 adapter |
| stale HTML contract checkpoint docs | mark superseded for managed V2 authoring; retain only as V1 historical evidence where necessary |
| checked-in V1 provider runner `designer-artifact-flow` | retire as V1 gate; replace with V2 golden/provider journeys before cutover |

### 10.3 Legacy V1 ready adapter

`artifactv2.LegacyReadyReader` is the only permitted post-cutover V1 dependency. Its interface is structurally read-only:

```text
interface LegacyReadyReader {
  ListReady(ctx, principal, page) -> LegacyReadyPage
  GetReady(ctx, principal, exactV1Ref) -> LegacyReadyMetadata
  ReadReady(ctx, principal, exactV1Ref, byteBound) -> bytes
  ReadPackageEntry(ctx, principal, exactV1Ref, entry, byteBound) -> bytes
  IssuePreview(ctx, principal, exactV1Ref) -> expiring GET-only ticket
  MaterializeReady(ctx, principal, exactV1Ref, containedDestination, overwrite=false)
}
```

Rules:

- accepts only exact historical records whose durable V1 status is `ready`;
- never returns a reference accepted by a V2 write command;
- results are tagged `legacy_v1_readonly` in Desktop;
- no create/update/finalize/fail/unavailable/select/delete/derive/export/convert method exists;
- no generic `ApplySessionMutation`, V1 `artifact.Authority`, tool runtime, Video Studio service, or writable `artifactgit.Repository` handle is reachable;
- materialization is a contained filesystem read-out and cannot publish/import into V2;
- read calls record ordinary access telemetry only if that telemetry is outside artifact/session mutation state; they must not change V1/V2 event sequence, selection, official ref, outbox, or catalog content.

Structural tests inspect imports, interface method sets, route registration, dependency graph, and a spy store that fails if any mutation/write method is called. Behavior tests snapshot all V1/V2 records, outbox entries, Git refs, and destination state before rejected calls and prove exact equality afterward.

## 11. Implementation checkpoint entry gate

Before any later implementation checkpoint writes code, its executing agent must:

1. cite this constitution as the authority;
2. state the V2 invariant and likely attack/regression for its scope;
3. list the V1 paths inspected only for deletion/quarantine or primitive audit;
4. list every retained primitive interface used;
5. run the structural dependency check for its V2 scope;
6. provide positive plus no-state-change negative tests at the narrowest layer; and
7. redesign rather than request an exception if a missing V2 interface is discovered.

A review that finds any forbidden dependency returns the implementation for redesign. Removing the obvious import while retaining copied state/schema/control flow does not satisfy the gate.

## 12. Required proof before launch

The final cutover revision must include:

- deterministic golden journey from Working Artifact allocation through V2 publication and pending Video Studio proposal;
- two provider-backed single-candidate Designer journeys using only `artifact_v2_author`;
- one bounded three-candidate Iteration Round with exact target preservation, one selection, and lock proof;
- repair journey from invalid validation to a derived Part Revision without changing preserved parts;
- daemon restart/reconnect proof for working/build/validation/round/publication projections;
- rendered-pixel inspection of every claimed state/frame, including clipping, overflow, sizing, text legibility, overlaps, scrollbars, capture chrome, and brief fidelity;
- focused Desktop interaction proof for immediate sidebar visibility, real part navigation, diagnostics, candidate comparison, selection, locks, publication, and conversion;
- cross-account, stale-reference, idempotency, concurrency, cancellation, restart, and injected-failure tests for every §9 boundary;
- structural proof of zero V1 managed-write registration/dependency and a behavior proof that legacy reads cause zero mutations; and
- synchronized Atlas, test audit ledger, curated gate decisions, public prompt/tool contracts, and deletion evidence at the exact candidate revision.

## 13. Relevant implementation evidence inspected for this freeze

Current code was inspected only to define quarantine, retained primitives, and attack points:

- `swarmd/internal/tool/runtime_manage_artifact.go`
- `swarmd/internal/tool/runtime_manage_artifact_parts.go`
- `swarmd/internal/tool/runtime_manage_artifact_derive.go`
- `swarmd/internal/tool/runtime_manage_artifact_capture.go`
- `swarmd/internal/tool/runtime_manage_artifact_animation.go`
- `swarmd/internal/artifact/authority.go`
- `swarmd/internal/artifact/authority_git.go`
- `swarmd/internal/artifact/authority_parts*.go`
- `swarmd/internal/store/pebble/session_artifact_store.go`
- `swarmd/internal/store/pebble/session_artifact_parts.go`
- `swarmd/internal/store/pebble/session_event_store.go`
- `swarmd/internal/run/service_tools.go`
- `swarmd/internal/run/service_task_launch.go`
- `swarmd/internal/run/service_task_swarm.go`
- `swarmd/internal/run/service.go`
- `swarmd/internal/run/designer_animation_guidance.go`
- `swarmd/internal/htmlcapture/renderer.go`
- `swarmd/internal/htmlcapture/animation.go`
- `swarmd/internal/api/sessions_v3_artifacts*.go`
- `swarmd/internal/tool/runtime_manage_video.go`
- `swarmd/internal/tool/runtime_manage_video_storyboard.go`
- `swarmd/internal/videoproject/service.go`
- `web/src/features/desktop/session-v3/artifact-api.ts`
- `web/src/features/desktop/session-v3/artifact-studio-model.ts`
- `web/src/features/desktop/chat/components/desktop-v3-artifact-sidebar.tsx`
- `web/src/features/desktop/chat/components/desktop-v3-artifact-gallery.tsx`
- `docs/swarm-atlas.md`

This evidence does not make any V1 write component an implementation baseline or allowlisted V2 dependency.

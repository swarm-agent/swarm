# Retained artifact tool/runtime reuse

## Status and authority

Implementation is based on `f217ba29f`, with integrated commits through `9144bdac8` plus the reviewed working changes. This is **not an installed-runtime or passing-test claim**. Go formatting and the atlas/whitespace checks are recorded separately; integration tests have not run because the required environment lease cannot be obtained: `ensure` reports `environment_id is required for ensure (no workspace default test environment configured)`, and environment/connection lists are empty. No lease was acquired, host rebuilt, runtime restarted, source artifact modified, or final video rendered.

The provider-facing `Runtime.Definitions` and `executeManageArtifact` expose the contract below. Native dispatch uses `artifactV3RuntimeAdapter` → `ArtifactV3Service.SearchCatalog/ResolveRetainedSource/ReadRetainedRevision/Import`. Legacy import uses the existing `artifact.Authority.Import`; it does not reconstruct legacy HTML as a native artifact. Destination owner, IDs, transaction identity and lineage are server-derived. Imported native heads inherit verified source evidence, not a newly rendered preview.

The running daemon must receive this code through a separately authorized update before these newly registered actions are usable. Do not work around an old tool schema using raw storage, forged references, source selection, or HTML regeneration.

## Recover an original 42-second HTML animation

1. Discover within the authenticated account/user. For native artifacts use bounded `list_v3`, optionally filtered by known source session/artifact, creation range, or metadata query. Query searches IDs, intent and Part labels, **not arbitrary HTML titles/content**. For legacy artifacts use `search` with `library=legacy`. Follow `next_cursor` unchanged even when the result page is empty; retain the same filters. Do not scan transcripts or private storage directories.
2. Copy one complete exact ready reference from the returned result. Head, ready unselected candidate and historical revision are distinct candidates. Do not infer the original from current head, title, list order or approximate duration. Ask for selection if multiple originals remain plausible.
3. Read that exact source. Check the original entrypoint, complete project/asset bytes, Part IDs/locators, animation profile, `duration_ms: 42000`, original FPS and ready/seek contract. Preserve the original code and assets; do not create a creative remaster or change the soundtrack/timing. Duration alone is not proof of identity. No private original reference is embedded in these examples.
4. Import the exact reference, then read the **returned destination reference**. Compare every `files_base64` entry and manifest/Parts for native projects (and exact package bytes/digests for legacy). The destination commit differs; project tree content remains the same. Source selection and source turns must not move.
5. Stop after recovery if that is the request. Later edits use only the destination reference with `begin_v3`/`revise_v3`. For bounded multi-file edits use `begin_v3`, copy its `draft_handle`, then `author_v3` operations; rebuild and finish the destination. Native candidate selection still needs its distinct approval. Studio conversion creates a pending proposal; user acceptance and final rendering remain separate boundaries.

The following are **tool argument templates**, not shell commands. Replace placeholders only with fields returned by authenticated discovery; do not invent identities.

### Discovery

<copy label="Discover native retained artifacts">{"action":"list_v3","limit":20}</copy>

<copy label="Discover legacy retained HTML">{"action":"search","library":"legacy","media_type":"text/html","limit":20}</copy>

A native alias is `search` with `library=native`. Omit `source_kind` to include candidates/history, or use `head`, `candidate`, or `historical_revision` explicitly. Native status accepts `ready` or `selected`; legacy status uses its existing vocabulary. Archives are readable without reactivation; deleted or foreign-principal sources are rejected.

### Native exact read and import

<copy label="Read exact native original">{"action":"read_v3","artifact_v3_reference":{"session_id":"SOURCE_SESSION","artifact_id":"SOURCE_ARTIFACT","revision_ref":"EXACT_REVISION_REF_FROM_DISCOVERY"}}</copy>

<copy label="Import independent native copy">{"action":"import","artifact_v3_reference":{"session_id":"SOURCE_SESSION","artifact_id":"SOURCE_ARTIFACT","revision_ref":"EXACT_REVISION_REF_FROM_DISCOVERY"}}</copy>

Native revision references have `revision-` plus an exact lowercase 40-character hexadecimal Git OID. `read_v3` returns complete bounded project content; it rejects `max_bytes` and over-limit projects rather than pretending truncated bytes are complete. `source_v3` accepts the same nested reference for retained resolution; current-session discovery by `artifact_id` preserves explicit selection/recovery guidance.

For later authorized editing, using **destination** fields returned by import:

<copy label="Begin destination-only revision">{"action":"begin_v3","artifact_v3_reference":{"session_id":"DESTINATION_SESSION","artifact_id":"IMPORTED_ARTIFACT","revision_ref":"EXACT_IMPORTED_REVISION_REF"},"revision_intent":"whole_project"}</copy>

Choose `focused_parts` plus exact `target_part_ids` instead when only declared regions may change. Never pass a source session to begin/revise as a substitute for import.

### Legacy exact read and import

Legacy read keeps four identity fields at top level. The sequence below uses `1` only as an example: copy the actual positive `event_seq` returned by discovery.

<copy label="Read exact legacy original">{"action":"read","session_id":"SOURCE_SESSION","collection_id":"SOURCE_COLLECTION","variant_id":"SOURCE_VARIANT","event_seq":1}</copy>

<copy label="Import independent legacy copy">{"action":"import","artifact_reference":{"session_id":"SOURCE_SESSION","collection_id":"SOURCE_COLLECTION","variant_id":"SOURCE_VARIANT","event_seq":1}}</copy>

Use only one nested discriminator for import. Native and legacy fields cannot be mixed. Caller-supplied destination IDs, ownership, request IDs, evidence, provider or model are rejected. Oversized legacy reads can use exact-reference materialization when a workspace copy is actually required; materialization alone is not native import/edit authority.

## Requirement-first validation inventory

- `runtime_manage_artifact_retained_test.go`: actual registered tool schema; strict reference/identity rejection with zero importer calls; native/legacy dispatch; retained read; opaque empty-page continuation; incomplete projection cannot advertise ready. Recording fakes do **not** establish persistence.
- `retained_artifact_schema_test.go`: actual registration through the Codex schema transformation retains action and nested fields. No provider request.
- `artifact_v3_approval_test.go`: read mode denies import while discovery/read stay allowed; native select/resume retain their distinct permission identities.
- `artifact_v3_retained_reuse_test.go`: actual dispatch, canonical temporary Git/Pebble services and runtime adapters; archived/deleted/foreign sources, exact read/import/edit, historical/unselected fidelity, stable retry/conflict, destination no-write rejection, inherited API/evidence integrity and source-deletion independence. Studio bridge asserts a pending, not accepted, proposal and preserved timing. Capture and conversion renderers are explicitly fake, not decodable visual evidence.
- `artifact-v3-retained.spec.ts`: canonical nested owner provenance survives Desktop normalization without replacing destination identity. No browser/pixel claim.
- Existing `artifact_v3_import_atomic_test.go` and `artifact_v3_reuse_test.go` own real scan-budget empty continuation, asset/timing fidelity, concurrent retry and reopened-store failure recovery. Their earlier foundation results do not validate the new tool/runtime changes.

Source review corrected non-existent flattened projection field access, incompatible retained/local Part types and a missing import; compile-time interface assertions now pin the adapter seams. It also corrected negative tests that previously rejected incomplete references before reaching source authorization, and added exact edited-byte and historical-import assertions. Execution is still required to find any further defects. No new test is promoted into the curated critical runner; independent first/second-pass review remains pending.

### Exact pending commands

Run inside the approved leased environment against the exact committed or explicitly captured working-tree snapshot. Keep concurrency bounded; do not replace these with whole-module suites. Any live end-to-end extension must use GCP. These commands were **not executed** in this checkpoint.

<copy label="Focused tool dispatch tests">cd swarmd && GOMAXPROCS=1 go test -p 1 -parallel 1 ./internal/tool -run '^(TestManageArtifactDefinitionExposesImportAndNestedReferences|TestManageArtifactImportValidationRejectsBeforeWrites|TestManageArtifactNativeImportSuccess|TestManageArtifactLegacyImportSuccess|TestManageArtifactReadV3RetainedCrossSession|TestManageArtifactListV3NativeCatalogPaging|TestManageArtifactReviseRejectsRetainedSessionWithImportGuidance|TestManageArtifactImportRejectsForgedIdentity|TestManageArtifactImportRejectsIncompleteProjection|TestNativeHandoffDiscoveryTuple|TestNativeHandoffExplicitStaleSelection|TestManageArtifactReviseV3CreatesExactBaseCandidateWithoutSelecting|TestManageArtifactReviseV3DefaultIdentity)$' -count=2 -timeout=120s</copy>

<copy label="Focused runtime integration tests">cd swarmd && GOMAXPROCS=1 go test -p 1 -parallel 1 ./internal/runtime -run '^(TestRetainedArtifactToolDiscovery|TestRetainedArtifactToolExactRead|TestRetainedArtifactIndependentImportAndEdit|TestRetainedArtifactDownstreamVideoStudioConversion|TestRetainedArtifactToolLegacyDiscrimination|TestRetainedArtifactLegacyImportActualDispatch|TestRetainedArtifactInheritedEvidenceAndIntegrity|TestRetainedArtifactUnselectedAndHistoricalDispatch|TestRetainedArtifactImportDeniedWithoutDestinationWrites)$' -count=2 -timeout=180s</copy>

<copy label="Focused permission and provider-schema tests">cd swarmd && GOMAXPROCS=1 go test -p 1 -parallel 1 ./internal/permission ./internal/provider/codex -run '^(TestArtifactV3ExactActionApproval|TestArtifactV3ExactActionRules|TestArtifactImportAndDiscoveryPolicyClassification|TestCodexRetainedArtifactRegisteredSchema)$' -count=2 -timeout=120s</copy>

<copy label="Focused retained authority regression tests">cd swarmd && GOMAXPROCS=1 go test -p 1 -parallel 1 ./internal/store/pebble -run '^(TestArtifactV3ImportCrashRecoveryAndConflict|TestArtifactV3CatalogScanBudgetAndPrincipalCursor|TestArtifactV3ImportConcurrentReplay|TestArtifactV3ImportRetainedRejections|TestArtifactV3ImportCreatesFinalizedEditableReadyHead|TestArtifactV3DiscoveryAndCatalogSearch|TestArtifactV3ResolveRetainedSource|TestArtifactV3RepositoryReadProjectLimitsAndIntegrity)$' -count=2 -timeout=180s</copy>

<copy label="Focused Desktop provenance normalization">cd web && node --import tsx --test --test-concurrency=1 --test-timeout=30000 src/features/desktop/session-v3/artifact-v3-retained.spec.ts</copy>

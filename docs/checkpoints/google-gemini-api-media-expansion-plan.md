# Follow-on plan: Google Gemini API-key media input

Status: approval-ready proposal only. No Google media implementation is authorized or started by this document.

## Gate evidence and selection

The durable staged plan entered its provider-expansion gate only after the OpenAI API-key Responses and Codex OAuth/client slice reached its explicit approval boundary. The accepted pilot contract and validation matrix are recorded in `docs/checkpoints/openai-codex-session-media-contract.md`.

The OpenAI/Codex implementation established the reusable architecture:

- hydrated Pebble catalog records with snapshot provenance and structural media vocabulary;
- one fail-closed `SessionMediaContract` consumed by schema, prompts, invocation admission, capability projection, and lineage;
- immutable account/session-scoped, content-addressed V3 assets and durable message references;
- independently compiled primary and child-agent contracts;
- backend-driven Desktop capability projection and submit-time revalidation;
- provider-specific declarations and payload mapping behind the shared contract;
- hostile-input, replay, cleanup, and cross-surface denial gates.

The following pilot assumptions are provider-specific and must not become generic defaults:

- the current catalog media mapper admits only `openai` and `codex` and assigns their provider/credential surfaces;
- runtime and V3 media admission explicitly allow only those two provider IDs;
- adapter IDs, credential predicates, and transport validators are surface-specific;
- OpenAI/Codex payload content types (`input_image` and `input_file`) are not a portable provider representation;
- Google currently constructs text/tool-only `generateContent` parts and has no media adapter declaration or contract-aware inline-data mapping.

### Selected next surface

Propose exactly one next surface: **Google Gemini Developer API `generateContent` / `streamGenerateContent` using the existing Google API-key credential path**.

Evidence:

- The embedded snapshot marks Google `multimodal_verified`, contains 56 Google models, and carries exact-model modality facts backed by first-party Models API and model/media documentation. Forty-one records currently affirm image input, while unknown and non-image vocabulary remains structurally distinguishable.
- `swarmd/internal/provider/google/adapter.go` already resolves one explicit Google API-key credential surface.
- `swarmd/internal/provider/google/runner.go` already owns first-party Gemini `generateContent` and streaming request transport with stateless full-input replay, making the adapter boundary and focused request tests clear.
- The required transport addition is independently testable: contract-authorized immutable image bytes become Google `inlineData` parts. No URL, local path, Files API upload, Vertex surface, Live API, generated media, audio, video, or generic document behavior is implied.

Google is selected over Anthropic, Fireworks, and OpenRouter for this next review because its catalog is explicitly multimodal-verified and its existing first-party API-key runner has a single inspectable request surface. Those unselected providers remain undeclared and text-only.

## Scope and invariants

Goal: admit immutable V3 **image input only** on the Google Gemini Developer API-key `generateContent` surface while reusing the accepted OpenAI/Codex architecture unchanged.

The exact initial surface identifiers must be introduced as Google-specific constants and reviewed against current first-party transport behavior. A suggested naming set is:

- provider: `google`;
- provider surface: `gemini_generate_content`;
- credential surface: `google_api_key`;
- adapter: `google-generate-content-v1`.

The implementation must remain fail-closed:

- only exact catalog-supported image MIME types intersected with the adapter declaration may be admitted;
- catalog unknown, unsupported, contradictory, missing-provenance, alias-ambiguous, or output-only facts grant nothing;
- PDF/file, audio, video, image generation, Files API, URI fetch, Live API, Vertex AI, OAuth, and service-account surfaces remain denied;
- OpenAI, Codex, or Google contracts/assets/continuations must not cross provider or credential surfaces;
- every other provider remains undeclared and text-only;
- the shared immutable V3 asset, projection, replay, tool, Desktop, and lineage architecture is extended, not forked or redesigned.

## Proposed checkpoints

### G1 — Normalize Google catalog authority

Tasks:

1. Extend snapshot-to-`ModelCatalogRecord` media normalization for the single Google Gemini Developer API surface without removing the OpenAI/Codex mappings.
2. Preserve exact-model supported/unknown/unsupported states, MIME vocabulary, source IDs, snapshot identity, and native processing semantics.
3. Map only image input for this slice; retain all other Google vocabulary structurally but deny it at runtime.
4. Add focused catalog tests for supported image models, unknown aliases, non-image models, contradictions, stale valid hydrated snapshots, and absent provenance.

Acceptance criteria:

- A valid hydrated Google exact-model record can carry image media facts and the reviewed Google provider/credential surfaces.
- Unknown or contradictory Google records compile no admitted capability.
- Refresh failure does not replace a valid hydrated snapshot with embedded defaults.
- OpenAI/Codex catalog behavior remains unchanged and all other providers still produce no runtime media record.

Focused validation gate:

- Run only named Google catalog parsing/provenance tests plus the existing named OpenAI/Codex catalog regression tests.

### G2 — Declare and enforce the Google adapter surface

Tasks:

1. Add a Google-specific `MediaCapabilityDeclaration` bound to the existing active API-key credential, using a non-secret fingerprint.
2. Narrow the declaration to native images and exact MIME/size/count ceilings supported by inline `generateContent` transport.
3. Replace the hard-coded two-provider pilot checks with an explicit reviewed surface registry or equivalent canonical allowlist that contains OpenAI Responses, Codex OAuth/client, and only this Google surface.
4. Retain cross-surface checks at compiler, upload, tool invocation, payload assembly, and continuation boundaries.

Acceptance criteria:

- Google image capability survives the compiler only when catalog, snapshot, adapter, credential, agent, mode, workspace, and session all agree.
- Missing/rotated credentials, mismatched adapter IDs, forged surfaces, denied profiles, utility runs, and unsupported modes produce a text-only contract.
- OpenAI/Codex declarations cannot authorize Google and Google cannot authorize either pilot surface.
- Anthropic, Fireworks, OpenRouter, and every other provider remain undeclared and text-only.

Focused validation gate:

- Run named compiler intersection/hash/lineage tests, Google declaration tests, every-provider-denied tests, and three-way cross-surface denial tests.

### G3 — Map immutable images to Gemini request parts

Tasks:

1. Extend the Google request model with the exact first-party inline image part shape and base64 encoding at the transport boundary only.
2. Reconstruct bytes solely from validated immutable V3 assets already carried in `SessionMediaPayload`; never accept frontend URLs, provider URLs, or local paths.
3. Revalidate digest, size, MIME, count, contract hash, provider surface, credential surface, and adapter ID immediately before request construction.
4. Apply the same mapping to non-streaming and streaming `generateContent` requests while preserving stateless replay ordering.
5. Reject PDF/file, audio, video, generated media, URI/Files API parts, stale contracts, and OpenAI/Codex payload shapes.

Acceptance criteria:

- A valid authorized image produces the expected Google inline-data request part in deterministic message order.
- Raw bytes remain excluded from diagnostics, realtime, exports, tool results, and persisted V3 message events.
- Forged identity/type/size/digest, stale contract, excessive count, and cross-surface payloads fail before network dispatch.
- Text-only Google requests and existing OpenAI/Codex requests retain their current behavior.

Focused validation gate:

- Run named Google request mapping and streaming parity tests, hostile-payload tests, diagnostics redaction tests, and targeted OpenAI/Codex transport regression tests.

### G4 — Prove shared runtime, V3, child, Desktop, and replay behavior

Tasks:

1. Verify the existing shared compiler hydrates Google contracts for primary Swarm and independently for Finder/Coder/Designer/custom children after profile and preference reconciliation.
2. Verify `media_inspect` schema and instructions are narrowed from the Google contract and omitted when denied.
3. Verify V3 upload, mutation, event/projection, sync/realtime, replay-after-restart, continuation, deletion cleanup, search/privacy, and per-account/session ownership use the unchanged generic asset/reference path.
4. Verify Desktop reads the backend projection, offers only admitted image types, sends the current contract token, and handles capability changes without interpreting snapshot JSON.
5. Verify Google snapshot, credential, model, agent, or admission changes alter provider configuration/lineage and force a fresh chain.

Acceptance criteria:

- Primary and child Google runs independently receive only their effective contract and tool schema.
- Durable replay reconstructs Google image input from immutable references without mutable paths or URLs.
- Desktop affordances exactly match backend projection and stale submissions are rejected.
- Provider/model/credential/snapshot/agent capability changes invalidate continuation identity.
- OpenAI/Codex behavior remains intact and unselected providers remain text-only.

Focused validation gate:

- Run named primary/child assembly, tool omission/narrowing, V3 mutation/replay/ownership/cleanup, Desktop projection/composer, lineage, and non-provider-leakage tests.

### G5 — Focused end-to-end review gate

Tasks:

1. Execute the narrow named gates from G1–G4; do not substitute broad repository suites without separate permission.
2. Record exact commands, results, changed files, unresolved risks, and negative coverage in a Google-specific acceptance report.
3. Inspect the final diff for provider leakage, duplicate architecture, raw-byte/path/URL exposure, unsafe fallback, and accidental enablement of non-image Google modalities.
4. Pause for explicit review before declaring Google enabled or proposing any later provider.

Acceptance criteria:

- Every named Google positive and hostile-input gate passes, along with targeted OpenAI/Codex regressions.
- The acceptance report proves catalog provenance, fail-closed compilation, immutable V3 replay, tool hydration, primary/child parity, Desktop projection, lineage, transport mapping, security, privacy, cleanup, and three-surface isolation.
- No provider other than OpenAI, Codex, and the reviewed Google API-key `generateContent` surface is media-enabled.
- Any later provider requires a new single-provider reviewed plan; approval of this proposal grants no authority for it.

## Review decision requested

Approve or reject only this Google Gemini Developer API-key image-input proposal. Approval should create a separately executed plan from G1 through G5; it must not mutate the completed OpenAI/Codex plan, start implementation automatically from this planning gate, or bundle another provider.

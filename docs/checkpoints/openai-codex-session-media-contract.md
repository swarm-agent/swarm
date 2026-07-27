# OpenAI/Codex V3 session media contract

Status: pilot acceptance contract. This document does not authorize any additional provider.

## Runtime authority

The runtime compiles one `SessionMediaContract` for every provider request. Its inputs are the active model/provider, the latest successfully hydrated Pebble catalog record and snapshot provenance, the adapter declaration for the active credential surface, the reconciled agent profile, execution mode, workspace, and session. Missing, unknown, contradictory, or cross-surface facts produce a text-only contract.

The embedded model snapshot is seed/offline cache data. A valid hydrated snapshot remains authoritative when refresh is unavailable. Snapshot records preserve structural media state, modality, MIME/file vocabulary, processing semantics, provider surface, credential surface, and source IDs.

The media contract hash is included in provider configuration lineage. The current hash policy includes resolved provider/model, adapter and credential fingerprint, agent/mode/scope, effective capabilities/instructions/schema, and snapshot identity. Consequently a provenance snapshot identity change starts a fresh provider lineage even when its effective media vocabulary is otherwise equivalent; mere reordering of normalized vocabulary is stable.

## Authorized pilot surfaces

| Provider | Provider surface | Credential surface | Adapter | Admitted session input |
| --- | --- | --- | --- | --- |
| OpenAI | `responses_api` | `openai_api_key` | `openai-responses-v1` | Native GIF/JPEG/PNG/WebP images and provider-processed PDFs, further narrowed by the hydrated catalog |
| Codex | `chatgpt_codex` | `codex_oauth` | `codex-chatgpt-v1` | Model-native images whose exact MIME vocabulary is supplied by the hydrated catalog; documents stay denied |

System Swarm and conversational Finder, Coder, and Designer profiles explicitly authorize `media_inspect`. Saved/custom profiles must explicitly enable it. Utility or tool-free runs remain media-tool-free. Child sessions compile their own contract after child profile and preference reconciliation; no parent capability is copied as authority.

All providers other than OpenAI and Codex are text-only. Cross-use of an OpenAI catalog, API-key contract, adapter, asset, or continuation on Codex is denied, and the inverse is denied. Audio transcription, image generation, video, local-path ingestion, public/expiring URLs, and undeclared client processing are unsupported.

## V3 API and durable storage

Desktop reads backend projections from V3 sync `session_view.media_capability` or `GET /v3/sessions/{id}/media-capability`. The projection includes contract status/token, provider/model, adapter and credential/provider surfaces, snapshot provenance, sanitized denial reasons, and the exact admitted modality/type/limit set. Desktop does not parse raw catalog JSON.

`POST /v3/sessions/{id}/media` requires:

- `X-Swarm-Media-Contract`: the current contract token;
- `X-Swarm-Media-Modality`: an admitted modality;
- `X-Swarm-Media-File-Type` when the contract declares one;
- an exact admitted `Content-Type`.

The backend bounds the body before reading it, detects MIME from bytes, enforces per-capability/session quotas, and writes immutable account/session-scoped metadata plus blob bytes under separate Pebble prefixes. Identity is content-addressed by digest and admission contract. No filesystem path or frontend URL is persisted.

A V3 user message carries ordered `media` references containing only asset ID, modality/type, size, digest, and contract token. `ApplyV3SessionMutation` verifies ownership and immutable metadata, increments references atomically, and emits durable events/projections/realtime records containing references only. Provider payload reconstruction reads bytes internally and revalidates the current contract. Search indexes message text, not media bytes or digests. Diagnostics, realtime, and tool results never include raw bytes, paths, URLs, or credentials.

Session deletion purges both media metadata and blob prefixes in the same content purge as messages/events. Archived sessions retain assets because they remain restorable. Referenced assets cannot be independently removed; unreferenced uploads can be deleted. Quotas bound growth, and ordinary Pebble maintenance only reports/compacts the existing logical data.

## Dynamic tool and continuation behavior

`media_inspect` is the sole media tool. The static profile authorization placeholder is removed and replaced per request with a schema whose enums contain only admitted modalities, MIME types, file types, and current contract hash. The tool is omitted when the intersection is empty or the agent is unauthorized. Invocation revalidates the current contract, authenticated account/session ownership, immutable digest/type/size, and current admission before provider payload assembly.

Provider configuration and lineage change when model, credential fingerprint/surface, adapter surface, snapshot identity, agent authorization, effective schema/instructions, or admission changes. Replay after daemon restart reconstructs provider input from durable message references and immutable blobs. A stale upload, forged tool call/reference, or continuation from the other pilot surface is rejected rather than downgraded through a fallback.

## Focused acceptance matrix

The pilot gate covers catalog parsing/provenance, contract intersection and hashes, tool narrowing/omission, immutable storage and V3 replay, MIME/size/ownership hostility, OpenAI and Codex payload mapping, cross-surface denial, Desktop projection/reference submission, non-pilot denial, search/privacy, and deletion cleanup. Broader provider work requires a separate reviewed plan amendment or follow-on plan, one provider surface at a time.

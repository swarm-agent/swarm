# Provider-neutral moment-level video understanding plan

Status: execution-ready design only. This document changes no production behavior.

## Decision

The current Google integration uses the correct Gemini Developer API path: upload the video with the Files API, wait for the file to become `ACTIVE`, then send the file to `models.generateContent`. Gemini natively processes the video's visual and embedded audio streams.

The important distinction is:

- Google samples video visuals at **1 frame per second by default**, processes audio, and adds timestamps every second.
- That gives the model second-addressable input, but it does **not** guarantee one faithful output record for every second.
- Swarm's current prompt asks for chronological multimodal segments but does not demand fixed one-second records. Gemini therefore compressed the 3:03 test video into five 29–40 second semantic segments.
- The current request does not send `videoMetadata`, so it uses Google's default 1 FPS sampling.
- Google's API supports a custom `videoMetadata.fps`, plus start/end offsets. A higher FPS can improve fast-action analysis, at higher token cost and latency.

Therefore, Google did provide real multimodal video understanding; the successful result was not merely an audio transcription. But the current one-shot generation path is a coarse model-authored summary timeline, not a durable second-by-second observation index.

We should retain Gemini as one optional adapter, while moving sampling, chunking, retries, normalization, and indexing into provider-neutral Swarm orchestration.

## Evidence

Current Swarm behavior:

- `swarmd/internal/provider/google/video_transcription.go` uploads the complete video through Google's Files API and calls `v1beta/models/{model}:generateContent` with one `file_data` video part and one prompt.
- The request omits `videoMetadata`; Google therefore chooses its default video sampling.
- `swarmd/internal/videotranscription/service.go` currently resolves only a Google model and wires a single Google adapter.
- `video_timeline_prompt.v2` requests `speech`, `audio`, `visual`, and `on_screen_text`, but allows the model to choose segment boundaries.
- `normalized_transcript.v2` can store multimodal timestamped segments, but does not represent source observations, chunk attempts, per-second coverage, confidence, or hierarchical navigation.
- The durable `ycfinalwithaudio.mp4` result contains visual descriptions and OCR as well as speech, proving Gemini used both modalities. It contains only five segments, proving that input sampling and output granularity are different contracts.

Official Google behavior, checked 2026-08-15:

- [Gemini video understanding](https://ai.google.dev/gemini-api/docs/generate-content/video-understanding): Gemini processes audio and visual streams; visual descriptions default to 1 FPS; custom FPS and clip offsets are supported through `videoMetadata`; Google warns that rapid changes may be missed at 1 FPS.
- The same guide states that File API processing uses 1 FPS, single-channel audio at 1 Kbps, and timestamps every second. Approximate default input cost is about 300 tokens per video second on the documented pre-Gemini-3 accounting, with model-specific media-resolution differences.
- [Gemini image understanding](https://ai.google.dev/gemini-api/docs/generate-content/image-understanding): multiple image `Part` objects are supported in one prompt, mixing inline bytes and File API references. The documented ceiling is 3,600 image files per request, but practical batches must be much smaller for reliable structured output and portable provider support.
- [Gemini Batch API](https://ai.google.dev/gemini-api/docs/batch-api): asynchronous multimodal batches are supported at reduced cost, with a target turnaround of up to 24 hours. This is suitable for non-urgent backfills, not interactive video navigation.
- [Gemini rate limits](https://ai.google.dev/gemini-api/docs/rate-limits): limits are enforced per project across RPM, input TPM, RPD, and spend; Batch API has separate concurrent-job and enqueued-token limits. Exact active limits vary by model and tier.

Google also offers Cloud Video Intelligence, which can return specialized shot, object, OCR, label, and speech annotations. It is not an out-of-the-box replacement for a coherent, generative, second-by-second narrative timeline, and adopting it would deepen Google-specific orchestration rather than solve provider portability.

## Product contract

A completed video understanding job must produce two related views:

1. **Atomic observation timeline** — navigable fixed-duration buckets, normally one second, preserving what was observed and what is unknown.
2. **Semantic event timeline** — model-merged spans and chapters for efficient reading and retrieval, always linked back to atomic observations.

Each atomic observation should contain:

- absolute `start_ms` and `end_ms`;
- visual action/state description;
- OCR/on-screen text;
- overlapping speech text with word or utterance timestamps when available;
- non-speech audio events;
- source observation references (frame timestamp or native-video range);
- provider, model snapshot, adapter version, prompt/schema version, and attempt provenance;
- confidence/quality flags and an explicit `unknown`/`not_observed` state;
- content digest and validation state.

“One record per second” must mean timeline coverage, not a claim that every millisecond was visually inspected. At 1 FPS, sub-second events can still be missed. Fast-motion presets must sample more densely or add change-triggered frames.

## Architecture

### 1. Provider-neutral orchestration

Replace the Google-owned service flow with a `videounderstanding` coordinator that owns:

- media probing and source fingerprint validation;
- analysis profile selection;
- chunk and observation manifests;
- durable work-item state and idempotency;
- provider/model capability selection;
- rate limiting, retries, cancellation, and resume;
- deterministic merge, coverage validation, and final indexing.

Google must not decide chunk boundaries, durable schema, job lifecycle, or navigation format.

### 2. Separate adapter capabilities

Use capability-specific interfaces rather than one Google-shaped `Transcribe` method:

- `NativeVideoAnalyzer.AnalyzeRange(video, start, end, fps, schema)` — for Gemini-like models that accept video plus audio.
- `FrameBatchAnalyzer.AnalyzeFrames(timestampedImages, optionalContext, schema)` — portable fallback for models that accept multiple images.
- `SpeechTranscriber.TranscribeAudio(audio, languageHints)` — word/utterance timestamps where supported.
- Optional `AudioEventAnalyzer.AnalyzeAudioRange` — non-speech sound classification when the speech adapter does not provide it.

An adapter declares supported input methods, maximum media/request sizes, image count, context/output limits, custom-FPS support, structured-output support, and batch support. Provider catalogs and account model settings select adapters; orchestration fails clearly when no valid combination exists.

A native multimodal adapter may produce both visual and audio observations in one pass. The coordinator still keeps visual and speech tracks logically separate so a stronger ASR adapter can replace or verify the native speech output.

### 3. Local preprocessing

Introduce a bounded media-probe/extraction boundary:

- inspect duration, streams, frame rate, keyframes, rotation, and audio presence;
- extract a normalized audio track only when the selected pipeline requires it;
- extract timestamped JPEG/WebP observations at the selected cadence;
- optionally run cheap local frame-difference/scene-change detection to add observations around visual changes;
- keep extracted media in managed private job storage with retention and cleanup, never in tracked repository paths;
- validate source fingerprint before every resume.

Use a maintained executable adapter (initially `ffprobe`/`ffmpeg` if present and explicitly packaged) behind a narrow interface. Startup or job admission must report a missing required executable instead of silently changing quality.

### 4. Adaptive analysis profiles

Provide explicit profiles rather than claiming one universal cadence:

- `economy`: 0.2–0.5 FPS, scene-change additions, suitable for long/static lectures.
- `standard`: 1 FPS plus scene-change additions; default for demos and ordinary video.
- `detailed`: 2 FPS plus change-triggered frames and higher visual resolution for UI/OCR.
- `motion`: 4–5 FPS or native custom FPS for short fast-action clips, with strict duration/token warnings.

For a screen recording, `detailed` should be the recommended profile because transient UI state and small text matter. Regardless of sampling profile, normalize the result into one-second coverage buckets; multiple observations may support one bucket.

### 5. Chunking and overlap

Do not send one request per frame and do not make one unbounded request for a long video.

Initial interactive defaults:

- native-video ranges: 30–60 seconds each;
- frame batches: 15–30 seconds per request, normally 15–30 images at 1 FPS;
- overlap: 2 seconds at each boundary;
- per-provider concurrency: start at 2, permit 4 only when recent quota/latency evidence is healthy;
- global account concurrency: bounded independently from per-job concurrency.

For a 20-minute standard-profile video, 30-second chunks produce about 40 analysis requests, not 1,200 requests. Each request receives timestamped observations for its range and emits fixed observation records plus a short chunk summary. Boundary overlap is deduplicated deterministically.

Native Gemini video ranges should be preferred over manually uploaded image batches when Gemini is selected: they preserve embedded audio and temporal context with fewer requests. The image-batch path exists for provider portability and quality fallback, not as the default Google path.

### 6. Rate-limit and retry control

Create a provider/model/account-scoped scheduler with:

- token and media-size estimation before dispatch;
- configurable RPM/TPM/concurrency ceilings with conservative defaults;
- observed-rate adaptation when exact provider quotas are unavailable;
- `Retry-After` support and exponential backoff with jitter for 429/5xx;
- a circuit breaker for repeated quota/provider failures;
- durable chunk states (`pending`, `running`, `ready`, `retry_wait`, `failed`, `cancelled`);
- idempotent chunk fingerprints based on source, range, profile, model snapshot, schema, and prompt;
- fair scheduling across jobs so one long video cannot starve short videos;
- separate interactive and asynchronous batch queues.

A rate-limited job remains resumable. Completed chunks must never be regenerated unless their immutable inputs or analysis contract changed.

### 7. Speech and visual alignment

Run the selected speech path once per audio chunk, preferably with 2–5 minute ASR chunks and short overlaps, then align utterances/words into one-second observation buckets. Do not ask every frame batch to retranscribe the same audio.

When a native video model supplies speech:

- retain it as a model observation;
- use it as the primary speech track only if the adapter declares timestamped-ASR quality;
- otherwise run the configured speech adapter and treat conflicts as validation flags, not silently choose text.

This allows Gemini to handle both modalities today while supporting combinations such as local/hosted ASR plus any image-capable vision model later.

### 8. Hierarchical synthesis and AI navigation

After atomic observations are validated:

- merge adjacent equivalent observations into semantic events;
- create 30–60 second chunk summaries;
- create 3–5 minute chapter summaries;
- create one global synopsis and topic/entity index;
- retain direct links from every summary/event to atomic timestamp ranges;
- build lexical and embedding indexes over speech, OCR, visual actions, and summaries separately.

When an agent asks about the video, retrieval should first search the compact indexes, then hydrate only the relevant atomic observations and neighboring seconds. The full 1,200-row timeline for a 20-minute video should not be injected into every prompt.

### 9. Durable schema

Add a provider-neutral `video_understanding.v1` aggregate instead of stretching `normalized_transcript.v2` into orchestration authority. Preserve `normalized_transcript.v2` as a readable compatibility projection.

New durable records should include:

- job/profile/adapter contract;
- source media metadata;
- chunk manifests and attempts;
- atomic observations;
- speech utterances/words when available;
- semantic events and chapters;
- indexes and quality report;
- generation/validation provenance.

Every mutation must use the existing V3 session mutation boundary. Provider file IDs, local paths, raw frames, credentials, and unbounded provider payloads remain excluded from durable public records.

## Staged implementation plan

### Stage 1 — Contract and durable records

1. Define `video_understanding.v1`, analysis profiles, adapter capability declarations, chunk identity, and terminal/partial semantics.
2. Add Pebble records/projections through canonical V3 mutation paths.
3. Add compatibility rendering from the new aggregate to the existing readable transcript shape.
4. Add validation for chronological coverage, overlap, gaps, size bounds, provenance, and replay.

Gate: provider-free unit tests can commit and replay a synthetic one-minute multimodal timeline with complete one-second coverage.

### Stage 2 — Local media preparation

1. Add bounded probe/extraction interfaces and an explicitly packaged implementation.
2. Produce deterministic timestamped frames, optional change frames, and audio chunks.
3. Add private retention/cleanup and restart-safe manifests.
4. Test rotation, variable frame rate, silent video, audio-only content, corrupted media, cancellation, and source changes.

Gate: the same source/profile always produces the same observation manifest and leaves no unmanaged media after retention cleanup.

### Stage 3 — Scheduler and provider contracts

1. Implement provider/model/account quota buckets, concurrency, durable retry waits, circuit breaking, and fair scheduling.
2. Add token/request estimators and interactive versus asynchronous batch policy.
3. Split native-video, frame-batch, and speech interfaces from the current Google-shaped adapter.
4. Move provider/model selection from the hard-coded Google service into account media settings and catalog-backed capability compilation.

Gate: deterministic fake adapters prove bounded concurrency, 429 recovery, cancellation, crash resume, idempotency, and no duplicate completed work.

### Stage 4 — Gemini adapter migration

1. Adapt the existing Files API upload/poll/delete code to `NativeVideoAnalyzer`.
2. Send `videoMetadata.startOffset`, `endOffset`, and profile FPS where the selected model supports them.
3. Require fixed observation records for each chunk while retaining explicit unknown states.
4. Implement Google frame-batch and optional speech paths only behind the generic interfaces.
5. Retain sanitized diagnostics and temporary-file deletion.

Gate: rerun `ycfinalwithaudio.mp4` and prove one-second coverage plus aligned speech while preserving the existing five-segment compatibility view.

### Stage 5 — Additional provider adapters

1. Add one frame-batch adapter for an already-supported image-capable provider.
2. Add one timestamp-capable speech adapter.
3. Validate mixed-provider composition and provider/model changes in lineage.
4. Do not add provider-specific fields to the durable aggregate.

Gate: the same fixture can be processed with Gemini-only and mixed vision/ASR pipelines and queried through the same normalized navigation API.

### Stage 6 — Navigation, UI, and operational rollout

1. Add timestamp/topic/OCR/speech search and bounded context hydration.
2. Show job progress by chunks, quality profile, retry waits, partial availability, and cost/token estimates.
3. Offer seekable atomic observations, merged events, transcript, and chapters.
4. Add migration/read compatibility for existing `normalized_transcript.v2` records.
5. Roll out behind an explicit media setting; retain the current path until live parity evidence passes.

Gate: a 20-minute fixture survives rate limiting and restart, becomes incrementally navigable, and answers timestamp-specific visual/audio questions without loading its complete timeline into the agent context.

## Quality and acceptance tests

Required fixtures:

- the existing 3:03 Swarm screen demo;
- a fast-changing UI recording with text visible for under one second;
- a silent visual-only video;
- audio with a static/blank visual track;
- overlapping speakers and non-speech sounds;
- variable-frame-rate and rotated video;
- a 20-minute video exercised under forced low RPM/TPM and injected 429/5xx failures.

Measure:

- atomic timeline coverage and explicit unknown rate;
- timestamp drift for speech and events;
- OCR recall on selected ground-truth frames;
- fast-event recall by profile;
- contradiction rate across chunk overlaps;
- retry/resume duplication;
- total requests, estimated/actual input tokens, latency, and cost;
- retrieval precision and hydrated context size.

Do not claim “second-by-second understanding” until every second is represented, gaps are explicit, provenance is retained, and the detailed fixture meets agreed timestamp/OCR/event thresholds.

## Likely attack points

- `swarmd/internal/videotranscription/service.go`: hard-coded Google model resolution, single adapter, one-shot lifecycle, and 12-minute worker timeout.
- `swarmd/internal/provider/google/video_transcription.go`: correct native Files/generateContent path, but no `videoMetadata`, no range chunking, and model-selected output granularity.
- `swarmd/internal/store/pebble/session_transcription_store.go`: useful multimodal segment schema, but missing chunk/observation/provenance/index records.
- `swarmd/internal/runtime/daemon.go`: directly wires the Google adapter into the service.
- `swarmd/internal/tool/runtime_manage_video.go`: current job/read surface will need profile, progress, partial-navigation, and quality fields without exposing provider internals.
- `swarmd/internal/api/media_settings.go` and Desktop media settings: current transcription selection is Google-oriented and should become capability/track based.

## Recommendation

Proceed with Stages 1–4 first. Keep Gemini as the initial native-video adapter because it already consumes the whole visual+audio file and supports custom FPS/ranges. Do **not** begin with one provider call per second. Build the provider-neutral durable coordinator, use 30–60 second overlapping ranges or 15–30 frame batches, and preserve one-second atomic observations plus hierarchical indexes. Add separate vision and ASR adapters after the orchestration contract is proven.

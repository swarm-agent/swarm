# Google-optimized Gemini video processing strategy

Status: researched recommendation only; no production behavior changed.

Checked against current official Google documentation on 2026-08-15.

## Decision

Use Gemini's native video input as the primary Google path. Upload each source once with the Files API, then analyze overlapping time ranges from that same file reference. Do not extract and upload one still per second as the default Google path: native video already samples visuals, processes embedded audio, preserves temporal context, and uses substantially fewer requests and media tokens.

Provide two execution classes:

1. **Interactive queue** — ordinary `generateContent` calls with bounded concurrency for users waiting on results.
2. **Background queue** — one Gemini Batch API job containing the range requests for a video when completion within minutes is not required. Batch is asynchronous, has separate quota, costs 50% of the equivalent interactive API, and has a target turnaround of up to 24 hours.

Swarm must still own the durable range manifest, retries, one-second output contract, merge, coverage validation, and navigation index. Google Batch executes requests; it does not create or validate that workflow for us.

## Current Swarm path

The current implementation already uses the correct Gemini Developer API surface:

1. Resumable upload to `POST /upload/v1beta/files`.
2. Poll `GET /v1beta/{file.name}` until `ACTIVE`.
3. Call `POST /v1beta/models/{selected-media-model}:generateContent` with one `file_data` video part and the prompt.
4. Request JSON mode and normalize the response into Swarm's durable transcript.
5. Delete the temporary Google file.

The exact model is account-selected in `Media.TranscriptionModel` and validated against the current Google catalog for video input and text output. It is therefore not hard-coded in the adapter. Current `manage_video status` intentionally does not expose that model identity. For new Google-optimized work, prefer the latest stable Flash video/text model available in the account catalog; as of this review Google's current stable recommendation surface includes `gemini-3.6-flash`, with 1,048,576 input tokens, 65,536 output tokens, structured output, caching, and Batch API support.

Current implementation gaps relevant to this strategy:

- one whole-video `generateContent` request;
- no `videoMetadata.startOffset`, `endOffset`, or `fps`;
- no media-resolution selection;
- no provider/account/model quota scheduler;
- no Gemini Batch API adapter;
- only three short retries for generation;
- model-selected semantic segment boundaries instead of fixed observation coverage.

## Recommended interactive request plan

### Upload once

Use the Files API for the source video and reuse its URI for every range request. Files persist for 48 hours, support up to 2 GB per file and 20 GB per project, and are explicitly recommended for long or repeatedly queried video.

Do not upload one physical clip per range unless range metadata proves unreliable for a selected model. Each request should reference the same uploaded video and attach:

- `videoMetadata.startOffset`;
- `videoMetadata.endOffset`;
- `videoMetadata.fps`;
- one structured range prompt placed after the video part.

### Initial range geometry

Use these production defaults:

- core range: **45 seconds**;
- overlap: **2 seconds before and after**, clipped at media boundaries;
- sampling: **1 FPS**;
- initial concurrency: **2 requests per account/model**;
- maximum adaptive concurrency: **4**, only after healthy quota and latency evidence;
- one stable request key derived from source digest, range, profile, model snapshot, prompt version, and schema version.

A 20-minute video becomes approximately 27 requests, not 1,200. Forty-five seconds is a practical middle ground: it preserves enough temporal context while keeping each fixed per-second JSON result small enough to validate and retry cheaply. If output adherence is poor, reduce to 30 seconds; if it is consistently excellent and RPM is the bottleneck, increase to 60 seconds.

### Media resolution

Gemini 3 applies approximately:

- general video at low/medium: about 70 visual tokens per sampled frame, plus about 32 audio tokens per second;
- text-heavy video at high: about 280 visual tokens per sampled frame, plus audio.

Use:

- **low/medium** for ordinary scenes, meetings, lectures, and general action;
- **high** for screen recordings, slides, dense OCR, or small UI details;
- a targeted high-resolution second pass for ambiguous/OCR-heavy ranges when the first pass was low.

Do not run high resolution or greater than 1 FPS across an entire long video by default. Raise FPS only for short ranges flagged by scene changes, motion, missing observations, or a detailed user profile.

### Audio

Keep embedded audio in the native video request for the first implementation. Gemini already processes both modalities, so splitting audio immediately would duplicate work and lose some cross-modal context.

Add a separate audio transcription pass only when benchmarks show that native-video speech timing or wording is insufficient. In that mode, transcribe 2–5 minute audio chunks once, then align utterances into visual observations; do not retranscribe audio in every frame batch.

## Rate-limit controller

Google enforces limits per project, not per API key, across RPM, input TPM, daily requests/tokens, and sometimes rolling spend. Exact active limits vary by model, tier, and account status and are shown in AI Studio; Google says published limits are not guaranteed. Swarm must not hard-code a claimed universal Gemini quota.

For interactive work:

1. Maintain token buckets scoped by account, Google project, and model.
2. Admit work below 80% of configured/observed RPM and TPM ceilings.
3. Estimate video tokens from duration, FPS, media resolution, audio, prompt, and expected output; optionally verify unusual requests with `countTokens`.
4. Start at concurrency 2. Increase by one only after a healthy observation window; cap at 4.
5. On 429, honor `Retry-After` when present, otherwise use full-jitter exponential backoff, reduce concurrency by half, and persist `retry_wait`.
6. Retry bounded 5xx/network failures without regenerating completed ranges.
7. Give each job fair scheduling so a long video cannot consume the whole account quota.
8. Preserve completed chunks durably and resume only missing/failed ranges.

A response-size validator must reject missing seconds, invalid timestamps, range leakage, duplicate output, and malformed JSON. A successful HTTP response is not sufficient to mark a range complete.

## Gemini Batch API

The Batch API is the Google facility that most closely matches “submit a job and let it run until it is over.” It supports complete `GenerateContentRequest` objects, multimodal file references, structured output, request metadata keys, polling, cancellation, and result retrieval.

Use it when:

- the user selected background/economy processing;
- expected completion within minutes is not required;
- the video has many ranges or multiple videos are queued;
- lower cost and avoiding interactive RPM/TPM pressure matter more than latency.

Submission shape:

- upload the source video once;
- create one keyed request per overlapping range, each referencing that file URI and range metadata;
- for small jobs under 20 MB of request metadata, submit inline requests;
- for larger jobs, upload a JSONL request manifest and submit it by file;
- normally create one Batch job per video; split very long work into 3–5 minute result waves only when intermediate availability matters;
- poll the batch job about every 30 seconds, then map each keyed result back to its durable range;
- retry only failed/missing ranges in a new batch, never the entire completed set.

Official current Batch properties:

- 50% of equivalent interactive model cost;
- target completion within 24 hours, often faster, but not an interactive SLO;
- separate batch quota from ordinary calls;
- up to 100 concurrent batch requests;
- 2 GB batch input-file limit and 20 GB File API project storage;
- per-model enqueued-token ceilings that vary sharply by account tier.

Do not create one Batch job per second or one Batch job per range. That would exchange generate-call pressure for job-enqueue pressure and make recovery harder.

## Image-batch fallback

Gemini accepts multiple images in one request, but still-image batching should be a fallback or targeted quality pass rather than the main Google route.

Practical defaults:

- 20–30 timestamped images per request at 1 FPS;
- place an explicit timestamp immediately before each image part;
- require one keyed observation for every supplied timestamp;
- use high image resolution only where OCR/detail requires it;
- keep 2-image/2-second overlap at boundaries;
- never approach the large documented file-count ceiling for timeline extraction, because structured-output fidelity and output size become the limiting factors first.

At Gemini 3 high image resolution, 30 images can consume roughly 33,600 visual tokens before prompt/output. Thirty seconds of native low-resolution video is closer to roughly 3,000 media tokens and includes audio. Native video ranges are therefore the faster and cheaper Google default.

Use still batches for:

- a provider-neutral fallback;
- selected high-detail frames after the native pass;
- models without native video input;
- verifying missing/contradictory observations.

## Context caching

Do not make explicit context caching a requirement for the initial extraction scheduler.

- Implicit caching is already enabled for Gemini 2.5+ and benefits requests with similar large prefixes, but hits are not guaranteed.
- Explicit caching can cache a full video and system instruction and is useful for repeated follow-up questions or global synthesis.
- Range extraction needs per-request range metadata, so benchmark explicit caching before coupling it to the range path.
- Standard rate limits still apply to cached requests, and cached tokens still count toward token limits.

Recommended use: after the durable timeline exists, optionally cache the source for a short follow-up-analysis window; rely on Swarm's compact timeline/index for normal navigation.

## Concrete mode policy

### Fast mode (default user-facing)

- Gemini stable Flash model from the media setting, preferably `gemini-3.6-flash` when available.
- Files API upload once.
- 45-second ranges, 2-second overlap, 1 FPS.
- Low/medium video resolution normally; high for screen/UI/OCR profile.
- Interactive `generateContent`, concurrency 2 with adaptive maximum 4.
- Native audio included.
- Incremental durable results become queryable as ranges finish.

### Economy/background mode

- Same range manifest and schema.
- One Gemini Batch job per video, or several 3–5 minute result waves when partial availability matters.
- Poll and reconcile keyed results.
- Expect up to 24 hours; charge is 50% of equivalent interactive processing.

### Detailed repair mode

- Dispatch only failed, low-confidence, fast-motion, or OCR-heavy seconds.
- Use short high-resolution native ranges or 20–30 high-resolution stills per request.
- Optionally run dedicated audio transcription for disputed speech.

## What to build first

Implement the **interactive native-video range path** first, not image extraction and not Batch:

1. Keep one Files API upload alive across multiple range requests.
2. Add range/FPS/media-resolution request fields.
3. Add the 45-second manifest, durable chunk states, and idempotent merge.
4. Add the account/project/model limiter with 429 adaptation.
5. Require and validate one-second observations.
6. Validate on the existing 3:03 screen recording.

Then add Gemini Batch as a second dispatcher over the exact same immutable manifest. That isolates Google-specific throughput choices from Swarm's timeline contract and avoids building two orchestration systems.

## Official sources

- Gemini video understanding: https://ai.google.dev/gemini-api/docs/generate-content/video-understanding
- Gemini Batch API: https://ai.google.dev/gemini-api/docs/batch-api
- Gemini rate limits: https://ai.google.dev/gemini-api/docs/rate-limits
- Gemini file input methods: https://ai.google.dev/gemini-api/docs/file-input-methods
- Gemini media resolution: https://ai.google.dev/gemini-api/docs/media-resolution
- Gemini 3.6 Flash: https://ai.google.dev/gemini-api/docs/models/gemini-3.6-flash
- Gemini context caching: https://ai.google.dev/gemini-api/docs/generate-content/caching

## Relevant filepaths

- `swarmd/internal/provider/google/video_transcription.go`
- `swarmd/internal/videotranscription/service.go`
- `swarmd/internal/store/pebble/session_transcription_store.go`
- `swarmd/internal/api/media_settings.go`
- `swarmd/internal/runtime/daemon.go`
- `swarmd/internal/tool/runtime_manage_video.go`
- `docs/checkpoints/provider-neutral-video-understanding-plan.md`

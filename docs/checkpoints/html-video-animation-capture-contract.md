# Deterministic HTML animation capture contract

## Purpose

`export_html_animation` is the time-based companion to `export_html_stills`. It preserves authored motion without weakening the stable `swarm.capture/v1` state-to-PNG contract.

The operation authenticates one exact ready `text/html` or canonical HTML package artifact, samples an author-controlled deterministic clock in trusted Chrome, encodes a silent H.264 MP4 with the system FFmpeg binary, and publishes the result through the existing managed-artifact authority with exact source lineage.

## Authoring contract: `swarm.animation/v1`

The canonical HTML entry contains exactly one manifest:

```html
<script id="swarm-animation-manifest" type="application/json">
{"version":"swarm.animation/v1","duration_ms":3000,"fps":30}
</script>
```

Before `DOMContentLoaded`, it installs:

```js
globalThis.__SWARM_ANIMATION_V1__ = Object.freeze({
  version: "swarm.animation/v1",
  async ready() {
    return { duration_ms: 3000, fps: 30 };
  },
  async seek(timeMs) {
    // Set every CSS/WAAPI/SVG/Canvas/WebGL value from timeMs, then await
    // any state-specific canvas/layout work required for stable pixels.
    document.documentElement.dataset.swarmAnimationTimeMs = String(timeMs);
    return { time_ms: timeMs };
  }
});
```

`ready()` returns exactly `{duration_ms, fps}` matching the manifest. `seek(timeMs)` returns exactly `{time_ms: timeMs}` and sets `document.documentElement.dataset.swarmAnimationTimeMs` to the same integer only after the timestamp is stable.

The page may author any motion permitted by its reviewed `motion_ui`, `spatial_3d`, or `vector_playback` artifact profile. Deterministic capture must derive visible state from the supplied timestamp rather than wall time, randomness, or asynchronous external input.

## Fixed limits and security

- Output is 1920×1080, 1–60 FPS, 100–600,000 ms, and at most 36,000 frames.
- Capture uses bounded 300-frame windows. Each window seeks the authored runtime with global timeline timestamps, encodes one silent segment, removes its private PNGs, then deterministically concatenates all segments.
- Browser and encoder paths are daemon-owned system paths. Tool calls cannot override them.
- A fresh sandboxed Chrome profile and private job directory are used for each operation.
- External network, downloads, popups, navigation, forms, workers, and HTML media are blocked by CSP and request interception.
- Visible dialogs/popovers or `data-swarm-capture-blocking` elements fail capture.
- `data-swarm-capture-ui` elements are removed before frame sampling.
- Every timestamp is renderer-selected from frame index and FPS. The page cannot choose sampling time.
- Two pixel samples must match after every `seek`; otherwise capture fails as unstable.
- MP4 encoding uses one FFmpeg thread, no audio, removed metadata, bitexact flags, H.264, and `yuv420p`.
- Job files are private and removed on success, failure, cancellation, or timeout.

HTML audio is intentionally unsupported. Soundtracks remain exact `source_audio` timeline clips governed by the existing video proposal, revision, and user-acceptance boundaries.

## Tool flow

Call `manage_artifact` with:

```json
{
  "action": "export_html_animation",
  "session_id": "<source session>",
  "collection_id": "<source collection>",
  "variant_id": "<source variant>",
  "event_seq": 1
}
```

No duration, FPS, browser, encoder, dimensions, runtime, output path, or network override is accepted. Successful output is one exact ready managed `video/mp4` reference carrying:

- `landscape_video` output requirements;
- `final_render` playback profile;
- temporal review targets copied from a compatible `swarm.iteration/v1` manifest whose duration matches the animation timeline;
- source session, collection, variant, and event-sequence lineage;
- video presentation metadata suitable for preview.

Use that exact reference as `artifact_ref` on a visible `managed_artifact` video timeline clip. The existing video renderer materializes and probes the MP4, while a separate exact `source_audio` clip supplies the soundtrack. AI may propose the timeline edit; the user still accepts proposals and starts final rendering.

## Dogfood evidence

`swarmd/internal/videorender/html_animation_soundtrack_integration_test.go` renders a deterministic moving HTML fixture through trusted Chrome, creates a local sine-wave soundtrack fixture, passes the silent MP4 and audio-only input through the existing timeline/FFmpeg builder, and verifies that the final MP4 contains both video and audio streams. All media stays in the test temporary directory; no generated output or user media is committed.

## Known limits

The v1 contract records deterministic visual state, not browser wall-clock behavior. Authored code that cannot implement stable random-access `seek(timeMs)` is unsupported. HTML-originated audio, live microphone/camera input, remote assets, dynamic network data, and durations above ten minutes require a separately reviewed future contract.

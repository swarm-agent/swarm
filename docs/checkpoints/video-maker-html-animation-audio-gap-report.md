# Video Maker: HTML Animation and Audio Flow Audit

Date: 2026-08-22

## Executive answer

**Partially.** Swarm has a working, AI-accessible pipeline for creating HTML animation artifacts, freezing declared HTML states into managed PNG stills, arranging those stills or existing videos in a durable video project, reviewing AI-proposed changes, and rendering an MP4.

Swarm **does not yet have the end-to-end flow** implied by:

> “Take this song and overlay it onto the video while we keep building the animated music video.”

The render engine can preserve and mix audio already embedded in accepted video inputs, including layered tracks, but there is no first-class song/audio source, upload/attachment flow, audio-only timeline clip, AI tool contract, or Video Studio music controls. HTML is converted to **stable stills**, not recorded as a continuously animated video clip.

## Capability matrix

| Desired capability | Status | Current behavior |
| --- | --- | --- |
| AI creates animated HTML artifacts | Implemented | Managed artifacts support animation profiles for CSS/WAAPI/SVG, pinned Three.js, imported vector playback, and final MP4 playback. |
| Convert HTML artifact into video input | Implemented | `export_html_stills` captures 1–16 stable PNG states; the separate `export_html_animation` path samples a bounded deterministic `swarm.animation/v1` timeline and publishes a silent managed MP4. |
| AI makes a visual video plan from those outputs | Implemented | Exact ready PNG references can be used directly as `manage_video` plan-part visuals. Plans are durable pending review objects. |
| Assemble stills and video clips, captions, transitions | Implemented | Timeline supports source videos, managed artifacts, synthetic color/text, captions, tracks/layers, and a bounded transition set. |
| Render reviewed timeline to MP4 | Implemented | FFmpeg render jobs create managed `video/mp4` output; Desktop can start, preview, and export it. |
| Preserve audio from source videos | Implemented | Renderer probes input media for audio and includes existing audio streams. |
| Mix audio from layered video clips | Implemented, basic | Delayed secondary-track audio is combined with `amix`; primary cuts concatenate audio and dissolve transitions use `acrossfade`. |
| Add a supplied MP3/WAV/M4A as a soundtrack | Missing | Source browser accepts video extensions only; timeline source kinds have no audio-only source. |
| AI command to overlay a selected song | Missing | `manage_video` accepts video references and image visuals, not an exact audio reference or soundtrack operation. |
| Music volume, fade, loop, ducking, beat sync | Mostly missing | Per-clip volume/mute is rendered. `VideoAudioPolicy` declares master volume and ducking fields, but the renderer does not consume the policy. No envelopes, fades, loop, beat markers, or tempo analysis exist. |
| Preview/edit music in Video Studio | Missing | Frontend wire types expose clip volume/mute, but current Video Tool has no soundtrack lane, audio picker, waveform, mixer, or volume controls. |

## What is implemented

### 1. Managed animated artifacts

Swarm has explicit animation profiles in `swarmd/internal/artifact/animation_profile.go`:

- `motion_ui` — native CSS, WAAPI, SVG;
- `spatial_3d` — pinned Three.js/WebGL;
- `vector_playback` — imported dotLottie/Rive playback;
- `final_render` — MP4 playback.

These profiles govern artifact preview/runtime safety and budgets. They do not themselves render animation into a new MP4.

### 2. HTML-to-video-still bridge

The normalized `swarm.capture/v1` flow is implemented and documented in `docs/checkpoints/html-video-still-capture-contract.md`.

A compatible ready HTML artifact or HTML package:

1. declares 1–16 states in `#swarm-capture-manifest`;
2. implements `globalThis.__SWARM_CAPTURE_V1__.select(stateId)` and `.ready(stateId)`;
3. is passed by exact managed reference to `manage_artifact` action `export_html_stills`;
4. is rendered in trusted Chrome at 1920×1080;
5. produces one immutable managed PNG per declared state.

The renderer deliberately disables/cancels animation and verifies two identical pixel samples. This is a **state-to-still** contract. It explicitly says HTML does not become a video clip.

Relevant implementation:

- `swarmd/internal/tool/runtime_manage_artifact_capture.go`
- `swarmd/internal/htmlcapture/renderer.go`
- `docs/checkpoints/html-video-still-capture-contract.md`

### 3. AI visual planning and review

`manage_video` supports:

- source discovery and video transcription;
- project creation and immutable revisions;
- atomic visual plans where every part has one exact ready image;
- typed timeline edit proposals;
- proposal status and accepted-cut inspection;
- render-setting recommendations.

The expected initial workflow is:

1. `create_project` without an initial timeline;
2. use its exact `project_id` and `revision_id`;
3. `propose_plan` with `plan.kind=initial` and one ready image per part;
4. user reviews and accepts the proposal through the Video Studio/API boundary;
5. user explicitly starts the final render.

AI cannot accept its own proposal or start the final Video Studio render. That is an intentional user-authority boundary.

Relevant implementation:

- `swarmd/internal/tool/runtime_manage_video.go`
- `swarmd/internal/store/pebble/session_video_project_store.go`
- `swarmd/internal/api/sessions_v3_video_projects.go`
- `web/src/features/desktop/tools/video-studio/video-studio-surface.tsx`

### 4. Video timeline and FFmpeg render

The durable timeline supports:

- `source_video`, `managed_artifact`, `color`, and `text` clip source kinds;
- exact managed artifact references;
- tracks/layers, timeline placement, source trimming, volume and mute;
- captions;
- cut, crossfade, and fade-through/to/from-black transitions;
- bounded 1–60 FPS and common landscape/portrait/square presets.

The FFmpeg builder:

- loops still images for clip duration;
- scales and pads media;
- trims video/audio;
- applies per-input volume;
- concatenates or crossfades primary audio;
- delays and `amix`es audio from layered clips;
- outputs H.264/AAC MP4.

Relevant implementation:

- `swarmd/internal/videorender/builder.go`
- `swarmd/internal/videorender/service.go`
- `swarmd/internal/videoproject/service.go`
- `web/src/features/desktop/tools/pages/video-tool-page.tsx`

## Why the requested song-overlay flow does not work yet

### Gap 1: no audio source authority

`videosource.Service` only indexes video containers with these extensions:

- AVI, M4V, MKV, MOV, MP4, MPEG/MPG, WebM.

There is no authenticated audio source catalog for MP3, WAV, M4A/AAC, FLAC, or OGG and no audio attachment contract comparable to video attachments.

Result: a user cannot select “this song” as an exact trusted source for the video project.

### Gap 2: no audio-only timeline clip

The timeline has no `source_audio`/`managed_audio` source kind. Its validated source kinds are video, managed artifact, color, and text. Render materialization initializes every visible clip as video, and FFmpeg expects a video stream for every input before it processes audio.

The `MaterializedInput.IsAudio` field exists but is unused. An audio-only file would currently fail because the builder constructs `[N:v]` for every input.

Result: a song cannot occupy a dedicated music track while image/video clips continue to define the visuals.

### Gap 3: no AI soundtrack operation

`manage_video` has no fields or actions for:

- audio refs;
- add/replace/remove soundtrack;
- soundtrack start offset or trim range;
- looping to timeline duration;
- music gain/fade envelopes;
- ducking beneath narration;
- beat/tempo analysis or sync.

Generic `add_clip` could only express this after the durable timeline gains an audio source kind and trusted exact-reference resolver.

### Gap 4: declared audio policy is not executed

`VideoAudioPolicy` contains:

- `master_volume`;
- `muted`;
- `duck_other_tracks`;
- `ducking_level`.

Search of the render implementation shows these fields are not consumed. Only the clip-level `Volume` and `Muted` fields affect FFmpeg today.

Result: the schema suggests more control than the renderer actually delivers. This should either be implemented end-to-end or narrowed until it is real.

### Gap 5: no music editing UI or preview

The Video Tool wire type includes `volume` and `muted`, but timeline creation hard-codes volume to `1.0`, and the visible editor provides no music/audio picker, audio lane, waveform, mute/solo, gain, trim, fade, or ducking controls.

Result: even renderer-supported per-clip gain is not a usable music workflow in Video Studio.

### Gap 6: HTML animation is not captured over time

The HTML capture contract enforces reduced motion, cancels animation, and exports stable frames. This is correct for reliable storyboard slides, but it cannot produce a continuous animated sequence.

Result: an “animated music video” can currently be approximated with multiple declared still states, image durations, captions, and crossfades. It cannot preserve authored CSS/WAAPI/Three.js animation timing as moving video.

## Recommended implementation order

### P0 — first-class soundtrack overlay

This is the minimum required to support the exact target request.

1. **Create audio source authority**
   - Register trusted workspace audio folders/files with opaque references and fingerprints.
   - Support a reviewed allowlist such as MP3, WAV, M4A/AAC, FLAC, and OGG.
   - Add triggering-message audio attachments if upload/chat attachment is required.

2. **Extend the durable timeline**
   - Add `source_audio` (and optionally exact managed audio artifact) source kinds.
   - Define audio clip source range, timeline range, track, gain, mute, and loop behavior.
   - Validate audio clips independently from visible video clips.

3. **Extend render materialization and FFmpeg planning**
   - Do not require `[N:v]` for audio-only inputs.
   - Delay/trim/pad/loop music to the accepted timeline range.
   - Mix it with source video audio using deterministic gain rules.
   - Apply timeline master volume/mute and make `VideoAudioPolicy` real.

4. **Add AI-safe soundtrack operations**
   - Exact audio reference only; no arbitrary paths.
   - Typed add/replace/remove/update operations with affected ranges.
   - AI proposes; user reviews/accepts; user starts final render.

5. **Add Video Studio controls**
   - Music picker, dedicated audio lane, start/trim/end, volume, mute, fade in/out, and preview.

After P0, the sentence “take this song and overlay it onto the video while we work” should create a pending soundtrack edit against the current exact revision, while visual work continues as later proposals.

### P1 — practical launch-video mixing

- narration-vs-music ducking with explicit threshold/attack/release or a simpler deterministic sidechain policy;
- volume envelopes and fades;
- loop/crop/fill-to-project-duration options;
- waveform thumbnails and scrub-synchronized audio preview;
- loudness normalization and peak limiting with documented targets;
- tests proving preview/export/render use the same accepted audio policy.

### P2 — real HTML animation-to-video (implemented)

The separate `swarm.animation/v1` / `export_html_animation` contract is documented in `docs/checkpoints/html-video-animation-capture-contract.md`. It accepts an exact ready HTML/package source with a reviewed animation profile, uses a renderer-controlled deterministic clock under fixed duration/FPS/frame limits, blocks external network and HTML audio, publishes a managed `video/mp4` with exact source lineage, and feeds the existing timeline as a managed artifact clip.

The still-capture action remains unchanged: stable storyboard capture and time-based animation capture are separate, auditable operations.

### P3 — music-video intelligence

- beat, onset, bar, tempo, and section analysis stored as durable evidence;
- snap cuts/keyframes/transitions to musical markers;
- lyric/transcript alignment where licensed and user-supplied;
- AI proposals that cite exact music ranges and preserve user review boundaries.

## Target future flow

A complete product flow should be:

1. User supplies or selects a song from an authenticated audio source.
2. Swarm creates/loads the Video Studio project and exact current revision.
3. AI proposes an `add_audio_clip` soundtrack operation with exact source, range, gain, loop/fill, and fade settings.
4. The working preview plays the song under the current visual cut without changing the accepted revision.
5. User accepts the soundtrack proposal.
6. AI and the user continue making HTML/still/video visual iterations against later exact revisions.
7. Optional beat analysis provides durable markers for animation timing and cuts.
8. User explicitly starts the final MP4 render.

## Conclusion

Swarm already has the durable project/revision/proposal/render foundation and the HTML-to-still visual bridge needed for an AI-assisted launch-video workflow. The missing product boundary is **first-class audio media and audio-only timeline editing**. Implement that before deeper music intelligence. Separately, add a deterministic HTML-animation-to-MP4 contract if the launch video must preserve authored animation rather than use storyboard states and transitions.

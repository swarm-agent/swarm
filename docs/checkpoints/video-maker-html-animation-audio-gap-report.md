# Video Maker: HTML Animation and Audio Flow Audit

Date: 2026-08-22

## Executive answer

**Implemented for the reviewed baseline.** Swarm can create deterministic HTML animation artifacts, export them as silent managed MP4 clips, register trusted local audio sources, transcribe and analyze selected audio, propose audio-only soundtrack edits against an exact video-project revision, preview pending soundtrack changes in Video Studio, and render accepted visuals and audio to MP4.

The user-authority boundaries remain unchanged: AI can propose soundtrack and visual edits, but only the user can accept a proposal or start final rendering. Direct chat audio uploads, fades, looping, ducking, and automatic beat-synced editing remain outside this baseline.

## Capability matrix

| Desired capability | Status | Current behavior |
| --- | --- | --- |
| AI creates animated HTML artifacts | Implemented | Managed artifacts support reviewed animation profiles for CSS/WAAPI/SVG, pinned Three.js, and licensed vector playback. |
| Convert HTML artifact into video input | Implemented | `export_html_stills` captures stable PNG states; `export_html_animation` samples a bounded deterministic `swarm.animation/v1` timeline and publishes a silent managed MP4. |
| Assemble and render visuals | Implemented | Durable projects support exact managed artifacts, source videos, stills, text, captions, tracks, layers, and reviewed transitions. |
| Add MP3/WAV/M4A and other supported audio | Implemented for registered sources | Registered source-media folders expose supported audio through opaque exact references. Direct chat audio upload remains deferred. |
| AI command to overlay selected audio | Implemented | `manage_video` accepts exact audio references and typed add/update/replace/remove `source_audio` clip proposals against an exact base revision. |
| Preview/edit music in Video Studio | Implemented baseline | Video Studio provides a trusted-audio picker, soundtrack lane, pending-proposal preview, placement/trim, gain/mute, replacement, and removal. |
| Speech transcription and timing | Implemented | Registered audio can be transcribed with bounded word timing and durable normalized transcript evidence. |
| Waveform and music analysis | Implemented baseline | Deterministic PCM analysis provides bounded levels, onsets, tempo, beat timestamps, and energy sections when confidence permits. |
| Fades, looping, ducking, and automatic beat sync | Deferred | Clip gain/mute and timeline master volume/mute are rendered; advanced envelopes and automatic edits are not part of this contract. |

## Implemented contracts

### Deterministic HTML animation capture

`swarm.animation/v1` is the time-based companion to stable `swarm.capture/v1` still capture. A compatible exact ready HTML artifact:

1. declares bounded `duration_ms` and `fps` in `#swarm-animation-manifest`;
2. installs `globalThis.__SWARM_ANIMATION_V1__` before `DOMContentLoaded`;
3. returns exact metadata from `ready()` and stable timestamp state from `seek(timeMs)`;
4. is sampled only at renderer-selected timestamps in trusted Chrome;
5. is encoded as a silent managed H.264 MP4 with exact source lineage.

The renderer owns dimensions, timing, browser, encoder, frame limits, and network isolation. HTML audio is blocked. The resulting exact managed MP4 can be used as a visible video timeline clip while a separate `source_audio` clip supplies the soundtrack.

Relevant implementation:

- `swarmd/internal/htmlcapture/animation.go`
- `swarmd/internal/tool/runtime_manage_artifact_animation.go`
- `swarmd/internal/videorender/html_animation_soundtrack_integration_test.go`
- `docs/checkpoints/html-video-animation-capture-contract.md`

### Trusted registered audio

Source-media browsing recognizes reviewed audio extensions and validates media content before returning it. The browser returns an opaque `audiosrc_…` reference plus display name, media type, bounded size, source fingerprint, and fingerprint version; host paths are not returned through the AI tool contract.

Exact references are revalidated against the authenticated workspace, registered root, file metadata, fingerprint, and supported media type before transcription, analysis, project edits, media reads, or rendering. A changed or moved source fails stale or unavailable rather than silently reading different bytes.

Relevant implementation:

- `swarmd/internal/videosource/service.go`
- `swarmd/internal/store/pebble/session_audio_source_store.go`
- `swarmd/internal/api/audio_transcription.go`
- `docs/checkpoints/trusted-audio-source-manual-check.md`

### Audio transcription and deterministic analysis

Registered audio transcription uses a private bounded preparation step. Semantic speech and non-speech output is normalized onto the source millisecond playhead. Separately, deterministic 16 kHz mono PCM is the timing authority for:

- bounded waveform RMS and peak levels;
- onset events;
- tempo and beat timestamps when confidence is sufficient;
- conservative energy sections.

Analysis snapshots are durable, workspace-scoped, fingerprint-bound, immutable, and exposed through bounded range reads. Provider payloads, raw PCM, private storage paths, and unbounded arrays are not returned.

Relevant implementation:

- `swarmd/internal/videotranscription/audio_analysis.go`
- `swarmd/internal/videotranscription/service.go`
- `swarmd/internal/store/pebble/audio_analysis_store.go`
- `swarmd/internal/tool/runtime_manage_video.go`

### Reviewed soundtrack proposals

The durable timeline supports invisible `source_audio` clips carrying one complete exact audio reference. Validation requires coherent source and timeline ranges, a bounded gain, no visual captions, and no arbitrary source path or artifact substitution.

AI-safe edit operations are the existing typed `add_clip`, `update_clip`, `replace_clip`, and `remove_clip` operations. Every proposal includes affected ranges and an exact base revision. Proposals remain pending until user acceptance; AI cannot accept a proposal or start final rendering.

Video Studio provides:

- a registered-audio picker;
- a dedicated soundtrack lane;
- pending-proposal preview while the accepted revision remains unchanged;
- start, trim, gain, mute, replace, and remove controls.

Relevant implementation:

- `swarmd/internal/store/pebble/session_video_project_store.go`
- `swarmd/internal/videoproject/service.go`
- `swarmd/internal/tool/runtime_manage_video.go`
- `swarmd/internal/api/sessions_v3_video_projects.go`
- `web/src/features/desktop/tools/pages/video-tool-page.tsx`
- `docs/checkpoints/video-studio-soundtrack-manual-walkthrough.md`

### Deterministic final rendering

Audio-only inputs are materialized without requiring a video stream. The FFmpeg plan trims and normalizes audio, applies clip gain/mute, delays soundtrack placement onto the project timeline, mixes it with accepted source-video audio, and applies timeline master volume/mute before producing H.264/AAC MP4.

The integration test exercises deterministic HTML animation capture, a local generated audio fixture, audio-only timeline materialization, and final video-plus-audio output. Test media remains temporary and is not committed.

Relevant implementation:

- `swarmd/internal/videorender/builder.go`
- `swarmd/internal/videorender/service.go`
- `swarmd/internal/videorender/html_animation_soundtrack_integration_test.go`

## Current limits

- Audio files must come from registered source-media folders; direct audio chat attachments are not supported.
- HTML-originated audio is blocked; soundtracks use a separate exact audio source.
- Soundtrack fades, envelopes, looping/fill, ducking, loudness normalization, and limiting are not implemented.
- Video Studio does not yet show a waveform or advanced mixer.
- Deterministic onset/tempo/beat evidence is readable, but automatic beat-snapped edits are not implemented.
- `swarm.animation/v1` requires stable random-access rendering through `seek(timeMs)` and is capped by the trusted renderer's fixed duration, FPS, and frame limits.

## Reviewed end-to-end flow

1. Register or select a trusted source-media folder containing licensed audio.
2. Browse it through Video Studio or `manage_video` and retain the complete exact audio reference.
3. Optionally start transcription and inspect bounded transcript/audio-analysis evidence.
4. Create or load a video project and read its exact current revision.
5. Export compatible HTML motion through `export_html_animation` or use other accepted visual sources.
6. Propose an exact `source_audio` add/update/replace/remove operation against the base revision.
7. Preview the pending soundtrack in Video Studio while the accepted revision remains unchanged.
8. User accepts the proposal.
9. Continue visual or soundtrack iterations against later exact revisions.
10. User explicitly starts the final render.

## Conclusion

Swarm now has the reviewed baseline needed to combine deterministic authored HTML motion with trusted local audio under durable project, exact-reference, proposal, and user-acceptance boundaries. Future work should focus on direct persistent audio ingestion, advanced mixing, waveform UX, and optional analysis-driven edit proposals without weakening those authorities.

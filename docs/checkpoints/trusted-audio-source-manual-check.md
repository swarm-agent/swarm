# Trusted audio source manual check

Use a non-copyrighted local audio file you created or are licensed to use. Do not add the media file to this repository.

1. In the workspace source-media settings, register the directory containing the local audio file.
2. Place one supported file in that directory: MP3, WAV, M4A, AAC, FLAC, OGG/OGA, or Opus.
3. In Video Studio, browse that registered source-media directory.
4. Confirm the audio entry is returned separately from videos with:
   - `media_kind: "audio"`
   - an opaque `audiosrc_…` reference
   - `mime_type`, `size_bytes`, `source_fingerprint`, and `fingerprint_version`
   - no host filesystem path
5. Browse the same directory again without changing the file and confirm its opaque reference and fingerprint are unchanged.
6. Start transcription with the exact `audio_refs` value and confirm one durable job is returned without creating a chat audio attachment.
7. When the job is ready, read the transcript and confirm:
   - spoken `words` include integer `start_ms` and `end_ms` on the source playhead
   - word timing is explicitly model-generated semantic timing
   - speech and non-speech segments use the same millisecond timeline
8. Read the corresponding audio analysis by `analysis_ref` or `source_fingerprint` with a bounded `start_ms`/`end_ms` range. Confirm it returns:
   - bounded waveform RMS/peak levels
   - deterministic onset events
   - tempo and beat timestamps when confidence is sufficient
   - musically useful energy sections when derivable
   - `timing_authority: "deterministic_pcm"` and `model_generated: false`
9. Seek playback to a returned word, onset, beat, and section boundary; confirm they align to the same source millisecond playhead.
10. Rename a copy to an unsupported extension, or give a supported extension to non-audio content, and confirm it is not returned.
11. Move or modify the indexed file and confirm exact-reference transcription/analysis reports it as stale or unavailable rather than silently using changed bytes.
12. Confirm bounded range reads do not expose host paths, raw PCM, provider payloads, or unbounded waveform arrays.

This contract covers registered source-folder audio. Direct MP3/WAV chat uploads remain a separate deferred persistent-media ingestion feature.

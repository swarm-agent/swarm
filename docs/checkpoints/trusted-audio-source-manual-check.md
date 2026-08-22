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
6. Rename a copy to an unsupported extension, or give a supported extension to non-audio content, and confirm it is not returned.
7. Move or modify the indexed file and confirm exact-reference resolution reports it as stale or unavailable rather than silently using changed bytes.

This checkpoint only establishes trusted discovery and references. It intentionally does not add audio timeline clips, rendering, AI soundtrack operations, or Studio editing controls.

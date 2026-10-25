package claudecode

import (
	"testing"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
)

// Purpose: buildTranscriptPrompt/contentText must not imply that session_media
// attachments were delivered visually through the text-only CLI prompt. This
// serialization test proves explicit inspection guidance and prevents payload
// metadata or bytes from falling through to JSON, including malformed input.
func TestSessionMediaTranscriptRequiresInspection(t *testing.T) {
	for _, tc := range []struct {
		name  string
		media any
		want  string
	}{
		{"image", provideriface.SessionMediaPayload{AssetID: "fixture-image", Modality: "image", MIMEType: "image/png", Bytes: []byte("private-image-bytes"), DigestSHA256: "private-digest"}, `[Attachment not delivered natively: you cannot see or hear this attachment directly. To inspect it, call media_inspect with {"asset_id":"fixture-image"}.]`},
		{"missing identity", provideriface.SessionMediaPayload{Bytes: []byte("private-image-bytes")}, "[Attachment not delivered: you cannot see or hear this attachment directly; its asset_id is unavailable.]"},
		{"malformed", map[string]any{"bytes": "private-image-bytes"}, "[Attachment not delivered: you cannot see or hear this attachment directly; its asset_id is unavailable.]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := buildTranscriptPrompt([]map[string]any{{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": "Review this"}, map[string]any{"type": "session_media", "media": tc.media}}}}, "")
			if want := "[user]\nReview this\n" + tc.want; got != want {
				t.Fatalf("transcript = %q, want %q", got, want)
			}
		})
	}
	if got := contentText(map[string]any{"type": "input_image", "image_url": "private-url"}); got != "[attachment omitted]" {
		t.Fatalf("raw image placeholder changed: %q", got)
	}
}

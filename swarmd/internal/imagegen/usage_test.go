package imagegen

import "testing"

// Purpose: Gemini modality counters must not mix text and image tokens before
// GenerateManagedImage passes them to pricing. This parser unit test is the
// narrowest layer proving modality selection without a live provider request.
func TestMediaCostModalityTokens(t *testing.T) {
	usage := map[string]any{"candidatesTokensDetails": []any{map[string]any{"modality": "IMAGE", "tokenCount": float64(1120)}, map[string]any{"modality": "TEXT", "tokenCount": float64(12)}}}
	if got := modalityTokens(usage, "candidatesTokensDetails", "TEXT"); got != 12 {
		t.Fatalf("text %d", got)
	}
	if got := extractImageOutputTokens(usage); got != 1120 {
		t.Fatalf("image %d", got)
	}
	if got := modalityTokens(nil, "promptTokensDetails", "IMAGE"); got != 0 {
		t.Fatalf("missing metadata %d", got)
	}
}

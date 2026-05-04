package codex

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestBuildImageGenerationPayloadForcesImageGenerationToolChoice(t *testing.T) {
	payload, err := buildImageGenerationPayload(ImageGenerationRequest{Model: "gpt-5.1-codex", Prompt: "draw a cat", Size: "auto"})
	if err != nil {
		t.Fatalf("buildImageGenerationPayload: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	toolChoice, ok := decoded["tool_choice"].(map[string]any)
	if !ok || toolChoice["type"] != "image_generation" {
		t.Fatalf("tool_choice = %#v, want forced image_generation", decoded["tool_choice"])
	}
	if decoded["model"] != "gpt-5.1-codex" {
		t.Fatalf("model = %#v", decoded["model"])
	}
}

func TestParseImageGenerationResultDecodesCompletedCall(t *testing.T) {
	payload := base64.StdEncoding.EncodeToString(testPNGBytes())
	result, err := parseImageGenerationResult(map[string]any{
		"id":    "resp_1",
		"model": "gpt-5.5",
		"output": []any{map[string]any{
			"type":           "image_generation_call",
			"id":             "ig_1",
			"status":         "completed",
			"revised_prompt": "revised",
			"result":         payload,
		}},
	})
	if err != nil {
		t.Fatalf("parseImageGenerationResult: %v", err)
	}
	if result.ResponseID != "resp_1" || result.Model != "gpt-5.5" || result.CallID != "ig_1" || result.RevisedPrompt != "revised" || !looksLikePNG(result.DecodedPNG) {
		t.Fatalf("result = %#v", result)
	}
	if result.ProviderResponse == nil || result.ProviderResponse["id"] != "resp_1" {
		t.Fatalf("provider response = %#v, want raw decoded response", result.ProviderResponse)
	}
}

func TestParseImageGenerationResultSkipsGeneratingCallAndUsesCompletedCall(t *testing.T) {
	payload := base64.StdEncoding.EncodeToString(testPNGBytes())
	result, err := parseImageGenerationResult(map[string]any{
		"id":    "resp_2",
		"model": "gpt-5.5",
		"output": []any{
			map[string]any{
				"type":           "image_generation_call",
				"id":             "ig_1",
				"status":         "generating",
				"revised_prompt": "draft prompt",
			},
			map[string]any{
				"type":           "image_generation_call",
				"id":             "ig_1",
				"status":         "completed",
				"revised_prompt": "final prompt",
				"result":         payload,
			},
		},
	})
	if err != nil {
		t.Fatalf("parseImageGenerationResult: %v", err)
	}
	if result.ResponseID != "resp_2" || result.CallID != "ig_1" || result.RevisedPrompt != "final prompt" || !looksLikePNG(result.DecodedPNG) {
		t.Fatalf("result = %#v", result)
	}
}

func TestParseImageGenerationResultReportsIncompleteCallOnlyAfterScanningAllItems(t *testing.T) {
	_, err := parseImageGenerationResult(map[string]any{
		"output": []any{map[string]any{
			"type":   "image_generation_call",
			"id":     "ig_1",
			"status": "generating",
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "observed statuses: generating") {
		t.Fatalf("error = %v, want observed generating status", err)
	}
}

func TestProviderResponseFromErrorReturnsRawResponse(t *testing.T) {
	raw := map[string]any{"id": "resp_generating"}
	err := &ProviderResponseError{Err: errors.New("observed statuses: generating"), Response: raw}
	got, ok := ProviderResponseFromError(err)
	if !ok || got["id"] != "resp_generating" {
		t.Fatalf("ProviderResponseFromError = %#v, %v", got, ok)
	}
}

func TestParseImageGenerationResultRejectsNonPNGBase64(t *testing.T) {
	payload := base64.StdEncoding.EncodeToString([]byte("not-a-png"))
	_, err := parseImageGenerationResult(map[string]any{
		"output": []any{map[string]any{
			"type":   "image_generation_call",
			"id":     "ig_1",
			"status": "completed",
			"result": payload,
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "not a PNG") {
		t.Fatalf("error = %v, want non-PNG rejection", err)
	}
}

func TestParseImageGenerationResultRejectsDataURL(t *testing.T) {
	_, err := parseImageGenerationResult(map[string]any{
		"output": []any{map[string]any{
			"type":   "image_generation_call",
			"id":     "ig_1",
			"status": "completed",
			"result": "data:image/png;base64,Zm9v",
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "raw base64") {
		t.Fatalf("error = %v, want raw base64 rejection", err)
	}
}

func testPNGBytes() []byte {
	return []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 0, 0, 0}
}

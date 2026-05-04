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

func TestParseImageGenerationResultAcceptsGeneratingCallWithValidImageResult(t *testing.T) {
	payload := base64.StdEncoding.EncodeToString(testPNGBytes())
	result, err := parseImageGenerationResult(map[string]any{
		"id":    "resp_generating_result",
		"model": "gpt-5.5",
		"output": []any{map[string]any{
			"type":           "image_generation_call",
			"id":             "ig_generating",
			"status":         "generating",
			"revised_prompt": "final prompt",
			"result":         payload,
		}},
	})
	if err != nil {
		t.Fatalf("parseImageGenerationResult: %v", err)
	}
	if result.ResponseID != "resp_generating_result" || result.CallID != "ig_generating" || result.RevisedPrompt != "final prompt" || !looksLikePNG(result.DecodedPNG) {
		t.Fatalf("result = %#v", result)
	}
	if len(result.Results) != 1 || result.Results[0].CallID != "ig_generating" {
		t.Fatalf("results = %#v, want generating status result promoted as final", result.Results)
	}
}

func TestParseImageGenerationResultSkipsGeneratingCallWithoutResultAndUsesCompletedCall(t *testing.T) {
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

func TestBuildImageGenerationPayloadSupportsCountAndPartialPreviewSemantics(t *testing.T) {
	payload, err := buildImageGenerationPayload(ImageGenerationRequest{Model: "gpt-5.5", Prompt: "draw cats", Count: 3, PartialImages: 9})
	if err != nil {
		t.Fatalf("buildImageGenerationPayload: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if decoded["parallel_tool_calls"] != true {
		t.Fatalf("parallel_tool_calls = %#v, want true for multi-image generation", decoded["parallel_tool_calls"])
	}
	instructions := asString(decoded["instructions"])
	if !strings.Contains(instructions, "exactly 3 completed images") {
		t.Fatalf("instructions = %q, want exact final image count", instructions)
	}
	input := asSlice(decoded["input"])
	inputItem, ok := input[0].(map[string]any)
	if !ok {
		t.Fatalf("input = %#v", decoded["input"])
	}
	content := asSlice(inputItem["content"])
	contentItem, ok := content[0].(map[string]any)
	if !ok || !strings.Contains(asString(contentItem["text"]), "Create 3 distinct final images") {
		t.Fatalf("input content = %#v, want distinct final image request", inputItem["content"])
	}
	tools := asSlice(decoded["tools"])
	if len(tools) != 1 {
		t.Fatalf("tools = %#v", decoded["tools"])
	}
	tool, ok := tools[0].(map[string]any)
	if !ok || intFromAny(tool["partial_images"], -1) != 3 {
		t.Fatalf("image tool = %#v, want partial_images clamped to 3", tool)
	}
}

func TestParseImageGenerationResultReturnsAllCompletedCalls(t *testing.T) {
	payload := base64.StdEncoding.EncodeToString(testPNGBytes())
	result, err := parseImageGenerationResult(map[string]any{
		"id":    "resp_multi",
		"model": "gpt-5.5",
		"output": []any{
			map[string]any{
				"type":           "image_generation_call",
				"id":             "ig_1",
				"status":         "completed",
				"revised_prompt": "first",
				"result":         payload,
			},
			map[string]any{
				"type":           "image_generation_call",
				"id":             "ig_2",
				"status":         "completed",
				"revised_prompt": "second",
				"result":         payload,
			},
			map[string]any{
				"type":           "image_generation_call",
				"id":             "ig_3",
				"status":         "completed",
				"revised_prompt": "third",
				"result":         payload,
			},
		},
	})
	if err != nil {
		t.Fatalf("parseImageGenerationResult: %v", err)
	}
	if len(result.Results) != 3 {
		t.Fatalf("results len = %d, want 3", len(result.Results))
	}
	for i, image := range result.Results {
		if image.CallID == "" || image.OutputIndex != i || !looksLikePNG(image.DecodedPNG) {
			t.Fatalf("result[%d] = %#v", i, image)
		}
	}
	if result.CallID != "ig_1" || result.RevisedPrompt != "first" {
		t.Fatalf("primary result = %#v, want first output for compatibility", result)
	}
	summary := asSlice(result.ProviderResponse["image_generation_results"])
	if len(summary) != 3 {
		t.Fatalf("provider response image_generation_results = %#v, want 3 metadata entries", result.ProviderResponse["image_generation_results"])
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

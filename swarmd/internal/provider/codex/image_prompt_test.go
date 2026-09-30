package codex

import (
	"encoding/json"
	"strings"
	"testing"
)

// Purpose: buildImageGenerationPayload must preserve the original single-image
// user prompt; output-size transport instructions cannot rewrite that text.
// The payload encoder is the narrowest boundary proving serialized fidelity and
// blank-prompt rejection before any provider request can occur.
func TestImagePayloadPreservesOriginalPrompt(t *testing.T) {
	prompt := "  café\nwith blue chairs  "
	for _, size := range []string{"", "auto", "1024x1024"} {
		payload, err := buildImageGenerationPayload(ImageGenerationRequest{Model: "image-model", Prompt: prompt, Size: size, Count: 1})
		if err != nil {
			t.Fatal(err)
		}
		var body struct {
			Input []struct {
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"input"`
			Instructions string `json:"instructions"`
		}
		if err := json.Unmarshal(payload, &body); err != nil {
			t.Fatal(err)
		}
		if len(body.Input) != 1 || len(body.Input[0].Content) != 1 || body.Input[0].Content[0].Text != prompt {
			t.Fatalf("original prompt changed: %s", payload)
		}
		if size == "1024x1024" && !strings.Contains(body.Instructions, "Requested output size: 1024x1024.") {
			t.Fatal("output-size instruction lost")
		}
	}
	if payload, err := buildImageGenerationPayload(ImageGenerationRequest{Model: "image-model", Prompt: " \n\t "}); err == nil || len(payload) != 0 {
		t.Fatal("blank prompt produced provider payload")
	}
}

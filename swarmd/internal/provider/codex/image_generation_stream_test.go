package codex

import (
	"encoding/base64"
	"testing"
)

func TestParseEventStreamReaderMergesGeneratingAndCompletedImageGenerationCall(t *testing.T) {
	payload := base64.StdEncoding.EncodeToString(testPNGBytes())
	stream := "event: response.output_item.added\n" +
		"data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"image_generation_call\",\"id\":\"ig_1\",\"status\":\"generating\"}}\n\n" +
		"event: response.output_item.done\n" +
		"data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"image_generation_call\",\"id\":\"ig_1\",\"status\":\"completed\",\"revised_prompt\":\"final prompt\",\"result\":\"" + payload + "\"}}\n\n" +
		"event: response.completed\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"model\":\"gpt-image-1\",\"output\":[]}}\n\n"

	decoded, err := parseEventStream([]byte(stream))
	if err != nil {
		t.Fatalf("parseEventStream: %v", err)
	}
	result, err := parseImageGenerationResult(decoded)
	if err != nil {
		t.Fatalf("parseImageGenerationResult: %v", err)
	}
	if result.ResponseID != "resp_1" || result.Model != "gpt-image-1" || result.CallID != "ig_1" || result.RevisedPrompt != "final prompt" || !looksLikePNG(result.DecodedPNG) {
		t.Fatalf("result = %#v", result)
	}
}

func TestParseEventStreamReaderCapturesCompletedImageGenerationResult(t *testing.T) {
	payload := base64.StdEncoding.EncodeToString(testPNGBytes())
	partialPayload := base64.StdEncoding.EncodeToString([]byte("partial-not-final"))
	stream := "event: response.output_item.added\n" +
		"data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"image_generation_call\",\"id\":\"ig_1\",\"status\":\"generating\",\"revised_prompt\":\"final prompt\"}}\n\n" +
		"event: response.image_generation_call.partial_image\n" +
		"data: {\"type\":\"response.image_generation_call.partial_image\",\"item_id\":\"ig_1\",\"output_index\":0,\"partial_image_index\":0,\"partial_image_b64\":\"" + partialPayload + "\",\"sequence_number\":2}\n\n" +
		"event: response.image_generation_call.completed\n" +
		"data: {\"type\":\"response.image_generation_call.completed\",\"item_id\":\"ig_1\",\"output_index\":0,\"sequence_number\":3}\n\n" +
		"event: response.completed\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"model\":\"gpt-image-1\",\"output\":[{\"type\":\"image_generation_call\",\"id\":\"ig_1\",\"status\":\"completed\",\"revised_prompt\":\"final prompt\",\"result\":\"" + payload + "\"}]}}\n\n"

	decoded, err := parseEventStream([]byte(stream))
	if err != nil {
		t.Fatalf("parseEventStream: %v", err)
	}
	result, err := parseImageGenerationResult(decoded)
	if err != nil {
		t.Fatalf("parseImageGenerationResult: %v", err)
	}
	if result.ResponseID != "resp_1" || result.Model != "gpt-image-1" || result.CallID != "ig_1" || result.Base64Image != payload || !looksLikePNG(result.DecodedPNG) {
		t.Fatalf("result = %#v", result)
	}
	if len(result.PartialImages) != 1 || result.PartialImages[0].Base64Image != partialPayload {
		t.Fatalf("partials = %#v", result.PartialImages)
	}
}

func TestParseEventStreamReaderCapturesCompletedNestedImageGenerationResult(t *testing.T) {
	payload := base64.StdEncoding.EncodeToString(testPNGBytes())
	stream := "event: response.output_item.added\n" +
		"data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"image_generation_call\",\"id\":\"ig_1\",\"status\":\"generating\"}}\n\n" +
		"event: response.image_generation_call.completed\n" +
		"data: {\"type\":\"response.image_generation_call.completed\",\"item_id\":\"ig_1\",\"output_index\":0,\"item\":{\"type\":\"image_generation_call\",\"id\":\"ig_1\",\"status\":\"completed\",\"result\":\"" + payload + "\"}}\n\n" +
		"event: response.completed\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"model\":\"gpt-image-1\",\"output\":[]}}\n\n"

	decoded, err := parseEventStream([]byte(stream))
	if err != nil {
		t.Fatalf("parseEventStream: %v", err)
	}
	result, err := parseImageGenerationResult(decoded)
	if err != nil {
		t.Fatalf("parseImageGenerationResult: %v", err)
	}
	if result.CallID != "ig_1" || result.Base64Image != payload || !looksLikePNG(result.DecodedPNG) {
		t.Fatalf("result = %#v", result)
	}
}

func TestParseEventStreamReaderCapturesStandaloneImageCompletedEventB64JSON(t *testing.T) {
	payload := base64.StdEncoding.EncodeToString(testPNGBytes())
	stream := "event: image_generation.completed\n" +
		"data: {\"type\":\"image_generation.completed\",\"id\":\"ig_1\",\"output_index\":0,\"sequence_number\":3,\"b64_json\":\"" + payload + "\",\"output_format\":\"png\"}\n\n" +
		"event: response.completed\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"model\":\"gpt-image-1\",\"output\":[]}}\n\n"

	decoded, err := parseEventStream([]byte(stream))
	if err != nil {
		t.Fatalf("parseEventStream: %v", err)
	}
	result, err := parseImageGenerationResult(decoded)
	if err != nil {
		t.Fatalf("parseImageGenerationResult: %v", err)
	}
	if result.CallID != "ig_1" || result.Base64Image != payload || !looksLikePNG(result.DecodedPNG) {
		t.Fatalf("result = %#v", result)
	}
}

func TestParseEventStreamReaderCapturesMultipleCompletedImageGenerationResults(t *testing.T) {
	payload := base64.StdEncoding.EncodeToString(testPNGBytes())
	stream := "event: response.image_generation_call.completed\n" +
		"data: {\"type\":\"response.image_generation_call.completed\",\"item_id\":\"ig_1\",\"output_index\":0,\"sequence_number\":3}\n\n" +
		"event: response.image_generation_call.completed\n" +
		"data: {\"type\":\"response.image_generation_call.completed\",\"item_id\":\"ig_2\",\"output_index\":1,\"sequence_number\":6}\n\n" +
		"event: response.image_generation_call.completed\n" +
		"data: {\"type\":\"response.image_generation_call.completed\",\"item_id\":\"ig_3\",\"output_index\":2,\"sequence_number\":9}\n\n" +
		"event: response.completed\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"model\":\"gpt-image-1\",\"output\":[{\"type\":\"image_generation_call\",\"id\":\"ig_1\",\"status\":\"completed\",\"result\":\"" + payload + "\"},{\"type\":\"image_generation_call\",\"id\":\"ig_2\",\"status\":\"completed\",\"result\":\"" + payload + "\"},{\"type\":\"image_generation_call\",\"id\":\"ig_3\",\"status\":\"completed\",\"result\":\"" + payload + "\"}]}}\n\n"

	decoded, err := parseEventStream([]byte(stream))
	if err != nil {
		t.Fatalf("parseEventStream: %v", err)
	}
	result, err := parseImageGenerationResult(decoded)
	if err != nil {
		t.Fatalf("parseImageGenerationResult: %v", err)
	}
	if len(result.Results) != 3 {
		t.Fatalf("results len = %d, want 3", len(result.Results))
	}
	expectedCallIDs := []string{"ig_1", "ig_2", "ig_3"}
	for i, image := range result.Results {
		if image.CallID != expectedCallIDs[i] || image.OutputIndex != i || image.Base64Image != payload || !looksLikePNG(image.DecodedPNG) {
			t.Fatalf("result[%d] = %#v", i, image)
		}
	}
}

func TestParseEventStreamReaderExposesPartialImageWithoutUsingItAsFinalImage(t *testing.T) {
	payload := base64.StdEncoding.EncodeToString([]byte("partial-not-final"))
	stream := "event: response.output_item.added\n" +
		"data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"image_generation_call\",\"id\":\"ig_1\",\"status\":\"generating\",\"revised_prompt\":\"final prompt\"}}\n\n" +
		"event: response.image_generation_call.generating\n" +
		"data: {\"type\":\"response.image_generation_call.generating\",\"item_id\":\"ig_1\",\"output_index\":0,\"sequence_number\":1}\n\n" +
		"event: response.image_generation_call.partial_image\n" +
		"data: {\"type\":\"response.image_generation_call.partial_image\",\"item_id\":\"ig_1\",\"output_index\":0,\"partial_image_index\":0,\"partial_image_b64\":\"" + payload + "\",\"sequence_number\":2}\n\n" +
		"event: response.image_generation_call.completed\n" +
		"data: {\"type\":\"response.image_generation_call.completed\",\"item_id\":\"ig_1\",\"output_index\":0,\"sequence_number\":3}\n\n" +
		"event: response.completed\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"model\":\"gpt-image-1\",\"output\":[]}}\n\n"

	decoded, err := parseEventStream([]byte(stream))
	if err != nil {
		t.Fatalf("parseEventStream: %v", err)
	}
	result, err := parseImageGenerationResult(decoded)
	if err != nil {
		t.Fatalf("parseImageGenerationResult: %v", err)
	}
	if len(result.PartialImages) != 1 || result.PartialImages[0].Base64Image != payload || result.Base64Image != "" || len(result.DecodedPNG) != 0 {
		t.Fatalf("result = %#v, want partial image exposed but no final image promoted", result)
	}
	if rawEvents := asSlice(decoded["raw_events"]); len(rawEvents) == 0 {
		t.Fatalf("decoded raw_events = %#v, want original SSE events preserved", decoded["raw_events"])
	}
}

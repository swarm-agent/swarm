package codex

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func testMediaPayload(modality, mimeType, fileType string, body []byte) provideriface.SessionMediaPayload {
	digest := sha256.Sum256(body)
	return provideriface.SessionMediaPayload{
		AssetID:      "media_test",
		Modality:     modality,
		MIMEType:     mimeType,
		FileType:     fileType,
		DigestSHA256: hex.EncodeToString(digest[:]),
		Size:         int64(len(body)),
		Bytes:        body,
	}
}

func testMediaRequest(contract provideriface.SessionMediaContract, payload provideriface.SessionMediaPayload) Request {
	return Request{
		Model:                     "gpt-test",
		ProviderConfigurationHash: "configuration-hash",
		MediaContract:             contract,
		Input: []map[string]any{{
			"role": "user",
			"content": []map[string]any{
				{"type": "input_text", "text": "inspect"},
				{"type": "session_media", "media": payload},
			},
		}},
	}
}

func allowedMediaContract(providerID, providerSurface, credentialSurface, adapterID string, capability provideriface.MediaContractCapability) provideriface.SessionMediaContract {
	return provideriface.SessionMediaContract{
		ProviderID: providerID, ProviderSurface: providerSurface, CredentialSurface: credentialSurface,
		AdapterID: adapterID, Hash: "contract-hash", Capabilities: []provideriface.MediaContractCapability{capability},
	}
}

func TestBuildRequestPayloadMaterializesOpenAIImageAndPDF(t *testing.T) {
	tests := []struct {
		name       string
		payload    provideriface.SessionMediaPayload
		capability provideriface.MediaContractCapability
		wantType   string
		wantField  string
		wantPrefix string
	}{
		{
			name: "image", payload: testMediaPayload("image", "image/png", "", []byte("image-bytes")),
			capability: provideriface.MediaContractCapability{Modality: "image", State: provideriface.MediaCapabilityStateAllowed, Semantics: pebblestore.ModelCatalogMediaSemanticsNative, MIMETypes: []string{"image/png"}, ContentTypes: []string{"input_image"}, MaxBytes: 1024, MaxCount: 1},
			wantType:   "input_image", wantField: "image_url", wantPrefix: "data:image/png;base64,",
		},
		{
			name: "pdf", payload: testMediaPayload("pdf", "application/pdf", "pdf", []byte("%PDF-1.7\n")),
			capability: provideriface.MediaContractCapability{Modality: "pdf", State: provideriface.MediaCapabilityStateAllowed, Semantics: pebblestore.ModelCatalogMediaSemanticsProviderProcessed, MIMETypes: []string{"application/pdf"}, FileTypes: []string{"pdf"}, ContentTypes: []string{"input_file"}, MaxBytes: 1024, MaxCount: 1},
			wantType:   "input_file", wantField: "file_data", wantPrefix: "data:application/pdf;base64,",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			contract := allowedMediaContract("openai", "responses_api", "openai_api_key", "openai-responses-v1", test.capability)
			encoded, err := buildRequestPayload(testMediaRequest(contract, test.payload))
			if err != nil {
				t.Fatalf("buildRequestPayload: %v", err)
			}
			var body map[string]any
			if err := json.Unmarshal(encoded, &body); err != nil {
				t.Fatalf("decode payload: %v", err)
			}
			content := asSlice(asSlice(body["input"])[0].(map[string]any)["content"])
			media := content[1].(map[string]any)
			if media["type"] != test.wantType || !strings.HasPrefix(asString(media[test.wantField]), test.wantPrefix) {
				t.Fatalf("media payload = %#v", media)
			}
			if strings.Contains(string(encoded), test.payload.AssetID) || strings.Contains(string(encoded), test.payload.DigestSHA256) {
				t.Fatal("provider payload disclosed internal media identity")
			}
		})
	}
}

func TestMaterializeSessionMediaInputBoundsCodexFullReplayToNewestImages(t *testing.T) {
	contract := allowedMediaContract("codex", "chatgpt_codex", "codex_oauth", "codex-chatgpt-v1", provideriface.MediaContractCapability{
		Modality: "image", State: provideriface.MediaCapabilityStateAllowed, Semantics: pebblestore.ModelCatalogMediaSemanticsNative,
		MIMETypes: []string{"image/png"}, ContentTypes: []string{"input_image"}, MaxBytes: 1024, MaxCount: 20,
	})
	input := make([]map[string]any, 0, 3)
	for _, body := range [][]byte{[]byte("old1"), []byte("new2"), []byte("new3")} {
		input = append(input, map[string]any{
			"role":    "user",
			"content": []map[string]any{{"type": "session_media", "media": testMediaPayload("image", "image/png", "", body)}},
		})
	}

	// The assistant has already consumed the historical images.
	input = append(input, map[string]any{"role": "assistant", "content": "reviewed"})
	materialized, err := materializeSessionMediaInputWithReplayBudget(Request{
		ProviderConfigurationHash: "configuration-hash",
		MediaContract:             contract,
	}, input, 10)
	if err != nil {
		t.Fatalf("materializeSessionMediaInputWithReplayBudget: %v", err)
	}
	if len(materialized) != 4 {
		t.Fatalf("materialized input count = %d, want 4", len(materialized))
	}
	oldContent, _ := inputContentMaps(materialized[0]["content"])
	if len(oldContent) != 1 || oldContent[0]["type"] != "input_text" || !strings.Contains(asString(oldContent[0]["text"]), "omitted") {
		t.Fatalf("old replay media was not replaced by an explicit marker: %#v", materialized[0])
	}
	for i := 1; i < 3; i++ {
		content, _ := inputContentMaps(materialized[i]["content"])
		if len(content) != 1 || content[0]["type"] != "input_image" || !strings.HasPrefix(asString(content[0]["image_url"]), "data:image/png;base64,") {
			t.Fatalf("new replay media %d was not retained: %#v", i, materialized[i])
		}
	}
}

func TestBuildRequestPayloadAcceptsImageExtensionWhenMIMEIsAuthoritative(t *testing.T) {
	payload := testMediaPayload("image", "image/png", "png", []byte("image-bytes"))
	capability := provideriface.MediaContractCapability{Modality: "image", State: provideriface.MediaCapabilityStateAllowed, Semantics: pebblestore.ModelCatalogMediaSemanticsNative, MIMETypes: []string{"image/png"}, ContentTypes: []string{"input_image"}, MaxBytes: 1024, MaxCount: 1}
	contract := allowedMediaContract("codex", "chatgpt_codex", "codex_oauth", "codex-chatgpt-v1", capability)
	if _, err := buildRequestPayload(testMediaRequest(contract, payload)); err != nil {
		t.Fatalf("MIME-authoritative image contract rejected filename extension: %v", err)
	}
}

func TestBuildRequestPayloadMaterializesCodexOAuthImageOnly(t *testing.T) {
	payload := testMediaPayload("image", "image/jpeg", "", []byte("jpeg-bytes"))
	capability := provideriface.MediaContractCapability{Modality: "image", State: provideriface.MediaCapabilityStateAllowed, Semantics: pebblestore.ModelCatalogMediaSemanticsNative, MIMETypes: []string{"image/jpeg"}, ContentTypes: []string{"input_image"}, MaxBytes: 1024, MaxCount: 1}
	contract := allowedMediaContract("codex", "chatgpt_codex", "codex_oauth", "codex-chatgpt-v1", capability)
	encoded, err := buildRequestPayload(testMediaRequest(contract, payload))
	if err != nil {
		t.Fatalf("buildRequestPayload: %v", err)
	}
	if !strings.Contains(string(encoded), `"type":"input_image"`) || !strings.Contains(string(encoded), `data:image/jpeg;base64,`) {
		t.Fatalf("Codex image payload not materialized: %s", encoded)
	}
}

func TestBuildRequestPayloadRejectsCrossSurfaceUnsupportedAndForgedMedia(t *testing.T) {
	image := testMediaPayload("image", "image/png", "", []byte("image-bytes"))
	imageCapability := provideriface.MediaContractCapability{Modality: "image", State: provideriface.MediaCapabilityStateAllowed, Semantics: pebblestore.ModelCatalogMediaSemanticsNative, MIMETypes: []string{"image/png"}, ContentTypes: []string{"input_image"}, MaxBytes: 1024, MaxCount: 1}
	openAI := allowedMediaContract("openai", "responses_api", "openai_api_key", "openai-responses-v1", imageCapability)
	codex := allowedMediaContract("codex", "chatgpt_codex", "codex_oauth", "codex-chatgpt-v1", imageCapability)

	tests := []struct {
		name     string
		contract provideriface.SessionMediaContract
		payload  provideriface.SessionMediaPayload
	}{
		{name: "cross surface", contract: allowedMediaContract("openai", "chatgpt_codex", "codex_oauth", "codex-chatgpt-v1", imageCapability), payload: image},
		{name: "unsupported MIME", contract: openAI, payload: testMediaPayload("image", "image/svg+xml", "", []byte("svg"))},
		{name: "Codex MIME denied by compiled contract", contract: codex, payload: testMediaPayload("image", "image/gif", "", []byte("gif"))},
		{name: "undeclared provider", contract: allowedMediaContract("anthropic", "messages", "api_key", "anthropic", imageCapability), payload: image},
		{name: "forged digest", contract: openAI, payload: func() provideriface.SessionMediaPayload {
			forged := image
			forged.DigestSHA256 = strings.Repeat("0", 64)
			return forged
		}()},
		{name: "empty contract", contract: provideriface.SessionMediaContract{}, payload: image},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := buildRequestPayload(testMediaRequest(test.contract, test.payload))
			if err == nil {
				t.Fatal("buildRequestPayload error = nil")
			}
			if strings.Contains(err.Error(), string(test.payload.Bytes)) || strings.Contains(err.Error(), test.payload.AssetID) || strings.Contains(err.Error(), test.payload.DigestSHA256) {
				t.Fatalf("error disclosed sensitive media material: %v", err)
			}
		})
	}
}

func TestBuildRequestPayloadRejectsCodexClientProcessedFileWithoutMapper(t *testing.T) {
	payload := testMediaPayload("pdf", "application/pdf", "pdf", []byte("%PDF-1.7\n"))
	capability := provideriface.MediaContractCapability{Modality: "pdf", State: provideriface.MediaCapabilityStateAllowed, Semantics: pebblestore.ModelCatalogMediaSemanticsClientProcessed, MIMETypes: []string{"application/pdf"}, FileTypes: []string{"pdf"}, ContentTypes: []string{"input_file"}, MaxBytes: 1024, MaxCount: 1}
	contract := allowedMediaContract("codex", "chatgpt_codex", "codex_oauth", "codex-chatgpt-v1", capability)
	if _, err := buildRequestPayload(testMediaRequest(contract, payload)); err == nil || !strings.Contains(err.Error(), "not implemented") {
		t.Fatalf("buildRequestPayload error = %v, want fail-closed client-processing error", err)
	}
}

func TestMediaContractParticipatesInConvertedContinuationIdentity(t *testing.T) {
	contract := provideriface.SessionMediaContract{ProviderID: "openai", Hash: "contract-new"}
	converted := ToRequest(provideriface.Request{ProviderConfigurationHash: "configuration-new", MediaContract: contract})
	if converted.ProviderConfigurationHash != "configuration-new" || converted.MediaContract.Hash != "contract-new" {
		t.Fatalf("converted request lost media continuation identity: %+v", converted)
	}
}

func TestValidateRunnerMediaSurfaceRejectsOpenAIAndAllowsTextOnly(t *testing.T) {
	if err := validateRunnerMediaSurface(provideriface.SessionMediaContract{}, "codex", "chatgpt_codex", "codex_oauth", "codex-chatgpt-v1"); err != nil {
		t.Fatalf("text-only contract rejected: %v", err)
	}
	openAI := provideriface.SessionMediaContract{ProviderID: "openai", ProviderSurface: "responses_api", CredentialSurface: "openai_api_key", AdapterID: "openai-responses-v1", Hash: "hash"}
	if err := validateRunnerMediaSurface(openAI, "codex", "chatgpt_codex", "codex_oauth", "codex-chatgpt-v1"); err == nil {
		t.Fatal("OpenAI media contract accepted by Codex runner")
	}
}

// TestMaterializeSessionMediaInputBoundsCodexFullReplayToNewestImages21Retains20 verifies
// that full replay bounded media materialization prunes older historical images when the
// cumulative image count exceeds the active contract limit (e.g. 21+ images with cap 20),
// retaining the 20 newest images as native pixels and replacing older ones with an omission marker.
// Requirement: Codex full replay must bound media replay by both byte budget and count limit.
// Threat/Regression: Multi-turn media inspection runs (e.g. 21 image inspections totalling <20MiB)
// failed with "provider media payload exceeds the current contract count limit" on reconnect.
// Symbols: materializeSessionMediaInputWithReplayBudget, validateProviderMediaPayloadItem in client.go.
// Narrowest layer: Direct unit test on materializeSessionMediaInputWithReplayBudget with cap 20.
func TestMaterializeSessionMediaInputBoundsCodexFullReplayToNewestImages21Retains20(t *testing.T) {
	contract := allowedMediaContract("codex", "chatgpt_codex", "codex_oauth", "codex-chatgpt-v1", provideriface.MediaContractCapability{
		Modality: "image", State: provideriface.MediaCapabilityStateAllowed, Semantics: pebblestore.ModelCatalogMediaSemanticsNative,
		MIMETypes: []string{"image/png"}, ContentTypes: []string{"input_image"}, MaxBytes: 1024, MaxCount: 20,
	})
	input := make([]map[string]any, 0, 22)
	for i := 0; i < 22; i++ {
		input = append(input, map[string]any{
			"role":    "user",
			"content": []map[string]any{{"type": "session_media", "media": testMediaPayload("image", "image/png", "", []byte(fmt.Sprintf("img-%02d", i)))}},
		})
	}

	input = append(input, map[string]any{"role": "assistant", "content": "reviewed"})
	original, _ := json.Marshal(input)
	materialized, err := materializeSessionMediaInputWithReplayBudget(Request{
		ProviderConfigurationHash: "configuration-hash",
		MediaContract:             contract,
	}, input, maxCodexFullReplayMediaBytes)
	if err != nil {
		t.Fatalf("materializeSessionMediaInputWithReplayBudget: %v", err)
	}
	after, _ := json.Marshal(input)
	if string(original) != string(after) {
		t.Fatal("materialization mutated original media history")
	}
	if len(materialized) != 23 {
		t.Fatalf("materialized input count = %d, want 23", len(materialized))
	}
	// With 22 images and cap 20, the 2 oldest images (0 and 1) should be replaced with omission markers.
	for i := 0; i < 2; i++ {
		content, _ := inputContentMaps(materialized[i]["content"])
		if len(content) != 1 || content[0]["type"] != "input_text" || !strings.Contains(asString(content[0]["text"]), "omitted") {
			t.Fatalf("old replay media %d was not replaced by omission marker: %#v", i, materialized[i])
		}
	}
	// The 20 newest images (2 through 21) must be retained as input_image.
	for i := 2; i < 22; i++ {
		content, _ := inputContentMaps(materialized[i]["content"])
		if len(content) != 1 || content[0]["type"] != "input_image" || !strings.HasPrefix(asString(content[0]["image_url"]), "data:image/png;base64,") {
			t.Fatalf("new replay media %d was not retained: %#v", i, materialized[i])
		}
	}
}

// TestMaterializeSessionMediaInputExactCountAndByteBoundaries verifies exact boundary behavior
// for both count limits and byte budgets without off-by-one errors.
// Requirement: Replay materialization must respect exact count and byte boundaries without off-by-one errors.
// Threat/Regression: Off-by-one errors could omit valid images prematurely or allow over-limit payloads.
// Symbols: materializeSessionMediaInputWithReplayBudget in client.go.
// Narrowest layer: Parameterized unit test verifying exact boundary cases (20 images at cap 20; 2 images matching byte budget; dual limits).
func TestMaterializeSessionMediaInputExactCountAndByteBoundaries(t *testing.T) {
	contract := allowedMediaContract("codex", "chatgpt_codex", "codex_oauth", "codex-chatgpt-v1", provideriface.MediaContractCapability{
		Modality: "image", State: provideriface.MediaCapabilityStateAllowed, Semantics: pebblestore.ModelCatalogMediaSemanticsNative,
		MIMETypes: []string{"image/png"}, ContentTypes: []string{"input_image"}, MaxBytes: 1024, MaxCount: 20,
	})

	t.Run("exact count boundary 20 retains all 20", func(t *testing.T) {
		input := make([]map[string]any, 0, 20)
		for i := 0; i < 20; i++ {
			input = append(input, map[string]any{
				"role":    "user",
				"content": []map[string]any{{"type": "session_media", "media": testMediaPayload("image", "image/png", "", []byte(fmt.Sprintf("img-%02d", i)))}},
			})
		}
		materialized, err := materializeSessionMediaInputWithReplayBudget(Request{
			ProviderConfigurationHash: "configuration-hash",
			MediaContract:             contract,
		}, input, maxCodexFullReplayMediaBytes)
		if err != nil {
			t.Fatalf("materializeSessionMediaInputWithReplayBudget: %v", err)
		}
		for i := 0; i < 20; i++ {
			content, _ := inputContentMaps(materialized[i]["content"])
			if len(content) != 1 || content[0]["type"] != "input_image" {
				t.Fatalf("media %d was not retained at exact count boundary: %#v", i, materialized[i])
			}
		}
	})

	t.Run("exact byte budget boundary retains all", func(t *testing.T) {
		input := []map[string]any{
			{"role": "user", "content": []map[string]any{{"type": "session_media", "media": testMediaPayload("image", "image/png", "", []byte("12345"))}}},
			{"role": "user", "content": []map[string]any{{"type": "session_media", "media": testMediaPayload("image", "image/png", "", []byte("67890"))}}},
		}
		materialized, err := materializeSessionMediaInputWithReplayBudget(Request{
			ProviderConfigurationHash: "configuration-hash",
			MediaContract:             contract,
		}, input, 10)
		if err != nil {
			t.Fatalf("materializeSessionMediaInputWithReplayBudget: %v", err)
		}
		for i := 0; i < 2; i++ {
			content, _ := inputContentMaps(materialized[i]["content"])
			if len(content) != 1 || content[0]["type"] != "input_image" {
				t.Fatalf("media %d was omitted at exact byte boundary: %#v", i, materialized[i])
			}
		}
	})

	t.Run("dual budget boundary prunes to satisfy both constraints", func(t *testing.T) {
		// 21 images of 1 byte each; cap is 20, byte budget is 10.
		// To satisfy both count <= 20 and bytes <= 10, oldest 11 must be omitted, newest 10 retained.
		input := make([]map[string]any, 0, 21)
		for i := 0; i < 21; i++ {
			input = append(input, map[string]any{
				"role":    "user",
				"content": []map[string]any{{"type": "session_media", "media": testMediaPayload("image", "image/png", "", []byte("a"))}},
			})
		}
		input = append(input, map[string]any{"role": "assistant", "content": "reviewed"})
		materialized, err := materializeSessionMediaInputWithReplayBudget(Request{
			ProviderConfigurationHash: "configuration-hash",
			MediaContract:             contract,
		}, input, 10)
		if err != nil {
			t.Fatalf("materializeSessionMediaInputWithReplayBudget: %v", err)
		}
		for i := 0; i < 11; i++ {
			content, _ := inputContentMaps(materialized[i]["content"])
			if content[0]["type"] != "input_text" || !strings.Contains(asString(content[0]["text"]), "omitted") {
				t.Fatalf("media %d was not omitted under dual budget: %#v", i, materialized[i])
			}
		}
		for i := 11; i < 21; i++ {
			content, _ := inputContentMaps(materialized[i]["content"])
			if content[0]["type"] != "input_image" {
				t.Fatalf("media %d was not retained under dual budget: %#v", i, materialized[i])
			}
		}
	})
}

// TestMaterializeSessionMediaInputPreservesMediaTextAndToolOrdering verifies that nonmedia
// parts, assistant tool calls, and text ordering are fully preserved during media replay materialization.
// Requirement: Replay materialization must preserve the relative order of text parts, media parts,
// and nonmedia messages in-place without displacement.
// Threat/Regression: Normalization could displace adjacent user text or break tool pairings during omission.
// Symbols: materializeSessionMediaInputWithReplayBudget in client.go.
// Narrowest layer: Unit test verifying mixed sequence of user text, tool call, tool output with media, and follow-up.
func TestMaterializeSessionMediaInputPreservesMediaTextAndToolOrdering(t *testing.T) {
	contract := allowedMediaContract("codex", "chatgpt_codex", "codex_oauth", "codex-chatgpt-v1", provideriface.MediaContractCapability{
		Modality: "image", State: provideriface.MediaCapabilityStateAllowed, Semantics: pebblestore.ModelCatalogMediaSemanticsNative,
		MIMETypes: []string{"image/png"}, ContentTypes: []string{"input_image"}, MaxBytes: 1024, MaxCount: 1,
	})
	input := []map[string]any{
		{
			"role": "user",
			"content": []map[string]any{
				{"type": "input_text", "text": "initial user prompt"},
				{"type": "session_media", "media": testMediaPayload("image", "image/png", "", []byte("old-image"))},
			},
		},
		{
			"role": "assistant",
			"tool_calls": []map[string]any{
				{"id": "call_1", "type": "function", "function": map[string]any{"name": "media_inspect", "arguments": `{"path":"image.png"}`}},
			},
		},
		{
			"role":         "tool",
			"tool_call_id": "call_1",
			"content":      "inspection complete",
		},
		{
			"role": "user",
			"content": []map[string]any{
				{"type": "session_media", "media": testMediaPayload("image", "image/png", "", []byte("new-image"))},
				{"type": "input_text", "text": "user review question"},
			},
		},
	}

	materialized, err := materializeSessionMediaInputWithReplayBudget(Request{
		ProviderConfigurationHash: "configuration-hash",
		MediaContract:             contract,
	}, input, maxCodexFullReplayMediaBytes)
	if err != nil {
		t.Fatalf("materializeSessionMediaInputWithReplayBudget: %v", err)
	}
	if len(materialized) != 4 {
		t.Fatalf("materialized length = %d, want 4", len(materialized))
	}

	// Message 0: text part first, omission marker second.
	m0Content, _ := inputContentMaps(materialized[0]["content"])
	if len(m0Content) != 2 || m0Content[0]["type"] != "input_text" || m0Content[0]["text"] != "initial user prompt" {
		t.Fatalf("m0 part 0 not preserved: %#v", m0Content[0])
	}
	if m0Content[1]["type"] != "input_text" || !strings.Contains(asString(m0Content[1]["text"]), "omitted") {
		t.Fatalf("m0 part 1 not omission marker: %#v", m0Content[1])
	}

	// Message 1: assistant tool calls preserved.
	calls, ok := materialized[1]["tool_calls"].([]map[string]any)
	if asString(materialized[1]["role"]) != "assistant" || !ok || len(calls) != 1 || calls[0]["id"] != "call_1" {
		t.Fatalf("m1 assistant tool call not preserved: %#v", materialized[1])
	}

	// Message 2: tool output preserved.
	if asString(materialized[2]["role"]) != "tool" || asString(materialized[2]["content"]) != "inspection complete" {
		t.Fatalf("m2 tool response not preserved: %#v", materialized[2])
	}

	// Message 3: retained media first, user review text second.
	m3Content, _ := inputContentMaps(materialized[3]["content"])
	if len(m3Content) != 2 || m3Content[0]["type"] != "input_image" {
		t.Fatalf("m3 part 0 not input_image: %#v", m3Content[0])
	}
	if m3Content[1]["type"] != "input_text" || m3Content[1]["text"] != "user review question" {
		t.Fatalf("m3 part 1 not user review question: %#v", m3Content[1])
	}
}

// TestMaterializeSessionMediaInputRejectsInvalidForgedAndOversizedMediaOldAndNew verifies that
// upfront validation rejects forged digests, unsupported MIME types, oversized payloads (> MaxBytes),
// and malformed inputs whether they occur on older historical images or newest retained images.
// Requirement: Upfront media validation must reject forged, unsupported, and oversized payloads across all positions.
// Threat/Regression: Silent omission of older images without validation could mask forged media attacks; silently
// dropping invalid newest images would corrupt current turn processing.
// Symbols: validateProviderMediaPayloadItem, materializeSessionMediaInputWithReplayBudget in client.go.
// Narrowest layer: Parameterized unit test verifying fail-closed rejection across old and new positions.
func TestMaterializeSessionMediaInputRejectsInvalidForgedAndOversizedMediaOldAndNew(t *testing.T) {
	contract := allowedMediaContract("codex", "chatgpt_codex", "codex_oauth", "codex-chatgpt-v1", provideriface.MediaContractCapability{
		Modality: "image", State: provideriface.MediaCapabilityStateAllowed, Semantics: pebblestore.ModelCatalogMediaSemanticsNative,
		MIMETypes: []string{"image/png"}, ContentTypes: []string{"input_image"}, MaxBytes: 1024, MaxCount: 20,
	})

	validPayload := testMediaPayload("image", "image/png", "", []byte("valid-png-data"))

	forgedPayload := testMediaPayload("image", "image/png", "", []byte("forged-png-data"))
	forgedPayload.DigestSHA256 = strings.Repeat("0", 64)

	unsupportedMIME := testMediaPayload("image", "image/gif", "", []byte("gif-data"))

	oversizePayload := testMediaPayload("image", "image/png", "", []byte(strings.Repeat("X", 2048)))

	tests := []struct {
		name       string
		oldMedia   provideriface.SessionMediaPayload
		newMedia   provideriface.SessionMediaPayload
		wantErrSub string
	}{
		{
			name:       "old image forged digest fails before omission",
			oldMedia:   forgedPayload,
			newMedia:   validPayload,
			wantErrSub: "failed immutable digest validation",
		},
		{
			name:       "newest image forged digest fails",
			oldMedia:   validPayload,
			newMedia:   forgedPayload,
			wantErrSub: "failed immutable digest validation",
		},
		{
			name:       "old image unsupported MIME fails before omission",
			oldMedia:   unsupportedMIME,
			newMedia:   validPayload,
			wantErrSub: "denied by the active contract",
		},
		{
			name:       "newest image unsupported MIME fails",
			oldMedia:   validPayload,
			newMedia:   unsupportedMIME,
			wantErrSub: "denied by the active contract",
		},
		{
			name:       "old image oversize fails before omission",
			oldMedia:   oversizePayload,
			newMedia:   validPayload,
			wantErrSub: "denied by the active contract",
		},
		{
			name:       "newest image oversize fails",
			oldMedia:   validPayload,
			newMedia:   oversizePayload,
			wantErrSub: "denied by the active contract",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// 21 messages: message 0 has oldMedia (would be candidate for omission with cap 20),
			// messages 1..19 have validPayload, message 20 has newMedia.
			input := make([]map[string]any, 0, 21)
			input = append(input, map[string]any{
				"role":    "user",
				"content": []map[string]any{{"type": "session_media", "media": tc.oldMedia}},
			})
			for i := 1; i < 20; i++ {
				input = append(input, map[string]any{
					"role":    "user",
					"content": []map[string]any{{"type": "session_media", "media": validPayload}},
				})
			}
			input = append(input, map[string]any{
				"role":    "user",
				"content": []map[string]any{{"type": "session_media", "media": tc.newMedia}},
			})

			input = append(input, map[string]any{"role": "assistant", "content": "reviewed"})
			_, err := materializeSessionMediaInputWithReplayBudget(Request{
				ProviderConfigurationHash: "configuration-hash",
				MediaContract:             contract,
			}, input, maxCodexFullReplayMediaBytes)
			if err == nil {
				t.Fatalf("materializeSessionMediaInputWithReplayBudget returned nil error, want error containing %q", tc.wantErrSub)
			}
			if !strings.Contains(err.Error(), tc.wantErrSub) {
				t.Fatalf("error = %q, want substring %q", err.Error(), tc.wantErrSub)
			}
		})
	}

	t.Run("single message with 21 images fails as over-limit current batch", func(t *testing.T) {
		parts := make([]map[string]any, 0, 21)
		for i := 0; i < 21; i++ {
			parts = append(parts, map[string]any{"type": "session_media", "media": validPayload})
		}
		input := []map[string]any{
			{"role": "user", "content": parts},
		}
		_, err := materializeSessionMediaInputWithReplayBudget(Request{
			ProviderConfigurationHash: "configuration-hash",
			MediaContract:             contract,
		}, input, maxCodexFullReplayMediaBytes)
		if err == nil || !strings.Contains(err.Error(), "exceeds the current contract count limit") {
			t.Fatalf("single message with 21 images error = %v, want exceeds contract count limit", err)
		}
	})
}

// TestMaterializeSessionMediaInputIncrementalCurrentBatchExceedingLimitFails verifies that
// incremental current batches (delta input) exceeding the contract limit fail rather than drop images.
// Requirement: Incremental current batches must fail closed when exceeding count limits instead of dropping images.
// Threat/Regression: Delta continuation could drop new images from active turns if full-replay omission logic was misapplied.
// Symbols: materializeSessionMediaDeltaInput in client.go.
// Narrowest layer: Unit test verifying materializeSessionMediaDeltaInput rejects 21 delta images.
func TestMaterializeSessionMediaInputIncrementalCurrentBatchExceedingLimitFails(t *testing.T) {
	contract := allowedMediaContract("codex", "chatgpt_codex", "codex_oauth", "codex-chatgpt-v1", provideriface.MediaContractCapability{
		Modality: "image", State: provideriface.MediaCapabilityStateAllowed, Semantics: pebblestore.ModelCatalogMediaSemanticsNative,
		MIMETypes: []string{"image/png"}, ContentTypes: []string{"input_image"}, MaxBytes: 1024, MaxCount: 20,
	})
	validPayload := testMediaPayload("image", "image/png", "", []byte("valid-png"))

	// Incremental batch with 21 messages each with 1 image.
	input := make([]map[string]any, 0, 21)
	for i := 0; i < 21; i++ {
		input = append(input, map[string]any{
			"role":    "user",
			"content": []map[string]any{{"type": "session_media", "media": validPayload}},
		})
	}

	_, err := materializeSessionMediaDeltaInput(Request{
		ProviderConfigurationHash: "configuration-hash",
		MediaContract:             contract,
	}, input)
	if err == nil || !strings.Contains(err.Error(), "exceeds the current contract count limit") {
		t.Fatalf("incremental batch with 21 images error = %v, want exceeds contract count limit", err)
	}

	// Exactly 20 images in incremental batch succeeds and retains all 20 without dropping.
	validDelta, err := materializeSessionMediaDeltaInput(Request{
		ProviderConfigurationHash: "configuration-hash",
		MediaContract:             contract,
	}, input[:20])
	if err != nil {
		t.Fatalf("incremental batch with 20 images error: %v", err)
	}
	if len(validDelta) != 20 {
		t.Fatalf("incremental batch retained count = %d, want 20", len(validDelta))
	}
	for i := 0; i < 20; i++ {
		content, _ := inputContentMaps(validDelta[i]["content"])
		if len(content) != 1 || content[0]["type"] != "input_image" {
			t.Fatalf("incremental delta %d was not input_image: %#v", i, validDelta[i])
		}
	}
}

// TestMaterializeSessionMediaInputOpenAIUnchanged verifies that OpenAI provider requests
// do not have Codex replay omission applied and retain all valid images or fail at contract limits.
// Requirement: OpenAI requests must not apply Codex replay omission.
// Threat/Regression: Leaking Codex replay logic into OpenAI requests could alter OpenAI multi-image payloads.
// Symbols: materializeSessionMediaInputWithReplayBudget, boundCodexReplay in client.go.
// Narrowest layer: Unit test in media_transport_test.go confirming that an OpenAI contract with multiple images does not omit older images.
func TestMaterializeSessionMediaInputOpenAIUnchanged(t *testing.T) {
	contract := allowedMediaContract("openai", "responses_api", "openai_api_key", "openai-responses-v1", provideriface.MediaContractCapability{
		Modality: "image", State: provideriface.MediaCapabilityStateAllowed, Semantics: pebblestore.ModelCatalogMediaSemanticsNative,
		MIMETypes: []string{"image/png"}, ContentTypes: []string{"input_image"}, MaxBytes: 1024, MaxCount: 20,
	})
	input := []map[string]any{
		{"role": "user", "content": []map[string]any{{"type": "session_media", "media": testMediaPayload("image", "image/png", "", []byte("first-image"))}}},
		{"role": "user", "content": []map[string]any{{"type": "session_media", "media": testMediaPayload("image", "image/png", "", []byte("second-image"))}}},
	}

	// With replayBudget = 5 (less than total 23 bytes), OpenAI should NOT omit any image because boundCodexReplay is false.
	materialized, err := materializeSessionMediaInputWithReplayBudget(Request{
		ProviderConfigurationHash: "configuration-hash",
		MediaContract:             contract,
	}, input, 5)
	if err != nil {
		t.Fatalf("OpenAI materializeSessionMediaInputWithReplayBudget: %v", err)
	}
	if len(materialized) != 2 {
		t.Fatalf("materialized count = %d, want 2", len(materialized))
	}
	for i := 0; i < 2; i++ {
		content, _ := inputContentMaps(materialized[i]["content"])
		if len(content) != 1 || content[0]["type"] != "input_image" {
			t.Fatalf("OpenAI media %d was omitted or altered: %#v", i, materialized[i])
		}
	}
}

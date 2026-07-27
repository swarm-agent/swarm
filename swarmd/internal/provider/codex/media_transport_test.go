package codex

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

package openrouter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func openRouterTestImage(body []byte, mimeType string) provideriface.SessionMediaPayload {
	digest := sha256.Sum256(body)
	return provideriface.SessionMediaPayload{
		AssetID:      "media_test",
		Modality:     "image",
		MIMEType:     mimeType,
		DigestSHA256: hex.EncodeToString(digest[:]),
		Size:         int64(len(body)),
		Bytes:        body,
	}
}

func openRouterTestContract(capabilities ...provideriface.MediaContractCapability) provideriface.SessionMediaContract {
	return provideriface.SessionMediaContract{
		ProviderID:        "openrouter",
		ProviderSurface:   openRouterMediaProviderSurface,
		CredentialSurface: openRouterMediaCredentialSurface,
		AdapterID:         openRouterMediaAdapterID,
		Hash:              "contract-hash",
		Capabilities:      capabilities,
	}
}

func openRouterImageCapability(maxCount int) provideriface.MediaContractCapability {
	return provideriface.MediaContractCapability{
		Modality:     "image",
		State:        provideriface.MediaCapabilityStateAllowed,
		Semantics:    pebblestore.ModelCatalogMediaSemanticsNative,
		MIMETypes:    append([]string(nil), openRouterImageMIMETypes...),
		ContentTypes: []string{"image_url"},
		MaxBytes:     openRouterMaxImageBytes,
		MaxCount:     maxCount,
	}
}

func openRouterMediaRequest(payloads ...provideriface.SessionMediaPayload) provideriface.Request {
	content := []map[string]any{{"type": "input_text", "text": "inspect"}}
	for _, payload := range payloads {
		content = append(content, map[string]any{"type": "session_media", "media": payload})
	}
	return provideriface.Request{
		Model:                     "openai/gpt-test",
		ProviderConfigurationHash: "configuration-hash",
		MediaContract:             openRouterTestContract(openRouterImageCapability(openRouterMaxImageCount)),
		Input:                     []map[string]any{{"role": "user", "content": content}},
	}
}

func TestOpenRouterMediaDeclarationCeiling(t *testing.T) {
	var _ provideriface.MediaCapabilityRunner = (*Runner)(nil)
	declaration := openRouterMediaDeclaration(pebblestore.AuthCredentialRecord{
		AccountScopeID: "account", Provider: "openrouter", ID: "credential", Type: pebblestore.AuthTypeAPI,
	})
	if declaration.AdapterID != openRouterMediaAdapterID || declaration.ProviderID != "openrouter" || declaration.ProviderSurface != "chat_completions" || declaration.CredentialSurface != "openrouter_api_key" || declaration.CredentialFingerprint == "" {
		t.Fatalf("media declaration surface = %+v", declaration)
	}
	if len(declaration.Inputs) != 1 {
		t.Fatalf("media declaration inputs = %+v", declaration.Inputs)
	}
	capability := declaration.Inputs[0]
	if got := strings.Join(capability.MIMETypes, ","); got != "image/gif,image/jpeg,image/png,image/webp" {
		t.Fatalf("image MIME ceiling = %q", got)
	}
	if capability.Modality != "image" || capability.Semantics != pebblestore.ModelCatalogMediaSemanticsNative || capability.MaxBytes != 20<<20 || capability.MaxCount != 20 || !openRouterStringAllowed(capability.ContentTypes, "image_url") {
		t.Fatalf("image capability = %+v", capability)
	}
}

func TestBuildChatCompletionRequestMaterializesExactOpenRouterImageJSON(t *testing.T) {
	payload, err := buildChatCompletionRequest(openRouterMediaRequest(openRouterTestImage([]byte("image"), "image/png")))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	want := `{"model":"openai/gpt-test","messages":[{"content":[{"text":"inspect","type":"text"},{"image_url":{"url":"data:image/png;base64,aW1hZ2U="},"type":"image_url"}],"role":"user"}]}`
	if string(encoded) != want {
		t.Fatalf("request JSON\n got: %s\nwant: %s", encoded, want)
	}
	for _, forbidden := range []string{"media_test", "contract-hash", "configuration-hash", "http://", "https://", "file://"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("request disclosed forbidden value %q: %s", forbidden, encoded)
		}
	}
}

type openRouterRoundTripperFunc func(*http.Request) (*http.Response, error)

func (f openRouterRoundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestOpenRouterClientSendsExactImageRequest(t *testing.T) {
	var received []byte
	client := &Client{httpClient: &http.Client{Transport: openRouterRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != chatURL || req.Method != http.MethodPost {
			t.Fatalf("request target = %s %s", req.Method, req.URL)
		}
		if req.Header.Get("Authorization") != "Bearer test-key" || req.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("request headers = %#v", req.Header)
		}
		var err error
		received, err = io.ReadAll(req.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"id":"response","model":"openai/gpt-test","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))}, nil
	})}}
	payload, err := buildChatCompletionRequest(openRouterMediaRequest(openRouterTestImage([]byte("image"), "image/png")))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if _, err := client.CreateChatCompletion(context.Background(), "test-key", payload); err != nil {
		t.Fatalf("create chat completion: %v", err)
	}
	want := `{"model":"openai/gpt-test","messages":[{"content":[{"text":"inspect","type":"text"},{"image_url":{"url":"data:image/png;base64,aW1hZ2U="},"type":"image_url"}],"role":"user"}]}`
	if string(received) != want {
		t.Fatalf("transport JSON\n got: %s\nwant: %s", received, want)
	}
}

func TestBuildChatCompletionRequestPreservesTextImageOrdering(t *testing.T) {
	first := openRouterTestImage([]byte("first"), "image/jpeg")
	second := openRouterTestImage([]byte("second"), "image/webp")
	req := openRouterMediaRequest()
	req.Input[0]["content"] = []map[string]any{
		{"type": "input_text", "text": "before"},
		{"type": "session_media", "media": first},
		{"type": "input_text", "text": "between"},
		{"type": "session_media", "media": second},
	}
	payload, err := buildChatCompletionRequest(req)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	content := payload.Messages[0]["content"].([]map[string]any)
	if len(content) != 4 || content[0]["type"] != "text" || content[1]["type"] != "image_url" || content[2]["type"] != "text" || content[3]["type"] != "image_url" {
		t.Fatalf("content order = %#v", content)
	}
}

func TestBuildChatCompletionRequestRejectsUnimplementedOrMalformedMedia(t *testing.T) {
	valid := openRouterTestImage([]byte("image"), "image/png")
	tests := []struct {
		name string
		edit func(*provideriface.Request)
	}{
		{name: "absent capability", edit: func(req *provideriface.Request) { req.MediaContract.Capabilities = nil }},
		{name: "unsupported MIME", edit: func(req *provideriface.Request) { media := openRouterTestImage([]byte("svg"), "image/svg+xml"); req.Input[0]["content"] = []map[string]any{{"type": "session_media", "media": media}} }},
		{name: "unsupported modality", edit: func(req *provideriface.Request) { media := valid; media.Modality = "audio"; req.Input[0]["content"] = []map[string]any{{"type": "session_media", "media": media}} }},
		{name: "unsupported semantics", edit: func(req *provideriface.Request) { req.MediaContract.Capabilities[0].Semantics = pebblestore.ModelCatalogMediaSemanticsProviderProcessed }},
		{name: "missing configuration identity", edit: func(req *provideriface.Request) { req.ProviderConfigurationHash = "" }},
		{name: "forged digest", edit: func(req *provideriface.Request) { media := valid; media.DigestSHA256 = strings.Repeat("0", 64); req.Input[0]["content"] = []map[string]any{{"type": "session_media", "media": media}} }},
		{name: "malformed payload", edit: func(req *provideriface.Request) { req.Input[0]["content"] = []map[string]any{{"type": "session_media", "media": map[string]any{"url": "https://example.invalid/image.png"}}} }},
		{name: "direct URL", edit: func(req *provideriface.Request) { req.Input[0]["content"] = []map[string]any{{"type": "image_url", "image_url": map[string]any{"url": "https://example.invalid/image.png"}}} }},
		{name: "count exceeded", edit: func(req *provideriface.Request) { req.MediaContract.Capabilities[0].MaxCount = 1; req.Input[0]["content"] = []map[string]any{{"type": "session_media", "media": valid}, {"type": "session_media", "media": valid}} }},
		{name: "adapter size ceiling exceeded", edit: func(req *provideriface.Request) { req.MediaContract.Capabilities[0].MaxBytes = openRouterMaxImageBytes + 1 }},
		{name: "cross surface", edit: func(req *provideriface.Request) { req.MediaContract.ProviderSurface = "responses_api" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := openRouterMediaRequest(valid)
			test.edit(&req)
			if _, err := buildChatCompletionRequest(req); err == nil {
				t.Fatal("build request error = nil")
			}
		})
	}
}

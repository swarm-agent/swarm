package fireworks

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func fireworksTestPayload(modality, mimeType, fileType string, body []byte) provideriface.SessionMediaPayload {
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

func fireworksTestContract(capability provideriface.MediaContractCapability) provideriface.SessionMediaContract {
	return provideriface.SessionMediaContract{
		ProviderID:            "fireworks",
		ProviderSurface:       fireworksMediaProviderSurface,
		CredentialSurface:     fireworksMediaCredentialSurface,
		CredentialFingerprint: "credential-fingerprint",
		AdapterID:             fireworksMediaAdapterID,
		Hash:                  "contract-hash",
		Capabilities:          []provideriface.MediaContractCapability{capability},
	}
}

func fireworksTestImageCapability(maxBytes int64, maxCount int) provideriface.MediaContractCapability {
	return provideriface.MediaContractCapability{
		Modality:     "image",
		State:        provideriface.MediaCapabilityStateAllowed,
		Semantics:    pebblestore.ModelCatalogMediaSemanticsNative,
		MIMETypes:    []string{"image/jpeg", "image/png"},
		FileTypes:    []string{"jpeg", "jpg", "png"},
		ContentTypes: []string{"image_url"},
		MaxBytes:     maxBytes,
		MaxCount:     maxCount,
	}
}

func TestFireworksMediaCapabilityDeclarationIsExactImageOnly(t *testing.T) {
	store, err := pebblestore.Open(t.TempDir() + "/auth.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	auth := pebblestore.NewAuthStore(store)
	if _, err := auth.UpsertCredential(pebblestore.AuthCredentialInput{
		AccountScopeID: "account-test", Provider: "fireworks", ID: "primary", Type: pebblestore.AuthTypeAPI,
		APIKey: "test-key", SetActive: true,
	}); err != nil {
		t.Fatalf("upsert credential: %v", err)
	}
	ctx := identity.ContextWithPrincipal(context.Background(), identity.Principal{
		Type: identity.PrincipalTypeUser, UserID: "user-test", AccountScopeID: "account-test",
	})
	declaration, err := NewRunner(auth).MediaCapabilityDeclaration(ctx)
	if err != nil {
		t.Fatalf("declaration: %v", err)
	}
	if declaration.AdapterID != fireworksMediaAdapterID || declaration.ProviderID != "fireworks" || declaration.ProviderSurface != fireworksMediaProviderSurface || declaration.CredentialSurface != fireworksMediaCredentialSurface || declaration.CredentialFingerprint == "" {
		t.Fatalf("declaration identity = %+v", declaration)
	}
	if len(declaration.Inputs) != 1 {
		t.Fatalf("input declarations = %+v", declaration.Inputs)
	}
	image := declaration.Inputs[0]
	if image.Modality != "image" || image.Semantics != pebblestore.ModelCatalogMediaSemanticsNative || image.MaxBytes != fireworksMaxImageBytes || image.MaxCount != fireworksMaxImageCount || len(image.ContentTypes) != 1 || image.ContentTypes[0] != "image_url" {
		t.Fatalf("image declaration = %+v", image)
	}
	for _, forbidden := range []string{"audio", "video", "pdf", "file", "generation"} {
		if strings.Contains(strings.ToLower(strings.Join(image.ContentTypes, " ")), forbidden) {
			t.Fatalf("declaration unexpectedly exposes %s: %+v", forbidden, image)
		}
	}
}

func TestBuildFireworksChatCompletionRequestMaterializesImagesInOrder(t *testing.T) {
	first := fireworksTestPayload("image", "image/png", "png", []byte("png-one"))
	second := fireworksTestPayload("image", "image/jpeg", "jpg", []byte("jpeg-two"))
	contract := fireworksTestContract(fireworksTestImageCapability(1024, 30))
	payload, err := buildChatCompletionRequest(provideriface.Request{
		Model: "accounts/fireworks/models/vision-test", MediaContract: contract,
		Input: []map[string]any{{
			"role": "user",
			"content": []map[string]any{
				{"type": "input_text", "text": "compare"},
				{"type": "session_media", "media": first},
				{"type": "input_text", "text": "then"},
				{"type": "session_media", "media": second},
			},
		}},
	})
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	want := `{"model":"accounts/fireworks/models/vision-test","messages":[{"content":[{"text":"compare","type":"text"},{"image_url":{"url":"data:image/png;base64,` + base64.StdEncoding.EncodeToString(first.Bytes) + `"},"type":"image_url"},{"text":"then","type":"text"},{"image_url":{"url":"data:image/jpeg;base64,` + base64.StdEncoding.EncodeToString(second.Bytes) + `"},"type":"image_url"}],"role":"user"}]}`
	if string(encoded) != want {
		t.Fatalf("request JSON\n got: %s\nwant: %s", encoded, want)
	}
	if strings.Contains(string(encoded), first.AssetID) || strings.Contains(string(encoded), first.DigestSHA256) {
		t.Fatal("outbound request disclosed internal media identity")
	}
}

func TestBuildFireworksChatCompletionRequestRejectsInvalidMedia(t *testing.T) {
	valid := fireworksTestPayload("image", "image/png", "png", []byte("image"))
	capability := fireworksTestImageCapability(1024, 1)
	validContract := fireworksTestContract(capability)

	tests := []struct {
		name     string
		contract provideriface.SessionMediaContract
		content  []map[string]any
	}{
		{name: "missing contract", contract: provideriface.SessionMediaContract{}, content: []map[string]any{{"type": "session_media", "media": valid}}},
		{name: "cross surface", contract: func() provideriface.SessionMediaContract {
			c := validContract
			c.ProviderSurface = "responses_api"
			return c
		}(), content: []map[string]any{{"type": "session_media", "media": valid}}},
		{name: "missing credential fingerprint", contract: func() provideriface.SessionMediaContract { c := validContract; c.CredentialFingerprint = ""; return c }(), content: []map[string]any{{"type": "session_media", "media": valid}}},
		{name: "unsupported modality", contract: validContract, content: []map[string]any{{"type": "session_media", "media": fireworksTestPayload("video", "image/png", "png", []byte("video"))}}},
		{name: "unsupported mime", contract: func() provideriface.SessionMediaContract {
			c := validContract
			c.Capabilities = append([]provideriface.MediaContractCapability(nil), validContract.Capabilities...)
			c.Capabilities[0].MIMETypes = []string{"image/svg+xml"}
			c.Capabilities[0].FileTypes = []string{"svg"}
			return c
		}(), content: []map[string]any{{"type": "session_media", "media": fireworksTestPayload("image", "image/svg+xml", "svg", []byte("svg"))}}},
		{name: "unsupported file type", contract: func() provideriface.SessionMediaContract {
			c := validContract
			c.Capabilities = append([]provideriface.MediaContractCapability(nil), validContract.Capabilities...)
			c.Capabilities[0].FileTypes = []string{"exe"}
			return c
		}(), content: []map[string]any{{"type": "session_media", "media": fireworksTestPayload("image", "image/png", "exe", []byte("image"))}}},
		{name: "oversize", contract: fireworksTestContract(fireworksTestImageCapability(4, 1)), content: []map[string]any{{"type": "session_media", "media": fireworksTestPayload("image", "image/png", "png", []byte("image"))}}},
		{name: "wrong semantics", contract: func() provideriface.SessionMediaContract {
			c := validContract
			c.Capabilities = append([]provideriface.MediaContractCapability(nil), validContract.Capabilities...)
			c.Capabilities[0].Semantics = pebblestore.ModelCatalogMediaSemanticsClientProcessed
			return c
		}(), content: []map[string]any{{"type": "session_media", "media": valid}}},
		{name: "wrong content type", contract: func() provideriface.SessionMediaContract {
			c := validContract
			c.Capabilities = append([]provideriface.MediaContractCapability(nil), validContract.Capabilities...)
			c.Capabilities[0].ContentTypes = []string{"input_image"}
			return c
		}(), content: []map[string]any{{"type": "session_media", "media": valid}}},
		{name: "provider-exceeding byte limit", contract: fireworksTestContract(fireworksTestImageCapability(fireworksMaxImageBytes+1, 1)), content: []map[string]any{{"type": "session_media", "media": valid}}},
		{name: "provider-exceeding count limit", contract: fireworksTestContract(fireworksTestImageCapability(1024, fireworksMaxImageCount+1)), content: []map[string]any{{"type": "session_media", "media": valid}}},
		{name: "too many", contract: validContract, content: []map[string]any{{"type": "session_media", "media": valid}, {"type": "session_media", "media": valid}}},
		{name: "forged digest", contract: validContract, content: []map[string]any{{"type": "session_media", "media": func() provideriface.SessionMediaPayload {
			p := valid
			p.DigestSHA256 = strings.Repeat("0", 64)
			return p
		}()}}},
		{name: "malformed payload", contract: validContract, content: []map[string]any{{"type": "session_media", "media": map[string]any{"url": "https://example.invalid/image.png"}}}},
		{name: "user url", contract: validContract, content: []map[string]any{{"type": "image_url", "image_url": map[string]any{"url": "https://example.invalid/image.png"}}}},
		{name: "audio block", contract: validContract, content: []map[string]any{{"type": "input_audio", "audio": "data:audio/wav;base64,AAAA"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := buildChatCompletionRequest(provideriface.Request{
				Model: "vision-test", MediaContract: test.contract,
				Input: []map[string]any{{"role": "user", "content": test.content}},
			})
			if err == nil {
				t.Fatal("build request error = nil")
			}
			if strings.Contains(err.Error(), valid.AssetID) || strings.Contains(err.Error(), valid.DigestSHA256) || strings.Contains(err.Error(), string(valid.Bytes)) {
				t.Fatalf("error disclosed media material: %v", err)
			}
		})
	}
}

func TestBuildFireworksChatCompletionRequestRejectsUnsupportedMessageRole(t *testing.T) {
	tests := []struct {
		name string
		role any
	}{
		{name: "system", role: "system"},
		{name: "tool", role: "tool"},
		{name: "non-string", role: 7},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := buildChatCompletionRequest(provideriface.Request{
				Model: "vision-test",
				Input: []map[string]any{{"role": test.role, "content": "bypass"}},
			})
			if err == nil || !strings.Contains(err.Error(), "role") {
				t.Fatalf("error = %v, want role rejection", err)
			}
		})
	}
}

func TestValidateFireworksMediaContractRejectsCredentialRotation(t *testing.T) {
	record := pebblestore.AuthCredentialRecord{AccountScopeID: "account-test", Provider: "fireworks", ID: "primary", Type: pebblestore.AuthTypeAPI}
	contract := fireworksTestContract(fireworksTestImageCapability(1024, 1))
	contract.CredentialFingerprint = fireworksCredentialFingerprint(record.AccountScopeID, record.Provider, record.ID, record.Type)
	if err := validateFireworksMediaContractForCredential(contract, record); err != nil {
		t.Fatalf("matching credential: %v", err)
	}
	record.ID = "rotated"
	if err := validateFireworksMediaContractForCredential(contract, record); err == nil || !strings.Contains(err.Error(), "active Fireworks credential") {
		t.Fatalf("error = %v, want credential rotation rejection", err)
	}
}

func TestBuildFireworksChatCompletionRequestRejectsTopLevelMediaItem(t *testing.T) {
	payload := fireworksTestPayload("image", "image/png", "png", []byte("image"))
	_, err := buildChatCompletionRequest(provideriface.Request{
		Model: "vision-test", MediaContract: fireworksTestContract(fireworksTestImageCapability(1024, 1)),
		Input: []map[string]any{{"type": "session_media", "media": payload}},
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported item type") {
		t.Fatalf("error = %v, want top-level item rejection", err)
	}
}

func TestBuildFireworksChatCompletionRequestRejectsProviderEncodedLimit(t *testing.T) {
	body := make([]byte, fireworksMaxImageBytes)
	payload := fireworksTestPayload("image", "image/png", "png", body)
	_, err := buildChatCompletionRequest(provideriface.Request{
		Model: "vision-test", MediaContract: fireworksTestContract(fireworksTestImageCapability(int64(len(body)), 1)),
		Input: []map[string]any{{"role": "user", "content": []map[string]any{{"type": "session_media", "media": payload}}}},
	})
	if err == nil || !strings.Contains(err.Error(), "encoded-image limit") {
		t.Fatalf("error = %v, want provider encoded-image limit", err)
	}
}

func TestBuildFireworksChatCompletionRequestRejectsMediaOnAssistantMessage(t *testing.T) {
	payload := fireworksTestPayload("image", "image/png", "png", []byte("image"))
	_, err := buildChatCompletionRequest(provideriface.Request{
		Model: "vision-test", MediaContract: fireworksTestContract(fireworksTestImageCapability(1024, 1)),
		Input: []map[string]any{{"role": "assistant", "content": []map[string]any{{"type": "session_media", "media": payload}}}},
	})
	if err == nil || !strings.Contains(err.Error(), "user messages") {
		t.Fatalf("error = %v, want role rejection", err)
	}
}

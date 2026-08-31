package google

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	toolruntime "swarm/packages/swarmd/internal/tool"
)

func TestGoogleExecutionEpochRequestContainsOnlyExplicitInput(t *testing.T) {
	lifecycle := (&Runner{}).ExecutionEpochLifecycle()
	if lifecycle.ContextMode != provideriface.ExecutionEpochContextStatelessFullInput || !lifecycle.Valid() {
		t.Fatalf("lifecycle = %+v, want valid stateless full-input mode", lifecycle)
	}
	payload, err := buildGoogleRequest(provideriface.Request{
		Instructions: "current instructions",
		Input: []map[string]any{
			{"role": "user", "content": "epoch user"},
			{"role": "assistant", "content": "epoch assistant"},
			{"role": "user", "content": "epoch follow-up"},
		},
	})
	if err != nil {
		t.Fatalf("build payload: %v", err)
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	body := string(encoded)
	for _, text := range []string{"current instructions", "epoch user", "epoch assistant", "epoch follow-up"} {
		if !strings.Contains(body, text) {
			t.Fatalf("payload missing %q: %s", text, body)
		}
	}
	for _, forbidden := range []string{"previous_response_id", "conversation", "predecessor"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("payload contains stateful continuation field %q: %s", forbidden, body)
		}
	}
}

func TestBuildGoogleRequestAppliesCatalogBackedPriorityServiceTier(t *testing.T) {
	payload, err := buildGoogleRequest(provideriface.Request{
		Input:       []map[string]any{{"role": "user", "content": "urgent request"}},
		ServiceTier: "fast",
		ModelCatalog: pebblestore.ModelCatalogRecord{ServiceTierMappings: []pebblestore.ModelCatalogServiceTierMapping{{
			Tier: "priority", SwarmSetting: "fast", ProviderParameter: "service_tier", ProviderValue: "priority",
		}}},
	})
	if err != nil {
		t.Fatalf("build priority request: %v", err)
	}
	if payload.ServiceTier != "priority" {
		t.Fatalf("service tier = %q, want priority", payload.ServiceTier)
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal priority request: %v", err)
	}
	if !strings.Contains(string(encoded), `"service_tier":"priority"`) {
		t.Fatalf("serialized priority request missing service_tier: %s", encoded)
	}

	for _, tier := range []string{"batch", "flex"} {
		payload, err = buildGoogleRequest(provideriface.Request{
			Input:       []map[string]any{{"role": "user", "content": "unsupported tier request"}},
			ServiceTier: tier,
			ModelCatalog: pebblestore.ModelCatalogRecord{ServiceTierMappings: []pebblestore.ModelCatalogServiceTierMapping{{
				Tier: tier, SwarmSetting: tier, ProviderParameter: "service_tier", ProviderValue: tier,
			}}},
		})
		if err != nil {
			t.Fatalf("build %s request: %v", tier, err)
		}
		if payload.ServiceTier != "" {
			t.Fatalf("Google %s service tier = %q, want omitted", tier, payload.ServiceTier)
		}
	}

	payload, err = buildGoogleRequest(provideriface.Request{
		Input:       []map[string]any{{"role": "user", "content": "ordinary request"}},
		ServiceTier: "priority",
	})
	if err != nil {
		t.Fatalf("build request without catalog mapping: %v", err)
	}
	if payload.ServiceTier != "" {
		t.Fatalf("unmapped service tier = %q, want omitted", payload.ServiceTier)
	}
}

type googleRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn googleRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func newGoogleTransportTestRunner(t *testing.T, transport http.RoundTripper) (*Runner, context.Context) {
	t.Helper()
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "google-transport.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	authStore := pebblestore.NewAuthStore(store)
	if _, err := authStore.UpsertCredential(pebblestore.AuthCredentialInput{AccountScopeID: "account-1", ID: "credential-1", Provider: "google", Type: pebblestore.AuthTypeAPI, APIKey: "test-key", SetActive: true}); err != nil {
		t.Fatalf("seed Google credential: %v", err)
	}
	runner := NewRunner(authStore)
	runner.httpClient = &http.Client{Transport: transport}
	ctx := identity.ContextWithPrincipal(context.Background(), identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user-1", AccountScopeID: "account-1"})
	return runner, ctx
}

func googlePriorityTransportRequest() provideriface.Request {
	return provideriface.Request{
		Model:       "gemini-test",
		Input:       []map[string]any{{"role": "user", "content": "urgent request"}},
		ServiceTier: "fast",
		ModelCatalog: pebblestore.ModelCatalogRecord{ServiceTierMappings: []pebblestore.ModelCatalogServiceTierMapping{{
			Tier: "priority", SwarmSetting: "fast", ProviderParameter: "service_tier", ProviderValue: "priority",
		}}},
	}
}

func TestGooglePriorityTransportSendsCanonicalTierAndCapturesStreamBodyDowngrade(t *testing.T) {
	var capturedBody []byte
	runner, ctx := newGoogleTransportTestRunner(t, googleRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		var err error
		capturedBody, err = io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader("data: {\"candidates\":[{\"finishReason\":\"STOP\",\"content\":{\"parts\":[{\"text\":\"ok\"}]}}],\"usageMetadata\":{\"promptTokenCount\":1,\"candidatesTokenCount\":1,\"totalTokenCount\":2,\"serviceTier\":\"standard\"}}\n\n")),
			Request:    req,
		}, nil
	}))
	response, err := runner.CreateResponseStreaming(ctx, googlePriorityTransportRequest(), nil)
	if err != nil {
		t.Fatalf("Google streaming request: %v", err)
	}
	var requestBody map[string]any
	if err := json.Unmarshal(capturedBody, &requestBody); err != nil {
		t.Fatalf("decode captured request: %v", err)
	}
	if requestBody["service_tier"] != "priority" {
		t.Fatalf("captured request service_tier = %#v, want priority; body=%s", requestBody["service_tier"], capturedBody)
	}
	usage := response.Usage
	if usage.RequestedServiceTier != "priority" || usage.ServiceTier != "standard" || usage.ServiceTierStatus != "confirmed" {
		t.Fatalf("usage tiers = requested=%q served=%q status=%q", usage.RequestedServiceTier, usage.ServiceTier, usage.ServiceTierStatus)
	}
	if usage.APIUsageRaw["serviceTier"] != "standard" || usage.APIUsageRaw["requested_service_tier"] != "priority" || usage.APIUsageRaw["service_tier"] != "standard" || usage.APIUsageRaw["service_tier_status"] != "confirmed" {
		t.Fatalf("raw usage tier evidence = %#v", usage.APIUsageRaw)
	}
}

func TestGooglePriorityTransportCapturesPriorityStreamBodyTier(t *testing.T) {
	runner, ctx := newGoogleTransportTestRunner(t, googleRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader("data: {\"candidates\":[{\"finishReason\":\"STOP\",\"content\":{\"parts\":[{\"text\":\"ok\"}]}}],\"usageMetadata\":{\"promptTokenCount\":1,\"candidatesTokenCount\":1,\"totalTokenCount\":2,\"serviceTier\":\"priority\"}}\n\n")),
			Request:    req,
		}, nil
	}))
	response, err := runner.CreateResponseStreaming(ctx, googlePriorityTransportRequest(), nil)
	if err != nil {
		t.Fatalf("Google streaming request: %v", err)
	}
	if usage := response.Usage; usage.RequestedServiceTier != "priority" || usage.ServiceTier != "priority" || usage.ServiceTierStatus != "confirmed" {
		t.Fatalf("usage tiers = requested=%q served=%q status=%q", usage.RequestedServiceTier, usage.ServiceTier, usage.ServiceTierStatus)
	}
}

func TestGooglePriorityTransportRejectsConflictingHeaderAndStreamBodyTiers(t *testing.T) {
	runner, ctx := newGoogleTransportTestRunner(t, googleRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "X-Gemini-Service-Tier": []string{"priority"}},
			Body:       io.NopCloser(strings.NewReader("data: {\"candidates\":[{\"finishReason\":\"STOP\",\"content\":{\"parts\":[{\"text\":\"ok\"}]}}],\"usageMetadata\":{\"promptTokenCount\":1,\"candidatesTokenCount\":1,\"totalTokenCount\":2,\"serviceTier\":\"standard\"}}\n\n")),
			Request:    req,
		}, nil
	}))
	_, err := runner.CreateResponseStreaming(ctx, googlePriorityTransportRequest(), nil)
	if err == nil || !strings.Contains(err.Error(), "conflicting google service tier signals") || !strings.Contains(err.Error(), `x-gemini-service-tier="priority"`) || !strings.Contains(err.Error(), `usageMetadata.serviceTier="standard"`) {
		t.Fatalf("conflicting tier error = %v", err)
	}
}

func TestGoogleUnaryTransportRetainsHeaderServedTier(t *testing.T) {
	runner, ctx := newGoogleTransportTestRunner(t, googleRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}, "X-Gemini-Service-Tier": []string{"standard"}},
			Body:       io.NopCloser(strings.NewReader(`{"candidates":[{"finishReason":"STOP","content":{"parts":[{"text":"ok"}]}}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`)),
			Request:    req,
		}, nil
	}))
	response, err := runner.CreateResponse(ctx, googlePriorityTransportRequest())
	if err != nil {
		t.Fatalf("Google unary request: %v", err)
	}
	if usage := response.Usage; usage.RequestedServiceTier != "priority" || usage.ServiceTier != "standard" || usage.ServiceTierStatus != "confirmed" {
		t.Fatalf("usage tiers = requested=%q served=%q status=%q", usage.RequestedServiceTier, usage.ServiceTier, usage.ServiceTierStatus)
	}
}

func TestGooglePriorityTransportMarksMissingServedTierUnconfirmed(t *testing.T) {
	runner, ctx := newGoogleTransportTestRunner(t, googleRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader("data: {\"candidates\":[{\"finishReason\":\"STOP\",\"content\":{\"parts\":[{\"text\":\"ok\"}]}}],\"usageMetadata\":{\"promptTokenCount\":1,\"candidatesTokenCount\":1,\"totalTokenCount\":2}}\n\n")),
			Request:    req,
		}, nil
	}))
	response, err := runner.CreateResponseStreaming(ctx, googlePriorityTransportRequest(), nil)
	if err != nil {
		t.Fatalf("Google streaming request: %v", err)
	}
	usage := response.Usage
	if usage.RequestedServiceTier != "priority" || usage.ServiceTier != "" || usage.ServiceTierStatus != "unconfirmed" {
		t.Fatalf("usage tiers = requested=%q served=%q status=%q", usage.RequestedServiceTier, usage.ServiceTier, usage.ServiceTierStatus)
	}
	if _, claimed := usage.APIUsageRaw["service_tier"]; claimed || usage.APIUsageRaw["requested_service_tier"] != "priority" || usage.APIUsageRaw["service_tier_status"] != "unconfirmed" {
		t.Fatalf("missing header must remain explicitly unconfirmed: %#v", usage.APIUsageRaw)
	}
}

func TestBuildGoogleRequestOmitsUnsupportedConstraintsWithoutMutatingCanonicalSchema(t *testing.T) {
	parameters := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"state_ids": map[string]any{
				"type":        "array",
				"minItems":    1,
				"maxItems":    16,
				"uniqueItems": true,
				"items": map[string]any{
					"type":    "string",
					"pattern": "^[a-z0-9][a-z0-9._-]{0,63}$",
				},
			},
			"parts": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"width":  map[string]any{"type": "number", "exclusiveMinimum": 0, "maximum": 1},
						"height": map[string]any{"type": "number", "exclusiveMaximum": 1, "minimum": 0},
					},
				},
			},
		},
	}

	request, err := buildGoogleRequest(provideriface.Request{
		Input: []map[string]any{{"role": "user", "content": "export the selected states"}},
		Tools: []provideriface.ToolDefinition{{
			Type: "function", Name: "manage_artifact", Parameters: parameters,
		}},
	})
	if err != nil {
		t.Fatalf("build Google request: %v", err)
	}
	if len(request.Tools) != 1 || len(request.Tools[0].FunctionDeclarations) != 1 {
		t.Fatalf("Google request tools = %+v, want one declaration", request.Tools)
	}
	declaration := request.Tools[0].FunctionDeclarations[0]
	encoded, err := json.Marshal(declaration.Parameters)
	if err != nil {
		t.Fatalf("marshal Google tool parameters: %v", err)
	}
	for _, unsupported := range []string{`"uniqueItems"`, `"exclusiveMinimum"`, `"exclusiveMaximum"`} {
		if strings.Contains(string(encoded), unsupported) {
			t.Fatalf("Google tool parameters retain unsupported constraint %s: %s", unsupported, encoded)
		}
	}
	for _, supported := range []string{`"minItems":1`, `"maxItems":16`, `"pattern":"^[a-z0-9][a-z0-9._-]{0,63}$"`, `"maximum":1`, `"minimum":0`} {
		if !strings.Contains(string(encoded), supported) {
			t.Fatalf("Google tool parameters lost supported constraint %s: %s", supported, encoded)
		}
	}
	canonicalProperties := parameters["properties"].(map[string]any)
	stateIDs := canonicalProperties["state_ids"].(map[string]any)
	parts := canonicalProperties["parts"].(map[string]any)
	partProperties := parts["items"].(map[string]any)["properties"].(map[string]any)
	if stateIDs["uniqueItems"] != true || partProperties["width"].(map[string]any)["exclusiveMinimum"] != 0 || partProperties["height"].(map[string]any)["exclusiveMaximum"] != 1 {
		t.Fatalf("Google serialization mutated canonical schema: %#v", parameters)
	}
}

func TestBuildGoogleRequestNormalizesPlanCheckpointAnyOfRequiredBranches(t *testing.T) {
	definitions := toolruntime.NewRuntime(1).Definitions()
	planTools := make([]provideriface.ToolDefinition, 0, 2)
	canonicalParameters := make(map[string][]byte, 2)
	for _, definition := range definitions {
		if definition.Name != "exit_plan_mode" && definition.Name != "plan_manage" {
			continue
		}
		encoded, err := json.Marshal(definition.Parameters)
		if err != nil {
			t.Fatalf("marshal canonical %s parameters: %v", definition.Name, err)
		}
		canonicalParameters[definition.Name] = encoded
		if !hasRequiredBranchWithoutObjectProperties(definition.Parameters) {
			t.Fatalf("canonical %s schema no longer reproduces a required-only anyOf branch", definition.Name)
		}
		planTools = append(planTools, provideriface.ToolDefinition{
			Type: definition.Type, Name: definition.Name, Description: definition.Description, Parameters: definition.Parameters,
		})
	}
	if len(planTools) != 2 {
		t.Fatalf("found %d plan tools, want exit_plan_mode and plan_manage", len(planTools))
	}

	request, err := buildGoogleRequest(provideriface.Request{
		Input: []map[string]any{{"role": "user", "content": "plan the change"}},
		Tools: planTools,
	})
	if err != nil {
		t.Fatalf("build Google request with plan tools: %v", err)
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal Google request: %v", err)
	}
	var serialized map[string]any
	if err := json.Unmarshal(encoded, &serialized); err != nil {
		t.Fatalf("decode serialized Google request: %v", err)
	}
	assertGoogleRequiredSchemasHaveProperties(t, serialized, "$", false)
	for _, declaration := range request.Tools[0].FunctionDeclarations {
		if !hasGoogleRequiredAlternatives(declaration.Parameters, "objective", "tasks") {
			t.Fatalf("serialized %s parameters lost the checkpoint objective-or-tasks requirement", declaration.Name)
		}
	}

	for _, definition := range planTools {
		after, err := json.Marshal(definition.Parameters)
		if err != nil {
			t.Fatalf("marshal canonical %s parameters after request: %v", definition.Name, err)
		}
		if !reflect.DeepEqual(after, canonicalParameters[definition.Name]) {
			t.Fatalf("Google serialization mutated canonical %s parameters", definition.Name)
		}
	}
}

func hasRequiredBranchWithoutObjectProperties(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		if required, ok := typed["required"]; ok && required != nil {
			if _, hasProperties := typed["properties"]; !hasProperties {
				return true
			}
		}
		for _, child := range typed {
			if hasRequiredBranchWithoutObjectProperties(child) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if hasRequiredBranchWithoutObjectProperties(child) {
				return true
			}
		}
	case []map[string]any:
		for _, child := range typed {
			if hasRequiredBranchWithoutObjectProperties(child) {
				return true
			}
		}
	}
	return false
}

func hasGoogleRequiredAlternatives(value any, requiredNames ...string) bool {
	switch typed := value.(type) {
	case map[string]any:
		if alternatives, ok := typed["anyOf"].([]any); ok {
			matched := make(map[string]bool, len(requiredNames))
			for _, alternative := range alternatives {
				schema, ok := alternative.(map[string]any)
				if !ok || schema["type"] != "object" {
					continue
				}
				properties, _ := schema["properties"].(map[string]any)
				for _, name := range googleToolSchemaRequiredNames(schema["required"]) {
					if _, ok := properties[name]; ok {
						matched[name] = true
					}
				}
			}
			allMatched := true
			for _, name := range requiredNames {
				allMatched = allMatched && matched[name]
			}
			if allMatched {
				return true
			}
		}
		for _, child := range typed {
			if hasGoogleRequiredAlternatives(child, requiredNames...) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if hasGoogleRequiredAlternatives(child, requiredNames...) {
				return true
			}
		}
	}
	return false
}

func assertGoogleRequiredSchemasHaveProperties(t *testing.T, value any, path string, inParameters bool) {
	t.Helper()
	switch typed := value.(type) {
	case map[string]any:
		if _, ok := typed["functionDeclarations"]; ok {
			inParameters = false
		}
		if parameters, ok := typed["parameters"]; ok {
			assertGoogleRequiredSchemasHaveProperties(t, parameters, path+".parameters", true)
			for key, child := range typed {
				if key != "parameters" {
					assertGoogleRequiredSchemasHaveProperties(t, child, path+"."+key, false)
				}
			}
			return
		}
		if inParameters {
			if required, ok := typed["required"].([]any); ok && len(required) > 0 {
				if typed["type"] != "object" {
					t.Fatalf("%s required schema type = %#v, want object", path, typed["type"])
				}
				properties, ok := typed["properties"].(map[string]any)
				if !ok {
					t.Fatalf("%s required schema properties = %T, want object", path, typed["properties"])
				}
				for _, item := range required {
					name, ok := item.(string)
					if !ok {
						t.Fatalf("%s required item = %#v, want string", path, item)
					}
					if _, ok := properties[name]; !ok {
						t.Fatalf("%s requires %q without defining it in properties", path, name)
					}
				}
			}
		}
		for key, child := range typed {
			assertGoogleRequiredSchemasHaveProperties(t, child, path+"."+key, inParameters)
		}
	case []any:
		for index, child := range typed {
			assertGoogleRequiredSchemasHaveProperties(t, child, fmt.Sprintf("%s[%d]", path, index), inParameters)
		}
	}
}

func TestBuildGoogleRequestEncodesImmutableImageInlineData(t *testing.T) {
	body := []byte("image-bytes")
	digest := sha256.Sum256(body)
	payload := provideriface.SessionMediaPayload{AssetID: "asset-1", Modality: "image", MIMEType: "image/png", FileType: "png", DigestSHA256: hex.EncodeToString(digest[:]), Size: int64(len(body)), Bytes: body}
	contract := provideriface.SessionMediaContract{
		ProviderID: "google", ProviderSurface: provideriface.MediaProviderSurfaceGoogleGenerateContent,
		CredentialSurface: provideriface.MediaCredentialSurfaceGoogleAPIKey, CredentialFingerprint: "credential", AdapterID: provideriface.MediaAdapterIDGoogleGenerateContentV1, Hash: "contract",
		Capabilities: []provideriface.MediaContractCapability{{Modality: "image", State: provideriface.MediaCapabilityStateAllowed, Semantics: pebblestore.ModelCatalogMediaSemanticsNative, MIMETypes: []string{"image/png"}, ContentTypes: []string{"inline_data"}, MaxBytes: 1024, MaxCount: 1}},
	}
	request, err := buildGoogleRequest(provideriface.Request{ProviderConfigurationHash: "configuration", MediaContract: contract, Input: []map[string]any{{
		"role": "user", "content": []map[string]any{{"type": "session_media", "media": payload}, {"type": "input_text", "text": "describe"}},
	}}})
	if err != nil {
		t.Fatalf("build Google image request: %v", err)
	}
	encoded, _ := json.Marshal(request)
	want := `{"contents":[{"role":"user","parts":[{"inlineData":{"mimeType":"image/png","data":"aW1hZ2UtYnl0ZXM="}},{"text":"describe"}]}]}`
	if string(encoded) != want {
		t.Fatalf("Google request = %s, want %s", encoded, want)
	}
}

func TestBuildGoogleRequestEnforcesMediaCountPerExplicitMessage(t *testing.T) {
	body := []byte("image-bytes")
	digest := sha256.Sum256(body)
	payload := provideriface.SessionMediaPayload{AssetID: "asset-1", Modality: "image", MIMEType: "image/png", FileType: "png", DigestSHA256: hex.EncodeToString(digest[:]), Size: int64(len(body)), Bytes: body}
	contract := provideriface.SessionMediaContract{
		ProviderID: "google", ProviderSurface: provideriface.MediaProviderSurfaceGoogleGenerateContent,
		CredentialSurface: provideriface.MediaCredentialSurfaceGoogleAPIKey, CredentialFingerprint: "credential", AdapterID: provideriface.MediaAdapterIDGoogleGenerateContentV1, Hash: "contract",
		Capabilities: []provideriface.MediaContractCapability{{Modality: "image", State: provideriface.MediaCapabilityStateAllowed, Semantics: pebblestore.ModelCatalogMediaSemanticsNative, MIMETypes: []string{"image/png"}, ContentTypes: []string{"inline_data"}, MaxBytes: 1024, MaxCount: 1}},
	}
	mediaPart := map[string]any{"type": "session_media", "media": payload}

	request, err := buildGoogleRequest(provideriface.Request{ProviderConfigurationHash: "configuration", MediaContract: contract, Input: []map[string]any{
		{"role": "user", "content": []map[string]any{mediaPart}},
		{"role": "assistant", "content": "earlier response"},
		{"role": "user", "content": []map[string]any{mediaPart}},
	}})
	if err != nil {
		t.Fatalf("build Google request with one image per explicit message: %v", err)
	}
	if len(request.Contents) != 3 || len(request.Contents[0].Parts) != 1 || request.Contents[0].Parts[0].InlineData == nil || len(request.Contents[2].Parts) != 1 || request.Contents[2].Parts[0].InlineData == nil {
		t.Fatalf("Google request did not retain both historical message images: %+v", request.Contents)
	}

	_, err = buildGoogleRequest(provideriface.Request{ProviderConfigurationHash: "configuration", MediaContract: contract, Input: []map[string]any{{
		"role": "user", "content": []map[string]any{mediaPart, mediaPart},
	}}})
	if err == nil || !strings.Contains(err.Error(), "count limit") {
		t.Fatalf("same-message Google media count error = %v", err)
	}
}

func TestBuildGoogleRequestEnforcesTotalInlineRequestLimit(t *testing.T) {
	_, err := buildGoogleRequest(provideriface.Request{Input: []map[string]any{{"role": "user", "content": strings.Repeat("x", maxInlineRequestBytes)}}})
	if err == nil || !strings.Contains(err.Error(), "20 MB") {
		t.Fatalf("oversize Google request error = %v", err)
	}
}

func TestBuildGoogleRequestRejectsForgedAndUnsupportedMedia(t *testing.T) {
	body := []byte("image-bytes")
	digest := sha256.Sum256(body)
	basePayload := provideriface.SessionMediaPayload{AssetID: "asset-1", Modality: "image", MIMEType: "image/png", DigestSHA256: hex.EncodeToString(digest[:]), Size: int64(len(body)), Bytes: body}
	baseContract := provideriface.SessionMediaContract{
		ProviderID: "google", ProviderSurface: provideriface.MediaProviderSurfaceGoogleGenerateContent,
		CredentialSurface: provideriface.MediaCredentialSurfaceGoogleAPIKey, CredentialFingerprint: "credential", AdapterID: provideriface.MediaAdapterIDGoogleGenerateContentV1, Hash: "contract",
		Capabilities: []provideriface.MediaContractCapability{{Modality: "image", State: provideriface.MediaCapabilityStateAllowed, Semantics: pebblestore.ModelCatalogMediaSemanticsNative, MIMETypes: []string{"image/png"}, ContentTypes: []string{"inline_data"}, MaxBytes: 1024, MaxCount: 1}},
	}
	for _, test := range []struct {
		name   string
		mutate func(*provideriface.SessionMediaPayload, *provideriface.SessionMediaContract)
	}{
		{"forged digest", func(payload *provideriface.SessionMediaPayload, _ *provideriface.SessionMediaContract) {
			payload.DigestSHA256 = strings.Repeat("0", 64)
		}},
		{"unsupported MIME", func(payload *provideriface.SessionMediaPayload, _ *provideriface.SessionMediaContract) {
			payload.MIMEType = "image/svg+xml"
		}},
		{"audio modality", func(payload *provideriface.SessionMediaPayload, _ *provideriface.SessionMediaContract) {
			payload.Modality = "audio"
		}},
		{"cross surface", func(_ *provideriface.SessionMediaPayload, contract *provideriface.SessionMediaContract) {
			contract.ProviderID = "openai"
		}},
		{"missing credential fingerprint", func(_ *provideriface.SessionMediaPayload, contract *provideriface.SessionMediaContract) {
			contract.CredentialFingerprint = ""
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			payload := basePayload
			contract := baseContract
			test.mutate(&payload, &contract)
			_, err := buildGoogleRequest(provideriface.Request{ProviderConfigurationHash: "configuration", MediaContract: contract, Input: []map[string]any{{"role": "user", "content": []map[string]any{{"type": "session_media", "media": payload}}}}})
			if err == nil {
				t.Fatal("forged or unsupported media was accepted")
			}
		})
	}
}

func TestBuildGoogleRequestRejectsBypassMediaRepresentations(t *testing.T) {
	for _, test := range []struct {
		name string
		part map[string]any
	}{
		{name: "image URL", part: map[string]any{"type": "image_url", "url": "https://example.invalid/image.png"}},
		{name: "input image", part: map[string]any{"type": "input_image", "image_url": "data:image/png;base64,AAAA"}},
		{name: "local path", part: map[string]any{"type": "file", "path": "image.png"}},
		{name: "audio", part: map[string]any{"type": "input_audio", "data": "AAAA"}},
		{name: "video", part: map[string]any{"type": "video", "url": "https://example.invalid/video.mp4"}},
		{name: "malformed session media", part: map[string]any{"type": "session_media", "media": map[string]any{"url": "https://example.invalid/image.png"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := buildGoogleRequest(provideriface.Request{Input: []map[string]any{{"role": "user", "content": []map[string]any{test.part}}}})
			if err == nil {
				t.Fatal("bypass media representation was accepted")
			}
		})
	}
}

func TestBuildGoogleRequestRejectsInflatedContractLimits(t *testing.T) {
	body := []byte("image-bytes")
	digest := sha256.Sum256(body)
	payload := provideriface.SessionMediaPayload{AssetID: "asset-1", Modality: "image", MIMEType: "image/png", DigestSHA256: hex.EncodeToString(digest[:]), Size: int64(len(body)), Bytes: body}
	for _, test := range []struct {
		name     string
		maxBytes int64
		maxCount int
	}{
		{name: "max bytes", maxBytes: maxInlineImageBytes + 1, maxCount: 1},
		{name: "max count", maxBytes: 1024, maxCount: pebblestore.SessionMediaDefaultMaxCount + 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			contract := provideriface.SessionMediaContract{
				ProviderID: "google", ProviderSurface: provideriface.MediaProviderSurfaceGoogleGenerateContent,
				CredentialSurface: provideriface.MediaCredentialSurfaceGoogleAPIKey, CredentialFingerprint: "credential", AdapterID: provideriface.MediaAdapterIDGoogleGenerateContentV1, Hash: "contract",
				Capabilities: []provideriface.MediaContractCapability{{Modality: "image", State: provideriface.MediaCapabilityStateAllowed, Semantics: pebblestore.ModelCatalogMediaSemanticsNative, MIMETypes: []string{"image/png"}, ContentTypes: []string{"inline_data"}, MaxBytes: test.maxBytes, MaxCount: test.maxCount}},
			}
			_, err := buildGoogleRequest(provideriface.Request{ProviderConfigurationHash: "configuration", MediaContract: contract, Input: []map[string]any{{"role": "user", "content": []map[string]any{{"type": "session_media", "media": payload}}}}})
			if err == nil {
				t.Fatal("inflated contract capability was accepted")
			}
		})
	}
}

func TestBuildGoogleRequestRejectsMediaOutsideExplicitUserRole(t *testing.T) {
	body := []byte("image-bytes")
	digest := sha256.Sum256(body)
	payload := provideriface.SessionMediaPayload{AssetID: "asset-1", Modality: "image", MIMEType: "image/png", DigestSHA256: hex.EncodeToString(digest[:]), Size: int64(len(body)), Bytes: body}
	contract := provideriface.SessionMediaContract{
		ProviderID: "google", ProviderSurface: provideriface.MediaProviderSurfaceGoogleGenerateContent,
		CredentialSurface: provideriface.MediaCredentialSurfaceGoogleAPIKey, CredentialFingerprint: "credential", AdapterID: provideriface.MediaAdapterIDGoogleGenerateContentV1, Hash: "contract",
		Capabilities: []provideriface.MediaContractCapability{{Modality: "image", State: provideriface.MediaCapabilityStateAllowed, Semantics: pebblestore.ModelCatalogMediaSemanticsNative, MIMETypes: []string{"image/png"}, ContentTypes: []string{"inline_data"}, MaxBytes: 1024, MaxCount: 1}},
	}
	for _, role := range []string{"", "system", "tool", "assistant"} {
		t.Run("role_"+role, func(t *testing.T) {
			_, err := buildGoogleRequest(provideriface.Request{ProviderConfigurationHash: "configuration", MediaContract: contract, Input: []map[string]any{{"role": role, "content": []map[string]any{{"type": "session_media", "media": payload}}}}})
			if err == nil || !strings.Contains(err.Error(), "explicit user") {
				t.Fatalf("role %q media error = %v", role, err)
			}
		})
	}
}

func TestValidateGoogleMediaSurfaceRejectsCredentialMismatch(t *testing.T) {
	contract := provideriface.SessionMediaContract{
		ProviderID: "google", ProviderSurface: provideriface.MediaProviderSurfaceGoogleGenerateContent,
		CredentialSurface: provideriface.MediaCredentialSurfaceGoogleAPIKey, CredentialFingerprint: "credential-a", AdapterID: provideriface.MediaAdapterIDGoogleGenerateContentV1, Hash: "contract",
	}
	if err := validateGoogleMediaSurface(contract, "credential-a"); err != nil {
		t.Fatalf("matching credential surface: %v", err)
	}
	for _, active := range []string{"", "credential-b"} {
		if err := validateGoogleMediaSurface(contract, active); err == nil {
			t.Fatalf("active credential %q was accepted", active)
		}
	}
}

func TestGoogleMediaDeclarationUsesDocumentedInlineImageCeiling(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "google-media.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	authStore := pebblestore.NewAuthStore(store)
	if _, err := authStore.UpsertCredential(pebblestore.AuthCredentialInput{AccountScopeID: "account-1", ID: "credential-1", Provider: "google", Type: pebblestore.AuthTypeAPI, APIKey: "test-key", SetActive: true}); err != nil {
		t.Fatalf("seed Google credential: %v", err)
	}
	ctx := identity.ContextWithPrincipal(context.Background(), identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user-1", AccountScopeID: "account-1"})
	declaration, err := NewRunner(authStore).MediaCapabilityDeclaration(ctx)
	if err != nil {
		t.Fatalf("media declaration: %v", err)
	}
	if declaration.AdapterID != provideriface.MediaAdapterIDGoogleGenerateContentV1 || declaration.ProviderSurface != provideriface.MediaProviderSurfaceGoogleGenerateContent || declaration.CredentialSurface != provideriface.MediaCredentialSurfaceGoogleAPIKey || declaration.CredentialFingerprint == "" || len(declaration.Inputs) != 1 {
		t.Fatalf("Google media declaration = %+v", declaration)
	}
	capability := declaration.Inputs[0]
	if capability.Modality != "image" || capability.Semantics != pebblestore.ModelCatalogMediaSemanticsNative || len(capability.FileTypes) != 0 || capability.MaxBytes != 14<<20 || capability.MaxCount != pebblestore.SessionMediaDefaultMaxCount || !reflect.DeepEqual(capability.ContentTypes, []string{"inline_data"}) || !reflect.DeepEqual(capability.MIMETypes, []string{"image/heic", "image/heif", "image/jpeg", "image/png", "image/webp"}) {
		t.Fatalf("Google inline image capability = %+v", capability)
	}
}

func TestGoogleThinkingConfigUsesCatalogThinkingLevelMapping(t *testing.T) {
	cfg := googleThinkingConfigForRequest(provideriface.Request{
		Model:    "gemini-3.5-flash",
		Thinking: "high",
		ModelCatalog: pebblestore.ModelCatalogRecord{ThinkingMappings: []pebblestore.ModelCatalogThinkingMapping{{
			SwarmSetting:           "high",
			ProviderParameter:      "generationConfig.thinkingConfig.thinkingLevel",
			ProviderValue:          "high",
			EffectiveProviderValue: "high",
			Behavior:               "effort",
		}}},
	})
	if cfg == nil || cfg.ThinkingLevel != "high" || cfg.ThinkingBudget != nil {
		t.Fatalf("google thinking config = %+v, want thinkingLevel=high without thinkingBudget", cfg)
	}
}

func TestGoogleThinkingConfigUsesCatalogBudgetMapping(t *testing.T) {
	cfg := googleThinkingConfigForRequest(provideriface.Request{
		Model:    "gemini-2.5-flash",
		Thinking: "off",
		ModelCatalog: pebblestore.ModelCatalogRecord{ThinkingMappings: []pebblestore.ModelCatalogThinkingMapping{{
			SwarmSetting:      "off",
			ProviderParameter: "generationConfig.thinkingConfig.thinkingBudget",
			ProviderValue:     "0",
			Behavior:          "disabled",
		}}},
	})
	if cfg == nil || cfg.ThinkingBudget == nil || *cfg.ThinkingBudget != 0 || cfg.ThinkingLevel != "" {
		t.Fatalf("google thinking config = %+v, want thinkingBudget=0 without thinkingLevel", cfg)
	}
}

func TestBuildGoogleContentsPreservesThoughtSignatureMetadata(t *testing.T) {
	contents, err := buildGoogleContents(provideriface.Request{Model: "gemini-3.5-flash", Input: []map[string]any{{
		"type":      "function_call",
		"call_id":   "call_weather",
		"name":      "weather",
		"arguments": `{"city":"Paris"}`,
		"metadata":  map[string]any{"google": map[string]any{"thought_signature": "sig-123"}},
	}}})
	if err != nil {
		t.Fatalf("build contents: %v", err)
	}
	if len(contents) != 1 || len(contents[0].Parts) != 1 {
		t.Fatalf("contents = %+v, want one function call part", contents)
	}
	part := contents[0].Parts[0]
	if part.ThoughtSignature != "sig-123" {
		t.Fatalf("thought signature = %q, want sig-123", part.ThoughtSignature)
	}
	if part.FunctionCall == nil || part.FunctionCall.ID != "call_weather" {
		t.Fatalf("function call = %+v, want provider call id preserved", part.FunctionCall)
	}
}

func TestBuildGoogleContentsUsesDocumentedSentinelForUnsignedGemini3History(t *testing.T) {
	contents, err := buildGoogleContents(provideriface.Request{Model: "gemini-3.5-flash", Input: []map[string]any{
		{
			"type":      "function_call",
			"call_id":   "call_read",
			"name":      "read",
			"arguments": `{"path":"README.md"}`,
		},
		{
			"type":      "function_call",
			"call_id":   "call_search",
			"name":      "search",
			"arguments": `{"query":"Gemini"}`,
		},
	}})
	if err != nil {
		t.Fatalf("build contents: %v", err)
	}
	if len(contents) != 1 || len(contents[0].Parts) != 2 {
		t.Fatalf("contents = %+v, want one parallel model step", contents)
	}
	if contents[0].Parts[0].ThoughtSignature != googleSkipSignature {
		t.Fatalf("first signature = %q, want documented sentinel", contents[0].Parts[0].ThoughtSignature)
	}
	if contents[0].Parts[1].ThoughtSignature != "" {
		t.Fatalf("parallel sibling signature = %q, want empty", contents[0].Parts[1].ThoughtSignature)
	}

	legacy, err := buildGoogleContents(provideriface.Request{Model: "gemini-2.5-flash", Input: []map[string]any{{
		"type": "function_call", "call_id": "call_read", "name": "read", "arguments": `{}`,
	}}})
	if err != nil {
		t.Fatalf("build Gemini 2.5 contents: %v", err)
	}
	if legacy[0].Parts[0].ThoughtSignature != "" {
		t.Fatalf("Gemini 2.5 signature = %q, want no synthetic sentinel", legacy[0].Parts[0].ThoughtSignature)
	}
}

func TestParseGoogleUsageMapsResponseAndThoughtCacheTokens(t *testing.T) {
	usage := parseGoogleUsage(googleResponse{UsageMetadata: &googleUsageMetadata{
		PromptTokenCount:        10,
		ResponseTokenCount:      7,
		ThoughtsTokenCount:      5,
		TotalTokenCount:         22,
		CachedContentTokenCount: 3,
		ToolUsePromptTokenCount: 2,
	}})
	if usage.InputTokens != 10 || usage.OutputTokens != 7 || usage.ThinkingTokens != 5 || usage.TotalTokens != 22 || usage.CacheReadTokens != 3 {
		t.Fatalf("usage = %+v, want prompt/output/thought/total/cache tokens mapped", usage)
	}
	if usage.APIUsageRaw["responseTokenCount"] != int64(7) || usage.APIUsageRaw["toolUsePromptTokenCount"] != int64(2) {
		t.Fatalf("raw usage = %+v, want response/tool-use fields preserved", usage.APIUsageRaw)
	}
	annotateGoogleServiceTier(&usage, "priority", "standard")
	if usage.RequestedServiceTier != "priority" || usage.ServiceTier != "standard" || usage.ServiceTierStatus != "confirmed" || usage.APIUsageRaw["requested_service_tier"] != "priority" || usage.APIUsageRaw["service_tier"] != "standard" || usage.APIUsageRaw["service_tier_status"] != "confirmed" {
		t.Fatalf("tier observability = requested=%q served=%q status=%q raw=%+v", usage.RequestedServiceTier, usage.ServiceTier, usage.ServiceTierStatus, usage.APIUsageRaw)
	}
}

func TestGoogleStreamEmitsToolCallConstructionLifecycle(t *testing.T) {
	acc := newGoogleStreamAccumulator("gemini-3.5-flash")
	events := make([]provideriface.StreamEvent, 0, 3)
	payload := `{"candidates":[{"finishReason":"STOP","content":{"parts":[{"functionCall":{"id":"call_weather","name":"weather","args":{"city":"Paris"}},"thoughtSignature":"sig-123"}]}}]}`
	if err := acc.applyPayload(payload, func(event provideriface.StreamEvent) { events = append(events, event) }); err != nil {
		t.Fatalf("applyPayload error: %v", err)
	}
	wantTypes := []provideriface.StreamEventType{
		provideriface.StreamEventToolCallStarted,
		provideriface.StreamEventToolCallArgumentsSnapshot,
		provideriface.StreamEventToolCallCompleted,
	}
	gotTypes := make([]provideriface.StreamEventType, 0, len(events))
	for _, event := range events {
		gotTypes = append(gotTypes, event.Type)
	}
	if !reflect.DeepEqual(gotTypes, wantTypes) {
		t.Fatalf("event types = %v, want %v (events=%+v)", gotTypes, wantTypes, events)
	}
	if events[0].ToolCallID != "call_weather" || events[0].ToolName != "weather" {
		t.Fatalf("started event = %+v, want call id/name", events[0])
	}
	if events[1].ArgumentsSnapshot != `{"city":"Paris"}` {
		t.Fatalf("snapshot arguments = %q, want JSON args", events[1].ArgumentsSnapshot)
	}
	if events[2].Arguments != `{"city":"Paris"}` {
		t.Fatalf("completed arguments = %q, want JSON args", events[2].Arguments)
	}
	for i, event := range events {
		if event.ProviderID != "google" || event.Model != "gemini-3.5-flash" || event.StartedAtUnixMs == 0 || event.RecordedAtUnixMs == 0 {
			t.Fatalf("event[%d] normalization context = %+v", i, event)
		}
	}
	metadataJSON, _ := json.Marshal(events[2].Metadata)
	if string(metadataJSON) == "null" || !strings.Contains(string(metadataJSON), "sig-123") {
		t.Fatalf("completed metadata = %s, want thought signature preserved", metadataJSON)
	}
}

func TestGoogleThinkingConfigRequestsThoughtSummaries(t *testing.T) {
	cfg := googleThinkingConfigForRequest(provideriface.Request{
		Model:    "gemini-3.5-flash",
		Thinking: "high",
		ModelCatalog: pebblestore.ModelCatalogRecord{ThinkingMappings: []pebblestore.ModelCatalogThinkingMapping{{
			SwarmSetting:           "high",
			ProviderParameter:      "generationConfig.thinkingConfig.thinkingLevel",
			ProviderValue:          "high",
			EffectiveProviderValue: "high",
			Behavior:               "effort",
		}}},
	})
	if cfg == nil || cfg.ThinkingLevel != "high" || !cfg.IncludeThoughts {
		t.Fatalf("google thinking config = %+v, want high with thought summaries", cfg)
	}

	off := googleThinkingConfigForRequest(provideriface.Request{Model: "gemini-2.5-flash", Thinking: "off"})
	if off == nil || off.ThinkingBudget == nil || *off.ThinkingBudget != 0 || off.IncludeThoughts {
		t.Fatalf("google off thinking config = %+v, want disabled without summaries", off)
	}
}

func TestGoogleStreamSeparatesThoughtSummariesFromAnswer(t *testing.T) {
	acc := newGoogleStreamAccumulator("gemini-3.5-flash")
	events := make([]provideriface.StreamEvent, 0, 2)
	payload := `{"candidates":[{"finishReason":"STOP","content":{"parts":[{"text":"Checking the constraints.","thought":true},{"text":"Final answer."}]}}]}`
	if err := acc.applyPayload(payload, func(event provideriface.StreamEvent) { events = append(events, event) }); err != nil {
		t.Fatalf("applyPayload error: %v", err)
	}
	if len(events) != 2 || events[0].Type != provideriface.StreamEventReasoningSummaryDelta || events[0].DeltaMode != provideriface.StreamEventDeltaModeReplace || events[0].ReasoningKey != "google-thinking" || events[0].Delta != "Checking the constraints." || events[1].Type != provideriface.StreamEventOutputTextDelta {
		t.Fatalf("stream events = %+v, want reasoning then answer", events)
	}
	response := acc.response()
	if response.ReasoningSummary != "Checking the constraints." || response.Text != "Final answer." {
		t.Fatalf("response = %+v, want separate reasoning and answer", response)
	}
}

func TestGoogleStreamPreservesLateThoughtSignatureForFunctionCall(t *testing.T) {
	acc := newGoogleStreamAccumulator("gemini-3.5-flash")
	first := `{"candidates":[{"index":0,"content":{"parts":[{"functionCall":{"id":"call_read","name":"read","args":{"path":"README.md"}}}]}}]}`
	if err := acc.applyPayload(first, nil); err != nil {
		t.Fatalf("apply first payload: %v", err)
	}
	last := `{"candidates":[{"index":0,"finishReason":"STOP","content":{"parts":[{"text":"","thoughtSignature":"late-sig"}]}}]}`
	if err := acc.applyPayload(last, nil); err != nil {
		t.Fatalf("apply final payload: %v", err)
	}
	response := acc.response()
	if len(response.FunctionCalls) != 1 {
		t.Fatalf("function calls = %+v, want one", response.FunctionCalls)
	}
	googleMetadata, ok := response.FunctionCalls[0].Metadata["google"].(map[string]any)
	if !ok || googleMetadata["thought_signature"] != "late-sig" {
		t.Fatalf("function call metadata = %+v, want late thought signature", response.FunctionCalls[0].Metadata)
	}
}

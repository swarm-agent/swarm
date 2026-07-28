package google

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/privacy"
	providerdiagnostics "swarm/packages/swarmd/internal/provider/diagnostics"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	generateContentURL         = "https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent"
	streamGenerateContentURL   = "https://generativelanguage.googleapis.com/v1beta/models/%s:streamGenerateContent"
	googleAPIKeyHeader         = "x-goog-api-key"
	maxResponseBytes           = 8 << 20
	maxStreamEvents            = 16_384
	maxStreamOutputBytes       = 4 << 20
	maxStreamToolArgumentBytes = 1 << 20
	maxInlineRequestBytes      = 20 << 20
)

var googleAPIKeyQueryPattern = regexp.MustCompile(`(?i)([?&]key=)[^&#\s]+`)

type Runner struct {
	authStore  *pebblestore.AuthStore
	httpClient *http.Client
}

type googleAuth struct {
	APIKey      string
	Fingerprint string
}

type googleContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []googlePart `json:"parts,omitempty"`
}

type googlePart struct {
	Text                  string                  `json:"text,omitempty"`
	InlineData            *googleInlineData       `json:"inlineData,omitempty"`
	FunctionCall          *googleFunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse      *googleFunctionResponse `json:"functionResponse,omitempty"`
	ThoughtSignature      string                  `json:"thoughtSignature,omitempty"`
	ThoughtSignatureSnake string                  `json:"thought_signature,omitempty"`
}

type googleInlineData struct {
	MIMEType string `json:"mimeType"`
	Data     string `json:"data"`
}

type googleFunctionCall struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name"`
	Args any    `json:"args,omitempty"`
}

type googleFunctionResponse struct {
	ID       string         `json:"id,omitempty"`
	Name     string         `json:"name"`
	Response map[string]any `json:"response,omitempty"`
}

type googleTool struct {
	FunctionDeclarations []googleFunctionDeclaration `json:"functionDeclarations,omitempty"`
}

type googleFunctionDeclaration struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

type googleToolConfig struct {
	FunctionCallingConfig googleFunctionCallingConfig `json:"functionCallingConfig"`
}

type googleFunctionCallingConfig struct {
	Mode string `json:"mode"`
}

type googleGenerationConfig struct {
	ThinkingConfig *googleThinkingConfig `json:"thinkingConfig,omitempty"`
}

type googleThinkingConfig struct {
	ThinkingBudget *int   `json:"thinkingBudget,omitempty"`
	ThinkingLevel  string `json:"thinkingLevel,omitempty"`
}

type googleRequest struct {
	Contents          []googleContent         `json:"contents"`
	SystemInstruction *googleContent          `json:"systemInstruction,omitempty"`
	Tools             []googleTool            `json:"tools,omitempty"`
	ToolConfig        *googleToolConfig       `json:"toolConfig,omitempty"`
	GenerationConfig  *googleGenerationConfig `json:"generationConfig,omitempty"`
}

type googleResponse struct {
	Candidates    []googleCandidate    `json:"candidates"`
	UsageMetadata *googleUsageMetadata `json:"usageMetadata,omitempty"`
}

type googleCandidate struct {
	Content      googleContent `json:"content"`
	FinishReason string        `json:"finishReason"`
}

type googleUsageMetadata struct {
	PromptTokenCount        int64 `json:"promptTokenCount,omitempty"`
	CandidatesTokenCount    int64 `json:"candidatesTokenCount,omitempty"`
	ResponseTokenCount      int64 `json:"responseTokenCount,omitempty"`
	ThoughtsTokenCount      int64 `json:"thoughtsTokenCount,omitempty"`
	TotalTokenCount         int64 `json:"totalTokenCount,omitempty"`
	CachedContentTokenCount int64 `json:"cachedContentTokenCount,omitempty"`
	ToolUsePromptTokenCount int64 `json:"toolUsePromptTokenCount,omitempty"`
}

func NewRunner(authStore *pebblestore.AuthStore) *Runner {
	return &Runner{
		authStore: authStore,
		httpClient: &http.Client{
			Timeout: 10 * time.Minute,
		},
	}
}

func (r *Runner) ID() string {
	return "google"
}

func (r *Runner) ExecutionEpochLifecycle() provideriface.ExecutionEpochLifecycleCapabilities {
	return provideriface.ExecutionEpochLifecycleCapabilities{
		ContextMode: provideriface.ExecutionEpochContextStatelessFullInput,
	}
}

func (r *Runner) MediaCapabilityDeclaration(ctx context.Context) (provideriface.MediaAdapterDeclaration, error) {
	auth, err := r.ensureAuth(ctx)
	if err != nil {
		return provideriface.MediaAdapterDeclaration{}, err
	}
	return provideriface.MediaAdapterDeclaration{
		AdapterID:             provideriface.MediaAdapterIDGoogleGenerateContentV1,
		ProviderID:            "google",
		ProviderSurface:       provideriface.MediaProviderSurfaceGoogleGenerateContent,
		CredentialSurface:     provideriface.MediaCredentialSurfaceGoogleAPIKey,
		CredentialFingerprint: auth.Fingerprint,
		Inputs: []provideriface.MediaAdapterCapability{{
			Modality: "image", Semantics: pebblestore.ModelCatalogMediaSemanticsNative,
			MIMETypes: []string{"image/heic", "image/heif", "image/jpeg", "image/png", "image/webp"},
			// Keep one immutable asset below the documented 20 MB total request limit
			// after base64 expansion; buildGoogleRequest enforces the exact payload total.
			// The shared durable message ceiling is intentionally narrower than Google's
			// documented 3,600-file model ceiling.
			ContentTypes: []string{"inline_data"}, MaxBytes: 14 << 20, MaxCount: pebblestore.SessionMediaDefaultMaxCount,
		}},
	}, nil
}

func (r *Runner) CreateResponse(ctx context.Context, req provideriface.Request) (provideriface.Response, error) {
	return r.createResponse(ctx, req)
}

func (r *Runner) CreateResponseStreaming(ctx context.Context, req provideriface.Request, onEvent func(provideriface.StreamEvent)) (provideriface.Response, error) {
	return r.createStreamingResponse(ctx, req, onEvent)
}

func (r *Runner) createResponse(ctx context.Context, req provideriface.Request) (provideriface.Response, error) {
	if r.authStore == nil {
		return provideriface.Response{}, errors.New("google runner auth store is not configured")
	}
	modelID := strings.TrimSpace(req.Model)
	if modelID == "" {
		return provideriface.Response{}, errors.New("model is required")
	}
	auth, err := r.ensureAuth(ctx)
	if err != nil {
		return provideriface.Response{}, err
	}
	if err := validateGoogleMediaSurface(req.MediaContract); err != nil {
		return provideriface.Response{}, err
	}

	requestPayload, err := buildGoogleRequest(req)
	if err != nil {
		return provideriface.Response{}, err
	}
	raw, err := json.Marshal(requestPayload)
	if err != nil {
		return provideriface.Response{}, fmt.Errorf("marshal google request: %w", err)
	}

	endpoint := fmt.Sprintf(generateContentURL, url.PathEscape(modelID))
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return provideriface.Response{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set(googleAPIKeyHeader, auth.APIKey)

	providerdiagnostics.LogRequest("google", "generateContent", httpReq, raw)
	resp, err := r.httpClient.Do(httpReq)
	if err != nil {
		providerdiagnostics.LogErrorContext(ctx, "google", "generateContent", err)
		return provideriface.Response{}, sanitizeGoogleError("google generateContent request failed", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	providerdiagnostics.LogResponse("google", "generateContent", resp, body)
	if err != nil {
		providerdiagnostics.LogErrorContext(ctx, "google", "generateContent", err)
		return provideriface.Response{}, sanitizeGoogleError("read google generateContent response", err)
	}
	if resp.StatusCode >= http.StatusBadRequest {
		return provideriface.Response{}, googleStatusError("google generateContent failed", resp.StatusCode, body)
	}

	var decoded googleResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return provideriface.Response{}, sanitizeGoogleError("decode google response", err)
	}
	result := parseGoogleResponse(decoded)
	result.Model = modelID
	return result, nil
}

func (r *Runner) createStreamingResponse(ctx context.Context, req provideriface.Request, onEvent func(provideriface.StreamEvent)) (provideriface.Response, error) {
	if r.authStore == nil {
		return provideriface.Response{}, errors.New("google runner auth store is not configured")
	}
	modelID := strings.TrimSpace(req.Model)
	if modelID == "" {
		return provideriface.Response{}, errors.New("model is required")
	}
	auth, err := r.ensureAuth(ctx)
	if err != nil {
		return provideriface.Response{}, err
	}
	if err := validateGoogleMediaSurface(req.MediaContract); err != nil {
		return provideriface.Response{}, err
	}

	requestPayload, err := buildGoogleRequest(req)
	if err != nil {
		return provideriface.Response{}, err
	}
	raw, err := json.Marshal(requestPayload)
	if err != nil {
		return provideriface.Response{}, fmt.Errorf("marshal google request: %w", err)
	}

	endpoint := fmt.Sprintf(streamGenerateContentURL, url.PathEscape(modelID))
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return provideriface.Response{}, err
	}
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Content-Type", "application/json")
	query := httpReq.URL.Query()
	query.Set("alt", "sse")
	httpReq.URL.RawQuery = query.Encode()
	httpReq.Header.Set(googleAPIKeyHeader, auth.APIKey)

	providerdiagnostics.LogRequest("google", "streamGenerateContent", httpReq, raw)
	resp, err := r.httpClient.Do(httpReq)
	if err != nil {
		providerdiagnostics.LogErrorContext(ctx, "google", "streamGenerateContent", err)
		return provideriface.Response{}, sanitizeGoogleError("google streamGenerateContent request failed", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= http.StatusBadRequest {
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
		providerdiagnostics.LogResponse("google", "streamGenerateContent", resp, body)
		if readErr != nil {
			providerdiagnostics.LogErrorContext(ctx, "google", "streamGenerateContent", readErr)
			return provideriface.Response{}, sanitizeGoogleError("read google streamGenerateContent error response", readErr)
		}
		return provideriface.Response{}, googleStatusError("google streamGenerateContent failed", resp.StatusCode, body)
	}

	providerdiagnostics.LogResponse("google", "streamGenerateContent", resp, nil)
	accumulator := newGoogleStreamAccumulator(modelID)
	if err := parseGoogleEventStream(resp.Body, func(payload string) error {
		providerdiagnostics.LogStreamChunkContext(ctx, "google", "streamGenerateContent", []byte(payload))
		return accumulator.applyPayload(payload, onEvent)
	}); err != nil {
		providerdiagnostics.LogErrorContext(ctx, "google", "streamGenerateContent", err)
		return provideriface.Response{}, sanitizeGoogleError("decode google stream response", err)
	}
	if !accumulator.finished {
		return provideriface.Response{}, errors.New("google stream ended without a finish reason")
	}
	return accumulator.response(), nil
}

func (r *Runner) ensureAuth(ctx context.Context) (googleAuth, error) {
	if r == nil || r.authStore == nil {
		return googleAuth{}, errors.New("google runner auth store is not configured")
	}
	principal, principalOK := identity.PrincipalFromContext(ctx)
	if !principalOK {
		return googleAuth{}, identity.ErrPrincipalRequired
	}
	record, ok, err := r.authStore.GetActiveCredentialForAccount(principal.AccountScopeID, "google")
	if err != nil {
		return googleAuth{}, fmt.Errorf("read google auth: %w", err)
	}
	if !ok {
		return googleAuth{}, errors.New("google auth is not configured")
	}
	if apiKey := strings.TrimSpace(record.APIKey); apiKey != "" {
		fingerprint := sha256.Sum256([]byte(strings.Join([]string{record.AccountScopeID, "google", record.ID, record.Type}, "\x00")))
		return googleAuth{APIKey: apiKey, Fingerprint: hex.EncodeToString(fingerprint[:16])}, nil
	}
	return googleAuth{}, errors.New("google api key is required")
}

func buildGoogleRequest(req provideriface.Request) (googleRequest, error) {
	contents, err := buildGoogleContents(req)
	if err != nil {
		return googleRequest{}, err
	}
	out := googleRequest{Contents: contents}
	if strings.TrimSpace(req.Instructions) != "" {
		out.SystemInstruction = &googleContent{
			Parts: []googlePart{{Text: strings.TrimSpace(req.Instructions)}},
		}
	}
	if thinkingConfig := googleThinkingConfigForRequest(req); thinkingConfig != nil {
		out.GenerationConfig = &googleGenerationConfig{ThinkingConfig: thinkingConfig}
	}
	if len(req.Tools) > 0 {
		declarations := make([]googleFunctionDeclaration, 0, len(req.Tools))
		for _, definition := range req.Tools {
			name := strings.TrimSpace(definition.Name)
			if name == "" {
				continue
			}
			declarations = append(declarations, googleFunctionDeclaration{
				Name:        name,
				Description: strings.TrimSpace(definition.Description),
				Parameters:  sanitizeGoogleToolParameters(definition.Parameters),
			})
		}
		if len(declarations) > 0 {
			out.Tools = []googleTool{{FunctionDeclarations: declarations}}
			out.ToolConfig = &googleToolConfig{
				FunctionCallingConfig: googleFunctionCallingConfig{Mode: "AUTO"},
			}
		}
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return googleRequest{}, fmt.Errorf("marshal google request for size validation: %w", err)
	}
	if len(encoded) > maxInlineRequestBytes {
		return googleRequest{}, errors.New("google inline request exceeds the 20 MB request limit")
	}
	return out, nil
}

func googleThinkingConfigForRequest(req provideriface.Request) *googleThinkingConfig {
	level := normalizeGoogleThinkingLevel(req.Thinking)
	if level == "" {
		return nil
	}
	if config := googleThinkingConfigFromCatalog(req.ModelCatalog, level); config != nil {
		return config
	}
	if !supportsGoogleThinking(req.Model) {
		return nil
	}
	return googleLegacyThinkingBudgetConfig(level)
}

func googleThinkingConfigFromCatalog(catalog any, level string) *googleThinkingConfig {
	record, ok := catalog.(pebblestore.ModelCatalogRecord)
	if !ok {
		return nil
	}
	for _, mapping := range record.ThinkingMappings {
		if !strings.EqualFold(strings.TrimSpace(mapping.SwarmSetting), level) {
			continue
		}
		return googleThinkingConfigFromMapping(mapping, level)
	}
	return nil
}

func googleThinkingConfigFromMapping(mapping pebblestore.ModelCatalogThinkingMapping, level string) *googleThinkingConfig {
	providerParameter := strings.ToLower(strings.TrimSpace(mapping.ProviderParameter))
	providerValue := firstNonEmpty(strings.TrimSpace(mapping.EffectiveProviderValue), strings.TrimSpace(mapping.ProviderValue))
	behavior := strings.ToLower(strings.TrimSpace(mapping.Behavior))
	if behavior == "disabled" || strings.EqualFold(level, "off") {
		return &googleThinkingConfig{ThinkingBudget: intPointer(0)}
	}
	if strings.Contains(providerParameter, "thinkinglevel") {
		providerValue = strings.ToLower(strings.TrimSpace(providerValue))
		switch providerValue {
		case "minimal", "low", "medium", "high":
			return &googleThinkingConfig{ThinkingLevel: providerValue}
		default:
			return nil
		}
	}
	if strings.Contains(providerParameter, "thinkingbudget") {
		budget := googleThinkingBudgetFromMapping(providerValue)
		if budget == nil {
			return nil
		}
		return &googleThinkingConfig{ThinkingBudget: budget}
	}
	return nil
}

func googleThinkingBudgetFromMapping(providerValue string) *int {
	providerValue = strings.TrimSpace(providerValue)
	if providerValue == "" {
		return nil
	}
	var parsed int64
	if _, err := fmt.Sscan(providerValue, &parsed); err != nil {
		return nil
	}
	if parsed < -1 {
		return nil
	}
	value := int(parsed)
	return &value
}

func supportsGoogleThinking(modelID string) bool {
	modelID = strings.ToLower(strings.TrimSpace(modelID))
	if modelID == "" || !strings.Contains(modelID, "gemini") {
		return false
	}
	return strings.Contains(modelID, "gemini-3") ||
		strings.Contains(modelID, "gemini-2.5") ||
		strings.Contains(modelID, "thinking")
}

func googleLegacyThinkingBudgetConfig(level string) *googleThinkingConfig {
	switch level {
	case "off":
		return &googleThinkingConfig{ThinkingBudget: intPointer(0)}
	case "low":
		return &googleThinkingConfig{ThinkingBudget: intPointer(1024)}
	case "medium":
		return &googleThinkingConfig{ThinkingBudget: intPointer(4096)}
	case "high":
		return &googleThinkingConfig{ThinkingBudget: intPointer(8192)}
	case "xhigh":
		return &googleThinkingConfig{ThinkingBudget: intPointer(16384)}
	default:
		return nil
	}
}

func normalizeGoogleThinkingLevel(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "off":
		return "off"
	case "minimal":
		return "minimal"
	case "low":
		return "low"
	case "medium":
		return "medium"
	case "high":
		return "high"
	case "xhigh":
		return "xhigh"
	default:
		return ""
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func intPointer(value int) *int {
	return &value
}

func sanitizeGoogleToolParameters(parameters map[string]any) map[string]any {
	if len(parameters) == 0 {
		return nil
	}
	cleaned, ok := sanitizeGoogleToolSchemaValue(parameters).(map[string]any)
	if !ok || len(cleaned) == 0 {
		return nil
	}
	return cleaned
}

func sanitizeGoogleToolSchemaValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			if key == "additionalProperties" {
				continue
			}
			out[key] = sanitizeGoogleToolSchemaValue(item)
		}
		return out
	case []any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, sanitizeGoogleToolSchemaValue(item))
		}
		return out
	case []map[string]any:
		out := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			cleaned, ok := sanitizeGoogleToolSchemaValue(item).(map[string]any)
			if !ok {
				continue
			}
			out = append(out, cleaned)
		}
		return out
	default:
		return value
	}
}

func buildGoogleContents(req provideriface.Request) ([]googleContent, error) {
	input := req.Input
	contents := make([]googleContent, 0, len(input))
	mediaCounts := map[string]int{}
	callNameByID := make(map[string]string, 32)

	for i := 0; i < len(input); i++ {
		item := input[i]
		if typeName, ok := stringField(item, "type"); ok {
			switch strings.ToLower(strings.TrimSpace(typeName)) {
			case "function_call":
				parts := make([]googlePart, 0, 4)
				for ; i < len(input); i++ {
					current := input[i]
					currentType, _ := stringField(current, "type")
					if !strings.EqualFold(strings.TrimSpace(currentType), "function_call") {
						i--
						break
					}
					callID, _ := stringField(current, "call_id")
					name, _ := stringField(current, "name")
					name = strings.TrimSpace(name)
					if name == "" {
						name = "tool"
					}
					argsRaw, _ := stringField(current, "arguments")
					callNameByID[callID] = name
					part := googlePart{
						FunctionCall: &googleFunctionCall{
							ID:   extractGoogleProviderCallID(current),
							Name: name,
							Args: parseFunctionArgs(argsRaw),
						},
					}
					if thoughtSignature := extractGoogleThoughtSignature(current); thoughtSignature != "" {
						part.ThoughtSignature = thoughtSignature
					}
					parts = append(parts, part)
				}
				if len(parts) > 0 {
					contents = append(contents, googleContent{
						Role:  "model",
						Parts: parts,
					})
				}
			case "function_call_output":
				parts := make([]googlePart, 0, 4)
				for ; i < len(input); i++ {
					current := input[i]
					currentType, _ := stringField(current, "type")
					if !strings.EqualFold(strings.TrimSpace(currentType), "function_call_output") {
						i--
						break
					}
					callID, _ := stringField(current, "call_id")
					outputRaw, _ := stringField(current, "output")
					name := strings.TrimSpace(callNameByID[callID])
					if name == "" {
						name = "tool"
					}
					parts = append(parts, googlePart{
						FunctionResponse: &googleFunctionResponse{
							ID:   extractGoogleProviderCallID(current),
							Name: name,
							Response: map[string]any{
								"output": parseFunctionOutput(outputRaw),
							},
						},
					})
				}
				if len(parts) > 0 {
					contents = append(contents, googleContent{
						Role:  "user",
						Parts: parts,
					})
				}
			}
			continue
		}

		role, _ := stringField(item, "role")
		googleRole := "user"
		if strings.EqualFold(strings.TrimSpace(role), "assistant") {
			googleRole = "model"
		}
		parts, err := googleMessageParts(req, item["content"], googleRole, mediaCounts)
		if err != nil {
			return nil, err
		}
		if len(parts) == 0 {
			continue
		}
		contents = append(contents, googleContent{Role: googleRole, Parts: parts})
	}
	return contents, nil
}

func googleMessageParts(req provideriface.Request, content any, role string, mediaCounts map[string]int) ([]googlePart, error) {
	if text, ok := content.(string); ok {
		text = strings.TrimSpace(text)
		if text == "" {
			return nil, nil
		}
		return []googlePart{{Text: text}}, nil
	}
	contentParts, ok := googleInputContentMaps(content)
	if !ok {
		return nil, errors.New("google message content is malformed")
	}
	parts := make([]googlePart, 0, len(contentParts))
	for _, part := range contentParts {
		typeName, _ := stringField(part, "type")
		typeName = strings.ToLower(strings.TrimSpace(typeName))
		switch typeName {
		case "input_text", "output_text", "text":
			text, _ := stringField(part, "text")
			text = strings.TrimSpace(text)
			if text != "" {
				parts = append(parts, googlePart{Text: text})
			}
		case "session_media":
			if role != "user" {
				return nil, errors.New("google media input is only valid in user messages")
			}
			payload, ok := part["media"].(provideriface.SessionMediaPayload)
			if !ok {
				return nil, errors.New("google media input is malformed")
			}
			if err := validateGoogleMediaPayload(req, payload, mediaCounts); err != nil {
				return nil, err
			}
			parts = append(parts, googlePart{InlineData: &googleInlineData{MIMEType: strings.ToLower(strings.TrimSpace(payload.MIMEType)), Data: base64.StdEncoding.EncodeToString(payload.Bytes)}})
		default:
			return nil, fmt.Errorf("google message content type %q is not implemented", typeName)
		}
	}
	return parts, nil
}

func googleInputContentMaps(value any) ([]map[string]any, bool) {
	switch typed := value.(type) {
	case []map[string]any:
		return typed, true
	case []any:
		out := make([]map[string]any, 0, len(typed))
		for _, value := range typed {
			part, ok := value.(map[string]any)
			if !ok {
				return nil, false
			}
			out = append(out, part)
		}
		return out, true
	case nil:
		return nil, true
	default:
		return nil, false
	}
}

func validateGoogleMediaPayload(req provideriface.Request, payload provideriface.SessionMediaPayload, counts map[string]int) error {
	contract := req.MediaContract
	if contract.Hash == "" || req.ProviderConfigurationHash == "" ||
		!strings.EqualFold(strings.TrimSpace(contract.ProviderID), "google") ||
		contract.ProviderSurface != provideriface.MediaProviderSurfaceGoogleGenerateContent ||
		contract.CredentialSurface != provideriface.MediaCredentialSurfaceGoogleAPIKey ||
		contract.AdapterID != provideriface.MediaAdapterIDGoogleGenerateContentV1 {
		return errors.New("google media contract does not match the active API-key generateContent surface")
	}
	if len(payload.Bytes) == 0 || payload.Size <= 0 || int64(len(payload.Bytes)) != payload.Size || strings.TrimSpace(payload.AssetID) == "" || strings.TrimSpace(payload.DigestSHA256) == "" {
		return errors.New("google media payload failed immutable size or identity validation")
	}
	digest := sha256.Sum256(payload.Bytes)
	if !strings.EqualFold(hex.EncodeToString(digest[:]), strings.TrimSpace(payload.DigestSHA256)) {
		return errors.New("google media payload failed immutable digest validation")
	}
	if !strings.EqualFold(strings.TrimSpace(payload.Modality), "image") {
		return errors.New("google media payload modality is not implemented")
	}
	for _, capability := range contract.Capabilities {
		if capability.State != provideriface.MediaCapabilityStateAllowed || !strings.EqualFold(capability.Modality, payload.Modality) {
			continue
		}
		if !strings.EqualFold(capability.Semantics, pebblestore.ModelCatalogMediaSemanticsNative) ||
			!googleStringAllowed(capability.ContentTypes, "inline_data") ||
			!googleStringAllowed(capability.MIMETypes, payload.MIMEType) ||
			strings.TrimSpace(payload.FileType) != "" || capability.MaxBytes <= 0 || payload.Size > capability.MaxBytes {
			break
		}
		counts[capability.Modality]++
		if capability.MaxCount <= 0 || counts[capability.Modality] > capability.MaxCount {
			return errors.New("google media payload exceeds the active contract count limit")
		}
		return nil
	}
	return errors.New("google media payload is denied by the active contract")
}

func googleStringAllowed(values []string, expected string) bool {
	expected = strings.ToLower(strings.TrimSpace(expected))
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), expected) {
			return true
		}
	}
	return false
}

func validateGoogleMediaSurface(contract provideriface.SessionMediaContract) error {
	if strings.TrimSpace(contract.Hash) == "" {
		return nil
	}
	if !strings.EqualFold(strings.TrimSpace(contract.ProviderID), "google") || contract.ProviderSurface != provideriface.MediaProviderSurfaceGoogleGenerateContent || contract.CredentialSurface != provideriface.MediaCredentialSurfaceGoogleAPIKey || contract.AdapterID != provideriface.MediaAdapterIDGoogleGenerateContentV1 {
		return errors.New("media contract does not match the active Google API-key generateContent surface")
	}
	return nil
}

func extractGoogleThoughtSignature(item map[string]any) string {
	if signature, ok := stringField(item, "thought_signature"); ok {
		if signature := strings.TrimSpace(signature); signature != "" {
			return signature
		}
	}
	if signature, ok := stringField(item, "thoughtSignature"); ok {
		if signature := strings.TrimSpace(signature); signature != "" {
			return signature
		}
	}
	metadata, ok := mapField(item, "metadata")
	if !ok {
		return ""
	}
	googleMetadata, ok := mapField(metadata, "google")
	if !ok {
		return ""
	}
	if signature, ok := stringField(googleMetadata, "thought_signature"); ok {
		if signature := strings.TrimSpace(signature); signature != "" {
			return signature
		}
	}
	if signature, ok := stringField(googleMetadata, "thoughtSignature"); ok {
		if signature := strings.TrimSpace(signature); signature != "" {
			return signature
		}
	}
	return ""
}

func parseGoogleResponse(resp googleResponse) provideriface.Response {
	if len(resp.Candidates) == 0 {
		return provideriface.Response{}
	}
	candidate := resp.Candidates[0]
	out := provideriface.Response{
		StopReason: strings.TrimSpace(candidate.FinishReason),
		Usage:      parseGoogleUsage(resp),
	}

	textParts := make([]string, 0, len(candidate.Content.Parts))
	functionCalls := make([]provideriface.FunctionCall, 0, len(candidate.Content.Parts))
	pendingThoughtSignature := ""
	functionCallSequence := 0
	for _, part := range candidate.Content.Parts {
		partThoughtSignature := partThoughtSignatureValue(part)
		if text := strings.TrimSpace(part.Text); text != "" {
			textParts = append(textParts, text)
		}
		if part.FunctionCall == nil {
			if partThoughtSignature != "" {
				pendingThoughtSignature = partThoughtSignature
			}
			continue
		}
		functionCallSequence++
		if partThoughtSignature == "" {
			partThoughtSignature = strings.TrimSpace(pendingThoughtSignature)
		}
		pendingThoughtSignature = ""
		functionCalls = append(functionCalls, buildGoogleFunctionCall(part, functionCallSequence, partThoughtSignature))
	}
	out.Text = strings.TrimSpace(strings.Join(textParts, "\n\n"))
	out.FunctionCalls = functionCalls
	return out
}

type googleStreamAccumulator struct {
	modelID                 string
	merged                  googleResponse
	text                    string
	functionCalls           []provideriface.FunctionCall
	pendingThoughtSignature string
	toolState               *googleToolCallConstructionState
	finished                bool
	eventCount              int
	outputBytes             int
	toolArgumentBytes       int
}

func newGoogleStreamAccumulator(modelID string) *googleStreamAccumulator {
	return &googleStreamAccumulator{
		modelID:   strings.TrimSpace(modelID),
		toolState: newGoogleToolCallConstructionState(),
	}
}

func (a *googleStreamAccumulator) applyPayload(payload string, onEvent func(provideriface.StreamEvent)) error {
	payload = strings.TrimSpace(payload)
	if payload == "" {
		return nil
	}
	a.eventCount++
	if a.eventCount > maxStreamEvents {
		return errors.New("google stream event limit exceeded")
	}
	var decoded googleResponse
	if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
		return fmt.Errorf("decode google stream payload: %w", err)
	}
	a.merged = mergeGoogleResponses(a.merged, decoded)
	if len(decoded.Candidates) == 0 {
		return nil
	}
	candidate := decoded.Candidates[0]
	functionCallSequence := 0
	for _, part := range candidate.Content.Parts {
		partThoughtSignature := partThoughtSignatureValue(part)
		if part.Text != "" {
			a.outputBytes += len(part.Text)
			if a.outputBytes > maxStreamOutputBytes {
				return errors.New("google stream output limit exceeded")
			}
			a.text += part.Text
			if onEvent != nil {
				onEvent(provideriface.StreamEvent{Type: provideriface.StreamEventOutputTextDelta, Delta: part.Text})
			}
		}
		if part.FunctionCall == nil {
			if partThoughtSignature != "" {
				a.pendingThoughtSignature = partThoughtSignature
			}
			continue
		}
		functionCallSequence++
		if partThoughtSignature == "" {
			partThoughtSignature = strings.TrimSpace(a.pendingThoughtSignature)
		}
		a.pendingThoughtSignature = ""
		call := buildGoogleFunctionCall(part, functionCallSequence, partThoughtSignature)
		a.toolArgumentBytes += len(call.Arguments)
		if a.toolArgumentBytes > maxStreamToolArgumentBytes {
			return errors.New("google stream tool argument limit exceeded")
		}
		a.upsertFunctionCall(call)
		a.emitToolCallConstructionEvents(functionCallSequence-1, call, onEvent)
	}
	if strings.TrimSpace(candidate.FinishReason) != "" {
		a.finished = true
		a.completeToolCallConstructionEvents(candidate.FinishReason, onEvent)
	}
	return nil
}

func (a *googleStreamAccumulator) response() provideriface.Response {
	result := parseGoogleResponse(a.merged)
	if strings.TrimSpace(result.Model) == "" {
		result.Model = a.modelID
	}
	if a.text != "" {
		result.Text = a.text
	}
	if len(a.functionCalls) > 0 {
		result.FunctionCalls = a.functionCalls
	}
	return result
}

func googleStatusError(prefix string, statusCode int, body []byte) error {
	detail := strings.TrimSpace(sanitizeGoogleText(string(body)))
	if detail == "" {
		return fmt.Errorf("%s status=%d", strings.TrimSpace(prefix), statusCode)
	}
	return fmt.Errorf("%s status=%d body=%s", strings.TrimSpace(prefix), statusCode, detail)
}

func sanitizeGoogleError(prefix string, err error) error {
	if err == nil {
		return nil
	}
	detail := strings.TrimSpace(sanitizeGoogleText(err.Error()))
	if strings.TrimSpace(prefix) == "" {
		if detail == "" {
			return errors.New("google request failed")
		}
		return errors.New(detail)
	}
	if detail == "" {
		return errors.New(strings.TrimSpace(prefix))
	}
	return fmt.Errorf("%s: %s", strings.TrimSpace(prefix), detail)
}

func sanitizeGoogleText(raw string) string {
	sanitized := privacy.SanitizeText(raw)
	sanitized = googleAPIKeyQueryPattern.ReplaceAllString(sanitized, `${1}[redacted]`)
	return strings.TrimSpace(sanitized)
}

func parseGoogleEventStream(reader io.Reader, onPayload func(string) error) error {
	scanner := bufio.NewScanner(io.LimitReader(reader, maxResponseBytes+1))
	scanner.Buffer(make([]byte, 0, 64*1024), maxResponseBytes)
	dataLines := make([]string, 0, 8)
	totalBytes := 0
	eventCount := 0
	flush := func() error {
		if len(dataLines) == 0 {
			return nil
		}
		payload := strings.Join(dataLines, "\n")
		dataLines = dataLines[:0]
		if strings.TrimSpace(payload) == "[DONE]" {
			return nil
		}
		eventCount++
		if eventCount > maxStreamEvents {
			return errors.New("google stream event limit exceeded")
		}
		return onPayload(payload)
	}
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		totalBytes += len(line) + 1
		if totalBytes > maxResponseBytes {
			return errors.New("google stream byte limit exceeded")
		}
		if strings.TrimSpace(line) == "" {
			if err := flush(); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimLeft(line[len("data:"):], " \t"))
		}
	}
	if err := scanner.Err(); err != nil {
		return sanitizeGoogleError("scan google event stream", err)
	}
	return flush()
}

func mergeGoogleResponses(base, next googleResponse) googleResponse {
	if len(next.Candidates) > 0 {
		base.Candidates = next.Candidates
	}
	if next.UsageMetadata != nil {
		base.UsageMetadata = next.UsageMetadata
	}
	return base
}

func (a *googleStreamAccumulator) upsertFunctionCall(call provideriface.FunctionCall) {
	callKey := strings.TrimSpace(call.CallID)
	if callKey == "" {
		a.functionCalls = append(a.functionCalls, call)
		return
	}
	for i := len(a.functionCalls) - 1; i >= 0; i-- {
		if strings.TrimSpace(a.functionCalls[i].CallID) != callKey {
			continue
		}
		a.functionCalls[i] = mergeGoogleProviderFunctionCall(a.functionCalls[i], call)
		return
	}
	a.functionCalls = append(a.functionCalls, call)
}

type googleToolCallConstructionState struct {
	seenStarted   map[int]bool
	seenCompleted map[int]bool
	arguments     map[int]string
	ids           map[int]string
	names         map[int]string
	metadata      map[int]map[string]any
}

func newGoogleToolCallConstructionState() *googleToolCallConstructionState {
	return &googleToolCallConstructionState{
		seenStarted:   make(map[int]bool),
		seenCompleted: make(map[int]bool),
		arguments:     make(map[int]string),
		ids:           make(map[int]string),
		names:         make(map[int]string),
		metadata:      make(map[int]map[string]any),
	}
}

func (a *googleStreamAccumulator) emitToolCallConstructionEvents(index int, call provideriface.FunctionCall, onEvent func(provideriface.StreamEvent)) {
	if a == nil || a.toolState == nil || onEvent == nil {
		return
	}
	state := a.toolState
	if callID := strings.TrimSpace(call.CallID); callID != "" {
		state.ids[index] = callID
	}
	if name := strings.TrimSpace(call.Name); name != "" {
		state.names[index] = name
	}
	if metadata := cloneGoogleMetadataMap(call.Metadata); len(metadata) > 0 {
		state.metadata[index] = metadata
	}
	if !state.seenStarted[index] {
		state.seenStarted[index] = true
		onEvent(provideriface.StreamEvent{
			Type:          provideriface.StreamEventToolCallStarted,
			ToolCallID:    state.ids[index],
			ToolCallIndex: intPointer(index),
			ToolName:      state.names[index],
			Metadata:      cloneGoogleMetadataMap(state.metadata[index]),
		})
	}
	arguments := strings.TrimSpace(call.Arguments)
	if arguments == "" {
		return
	}
	state.arguments[index] = arguments
	onEvent(provideriface.StreamEvent{
		Type:              provideriface.StreamEventToolCallArgumentsSnapshot,
		ToolCallID:        state.ids[index],
		ToolCallIndex:     intPointer(index),
		ToolName:          state.names[index],
		ArgumentsSnapshot: arguments,
		Metadata:          cloneGoogleMetadataMap(state.metadata[index]),
	})
}

func (a *googleStreamAccumulator) completeToolCallConstructionEvents(finishReason string, onEvent func(provideriface.StreamEvent)) {
	if a == nil || a.toolState == nil || onEvent == nil {
		return
	}
	state := a.toolState
	for index := range state.seenStarted {
		if state.seenCompleted[index] {
			continue
		}
		state.seenCompleted[index] = true
		onEvent(provideriface.StreamEvent{
			Type:          provideriface.StreamEventToolCallCompleted,
			ToolCallID:    state.ids[index],
			ToolCallIndex: intPointer(index),
			ToolName:      state.names[index],
			Arguments:     strings.TrimSpace(state.arguments[index]),
			Metadata: mergeGoogleFunctionCallMetadata(state.metadata[index], map[string]any{
				"google": map[string]any{"finish_reason": strings.TrimSpace(finishReason)},
			}),
		})
	}
}

func mergeGoogleProviderFunctionCall(existing, incoming provideriface.FunctionCall) provideriface.FunctionCall {
	if strings.TrimSpace(incoming.CallID) != "" {
		existing.CallID = strings.TrimSpace(incoming.CallID)
	}
	if strings.TrimSpace(incoming.Name) != "" {
		existing.Name = strings.TrimSpace(incoming.Name)
	}
	if strings.TrimSpace(incoming.Arguments) != "" && strings.TrimSpace(incoming.Arguments) != "{}" {
		existing.Arguments = strings.TrimSpace(incoming.Arguments)
	} else if strings.TrimSpace(existing.Arguments) == "" {
		existing.Arguments = strings.TrimSpace(incoming.Arguments)
	}
	if metadata := mergeGoogleFunctionCallMetadata(existing.Metadata, incoming.Metadata); len(metadata) > 0 {
		existing.Metadata = metadata
	}
	return existing
}

func mergeGoogleFunctionCallMetadata(existing, incoming map[string]any) map[string]any {
	if len(existing) == 0 && len(incoming) == 0 {
		return nil
	}
	if len(existing) == 0 {
		return cloneGoogleMetadataMap(incoming)
	}
	if len(incoming) == 0 {
		return cloneGoogleMetadataMap(existing)
	}
	merged := cloneGoogleMetadataMap(existing)
	for key, value := range incoming {
		if existingValue, ok := merged[key].(map[string]any); ok {
			if incomingValue, ok := value.(map[string]any); ok {
				merged[key] = mergeGoogleFunctionCallMetadata(existingValue, incomingValue)
				continue
			}
		}
		merged[key] = value
	}
	return merged
}

func cloneGoogleMetadataMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		if nested, ok := value.(map[string]any); ok {
			out[key] = cloneGoogleMetadataMap(nested)
			continue
		}
		out[key] = value
	}
	return out
}

func buildGoogleFunctionCall(part googlePart, sequence int, thoughtSignature string) provideriface.FunctionCall {
	name := strings.TrimSpace(part.FunctionCall.Name)
	if name == "" {
		name = "tool"
	}
	argsRaw := "{}"
	if encoded, err := json.Marshal(part.FunctionCall.Args); err == nil && len(encoded) > 0 {
		argsRaw = string(encoded)
	}
	callID, syntheticCallID := googleFunctionCallID(part, sequence)
	return provideriface.FunctionCall{
		CallID:    callID,
		Name:      name,
		Arguments: argsRaw,
		Metadata:  googleFunctionCallMetadata(thoughtSignature, syntheticCallID),
	}
}

func parseGoogleUsage(resp googleResponse) provideriface.TokenUsage {
	usage := resp.UsageMetadata
	if usage == nil {
		return provideriface.TokenUsage{}
	}

	usageRaw := map[string]any{
		"promptTokenCount":        usage.PromptTokenCount,
		"candidatesTokenCount":    usage.CandidatesTokenCount,
		"responseTokenCount":      usage.ResponseTokenCount,
		"thoughtsTokenCount":      usage.ThoughtsTokenCount,
		"totalTokenCount":         usage.TotalTokenCount,
		"cachedContentTokenCount": usage.CachedContentTokenCount,
		"toolUsePromptTokenCount": usage.ToolUsePromptTokenCount,
	}
	outputTokens := usage.CandidatesTokenCount
	if outputTokens == 0 {
		outputTokens = usage.ResponseTokenCount
	}
	out := provideriface.TokenUsage{
		InputTokens:      usage.PromptTokenCount,
		OutputTokens:     outputTokens,
		ThinkingTokens:   usage.ThoughtsTokenCount,
		TotalTokens:      usage.TotalTokenCount,
		CacheReadTokens:  usage.CachedContentTokenCount,
		CacheWriteTokens: 0,
		Source:           "google_api_usage",
		APIUsageRaw:      cloneGoogleUsageMap(usageRaw),
		APIUsageRawPath:  "usageMetadata",
		APIUsageHistory:  []map[string]any{cloneGoogleUsageMap(usageRaw)},
		APIUsagePaths:    []string{"usageMetadata"},
	}
	if out.InputTokens < 0 {
		out.InputTokens = 0
	}
	if out.OutputTokens < 0 {
		out.OutputTokens = 0
	}
	if out.ThinkingTokens < 0 {
		out.ThinkingTokens = 0
	}
	if out.CacheReadTokens < 0 {
		out.CacheReadTokens = 0
	}
	if out.TotalTokens < 0 {
		out.TotalTokens = 0
	}
	return out
}

func cloneGoogleUsageMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func partThoughtSignatureValue(part googlePart) string {
	if signature := strings.TrimSpace(part.ThoughtSignature); signature != "" {
		return signature
	}
	return strings.TrimSpace(part.ThoughtSignatureSnake)
}

func googleFunctionCallID(part googlePart, sequence int) (string, bool) {
	if part.FunctionCall != nil {
		if callID := strings.TrimSpace(part.FunctionCall.ID); callID != "" {
			return callID, false
		}
	}
	return fmt.Sprintf("google_call_%d", sequence), true
}

func googleFunctionCallMetadata(thoughtSignature string, syntheticCallID bool) map[string]any {
	thoughtSignature = strings.TrimSpace(thoughtSignature)
	if thoughtSignature == "" && !syntheticCallID {
		return nil
	}
	google := map[string]any{}
	if thoughtSignature != "" {
		google["thought_signature"] = thoughtSignature
	}
	if syntheticCallID {
		google["synthetic_call_id"] = true
	}
	return map[string]any{
		"google": google,
	}
}

func extractGoogleProviderCallID(item map[string]any) string {
	if metadata, ok := mapField(item, "metadata"); ok {
		if googleMetadata, ok := mapField(metadata, "google"); ok {
			if synthetic, ok := googleMetadata["synthetic_call_id"].(bool); ok && synthetic {
				return ""
			}
		}
	}
	callID, _ := stringField(item, "call_id")
	return strings.TrimSpace(callID)
}

func parseFunctionArgs(raw string) any {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return map[string]any{}
	}
	var decoded any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		return map[string]any{"raw": raw}
	}
	return decoded
}

func parseFunctionOutput(raw string) any {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	var decoded any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		return raw
	}
	return decoded
}

func extractInputText(content any) string {
	switch typed := content.(type) {
	case string:
		return strings.TrimSpace(typed)
	case []any:
		lines := make([]string, 0, len(typed))
		for _, item := range typed {
			part, ok := item.(map[string]any)
			if !ok {
				continue
			}
			text, _ := stringField(part, "text")
			if strings.TrimSpace(text) == "" {
				continue
			}
			lines = append(lines, strings.TrimSpace(text))
		}
		return strings.TrimSpace(strings.Join(lines, "\n"))
	case []map[string]any:
		lines := make([]string, 0, len(typed))
		for _, part := range typed {
			text, _ := stringField(part, "text")
			if strings.TrimSpace(text) == "" {
				continue
			}
			lines = append(lines, strings.TrimSpace(text))
		}
		return strings.TrimSpace(strings.Join(lines, "\n"))
	default:
		return ""
	}
}

func stringField(values map[string]any, key string) (string, bool) {
	if values == nil {
		return "", false
	}
	raw, ok := values[key]
	if !ok || raw == nil {
		return "", false
	}
	switch typed := raw.(type) {
	case string:
		return typed, true
	default:
		return fmt.Sprintf("%v", typed), true
	}
}

func mapField(values map[string]any, key string) (map[string]any, bool) {
	if values == nil {
		return nil, false
	}
	raw, ok := values[key]
	if !ok || raw == nil {
		return nil, false
	}
	typed, ok := raw.(map[string]any)
	if !ok {
		return nil, false
	}
	return typed, true
}

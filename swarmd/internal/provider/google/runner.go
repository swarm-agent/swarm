package google

import (
	"bufio"
	"bytes"
	"context"
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
	generateContentURL       = "https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent"
	streamGenerateContentURL = "https://generativelanguage.googleapis.com/v1beta/models/%s:streamGenerateContent"
	interactionsURL          = "https://generativelanguage.googleapis.com/v1beta/interactions"
	googleAPIKeyHeader       = "x-goog-api-key"
	googleReasoningKey       = "google-thinking"
	maxResponseBytes         = 8 << 20
)

var googleAPIKeyQueryPattern = regexp.MustCompile(`(?i)([?&]key=)[^&#\s]+`)

type Runner struct {
	authStore  *pebblestore.AuthStore
	httpClient *http.Client
}

type googleAuth struct {
	APIKey string
}

type googleContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []googlePart `json:"parts,omitempty"`
}

type googlePart struct {
	Text                  string                  `json:"text,omitempty"`
	Thought               bool                    `json:"thought,omitempty"`
	FunctionCall          *googleFunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse      *googleFunctionResponse `json:"functionResponse,omitempty"`
	ThoughtSignature      string                  `json:"thoughtSignature,omitempty"`
	ThoughtSignatureSnake string                  `json:"thought_signature,omitempty"`
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

type googleInteractionsGenerationConfig struct {
	ThinkingLevel     string `json:"thinking_level,omitempty"`
	ThinkingSummaries string `json:"thinking_summaries,omitempty"`
	ToolChoice        string `json:"tool_choice,omitempty"`
}

type googleThinkingConfig struct {
	ThinkingBudget  *int   `json:"thinkingBudget,omitempty"`
	ThinkingLevel   string `json:"thinkingLevel,omitempty"`
	IncludeThoughts bool   `json:"includeThoughts,omitempty"`
}

type googleRequest struct {
	Contents          []googleContent         `json:"contents"`
	SystemInstruction *googleContent          `json:"systemInstruction,omitempty"`
	Tools             []googleTool            `json:"tools,omitempty"`
	ToolConfig        *googleToolConfig       `json:"toolConfig,omitempty"`
	GenerationConfig  *googleGenerationConfig `json:"generationConfig,omitempty"`
}

type googleInteractionsRequest struct {
	Model                 string                              `json:"model"`
	Input                 any                                 `json:"input"`
	PreviousInteractionID string                              `json:"previous_interaction_id,omitempty"`
	SystemInstruction     string                              `json:"system_instruction,omitempty"`
	Tools                 []googleInteractionsTool            `json:"tools,omitempty"`
	Stream                bool                                `json:"stream,omitempty"`
	Store                 *bool                               `json:"store,omitempty"`
	GenerationConfig      *googleInteractionsGenerationConfig `json:"generation_config,omitempty"`
}

type googleInteractionsTool struct {
	Type        string         `json:"type"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

type googleResponse struct {
	Candidates    []googleCandidate    `json:"candidates"`
	UsageMetadata *googleUsageMetadata `json:"usageMetadata,omitempty"`
}

type googleInteractionResponse struct {
	ID     string                  `json:"id,omitempty"`
	Model  string                  `json:"model,omitempty"`
	Status string                  `json:"status,omitempty"`
	Steps  []googleInteractionStep `json:"steps,omitempty"`
	Usage  *googleInteractionUsage `json:"usage,omitempty"`
	Raw    map[string]any          `json:"-"`
}

type googleInteractionStep struct {
	Type      string                     `json:"type,omitempty"`
	ID        string                     `json:"id,omitempty"`
	Name      string                     `json:"name,omitempty"`
	Signature string                     `json:"signature,omitempty"`
	Summary   []googleInteractionContent `json:"summary,omitempty"`
	Content   []googleInteractionContent `json:"content,omitempty"`
	Arguments any                        `json:"arguments,omitempty"`
}

type googleInteractionContent struct {
	Type string `json:"type,omitempty"`
	Text string `json:"text,omitempty"`
}

type googleInteractionStreamEvent struct {
	EventType   string                     `json:"event_type,omitempty"`
	Index       int                        `json:"index,omitempty"`
	Step        *googleInteractionStep     `json:"step,omitempty"`
	Delta       *googleInteractionDelta    `json:"delta,omitempty"`
	Interaction *googleInteractionResponse `json:"interaction,omitempty"`
}

type googleInteractionDelta struct {
	Type      string                    `json:"type,omitempty"`
	Text      string                    `json:"text,omitempty"`
	Signature string                    `json:"signature,omitempty"`
	Arguments string                    `json:"arguments,omitempty"`
	Content   *googleInteractionContent `json:"content,omitempty"`
}

type googleInteractionUsage struct {
	TotalInputTokens   int64          `json:"total_input_tokens,omitempty"`
	TotalOutputTokens  int64          `json:"total_output_tokens,omitempty"`
	TotalThoughtTokens int64          `json:"total_thought_tokens,omitempty"`
	TotalTokens        int64          `json:"total_tokens,omitempty"`
	TotalCachedTokens  int64          `json:"total_cached_tokens,omitempty"`
	TotalToolUseTokens int64          `json:"total_tool_use_tokens,omitempty"`
	Raw                map[string]any `json:"-"`
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

	requestPayload := buildGoogleRequest(req)
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
	if shouldUseGoogleInteractions(req) {
		result, err := r.createInteractionsStreamingResponse(ctx, req, modelID, auth, onEvent)
		if err == nil {
			return result, nil
		}
		var unavailable googleInteractionsUnavailableError
		if !errors.As(err, &unavailable) {
			return provideriface.Response{}, err
		}
	}

	requestPayload := buildGoogleRequest(req)
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
	return accumulator.response(), nil
}

type googleInteractionsUnavailableError struct {
	statusCode int
}

func (e googleInteractionsUnavailableError) Error() string {
	return fmt.Sprintf("google interactions unavailable status=%d", e.statusCode)
}

func (r *Runner) createInteractionsStreamingResponse(ctx context.Context, req provideriface.Request, modelID string, auth googleAuth, onEvent func(provideriface.StreamEvent)) (provideriface.Response, error) {
	requestPayload := buildGoogleInteractionsRequest(req, modelID, true)
	raw, err := json.Marshal(requestPayload)
	if err != nil {
		return provideriface.Response{}, fmt.Errorf("marshal google interactions request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, interactionsURL, bytes.NewReader(raw))
	if err != nil {
		return provideriface.Response{}, err
	}
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set(googleAPIKeyHeader, auth.APIKey)

	providerdiagnostics.LogRequest("google", "interactions", httpReq, raw)
	resp, err := r.httpClient.Do(httpReq)
	if err != nil {
		providerdiagnostics.LogErrorContext(ctx, "google", "interactions", err)
		return provideriface.Response{}, sanitizeGoogleError("google interactions request failed", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed || resp.StatusCode == http.StatusNotImplemented {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
		providerdiagnostics.LogResponse("google", "interactions", resp, body)
		return provideriface.Response{}, googleInteractionsUnavailableError{statusCode: resp.StatusCode}
	}
	if resp.StatusCode >= http.StatusBadRequest {
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
		providerdiagnostics.LogResponse("google", "interactions", resp, body)
		if readErr != nil {
			providerdiagnostics.LogErrorContext(ctx, "google", "interactions", readErr)
			return provideriface.Response{}, sanitizeGoogleError("read google interactions error response", readErr)
		}
		return provideriface.Response{}, googleStatusError("google interactions failed", resp.StatusCode, body)
	}

	providerdiagnostics.LogResponse("google", "interactions", resp, nil)
	accumulator := newGoogleInteractionsStreamAccumulator(modelID)
	if err := parseGoogleEventStream(resp.Body, func(payload string) error {
		providerdiagnostics.LogStreamChunkContext(ctx, "google", "interactions", []byte(payload))
		return accumulator.applyPayload(payload, onEvent)
	}); err != nil {
		providerdiagnostics.LogErrorContext(ctx, "google", "interactions", err)
		return provideriface.Response{}, sanitizeGoogleError("decode google interactions stream response", err)
	}
	return accumulator.response(), nil
}

func (r *Runner) ensureAuth(ctx context.Context) (googleAuth, error) {
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
		return googleAuth{APIKey: apiKey}, nil
	}
	return googleAuth{}, errors.New("google api key is required")
}

func shouldUseGoogleInteractions(req provideriface.Request) bool {
	if !supportsGoogleThinking(req.Model) {
		return false
	}
	return normalizeGoogleThinkingLevel(req.Thinking) != ""
}

func buildGoogleInteractionsRequest(req provideriface.Request, modelID string, stream bool) googleInteractionsRequest {
	store := false
	previousInteractionID := strings.TrimSpace(req.PreviousResponseID)
	out := googleInteractionsRequest{
		Model:                 strings.TrimSpace(modelID),
		Input:                 buildGoogleInteractionsInput(req.Input, previousInteractionID != "", req.PreviousResponseFunctionCallIDs),
		PreviousInteractionID: previousInteractionID,
		Stream:                stream,
		Store:                 &store,
	}
	if strings.TrimSpace(req.Instructions) != "" {
		out.SystemInstruction = strings.TrimSpace(req.Instructions)
	}
	if generationConfig := googleInteractionsGenerationConfigForRequest(req); generationConfig != nil {
		out.GenerationConfig = generationConfig
	}
	if tools := buildGoogleInteractionsTools(req.Tools); len(tools) > 0 {
		out.Tools = tools
	}
	return out
}

func googleInteractionsGenerationConfigForRequest(req provideriface.Request) *googleInteractionsGenerationConfig {
	level := normalizeGoogleThinkingLevel(req.Thinking)
	if level == "" {
		return nil
	}
	out := &googleInteractionsGenerationConfig{}
	if level != "off" && level != "xhigh" {
		out.ThinkingLevel = level
	}
	if level != "off" {
		out.ThinkingSummaries = "auto"
	}
	if toolChoice := googleInteractionsToolChoice(req.ToolChoice); toolChoice != "" {
		out.ToolChoice = toolChoice
	}
	if out.ThinkingLevel == "" && out.ThinkingSummaries == "" && out.ToolChoice == "" {
		return nil
	}
	return out
}

func googleInteractionsToolChoice(toolChoice string) string {
	switch strings.ToLower(strings.TrimSpace(toolChoice)) {
	case "auto":
		return "auto"
	case "required", "any":
		return "any"
	case "none":
		return "none"
	default:
		return ""
	}
}

func buildGoogleInteractionsTools(tools []provideriface.ToolDefinition) []googleInteractionsTool {
	out := make([]googleInteractionsTool, 0, len(tools))
	for _, definition := range tools {
		name := strings.TrimSpace(definition.Name)
		if name == "" {
			continue
		}
		out = append(out, googleInteractionsTool{
			Type:        "function",
			Name:        name,
			Description: strings.TrimSpace(definition.Description),
			Parameters:  sanitizeGoogleToolParameters(definition.Parameters),
		})
	}
	return out
}

func buildGoogleRequest(req provideriface.Request) googleRequest {
	out := googleRequest{
		Contents: buildGoogleContents(req.Input),
	}
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
	return out
}

func googleThinkingConfigForRequest(req provideriface.Request) *googleThinkingConfig {
	level := normalizeGoogleThinkingLevel(req.Thinking)
	if level == "" {
		return nil
	}
	if config := googleThinkingConfigFromCatalog(req.ModelCatalog, level); config != nil {
		return googleThinkingConfigWithVisibleThoughts(config, level)
	}
	if !supportsGoogleThinking(req.Model) {
		return nil
	}
	return googleThinkingConfigWithVisibleThoughts(googleLegacyThinkingBudgetConfig(level), level)
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

func googleThinkingConfigWithVisibleThoughts(config *googleThinkingConfig, level string) *googleThinkingConfig {
	if config == nil {
		return nil
	}
	if strings.EqualFold(strings.TrimSpace(level), "off") {
		return config
	}
	config.IncludeThoughts = true
	return config
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

func buildGoogleInteractionsInput(input []map[string]any, statefulContinuation bool, previousResponseFunctionCallIDs []string) any {
	if statefulContinuation {
		return buildGoogleInteractionsStatefulFunctionResults(input, previousResponseFunctionCallIDs)
	}
	steps := make([]map[string]any, 0, len(input))
	pendingThoughtSignature := ""
	for i := 0; i < len(input); i++ {
		item := input[i]
		if typeName, ok := stringField(item, "type"); ok {
			if strings.EqualFold(strings.TrimSpace(typeName), "reasoning") || strings.EqualFold(strings.TrimSpace(typeName), "thought") {
				if signature := extractGoogleThoughtSignature(item); signature != "" {
					pendingThoughtSignature = signature
				}
				continue
			}
			switch strings.ToLower(strings.TrimSpace(typeName)) {
			case "function_call":
				if pendingThoughtSignature != "" {
					steps = append(steps, map[string]any{"type": "thought", "signature": pendingThoughtSignature})
					pendingThoughtSignature = ""
				}
				step := map[string]any{"type": "function_call"}
				if signature := extractGoogleThoughtSignature(item); signature != "" {
					step["signature"] = signature
				}
				if callID := strings.TrimSpace(extractGoogleProviderCallID(item)); callID != "" {
					step["id"] = callID
				}
				if name, _ := stringField(item, "name"); strings.TrimSpace(name) != "" {
					step["name"] = strings.TrimSpace(name)
				}
				if arguments, _ := stringField(item, "arguments"); strings.TrimSpace(arguments) != "" {
					step["arguments"] = parseFunctionArgs(arguments)
				} else {
					step["arguments"] = map[string]any{}
				}
				steps = append(steps, step)
			case "function_call_output":
				step := map[string]any{"type": "function_result"}
				if callID, _ := stringField(item, "call_id"); strings.TrimSpace(callID) != "" {
					step["call_id"] = strings.TrimSpace(callID)
				}
				if name, _ := stringField(item, "name"); strings.TrimSpace(name) != "" {
					step["name"] = strings.TrimSpace(name)
				}
				outputRaw, _ := stringField(item, "output")
				step["result"] = []map[string]any{{"type": "text", "text": strings.TrimSpace(outputRaw)}}
				steps = append(steps, step)
			}
			continue
		}

		role, _ := stringField(item, "role")
		text := extractInputText(item["content"])
		if strings.TrimSpace(text) == "" {
			continue
		}
		stepType := "user_input"
		if strings.EqualFold(strings.TrimSpace(role), "assistant") {
			stepType = "model_output"
		}
		steps = append(steps, map[string]any{
			"type":    stepType,
			"content": []map[string]any{{"type": "text", "text": text}},
		})
	}
	if len(steps) == 1 {
		if content, ok := steps[0]["content"]; ok {
			return content
		}
	}
	return steps
}

func buildGoogleInteractionsStatefulFunctionResults(input []map[string]any, previousResponseFunctionCallIDs []string) any {
	results := make([]map[string]any, 0, len(input))
	pendingCallIDs := make(map[string]struct{}, len(previousResponseFunctionCallIDs))
	for _, callID := range previousResponseFunctionCallIDs {
		if callID = strings.TrimSpace(callID); callID != "" {
			pendingCallIDs[callID] = struct{}{}
		}
	}
	for _, item := range input {
		typeName, ok := stringField(item, "type")
		if !ok || !strings.EqualFold(strings.TrimSpace(typeName), "function_call_output") {
			continue
		}
		callID, _ := stringField(item, "call_id")
		callID = strings.TrimSpace(callID)
		if len(pendingCallIDs) > 0 {
			if _, ok := pendingCallIDs[callID]; !ok {
				continue
			}
		}
		step := map[string]any{"type": "function_result"}
		if callID != "" {
			step["call_id"] = callID
		}
		if name, _ := stringField(item, "name"); strings.TrimSpace(name) != "" {
			step["name"] = strings.TrimSpace(name)
		}
		outputRaw, _ := stringField(item, "output")
		step["result"] = []map[string]any{{"type": "text", "text": strings.TrimSpace(outputRaw)}}
		results = append(results, step)
	}
	return results
}

func buildGoogleContents(input []map[string]any) []googleContent {
	contents := make([]googleContent, 0, len(input))
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
		text := extractInputText(item["content"])
		if strings.TrimSpace(text) == "" {
			continue
		}
		googleRole := "user"
		if strings.EqualFold(strings.TrimSpace(role), "assistant") {
			googleRole = "model"
		}
		contents = append(contents, googleContent{
			Role:  googleRole,
			Parts: []googlePart{{Text: text}},
		})
	}
	return contents
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
	reasoningParts := make([]string, 0, len(candidate.Content.Parts))
	functionCalls := make([]provideriface.FunctionCall, 0, len(candidate.Content.Parts))
	pendingThoughtSignature := ""
	functionCallSequence := 0
	for _, part := range candidate.Content.Parts {
		partThoughtSignature := partThoughtSignatureValue(part)
		if text := strings.TrimSpace(part.Text); text != "" {
			if part.Thought {
				reasoningParts = append(reasoningParts, text)
			} else {
				textParts = append(textParts, text)
			}
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
	out.ReasoningSummary = strings.TrimSpace(strings.Join(reasoningParts, "\n\n"))
	out.FunctionCalls = functionCalls
	return out
}

type googleStreamAccumulator struct {
	modelID                 string
	merged                  googleResponse
	text                    string
	reasoningSummary        string
	functionCalls           []provideriface.FunctionCall
	pendingThoughtSignature string
	toolState               *googleToolCallConstructionState
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
			if part.Thought {
				a.reasoningSummary += part.Text
				if onEvent != nil {
					onEvent(provideriface.StreamEvent{Type: provideriface.StreamEventReasoningSummaryDelta, Delta: a.reasoningSummary, ReasoningKey: googleReasoningKey})
				}
			} else {
				a.text += part.Text
				if onEvent != nil {
					onEvent(provideriface.StreamEvent{Type: provideriface.StreamEventOutputTextDelta, Delta: part.Text})
				}
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
		a.upsertFunctionCall(call)
		a.emitToolCallConstructionEvents(functionCallSequence-1, call, onEvent)
	}
	if strings.TrimSpace(candidate.FinishReason) != "" {
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
	if a.reasoningSummary != "" {
		result.ReasoningSummary = strings.TrimSpace(a.reasoningSummary)
	}
	if len(a.functionCalls) > 0 {
		result.FunctionCalls = a.functionCalls
	}
	return result
}

type googleInteractionsStreamAccumulator struct {
	modelID                  string
	id                       string
	status                   string
	text                     string
	reasoningSummary         string
	thoughtSignatures        []string
	pendingThoughtSignature  string
	functionCalls            []provideriface.FunctionCall
	usage                    provideriface.TokenUsage
	currentFunctionCallIndex map[int]int
	currentFunctionCallArgs  map[int]string
}

func newGoogleInteractionsStreamAccumulator(modelID string) *googleInteractionsStreamAccumulator {
	return &googleInteractionsStreamAccumulator{
		modelID:                  strings.TrimSpace(modelID),
		currentFunctionCallIndex: make(map[int]int),
		currentFunctionCallArgs:  make(map[int]string),
	}
}

func (a *googleInteractionsStreamAccumulator) applyPayload(payload string, onEvent func(provideriface.StreamEvent)) error {
	payload = strings.TrimSpace(payload)
	if payload == "" {
		return nil
	}
	var decoded googleInteractionStreamEvent
	if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
		return fmt.Errorf("decode google interactions stream payload: %w", err)
	}
	switch strings.ToLower(strings.TrimSpace(decoded.EventType)) {
	case "interaction.created", "interaction.completed":
		if decoded.Interaction != nil {
			a.applyInteraction(*decoded.Interaction)
		}
	case "step.start":
		if decoded.Step != nil && strings.EqualFold(strings.TrimSpace(decoded.Step.Type), "function_call") {
			call := buildGoogleInteractionFunctionCall(*decoded.Step, decoded.Index, "")
			if signature := strings.TrimSpace(a.pendingThoughtSignature); signature != "" {
				call.Metadata = mergeGoogleFunctionCallMetadata(call.Metadata, googleFunctionCallMetadata(signature, false))
				a.pendingThoughtSignature = ""
			}
			a.upsertFunctionCall(decoded.Index, call)
			a.emitToolCallStarted(decoded.Index, call, onEvent)
		}
	case "step.delta":
		if decoded.Delta == nil {
			return nil
		}
		switch strings.ToLower(strings.TrimSpace(decoded.Delta.Type)) {
		case "text":
			a.text += decoded.Delta.Text
			if onEvent != nil && decoded.Delta.Text != "" {
				onEvent(provideriface.StreamEvent{Type: provideriface.StreamEventOutputTextDelta, Delta: decoded.Delta.Text})
			}
		case "thought_summary":
			delta := decoded.Delta.Text
			if delta == "" && decoded.Delta.Content != nil && strings.EqualFold(strings.TrimSpace(decoded.Delta.Content.Type), "text") {
				delta = decoded.Delta.Content.Text
			}
			if delta != "" {
				a.reasoningSummary += delta
				if onEvent != nil {
					onEvent(provideriface.StreamEvent{Type: provideriface.StreamEventReasoningSummaryDelta, Delta: a.reasoningSummary, ReasoningKey: googleReasoningKey})
				}
			}
		case "thought_signature":
			if signature := strings.TrimSpace(decoded.Delta.Signature); signature != "" {
				a.thoughtSignatures = append(a.thoughtSignatures, signature)
				a.pendingThoughtSignature = signature
			}
			// Preserve signatures in response metadata/state; do not render opaque
			// signatures as reasoning text.
		case "arguments_delta":
			if decoded.Delta.Arguments != "" {
				a.currentFunctionCallArgs[decoded.Index] += decoded.Delta.Arguments
				if callIndex, ok := a.currentFunctionCallIndex[decoded.Index]; ok && callIndex >= 0 && callIndex < len(a.functionCalls) {
					a.functionCalls[callIndex].Arguments = strings.TrimSpace(a.currentFunctionCallArgs[decoded.Index])
					call := a.functionCalls[callIndex]
					if onEvent != nil {
						onEvent(provideriface.StreamEvent{
							Type:              provideriface.StreamEventToolCallArgumentsSnapshot,
							ToolCallID:        call.CallID,
							ToolCallIndex:     intPointer(decoded.Index),
							ToolName:          call.Name,
							ArgumentsSnapshot: strings.TrimSpace(a.currentFunctionCallArgs[decoded.Index]),
							Metadata:          cloneGoogleMetadataMap(call.Metadata),
						})
					}
				}
			}
		}
	case "step.stop":
		if callIndex, ok := a.currentFunctionCallIndex[decoded.Index]; ok && callIndex >= 0 && callIndex < len(a.functionCalls) {
			call := a.functionCalls[callIndex]
			if strings.TrimSpace(call.Arguments) == "" {
				call.Arguments = strings.TrimSpace(a.currentFunctionCallArgs[decoded.Index])
				a.functionCalls[callIndex] = call
			}
			a.emitToolCallCompleted(decoded.Index, call, onEvent)
		}
	}
	return nil
}

func (a *googleInteractionsStreamAccumulator) applyInteraction(interaction googleInteractionResponse) {
	if strings.TrimSpace(interaction.ID) != "" {
		a.id = strings.TrimSpace(interaction.ID)
	}
	if strings.TrimSpace(interaction.Model) != "" {
		a.modelID = strings.TrimSpace(interaction.Model)
	}
	if strings.TrimSpace(interaction.Status) != "" {
		a.status = strings.TrimSpace(interaction.Status)
	}
	if interaction.Usage != nil {
		a.usage = parseGoogleInteractionUsage(*interaction.Usage)
	}
	for _, step := range interaction.Steps {
		if strings.EqualFold(strings.TrimSpace(step.Type), "thought") {
			if signature := strings.TrimSpace(step.Signature); signature != "" {
				a.thoughtSignatures = append(a.thoughtSignatures, signature)
			}
		}
	}
}

func (a *googleInteractionsStreamAccumulator) upsertFunctionCall(stepIndex int, call provideriface.FunctionCall) {
	if existingIndex, ok := a.currentFunctionCallIndex[stepIndex]; ok && existingIndex >= 0 && existingIndex < len(a.functionCalls) {
		a.functionCalls[existingIndex] = mergeGoogleProviderFunctionCall(a.functionCalls[existingIndex], call)
		return
	}
	a.functionCalls = append(a.functionCalls, call)
	a.currentFunctionCallIndex[stepIndex] = len(a.functionCalls) - 1
}

func (a *googleInteractionsStreamAccumulator) emitToolCallStarted(index int, call provideriface.FunctionCall, onEvent func(provideriface.StreamEvent)) {
	if onEvent == nil {
		return
	}
	onEvent(provideriface.StreamEvent{
		Type:          provideriface.StreamEventToolCallStarted,
		ToolCallID:    call.CallID,
		ToolCallIndex: intPointer(index),
		ToolName:      call.Name,
		Metadata:      cloneGoogleMetadataMap(call.Metadata),
	})
}

func (a *googleInteractionsStreamAccumulator) emitToolCallCompleted(index int, call provideriface.FunctionCall, onEvent func(provideriface.StreamEvent)) {
	if onEvent == nil {
		return
	}
	onEvent(provideriface.StreamEvent{
		Type:          provideriface.StreamEventToolCallCompleted,
		ToolCallID:    call.CallID,
		ToolCallIndex: intPointer(index),
		ToolName:      call.Name,
		Arguments:     strings.TrimSpace(call.Arguments),
		Metadata:      cloneGoogleMetadataMap(call.Metadata),
	})
}

func (a *googleInteractionsStreamAccumulator) response() provideriface.Response {
	out := provideriface.Response{
		ID:               strings.TrimSpace(a.id),
		Model:            strings.TrimSpace(a.modelID),
		StopReason:       strings.TrimSpace(a.status),
		Text:             strings.TrimSpace(a.text),
		ReasoningSummary: strings.TrimSpace(a.reasoningSummary),
		FunctionCalls:    append([]provideriface.FunctionCall(nil), a.functionCalls...),
		Usage:            a.usage,
	}
	if len(a.thoughtSignatures) > 0 {
		out.Raw = map[string]any{"google": map[string]any{"thought_signatures": append([]string(nil), a.thoughtSignatures...)}}
	}
	return out
}

func buildGoogleInteractionFunctionCall(step googleInteractionStep, sequence int, arguments string) provideriface.FunctionCall {
	name := strings.TrimSpace(step.Name)
	if name == "" {
		name = "tool"
	}
	callID := strings.TrimSpace(step.ID)
	if callID == "" {
		callID = fmt.Sprintf("google_interaction_call_%d", sequence)
	}
	arguments = strings.TrimSpace(arguments)
	if arguments == "" {
		if encoded, err := json.Marshal(step.Arguments); err == nil && len(encoded) > 0 && string(encoded) != "null" {
			arguments = string(encoded)
		}
	}
	if arguments == "" {
		arguments = "{}"
	}
	return provideriface.FunctionCall{
		CallID:    callID,
		Name:      name,
		Arguments: arguments,
		Metadata:  googleFunctionCallMetadata(strings.TrimSpace(step.Signature), strings.TrimSpace(step.ID) == ""),
	}
}

func parseGoogleInteractionResponse(resp googleInteractionResponse) provideriface.Response {
	out := provideriface.Response{
		ID:         strings.TrimSpace(resp.ID),
		Model:      strings.TrimSpace(resp.Model),
		StopReason: strings.TrimSpace(resp.Status),
	}
	textParts := make([]string, 0, len(resp.Steps))
	reasoningParts := make([]string, 0, len(resp.Steps))
	functionCalls := make([]provideriface.FunctionCall, 0, len(resp.Steps))
	thoughtSignatures := make([]string, 0, len(resp.Steps))
	for i, step := range resp.Steps {
		switch strings.ToLower(strings.TrimSpace(step.Type)) {
		case "thought":
			if signature := strings.TrimSpace(step.Signature); signature != "" {
				thoughtSignatures = append(thoughtSignatures, signature)
			}
			for _, content := range step.Summary {
				if strings.EqualFold(strings.TrimSpace(content.Type), "text") && strings.TrimSpace(content.Text) != "" {
					reasoningParts = append(reasoningParts, strings.TrimSpace(content.Text))
				}
			}
		case "model_output":
			for _, content := range step.Content {
				if strings.EqualFold(strings.TrimSpace(content.Type), "text") && strings.TrimSpace(content.Text) != "" {
					textParts = append(textParts, strings.TrimSpace(content.Text))
				}
			}
		case "function_call":
			functionCalls = append(functionCalls, buildGoogleInteractionFunctionCall(step, i+1, ""))
		}
	}
	out.Text = strings.TrimSpace(strings.Join(textParts, "\n\n"))
	out.ReasoningSummary = strings.TrimSpace(strings.Join(reasoningParts, "\n\n"))
	out.FunctionCalls = functionCalls
	if len(thoughtSignatures) > 0 {
		out.Raw = map[string]any{"google": map[string]any{"thought_signatures": thoughtSignatures}}
	}
	if resp.Usage != nil {
		out.Usage = parseGoogleInteractionUsage(*resp.Usage)
	}
	return out
}

func parseGoogleInteractionUsage(usage googleInteractionUsage) provideriface.TokenUsage {
	usageRaw := cloneGoogleUsageMap(usage.Raw)
	if len(usageRaw) == 0 {
		usageRaw = map[string]any{
			"total_input_tokens":    usage.TotalInputTokens,
			"total_output_tokens":   usage.TotalOutputTokens,
			"total_thought_tokens":  usage.TotalThoughtTokens,
			"total_tokens":          usage.TotalTokens,
			"total_cached_tokens":   usage.TotalCachedTokens,
			"total_tool_use_tokens": usage.TotalToolUseTokens,
		}
	}
	out := provideriface.TokenUsage{
		InputTokens:      usage.TotalInputTokens,
		OutputTokens:     usage.TotalOutputTokens,
		ThinkingTokens:   usage.TotalThoughtTokens,
		TotalTokens:      usage.TotalTokens,
		CacheReadTokens:  usage.TotalCachedTokens,
		CacheWriteTokens: 0,
		Source:           "google_api_usage",
		APIUsageRaw:      cloneGoogleUsageMap(usageRaw),
		APIUsageRawPath:  "usage",
		APIUsageHistory:  []map[string]any{cloneGoogleUsageMap(usageRaw)},
		APIUsagePaths:    []string{"usage"},
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
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), maxResponseBytes)
	dataLines := make([]string, 0, 8)
	flush := func() error {
		if len(dataLines) == 0 {
			return nil
		}
		payload := strings.Join(dataLines, "\n")
		dataLines = dataLines[:0]
		if strings.TrimSpace(payload) == "[DONE]" {
			return nil
		}
		return onPayload(payload)
	}
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
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

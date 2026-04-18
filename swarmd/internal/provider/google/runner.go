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

	"swarm/packages/swarmd/internal/privacy"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	generateContentURL       = "https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent"
	streamGenerateContentURL = "https://generativelanguage.googleapis.com/v1beta/models/%s:streamGenerateContent"
	googleAPIKeyHeader       = "x-goog-api-key"
	maxResponseBytes         = 8 << 20
)

var googleAPIKeyQueryPattern = regexp.MustCompile(`(?i)([?&]key=)[^&#\s]+`)

type Runner struct {
	authStore  *pebblestore.AuthStore
	httpClient *http.Client
}

type googleAuth struct {
	APIKey      string
	AccessToken string
}

type googleContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []googlePart `json:"parts,omitempty"`
}

type googlePart struct {
	Text                  string                  `json:"text,omitempty"`
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

type googleThinkingConfig struct {
	ThinkingBudget *int `json:"thinkingBudget,omitempty"`
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
	ThoughtsTokenCount      int64 `json:"thoughtsTokenCount,omitempty"`
	TotalTokenCount         int64 `json:"totalTokenCount,omitempty"`
	CachedContentTokenCount int64 `json:"cachedContentTokenCount,omitempty"`
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
	auth, err := r.ensureAuth()
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
	if auth.APIKey != "" {
		httpReq.Header.Set(googleAPIKeyHeader, auth.APIKey)
	} else {
		httpReq.Header.Set("Authorization", "Bearer "+auth.AccessToken)
	}

	resp, err := r.httpClient.Do(httpReq)
	if err != nil {
		return provideriface.Response{}, sanitizeGoogleError("google generateContent request failed", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
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
	auth, err := r.ensureAuth()
	if err != nil {
		return provideriface.Response{}, err
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
	if auth.APIKey != "" {
		httpReq.Header.Set(googleAPIKeyHeader, auth.APIKey)
	} else {
		httpReq.Header.Set("Authorization", "Bearer "+auth.AccessToken)
	}

	resp, err := r.httpClient.Do(httpReq)
	if err != nil {
		return provideriface.Response{}, sanitizeGoogleError("google streamGenerateContent request failed", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= http.StatusBadRequest {
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
		if readErr != nil {
			return provideriface.Response{}, sanitizeGoogleError("read google streamGenerateContent error response", readErr)
		}
		return provideriface.Response{}, googleStatusError("google streamGenerateContent failed", resp.StatusCode, body)
	}

	accumulator := newGoogleStreamAccumulator(modelID)
	if err := parseGoogleEventStream(resp.Body, func(payload string) error {
		return accumulator.applyPayload(payload, onEvent)
	}); err != nil {
		return provideriface.Response{}, sanitizeGoogleError("decode google stream response", err)
	}
	return accumulator.response(), nil
}

func (r *Runner) ensureAuth() (googleAuth, error) {
	record, ok, err := r.authStore.GetActiveCredential("google")
	if err != nil {
		return googleAuth{}, fmt.Errorf("read google auth: %w", err)
	}
	if !ok {
		return googleAuth{}, errors.New("google auth is not configured")
	}
	if apiKey := strings.TrimSpace(record.APIKey); apiKey != "" {
		return googleAuth{APIKey: apiKey}, nil
	}
	if accessToken := strings.TrimSpace(record.AccessToken); accessToken != "" {
		return googleAuth{AccessToken: accessToken}, nil
	}
	return googleAuth{}, errors.New("google auth is incomplete")
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
	if supportsGoogleThinking(req.Model) {
		if thinkingBudget := googleThinkingBudget(req.Thinking); thinkingBudget != nil {
			out.GenerationConfig = &googleGenerationConfig{
				ThinkingConfig: &googleThinkingConfig{
					ThinkingBudget: thinkingBudget,
				},
			}
		}
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

func supportsGoogleThinking(modelID string) bool {
	modelID = strings.ToLower(strings.TrimSpace(modelID))
	if modelID == "" || !strings.Contains(modelID, "gemini") {
		return false
	}
	return strings.Contains(modelID, "gemini-3") ||
		strings.Contains(modelID, "gemini-2.5") ||
		strings.Contains(modelID, "thinking")
}

func googleThinkingBudget(thinking string) *int {
	switch normalizeGoogleThinkingLevel(thinking) {
	case "off":
		return intPointer(0)
	case "low":
		return intPointer(1024)
	case "medium":
		return intPointer(4096)
	case "high":
		return intPointer(8192)
	case "xhigh":
		return intPointer(16384)
	default:
		return nil
	}
}

func normalizeGoogleThinkingLevel(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "off":
		return "off"
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
	modelID       string
	merged        googleResponse
	text          string
	functionCalls []provideriface.FunctionCall
}

func newGoogleStreamAccumulator(modelID string) *googleStreamAccumulator {
	return &googleStreamAccumulator{modelID: strings.TrimSpace(modelID)}
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
	pendingThoughtSignature := ""
	for _, part := range candidate.Content.Parts {
		partThoughtSignature := partThoughtSignatureValue(part)
		if part.Text != "" {
			a.text += part.Text
			if onEvent != nil {
				onEvent(provideriface.StreamEvent{Type: provideriface.StreamEventOutputTextDelta, Delta: part.Text})
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
		a.upsertFunctionCall(buildGoogleFunctionCall(part, functionCallSequence, partThoughtSignature))
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
		"thoughtsTokenCount":      usage.ThoughtsTokenCount,
		"totalTokenCount":         usage.TotalTokenCount,
		"cachedContentTokenCount": usage.CachedContentTokenCount,
	}
	out := provideriface.TokenUsage{
		InputTokens:      usage.PromptTokenCount,
		OutputTokens:     usage.CandidatesTokenCount,
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

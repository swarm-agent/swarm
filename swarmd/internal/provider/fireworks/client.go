package fireworks

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	providerdiagnostics "swarm/packages/swarmd/internal/provider/diagnostics"
)

const (
	accountsURL      = "https://api.fireworks.ai/v1/accounts"
	modelsURL        = "https://api.fireworks.ai/inference/v1/models"
	chatURL          = "https://api.fireworks.ai/inference/v1/chat/completions"
	maxResponseBytes = 8 << 20
)

type Client struct {
	httpClient *http.Client
}

type chatCompletionRequest struct {
	Model             string               `json:"model"`
	Messages          []map[string]any     `json:"messages"`
	Tools             []chatCompletionTool `json:"tools,omitempty"`
	ToolChoice        any                  `json:"tool_choice,omitempty"`
	ParallelToolCalls *bool                `json:"parallel_tool_calls,omitempty"`
	ReasoningEffort   string               `json:"reasoning_effort,omitempty"`
	ServiceTier       string               `json:"service_tier,omitempty"`
	Stream            bool                 `json:"stream,omitempty"`
}

type chatCompletionTool struct {
	Type     string                     `json:"type"`
	Function chatCompletionToolFunction `json:"function"`
}

type chatCompletionToolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

type chatCompletionResponse struct {
	ID      string                 `json:"id"`
	Model   string                 `json:"model"`
	Choices []chatCompletionChoice `json:"choices"`
	Usage   *chatCompletionUsage   `json:"usage,omitempty"`
}

type chatCompletionChoice struct {
	Message      chatCompletionMessage       `json:"message"`
	Delta        *chatCompletionMessageDelta `json:"delta,omitempty"`
	FinishReason string                      `json:"finish_reason"`
	Index        int                         `json:"index,omitempty"`
}

type chatCompletionMessage struct {
	Role             string                   `json:"role,omitempty"`
	Content          any                      `json:"content,omitempty"`
	ReasoningContent string                   `json:"reasoning_content,omitempty"`
	ToolCalls        []chatCompletionToolCall `json:"tool_calls,omitempty"`
}

type chatCompletionMessageDelta struct {
	Role             string                        `json:"role,omitempty"`
	Content          string                        `json:"content,omitempty"`
	ReasoningContent string                        `json:"reasoning_content,omitempty"`
	ToolCalls        []chatCompletionToolCallDelta `json:"tool_calls,omitempty"`
}

type chatCompletionToolCall struct {
	ID       string                         `json:"id,omitempty"`
	Type     string                         `json:"type,omitempty"`
	Function chatCompletionToolFunctionCall `json:"function"`
}

type chatCompletionToolFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type chatCompletionToolCallDelta struct {
	Index    int                              `json:"index,omitempty"`
	ID       string                           `json:"id,omitempty"`
	Type     string                           `json:"type,omitempty"`
	Function *chatCompletionToolFunctionDelta `json:"function,omitempty"`
}

type chatCompletionToolFunctionDelta struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type chatCompletionChunk struct {
	ID      string                 `json:"id,omitempty"`
	Model   string                 `json:"model,omitempty"`
	Choices []chatCompletionChoice `json:"choices,omitempty"`
	Usage   *chatCompletionUsage   `json:"usage,omitempty"`
}

type chatCompletionUsage struct {
	PromptTokens        int64                    `json:"prompt_tokens,omitempty"`
	CompletionTokens    int64                    `json:"completion_tokens,omitempty"`
	TotalTokens         int64                    `json:"total_tokens,omitempty"`
	PromptTokensDetails *chatPromptTokensDetails `json:"prompt_tokens_details,omitempty"`
	InputTokens         int64                    `json:"input_tokens,omitempty"`
	OutputTokens        int64                    `json:"output_tokens,omitempty"`
	InputTokenDetails   *chatPromptTokensDetails `json:"input_tokens_details,omitempty"`
}

type chatPromptTokensDetails struct {
	CachedTokens int64 `json:"cached_tokens,omitempty"`
}

type requestOptions struct {
	SessionAffinity string
}

func requestHeaders(options ...requestOptions) map[string]string {
	headers := make(map[string]string, 1)
	for _, option := range options {
		if affinity := strings.TrimSpace(option.SessionAffinity); affinity != "" {
			headers["x-session-affinity"] = affinity
		}
	}
	return headers
}

func NewClient() *Client {
	return &Client{httpClient: &http.Client{Timeout: 10 * time.Minute}}
}

func (c *Client) VerifyAPIKey(ctx context.Context, apiKey string) (string, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return "", errors.New("fireworks api verification requires api_key")
	}
	body, status, err := c.do(ctx, http.MethodGet, accountsURL, apiKey, nil)
	if err != nil {
		return "", err
	}
	if status >= http.StatusBadRequest {
		return "", fmt.Errorf("fireworks api verification failed status=%d: %s", status, apiErrorMessage(body))
	}
	accountName, displayName := parsePrimaryAccount(body)
	if displayName != "" {
		return fmt.Sprintf("Fireworks API key verified for %s (%s)", displayName, accountName), nil
	}
	if accountName != "" {
		return fmt.Sprintf("Fireworks API key verified for %s", accountName), nil
	}
	return "Fireworks API key verified via /v1/accounts", nil
}

func (c *Client) CreateChatCompletion(ctx context.Context, apiKey string, payload chatCompletionRequest, options ...requestOptions) (chatCompletionResponse, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return chatCompletionResponse{}, errors.New("fireworks auth is not configured")
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return chatCompletionResponse{}, fmt.Errorf("marshal fireworks request: %w", err)
	}
	body, status, err := c.do(ctx, http.MethodPost, chatURL, apiKey, raw, requestHeaders(options...))
	if err != nil {
		return chatCompletionResponse{}, err
	}
	if status >= http.StatusBadRequest {
		return chatCompletionResponse{}, fmt.Errorf("fireworks chat completions failed status=%d: %s", status, apiErrorMessage(body))
	}
	var decoded chatCompletionResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return chatCompletionResponse{}, fmt.Errorf("decode fireworks response: %w", err)
	}
	return decoded, nil
}

func (c *Client) CreateChatCompletionStream(ctx context.Context, apiKey string, payload chatCompletionRequest, onChunk func(chatCompletionChunk) error, options ...requestOptions) (chatCompletionResponse, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return chatCompletionResponse{}, errors.New("fireworks auth is not configured")
	}
	payload.Stream = true
	raw, err := json.Marshal(payload)
	if err != nil {
		return chatCompletionResponse{}, fmt.Errorf("marshal fireworks stream request: %w", err)
	}
	client := c.httpClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Minute}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, chatURL, bytes.NewReader(raw))
	if err != nil {
		return chatCompletionResponse{}, err
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	for key, value := range requestHeaders(options...) {
		req.Header.Set(key, value)
	}
	providerdiagnostics.LogRequest("fireworks", "chat.completions.stream", req, raw)
	resp, err := client.Do(req)
	if err != nil {
		providerdiagnostics.LogErrorContext(ctx, "fireworks", "chat.completions.stream", err)
		return chatCompletionResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusBadRequest {
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
		providerdiagnostics.LogResponse("fireworks", "chat.completions.stream", resp, body)
		if readErr != nil {
			providerdiagnostics.LogErrorContext(ctx, "fireworks", "chat.completions.stream", readErr)
			return chatCompletionResponse{}, readErr
		}
		return chatCompletionResponse{}, fmt.Errorf("fireworks chat completions stream failed status=%d: %s", resp.StatusCode, apiErrorMessage(body))
	}
	state := newFireworksStreamState()
	providerdiagnostics.LogResponse("fireworks", "chat.completions.stream", resp, nil)
	if err := parseFireworksEventStream(resp.Body, func(payload string) error {
		providerdiagnostics.LogStreamChunkContext(ctx, "fireworks", "chat.completions.stream", []byte(payload))
		var chunk chatCompletionChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			return fmt.Errorf("decode fireworks stream chunk: %w", err)
		}
		state.apply(chunk)
		if onChunk != nil {
			return onChunk(chunk)
		}
		return nil
	}); err != nil {
		providerdiagnostics.LogErrorContext(ctx, "fireworks", "chat.completions.stream", err)
		return chatCompletionResponse{}, err
	}
	return state.response(), nil
}

func (c *Client) do(ctx context.Context, method, url, apiKey string, body []byte, extraHeaders ...map[string]string) ([]byte, int, error) {
	client := c.httpClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Minute}
	}
	var reader io.Reader
	if len(body) > 0 {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(apiKey))
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, headers := range extraHeaders {
		for key, value := range headers {
			req.Header.Set(key, value)
		}
	}
	operation := "api"
	if strings.EqualFold(method, http.MethodPost) && strings.Contains(url, "/chat/completions") {
		operation = "chat.completions"
	} else if strings.EqualFold(method, http.MethodGet) && strings.Contains(url, "/accounts") {
		operation = "verify.accounts"
	}
	providerdiagnostics.LogRequest("fireworks", operation, req, body)
	resp, err := client.Do(req)
	if err != nil {
		providerdiagnostics.LogErrorContext(ctx, "fireworks", operation, err)
		return nil, 0, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	providerdiagnostics.LogResponse("fireworks", operation, resp, raw)
	if err != nil {
		providerdiagnostics.LogErrorContext(ctx, "fireworks", operation, err)
		return nil, resp.StatusCode, err
	}
	return raw, resp.StatusCode, nil
}

type fireworksStreamState struct {
	merged    chatCompletionResponse
	toolCalls map[int]*chatCompletionToolCall
}

func newFireworksStreamState() *fireworksStreamState {
	return &fireworksStreamState{toolCalls: make(map[int]*chatCompletionToolCall)}
}

func (s *fireworksStreamState) apply(chunk chatCompletionChunk) {
	if s == nil {
		return
	}
	if strings.TrimSpace(chunk.ID) != "" {
		s.merged.ID = chunk.ID
	}
	if strings.TrimSpace(chunk.Model) != "" {
		s.merged.Model = chunk.Model
	}
	if chunk.Usage != nil {
		s.merged.Usage = chunk.Usage
	}
	if len(chunk.Choices) == 0 {
		return
	}
	if len(s.merged.Choices) == 0 {
		s.merged.Choices = []chatCompletionChoice{{}}
	}
	choice := &s.merged.Choices[0]
	for _, next := range chunk.Choices {
		if next.Delta != nil {
			if strings.TrimSpace(next.Delta.Role) != "" {
				choice.Message.Role = next.Delta.Role
			}
			if next.Delta.Content != "" {
				current, _ := choice.Message.Content.(string)
				choice.Message.Content = current + next.Delta.Content
			}
			if next.Delta.ReasoningContent != "" {
				choice.Message.ReasoningContent += next.Delta.ReasoningContent
			}
			for _, delta := range next.Delta.ToolCalls {
				call := s.toolCalls[delta.Index]
				if call == nil {
					call = &chatCompletionToolCall{}
					s.toolCalls[delta.Index] = call
				}
				if strings.TrimSpace(delta.ID) != "" {
					call.ID = delta.ID
				}
				if strings.TrimSpace(delta.Type) != "" {
					call.Type = delta.Type
				}
				if delta.Function != nil {
					if delta.Function.Name != "" {
						call.Function.Name += delta.Function.Name
					}
					if delta.Function.Arguments != "" {
						call.Function.Arguments += delta.Function.Arguments
					}
				}
			}
		}
		if strings.TrimSpace(next.FinishReason) != "" {
			choice.FinishReason = next.FinishReason
		}
	}
	if len(s.toolCalls) > 0 {
		maxIndex := -1
		for index := range s.toolCalls {
			if index > maxIndex {
				maxIndex = index
			}
		}
		calls := make([]chatCompletionToolCall, 0, maxIndex+1)
		for i := 0; i <= maxIndex; i++ {
			call, ok := s.toolCalls[i]
			if !ok || call == nil {
				continue
			}
			calls = append(calls, *call)
		}
		choice.Message.ToolCalls = calls
	}
}

func (s *fireworksStreamState) response() chatCompletionResponse {
	if s == nil {
		return chatCompletionResponse{}
	}
	return s.merged
}

func parseFireworksEventStream(reader io.Reader, onPayload func(string) error) error {
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
			dataLines = append(dataLines, strings.TrimLeft(line[len("data:"):], " 	"))
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return flush()
}

func apiErrorMessage(raw []byte) string {
	message := strings.TrimSpace(string(raw))
	if message == "" {
		return "unknown error"
	}
	var payload struct {
		Error struct {
			Message string `json:"message"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &payload); err == nil {
		if msg := strings.TrimSpace(payload.Error.Message); msg != "" {
			return msg
		}
		if code := strings.TrimSpace(payload.Error.Code); code != "" {
			return code
		}
	}
	return message
}

func parsePrimaryAccount(raw []byte) (string, string) {
	var payload struct {
		Accounts []struct {
			Name        string `json:"name"`
			DisplayName string `json:"displayName"`
		} `json:"accounts"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return "", ""
	}
	if len(payload.Accounts) == 0 {
		return "", ""
	}
	return strings.TrimSpace(payload.Accounts[0].Name), strings.TrimSpace(payload.Accounts[0].DisplayName)
}

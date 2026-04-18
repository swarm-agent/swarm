package openrouter

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
)

const (
	chatURL          = "https://openrouter.ai/api/v1/chat/completions"
	keyURL           = "https://openrouter.ai/api/v1/key"
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
	Error   *streamErrorPayload    `json:"error,omitempty"`
}

type chatCompletionChoice struct {
	Message      chatCompletionMessage       `json:"message"`
	Delta        *chatCompletionMessageDelta `json:"delta,omitempty"`
	FinishReason string                      `json:"finish_reason"`
	Index        int                         `json:"index,omitempty"`
}

type chatCompletionMessage struct {
	Role      string                   `json:"role,omitempty"`
	Content   any                      `json:"content,omitempty"`
	ToolCalls []chatCompletionToolCall `json:"tool_calls,omitempty"`
}

type chatCompletionMessageDelta struct {
	Role      string                        `json:"role,omitempty"`
	Content   string                        `json:"content,omitempty"`
	ToolCalls []chatCompletionToolCallDelta `json:"tool_calls,omitempty"`
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
	Error   *streamErrorPayload    `json:"error,omitempty"`
}

type chatCompletionUsage struct {
	PromptTokens     int64 `json:"prompt_tokens,omitempty"`
	CompletionTokens int64 `json:"completion_tokens,omitempty"`
	TotalTokens      int64 `json:"total_tokens,omitempty"`
}

type streamErrorPayload struct {
	Code    any    `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

func NewClient() *Client {
	return &Client{httpClient: &http.Client{Timeout: 10 * time.Minute}}
}

func (c *Client) VerifyAPIKey(ctx context.Context, apiKey string) (string, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return "", errors.New("openrouter api verification requires api_key")
	}
	body, status, err := c.do(ctx, http.MethodGet, keyURL, apiKey, nil)
	if err != nil {
		return "", err
	}
	if status >= http.StatusBadRequest {
		return "", fmt.Errorf("openrouter api verification failed status=%d: %s", status, apiErrorMessage(body))
	}
	label, limitRemaining, err := parseCurrentKey(body)
	if err != nil {
		return "", err
	}
	if label != "" && limitRemaining != "" {
		return fmt.Sprintf("OpenRouter API key verified (%s, remaining limit %s)", label, limitRemaining), nil
	}
	if label != "" {
		return fmt.Sprintf("OpenRouter API key verified (%s)", label), nil
	}
	return "OpenRouter API key verified via /key", nil
}

func (c *Client) CreateChatCompletion(ctx context.Context, apiKey string, payload chatCompletionRequest) (chatCompletionResponse, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return chatCompletionResponse{}, errors.New("openrouter auth is not configured")
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return chatCompletionResponse{}, fmt.Errorf("marshal openrouter request: %w", err)
	}
	body, status, err := c.do(ctx, http.MethodPost, chatURL, apiKey, raw)
	if err != nil {
		return chatCompletionResponse{}, err
	}
	if status >= http.StatusBadRequest {
		return chatCompletionResponse{}, fmt.Errorf("openrouter chat completions failed status=%d: %s", status, apiErrorMessage(body))
	}
	var decoded chatCompletionResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return chatCompletionResponse{}, fmt.Errorf("decode openrouter response: %w", err)
	}
	if decoded.Error != nil && strings.TrimSpace(decoded.Error.Message) != "" {
		return chatCompletionResponse{}, errors.New(strings.TrimSpace(decoded.Error.Message))
	}
	return decoded, nil
}

func (c *Client) CreateChatCompletionStream(ctx context.Context, apiKey string, payload chatCompletionRequest, onChunk func(chatCompletionChunk) error) (chatCompletionResponse, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return chatCompletionResponse{}, errors.New("openrouter auth is not configured")
	}
	payload.Stream = true
	raw, err := json.Marshal(payload)
	if err != nil {
		return chatCompletionResponse{}, fmt.Errorf("marshal openrouter stream request: %w", err)
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
	resp, err := client.Do(req)
	if err != nil {
		return chatCompletionResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusBadRequest {
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
		if readErr != nil {
			return chatCompletionResponse{}, readErr
		}
		return chatCompletionResponse{}, fmt.Errorf("openrouter chat completions stream failed status=%d: %s", resp.StatusCode, apiErrorMessage(body))
	}
	state := newOpenRouterStreamState()
	if err := parseOpenRouterEventStream(resp.Body, func(payload string) error {
		var chunk chatCompletionChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			return fmt.Errorf("decode openrouter stream chunk: %w", err)
		}
		state.apply(chunk)
		if chunk.Error != nil && strings.TrimSpace(chunk.Error.Message) != "" {
			return errors.New(strings.TrimSpace(chunk.Error.Message))
		}
		if onChunk != nil {
			return onChunk(chunk)
		}
		return nil
	}); err != nil {
		return chatCompletionResponse{}, err
	}
	response := state.response()
	if response.Error != nil && strings.TrimSpace(response.Error.Message) != "" {
		return chatCompletionResponse{}, errors.New(strings.TrimSpace(response.Error.Message))
	}
	return response, nil
}

func (c *Client) do(ctx context.Context, method, url, apiKey string, body []byte) ([]byte, int, error) {
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
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return raw, resp.StatusCode, nil
}

type openRouterStreamState struct {
	merged    chatCompletionResponse
	toolCalls map[int]*chatCompletionToolCall
}

func newOpenRouterStreamState() *openRouterStreamState {
	return &openRouterStreamState{toolCalls: make(map[int]*chatCompletionToolCall)}
}

func (s *openRouterStreamState) apply(chunk chatCompletionChunk) {
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
	if chunk.Error != nil {
		s.merged.Error = chunk.Error
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

func (s *openRouterStreamState) response() chatCompletionResponse {
	if s == nil {
		return chatCompletionResponse{}
	}
	return s.merged
}

func parseOpenRouterEventStream(reader io.Reader, onPayload func(string) error) error {
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
			Code    any    `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &payload); err == nil {
		if msg := strings.TrimSpace(payload.Error.Message); msg != "" {
			return msg
		}
		if payload.Error.Code != nil {
			code := strings.TrimSpace(fmt.Sprintf("%v", payload.Error.Code))
			if code != "" {
				return code
			}
		}
	}
	return message
}

func parseCurrentKey(raw []byte) (label string, limitRemaining string, err error) {
	var payload struct {
		Data struct {
			Label          string  `json:"label"`
			LimitRemaining float64 `json:"limit_remaining"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return "", "", fmt.Errorf("decode openrouter key response: %w", err)
	}
	label = strings.TrimSpace(payload.Data.Label)
	if payload.Data.LimitRemaining != 0 {
		limitRemaining = fmt.Sprintf("%.2f", payload.Data.LimitRemaining)
	}
	return label, limitRemaining, nil
}

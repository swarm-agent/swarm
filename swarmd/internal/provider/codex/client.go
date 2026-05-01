package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"swarm/packages/swarmd/internal/privacy"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	responsesURL                               = "https://chatgpt.com/backend-api/codex/responses"
	openAIBetaHeader                           = "OpenAI-Beta"
	responsesWebsocketBetaHeaderV2             = "responses_websockets=2026-02-06"
	originatorHeader                           = "originator"
	defaultOriginatorHeaderValue               = "codex_cli_rs"
	userAgentHeader                            = "User-Agent"
	defaultCodexTransportUserAgent             = "codex_cli_rs/swarm-go"
	defaultCodexTextVerbosity                  = "low"
	includeReasoningEncryptedContentPath       = "reasoning.encrypted_content"
	chatGPTAccountIDHeader                     = "ChatGPT-Account-ID"
	tokenURL                                   = "https://auth.openai.com/oauth/token"
	clientID                                   = "app_EMoamEEZ73f0CkXaXp7hrann"
	maxCodexResponseBodyBytes            int64 = 32 << 20
	transportRetryAttempts                     = 2
	transportRetryBaseDelay                    = 300 * time.Millisecond
	startedWebsocketStreamRetryLimit           = 3
	codexTransportMetadataKey                  = "_swarm_transport"
	codexConnectedViaWSMetadataKey             = "_swarm_connected_via_websocket"
	codexTransportWebsocket                    = "websocket"
)

var (
	errWebsocketStreamStarted = errors.New("websocket stream interrupted after payload started")
	errWebsocketRetryFresh    = errors.New("websocket request requires a fresh connection")
)

type Client struct {
	authStore   *pebblestore.AuthStore
	httpClient  *http.Client
	earlyExpiry time.Duration
	sendWSFn    func(context.Context, pebblestore.CodexAuthRecord, []byte, func(StreamEvent)) (map[string]any, int, error)
	wsMu        sync.Mutex
	wsSessions  map[string]*cachedWebsocketSession
}

type startedWebsocketStreamError struct {
	cause error
}

type cachedWebsocketSession struct {
	mu             sync.Mutex
	conn           *websocket.Conn
	lastPayload    map[string]any
	lastResponseID string
	lastOutput     []any
}

func (e *startedWebsocketStreamError) Error() string {
	if e == nil || e.cause == nil {
		return errWebsocketStreamStarted.Error()
	}
	return fmt.Sprintf("%s: %v", errWebsocketStreamStarted, e.cause)
}

func (e *startedWebsocketStreamError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func (e *startedWebsocketStreamError) Is(target error) bool {
	return target == errWebsocketStreamStarted
}

func newStartedWebsocketStreamError(cause error) error {
	if cause == nil {
		return errWebsocketStreamStarted
	}
	return &startedWebsocketStreamError{cause: cause}
}

type retryAwareStreamEmitter struct {
	onEvent                 func(StreamEvent)
	emittedOutputText       string
	emittedReasoningSummary map[string]string
	attemptOutputText       string
	attemptReasoningSummary map[string]string
}

func (e *retryAwareStreamEmitter) beginAttempt() {
	if e == nil {
		return
	}
	e.attemptOutputText = ""
	e.attemptReasoningSummary = make(map[string]string, 4)
}

func (e *retryAwareStreamEmitter) emit(event StreamEvent) {
	if e == nil || e.onEvent == nil {
		return
	}
	switch event.Type {
	case StreamEventOutputTextDelta:
		e.attemptOutputText, _ = mergeStreamDelta(e.attemptOutputText, event.Delta)
		next, appended := mergeRetriedStreamText(e.emittedOutputText, e.attemptOutputText)
		e.emittedOutputText = next
		if appended != "" {
			e.onEvent(StreamEvent{Type: StreamEventOutputTextDelta, Delta: appended, Phase: event.Phase})
		}
	case StreamEventReasoningSummaryDelta:
		key := reasoningStreamStateKey(event.ReasoningKey)
		if e.attemptReasoningSummary == nil {
			e.attemptReasoningSummary = make(map[string]string, 4)
		}
		if e.emittedReasoningSummary == nil {
			e.emittedReasoningSummary = make(map[string]string, 4)
		}
		e.attemptReasoningSummary[key] = event.Delta
		next, snapshot, changed := mergeRetriedReasoningSummary(e.emittedReasoningSummary[key], e.attemptReasoningSummary[key])
		e.emittedReasoningSummary[key] = next
		if changed {
			e.onEvent(StreamEvent{Type: StreamEventReasoningSummaryDelta, Delta: snapshot, ReasoningKey: event.ReasoningKey})
		}
	default:
		e.onEvent(event)
	}
}

type ToolDefinition struct {
	Type        string         `json:"type"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters"`
	Strict      bool           `json:"strict"`
}

type Request struct {
	SessionID         string
	Model             string
	Thinking          string
	Instructions      string
	Input             []map[string]any
	Tools             []ToolDefinition
	ToolChoice        string
	ServiceTier       string
	ContextMode       string
	ContextWindow     int
	ParallelToolCalls bool
}

type FunctionCall struct {
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type TokenUsage struct {
	InputTokens      int64            `json:"input_tokens,omitempty"`
	OutputTokens     int64            `json:"output_tokens,omitempty"`
	ThinkingTokens   int64            `json:"thinking_tokens,omitempty"`
	TotalTokens      int64            `json:"total_tokens,omitempty"`
	CacheReadTokens  int64            `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int64            `json:"cache_write_tokens,omitempty"`
	Source           string           `json:"source,omitempty"`
	Transport        string           `json:"transport,omitempty"`
	ConnectedViaWS   *bool            `json:"connected_via_websocket,omitempty"`
	APIUsageRaw      map[string]any   `json:"api_usage_raw,omitempty"`
	APIUsageRawPath  string           `json:"api_usage_raw_path,omitempty"`
	APIUsageHistory  []map[string]any `json:"api_usage_history,omitempty"`
	APIUsagePaths    []string         `json:"api_usage_paths,omitempty"`
}

type Response struct {
	ID               string             `json:"id,omitempty"`
	Model            string             `json:"model,omitempty"`
	StopReason       string             `json:"stop_reason,omitempty"`
	Text             string             `json:"text,omitempty"`
	ReasoningSummary string             `json:"reasoning_summary,omitempty"`
	Messages         []AssistantMessage `json:"messages,omitempty"`
	FunctionCalls    []FunctionCall     `json:"function_calls,omitempty"`
	Usage            TokenUsage         `json:"usage,omitempty"`
}

type AssistantMessage struct {
	Text  string
	Phase provideriface.AssistantPhase
}

type StreamEventType string

const (
	StreamEventOutputTextDelta       StreamEventType = "response.output_text.delta"
	StreamEventReasoningSummaryDelta StreamEventType = "response.reasoning_summary_text.delta"
	StreamEventAssistantCommentary   StreamEventType = "response.assistant_commentary.delta"
)

type StreamEvent struct {
	Type         StreamEventType
	Delta        string
	Phase        provideriface.AssistantPhase
	ReasoningKey string
}

type tokenRefresh struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
}

func NewClient(authStore *pebblestore.AuthStore) *Client {
	return &Client{
		authStore: authStore,
		// Long-running response streams must be governed by per-request contexts
		// (run service deadlines), not a global client timeout.
		httpClient:  &http.Client{},
		earlyExpiry: 5 * time.Minute,
		wsSessions:  make(map[string]*cachedWebsocketSession),
	}
}

func (c *Client) cachedWebsocketSession(sessionID string) *cachedWebsocketSession {
	if c == nil {
		return nil
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	c.wsMu.Lock()
	defer c.wsMu.Unlock()
	if existing, ok := c.wsSessions[sessionID]; ok && existing != nil {
		return existing
	}
	session := &cachedWebsocketSession{}
	c.wsSessions[sessionID] = session
	return session
}

func closeCachedWebsocketSessionLocked(session *cachedWebsocketSession) {
	if session == nil {
		return
	}
	if session.conn != nil {
		_ = session.conn.Close()
		session.conn = nil
	}
	session.lastPayload = nil
	session.lastResponseID = ""
	session.lastOutput = nil
}

func contextErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

func watchCodexWebsocketCancel(ctx context.Context, activeConn *websocket.Conn) func() {
	if ctx == nil {
		return func() {}
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			if activeConn != nil {
				_ = activeConn.Close()
			}
		case <-done:
		}
	}()
	return func() {
		close(done)
	}
}

func (c *Client) CreateResponse(ctx context.Context, req Request) (Response, error) {
	return c.createResponse(ctx, req, nil)
}

func (c *Client) CreateResponseStreaming(ctx context.Context, req Request, onEvent func(StreamEvent)) (Response, error) {
	return c.createResponse(ctx, req, onEvent)
}

func (c *Client) createResponse(ctx context.Context, req Request, onEvent func(StreamEvent)) (Response, error) {
	record, err := c.ensureAuth(ctx)
	if err != nil {
		return Response{}, err
	}

	payload, err := buildRequestPayload(req)
	if err != nil {
		return Response{}, err
	}

	decoded, statusCode, err := c.send(ctx, record, payload, onEvent)
	if err != nil {
		return Response{}, err
	}
	if statusCode == http.StatusUnauthorized && record.Type == pebblestore.CodexAuthTypeOAuth {
		refreshed, refreshErr := c.refreshOAuth(ctx, record.RefreshToken)
		if refreshErr != nil {
			return Response{}, fmt.Errorf("codex request unauthorized and refresh failed: %w", refreshErr)
		}
		accountID := extractAccountIDFromToken(refreshed.AccessToken)
		record, err = c.authStore.UpdateOAuthCredential(record.Provider, record.ID, refreshed.AccessToken, refreshed.RefreshToken, refreshed.ExpiresAt, accountID)
		if err != nil {
			return Response{}, fmt.Errorf("persist refreshed codex oauth: %w", err)
		}
		decoded, statusCode, err = c.send(ctx, record, payload, onEvent)
		if err != nil {
			return Response{}, err
		}
	}

	if statusCode >= 400 {
		if transport, _ := extractCodexTransportMetadata(decoded); transport != "" {
			return Response{}, fmt.Errorf("codex responses request failed status=%d transport=%s body=%s", statusCode, transport, compactBody(decoded))
		}
		return Response{}, fmt.Errorf("codex responses request failed status=%d body=%s", statusCode, compactBody(decoded))
	}

	return parseResponse(decoded), nil
}

func (c *Client) ensureAuth(ctx context.Context) (pebblestore.CodexAuthRecord, error) {
	record, ok, err := c.authStore.GetCodexAuthRecord()
	if err != nil {
		return pebblestore.CodexAuthRecord{}, fmt.Errorf("read codex auth: %w", err)
	}
	if !ok {
		return pebblestore.CodexAuthRecord{}, errors.New("codex auth not configured")
	}

	switch record.Type {
	case pebblestore.CodexAuthTypeOAuth:
		if strings.TrimSpace(record.AccessToken) == "" || strings.TrimSpace(record.RefreshToken) == "" {
			return pebblestore.CodexAuthRecord{}, errors.New("codex oauth record is incomplete")
		}
		now := time.Now().Add(c.earlyExpiry).UnixMilli()
		if record.ExpiresAt > 0 && record.ExpiresAt <= now {
			refreshed, err := c.refreshOAuth(ctx, record.RefreshToken)
			if err != nil {
				return pebblestore.CodexAuthRecord{}, err
			}
			accountID := extractAccountIDFromToken(refreshed.AccessToken)
			record, err = c.authStore.UpdateOAuthCredential(record.Provider, record.ID, refreshed.AccessToken, refreshed.RefreshToken, refreshed.ExpiresAt, accountID)
			if err != nil {
				return pebblestore.CodexAuthRecord{}, fmt.Errorf("persist refreshed codex oauth: %w", err)
			}
		}
	default:
		if strings.TrimSpace(record.APIKey) == "" {
			return pebblestore.CodexAuthRecord{}, errors.New("codex api key is not configured")
		}
	}

	if strings.TrimSpace(record.AccountID) == "" && record.Type == pebblestore.CodexAuthTypeOAuth {
		accountID := extractAccountIDFromToken(record.AccessToken)
		if accountID != "" {
			updated, err := c.authStore.UpdateOAuthCredential(record.Provider, record.ID, record.AccessToken, record.RefreshToken, record.ExpiresAt, accountID)
			if err == nil {
				record = updated
			}
		}
	}

	return record, nil
}

func (c *Client) refreshOAuth(ctx context.Context, refreshToken string) (oauthTokens, error) {
	refreshToken = strings.TrimSpace(refreshToken)
	if refreshToken == "" {
		return oauthTokens{}, errors.New("codex refresh token is missing")
	}
	values := url.Values{}
	values.Set("grant_type", "refresh_token")
	values.Set("client_id", clientID)
	values.Set("refresh_token", refreshToken)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(values.Encode()))
	if err != nil {
		return oauthTokens{}, err
	}
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return oauthTokens{}, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return oauthTokens{}, err
	}
	if resp.StatusCode >= 400 {
		return oauthTokens{}, fmt.Errorf("oauth refresh failed status=%d body=%s", resp.StatusCode, sanitizeDiagnosticText(string(body)))
	}

	var decoded tokenRefresh
	if err := json.Unmarshal(body, &decoded); err != nil {
		return oauthTokens{}, fmt.Errorf("decode oauth refresh response: %w", err)
	}
	if strings.TrimSpace(decoded.AccessToken) == "" {
		return oauthTokens{}, errors.New("oauth refresh response missing access_token")
	}
	refreshOut := strings.TrimSpace(decoded.RefreshToken)
	if refreshOut == "" {
		refreshOut = refreshToken
	}
	expiresAt := time.Now().Add(time.Duration(decoded.ExpiresIn) * time.Second).Add(-c.earlyExpiry).UnixMilli()
	return oauthTokens{
		AccessToken:  decoded.AccessToken,
		RefreshToken: refreshOut,
		ExpiresAt:    expiresAt,
	}, nil
}

type oauthTokens struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    int64
}

func buildRequestPayload(req Request) ([]byte, error) {
	modelID := strings.TrimSpace(req.Model)
	if modelID == "" {
		return nil, errors.New("model is required")
	}
	if len(req.Input) == 0 {
		return nil, errors.New("input messages are required")
	}

	body := map[string]any{
		"model":  modelID,
		"stream": true,
		"store":  false,
		"input":  req.Input,
		"text": map[string]any{
			"verbosity": defaultCodexTextVerbosity,
		},
	}
	if sessionID := strings.TrimSpace(req.SessionID); sessionID != "" {
		body["prompt_cache_key"] = sessionID
	}
	if strings.TrimSpace(req.Instructions) != "" {
		body["instructions"] = strings.TrimSpace(req.Instructions)
	}
	if len(req.Tools) > 0 {
		body["tools"] = normalizeCodexRequestTools(req.Tools)
		toolChoice := strings.TrimSpace(req.ToolChoice)
		if toolChoice == "" {
			toolChoice = "auto"
		}
		body["tool_choice"] = toolChoice
		body["parallel_tool_calls"] = req.ParallelToolCalls
	}
	switch NormalizeServiceTier(req.ServiceTier) {
	case ServiceTierFast:
		body["service_tier"] = "priority"
	case ServiceTierFlex:
		body["service_tier"] = ServiceTierFlex
	}
	if reasoning := reasoningPayload(req.Thinking); len(reasoning) > 0 {
		body["reasoning"] = reasoning
		body["include"] = []string{includeReasoningEncryptedContentPath}
	}
	return json.Marshal(body)
}

func normalizeCodexRequestTools(tools []ToolDefinition) []ToolDefinition {
	out := make([]ToolDefinition, 0, len(tools))
	for _, tool := range tools {
		tool.Parameters = sanitizeCodexToolParameters(tool.Parameters)
		out = append(out, tool)
	}
	return out
}

func reasoningPayload(thinking string) map[string]any {
	thinking = strings.ToLower(strings.TrimSpace(thinking))
	switch thinking {
	case "", "off":
		return nil
	case "low":
		return map[string]any{"effort": "low", "summary": "auto"}
	case "medium":
		return map[string]any{"effort": "medium", "summary": "auto"}
	case "high":
		return map[string]any{"effort": "high", "summary": "auto"}
	case "xhigh":
		return map[string]any{"effort": "xhigh", "summary": "auto"}
	default:
		return map[string]any{"effort": "medium", "summary": "auto"}
	}
}

func (c *Client) send(ctx context.Context, record pebblestore.CodexAuthRecord, payload []byte, onEvent func(StreamEvent)) (map[string]any, int, error) {
	sendWS := c.sendWSFn
	if sendWS == nil {
		sendWS = c.sendWebsocket
	}

	streamEmitter := &retryAwareStreamEmitter{onEvent: onEvent}

	for attempt := 1; attempt <= transportRetryAttempts; attempt++ {
		var wsDecoded map[string]any
		var wsStatus int
		var wsErr error
		for startedRetry := 0; ; startedRetry++ {
			streamEmitter.beginAttempt()
			wsDecoded, wsStatus, wsErr = sendWS(ctx, record, payload, streamEmitter.emit)
			if wsErr == nil || !errors.Is(wsErr, errWebsocketStreamStarted) {
				break
			}
			if !shouldRetryStartedWebsocketStream(wsErr) || startedRetry >= startedWebsocketStreamRetryLimit {
				return nil, 0, wsErr
			}
			if err := sleepWithContext(ctx, transportRetryBaseDelay*time.Duration(startedRetry+1)); err != nil {
				return nil, 0, err
			}
		}
		if wsErr != nil {
			if ctxErr := contextErr(ctx); ctxErr != nil {
				return nil, 0, ctxErr
			}
			if shouldRetryWebsocketTransportError(wsErr) && attempt < transportRetryAttempts {
				if err := sleepWithContext(ctx, transportRetryBaseDelay*time.Duration(attempt)); err != nil {
					return nil, 0, err
				}
				continue
			}
			return nil, 0, wsErr
		}
		if ctxErr := contextErr(ctx); ctxErr != nil {
			return nil, 0, ctxErr
		}
		if shouldRetryTransportStatus(wsStatus, wsDecoded) && attempt < transportRetryAttempts {
			if err := sleepWithContext(ctx, transportRetryBaseDelay*time.Duration(attempt)); err != nil {
				return nil, 0, err
			}
			continue
		}
		return annotateRetryAttempts(annotateCodexTransportMetadata(wsDecoded, codexTransportWebsocket, true), attempt), wsStatus, nil
	}
	return map[string]any{
		"raw_body":       "",
		"retry_attempts": transportRetryAttempts,
	}, http.StatusServiceUnavailable, nil
}

func shouldRetryTransportStatus(statusCode int, decoded map[string]any) bool {
	switch statusCode {
	case http.StatusForbidden:
		if len(decoded) == 0 {
			return true
		}
		return strings.TrimSpace(asString(decoded["raw_body"])) == ""
	default:
		return statusCode >= http.StatusInternalServerError && statusCode <= 599
	}
}

func shouldRetryWebsocketTransportError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, errWebsocketRetryFresh) {
		return true
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	return !errors.Is(err, errWebsocketStreamStarted)
}

func annotateRetryAttempts(decoded map[string]any, attempts int) map[string]any {
	if attempts <= 1 {
		return decoded
	}
	if decoded == nil {
		decoded = map[string]any{}
	}
	decoded["retry_attempts"] = attempts
	return decoded
}

func annotateCodexTransportMetadata(decoded map[string]any, transport string, connectedViaWebsocket bool) map[string]any {
	transport = strings.ToLower(strings.TrimSpace(transport))
	if transport == "" {
		return decoded
	}
	if decoded == nil {
		decoded = map[string]any{}
	}
	decoded[codexTransportMetadataKey] = transport
	decoded[codexConnectedViaWSMetadataKey] = connectedViaWebsocket
	return decoded
}

func sleepWithContext(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	if ctx == nil {
		<-timer.C
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func shouldRetryStartedWebsocketStream(err error) bool {
	if !errors.Is(err, errWebsocketStreamStarted) {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var closeErr *websocket.CloseError
	if errors.As(err, &closeErr) {
		return closeErr.Code == websocket.CloseAbnormalClosure
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "close 1006") || strings.Contains(message, "unexpected eof")
}

func mergeRetriedStreamText(current, attempt string) (string, string) {
	if attempt == "" {
		return current, ""
	}
	if current == "" {
		return attempt, attempt
	}
	if strings.HasPrefix(attempt, current) {
		return attempt, attempt[len(current):]
	}
	if strings.HasPrefix(current, attempt) {
		return current, ""
	}
	return current, ""
}

func mergeRetriedReasoningSummary(current, attempt string) (string, string, bool) {
	merged := mergeReasoningSummarySnapshot(current, attempt)
	if normalizeReasoningSummary(merged) == normalizeReasoningSummary(current) {
		return current, "", false
	}
	return merged, merged, true
}

func responsesWebsocketURL() (string, error) {
	parsed, err := url.Parse(responsesURL)
	if err != nil {
		return "", fmt.Errorf("parse responses url: %w", err)
	}
	switch parsed.Scheme {
	case "http":
		parsed.Scheme = "ws"
	case "https":
		parsed.Scheme = "wss"
	case "ws", "wss":
		// already websocket.
	default:
		return "", fmt.Errorf("unsupported responses url scheme %q", parsed.Scheme)
	}
	return parsed.String(), nil
}

func (c *Client) sendWebsocket(ctx context.Context, record pebblestore.CodexAuthRecord, payload []byte, onEvent func(StreamEvent)) (map[string]any, int, error) {
	wsURL, err := responsesWebsocketURL()
	if err != nil {
		return nil, 0, err
	}

	requestPayload, err := decodeCodexPayload(payload)
	if err != nil {
		return nil, 0, err
	}
	sessionID := extractSessionIDFromDecodedPayload(requestPayload)
	headers := buildCodexTransportHeaders(record, payload)
	session := c.cachedWebsocketSession(sessionID)
	if session != nil {
		session.mu.Lock()
		defer session.mu.Unlock()
	}
	conn := (*websocket.Conn)(nil)
	if session != nil {
		conn = session.conn
	}
	if conn == nil {
		var failureBody map[string]any
		var status int
		conn, failureBody, status, err = dialCodexWebsocket(ctx, wsURL, headers)
		if err != nil {
			if ctxErr := contextErr(ctx); ctxErr != nil {
				return nil, status, ctxErr
			}
			return nil, status, err
		}
		if status > 0 {
			return failureBody, status, nil
		}
		if session != nil {
			session.conn = conn
		} else {
			defer conn.Close()
		}
	}

	sendPayload := cloneMapAny(requestPayload)
	if session != nil {
		sendPayload = prepareIncrementalWebsocketRequest(sendPayload, session.lastPayload, session.lastResponseID, session.lastOutput)
	}
	websocketPayload, err := buildCodexWebsocketPayload(sendPayload)
	if err != nil {
		if session != nil {
			closeCachedWebsocketSessionLocked(session)
		}
		return nil, 0, err
	}

	writeMessage := func(activeConn *websocket.Conn, encoded []byte) error {
		if activeConn == nil {
			return errors.New("websocket connection is unavailable")
		}
		activeConn.SetReadLimit(maxCodexResponseBodyBytes)
		if err := activeConn.WriteMessage(websocket.TextMessage, encoded); err != nil {
			if ctxErr := contextErr(ctx); ctxErr != nil {
				return ctxErr
			}
			return fmt.Errorf("send websocket request: %w", err)
		}
		return nil
	}
	if err := writeMessage(conn, websocketPayload); err != nil {
		if ctxErr := contextErr(ctx); ctxErr != nil {
			if session != nil {
				closeCachedWebsocketSessionLocked(session)
			}
			return nil, 0, ctxErr
		}
		if session != nil {
			closeCachedWebsocketSessionLocked(session)
			var failureBody map[string]any
			var status int
			var dialErr error
			conn, failureBody, status, dialErr = dialCodexWebsocket(ctx, wsURL, headers)
			if dialErr != nil {
				if ctxErr := contextErr(ctx); ctxErr != nil {
					return nil, status, ctxErr
				}
				return nil, status, dialErr
			}
			if status > 0 {
				return failureBody, status, nil
			}
			session.conn = conn
			sendPayload = cloneMapAny(requestPayload)
			websocketPayload, err = buildCodexWebsocketPayload(sendPayload)
			if err != nil {
				closeCachedWebsocketSessionLocked(session)
				return nil, 0, err
			}
			if retryErr := writeMessage(conn, websocketPayload); retryErr != nil {
				closeCachedWebsocketSessionLocked(session)
				return nil, 0, retryErr
			}
		} else {
			return nil, 0, err
		}
	}

	state := &streamDecodeState{}
	stopCancelWatch := watchCodexWebsocketCancel(ctx, conn)
	defer stopCancelWatch()
	for {
		messageType, message, err := conn.ReadMessage()
		if err != nil {
			if session != nil {
				closeCachedWebsocketSessionLocked(session)
			}
			if ctxErr := contextErr(ctx); ctxErr != nil {
				return nil, 0, ctxErr
			}
			if state.sawPayload {
				return nil, 0, newStartedWebsocketStreamError(err)
			}
			return nil, 0, fmt.Errorf("read websocket response: %w", err)
		}

		if messageType != websocket.TextMessage {
			if messageType == websocket.BinaryMessage {
				if session != nil {
					closeCachedWebsocketSessionLocked(session)
				}
				return nil, 0, errors.New("unexpected binary websocket message")
			}
			continue
		}

		payloadText := string(message)
		var decoded map[string]any
		if err := json.Unmarshal(message, &decoded); err != nil {
			codexThinkingDebugEvent("event.decode_error", map[string]any{
				"tag":           "websocket",
				"payload_chars": len(payloadText),
				"error":         err.Error(),
			})
			continue
		}

		if strings.EqualFold(strings.TrimSpace(asString(decoded["type"])), "error") {
			if session != nil {
				closeCachedWebsocketSessionLocked(session)
			}
			if shouldRetryFreshWebsocketRequest(decoded) {
				return nil, 0, fmt.Errorf("%w: %s", errWebsocketRetryFresh, compactBody(decoded))
			}
			if status, ok := websocketErrorStatus(decoded); ok {
				return map[string]any{
					"raw_body": sanitizeDiagnosticText(payloadText),
				}, status, nil
			}
			return nil, 0, fmt.Errorf("codex websocket error event: %s", sanitizeDiagnosticText(payloadText))
		}

		processResponseStreamEvent(asString(decoded["type"]), payloadText, state, onEvent)
		if strings.EqualFold(strings.TrimSpace(asString(decoded["type"])), "response.completed") {
			break
		}
	}

	decoded, err := finalizeStreamDecodeState(state)
	if err != nil {
		if session != nil {
			closeCachedWebsocketSessionLocked(session)
		}
		return nil, 0, fmt.Errorf("decode codex websocket response stream: %w", err)
	}
	if session != nil {
		session.lastPayload = cloneMapAny(requestPayload)
		session.lastResponseID = extractResponseID(decoded)
		session.lastOutput = extractResponseOutputItems(decoded)
	}
	return decoded, http.StatusOK, nil
}

func dialCodexWebsocket(ctx context.Context, wsURL string, headers http.Header) (*websocket.Conn, map[string]any, int, error) {
	dialer := websocket.Dialer{
		Proxy:             http.ProxyFromEnvironment,
		HandshakeTimeout:  30 * time.Second,
		EnableCompression: true,
	}

	conn, resp, err := dialer.DialContext(ctx, wsURL, headers)
	if err != nil {
		if resp != nil {
			defer resp.Body.Close()
			body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxCodexResponseBodyBytes))
			if readErr != nil {
				return nil, nil, resp.StatusCode, fmt.Errorf("read websocket handshake failure body: %w", readErr)
			}
			return nil, map[string]any{
				"raw_body": sanitizeDiagnosticText(string(body)),
			}, resp.StatusCode, nil
		}
		return nil, nil, 0, err
	}
	return conn, nil, 0, nil
}

func decodeCodexPayload(payload []byte) (map[string]any, error) {
	if len(payload) == 0 {
		return nil, errors.New("websocket payload is required")
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return nil, fmt.Errorf("decode codex websocket payload: %w", err)
	}
	return decoded, nil
}

func buildCodexWebsocketPayload(decoded map[string]any) ([]byte, error) {
	if len(decoded) == 0 {
		return nil, errors.New("websocket payload is required")
	}
	decoded = cloneMapAny(decoded)
	delete(decoded, "background")
	delete(decoded, "stream")
	requestType := strings.TrimSpace(asString(decoded["type"]))
	switch requestType {
	case "", "response.create":
		decoded["type"] = "response.create"
	default:
		return nil, fmt.Errorf("unsupported codex websocket request type %q", requestType)
	}
	normalizeCodexWebsocketToolParameters(decoded)
	encoded, err := json.Marshal(decoded)
	if err != nil {
		return nil, fmt.Errorf("encode codex websocket payload: %w", err)
	}
	return encoded, nil
}

func normalizeCodexWebsocketToolParameters(decoded map[string]any) {
	tools := asSlice(decoded["tools"])
	if len(tools) == 0 {
		return
	}
	for _, item := range tools {
		tool, ok := item.(map[string]any)
		if !ok {
			continue
		}
		parameters, _ := tool["parameters"].(map[string]any)
		tool["parameters"] = sanitizeCodexToolParameters(parameters)
	}
}

func extractSessionIDFromDecodedPayload(decoded map[string]any) string {
	if len(decoded) == 0 {
		return ""
	}
	sessionID := strings.TrimSpace(asString(decoded["prompt_cache_key"]))
	if sessionID == "" {
		sessionID = strings.TrimSpace(asString(decoded["session_id"]))
	}
	return sessionID
}

func prepareIncrementalWebsocketRequest(current, previous map[string]any, lastResponseID string, lastOutput []any) map[string]any {
	if len(current) == 0 || len(previous) == 0 || strings.TrimSpace(lastResponseID) == "" {
		return current
	}
	currentNoInput := cloneMapAny(current)
	delete(currentNoInput, "input")
	delete(currentNoInput, "type")
	delete(currentNoInput, "previous_response_id")

	previousNoInput := cloneMapAny(previous)
	delete(previousNoInput, "input")
	delete(previousNoInput, "type")
	delete(previousNoInput, "previous_response_id")

	if !reflect.DeepEqual(currentNoInput, previousNoInput) {
		return current
	}

	currentInput := cloneSliceAny(asSlice(current["input"]))
	baseline := cloneSliceAny(asSlice(previous["input"]))
	if len(lastOutput) > 0 {
		baseline = append(baseline, cloneSliceAny(lastOutput)...)
	}
	if !inputStartsWith(currentInput, baseline) {
		return current
	}

	incremental := cloneSliceAny(currentInput[len(baseline):])
	if incremental == nil {
		incremental = []any{}
	}
	current["previous_response_id"] = strings.TrimSpace(lastResponseID)
	current["input"] = incremental
	return current
}

func inputStartsWith(input []any, prefix []any) bool {
	if len(prefix) > len(input) {
		return false
	}
	for i := range prefix {
		if !reflect.DeepEqual(input[i], prefix[i]) {
			return false
		}
	}
	return true
}

func websocketErrorStatus(decoded map[string]any) (int, bool) {
	for _, key := range []string{"status", "status_code"} {
		if value, ok := asInt64(decoded[key]); ok && value >= 100 && value <= 599 {
			return int(value), true
		}
	}
	if nested, ok := decoded["error"].(map[string]any); ok {
		for _, key := range []string{"status", "status_code"} {
			if value, ok := asInt64(nested[key]); ok && value >= 100 && value <= 599 {
				return int(value), true
			}
		}
	}
	return 0, false
}

func websocketErrorCode(decoded map[string]any) string {
	if decoded == nil {
		return ""
	}
	if code := strings.TrimSpace(asString(decoded["code"])); code != "" {
		return code
	}
	if nested, ok := decoded["error"].(map[string]any); ok {
		return strings.TrimSpace(asString(nested["code"]))
	}
	return ""
}

func shouldRetryFreshWebsocketRequest(decoded map[string]any) bool {
	switch websocketErrorCode(decoded) {
	case "previous_response_not_found", "websocket_connection_limit_reached":
		return true
	default:
		return false
	}
}

func extractSessionIDFromPayload(payload []byte) string {
	if len(payload) == 0 {
		return ""
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return ""
	}
	sessionID := strings.TrimSpace(asString(decoded["prompt_cache_key"]))
	if sessionID == "" {
		sessionID = strings.TrimSpace(asString(decoded["session_id"]))
	}
	return sessionID
}

func buildCodexTransportHeaders(record pebblestore.CodexAuthRecord, payload []byte) http.Header {
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+bearerToken(record))
	headers.Set(originatorHeader, defaultOriginatorHeaderValue)
	headers.Set(userAgentHeader, defaultCodexTransportUserAgent)
	headers.Set(openAIBetaHeader, responsesWebsocketBetaHeaderV2)
	if sessionID := extractSessionIDFromPayload(payload); sessionID != "" {
		headers.Set("session_id", sessionID)
	}
	if record.Type == pebblestore.CodexAuthTypeOAuth && strings.TrimSpace(record.AccountID) != "" {
		headers.Set(chatGPTAccountIDHeader, strings.TrimSpace(record.AccountID))
	}
	return headers
}

func parseEventStream(body []byte) (map[string]any, error) {
	return parseEventStreamReader(bytes.NewReader(body), nil)
}

type streamDecodeState struct {
	completedResponse map[string]any
	lastObject        map[string]any
	outputText        string
	reasoningSummary  map[string]string
	reasoningOrder    []string
	outputItems       []map[string]any
	outputItemPos     map[string]int
	sawPayload        bool
}

func processResponseStreamEvent(eventName string, payload string, state *streamDecodeState, onEvent func(StreamEvent)) {
	trimmedPayload := strings.TrimSpace(payload)
	if trimmedPayload == "" || trimmedPayload == "[DONE]" {
		return
	}
	state.sawPayload = true

	var decoded map[string]any
	if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
		codexThinkingDebugEvent("event.decode_error", map[string]any{
			"tag":           eventName,
			"payload_chars": len(payload),
			"error":         err.Error(),
		})
		return
	}
	state.lastObject = decoded

	if strings.TrimSpace(eventName) == "" {
		eventName = asString(decoded["type"])
	}
	codexThinkingLogSSEEvent(eventName, decoded, len(payload))

	switch eventName {
	case "response.output_text.delta":
		delta := firstNonEmpty(asString(decoded["delta"]), asString(decoded["text"]), asString(decoded["output_text_delta"]))
		if delta != "" {
			next, appended := mergeStreamDelta(state.outputText, delta)
			state.outputText = next
			if onEvent != nil && appended != "" {
				onEvent(StreamEvent{Type: StreamEventOutputTextDelta, Delta: appended})
			}
		}
	case "response.output_text.done":
		text := strings.TrimSpace(firstNonEmpty(asString(decoded["text"]), asString(decoded["output_text"])))
		// Fallback for streams that emit only the terminal done payload without deltas.
		if text != "" {
			state.outputText = mergeOutputTextSnapshot(state.outputText, text)
		}
	case "response.output_item.added", "response.output_item.done":
		item := extractOutputItemFromEvent(decoded)
		if len(item) == 0 {
			break
		}
		recordOutputItemEvent(state, item, decoded)
		if text := extractOutputTextFromOutputItem(item); strings.TrimSpace(text) != "" {
			phase := outputItemAssistantPhase(item)
			next, appended := mergeStreamDelta(state.outputText, text)
			state.outputText = next
			if phase == provideriface.AssistantPhaseCommentary {
				if onEvent != nil && strings.TrimSpace(appended) != "" {
					onEvent(StreamEvent{Type: StreamEventAssistantCommentary, Delta: appended, Phase: phase})
				}
				break
			}
			if onEvent != nil && strings.TrimSpace(appended) != "" {
				onEvent(StreamEvent{Type: StreamEventOutputTextDelta, Delta: appended, Phase: phase})
			}
		}
	case "response.reasoning_summary_part.added", "response.reasoning_summary_part.done",
		"response.reasoning_summary_text.delta", "response.reasoning_summary.delta",
		"response.reasoning_summary_text.done", "response.reasoning_text.delta",
		"response.reasoning_text.done", "response.reasoning.delta", "response.reasoning.done":
		reasoningKey := reasoningEventKey(eventName, decoded)
		delta := reasoningEventText(eventName, decoded)
		deltaTrimmed := strings.TrimSpace(delta)
		if deltaTrimmed == "" {
			codexThinkingDebugf("tag=%s delta_chars=0", eventName)
			break
		}
		// Codex may echo reasoning-summary mode values in stream metadata events.
		// These are configuration knobs, not user-visible reasoning text.
		if isReasoningSummaryModeValue(deltaTrimmed) {
			codexThinkingDebugf("tag=%s mode_value=%s skipped=true", eventName, deltaTrimmed)
			break
		}
		previous := reasoningStateText(state, reasoningKey)
		if isReasoningSummaryModeValue(strings.TrimSpace(previous)) {
			setReasoningStateText(state, reasoningKey, "")
			previous = ""
		}
		next := previous
		if reasoningEventIsSnapshot(eventName) {
			next = mergeReasoningSummarySnapshot(previous, delta)
		} else {
			next = mergeReasoningSummaryChunk(previous, delta)
		}
		setReasoningStateText(state, reasoningKey, next)
		codexThinkingDebugf("tag=%s key=%s delta_chars=%d total_reasoning_chars=%d", eventName, reasoningKey, len(delta), len(next))
		// Emit full merged snapshot so downstream UI can atomically replace
		// the live reasoning sentence without briefly showing mixed fragments.
		if onEvent != nil && strings.TrimSpace(next) != "" && next != previous {
			onEvent(StreamEvent{Type: StreamEventReasoningSummaryDelta, Delta: next, ReasoningKey: reasoningKey})
		}
	case "response.completed":
		if responseObj, ok := decoded["response"].(map[string]any); ok {
			state.completedResponse = responseObj
			codexThinkingDebugf("tag=response.completed has_response=true")
		} else {
			state.completedResponse = decoded
			codexThinkingDebugf("tag=response.completed has_response=false")
		}
	}
}

func extractOutputItemFromEvent(decoded map[string]any) map[string]any {
	item, ok := decoded["item"].(map[string]any)
	if !ok || len(item) == 0 {
		return nil
	}
	return cloneMapAny(item)
}

func outputItemAssistantPhase(item map[string]any) provideriface.AssistantPhase {
	if len(item) == 0 {
		return provideriface.AssistantPhaseUnknown
	}
	switch strings.ToLower(strings.TrimSpace(asString(item["phase"]))) {
	case string(provideriface.AssistantPhaseCommentary):
		return provideriface.AssistantPhaseCommentary
	case string(provideriface.AssistantPhaseFinalAnswer):
		return provideriface.AssistantPhaseFinalAnswer
	default:
		return provideriface.AssistantPhaseUnknown
	}
}

func recordOutputItemEvent(state *streamDecodeState, item map[string]any, event map[string]any) {
	if state == nil || len(item) == 0 {
		return
	}
	if state.outputItemPos == nil {
		state.outputItemPos = make(map[string]int, 8)
	}
	key := outputItemEventKey(item, event, len(state.outputItems))
	if pos, ok := state.outputItemPos[key]; ok && pos >= 0 && pos < len(state.outputItems) {
		state.outputItems[pos] = cloneMapAny(item)
		return
	}
	state.outputItems = append(state.outputItems, cloneMapAny(item))
	state.outputItemPos[key] = len(state.outputItems) - 1
}

func outputItemEventKey(item map[string]any, event map[string]any, fallbackIndex int) string {
	if len(item) == 0 {
		return fmt.Sprintf("idx:%d", fallbackIndex)
	}
	if id := strings.TrimSpace(asString(item["id"])); id != "" {
		return "id:" + id
	}
	if callID := strings.TrimSpace(asString(item["call_id"])); callID != "" {
		return "call_id:" + callID
	}
	if event != nil {
		if outputIndex, ok := asInt64(event["output_index"]); ok {
			return fmt.Sprintf("output_index:%d", outputIndex)
		}
	}
	itemType := strings.TrimSpace(asString(item["type"]))
	if itemType == "" {
		return fmt.Sprintf("idx:%d", fallbackIndex)
	}
	if name := strings.TrimSpace(asString(item["name"])); name != "" {
		return fmt.Sprintf("type_name:%s:%s", itemType, name)
	}
	return fmt.Sprintf("type:%s:%d", itemType, fallbackIndex)
}

func outputItemMergeKey(item map[string]any, fallbackIndex int) string {
	if len(item) == 0 {
		return fmt.Sprintf("idx:%d", fallbackIndex)
	}
	if id := strings.TrimSpace(asString(item["id"])); id != "" {
		return "id:" + id
	}
	if callID := strings.TrimSpace(asString(item["call_id"])); callID != "" {
		return "call_id:" + callID
	}
	itemType := strings.TrimSpace(asString(item["type"]))
	if itemType == "" {
		return fmt.Sprintf("idx:%d", fallbackIndex)
	}
	switch itemType {
	case "message":
		role := strings.TrimSpace(asString(item["role"]))
		phase := strings.TrimSpace(asString(item["phase"]))
		text := strings.Join(strings.Fields(extractOutputTextFromOutputItem(item)), " ")
		if role != "" || phase != "" || text != "" {
			return fmt.Sprintf("message:%s:%s:%s", role, phase, text)
		}
	case "output_text", "text":
		text := strings.Join(strings.Fields(extractOutputTextFromOutputItem(item)), " ")
		if text != "" {
			return fmt.Sprintf("%s:%s", itemType, text)
		}
	case "function_call":
		name := strings.TrimSpace(asString(item["name"]))
		arguments := strings.TrimSpace(normalizeArguments(item["arguments"]))
		if name != "" || arguments != "" {
			return fmt.Sprintf("function_call:%s:%s", name, arguments)
		}
	}
	if name := strings.TrimSpace(asString(item["name"])); name != "" {
		return fmt.Sprintf("type_name:%s:%s", itemType, name)
	}
	return fmt.Sprintf("type:%s:%d", itemType, fallbackIndex)
}

func reasoningStreamStateKey(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return "default"
	}
	return key
}

func reasoningEventKey(eventName string, decoded map[string]any) string {
	itemID := strings.TrimSpace(asString(decoded["item_id"]))
	outputIndex, hasOutputIndex := asInt64(decoded["output_index"])
	summaryIndex, hasSummaryIndex := asInt64(decoded["summary_index"])
	contentIndex, hasContentIndex := asInt64(decoded["content_index"])

	buildKey := func(kind string) string {
		parts := make([]string, 0, 4)
		if kind != "" {
			parts = append(parts, kind)
		}
		if itemID != "" {
			parts = append(parts, itemID)
		}
		if hasOutputIndex {
			parts = append(parts, fmt.Sprintf("output:%d", outputIndex))
		}
		switch kind {
		case "summary":
			if hasSummaryIndex {
				parts = append(parts, fmt.Sprintf("summary:%d", summaryIndex))
			}
		case "text":
			if hasContentIndex {
				parts = append(parts, fmt.Sprintf("content:%d", contentIndex))
			}
		}
		return strings.Join(parts, "|")
	}

	switch eventName {
	case "response.reasoning_summary_part.added", "response.reasoning_summary_part.done",
		"response.reasoning_summary_text.delta", "response.reasoning_summary.delta",
		"response.reasoning_summary_text.done":
		return buildKey("summary")
	case "response.reasoning_text.delta", "response.reasoning_text.done":
		return buildKey("text")
	case "response.reasoning.delta", "response.reasoning.done":
		return buildKey("reasoning")
	default:
		return buildKey("")
	}
}

func reasoningEventText(eventName string, decoded map[string]any) string {
	switch eventName {
	case "response.reasoning_summary_part.added", "response.reasoning_summary_part.done":
		part, ok := decoded["part"].(map[string]any)
		if !ok {
			return ""
		}
		partType := strings.TrimSpace(asString(part["type"]))
		if partType != "" && partType != "summary_text" {
			return ""
		}
		return asString(part["text"])
	default:
		return firstNonEmpty(asString(decoded["delta"]), asString(decoded["text"]))
	}
}

func reasoningEventIsSnapshot(eventName string) bool {
	switch eventName {
	case "response.reasoning_summary_part.added", "response.reasoning_summary_part.done",
		"response.reasoning_summary_text.done", "response.reasoning_text.done", "response.reasoning.done":
		return true
	default:
		return false
	}
}

func reasoningStateText(state *streamDecodeState, key string) string {
	if state == nil || state.reasoningSummary == nil {
		return ""
	}
	return state.reasoningSummary[reasoningStreamStateKey(key)]
}

func setReasoningStateText(state *streamDecodeState, key, text string) {
	if state == nil {
		return
	}
	if state.reasoningSummary == nil {
		state.reasoningSummary = make(map[string]string, 4)
	}
	key = reasoningStreamStateKey(key)
	if _, ok := state.reasoningSummary[key]; !ok {
		state.reasoningOrder = append(state.reasoningOrder, key)
	}
	state.reasoningSummary[key] = text
}

func aggregateReasoningStateText(state *streamDecodeState) string {
	if state == nil || len(state.reasoningSummary) == 0 {
		return ""
	}
	parts := make([]string, 0, len(state.reasoningOrder))
	for _, key := range state.reasoningOrder {
		text := strings.TrimSpace(state.reasoningSummary[key])
		if text == "" {
			continue
		}
		parts = append(parts, text)
	}
	return strings.TrimSpace(strings.Join(parts, "\n\n"))
}

func extractOutputTextFromOutputItem(item map[string]any) string {
	if len(item) == 0 {
		return ""
	}
	itemType := strings.TrimSpace(asString(item["type"]))
	switch itemType {
	case "message":
		return extractOutputTextFromMessageContent(item["content"])
	case "output_text", "text":
		return strings.TrimSpace(asString(item["text"]))
	default:
		if text := strings.TrimSpace(asString(item["text"])); text != "" {
			return text
		}
		return extractOutputTextFromMessageContent(item["content"])
	}
}

func extractOutputTextFromMessageContent(value any) string {
	items := asSlice(value)
	if len(items) == 0 {
		return ""
	}
	parts := make([]string, 0, len(items))
	for _, rawContent := range items {
		content, ok := rawContent.(map[string]any)
		if !ok {
			continue
		}
		contentType := strings.TrimSpace(asString(content["type"]))
		if contentType != "" && contentType != "output_text" && contentType != "text" && contentType != "input_text" && contentType != "summary_text" {
			continue
		}
		if text := strings.TrimSpace(asString(content["text"])); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n\n"))
}

func extractResponseID(decoded map[string]any) string {
	if len(decoded) == 0 {
		return ""
	}
	if response, ok := decoded["response"].(map[string]any); ok {
		if id := strings.TrimSpace(asString(response["id"])); id != "" {
			return id
		}
	}
	if id := strings.TrimSpace(asString(decoded["id"])); id != "" {
		return id
	}
	return ""
}

func extractResponseOutputItems(decoded map[string]any) []any {
	if len(decoded) == 0 {
		return nil
	}
	if response, ok := decoded["response"].(map[string]any); ok {
		if output := cloneSliceAny(asSlice(response["output"])); len(output) > 0 {
			return output
		}
	}
	if output := cloneSliceAny(asSlice(decoded["output"])); len(output) > 0 {
		return output
	}
	return nil
}

func mergeOutputItemsIntoResponse(responseObj map[string]any, outputItems []map[string]any) {
	if responseObj == nil || len(outputItems) == 0 {
		return
	}
	existingRaw := asSlice(responseObj["output"])
	if len(existingRaw) == 0 {
		responseObj["output"] = mapsToAnySlice(outputItems)
		return
	}

	seen := make(map[string]struct{}, len(existingRaw)+len(outputItems))
	merged := make([]map[string]any, 0, len(existingRaw)+len(outputItems))
	for i, rawItem := range existingRaw {
		item, ok := rawItem.(map[string]any)
		if !ok {
			continue
		}
		key := outputItemMergeKey(item, i)
		seen[key] = struct{}{}
		merged = append(merged, cloneMapAny(item))
	}
	for i, item := range outputItems {
		key := outputItemMergeKey(item, i+len(merged))
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		merged = append(merged, cloneMapAny(item))
	}
	responseObj["output"] = mapsToAnySlice(merged)
}

func mapsToAnySlice(values []map[string]any) []any {
	if len(values) == 0 {
		return nil
	}
	out := make([]any, 0, len(values))
	for _, value := range values {
		if len(value) == 0 {
			continue
		}
		out = append(out, cloneMapAny(value))
	}
	return out
}

func finalizeStreamDecodeState(state *streamDecodeState) (map[string]any, error) {
	reasoningSummary := aggregateReasoningStateText(state)
	if state.completedResponse != nil {
		mergeOutputItemsIntoResponse(state.completedResponse, state.outputItems)
		if strings.TrimSpace(asString(state.completedResponse["output_text"])) == "" && strings.TrimSpace(state.outputText) != "" {
			state.completedResponse["output_text"] = state.outputText
		}
		if merged := mergeReasoningSummarySnapshot(asString(state.completedResponse["reasoning_summary_text"]), reasoningSummary); strings.TrimSpace(merged) != "" {
			state.completedResponse["reasoning_summary_text"] = merged
		}
		if summary := strings.TrimSpace(asString(state.completedResponse["reasoning_summary_text"])); summary != "" {
			codexThinkingDebugf("result=completed_response reasoning_summary_chars=%d", len(summary))
		}
		return state.completedResponse, nil
	}
	if state.lastObject != nil {
		mergeOutputItemsIntoResponse(state.lastObject, state.outputItems)
		if strings.TrimSpace(asString(state.lastObject["output_text"])) == "" && strings.TrimSpace(state.outputText) != "" {
			state.lastObject["output_text"] = state.outputText
		}
		if merged := mergeReasoningSummarySnapshot(asString(state.lastObject["reasoning_summary_text"]), reasoningSummary); strings.TrimSpace(merged) != "" {
			state.lastObject["reasoning_summary_text"] = merged
		}
		if summary := strings.TrimSpace(asString(state.lastObject["reasoning_summary_text"])); summary != "" {
			codexThinkingDebugf("result=last_object reasoning_summary_chars=%d", len(summary))
		}
		return state.lastObject, nil
	}
	if len(state.outputItems) > 0 {
		payload := map[string]any{
			"output": mapsToAnySlice(state.outputItems),
		}
		if strings.TrimSpace(state.outputText) != "" {
			payload["output_text"] = state.outputText
		}
		if strings.TrimSpace(reasoningSummary) != "" {
			payload["reasoning_summary_text"] = reasoningSummary
		}
		return payload, nil
	}
	if strings.TrimSpace(state.outputText) != "" {
		payload := map[string]any{
			"output_text": state.outputText,
		}
		if strings.TrimSpace(reasoningSummary) != "" {
			payload["reasoning_summary_text"] = reasoningSummary
			codexThinkingDebugf("result=output_fallback reasoning_summary_chars=%d", len(reasoningSummary))
		}
		return payload, nil
	}
	if strings.TrimSpace(reasoningSummary) != "" {
		codexThinkingDebugf("result=reasoning_only reasoning_summary_chars=%d", len(reasoningSummary))
		return map[string]any{
			"reasoning_summary_text": reasoningSummary,
		}, nil
	}
	if !state.sawPayload {
		return map[string]any{}, nil
	}
	return nil, errors.New("no decodable events in codex response stream")
}

func parseEventStreamReader(reader io.Reader, onEvent func(StreamEvent)) (map[string]any, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 8<<20)

	state := &streamDecodeState{}
	eventName := ""
	dataLines := make([]string, 0, 8)

	flushEvent := func() {
		if len(dataLines) == 0 {
			eventName = ""
			return
		}
		payload := strings.Join(dataLines, "\n")
		dataLines = dataLines[:0]
		processResponseStreamEvent(eventName, payload, state, onEvent)
		eventName = ""
	}

	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if strings.TrimSpace(line) == "" {
			flushEvent()
			continue
		}
		if strings.HasPrefix(line, "event:") {
			eventName = strings.TrimSpace(line[len("event:"):])
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimLeft(line[len("data:"):], " \t"))
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	flushEvent()
	return finalizeStreamDecodeState(state)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// mergeStreamDelta appends delta text while handling providers that emit cumulative
// chunks (where each chunk repeats prior content). It returns the merged content
// and only the truly appended suffix for downstream streaming callbacks.
func mergeStreamDelta(current, delta string) (string, string) {
	if delta == "" {
		return current, ""
	}
	if current == "" {
		return delta, delta
	}
	if strings.HasSuffix(current, delta) {
		return current, ""
	}
	if strings.HasPrefix(delta, current) {
		return delta, delta[len(current):]
	}

	maxOverlap := len(current)
	if len(delta) < maxOverlap {
		maxOverlap = len(delta)
	}
	for overlap := maxOverlap; overlap > 0; overlap-- {
		if strings.HasSuffix(current, delta[:overlap]) {
			return current + delta[overlap:], delta[overlap:]
		}
	}
	return current + delta, delta
}

func mergeCanonicalSnapshot(currentRaw, snapshotRaw string, normalize func(string) string, shouldReplace func(string, string) bool) string {
	current := normalize(currentRaw)
	snapshot := normalize(snapshotRaw)
	if snapshot == "" {
		return currentRaw
	}
	if current == "" {
		return snapshotRaw
	}
	if snapshot == current {
		return currentRaw
	}
	if strings.HasPrefix(snapshot, current) {
		return snapshotRaw
	}
	if strings.HasPrefix(current, snapshot) {
		return currentRaw
	}
	if shouldReplace != nil && shouldReplace(current, snapshot) {
		return snapshotRaw
	}
	next, _ := mergeStreamDelta(currentRaw, snapshotRaw)
	return next
}

func mergeOutputTextSnapshot(current, snapshot string) string {
	return mergeCanonicalSnapshot(current, snapshot, strings.TrimSpace, shouldReplaceOutputTextSnapshot)
}

func shouldReplaceOutputTextSnapshot(current, snapshot string) bool {
	return sharedPrefixLength(current, snapshot) >= 48
}

func mergeReasoningSummaryChunk(current, chunk string) string {
	currentRaw := current
	chunkRaw := chunk
	current = normalizeReasoningSummary(currentRaw)
	chunk = normalizeReasoningSummary(chunkRaw)
	if chunk == "" {
		return currentRaw
	}
	if current == "" {
		return chunkRaw
	}
	if chunk == current {
		return currentRaw
	}
	if strings.HasPrefix(chunk, current) {
		return chunkRaw
	}
	if strings.HasPrefix(current, chunk) {
		return currentRaw
	}
	if shouldReplaceReasoningSummarySnapshot(current, chunk) {
		return chunkRaw
	}
	next, _ := mergeStreamDelta(currentRaw, chunkRaw)
	return next
}

func mergeReasoningSummarySnapshot(current, snapshot string) string {
	return mergeCanonicalSnapshot(current, snapshot, normalizeReasoningSummary, shouldReplaceFullReasoningSummarySnapshot)
}

func shouldReplaceReasoningSummarySnapshot(current, chunk string) bool {
	currentLead := reasoningSummaryLead(current)
	chunkLead := reasoningSummaryLead(chunk)
	if currentLead != "" && chunkLead != "" && currentLead == chunkLead {
		return true
	}
	return sharedPrefixLength(current, chunk) >= 48
}

func shouldReplaceFullReasoningSummarySnapshot(current, snapshot string) bool {
	if shouldReplaceReasoningSummarySnapshot(current, snapshot) {
		return true
	}
	return looksLikeFullReasoningSummarySnapshot(current) && looksLikeFullReasoningSummarySnapshot(snapshot)
}

func normalizeReasoningSummary(summary string) string {
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return ""
	}
	summary = dedupeAdjacentReasoningSummaryBlocks(summary)
	lead := reasoningSummaryLead(summary)
	if lead == "" {
		return summary
	}
	if idx := strings.LastIndex(summary, lead); idx > 0 {
		candidate := strings.TrimSpace(summary[idx:])
		if candidate != "" {
			return dedupeAdjacentReasoningSummaryBlocks(candidate)
		}
	}
	return dedupeAdjacentReasoningSummaryBlocks(summary)
}

func looksLikeFullReasoningSummarySnapshot(summary string) bool {
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return false
	}
	return strings.Contains(summary, "\n\n") || strings.HasPrefix(summary, "**") || strings.HasPrefix(summary, "__") || len(summary) >= 96
}

func dedupeAdjacentReasoningSummaryBlocks(summary string) string {
	blocks := splitReasoningSummaryBlocks(summary)
	if len(blocks) < 2 {
		return strings.TrimSpace(summary)
	}
	deduped := make([]string, 0, len(blocks))
	for i := 0; i < len(blocks); {
		window, repeats := repeatedReasoningSummaryWindow(blocks, i)
		if window > 0 && repeats > 1 {
			deduped = append(deduped, blocks[i:i+window]...)
			i += window * repeats
			continue
		}
		deduped = append(deduped, blocks[i])
		i++
	}
	return strings.TrimSpace(strings.Join(deduped, "\n\n"))
}

func splitReasoningSummaryBlocks(summary string) []string {
	summary = strings.ReplaceAll(summary, "\r\n", "\n")
	parts := strings.Split(summary, "\n\n")
	blocks := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		blocks = append(blocks, part)
	}
	return blocks
}

func repeatedReasoningSummaryWindow(blocks []string, start int) (int, int) {
	remaining := len(blocks) - start
	maxWindow := remaining / 2
	for window := maxWindow; window >= 1; window-- {
		repeats := 1
		for start+(repeats+1)*window <= len(blocks) && reasoningSummaryBlockSliceEqual(blocks[start:start+window], blocks[start+repeats*window:start+(repeats+1)*window]) {
			repeats++
		}
		if repeats > 1 {
			return window, repeats
		}
	}
	return 0, 0
}

func reasoningSummaryBlockSliceEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if reasoningSummaryBlockKey(left[i]) != reasoningSummaryBlockKey(right[i]) {
			return false
		}
	}
	return true
}

func reasoningSummaryBlockKey(block string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(block)), " "))
}

func reasoningSummaryLead(summary string) string {
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return ""
	}
	if strings.HasPrefix(summary, "**") {
		if end := strings.Index(summary[2:], "**"); end >= 0 {
			return summary[:end+4]
		}
	}
	if strings.HasPrefix(summary, "__") {
		if end := strings.Index(summary[2:], "__"); end >= 0 {
			return summary[:end+4]
		}
	}
	if line := firstReasoningSummaryLine(summary); line != "" {
		return line
	}
	return ""
}

func firstReasoningSummaryLine(summary string) string {
	for _, line := range strings.Split(summary, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return ""
}

func sharedPrefixLength(left, right string) int {
	max := len(left)
	if len(right) < max {
		max = len(right)
	}
	count := 0
	for count < max && left[count] == right[count] {
		count++
	}
	return count
}

func compactBody(decoded map[string]any) string {
	if len(decoded) == 0 {
		return "{}"
	}
	body, err := json.Marshal(sanitizeDiagnosticValue(decoded))
	if err != nil {
		return "{}"
	}
	compact := sanitizeDiagnosticText(string(body))
	if len(compact) > 1200 {
		return compact[:1200] + "...[truncated]"
	}
	return compact
}

func sanitizeDiagnosticText(raw string) string {
	return privacy.SanitizeText(raw)
}

func sanitizeDiagnosticValue(value any) any {
	return privacy.SanitizeValue(value)
}

func isSensitiveDiagnosticKey(key string) bool {
	return false
}

func codexThinkingDebugEnabled() bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv("SWARMD_CODEX_THINKING_DEBUG")))
	switch value {
	case "1", "true", "yes", "on", "debug":
		return true
	default:
		return false
	}
}

func codexThinkingDebugf(format string, args ...any) {
	if !codexThinkingDebugEnabled() {
		return
	}
	_, _ = fmt.Fprintf(os.Stderr, "[swarmd.codex.thinking] "+format+"\n", args...)
}

func codexThinkingLogSSEEvent(tag string, decoded map[string]any, payloadChars int) {
	if !codexThinkingDebugEnabled() {
		return
	}
	meta := map[string]any{
		"tag":           strings.TrimSpace(tag),
		"payload_chars": payloadChars,
		"keys":          strings.Join(sortedMapKeys(decoded), ","),
	}
	if delta := firstNonEmpty(asString(decoded["delta"]), asString(decoded["text"]), asString(decoded["output_text_delta"])); delta != "" {
		meta["delta_chars"] = len(delta)
	}
	if response, ok := decoded["response"].(map[string]any); ok {
		meta["response_keys"] = strings.Join(sortedMapKeys(response), ",")
	}
	codexThinkingDebugEvent("sse_event", meta)
}

func codexThinkingDebugEvent(event string, data map[string]any) {
	if !codexThinkingDebugEnabled() {
		return
	}
	event = strings.TrimSpace(event)
	if event == "" {
		event = "event"
	}
	clean := map[string]any{
		"ts":    time.Now().UTC().Format(time.RFC3339Nano),
		"event": event,
		"data":  sanitizeDiagnosticValue(data),
	}
	if _, err := json.Marshal(clean); err != nil {
		codexThinkingDebugf("event=%s encode_error=true", event)
		return
	}
	codexThinkingDebugf("event=%s", event)
}

func appendCodexThinkingDebugLine(path string, line []byte) error {
	return nil
}

func sortedMapKeys(value map[string]any) []string {
	if len(value) == 0 {
		return nil
	}
	keys := make([]string, 0, len(value))
	for key := range value {
		if trimmed := strings.TrimSpace(key); trimmed != "" {
			keys = append(keys, trimmed)
		}
	}
	sort.Strings(keys)
	return keys
}

func bearerToken(record pebblestore.CodexAuthRecord) string {
	if record.Type == pebblestore.CodexAuthTypeOAuth {
		return record.AccessToken
	}
	return record.APIKey
}

func parseResponse(decoded map[string]any) Response {
	responseObj := decoded
	if nested, ok := decoded["response"].(map[string]any); ok {
		responseObj = nested
	}

	out := Response{
		ID:         asString(responseObj["id"]),
		Model:      asString(responseObj["model"]),
		StopReason: extractStopReason(responseObj, decoded),
	}
	if out.ID == "" {
		out.ID = asString(decoded["id"])
	}
	if out.Model == "" {
		out.Model = asString(decoded["model"])
	}
	out.ReasoningSummary = normalizeReasoningSummary(extractReasoningSummary(responseObj, decoded))
	out.Usage = extractTokenUsage(responseObj, decoded)

	textParts := make([]string, 0, 4)
	directOutputText := strings.TrimSpace(asString(responseObj["output_text"]))
	hasDirectOutputText := directOutputText != ""
	if hasDirectOutputText {
		textParts = append(textParts, directOutputText)
		out.Messages = append(out.Messages, AssistantMessage{Text: directOutputText, Phase: provideriface.AssistantPhaseFinalAnswer})
	}

	outputItems := asSlice(responseObj["output"])
	for _, rawItem := range outputItems {
		item, ok := rawItem.(map[string]any)
		if !ok {
			continue
		}
		switch asString(item["type"]) {
		case "function_call":
			call := FunctionCall{
				CallID:    strings.TrimSpace(asString(item["call_id"])),
				Name:      strings.TrimSpace(asString(item["name"])),
				Arguments: normalizeArguments(item["arguments"]),
			}
			if call.CallID == "" {
				call.CallID = strings.TrimSpace(asString(item["id"]))
			}
			if call.CallID != "" && call.Name != "" {
				out.FunctionCalls = append(out.FunctionCalls, call)
			}
		case "message":
			if hasDirectOutputText {
				// The completed response frequently includes equivalent message content;
				// prefer output_text to avoid duplicated assistant text.
				continue
			}
			phase := outputItemAssistantPhase(item)
			messageParts := make([]string, 0, 4)
			for _, rawContent := range asSlice(item["content"]) {
				content, ok := rawContent.(map[string]any)
				if !ok {
					continue
				}
				contentType := strings.TrimSpace(asString(content["type"]))
				if contentType != "" && contentType != "output_text" && contentType != "text" && contentType != "input_text" && contentType != "summary_text" {
					continue
				}
				if text := asString(content["text"]); strings.TrimSpace(text) != "" {
					messageParts = append(messageParts, text)
					textParts = append(textParts, text)
				}
			}
			messageText := strings.TrimSpace(strings.Join(messageParts, "\n\n"))
			if messageText != "" {
				out.Messages = append(out.Messages, AssistantMessage{Text: messageText, Phase: phase})
			}
		}
	}
	if len(textParts) == 0 {
		if fallback := asString(decoded["output_text"]); strings.TrimSpace(fallback) != "" {
			textParts = append(textParts, fallback)
		}
	}
	out.Text = strings.TrimSpace(strings.Join(textParts, "\n\n"))
	return out
}

func extractStopReason(responseObj map[string]any, decoded map[string]any) string {
	stopReason := strings.TrimSpace(firstNonEmpty(
		asString(responseObj["stop_reason"]),
		asString(decoded["stop_reason"]),
	))
	status := strings.TrimSpace(firstNonEmpty(
		asString(responseObj["status"]),
		asString(decoded["status"]),
	))
	incompleteReason := strings.TrimSpace(firstNonEmpty(
		extractIncompleteReason(responseObj["incomplete_details"]),
		extractIncompleteReason(decoded["incomplete_details"]),
	))
	errorDetail := strings.TrimSpace(firstNonEmpty(
		extractResponseErrorDetail(responseObj["error"]),
		extractResponseErrorDetail(decoded["error"]),
	))
	if stopReason == "" {
		stopReason = incompleteReason
	}

	if errorDetail != "" {
		prefix := ""
		if status != "" && !strings.EqualFold(status, "completed") {
			prefix = status
		} else if stopReason != "" {
			prefix = stopReason
		}
		if prefix != "" && !strings.Contains(strings.ToLower(errorDetail), strings.ToLower(prefix)) {
			return sanitizeDiagnosticText(prefix + ": " + errorDetail)
		}
		return sanitizeDiagnosticText(errorDetail)
	}

	if stopReason != "" && status != "" && !strings.EqualFold(status, "completed") && !strings.EqualFold(status, stopReason) {
		return sanitizeDiagnosticText(status + ": " + stopReason)
	}
	if stopReason != "" {
		return sanitizeDiagnosticText(stopReason)
	}
	if status != "" && !strings.EqualFold(status, "completed") {
		return sanitizeDiagnosticText(status)
	}
	return ""
}

func extractIncompleteReason(value any) string {
	details, ok := value.(map[string]any)
	if !ok || len(details) == 0 {
		return ""
	}
	reason := strings.TrimSpace(firstNonEmpty(
		asString(details["reason"]),
		asString(details["type"]),
		asString(details["message"]),
	))
	if reason == "" {
		return ""
	}
	return sanitizeDiagnosticText(reason)
}

func extractResponseErrorDetail(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return sanitizeDiagnosticText(strings.TrimSpace(typed))
	case map[string]any:
		nested := strings.TrimSpace(extractResponseErrorDetail(typed["error"]))
		code := strings.TrimSpace(firstNonEmpty(asString(typed["code"]), asString(typed["type"])))
		message := strings.TrimSpace(firstNonEmpty(
			asString(typed["message"]),
			asString(typed["detail"]),
			nested,
		))
		switch {
		case code != "" && message != "":
			return sanitizeDiagnosticText(code + ": " + message)
		case message != "":
			return sanitizeDiagnosticText(message)
		case code != "":
			return sanitizeDiagnosticText(code)
		default:
			if len(typed) == 0 {
				return ""
			}
			encoded, err := json.Marshal(sanitizeDiagnosticValue(typed))
			if err != nil {
				return ""
			}
			return sanitizeDiagnosticText(strings.TrimSpace(string(encoded)))
		}
	default:
		return ""
	}
}

func extractTokenUsage(responseObj map[string]any, decoded map[string]any) TokenUsage {
	transport, connectedViaWS := extractCodexTransportMetadata(decoded)
	usage, usagePath, ok := findUsageObject(responseObj, decoded)
	if !ok {
		if transport == "" && connectedViaWS == nil {
			return TokenUsage{}
		}
		return TokenUsage{
			Source:         "codex_api_usage",
			Transport:      transport,
			ConnectedViaWS: connectedViaWS,
		}
	}
	inputTokens, _ := intFromPath(usage, "input_tokens")
	outputTokens, _ := intFromPath(usage, "output_tokens")
	thinkingTokens, _ := intFromPath(usage, "output_tokens_details", "reasoning_tokens")
	totalTokens, _ := intFromPath(usage, "total_tokens")
	cacheReadTokens, _ := intFromPath(usage, "input_tokens_details", "cached_tokens")
	cacheWriteTokens, _ := intFromPath(usage, "input_tokens_details", "cache_creation_tokens")

	usageRaw := cloneMapAny(usage)
	out := TokenUsage{
		InputTokens:      inputTokens,
		OutputTokens:     outputTokens,
		ThinkingTokens:   thinkingTokens,
		TotalTokens:      totalTokens,
		CacheReadTokens:  cacheReadTokens,
		CacheWriteTokens: cacheWriteTokens,
		Source:           "codex_api_usage",
		Transport:        transport,
		ConnectedViaWS:   connectedViaWS,
		APIUsageRaw:      usageRaw,
		APIUsageRawPath:  usagePath,
		APIUsageHistory:  []map[string]any{cloneMapAny(usageRaw)},
		APIUsagePaths:    []string{usagePath},
	}
	return out
}

func extractCodexTransportMetadata(decoded map[string]any) (string, *bool) {
	if len(decoded) == 0 {
		return "", nil
	}
	transport := strings.ToLower(strings.TrimSpace(asString(decoded[codexTransportMetadataKey])))
	if transport == "" {
		return "", nil
	}
	connected, ok := decoded[codexConnectedViaWSMetadataKey].(bool)
	if !ok {
		return transport, nil
	}
	return transport, boolPointer(connected)
}

func boolPointer(value bool) *bool {
	out := value
	return &out
}

func findUsageObject(responseObj map[string]any, decoded map[string]any) (map[string]any, string, bool) {
	if usage, ok := responseObj["usage"].(map[string]any); ok && len(usage) > 0 {
		return usage, "response.usage", true
	}
	if response, ok := decoded["response"].(map[string]any); ok {
		if usage, ok := response["usage"].(map[string]any); ok && len(usage) > 0 {
			return usage, "response.usage", true
		}
	}
	if usage, ok := decoded["usage"].(map[string]any); ok && len(usage) > 0 {
		return usage, "usage", true
	}
	return nil, "", false
}

func intFromPath(root map[string]any, path ...string) (int64, bool) {
	if len(path) == 0 || root == nil {
		return 0, false
	}
	var current any = root
	for _, key := range path {
		node, ok := current.(map[string]any)
		if !ok {
			return 0, false
		}
		next, ok := node[key]
		if !ok {
			return 0, false
		}
		current = next
	}
	return asInt64(current)
}

func asInt64(value any) (int64, bool) {
	switch typed := value.(type) {
	case int:
		return int64(typed), true
	case int8:
		return int64(typed), true
	case int16:
		return int64(typed), true
	case int32:
		return int64(typed), true
	case int64:
		return typed, true
	case uint:
		return int64(typed), true
	case uint8:
		return int64(typed), true
	case uint16:
		return int64(typed), true
	case uint32:
		return int64(typed), true
	case uint64:
		if typed > uint64(^uint64(0)>>1) {
			return int64(^uint64(0) >> 1), true
		}
		return int64(typed), true
	case float32:
		return int64(typed), true
	case float64:
		return int64(typed), true
	case json.Number:
		if i, err := typed.Int64(); err == nil {
			return i, true
		}
		if f, err := typed.Float64(); err == nil {
			return int64(f), true
		}
		return 0, false
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return 0, false
		}
		if i, err := strconv.ParseInt(trimmed, 10, 64); err == nil {
			return i, true
		}
		if f, err := strconv.ParseFloat(trimmed, 64); err == nil {
			return int64(f), true
		}
		return 0, false
	default:
		return 0, false
	}
}

func cloneMapAny(input map[string]any) map[string]any {
	if len(input) == 0 {
		return nil
	}
	clone := make(map[string]any, len(input))
	for key, value := range input {
		if nestedMap, ok := value.(map[string]any); ok {
			clone[key] = cloneMapAny(nestedMap)
			continue
		}
		if nestedSlice, ok := value.([]any); ok {
			clone[key] = cloneSliceAny(nestedSlice)
			continue
		}
		clone[key] = value
	}
	return clone
}

func cloneSliceAny(input []any) []any {
	if len(input) == 0 {
		return nil
	}
	clone := make([]any, 0, len(input))
	for _, value := range input {
		if nestedMap, ok := value.(map[string]any); ok {
			clone = append(clone, cloneMapAny(nestedMap))
			continue
		}
		if nestedSlice, ok := value.([]any); ok {
			clone = append(clone, cloneSliceAny(nestedSlice))
			continue
		}
		clone = append(clone, value)
	}
	return clone
}

func extractReasoningSummary(responseObj map[string]any, decoded map[string]any) string {
	if summary := strings.TrimSpace(asString(responseObj["reasoning_summary_text"])); summary != "" {
		return summary
	}
	if summary := strings.TrimSpace(asString(decoded["reasoning_summary_text"])); summary != "" {
		return summary
	}
	if summary := extractReasoningSummaryFromOutput(responseObj["output"]); summary != "" {
		return summary
	}
	if reasoning, ok := responseObj["reasoning"].(map[string]any); ok {
		if summary := strings.TrimSpace(asString(reasoning["summary"])); summary != "" && !isReasoningSummaryModeValue(summary) {
			return summary
		}
	}
	if response, ok := decoded["response"].(map[string]any); ok {
		if summary := extractReasoningSummaryFromOutput(response["output"]); summary != "" {
			return summary
		}
		if reasoning, ok := response["reasoning"].(map[string]any); ok {
			if summary := strings.TrimSpace(asString(reasoning["summary"])); summary != "" && !isReasoningSummaryModeValue(summary) {
				return summary
			}
		}
	}
	if summary := extractReasoningSummaryFromOutput(decoded["output"]); summary != "" {
		return summary
	}
	return ""
}

func extractReasoningSummaryFromOutput(value any) string {
	items := asSlice(value)
	if len(items) == 0 {
		return ""
	}
	parts := make([]string, 0, 4)
	for _, rawItem := range items {
		item, ok := rawItem.(map[string]any)
		if !ok {
			continue
		}
		if strings.TrimSpace(asString(item["type"])) != "reasoning" {
			continue
		}
		for _, rawSummary := range asSlice(item["summary"]) {
			summaryPart, ok := rawSummary.(map[string]any)
			if !ok {
				continue
			}
			partType := strings.TrimSpace(asString(summaryPart["type"]))
			if partType != "" && partType != "summary_text" {
				continue
			}
			text := strings.TrimSpace(asString(summaryPart["text"]))
			if text == "" {
				continue
			}
			parts = append(parts, text)
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n\n"))
}

func isReasoningSummaryModeValue(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "auto", "concise", "detailed", "none":
		return true
	default:
		return false
	}
}

func normalizeArguments(value any) string {
	switch typed := value.(type) {
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return "{}"
		}
		return trimmed
	case map[string]any, []any:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return "{}"
		}
		return string(encoded)
	default:
		return "{}"
	}
}

func asString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case fmt.Stringer:
		return typed.String()
	default:
		return ""
	}
}

func asSlice(value any) []any {
	if value == nil {
		return nil
	}
	slice, ok := value.([]any)
	if !ok {
		return nil
	}
	return slice
}

func extractAccountIDFromToken(token string) string {
	token = strings.TrimSpace(token)
	if token == "" {
		return ""
	}
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return ""
	}
	if value := asString(claims["chatgpt_account_id"]); value != "" {
		return value
	}
	if authSection, ok := claims["https://api.openai.com/auth"].(map[string]any); ok {
		if value := asString(authSection["chatgpt_account_id"]); value != "" {
			return value
		}
	}
	if organizations := asSlice(claims["organizations"]); len(organizations) > 0 {
		if first, ok := organizations[0].(map[string]any); ok {
			if value := asString(first["id"]); value != "" {
				return value
			}
		}
	}
	return ""
}

package codex

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
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/longsessiondiag"
	"swarm/packages/swarmd/internal/privacy"
	providerdiagnostics "swarm/packages/swarmd/internal/provider/diagnostics"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	responsesURL                               = "https://chatgpt.com/backend-api/codex/responses"
	openAIResponsesURL                         = "https://api.openai.com/v1/responses"
	openAIBetaHeader                           = "OpenAI-Beta"
	responsesWebsocketBetaHeaderV2             = "responses_websockets=2026-02-06"
	originatorHeader                           = "originator"
	defaultOriginatorHeaderValue               = "codex_cli_rs"
	userAgentHeader                            = "User-Agent"
	defaultCodexTransportUserAgent             = "codex_cli_rs/swarm-go"
	defaultOpenAITransportUserAgent            = "swarm-go/openai-responses"
	defaultCodexTextVerbosity                  = "low"
	includeReasoningEncryptedContentPath       = "reasoning.encrypted_content"
	chatGPTAccountIDHeader                     = "ChatGPT-Account-ID"
	tokenURL                                   = "https://auth.openai.com/oauth/token"
	clientID                                   = "app_EMoamEEZ73f0CkXaXp7hrann"
	maxCodexResponseBodyBytes            int64 = 32 << 20
	maxCodexStreamBytes                        = 8 << 20
	maxCodexStreamEvents                       = 16_384
	maxCodexRawEvents                          = 4_096
	transportRetryAttempts                     = 2
	transportRetryBaseDelay                    = 300 * time.Millisecond
	startedWebsocketStreamRetryLimit           = 3
	websocketIdleTimeoutAttempts               = 3
	defaultWebsocketIdleTimeout                = 5 * time.Minute
	websocketWriteTimeout                      = 30 * time.Second
	maxPromptCacheKeyLength                    = 64
	codexTransportMetadataKey                  = "_swarm_transport"
	codexConnectedViaWSMetadataKey             = "_swarm_connected_via_websocket"
	codexTransportWebsocket                    = "websocket"
	codexTransportResponsesHTTP                = "responses_http"
)

var (
	errWebsocketStreamStarted = errors.New("websocket stream interrupted after payload started")
	errWebsocketRetryFresh    = errors.New("websocket request requires a fresh connection")
	errWebsocketIdleTimeout   = errors.New("codex websocket stream idle timeout")
)

type Client struct {
	authStore            *pebblestore.AuthStore
	httpClient           *http.Client
	earlyExpiry          time.Duration
	sendWSFn             func(context.Context, pebblestore.CodexAuthRecord, []byte, func(StreamEvent)) (map[string]any, int, error)
	responsesAPIURL      string
	responsesWSURL       string
	websocketIdleTimeout time.Duration
	wsMu                 sync.Mutex
	wsSessions           map[string]*cachedWebsocketSession
	diagnostics          *longsessiondiag.Recorder
}

type startedWebsocketStreamError struct {
	cause error
}

type forceFreshProviderContextKey struct{}
type codexTransportContextKey struct{}

type codexTransportContext struct {
	PromptCacheKey            string
	SessionAffinityKey        string
	StartNewChain             bool
	AllowContinuation         bool
	ReuseTransport            bool
	ResetTransport            bool
	NativeContinuationAllowed bool
	ForceFreshProviderContext bool
	BoundaryReason            string
}

func contextWithForceFreshProviderContext(ctx context.Context, force bool) context.Context {
	if !force {
		return ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, forceFreshProviderContextKey{}, true)
}

func forceFreshProviderContextFromContext(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	value, _ := ctx.Value(forceFreshProviderContextKey{}).(bool)
	return value
}

func contextWithCodexTransportContext(ctx context.Context, transport codexTransportContext) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, codexTransportContextKey{}, transport)
}

func codexTransportContextFromContext(ctx context.Context) codexTransportContext {
	if ctx == nil {
		return codexTransportContext{}
	}
	transport, _ := ctx.Value(codexTransportContextKey{}).(codexTransportContext)
	return transport
}

type cachedWebsocketSession struct {
	mu                    sync.Mutex
	conn                  *websocket.Conn
	lastPayload           map[string]any
	lastRequestProperties map[string]any
	lastInputLen          int
	lastResponseID        string
	lastOutput            []any
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
		if event.Delta == "" {
			return
		}
		e.attemptOutputText += event.Delta
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
	SessionID                     string
	ProviderLineageID             string
	ContextBranchID               string
	ProviderConfigurationHash     string
	ProviderCacheKey              string
	SessionAffinityKey            string
	TransportAffinityKey          string
	BoundaryReason                string
	PreviousProviderLineageID     string
	PreviousProviderID            string
	PreviousModel                 string
	NewProviderID                 string
	NewModel                      string
	HandoffSummaryMessageID       string
	HandoffSummaryGlobalSeq       uint64
	ProviderLineageStartMessageID string
	ProviderLineageStartRunID     string
	ProviderLineageStartGlobalSeq uint64
	StartNewChain                 bool
	AllowContinuation             bool
	ReuseTransport                bool
	ResetTransport                bool
	NativeContinuationAllowed     bool
	ForceFreshProviderContext     bool
	Model                         string
	Thinking                      string
	ReasoningProviderValue        string
	Instructions                  string
	Input                         []map[string]any
	Tools                         []ToolDefinition
	ToolChoice                    string
	ServiceTier                   string
	ContextMode                   string
	ContextWindow                 int
	MediaContract                 provideriface.SessionMediaContract
	ParallelToolCalls             bool
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
	ServiceTier      string           `json:"service_tier,omitempty"`
	EstimatedCostUSD float64          `json:"estimated_cost_usd,omitempty"`
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
	Raw              map[string]any     `json:"raw,omitempty"`
}

type AssistantMessage struct {
	Text  string
	Phase provideriface.AssistantPhase
}

type StreamEventType string

const (
	StreamEventOutputTextDelta              StreamEventType = "response.output_text.delta"
	StreamEventReasoningSummaryDelta        StreamEventType = "response.reasoning_summary_text.delta"
	StreamEventAssistantCommentary          StreamEventType = "response.assistant_commentary.delta"
	StreamEventToolCallStarted              StreamEventType = "response.tool_call.started"
	StreamEventToolCallArgumentsDelta       StreamEventType = "response.tool_call.arguments.delta"
	StreamEventToolCallArgumentsSnapshot    StreamEventType = "response.tool_call.arguments.snapshot"
	StreamEventToolCallCompleted            StreamEventType = "response.tool_call.completed"
	StreamEventImageGenerationPartialImage  StreamEventType = "response.image_generation_call.partial_image"
	StreamEventImageGenerationCallCompleted StreamEventType = "response.image_generation_call.completed"
)

type StreamEvent struct {
	Type              StreamEventType
	Delta             string
	Phase             provideriface.AssistantPhase
	ReasoningKey      string
	ToolCallID        string
	ToolCallIndex     *int
	ToolName          string
	Arguments         string
	ArgumentsDelta    string
	ArgumentsSnapshot string
	Metadata          map[string]any
	ItemID            string
	OutputIndex       int
	SequenceNumber    int
	PartialImageIndex int
	PartialImageB64   string
	OutputFormat      string
	Size              string
	Quality           string
	Background        string
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
		httpClient:           &http.Client{},
		earlyExpiry:          5 * time.Minute,
		responsesAPIURL:      openAIResponsesURL,
		websocketIdleTimeout: defaultWebsocketIdleTimeout,
		wsSessions:           make(map[string]*cachedWebsocketSession),
	}
}

// SetLongSessionDiagnostics enables metadata-only cache snapshots. A nil
// recorder restores the zero-overhead disabled path.
func (c *Client) SetLongSessionDiagnostics(recorder *longsessiondiag.Recorder) {
	if c != nil {
		c.diagnostics = recorder
	}
}

func (c *Client) LongSessionSnapshot() map[string]any {
	if c == nil || c.diagnostics == nil {
		return nil
	}
	type cacheEntry struct {
		id      string
		session *cachedWebsocketSession
	}
	c.wsMu.Lock()
	totalEntries := len(c.wsSessions)
	ids := make([]string, 0, totalEntries)
	for id, session := range c.wsSessions {
		if session != nil {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	if len(ids) > 64 {
		ids = ids[:64]
	}
	entries := make([]cacheEntry, 0, len(ids))
	for _, id := range ids {
		entries = append(entries, cacheEntry{id: id, session: c.wsSessions[id]})
	}
	c.wsMu.Unlock()
	var open, payloadBytes, requestBytes, outputBytes, inputItems int64
	detail := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		entry.session.mu.Lock()
		entryOpen := entry.session.conn != nil
		entryPayloadBytes := estimatedJSONBytes(entry.session.lastPayload)
		entryRequestBytes := estimatedJSONBytes(entry.session.lastRequestProperties)
		entryOutputBytes := estimatedJSONBytes(entry.session.lastOutput)
		entryInputItems := int64(entry.session.lastInputLen)
		entry.session.mu.Unlock()
		if entryOpen {
			open++
		}
		payloadBytes += entryPayloadBytes
		requestBytes += entryRequestBytes
		outputBytes += entryOutputBytes
		inputItems += entryInputItems
		detail = append(detail, map[string]any{"session_hash": c.diagnostics.HashIdentifier(entry.id), "open": entryOpen, "last_payload_bytes": entryPayloadBytes, "request_properties_bytes": entryRequestBytes, "last_output_bytes": entryOutputBytes, "last_input_items": entryInputItems})
	}
	return map[string]any{"cache_entries": totalEntries, "sampled_entries": len(entries), "omitted_entries": totalEntries - len(entries), "open_connections": open, "last_payload_bytes": payloadBytes, "request_properties_bytes": requestBytes, "last_output_bytes": outputBytes, "last_input_items": inputItems, "entries": detail}
}

func estimatedJSONBytes(value any) int64 {
	const limit = int64(64 << 20)
	var walk func(any, int) int64
	walk = func(current any, depth int) int64 {
		if current == nil || depth > 64 {
			return 0
		}
		switch typed := current.(type) {
		case string:
			return min(int64(len(typed)), limit)
		case []byte:
			return min(int64(len(typed)), limit)
		case json.RawMessage:
			return min(int64(len(typed)), limit)
		case []any:
			var total int64
			for _, item := range typed {
				total += walk(item, depth+1)
				if total >= limit {
					return limit
				}
			}
			return total
		case []map[string]any:
			var total int64
			for _, item := range typed {
				total += walk(item, depth+1)
				if total >= limit {
					return limit
				}
			}
			return total
		case map[string]any:
			var total int64
			for key, item := range typed {
				total += int64(len(key)) + walk(item, depth+1)
				if total >= limit {
					return limit
				}
			}
			return total
		default:
			return 8
		}
	}
	return walk(value, 0)
}

func (c *Client) effectiveWebsocketIdleTimeout() time.Duration {
	if c != nil && c.websocketIdleTimeout > 0 {
		return c.websocketIdleTimeout
	}
	return defaultWebsocketIdleTimeout
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

func resetCachedWebsocketChainLocked(session *cachedWebsocketSession) {
	if session == nil {
		return
	}
	session.lastPayload = nil
	session.lastRequestProperties = nil
	session.lastInputLen = 0
	session.lastResponseID = ""
	session.lastOutput = nil
}

func closeCachedWebsocketSessionLocked(session *cachedWebsocketSession) {
	if session == nil {
		return
	}
	if session.conn != nil {
		pebblestore.ObserveExecutionEpochSocketReset()
		_ = session.conn.Close()
		session.conn = nil
	}
	resetCachedWebsocketChainLocked(session)
}

func prepareCachedWebsocketSessionLocked(session *cachedWebsocketSession, transport codexTransportContext, freshContext, payloadPrepared bool, requestPayload map[string]any) (*websocket.Conn, bool, error) {
	if session == nil {
		return nil, false, nil
	}
	if transport.ResetTransport || (freshContext && !transport.ReuseTransport) {
		closeCachedWebsocketSessionLocked(session)
		if payloadPrepared && asString(requestPayload["previous_response_id"]) != "" {
			return nil, false, errWebsocketRetryFresh
		}
	} else if freshContext {
		// A new provider chain must not inherit response lineage. Keep the
		// compatible healthy socket, but clear only chain-scoped cache state.
		resetCachedWebsocketChainLocked(session)
	}
	if session.conn != nil {
		pebblestore.ObserveExecutionEpochSocketReuse()
	}
	return session.conn, session.conn != nil, nil
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

func (c *Client) CreateResponseWithAuth(ctx context.Context, record pebblestore.CodexAuthRecord, req Request) (Response, error) {
	return c.createResponseWithAuth(ctx, record, req, nil)
}

func (c *Client) CreateResponseStreaming(ctx context.Context, req Request, onEvent func(StreamEvent)) (Response, error) {
	return c.createResponse(ctx, req, onEvent)
}

func (c *Client) CreateResponseStreamingWithAuth(ctx context.Context, record pebblestore.CodexAuthRecord, req Request, onEvent func(StreamEvent)) (Response, error) {
	return c.createResponseWithAuth(ctx, record, req, onEvent)
}

func (c *Client) createResponse(ctx context.Context, req Request, onEvent func(StreamEvent)) (Response, error) {
	record, err := c.ensureAuth(ctx)
	if err != nil {
		return Response{}, err
	}
	return c.createResponseWithAuth(ctx, record, req, onEvent)
}

func (c *Client) createResponseWithAuth(ctx context.Context, record pebblestore.CodexAuthRecord, req Request, onEvent func(StreamEvent)) (Response, error) {
	if strings.TrimSpace(record.Provider) == "" {
		record.Provider = "codex"
	}
	ctx = c.contextWithResponsesLifecycle(ctx, record, req, false)
	decoded, statusCode, err := c.sendRequest(ctx, record, req, onEvent)
	if err != nil {
		return Response{}, err
	}
	if statusCode == http.StatusUnauthorized && record.Type == pebblestore.CodexAuthTypeOAuth {
		refreshed, refreshErr := c.refreshOAuth(ctx, record.RefreshToken)
		if refreshErr != nil {
			return Response{}, fmt.Errorf("codex request unauthorized and refresh failed: %w", refreshErr)
		}
		accountID := extractAccountIDFromToken(refreshed.AccessToken)
		record, err = c.authStore.UpdateOAuthCredentialForAccount(record.AccountScopeID, record.Provider, record.ID, refreshed.AccessToken, refreshed.RefreshToken, refreshed.ExpiresAt, accountID)
		if err != nil {
			return Response{}, fmt.Errorf("persist refreshed codex oauth: %w", err)
		}
		// The refreshed credential must never continue on the socket that rejected
		// its predecessor. Rebuild compatibility identity and force a redial while
		// preserving the request's fresh/full-input policy.
		ctx = c.contextWithResponsesLifecycle(ctx, record, req, true)
		decoded, statusCode, err = c.sendRequest(ctx, record, req, onEvent)
		if err != nil {
			return Response{}, err
		}
	}

	if statusCode >= 400 {
		providerLabel := "codex"
		if strings.EqualFold(strings.TrimSpace(record.Provider), "openai") || record.Type != pebblestore.CodexAuthTypeOAuth {
			providerLabel = "openai"
		}
		if transport, _ := extractCodexTransportMetadata(decoded); transport != "" {
			return Response{}, fmt.Errorf("%s responses request failed status=%d transport=%s body=%s", providerLabel, statusCode, transport, compactBody(decoded))
		}
		return Response{}, fmt.Errorf("%s responses request failed status=%d body=%s", providerLabel, statusCode, compactBody(decoded))
	}

	return parseResponse(decoded), nil
}

func (c *Client) contextWithResponsesLifecycle(ctx context.Context, record pebblestore.CodexAuthRecord, req Request, resetTransport bool) context.Context {
	allowContinuation := req.AllowContinuation || req.NativeContinuationAllowed
	startNewChain := req.StartNewChain || req.ForceFreshProviderContext || !allowContinuation
	forceFreshProviderContext := startNewChain
	ctx = contextWithForceFreshProviderContext(ctx, forceFreshProviderContext)
	return contextWithCodexTransportContext(ctx, codexTransportContext{
		PromptCacheKey:            codexPromptCacheKey(firstNonEmpty(req.ProviderCacheKey, req.ProviderLineageID)),
		SessionAffinityKey:        c.responsesTransportCompatibilityKey(record, req),
		StartNewChain:             startNewChain,
		AllowContinuation:         allowContinuation,
		ReuseTransport:            req.ReuseTransport,
		ResetTransport:            resetTransport || req.ResetTransport,
		NativeContinuationAllowed: req.NativeContinuationAllowed,
		ForceFreshProviderContext: forceFreshProviderContext,
		BoundaryReason:            strings.TrimSpace(req.BoundaryReason),
	})
}

func (c *Client) responsesTransportCompatibilityKey(record pebblestore.CodexAuthRecord, req Request) string {
	providerID := strings.ToLower(strings.TrimSpace(record.Provider))
	if providerID == "" {
		providerID = "codex"
	}
	credentialVersion := ""
	if record.Type != pebblestore.CodexAuthTypeOAuth {
		// API-key rotation under a stable credential record must not retain a
		// socket authenticated by the old key. Only the digest enters the key.
		sum := sha256.Sum256([]byte(strings.TrimSpace(record.APIKey)))
		credentialVersion = fmt.Sprintf("%x", sum[:8])
	}
	return codexSessionAffinityKey(provideriface.ShortProviderLineageKey(
		"responses.websocket.transport.v1",
		firstNonEmpty(req.TransportAffinityKey, req.SessionAffinityKey, req.ProviderLineageID),
		providerID,
		strings.TrimSpace(req.Model),
		strings.TrimSpace(record.Type),
		strings.TrimSpace(record.AccountScopeID),
		strings.TrimSpace(record.ID),
		strings.TrimSpace(record.AccountID),
		credentialVersion,
		c.responsesEndpointForRecord(record),
	))
}

func (c *Client) responsesEndpointForRecord(record pebblestore.CodexAuthRecord) string {
	if c != nil && strings.TrimSpace(c.responsesWSURL) != "" {
		return strings.TrimSpace(c.responsesWSURL)
	}
	if record.Type == pebblestore.CodexAuthTypeOAuth {
		return responsesURL
	}
	if c != nil && strings.TrimSpace(c.responsesAPIURL) != "" {
		return strings.TrimSpace(c.responsesAPIURL)
	}
	return openAIResponsesURL
}

func (c *Client) ensureAuth(ctx context.Context) (pebblestore.CodexAuthRecord, error) {
	principal, principalOK := identity.PrincipalFromContext(ctx)
	if !principalOK || !principal.Valid() {
		return pebblestore.CodexAuthRecord{}, identity.ErrPrincipalRequired
	}
	record, ok, err := c.authStore.GetCodexAuthRecordForAccount(principal.AccountScopeID)
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
			record, err = c.authStore.UpdateOAuthCredentialForAccount(record.AccountScopeID, record.Provider, record.ID, refreshed.AccessToken, refreshed.RefreshToken, refreshed.ExpiresAt, accountID)
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
			updated, err := c.authStore.UpdateOAuthCredentialForAccount(record.AccountScopeID, record.Provider, record.ID, record.AccessToken, record.RefreshToken, record.ExpiresAt, accountID)
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
	providerdiagnostics.LogRequest("codex", "oauth.refresh", httpReq, []byte(values.Encode()))
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		providerdiagnostics.LogErrorContext(ctx, "codex", "oauth.refresh", err)
		return oauthTokens{}, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	providerdiagnostics.LogResponse("codex", "oauth.refresh", resp, body)
	if err != nil {
		providerdiagnostics.LogErrorContext(ctx, "codex", "oauth.refresh", err)
		return oauthTokens{}, err
	}
	if resp.StatusCode >= 400 {
		return oauthTokens{}, fmt.Errorf("oauth refresh failed status=%d", resp.StatusCode)
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
	payload, _, err := buildRequestPayloadWithOptions(req)
	return payload, err
}

func buildRequestPayloadWithOptions(req Request) ([]byte, bool, error) {
	body, err := buildCodexRequestBody(req, req.Input)
	if err != nil {
		return nil, false, err
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, false, err
	}
	return payload, req.ForceFreshProviderContext || !req.NativeContinuationAllowed, nil
}

func buildCodexRequestBody(req Request, input []map[string]any) (map[string]any, error) {
	properties, err := buildCodexRequestProperties(req)
	if err != nil {
		return nil, err
	}
	if len(input) == 0 {
		return nil, errors.New("input messages are required")
	}
	normalizedInput, err := materializeSessionMediaInput(req, input)
	if err != nil {
		return nil, err
	}
	body := cloneMapAny(properties)
	body["input"] = sanitizeCodexRequestInput(normalizedInput)
	return body, nil
}

func buildCodexRequestProperties(req Request) (map[string]any, error) {
	modelID := strings.TrimSpace(req.Model)
	if modelID == "" {
		return nil, errors.New("model is required")
	}
	properties := map[string]any{
		"model":  modelID,
		"stream": true,
		"store":  false,
		"text": map[string]any{
			"verbosity": defaultCodexTextVerbosity,
		},
	}
	if cacheKey := codexPromptCacheKey(firstNonEmpty(req.ProviderCacheKey, req.ProviderLineageID)); cacheKey != "" {
		properties["prompt_cache_key"] = cacheKey
	}
	if strings.TrimSpace(req.Instructions) != "" {
		properties["instructions"] = strings.TrimSpace(req.Instructions)
	}
	if len(req.Tools) > 0 {
		properties["tools"] = normalizeCodexRequestTools(req.Tools)
		toolChoice := strings.TrimSpace(req.ToolChoice)
		if toolChoice == "" {
			toolChoice = "auto"
		}
		properties["tool_choice"] = toolChoice
		properties["parallel_tool_calls"] = req.ParallelToolCalls
	}
	if serviceTier := strings.TrimSpace(req.ServiceTier); serviceTier != "" {
		properties["service_tier"] = serviceTier
	}
	if reasoning := reasoningPayloadForRequest(req); len(reasoning) > 0 {
		properties["reasoning"] = reasoning
		properties["include"] = []string{includeReasoningEncryptedContentPath}
	}
	return properties, nil
}

func codexPromptCacheKey(sessionID string) string {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return ""
	}
	if len(sessionID) <= maxPromptCacheKeyLength {
		return sessionID
	}
	sum := sha256.Sum256([]byte(sessionID))
	return fmt.Sprintf("%x", sum)
}

func codexSessionAffinityKey(sessionID string) string {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return ""
	}
	if len(sessionID) <= maxPromptCacheKeyLength {
		return sessionID
	}
	sum := sha256.Sum256([]byte(sessionID))
	return fmt.Sprintf("%x", sum)
}

func normalizeCodexRequestTools(tools []ToolDefinition) []ToolDefinition {
	out := make([]ToolDefinition, 0, len(tools))
	for _, tool := range tools {
		tool.Parameters = sanitizeCodexToolParameters(tool.Parameters)
		out = append(out, tool)
	}
	return out
}

func materializeSessionMediaInput(req Request, input []map[string]any) ([]map[string]any, error) {
	if len(input) == 0 {
		return nil, nil
	}
	out := make([]map[string]any, 0, len(input))
	mediaCounts := map[string]int{}
	for _, item := range input {
		cloned := cloneMapAny(item)
		content, ok := inputContentMaps(cloned["content"])
		if !ok {
			out = append(out, cloned)
			continue
		}
		materialized := make([]map[string]any, 0, len(content))
		for _, part := range content {
			if !strings.EqualFold(strings.TrimSpace(asString(part["type"])), "session_media") {
				materialized = append(materialized, cloneMapAny(part))
				continue
			}
			payload, ok := part["media"].(provideriface.SessionMediaPayload)
			if !ok {
				return nil, errors.New("provider media input is malformed")
			}
			capability, err := validateProviderMediaPayload(req, payload, mediaCounts)
			if err != nil {
				return nil, err
			}
			transport, err := providerMediaContentItem(req.MediaContract, capability, payload)
			if err != nil {
				return nil, err
			}
			materialized = append(materialized, transport)
		}
		cloned["content"] = materialized
		out = append(out, cloned)
	}
	return out, nil
}

func inputContentMaps(value any) ([]map[string]any, bool) {
	switch typed := value.(type) {
	case []map[string]any:
		return typed, true
	case []any:
		out := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			mapped, ok := item.(map[string]any)
			if !ok {
				return nil, false
			}
			out = append(out, mapped)
		}
		return out, true
	default:
		return nil, false
	}
}

func validateProviderMediaPayload(req Request, payload provideriface.SessionMediaPayload, counts map[string]int) (provideriface.MediaContractCapability, error) {
	contract := req.MediaContract
	providerID := strings.ToLower(strings.TrimSpace(contract.ProviderID))
	if contract.Hash == "" || req.ProviderConfigurationHash == "" || (providerID != "openai" && providerID != "codex") {
		return provideriface.MediaContractCapability{}, errors.New("provider media contract is unavailable or outside the pilot")
	}
	if providerID == "openai" {
		if contract.ProviderSurface != provideriface.MediaProviderSurfaceOpenAIResponses || contract.CredentialSurface != provideriface.MediaCredentialSurfaceOpenAIAPIKey || contract.AdapterID != provideriface.MediaAdapterIDOpenAIResponsesV1 {
			return provideriface.MediaContractCapability{}, errors.New("OpenAI media payload does not match the active API-key Responses surface")
		}
	} else if contract.ProviderSurface != provideriface.MediaProviderSurfaceCodexChatGPT || contract.CredentialSurface != provideriface.MediaCredentialSurfaceCodexOAuth || contract.AdapterID != provideriface.MediaAdapterIDCodexChatGPTV1 {
		return provideriface.MediaContractCapability{}, errors.New("Codex media payload does not match the active OAuth client surface")
	}
	if len(payload.Bytes) == 0 || payload.Size <= 0 || int64(len(payload.Bytes)) != payload.Size || strings.TrimSpace(payload.AssetID) == "" || strings.TrimSpace(payload.DigestSHA256) == "" {
		return provideriface.MediaContractCapability{}, errors.New("provider media payload failed immutable size or identity validation")
	}
	digest := sha256.Sum256(payload.Bytes)
	if !strings.EqualFold(hex.EncodeToString(digest[:]), strings.TrimSpace(payload.DigestSHA256)) {
		return provideriface.MediaContractCapability{}, errors.New("provider media payload failed immutable digest validation")
	}
	for _, capability := range contract.Capabilities {
		if capability.State != provideriface.MediaCapabilityStateAllowed || !strings.EqualFold(capability.Modality, payload.Modality) {
			continue
		}
		if !mediaStringAllowed(capability.MIMETypes, payload.MIMEType) || len(capability.FileTypes) > 0 && !mediaStringAllowed(capability.FileTypes, payload.FileType) || capability.MaxBytes <= 0 || payload.Size > capability.MaxBytes {
			break
		}
		counts[capability.Modality]++
		if capability.MaxCount <= 0 || counts[capability.Modality] > capability.MaxCount {
			return provideriface.MediaContractCapability{}, errors.New("provider media payload exceeds the current contract count limit")
		}
		return capability, nil
	}
	return provideriface.MediaContractCapability{}, errors.New("provider media payload is denied by the active contract")
}

func mediaStringAllowed(allowed []string, value string) bool {
	value = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(value), "."))
	if len(allowed) == 0 {
		return value == ""
	}
	for _, candidate := range allowed {
		if strings.EqualFold(strings.TrimPrefix(strings.TrimSpace(candidate), "."), value) {
			return true
		}
	}
	return false
}

func providerMediaContentItem(contract provideriface.SessionMediaContract, capability provideriface.MediaContractCapability, payload provideriface.SessionMediaPayload) (map[string]any, error) {
	contentType := ""
	for _, candidate := range capability.ContentTypes {
		if candidate == "input_image" || candidate == "input_file" {
			contentType = candidate
			break
		}
	}
	encoded := base64.StdEncoding.EncodeToString(payload.Bytes)
	switch contentType {
	case "input_image":
		return map[string]any{"type": "input_image", "image_url": "data:" + payload.MIMEType + ";base64," + encoded}, nil
	case "input_file":
		if contract.ProviderID != "openai" || capability.Semantics != pebblestore.ModelCatalogMediaSemanticsProviderProcessed {
			return nil, errors.New("client-processed Codex file attachments are not implemented")
		}
		extension := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(payload.FileType)), ".")
		if extension == "" || strings.ContainsAny(extension, `/\\`) {
			extension = "bin"
		}
		return map[string]any{"type": "input_file", "filename": "asset." + extension, "file_data": "data:" + payload.MIMEType + ";base64," + encoded}, nil
	default:
		return nil, errors.New("provider media contract has no implemented content type")
	}
}

func sanitizeCodexRequestInput(input []map[string]any) []map[string]any {
	if len(input) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(input))
	for _, item := range input {
		sanitized, ok := sanitizeCodexRequestInputValue(item).(map[string]any)
		if !ok || len(sanitized) == 0 {
			continue
		}
		out = append(out, sanitized)
	}
	return out
}

func sanitizeCodexRequestInputValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		itemType := strings.ToLower(strings.TrimSpace(fmt.Sprint(typed["type"])))
		for key, child := range typed {
			if isCodexRequestInputResponseOnlyField(key) {
				continue
			}
			sanitized := sanitizeCodexRequestInputValue(child)
			if itemType == "reasoning" && strings.EqualFold(strings.TrimSpace(key), "content") {
				if isCodexRequestInputEmptyValue(sanitized) {
					out[key] = nil
				} else {
					out[key] = sanitized
				}
				continue
			}
			if isCodexRequestInputEmptyValue(sanitized) {
				continue
			}
			out[key] = sanitized
		}
		if itemType == "reasoning" {
			normalizeCodexReasoningReplayItem(out)
		}
		return out
	case []any:
		out := make([]any, 0, len(typed))
		for _, child := range typed {
			sanitized := sanitizeCodexRequestInputValue(child)
			if isCodexRequestInputEmptyValue(sanitized) {
				continue
			}
			out = append(out, sanitized)
		}
		if len(out) == 0 {
			return nil
		}
		return out
	case []map[string]any:
		out := make([]any, 0, len(typed))
		for _, child := range typed {
			sanitized := sanitizeCodexRequestInputValue(child)
			if isCodexRequestInputEmptyValue(sanitized) {
				continue
			}
			out = append(out, sanitized)
		}
		if len(out) == 0 {
			return nil
		}
		return out
	default:
		return value
	}
}

func normalizeCodexReasoningReplayItem(item map[string]any) {
	if len(item) == 0 {
		return
	}
	summary, ok := item["summary"]
	if !ok || !isCodexRequestInputArrayValue(summary) {
		item["summary"] = []any{}
	}
	content, ok := item["content"]
	if !ok || !isCodexRequestInputArrayValue(content) || isCodexRequestInputEmptyValue(content) {
		item["content"] = nil
	}
}

func isCodexRequestInputArrayValue(value any) bool {
	switch value.(type) {
	case []any, []map[string]any:
		return true
	default:
		return false
	}
}

func isCodexRequestInputResponseOnlyField(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "", "metadata", "output_index", "phase", "logprobs", "annotations":
		return true
	default:
		return false
	}
}

func sanitizeCodexRequestDeltaInput(input []map[string]any) []any {
	if len(input) == 0 {
		return []any{}
	}
	out := make([]any, 0, len(input))
	for _, item := range sanitizeCodexRequestInput(input) {
		out = append(out, item)
	}
	return out
}

func isCodexRequestInputEmptyValue(value any) bool {
	switch typed := value.(type) {
	case nil:
		return true
	case []any:
		return len(typed) == 0
	case []map[string]any:
		return len(typed) == 0
	case map[string]any:
		return len(typed) == 0
	default:
		return false
	}
}

func reasoningPayloadForRequest(req Request) map[string]any {
	providerValue := strings.ToLower(strings.TrimSpace(req.ReasoningProviderValue))
	if providerValue == "" || providerValue == "none" {
		return nil
	}
	return map[string]any{"effort": providerValue, "summary": "auto"}
}

func (c *Client) sendRequest(ctx context.Context, record pebblestore.CodexAuthRecord, req Request, onEvent func(StreamEvent)) (decoded map[string]any, status int, err error) {
	started := time.Now()
	defer func() {
		if c.diagnostics == nil {
			return
		}
		failed := err != nil || status >= http.StatusBadRequest
		c.diagnostics.ObserveOperation("codex.api_total", req.SessionID, time.Since(started), failed, map[string]int64{"input_items": int64(len(req.Input)), "tools": int64(len(req.Tools)), "status_class": int64(status / 100)}, map[string]bool{"continuation": req.AllowContinuation, "reset_transport": req.ResetTransport, "force_fresh": req.ForceFreshProviderContext})
	}()
	if c.sendWSFn != nil {
		payload, _, err := buildRequestPayloadWithOptions(req)
		if err != nil {
			return nil, 0, err
		}
		return c.send(ctx, record, payload, onEvent)
	}

	streamEmitter := &retryAwareStreamEmitter{onEvent: onEvent}
	for attempt := 1; attempt <= transportRetryAttempts; attempt++ {
		var wsDecoded map[string]any
		var wsStatus int
		var wsErr error
		idleTimeouts := 0
		for startedRetry := 0; ; startedRetry++ {
			streamEmitter.beginAttempt()
			wsDecoded, wsStatus, wsErr = c.sendWebsocketRequest(ctx, record, req, streamEmitter.emit)
			if wsErr == nil {
				break
			}
			if errors.Is(wsErr, errWebsocketIdleTimeout) {
				idleTimeouts++
				if idleTimeouts >= websocketIdleTimeoutAttempts {
					return nil, 0, exhaustedWebsocketIdleTimeoutError(wsErr, idleTimeouts)
				}
				if err := sleepWithContext(ctx, transportRetryBaseDelay*time.Duration(idleTimeouts)); err != nil {
					return nil, 0, err
				}
				startedRetry--
				continue
			}
			if !errors.Is(wsErr, errWebsocketStreamStarted) {
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
			if record.Type != pebblestore.CodexAuthTypeOAuth && !errors.Is(wsErr, errWebsocketStreamStarted) {
				return c.sendOpenAIResponsesRequest(ctx, record, req, streamEmitter.emit)
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
		if record.Type != pebblestore.CodexAuthTypeOAuth && openAIWebsocketShouldFallbackHTTP(wsStatus) {
			return c.sendOpenAIResponsesRequest(ctx, record, req, streamEmitter.emit)
		}
		return annotateRetryAttempts(annotateCodexTransportMetadata(wsDecoded, codexTransportWebsocket, true), attempt), wsStatus, nil
	}
	if record.Type != pebblestore.CodexAuthTypeOAuth {
		return c.sendOpenAIResponsesRequest(ctx, record, req, streamEmitter.emit)
	}
	return map[string]any{
		"raw_body":       "",
		"retry_attempts": transportRetryAttempts,
	}, http.StatusServiceUnavailable, nil
}

func (c *Client) send(ctx context.Context, record pebblestore.CodexAuthRecord, payload []byte, onEvent func(StreamEvent)) (map[string]any, int, error) {
	if record.Type != pebblestore.CodexAuthTypeOAuth {
		return c.sendOpenAIResponses(ctx, record, payload, onEvent)
	}

	sendWS := c.sendWSFn
	if sendWS == nil {
		sendWS = c.sendWebsocket
	}

	streamEmitter := &retryAwareStreamEmitter{onEvent: onEvent}

	for attempt := 1; attempt <= transportRetryAttempts; attempt++ {
		var wsDecoded map[string]any
		var wsStatus int
		var wsErr error
		idleTimeouts := 0
		for startedRetry := 0; ; startedRetry++ {
			streamEmitter.beginAttempt()
			wsDecoded, wsStatus, wsErr = sendWS(ctx, record, payload, streamEmitter.emit)
			if wsErr == nil {
				break
			}
			if errors.Is(wsErr, errWebsocketIdleTimeout) {
				idleTimeouts++
				if idleTimeouts >= websocketIdleTimeoutAttempts {
					return nil, 0, exhaustedWebsocketIdleTimeoutError(wsErr, idleTimeouts)
				}
				if err := sleepWithContext(ctx, transportRetryBaseDelay*time.Duration(idleTimeouts)); err != nil {
					return nil, 0, err
				}
				startedRetry--
				continue
			}
			if !errors.Is(wsErr, errWebsocketStreamStarted) {
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

func openAIWebsocketShouldFallbackHTTP(statusCode int) bool {
	if statusCode == 0 {
		return true
	}
	switch statusCode {
	case http.StatusBadRequest, http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusUpgradeRequired, http.StatusNotImplemented:
		return true
	default:
		return false
	}
}

func (c *Client) sendOpenAIResponsesRequest(ctx context.Context, record pebblestore.CodexAuthRecord, req Request, onEvent func(StreamEvent)) (map[string]any, int, error) {
	payload, _, err := buildRequestPayloadWithOptions(req)
	if err != nil {
		return nil, 0, err
	}
	return c.sendOpenAIResponses(ctx, record, payload, onEvent)
}

func (c *Client) sendOpenAIResponses(ctx context.Context, record pebblestore.CodexAuthRecord, payload []byte, onEvent func(StreamEvent)) (map[string]any, int, error) {
	apiURL := strings.TrimSpace(c.responsesAPIURL)
	if apiURL == "" {
		apiURL = openAIResponsesURL
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(payload))
	if err != nil {
		return nil, 0, err
	}
	providerID := strings.ToLower(strings.TrimSpace(record.Provider))
	if providerID == "" {
		providerID = "codex"
	}
	diagnosticProvider := providerID
	userAgent := defaultCodexTransportUserAgent
	if providerID == "openai" || record.Type != pebblestore.CodexAuthTypeOAuth {
		diagnosticProvider = "openai"
		userAgent = defaultOpenAITransportUserAgent
	}
	httpReq.Header.Set("Authorization", "Bearer "+bearerToken(record))
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set(userAgentHeader, userAgent)

	providerdiagnostics.LogRequest(diagnosticProvider, "responses.http", httpReq, payload)
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		providerdiagnostics.LogErrorContext(ctx, diagnosticProvider, "responses.http", err)
		return nil, 0, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxCodexResponseBodyBytes))
	providerdiagnostics.LogResponse(diagnosticProvider, "responses.http", resp, body)
	if err != nil {
		providerdiagnostics.LogErrorContext(ctx, diagnosticProvider, "responses.http", err)
		return nil, resp.StatusCode, err
	}
	if resp.StatusCode >= 400 {
		return annotateCodexTransportMetadata(map[string]any{
			"raw_body": sanitizeDiagnosticText(string(body)),
		}, codexTransportResponsesHTTP, false), resp.StatusCode, nil
	}

	decoded, err := parseOpenAIResponsesBody(body, resp.Header.Get("Content-Type"), onEvent)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return annotateCodexTransportMetadata(decoded, codexTransportResponsesHTTP, false), resp.StatusCode, nil
}

func (c *Client) VerifyOpenAIAPIKey(ctx context.Context, apiKey string) (provideriface.AuthVerification, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return provideriface.AuthVerification{Connected: false, Method: "api"}, errors.New("openai api verification requires api_key")
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.openai.com/v1/models", nil)
	if err != nil {
		return provideriface.AuthVerification{Connected: false, Method: "api"}, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set(userAgentHeader, defaultOpenAITransportUserAgent)
	providerdiagnostics.LogRequest("openai", "models.verify", httpReq, nil)
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		providerdiagnostics.LogErrorContext(ctx, "openai", "models.verify", err)
		return provideriface.AuthVerification{Connected: false, Method: "api"}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxCodexResponseBodyBytes))
	providerdiagnostics.LogResponse("openai", "models.verify", resp, body)
	if err != nil {
		return provideriface.AuthVerification{Connected: false, Method: "api"}, err
	}
	if resp.StatusCode >= 400 {
		return provideriface.AuthVerification{Connected: false, Method: "api"}, fmt.Errorf("openai api verification failed status=%d body=%s", resp.StatusCode, sanitizeDiagnosticText(string(body)))
	}
	return provideriface.AuthVerification{
		Connected: true,
		Method:    "api",
		Message:   "OpenAI API key verified via /v1/models",
	}, nil
}

func parseOpenAIResponsesBody(body []byte, contentType string, onEvent func(StreamEvent)) (map[string]any, error) {
	if strings.Contains(strings.ToLower(contentType), "text/event-stream") || bytes.Contains(body, []byte("data:")) {
		return parseEventStreamWithCallback(body, onEvent)
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, fmt.Errorf("decode openai responses body: %w", err)
	}
	return decoded, nil
}

func parseEventStreamWithCallback(body []byte, onEvent func(StreamEvent)) (map[string]any, error) {
	return parseEventStreamReader(bytes.NewReader(body), onEvent)
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
		return closeErr.Code == websocket.CloseNormalClosure ||
			closeErr.Code == websocket.CloseAbnormalClosure ||
			(closeErr.Code == websocket.CloseInternalServerErr && strings.Contains(strings.ToLower(closeErr.Text), "keepalive ping timeout"))
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "close 1000") ||
		strings.Contains(message, "close 1006") ||
		strings.Contains(message, "unexpected eof") ||
		(strings.Contains(message, "close 1011") && strings.Contains(message, "keepalive ping timeout"))
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

func (c *Client) responsesWebsocketURL(record pebblestore.CodexAuthRecord) (string, error) {
	parsed, err := url.Parse(c.responsesEndpointForRecord(record))
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

func (c *Client) codexWebsocketRequestPayload(ctx context.Context, req Request) (map[string]any, map[string]any, int, error) {
	properties, err := buildCodexRequestProperties(req)
	if err != nil {
		return nil, nil, 0, err
	}
	if len(req.Input) == 0 {
		return nil, nil, 0, errors.New("input messages are required")
	}
	requestInputLen := len(req.Input)
	transportContext := codexTransportContextFromContext(ctx)
	freshContext := forceFreshProviderContextFromContext(ctx)
	session := c.cachedWebsocketSession(transportContext.SessionAffinityKey)
	if session == nil || freshContext || transportContext.ResetTransport {
		// A forced transport reset cannot safely use previous_response_id from the
		// socket being discarded. Reconnect with the complete selected input, as
		// required for store=false Responses websocket recovery.
		payload, err := buildCodexRequestBody(req, req.Input)
		return payload, properties, requestInputLen, err
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if strings.TrimSpace(session.lastResponseID) == "" || !codexWebsocketRequestPropertiesMatch(session.lastRequestProperties, properties) {
		payload, err := buildCodexRequestBody(req, req.Input)
		return payload, properties, requestInputLen, err
	}
	baselineOutputLen := len(session.lastOutput)
	baselineLen := session.lastInputLen + baselineOutputLen
	if baselineLen < 0 || baselineLen > len(req.Input) {
		payload, err := buildCodexRequestBody(req, req.Input)
		return payload, properties, requestInputLen, err
	}
	if baselineOutputLen > 0 && !inputStartsWith(codexInputItemsForIncrementalCompare(mapsToAnySlice(req.Input[session.lastInputLen:baselineLen])), codexInputItemsForIncrementalCompare(session.lastOutput)) {
		payload, err := buildCodexRequestBody(req, req.Input)
		return payload, properties, requestInputLen, err
	}
	payload := cloneMapAny(properties)
	payload["input"] = sanitizeCodexRequestDeltaInput(req.Input[baselineLen:])
	payload["previous_response_id"] = strings.TrimSpace(session.lastResponseID)
	return payload, properties, requestInputLen, nil
}

func (c *Client) sendWebsocket(ctx context.Context, record pebblestore.CodexAuthRecord, payload []byte, onEvent func(StreamEvent)) (map[string]any, int, error) {
	requestPayload, err := decodeCodexPayload(payload)
	if err != nil {
		return nil, 0, err
	}
	return c.sendWebsocketMap(ctx, record, requestPayload, codexWebsocketRequestPropertiesFromPayload(requestPayload), len(asSlice(requestPayload["input"])), false, onEvent)
}

func (c *Client) sendWebsocketRequest(ctx context.Context, record pebblestore.CodexAuthRecord, req Request, onEvent func(StreamEvent)) (map[string]any, int, error) {
	requestPayload, requestProperties, requestInputLen, err := c.codexWebsocketRequestPayload(ctx, req)
	if err != nil {
		return nil, 0, err
	}
	return c.sendWebsocketMap(ctx, record, requestPayload, requestProperties, requestInputLen, true, onEvent)
}

func (c *Client) sendWebsocketMap(ctx context.Context, record pebblestore.CodexAuthRecord, requestPayload map[string]any, requestProperties map[string]any, requestInputLen int, payloadPrepared bool, onEvent func(StreamEvent)) (map[string]any, int, error) {
	wsURL, err := c.responsesWebsocketURL(record)
	if err != nil {
		return nil, 0, err
	}

	transportContext := codexTransportContextFromContext(ctx)
	cacheKey := strings.TrimSpace(transportContext.SessionAffinityKey)
	freshContext := forceFreshProviderContextFromContext(ctx)
	headers := buildCodexTransportHeaders(record, transportContext)
	session := c.cachedWebsocketSession(cacheKey)
	if session != nil {
		session.mu.Lock()
		defer session.mu.Unlock()
	}
	conn, websocketReused, err := prepareCachedWebsocketSessionLocked(session, transportContext, freshContext, payloadPrepared, requestPayload)
	if err != nil {
		return nil, 0, err
	}
	initialConnection := !websocketReused
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

	sendPayload := requestPayload
	if !payloadPrepared {
		sendPayload = codexWebsocketSendPayload(requestPayload, session, freshContext)
	}
	websocketPayload, err := buildCodexWebsocketPayload(sendPayload)
	if err != nil {
		if session != nil {
			closeCachedWebsocketSessionLocked(session)
		}
		return nil, 0, err
	}

	idleTimeout := c.effectiveWebsocketIdleTimeout()
	writeMessage := func(activeConn *websocket.Conn, encoded []byte, initialConnection bool) error {
		if activeConn == nil {
			return errors.New("websocket connection is unavailable")
		}
		activeConn.SetReadLimit(maxCodexResponseBodyBytes)
		if initialConnection {
			if err := activeConn.SetWriteDeadline(time.Now().Add(websocketWriteTimeout)); err != nil {
				return fmt.Errorf("set initial websocket request deadline: %w", err)
			}
		}
		if err := activeConn.WriteMessage(websocket.TextMessage, encoded); err != nil {
			if ctxErr := contextErr(ctx); ctxErr != nil {
				return ctxErr
			}
			return fmt.Errorf("send websocket request: %w", err)
		}
		if initialConnection {
			if err := activeConn.SetWriteDeadline(time.Time{}); err != nil {
				return fmt.Errorf("clear initial websocket request deadline: %w", err)
			}
		}
		return nil
	}
	providerdiagnostics.RecordContext(ctx, codexOutboundShapeEvent("websocket_request_shape", transportContext, sendPayload, asString(sendPayload["previous_response_id"]) != "", websocketReused))
	providerdiagnostics.LogWebsocketRequestContext(ctx, "codex", "responses.websocket", wsURL, headers, websocketPayload)
	if err := writeMessage(conn, websocketPayload, initialConnection); err != nil {
		if ctxErr := contextErr(ctx); ctxErr != nil {
			if session != nil {
				closeCachedWebsocketSessionLocked(session)
			}
			return nil, 0, ctxErr
		}
		if session != nil {
			closeCachedWebsocketSessionLocked(session)
			if payloadPrepared && asString(requestPayload["previous_response_id"]) != "" {
				return nil, 0, errWebsocketRetryFresh
			}
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
			initialConnection = true
			sendPayload = cloneMapAny(requestPayload)
			websocketPayload, err = buildCodexWebsocketPayload(sendPayload)
			if err != nil {
				closeCachedWebsocketSessionLocked(session)
				return nil, 0, err
			}
			if retryErr := writeMessage(conn, websocketPayload, initialConnection); retryErr != nil {
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
		if err := conn.SetReadDeadline(time.Now().Add(idleTimeout)); err != nil {
			if session != nil {
				closeCachedWebsocketSessionLocked(session)
			}
			return nil, 0, fmt.Errorf("set websocket idle deadline: %w", err)
		}
		messageType, message, err := conn.ReadMessage()
		if err != nil {
			if session != nil {
				closeCachedWebsocketSessionLocked(session)
			}
			if ctxErr := contextErr(ctx); ctxErr != nil {
				return nil, 0, ctxErr
			}
			if websocketTimeoutError(err) {
				timeoutErr := fmt.Errorf("%w after %s", errWebsocketIdleTimeout, idleTimeout)
				providerdiagnostics.LogWebsocketErrorContext(ctx, "codex", "responses.websocket", timeoutErr)
				return nil, 0, timeoutErr
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
		providerdiagnostics.LogWebsocketResponseContext(ctx, "codex", "responses.websocket", message)
		var decoded map[string]any
		if len(message) > maxCodexStreamBytes {
			return nil, 0, errors.New("codex websocket event byte limit exceeded")
		}
		if err := json.Unmarshal(message, &decoded); err != nil {
			codexThinkingDebugEvent("event.decode_error", map[string]any{
				"tag":           "websocket",
				"payload_chars": len(payloadText),
				"error":         err.Error(),
			})
			return nil, 0, fmt.Errorf("decode codex websocket event: %w", err)
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
		if state.decodeErr != nil {
			return nil, 0, state.decodeErr
		}
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
		session.lastRequestProperties = cloneMapAny(requestProperties)
		session.lastInputLen = requestInputLen
		session.lastResponseID = extractResponseID(decoded)
		session.lastOutput = normalizeCodexResponseOutputMapsForReplay(state.outputItemsDone)
	}
	return decoded, http.StatusOK, nil
}

func websocketTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	type timeoutError interface {
		Timeout() bool
	}
	var timeout timeoutError
	return errors.As(err, &timeout) && timeout.Timeout()
}

func exhaustedWebsocketIdleTimeoutError(err error, attempts int) error {
	return fmt.Errorf("codex timed out %d times in a row: %w", attempts, err)
}

func dialCodexWebsocket(ctx context.Context, wsURL string, headers http.Header) (*websocket.Conn, map[string]any, int, error) {
	dialer := websocket.Dialer{
		Proxy:             http.ProxyFromEnvironment,
		HandshakeTimeout:  30 * time.Second,
		EnableCompression: true,
	}

	dialStart := time.Now()
	conn, resp, err := dialer.DialContext(ctx, wsURL, headers)
	pebblestore.ObserveExecutionEpochSocketDial(dialStart)
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
	normalizeCodexWebsocketRequestInput(decoded)
	encoded, err := json.Marshal(decoded)
	if err != nil {
		return nil, fmt.Errorf("encode codex websocket payload: %w", err)
	}
	return encoded, nil
}

func normalizeCodexWebsocketRequestInput(decoded map[string]any) {
	if decoded == nil {
		return
	}
	raw, ok := decoded["input"]
	if !ok {
		return
	}
	normalized := sanitizeCodexRequestInputValue(raw)
	if normalized == nil {
		decoded["input"] = []any{}
		return
	}
	decoded["input"] = normalized
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
		if strings.TrimSpace(asString(tool["type"])) == "image_generation" {
			continue
		}
		parameters, _ := tool["parameters"].(map[string]any)
		tool["parameters"] = sanitizeCodexToolParameters(parameters)
	}
}

func codexWebsocketSendPayload(requestPayload map[string]any, session *cachedWebsocketSession, freshContext bool) map[string]any {
	if session == nil || freshContext {
		return cloneMapAny(requestPayload)
	}
	return prepareIncrementalWebsocketRequest(cloneMapAny(requestPayload), session.lastPayload, session.lastResponseID, session.lastOutput)
}

func codexOutboundShapeEvent(stage string, transport codexTransportContext, payload map[string]any, previousResponseIDPresent, websocketReused bool) providerdiagnostics.Event {
	inputItems, inputChars, nativeItems, encryptedItems := codexPayloadInputShape(payload)
	return providerdiagnostics.Event{
		Provider:  "codex",
		Operation: "responses.websocket",
		Stage:     strings.TrimSpace(stage),
		Extra: map[string]any{
			"native_continuation_allowed":  transport.NativeContinuationAllowed,
			"force_fresh_provider_context": transport.ForceFreshProviderContext,
			"boundary_reason":              transport.BoundaryReason,
			"prompt_cache_key_present":     strings.TrimSpace(transport.PromptCacheKey) != "",
			"session_affinity_key_present": strings.TrimSpace(transport.SessionAffinityKey) != "",
			"session_affinity_key_hash":    codexSafeKeyHash(transport.SessionAffinityKey),
			"previous_response_id_present": previousResponseIDPresent,
			"websocket_reused":             websocketReused,
			"input_items":                  inputItems,
			"input_text_chars":             inputChars,
			"native_input_items":           nativeItems,
			"encrypted_input_items":        encryptedItems,
		},
	}
}

func codexPayloadInputShape(payload map[string]any) (int, int, int, int) {
	input := asSlice(payload["input"])
	chars := 0
	nativeItems := 0
	encryptedItems := 0
	var walk func(any)
	walk = func(value any) {
		switch typed := value.(type) {
		case string:
			chars += len([]rune(typed))
		case []map[string]any:
			for _, child := range typed {
				walk(child)
			}
		case []any:
			for _, child := range typed {
				walk(child)
			}
		case map[string]any:
			itemType := strings.ToLower(strings.TrimSpace(asString(typed["type"])))
			if itemType == "reasoning" || itemType == "function_call" || itemType == "function_call_output" {
				nativeItems++
			}
			if encryptedContent, ok := typed["encrypted_content"]; ok && strings.TrimSpace(asString(encryptedContent)) != "" {
				encryptedItems++
			}
			for _, child := range typed {
				walk(child)
			}
		}
	}
	walk(input)
	return len(input), chars, nativeItems, encryptedItems
}

func codexSafeKeyHash(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%x", sum[:8])
}

func prepareIncrementalWebsocketRequest(current, previous map[string]any, lastResponseID string, lastOutput []any) map[string]any {
	if len(current) == 0 || len(previous) == 0 || strings.TrimSpace(lastResponseID) == "" {
		return current
	}
	if !reflect.DeepEqual(codexWebsocketRequestPropertiesFromPayload(previous), codexWebsocketRequestPropertiesFromPayload(current)) {
		return current
	}

	currentInputRaw := asSlice(current["input"])
	currentInput := codexInputItemsForIncrementalCompare(currentInputRaw)
	baseline := codexInputItemsForIncrementalCompare(asSlice(previous["input"]))
	if len(lastOutput) > 0 {
		baseline = append(baseline, codexInputItemsForIncrementalCompare(lastOutput)...)
	}
	if !inputStartsWith(currentInput, baseline) {
		return current
	}

	incremental := cloneSliceAny(currentInputRaw[len(baseline):])
	if incremental == nil {
		incremental = []any{}
	}
	current["previous_response_id"] = strings.TrimSpace(lastResponseID)
	current["input"] = incremental
	return current
}

func codexWebsocketRequestPropertiesMatch(previous, current map[string]any) bool {
	return reflect.DeepEqual(codexWebsocketRequestPropertiesFromPayload(previous), codexWebsocketRequestPropertiesFromPayload(current))
}

func codexWebsocketRequestPropertiesFromPayload(payload map[string]any) map[string]any {
	if len(payload) == 0 {
		return nil
	}
	properties := make(map[string]any, len(payload))
	for _, key := range []string{
		"model",
		"instructions",
		"tools",
		"tool_choice",
		"parallel_tool_calls",
		"reasoning",
		"store",
		"stream",
		"include",
		"service_tier",
		"prompt_cache_key",
		"text",
	} {
		if value, ok := payload[key]; ok {
			properties[key] = value
		}
	}
	return properties
}

func codexInputItemsForIncrementalCompare(items []any) []any {
	out := cloneSliceAny(items)
	for i, item := range out {
		itemMap, ok := item.(map[string]any)
		if !ok || itemMap == nil {
			continue
		}
		delete(itemMap, "internal_chat_message_metadata_passthrough")
		delete(itemMap, "internal_chat_message_metadata")
		out[i] = itemMap
	}
	return out
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

func buildCodexTransportHeaders(record pebblestore.CodexAuthRecord, transport codexTransportContext) http.Header {
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+bearerToken(record))
	headers.Set(openAIBetaHeader, responsesWebsocketBetaHeaderV2)
	if record.Type == pebblestore.CodexAuthTypeOAuth {
		headers.Set(originatorHeader, defaultOriginatorHeaderValue)
		headers.Set(userAgentHeader, defaultCodexTransportUserAgent)
	} else {
		headers.Set(userAgentHeader, defaultOpenAITransportUserAgent)
	}
	if sessionID := strings.TrimSpace(transport.SessionAffinityKey); sessionID != "" {
		headers.Set("session_id", sessionID)
		if record.Type == pebblestore.CodexAuthTypeOAuth {
			headers.Set("x-codex-window-id", sessionID)
		}
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
	completedResponse       map[string]any
	lastObject              map[string]any
	outputText              string
	reasoningSummary        map[string]string
	reasoningOrder          []string
	outputItems             []map[string]any
	outputItemsDone         []map[string]any
	outputItemPos           map[string]int
	outputItemDonePos       map[string]int
	imageGenerationResults  map[string]string
	imageGenerationPartials []map[string]any
	imageGenerationFinals   []map[string]any
	toolCallArguments        map[string]string
	toolCallSnapshots        map[string]string
	toolCallStarted          map[string]struct{}
	toolCallCompleted        map[string]struct{}
	toolCallPendingArguments map[string][]StreamEvent
	toolCallPendingComplete  map[string]StreamEvent
	toolCallsByIndex         map[int]StreamEvent
	rawEvents               []map[string]any
	sawPayload              bool
	streamBytes             int
	streamEvents            int
	decodeErr               error
}

func processResponseStreamEvent(eventName string, payload string, state *streamDecodeState, onEvent func(StreamEvent)) {
	trimmedPayload := strings.TrimSpace(payload)
	if trimmedPayload == "" || trimmedPayload == "[DONE]" {
		return
	}
	if state == nil || state.decodeErr != nil {
		return
	}
	state.sawPayload = true
	state.streamEvents++
	state.streamBytes += len(payload)
	if state.streamEvents > maxCodexStreamEvents {
		state.decodeErr = errors.New("codex stream event limit exceeded")
		return
	}
	if state.streamBytes > maxCodexStreamBytes {
		state.decodeErr = errors.New("codex stream byte limit exceeded")
		return
	}

	var decoded map[string]any
	if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
		codexThinkingDebugEvent("event.decode_error", map[string]any{
			"tag":           eventName,
			"payload_chars": len(payload),
			"error":         err.Error(),
		})
		state.decodeErr = fmt.Errorf("decode codex stream event: %w", err)
		return
	}
	state.lastObject = decoded
	if len(state.rawEvents) < maxCodexRawEvents {
		state.rawEvents = append(state.rawEvents, map[string]any{
			"event":         eventName,
			"payload_chars": len(payload),
			"data":          cloneMapAny(decoded),
		})
	}

	if strings.TrimSpace(eventName) == "" {
		eventName = asString(decoded["type"])
	}
	codexThinkingLogSSEEvent(eventName, decoded, len(payload))

	switch eventName {
	case "response.image_generation_call.partial_image":
		recordImageGenerationPartialImageEvent(state, decoded)
		if onEvent != nil {
			onEvent(streamEventImageGenerationPartialImage(decoded))
		}
	case "response.image_generation_call.completed", "image_generation.completed":
		completeImageGenerationCallEvent(state, decoded)
		if onEvent != nil {
			onEvent(streamEventImageGenerationCompleted(decoded))
		}
	case "response.output_text.delta":
		delta := firstNonEmpty(asString(decoded["delta"]), asString(decoded["text"]), asString(decoded["output_text_delta"]))
		if delta != "" {
			state.outputText += delta
			if onEvent != nil {
				onEvent(StreamEvent{Type: StreamEventOutputTextDelta, Delta: delta})
			}
		}
	case "response.output_text.done":
		text := strings.TrimSpace(firstNonEmpty(asString(decoded["text"]), asString(decoded["output_text"])))
		// Fallback for streams that emit only the terminal done payload without deltas.
		if text != "" {
			state.outputText = mergeOutputTextSnapshot(state.outputText, text)
		}
	case "response.function_call_arguments.delta", "response.function_call.arguments.delta",
		"response.custom_tool_call_input.delta", "response.custom_tool_call.input.delta":
		emitToolCallArgumentsDeltaEvent(state, decoded, onEvent)
	case "response.function_call_arguments.done", "response.function_call.arguments.done",
		"response.custom_tool_call_input.done", "response.custom_tool_call.input.done":
		emitToolCallCompletedEvent(state, decoded, onEvent)
	case "response.output_item.added", "response.output_item.done":
		item := extractOutputItemFromEvent(decoded)
		if len(item) == 0 {
			break
		}
		recordOutputItemEvent(state, item, decoded)
		if strings.EqualFold(strings.TrimSpace(eventName), "response.output_item.done") {
			recordDoneOutputItemEvent(state, item, decoded)
		}
		emitToolCallOutputItemEvent(eventName, state, item, decoded, onEvent)
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
		if isReasoningSummaryEvent(eventName) {
			next = mergeReasoningSummaryEvent(previous, delta, reasoningEventIsSnapshot(eventName))
		} else if reasoningEventIsSnapshot(eventName) {
			next = mergeReasoningSummarySnapshot(previous, delta)
		} else {
			next = mergeReasoningSummaryChunk(previous, delta)
		}
		setReasoningStateText(state, reasoningKey, next)
		codexThinkingDebugf("tag=%s key=%s delta_chars=%d total_reasoning_chars=%d", eventName, reasoningKey, len(delta), len(next))
		// Emit full merged snapshots so downstream UI can atomically replace
		// visible summary content. Hold heading-only partials until their body or
		// final event arrives, and never emit Codex's empty HTML-comment sentinel.
		finalSummaryPart := isReasoningSummaryEvent(eventName) && reasoningEventFinalizesPart(eventName)
		heldHeadingCompleted := finalSummaryPart && next == previous && isHeadingOnlyReasoningSummaryPart(next)
		if onEvent != nil && (next != previous || heldHeadingCompleted) && (!isReasoningSummaryEvent(eventName) || shouldEmitReasoningSummaryPart(next, finalSummaryPart)) {
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

func emitToolCallOutputItemEvent(eventName string, state *streamDecodeState, item map[string]any, event map[string]any, onEvent func(StreamEvent)) {
	if onEvent == nil || len(item) == 0 || !isToolCallOutputItem(item) {
		return
	}
	streamEvent := mergeToolCallEventWithState(state, streamEventToolCallFromOutputItem(item, event))
	rememberToolCallEvent(state, streamEvent)
	if streamEvent.ToolCallID == "" && streamEvent.ToolName == "" {
		return
	}
	key := toolCallStreamKey(streamEvent)
	if key == "" {
		return
	}
	ensureToolCallStarted(state, key, streamEvent, onEvent)
	flushPendingToolCallArguments(state, key, streamEvent, onEvent)
	if toolCallCompletedSeen(state, key) {
		return
	}

	snapshot := strings.TrimSpace(streamEvent.ArgumentsSnapshot)
	if snapshot != "" && rememberToolCallSnapshot(state, key, snapshot) {
		onEvent(StreamEvent{
			Type:              StreamEventToolCallArgumentsSnapshot,
			ToolCallID:        streamEvent.ToolCallID,
			ToolCallIndex:     cloneIntPtr(streamEvent.ToolCallIndex),
			ToolName:          streamEvent.ToolName,
			ArgumentsSnapshot: snapshot,
			Metadata:          cloneMapAny(streamEvent.Metadata),
		})
	}

	if pending, ok := takePendingToolCallCompletion(state, key); ok {
		streamEvent = mergeToolCallEvents(streamEvent, pending)
		emitCompletedToolCall(state, key, streamEvent, onEvent)
		return
	}
	if strings.EqualFold(strings.TrimSpace(eventName), "response.output_item.done") || strings.EqualFold(strings.TrimSpace(asString(item["status"])), "completed") {
		emitCompletedToolCall(state, key, streamEvent, onEvent)
	}
}

func emitToolCallArgumentsDeltaEvent(state *streamDecodeState, event map[string]any, onEvent func(StreamEvent)) {
	if onEvent == nil || len(event) == 0 {
		return
	}
	streamEvent := mergeToolCallEventWithState(state, streamEventToolCallFromArgumentEvent(event))
	key := toolCallStreamKey(streamEvent)
	if key == "" {
		return
	}

	delta := firstNonEmpty(
		asString(event["delta"]),
		asString(event["arguments_delta"]),
		asString(event["input_delta"]),
	)
	if delta == "" {
		return
	}
	if state != nil {
		if state.toolCallArguments == nil {
			state.toolCallArguments = make(map[string]string, 4)
		}
		state.toolCallArguments[key] += delta
	}
	deltaEvent := StreamEvent{
		Type:           StreamEventToolCallArgumentsDelta,
		ToolCallID:     streamEvent.ToolCallID,
		ToolCallIndex:  cloneIntPtr(streamEvent.ToolCallIndex),
		ToolName:       streamEvent.ToolName,
		ArgumentsDelta: delta,
		Delta:          delta,
		Metadata:       cloneMapAny(streamEvent.Metadata),
	}
	if streamEvent.ToolCallID == "" && streamEvent.ToolName == "" {
		rememberToolCallEvent(state, streamEvent)
		rememberPendingToolCallArgument(state, key, deltaEvent)
		return
	}
	ensureToolCallStarted(state, key, streamEvent, onEvent)
	rememberToolCallEvent(state, streamEvent)
	onEvent(deltaEvent)
}

func emitToolCallCompletedEvent(state *streamDecodeState, event map[string]any, onEvent func(StreamEvent)) {
	if onEvent == nil || len(event) == 0 {
		return
	}
	streamEvent := mergeToolCallEventWithState(state, streamEventToolCallFromArgumentEvent(event))
	key := toolCallStreamKey(streamEvent)
	if key == "" {
		return
	}
	if streamEvent.ToolCallID == "" && streamEvent.ToolName == "" {
		rememberToolCallEvent(state, streamEvent)
		rememberPendingToolCallCompletion(state, key, streamEvent)
		return
	}
	ensureToolCallStarted(state, key, streamEvent, onEvent)
	rememberToolCallEvent(state, streamEvent)
	flushPendingToolCallArguments(state, key, streamEvent, onEvent)
	if streamEvent.Arguments == "" && state != nil && state.toolCallArguments != nil {
		streamEvent.Arguments = strings.TrimSpace(state.toolCallArguments[key])
	}
	emitCompletedToolCall(state, key, streamEvent, onEvent)
}

func ensureToolCallStarted(state *streamDecodeState, key string, event StreamEvent, onEvent func(StreamEvent)) {
	if onEvent == nil || key == "" {
		return
	}
	if state != nil {
		if state.toolCallStarted == nil {
			state.toolCallStarted = make(map[string]struct{}, 4)
		}
		if _, exists := state.toolCallStarted[key]; exists {
			return
		}
		state.toolCallStarted[key] = struct{}{}
	}
	onEvent(StreamEvent{
		Type:          StreamEventToolCallStarted,
		ToolCallID:    event.ToolCallID,
		ToolCallIndex: cloneIntPtr(event.ToolCallIndex),
		ToolName:      event.ToolName,
		Metadata:      cloneMapAny(event.Metadata),
	})
}

func emitCompletedToolCall(state *streamDecodeState, key string, event StreamEvent, onEvent func(StreamEvent)) {
	if onEvent == nil || key == "" {
		return
	}
	if state != nil {
		if state.toolCallCompleted == nil {
			state.toolCallCompleted = make(map[string]struct{}, 4)
		}
		if toolCallCompletedSeen(state, key) {
			return
		}
		state.toolCallCompleted[key] = struct{}{}
		if event.Arguments == "" && state.toolCallArguments != nil {
			event.Arguments = strings.TrimSpace(state.toolCallArguments[key])
		}
	}
	onEvent(StreamEvent{
		Type:          StreamEventToolCallCompleted,
		ToolCallID:    event.ToolCallID,
		ToolCallIndex: cloneIntPtr(event.ToolCallIndex),
		ToolName:      event.ToolName,
		Arguments:     strings.TrimSpace(event.Arguments),
		Metadata:      cloneMapAny(event.Metadata),
	})
}

func toolCallCompletedSeen(state *streamDecodeState, key string) bool {
	if state == nil || state.toolCallCompleted == nil || key == "" {
		return false
	}
	_, exists := state.toolCallCompleted[key]
	return exists
}

func streamEventToolCallFromOutputItem(item map[string]any, event map[string]any) StreamEvent {
	index := intPointerFromAny(firstPresentValue(event, item, "output_index"))
	arguments := strings.TrimSpace(asString(item["arguments"]))
	if arguments == "" {
		switch typed := item["arguments"].(type) {
		case map[string]any, []any:
			arguments = strings.TrimSpace(normalizeArguments(typed))
		}
	}
	if strings.EqualFold(strings.TrimSpace(asString(item["type"])), "custom_tool_call") {
		arguments = strings.TrimSpace(firstNonEmpty(asString(item["input"]), asString(item["arguments"])))
	}
	return StreamEvent{
		ToolCallID:        toolCallIDFromMaps(item, event),
		ToolCallIndex:     index,
		ToolName:          toolCallNameFromMap(item),
		Arguments:         arguments,
		ArgumentsSnapshot: arguments,
		Metadata:          toolCallEventMetadata(item, event),
	}
}

func rememberToolCallSnapshot(state *streamDecodeState, key, snapshot string) bool {
	if state == nil {
		return true
	}
	if state.toolCallSnapshots == nil {
		state.toolCallSnapshots = make(map[string]string, 4)
	}
	if state.toolCallSnapshots[key] == snapshot {
		return false
	}
	state.toolCallSnapshots[key] = snapshot
	if state.toolCallArguments == nil {
		state.toolCallArguments = make(map[string]string, 4)
	}
	state.toolCallArguments[key] = snapshot
	return true
}

func rememberPendingToolCallArgument(state *streamDecodeState, key string, event StreamEvent) {
	if state == nil || key == "" {
		return
	}
	if state.toolCallPendingArguments == nil {
		state.toolCallPendingArguments = make(map[string][]StreamEvent, 4)
	}
	state.toolCallPendingArguments[key] = append(state.toolCallPendingArguments[key], event)
}

func flushPendingToolCallArguments(state *streamDecodeState, key string, identity StreamEvent, onEvent func(StreamEvent)) {
	if state == nil || state.toolCallPendingArguments == nil || key == "" || onEvent == nil {
		return
	}
	pending := state.toolCallPendingArguments[key]
	delete(state.toolCallPendingArguments, key)
	for _, event := range pending {
		onEvent(mergeToolCallEvents(identity, event))
	}
}

func rememberPendingToolCallCompletion(state *streamDecodeState, key string, event StreamEvent) {
	if state == nil || key == "" {
		return
	}
	if state.toolCallPendingComplete == nil {
		state.toolCallPendingComplete = make(map[string]StreamEvent, 4)
	}
	state.toolCallPendingComplete[key] = mergeToolCallEvents(state.toolCallPendingComplete[key], event)
}

func takePendingToolCallCompletion(state *streamDecodeState, key string) (StreamEvent, bool) {
	if state == nil || state.toolCallPendingComplete == nil || key == "" {
		return StreamEvent{}, false
	}
	event, ok := state.toolCallPendingComplete[key]
	if ok {
		delete(state.toolCallPendingComplete, key)
	}
	return event, ok
}

func streamEventToolCallFromArgumentEvent(event map[string]any) StreamEvent {
	arguments := strings.TrimSpace(firstNonEmpty(
		asString(event["arguments"]),
		asString(event["input"]),
		asString(event["arguments_done"]),
		asString(event["input_done"]),
	))
	return StreamEvent{
		// Responses API argument events identify the output item with item_id;
		// that value is not the stable function call_id. output_index remains the
		// repair key until an output item reveals call_id and name.
		ToolCallID:    strings.TrimSpace(firstNonEmpty(asString(event["call_id"]), asString(event["id"]))),
		ToolCallIndex: intPointerFromAny(firstPresentValue(event, nil, "output_index", "item_index")),
		ToolName:      toolCallNameFromMap(event),
		Arguments:     arguments,
		Metadata:      toolCallEventMetadata(nil, event),
	}
}

func rememberToolCallEvent(state *streamDecodeState, event StreamEvent) {
	if state == nil || event.ToolCallIndex == nil {
		return
	}
	if state.toolCallsByIndex == nil {
		state.toolCallsByIndex = make(map[int]StreamEvent, 4)
	}
	state.toolCallsByIndex[*event.ToolCallIndex] = mergeToolCallEvents(state.toolCallsByIndex[*event.ToolCallIndex], event)
}

func mergeToolCallEventWithState(state *streamDecodeState, event StreamEvent) StreamEvent {
	if state == nil || event.ToolCallIndex == nil || state.toolCallsByIndex == nil {
		return event
	}
	known, ok := state.toolCallsByIndex[*event.ToolCallIndex]
	if !ok {
		return event
	}
	return mergeToolCallEvents(known, event)
}

func mergeToolCallEvents(known, event StreamEvent) StreamEvent {
	if event.ToolCallID == "" {
		event.ToolCallID = known.ToolCallID
	}
	if event.ToolCallIndex == nil {
		event.ToolCallIndex = cloneIntPtr(known.ToolCallIndex)
	}
	if event.ToolName == "" {
		event.ToolName = known.ToolName
	}
	if event.Arguments == "" {
		event.Arguments = known.Arguments
	}
	if event.ArgumentsSnapshot == "" {
		event.ArgumentsSnapshot = known.ArgumentsSnapshot
	}
	mergedMetadata := cloneMapAny(known.Metadata)
	if mergedMetadata == nil && len(event.Metadata) > 0 {
		mergedMetadata = make(map[string]any, len(event.Metadata))
	}
	for key, value := range event.Metadata {
		mergedMetadata[key] = value
	}
	event.Metadata = mergedMetadata
	return event
}

func isToolCallOutputItem(item map[string]any) bool {
	switch strings.TrimSpace(asString(item["type"])) {
	case "function_call", "custom_tool_call":
		return true
	default:
		return false
	}
}

func toolCallIDFromMaps(maps ...map[string]any) string {
	for _, item := range maps {
		if len(item) == 0 {
			continue
		}
		for _, key := range []string{"call_id", "item_id", "id"} {
			if value := strings.TrimSpace(asString(item[key])); value != "" {
				return value
			}
		}
	}
	return ""
}

func toolCallNameFromMap(item map[string]any) string {
	if len(item) == 0 {
		return ""
	}
	return strings.TrimSpace(firstNonEmpty(asString(item["name"]), asString(item["tool_name"])))
}

func toolCallStreamKey(event StreamEvent) string {
	// output_index is stable across output-item and argument event shapes, while
	// item_id can precede and differ from the eventual function call_id.
	if event.ToolCallIndex != nil {
		return fmt.Sprintf("idx:%d", *event.ToolCallIndex)
	}
	if event.ToolCallID != "" {
		return "id:" + event.ToolCallID
	}
	if event.ToolName != "" {
		return "name:" + event.ToolName
	}
	return ""
}

func toolCallEventMetadata(item map[string]any, event map[string]any) map[string]any {
	metadata := make(map[string]any, 4)
	if itemType := strings.TrimSpace(asString(item["type"])); itemType != "" {
		metadata["provider_item_type"] = itemType
	}
	if itemID := strings.TrimSpace(asString(item["id"])); itemID != "" {
		metadata["provider_item_id"] = itemID
	}
	if eventType := strings.TrimSpace(asString(event["type"])); eventType != "" {
		metadata["provider_event_type"] = eventType
	}
	if itemID := strings.TrimSpace(asString(event["item_id"])); itemID != "" {
		metadata["provider_item_id"] = itemID
	}
	if outputIndex, ok := asInt64(firstPresentValue(event, item, "output_index")); ok {
		metadata["provider_output_index"] = outputIndex
	}
	if len(metadata) == 0 {
		return nil
	}
	return metadata
}

func firstPresentValue(primary map[string]any, secondary map[string]any, keys ...string) any {
	for _, key := range keys {
		if primary != nil {
			if value, exists := primary[key]; exists {
				return value
			}
		}
		if secondary != nil {
			if value, exists := secondary[key]; exists {
				return value
			}
		}
	}
	return nil
}

func intPointerFromAny(value any) *int {
	if number, ok := asInt64(value); ok {
		out := int(number)
		return &out
	}
	return nil
}

func streamEventImageGenerationPartialImage(event map[string]any) StreamEvent {
	return StreamEvent{
		Type:              StreamEventImageGenerationPartialImage,
		ItemID:            strings.TrimSpace(asString(event["item_id"])),
		OutputIndex:       intFromAny(event["output_index"], -1),
		SequenceNumber:    intFromAny(event["sequence_number"], -1),
		PartialImageIndex: intFromAny(event["partial_image_index"], -1),
		PartialImageB64:   strings.TrimSpace(asString(event["partial_image_b64"])),
		OutputFormat:      strings.TrimSpace(asString(event["output_format"])),
		Size:              strings.TrimSpace(asString(event["size"])),
		Quality:           strings.TrimSpace(asString(event["quality"])),
		Background:        strings.TrimSpace(asString(event["background"])),
	}
}

func streamEventImageGenerationCompleted(event map[string]any) StreamEvent {
	return StreamEvent{
		Type:           StreamEventImageGenerationCallCompleted,
		ItemID:         firstNonEmpty(strings.TrimSpace(asString(event["item_id"])), strings.TrimSpace(asString(event["id"]))),
		OutputIndex:    intFromAny(event["output_index"], -1),
		SequenceNumber: intFromAny(event["sequence_number"], -1),
		OutputFormat:   strings.TrimSpace(asString(event["output_format"])),
		Size:           strings.TrimSpace(asString(event["size"])),
		Quality:        strings.TrimSpace(asString(event["quality"])),
		Background:     strings.TrimSpace(asString(event["background"])),
	}
}

func intFromAny(value any, fallback int) int {
	if number, ok := asInt64(value); ok {
		return int(number)
	}
	return fallback
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
	if state == nil {
		return
	}
	recordOutputItemInto(state, item, event, &state.outputItems, &state.outputItemPos)
}

func recordDoneOutputItemEvent(state *streamDecodeState, item map[string]any, event map[string]any) {
	if state == nil {
		return
	}
	recordOutputItemInto(state, item, event, &state.outputItemsDone, &state.outputItemDonePos)
}

func recordOutputItemInto(state *streamDecodeState, item map[string]any, event map[string]any, items *[]map[string]any, positions *map[string]int) {
	if state == nil || len(item) == 0 || items == nil || positions == nil {
		return
	}
	item = cloneMapAny(item)
	if outputIndex, ok := asInt64(event["output_index"]); ok {
		if _, exists := item["output_index"]; !exists {
			item["output_index"] = outputIndex
		}
	}
	mergeImageGenerationResultIntoItem(state, item, event)
	if *positions == nil {
		*positions = make(map[string]int, 8)
	}
	key := outputItemEventKey(item, event, len(*items))
	if pos, ok := (*positions)[key]; ok && pos >= 0 && pos < len(*items) {
		(*items)[pos] = item
		return
	}
	*items = append(*items, item)
	(*positions)[key] = len(*items) - 1
}

func recordImageGenerationPartialImageEvent(state *streamDecodeState, event map[string]any) {
	if state == nil || len(event) == 0 {
		return
	}
	partial := cloneMapAny(event)
	state.imageGenerationPartials = append(state.imageGenerationPartials, partial)
}

func completeImageGenerationCallEvent(state *streamDecodeState, event map[string]any) {
	if state == nil || len(event) == 0 {
		return
	}
	key := imageGenerationEventKey(event, -1)
	if key == "" {
		if outputIndex := len(state.imageGenerationFinals); outputIndex >= 0 {
			key = fmt.Sprintf("output_index:%d", outputIndex)
		}
	}
	recordImageGenerationResultEvent(state, event, key)
	if imageGenerationEventHasFinalImageData(event) {
		state.imageGenerationFinals = append(state.imageGenerationFinals, cloneImageGenerationFinalEvent(event, len(state.imageGenerationFinals)))
	}
	if item := extractOutputItemFromEvent(event); len(item) != 0 {
		recordOutputItemEvent(state, item, event)
	}
	if key == "" {
		return
	}
	for pos := range state.outputItems {
		item := state.outputItems[pos]
		if !strings.EqualFold(strings.TrimSpace(asString(item["type"])), "image_generation_call") {
			continue
		}
		if !imageGenerationOutputItemMatchesEventKey(item, pos, key) {
			continue
		}
		item = cloneMapAny(item)
		item["status"] = "completed"
		if result := strings.TrimSpace(imageGenerationResultForItem(state, item, key)); result != "" && strings.TrimSpace(asString(item["result"])) == "" {
			item["result"] = result
		}
		state.outputItems[pos] = item
		return
	}
	if result := strings.TrimSpace(imageGenerationResultForKey(state, key)); result != "" {
		item := map[string]any{
			"type":   "image_generation_call",
			"status": "completed",
			"result": result,
		}
		if itemID := firstNonEmpty(strings.TrimSpace(asString(event["item_id"])), strings.TrimSpace(asString(event["id"]))); itemID != "" {
			item["id"] = itemID
		}
		if callID := strings.TrimSpace(asString(event["call_id"])); callID != "" {
			item["call_id"] = callID
		}
		if outputIndex, ok := asInt64(event["output_index"]); ok {
			item["output_index"] = outputIndex
		}
		copyImageGenerationEventMetadata(item, event)
		recordOutputItemEvent(state, item, event)
	}
}

func recordImageGenerationResultEvent(state *streamDecodeState, event map[string]any, key string) {
	if state == nil || len(event) == 0 || key == "" {
		return
	}
	result := extractImageGenerationResultFromEvent(event)
	if result == "" {
		return
	}
	if state.imageGenerationResults == nil {
		state.imageGenerationResults = make(map[string]string, 4)
	}
	state.imageGenerationResults[key] = result
	if outputIndex, ok := asInt64(event["output_index"]); ok {
		state.imageGenerationResults[fmt.Sprintf("output_index:%d", outputIndex)] = result
	}
	if id := strings.TrimSpace(asString(event["id"])); id != "" {
		state.imageGenerationResults["id:"+id] = result
	}
	if callID := strings.TrimSpace(asString(event["call_id"])); callID != "" {
		state.imageGenerationResults["call_id:"+callID] = result
	}
	if item, ok := event["item"].(map[string]any); ok {
		if itemKey := imageGenerationOutputItemKey(item, -1); itemKey != "" {
			state.imageGenerationResults[itemKey] = result
		}
		if eventKey := imageGenerationEventKey(item, -1); eventKey != "" {
			state.imageGenerationResults[eventKey] = result
		}
	}
}

func extractImageGenerationResultFromEvent(event map[string]any) string {
	if len(event) == 0 {
		return ""
	}
	for _, key := range []string{"result", "b64_json", "image_b64", "base64_image"} {
		if result := strings.TrimSpace(asString(event[key])); result != "" {
			return result
		}
	}
	if item, ok := event["item"].(map[string]any); ok {
		if result := extractImageGenerationResultFromEvent(item); result != "" {
			return result
		}
	}
	return ""
}

func mergeImageGenerationResultIntoItem(state *streamDecodeState, item map[string]any, event map[string]any) {
	if state == nil || len(item) == 0 || !strings.EqualFold(strings.TrimSpace(asString(item["type"])), "image_generation_call") {
		return
	}
	key := imageGenerationOutputItemKey(item, -1)
	if key == "" {
		key = imageGenerationEventKey(event, -1)
	}
	if key == "" {
		return
	}
	if direct := strings.TrimSpace(extractImageGenerationResultFromEvent(item)); direct != "" {
		recordImageGenerationResultEvent(state, item, key)
	}
	result := strings.TrimSpace(imageGenerationResultForItem(state, item, key))
	if result == "" {
		result = strings.TrimSpace(imageGenerationResultForKey(state, imageGenerationEventKey(event, -1)))
	}
	if result != "" && strings.TrimSpace(asString(item["result"])) == "" {
		item["result"] = result
	}
	copyImageGenerationEventMetadata(item, event)
	if imageGenerationEventIsCompleted(event) {
		item["status"] = "completed"
	}
}

func imageGenerationResultForKey(state *streamDecodeState, key string) string {
	if state == nil || state.imageGenerationResults == nil || key == "" {
		return ""
	}
	return state.imageGenerationResults[key]
}

func imageGenerationEventHasFinalImageData(event map[string]any) bool {
	return strings.TrimSpace(extractImageGenerationResultFromEvent(event)) != ""
}

func imageGenerationEventIsCompleted(event map[string]any) bool {
	eventType := strings.TrimSpace(asString(event["type"]))
	return strings.EqualFold(eventType, "response.image_generation_call.completed") || strings.EqualFold(eventType, "image_generation.completed") || strings.EqualFold(strings.TrimSpace(asString(event["status"])), "completed")
}

func cloneImageGenerationFinalEvent(event map[string]any, fallbackIndex int) map[string]any {
	out := map[string]any{
		"type":   "image_generation_call",
		"status": "completed",
		"result": strings.TrimSpace(extractImageGenerationResultFromEvent(event)),
	}
	if itemID := firstNonEmpty(strings.TrimSpace(asString(event["item_id"])), strings.TrimSpace(asString(event["id"]))); itemID != "" {
		out["id"] = itemID
	}
	if callID := strings.TrimSpace(asString(event["call_id"])); callID != "" {
		out["call_id"] = callID
	}
	if outputIndex, ok := asInt64(event["output_index"]); ok {
		out["output_index"] = outputIndex
	} else {
		out["output_index"] = fallbackIndex
	}
	copyImageGenerationEventMetadata(out, event)
	return out
}

func copyImageGenerationEventMetadata(dst map[string]any, src map[string]any) {
	if dst == nil || src == nil {
		return
	}
	for _, key := range []string{"revised_prompt", "output_format", "size", "quality", "background"} {
		if value := strings.TrimSpace(asString(src[key])); value != "" {
			dst[key] = value
		}
	}
}

func imageGenerationResultForItem(state *streamDecodeState, item map[string]any, key string) string {
	if result := imageGenerationResultForKey(state, key); result != "" {
		return result
	}
	if itemKey := imageGenerationOutputItemKey(item, -1); itemKey != "" {
		if result := imageGenerationResultForKey(state, itemKey); result != "" {
			return result
		}
	}
	if itemKey := imageGenerationEventKey(item, -1); itemKey != "" {
		if result := imageGenerationResultForKey(state, itemKey); result != "" {
			return result
		}
	}
	return ""
}

func imageGenerationOutputItemMatchesEventKey(item map[string]any, fallbackIndex int, key string) bool {
	key = strings.TrimSpace(key)
	if key == "" || len(item) == 0 {
		return false
	}
	if imageGenerationOutputItemKey(item, fallbackIndex) == key {
		return true
	}
	if imageGenerationEventKey(item, fallbackIndex) == key {
		return true
	}
	return fallbackIndex >= 0 && key == fmt.Sprintf("output_index:%d", fallbackIndex)
}

func imageGenerationEventKey(event map[string]any, fallbackIndex int) string {
	if len(event) == 0 {
		return ""
	}
	if itemID := strings.TrimSpace(asString(event["item_id"])); itemID != "" {
		return "id:" + itemID
	}
	if id := strings.TrimSpace(asString(event["id"])); id != "" {
		return "id:" + id
	}
	if callID := strings.TrimSpace(asString(event["call_id"])); callID != "" {
		return "call_id:" + callID
	}
	if outputIndex, ok := asInt64(event["output_index"]); ok {
		return fmt.Sprintf("output_index:%d", outputIndex)
	}
	if fallbackIndex >= 0 {
		return fmt.Sprintf("idx:%d", fallbackIndex)
	}
	return ""
}

func imageGenerationOutputItemKey(item map[string]any, fallbackIndex int) string {
	if len(item) == 0 {
		return ""
	}
	if id := strings.TrimSpace(asString(item["id"])); id != "" {
		return "id:" + id
	}
	if callID := strings.TrimSpace(asString(item["call_id"])); callID != "" {
		return "call_id:" + callID
	}
	if outputIndex, ok := asInt64(item["output_index"]); ok {
		return fmt.Sprintf("output_index:%d", outputIndex)
	}
	if fallbackIndex >= 0 {
		return fmt.Sprintf("output_index:%d", fallbackIndex)
	}
	return ""
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
	case "image_generation_call":
		if outputIndex, ok := asInt64(item["output_index"]); ok {
			return fmt.Sprintf("image_generation_call:output:%d", outputIndex)
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

func isReasoningSummaryEvent(eventName string) bool {
	return strings.HasPrefix(eventName, "response.reasoning_summary_") || eventName == "response.reasoning_summary.delta"
}

func reasoningEventFinalizesPart(eventName string) bool {
	switch eventName {
	case "response.reasoning_summary_part.done", "response.reasoning_summary_text.done":
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

func mergeOutputItemsIntoResponse(responseObj map[string]any, outputItems []map[string]any) {
	if responseObj == nil || len(outputItems) == 0 {
		return
	}
	existingRaw := asSlice(responseObj["output"])
	if len(existingRaw) == 0 {
		responseObj["output"] = normalizeCodexResponseOutputMapsForReplay(outputItems)
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
		merged = append(merged, normalizeCodexResponseOutputItemForReplay(item))
	}
	for i, item := range outputItems {
		key := outputItemMergeKey(item, i+len(merged))
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		merged = append(merged, normalizeCodexResponseOutputItemForReplay(item))
	}
	responseObj["output"] = mapsToAnySlice(merged)
}

func normalizeCodexResponseOutputMapsForReplay(items []map[string]any) []any {
	if len(items) == 0 {
		return nil
	}
	out := make([]any, 0, len(items))
	for _, item := range items {
		if len(item) == 0 {
			continue
		}
		out = append(out, normalizeCodexResponseOutputItemForReplay(item))
	}
	return out
}

func normalizeCodexResponseOutputItemForReplay(item map[string]any) map[string]any {
	cloned := cloneMapAny(item)
	if strings.EqualFold(strings.TrimSpace(asString(cloned["type"])), "reasoning") {
		normalizeCodexReasoningReplayItem(cloned)
	}
	return cloned
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

func attachRawStreamEvents(responseObj map[string]any, events []map[string]any) {
	if responseObj == nil || len(events) == 0 {
		return
	}
	responseObj["raw_events"] = mapsToAnySlice(events)
}

func attachImageGenerationPartials(responseObj map[string]any, partials []map[string]any) {
	if responseObj == nil || len(partials) == 0 {
		return
	}
	responseObj["image_generation_partials"] = mapsToAnySlice(partials)
}

func attachImageGenerationFinals(responseObj map[string]any, finals []map[string]any) {
	if responseObj == nil || len(finals) == 0 {
		return
	}
	existing := asSlice(responseObj["output"])
	merged := make([]map[string]any, 0, len(existing)+len(finals))
	posByKey := make(map[string]int, len(existing)+len(finals))
	for index, raw := range existing {
		item, ok := raw.(map[string]any)
		if !ok || len(item) == 0 {
			continue
		}
		cloned := cloneMapAny(item)
		key := imageGenerationAttachMergeKey(cloned, index)
		if key != "" {
			posByKey[key] = len(merged)
		}
		merged = append(merged, cloned)
	}
	for index, final := range finals {
		if len(final) == 0 {
			continue
		}
		cloned := cloneMapAny(final)
		key := imageGenerationAttachMergeKey(cloned, len(merged)+index)
		if key != "" {
			if pos, ok := posByKey[key]; ok && pos >= 0 && pos < len(merged) {
				merged[pos] = mergeImageGenerationOutputItem(merged[pos], cloned)
				continue
			}
			posByKey[key] = len(merged)
		}
		merged = append(merged, cloned)
	}
	if len(merged) > 0 {
		responseObj["output"] = mapsToAnySlice(merged)
	}
}

func imageGenerationAttachMergeKey(item map[string]any, fallbackIndex int) string {
	if !strings.EqualFold(strings.TrimSpace(asString(item["type"])), "image_generation_call") {
		return outputItemMergeKey(item, fallbackIndex)
	}
	if id := strings.TrimSpace(asString(item["id"])); id != "" {
		return "image_generation_call:id:" + id
	}
	if callID := strings.TrimSpace(asString(item["call_id"])); callID != "" {
		return "image_generation_call:call_id:" + callID
	}
	if outputIndex, ok := asInt64(item["output_index"]); ok {
		return fmt.Sprintf("image_generation_call:output:%d", outputIndex)
	}
	return fmt.Sprintf("image_generation_call:idx:%d", fallbackIndex)
}

func mergeImageGenerationOutputItem(existing map[string]any, final map[string]any) map[string]any {
	merged := cloneMapAny(existing)
	if merged == nil {
		merged = map[string]any{}
	}
	for key, value := range final {
		if key == "" || value == nil {
			continue
		}
		if strings.TrimSpace(asString(value)) == "" {
			if _, ok := value.(string); ok {
				continue
			}
		}
		merged[key] = value
	}
	return merged
}

func finalizeStreamDecodeState(state *streamDecodeState) (map[string]any, error) {
	if state == nil {
		return nil, errors.New("codex stream state is not configured")
	}
	if state.decodeErr != nil {
		return nil, state.decodeErr
	}
	if state.completedResponse == nil {
		return nil, errors.New("codex response stream ended before response.completed")
	}
	reasoningSummary := aggregateReasoningStateText(state)
	if state.completedResponse != nil {
		mergeOutputItemsIntoResponse(state.completedResponse, state.outputItems)
		attachImageGenerationFinals(state.completedResponse, state.imageGenerationFinals)
		attachImageGenerationPartials(state.completedResponse, state.imageGenerationPartials)
		attachRawStreamEvents(state.completedResponse, state.rawEvents)
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
		attachImageGenerationFinals(state.lastObject, state.imageGenerationFinals)
		attachImageGenerationPartials(state.lastObject, state.imageGenerationPartials)
		attachRawStreamEvents(state.lastObject, state.rawEvents)
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
	if len(state.outputItems) > 0 || len(state.imageGenerationFinals) > 0 {
		payload := map[string]any{
			"output": mapsToAnySlice(state.outputItems),
		}
		attachImageGenerationFinals(payload, state.imageGenerationFinals)
		attachImageGenerationPartials(payload, state.imageGenerationPartials)
		attachRawStreamEvents(payload, state.rawEvents)
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
	scanner := bufio.NewScanner(io.LimitReader(reader, maxCodexStreamBytes+1))
	scanner.Buffer(make([]byte, 0, 64*1024), maxCodexStreamBytes)

	state := &streamDecodeState{}
	eventName := ""
	dataLines := make([]string, 0, 8)

	totalBytes := 0
	eventCount := 0
	flushEvent := func() {
		if len(dataLines) == 0 {
			eventName = ""
			return
		}
		payload := strings.Join(dataLines, "\n")
		dataLines = dataLines[:0]
		eventCount++
		if eventCount > maxCodexStreamEvents {
			state.decodeErr = errors.New("codex stream event limit exceeded")
			eventName = ""
			return
		}
		processResponseStreamEvent(eventName, payload, state, onEvent)
		eventName = ""
	}

	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		totalBytes += len(line) + 1
		if totalBytes > maxCodexStreamBytes {
			return nil, errors.New("codex stream byte limit exceeded")
		}
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
	if summary == "" || isEmptyReasoningSummaryPart(summary) {
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
		Raw:        cloneMapAny(decoded),
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
	serviceTier := strings.TrimSpace(asString(usage["service_tier"]))
	estimatedCostUSD, _ := floatFromAny(usage["estimated_cost_usd"])

	usageRaw := cloneMapAny(usage)
	out := TokenUsage{
		InputTokens:      inputTokens,
		OutputTokens:     outputTokens,
		ThinkingTokens:   thinkingTokens,
		TotalTokens:      totalTokens,
		CacheReadTokens:  cacheReadTokens,
		CacheWriteTokens: cacheWriteTokens,
		ServiceTier:      serviceTier,
		EstimatedCostUSD: estimatedCostUSD,
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

func floatFromAny(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int8:
		return float64(typed), true
	case int16:
		return float64(typed), true
	case int32:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case uint:
		return float64(typed), true
	case uint8:
		return float64(typed), true
	case uint16:
		return float64(typed), true
	case uint32:
		return float64(typed), true
	case uint64:
		return float64(typed), true
	case json.Number:
		if v, err := typed.Float64(); err == nil {
			return v, true
		}
	case string:
		if v, err := strconv.ParseFloat(strings.TrimSpace(typed), 64); err == nil {
			return v, true
		}
	}
	return 0, false
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
			if text == "" || isEmptyReasoningSummaryPart(text) {
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

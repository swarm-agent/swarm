package diagnostics

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/privacy"
)

const EnvName = "SWARM_PROVIDER_API_DIAGNOSTICS"

type Event struct {
	Provider   string         `json:"provider"`
	Operation  string         `json:"operation"`
	Stage      string         `json:"stage"`
	Method     string         `json:"method,omitempty"`
	URL        string         `json:"url,omitempty"`
	StatusCode int            `json:"status_code,omitempty"`
	Headers    string         `json:"headers,omitempty"`
	Body       string         `json:"body,omitempty"`
	Error      string         `json:"error,omitempty"`
	RecordedAt int64          `json:"recorded_at"`
	Extra      map[string]any `json:"extra,omitempty"`
}

type Recorder func(context.Context, Event)

type recorderContextKey struct{}

func ContextWithRecorder(ctx context.Context, recorder Recorder) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if recorder == nil {
		return ctx
	}
	return context.WithValue(ctx, recorderContextKey{}, recorder)
}

func recorderFromContext(ctx context.Context) Recorder {
	if ctx == nil {
		return nil
	}
	recorder, _ := ctx.Value(recorderContextKey{}).(Recorder)
	return recorder
}

func RecordContext(ctx context.Context, event Event) {
	if !Enabled() {
		return
	}
	event = metadataOnly(event)
	if event.RecordedAt == 0 {
		event.RecordedAt = time.Now().UnixMilli()
	}
	if Enabled() {
		log.Printf("[swarmd.provider.api] provider=%q operation=%q stage=%s extra=%v", event.Provider, event.Operation, event.Stage, event.Extra)
	}
	record(ctx, event)
}

func Enabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(EnvName))) {
	case "1", "true", "yes", "on", "debug":
		return true
	default:
		return false
	}
}

func BoolEnvValue(enabled bool) string {
	if enabled {
		return "1"
	}
	return "0"
}

func LogRequest(provider, operation string, req *http.Request, body []byte) {
	if !Enabled() || req == nil {
		return
	}
	event := Event{Provider: clean(provider), Operation: clean(operation), Stage: "request", Method: clean(req.Method), URL: sanitizeURL(req), Headers: headerNames(req.Header), RecordedAt: time.Now().UnixMilli(), Extra: map[string]any{"request_bytes": len(body)}}
	log.Printf("[swarmd.provider.api] provider=%q operation=%q stage=request method=%q url=%q headers=%q request_bytes=%d", event.Provider, event.Operation, event.Method, event.URL, event.Headers, len(body))
	record(req.Context(), event)
}

func LogResponse(provider, operation string, resp *http.Response, body []byte) {
	if !Enabled() || resp == nil {
		return
	}
	event := Event{Provider: clean(provider), Operation: clean(operation), Stage: "response", StatusCode: resp.StatusCode, Headers: headerNames(resp.Header), RecordedAt: time.Now().UnixMilli(), Extra: map[string]any{"response_bytes": len(body)}}
	log.Printf("[swarmd.provider.api] provider=%q operation=%q stage=response status=%d headers=%q response_bytes=%d", event.Provider, event.Operation, event.StatusCode, event.Headers, len(body))
	ctx := context.Background()
	if resp.Request != nil {
		ctx = resp.Request.Context()
	}
	record(ctx, event)
}

func LogError(provider, operation string, err error) {
	LogErrorContext(context.Background(), provider, operation, err)
}

func LogErrorContext(ctx context.Context, provider, operation string, err error) {
	if !Enabled() || err == nil {
		return
	}
	event := Event{Provider: clean(provider), Operation: clean(operation), Stage: "error", Error: "provider operation failed", RecordedAt: time.Now().UnixMilli(), Extra: map[string]any{"error_type": fmt.Sprintf("%T", err)}}
	log.Printf("[swarmd.provider.api] provider=%q operation=%q stage=error error_type=%q", event.Provider, event.Operation, event.Extra["error_type"])
	record(ctx, event)
}

func LogStreamChunk(provider, operation string, chunk []byte) {
	LogStreamChunkContext(context.Background(), provider, operation, chunk)
}

func LogStreamChunkContext(ctx context.Context, provider, operation string, chunk []byte) {
	if !Enabled() || len(chunk) == 0 {
		return
	}
	event := Event{Provider: clean(provider), Operation: clean(operation), Stage: "stream_chunk", RecordedAt: time.Now().UnixMilli(), Extra: map[string]any{"chunk_bytes": len(chunk)}}
	log.Printf("[swarmd.provider.api] provider=%q operation=%q stage=stream_chunk chunk_bytes=%d", event.Provider, event.Operation, len(chunk))
	record(ctx, event)
}

func LogWebsocketRequest(provider, operation string, url string, headers http.Header, body []byte) {
	LogWebsocketRequestContext(context.Background(), provider, operation, url, headers, body)
}

func LogWebsocketRequestContext(ctx context.Context, provider, operation string, url string, headers http.Header, body []byte) {
	if !Enabled() {
		return
	}
	event := Event{Provider: clean(provider), Operation: clean(operation), Stage: "websocket_request", URL: sanitizeEndpoint(url), Headers: headerNames(headers), RecordedAt: time.Now().UnixMilli(), Extra: map[string]any{"request_bytes": len(body)}}
	log.Printf("[swarmd.provider.api] provider=%q operation=%q stage=websocket_request url=%q headers=%q request_bytes=%d", event.Provider, event.Operation, event.URL, event.Headers, len(body))
	record(ctx, event)
}

func LogWebsocketResponse(provider, operation string, body []byte) {
	LogWebsocketResponseContext(context.Background(), provider, operation, body)
}

func LogWebsocketResponseContext(ctx context.Context, provider, operation string, body []byte) {
	if !Enabled() || len(body) == 0 {
		return
	}
	event := Event{Provider: clean(provider), Operation: clean(operation), Stage: "websocket_response", RecordedAt: time.Now().UnixMilli(), Extra: map[string]any{"response_bytes": len(body)}}
	log.Printf("[swarmd.provider.api] provider=%q operation=%q stage=websocket_response response_bytes=%d", event.Provider, event.Operation, len(body))
	record(ctx, event)
}

func LogWebsocketError(provider, operation string, err error) {
	LogError(provider, operation, err)
}

func LogWebsocketErrorContext(ctx context.Context, provider, operation string, err error) {
	LogErrorContext(ctx, provider, operation, err)
}

func RoundTrip(provider, operation string, next func(*http.Request) (*http.Response, error), req *http.Request) (*http.Response, error) {
	if next == nil {
		next = http.DefaultTransport.RoundTrip
	}
	if !Enabled() || req == nil {
		return next(req)
	}
	LogRequest(provider, operation, req, nil)
	resp, err := next(req)
	if err != nil {
		LogErrorContext(req.Context(), provider, operation, err)
		return resp, err
	}
	if resp == nil || resp.Body == nil {
		LogResponse(provider, operation, resp, nil)
		return resp, nil
	}
	LogResponse(provider, operation, resp, nil)
	return resp, nil
}

func sanitizeURL(req *http.Request) string {
	if req == nil || req.URL == nil {
		return ""
	}
	endpoint := *req.URL
	endpoint.RawQuery = ""
	endpoint.ForceQuery = false
	endpoint.Fragment = ""
	endpoint.User = nil
	return privacy.SanitizeText(endpoint.String())
}

func sanitizeEndpoint(raw string) string {
	req, err := http.NewRequest(http.MethodGet, strings.TrimSpace(raw), nil)
	if err != nil {
		return ""
	}
	return sanitizeURL(req)
}

func headerNames(headers http.Header) string {
	if len(headers) == 0 {
		return "{}"
	}
	keys := make([]string, 0, len(headers))
	for key := range headers {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for index, key := range keys {
		keys[index] = clean(key)
	}
	return strings.Join(keys, ",")
}

func clean(value string) string {
	return privacy.SanitizeText(strings.TrimSpace(value))
}

func metadataOnly(event Event) Event {
	event.Provider = boundedClean(event.Provider, 80)
	event.Operation = boundedClean(event.Operation, 120)
	event.Stage = boundedClean(event.Stage, 80)
	event.Method = boundedClean(event.Method, 24)
	event.URL = boundedClean(event.URL, 512)
	event.Headers = boundedClean(event.Headers, 512)
	event.Error = boundedClean(event.Error, 160)
	event.Body = ""
	event.Extra = metadataExtra(event.Extra)
	return event
}

func boundedClean(value string, max int) string {
	value = clean(value)
	runes := []rune(value)
	if max > 0 && len(runes) > max {
		return string(runes[:max]) + "...[truncated]"
	}
	return value
}

func metadataExtra(extra map[string]any) map[string]any {
	if len(extra) == 0 {
		return nil
	}
	cleaned := make(map[string]any, len(extra))
	for key, value := range extra {
		key = boundedClean(key, 80)
		switch typed := value.(type) {
		case bool:
			cleaned[key] = typed
		case int:
			cleaned[key] = typed
		case int64:
			cleaned[key] = typed
		case float64:
			cleaned[key] = typed
		case string:
			cleaned[key] = boundedClean(typed, 160)
		}
	}
	if len(cleaned) == 0 {
		return nil
	}
	return cleaned
}

func record(ctx context.Context, event Event) {
	if recorder := recorderFromContext(ctx); recorder != nil {
		recorder(ctx, metadataOnly(event))
	}
}

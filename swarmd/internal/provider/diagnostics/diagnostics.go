package diagnostics

import (
	"bytes"
	"context"
	"io"
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
	event := Event{Provider: clean(provider), Operation: clean(operation), Stage: "request", Method: req.Method, URL: sanitizeURL(req), Headers: sanitizeHeaders(req.Header), Body: sanitizeBytes(body), RecordedAt: time.Now().UnixMilli()}
	log.Printf("[swarmd.provider.api] provider=%q operation=%q stage=request method=%q url=%q headers=%q body=%q", event.Provider, event.Operation, event.Method, event.URL, event.Headers, event.Body)
	record(req.Context(), event)
}

func LogResponse(provider, operation string, resp *http.Response, body []byte) {
	if !Enabled() || resp == nil {
		return
	}
	event := Event{Provider: clean(provider), Operation: clean(operation), Stage: "response", StatusCode: resp.StatusCode, Headers: sanitizeHeaders(resp.Header), Body: sanitizeBytes(body), RecordedAt: time.Now().UnixMilli()}
	log.Printf("[swarmd.provider.api] provider=%q operation=%q stage=response status=%d headers=%q body=%q", event.Provider, event.Operation, event.StatusCode, event.Headers, event.Body)
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
	event := Event{Provider: clean(provider), Operation: clean(operation), Stage: "error", Error: privacy.SanitizeText(err.Error()), RecordedAt: time.Now().UnixMilli()}
	log.Printf("[swarmd.provider.api] provider=%q operation=%q stage=error error=%q", event.Provider, event.Operation, event.Error)
	record(ctx, event)
}

func LogStreamChunk(provider, operation string, chunk []byte) {
	LogStreamChunkContext(context.Background(), provider, operation, chunk)
}

func LogStreamChunkContext(ctx context.Context, provider, operation string, chunk []byte) {
	if !Enabled() || len(chunk) == 0 {
		return
	}
	event := Event{Provider: clean(provider), Operation: clean(operation), Stage: "stream_chunk", Body: sanitizeBytes(chunk), RecordedAt: time.Now().UnixMilli()}
	log.Printf("[swarmd.provider.api] provider=%q operation=%q stage=stream_chunk body=%q", event.Provider, event.Operation, event.Body)
	record(ctx, event)
}

func LogWebsocketRequest(provider, operation string, url string, headers http.Header, body []byte) {
	LogWebsocketRequestContext(context.Background(), provider, operation, url, headers, body)
}

func LogWebsocketRequestContext(ctx context.Context, provider, operation string, url string, headers http.Header, body []byte) {
	if !Enabled() {
		return
	}
	event := Event{Provider: clean(provider), Operation: clean(operation), Stage: "websocket_request", URL: privacy.SanitizeText(strings.TrimSpace(url)), Headers: sanitizeHeaders(headers), Body: sanitizeBytes(body), RecordedAt: time.Now().UnixMilli()}
	log.Printf("[swarmd.provider.api] provider=%q operation=%q stage=websocket_request url=%q headers=%q body=%q", event.Provider, event.Operation, event.URL, event.Headers, event.Body)
	record(ctx, event)
}

func LogWebsocketResponse(provider, operation string, body []byte) {
	LogWebsocketResponseContext(context.Background(), provider, operation, body)
}

func LogWebsocketResponseContext(ctx context.Context, provider, operation string, body []byte) {
	if !Enabled() || len(body) == 0 {
		return
	}
	event := Event{Provider: clean(provider), Operation: clean(operation), Stage: "websocket_response", Body: sanitizeBytes(body), RecordedAt: time.Now().UnixMilli()}
	log.Printf("[swarmd.provider.api] provider=%q operation=%q stage=websocket_response body=%q", event.Provider, event.Operation, event.Body)
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
	body, restoreErr := readAndRestoreRequestBody(req)
	if restoreErr != nil {
		LogErrorContext(req.Context(), provider, operation, restoreErr)
		return nil, restoreErr
	}
	LogRequest(provider, operation, req, body)
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
	resp.Body = &loggingReadCloser{ctx: req.Context(), provider: provider, operation: operation, rc: resp.Body}
	return resp, nil
}

type loggingReadCloser struct {
	ctx       context.Context
	provider  string
	operation string
	rc        io.ReadCloser
}

func (r *loggingReadCloser) Read(p []byte) (int, error) {
	if r == nil || r.rc == nil {
		return 0, io.EOF
	}
	n, err := r.rc.Read(p)
	if n > 0 {
		LogStreamChunkContext(r.ctx, r.provider, r.operation, p[:n])
	}
	if err != nil && err != io.EOF {
		LogErrorContext(r.ctx, r.provider, r.operation, err)
	}
	return n, err
}

func (r *loggingReadCloser) Close() error {
	if r == nil || r.rc == nil {
		return nil
	}
	err := r.rc.Close()
	if err != nil {
		LogErrorContext(r.ctx, r.provider, r.operation, err)
	}
	return err
}

func readAndRestoreRequestBody(req *http.Request) ([]byte, error) {
	if req == nil || req.Body == nil {
		return nil, nil
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	if err := req.Body.Close(); err != nil {
		return nil, err
	}
	req.Body = io.NopCloser(bytes.NewReader(body))
	return body, nil
}

func sanitizeURL(req *http.Request) string {
	if req == nil || req.URL == nil {
		return ""
	}
	return privacy.SanitizeText(req.URL.String())
}

func sanitizeHeaders(headers http.Header) string {
	if len(headers) == 0 {
		return "{}"
	}
	keys := make([]string, 0, len(headers))
	for key := range headers {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		values := headers.Values(key)
		if sensitiveHeader(key) {
			values = []string{"[REDACTED]"}
		} else {
			cleanValues := make([]string, 0, len(values))
			for _, value := range values {
				cleanValues = append(cleanValues, privacy.SanitizeText(value))
			}
			values = cleanValues
		}
		parts = append(parts, key+"="+strings.Join(values, ","))
	}
	return strings.Join(parts, "; ")
}

func sensitiveHeader(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "authorization", "proxy-authorization", "x-goog-api-key", "x-api-key", "api-key", "cookie", "set-cookie":
		return true
	default:
		return false
	}
}

func sanitizeBytes(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	return privacy.SanitizeText(string(body))
}

func clean(value string) string {
	return privacy.SanitizeText(strings.TrimSpace(value))
}

func record(ctx context.Context, event Event) {
	if recorder := recorderFromContext(ctx); recorder != nil {
		recorder(ctx, event)
	}
}

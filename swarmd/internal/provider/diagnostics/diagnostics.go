package diagnostics

import (
	"bytes"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"

	"swarm/packages/swarmd/internal/privacy"
)

const EnvName = "SWARM_PROVIDER_API_DIAGNOSTICS"

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
	log.Printf("[swarmd.provider.api] provider=%q operation=%q stage=request method=%q url=%q headers=%q body=%q", clean(provider), clean(operation), req.Method, sanitizeURL(req), sanitizeHeaders(req.Header), sanitizeBytes(body))
}

func LogResponse(provider, operation string, resp *http.Response, body []byte) {
	if !Enabled() || resp == nil {
		return
	}
	log.Printf("[swarmd.provider.api] provider=%q operation=%q stage=response status=%d headers=%q body=%q", clean(provider), clean(operation), resp.StatusCode, sanitizeHeaders(resp.Header), sanitizeBytes(body))
}

func LogError(provider, operation string, err error) {
	if !Enabled() || err == nil {
		return
	}
	log.Printf("[swarmd.provider.api] provider=%q operation=%q stage=error error=%q", clean(provider), clean(operation), privacy.SanitizeText(err.Error()))
}

func LogStreamChunk(provider, operation string, chunk []byte) {
	if !Enabled() || len(chunk) == 0 {
		return
	}
	log.Printf("[swarmd.provider.api] provider=%q operation=%q stage=stream_chunk body=%q", clean(provider), clean(operation), sanitizeBytes(chunk))
}

func LogWebsocketRequest(provider, operation string, url string, headers http.Header, body []byte) {
	if !Enabled() {
		return
	}
	log.Printf("[swarmd.provider.api] provider=%q operation=%q stage=websocket_request url=%q headers=%q body=%q", clean(provider), clean(operation), privacy.SanitizeText(strings.TrimSpace(url)), sanitizeHeaders(headers), sanitizeBytes(body))
}

func LogWebsocketResponse(provider, operation string, body []byte) {
	if !Enabled() || len(body) == 0 {
		return
	}
	log.Printf("[swarmd.provider.api] provider=%q operation=%q stage=websocket_response body=%q", clean(provider), clean(operation), sanitizeBytes(body))
}

func LogWebsocketError(provider, operation string, err error) {
	LogError(provider, operation, err)
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
		LogError(provider, operation, restoreErr)
		return nil, restoreErr
	}
	LogRequest(provider, operation, req, body)
	resp, err := next(req)
	if err != nil {
		LogError(provider, operation, err)
		return resp, err
	}
	if resp == nil || resp.Body == nil {
		LogResponse(provider, operation, resp, nil)
		return resp, nil
	}
	LogResponse(provider, operation, resp, nil)
	resp.Body = &loggingReadCloser{provider: provider, operation: operation, rc: resp.Body}
	return resp, nil
}

type loggingReadCloser struct {
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
		LogStreamChunk(r.provider, r.operation, p[:n])
	}
	if err != nil && err != io.EOF {
		LogError(r.provider, r.operation, err)
	}
	return n, err
}

func (r *loggingReadCloser) Close() error {
	if r == nil || r.rc == nil {
		return nil
	}
	err := r.rc.Close()
	if err != nil {
		LogError(r.provider, r.operation, err)
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

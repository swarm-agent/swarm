package codex

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"

	providerdiagnostics "swarm/packages/swarmd/internal/provider/diagnostics"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Only rejected handshakes can switch transports. A 5xx error frame on an
// established socket is not proof that replaying the request is safe.
func codexHandshakeShouldFallbackHTTP(status int, decoded map[string]any) bool {
	failed, _ := decoded["_swarm_websocket_handshake_failed"].(bool)
	return failed && (status >= 500 && status <= 599 || status == http.StatusUpgradeRequired)
}

// sendCodexResponsesHTTP is a request-scoped recovery, never a sticky transport
// preference. Callers exhaust WebSocket retries first and retain the full input
// rather than a socket-local previous_response_id continuation.
func (c *Client) sendCodexResponsesHTTP(ctx context.Context, record pebblestore.CodexAuthRecord, payload []byte, onEvent func(StreamEvent)) (map[string]any, int, error) {
	endpoint, err := url.Parse(c.responsesEndpointForRecord(record))
	if err != nil {
		return nil, 0, err
	}
	switch endpoint.Scheme {
	case "wss":
		endpoint.Scheme = "https"
	case "ws":
		endpoint.Scheme = "http"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(payload))
	if err != nil {
		return nil, 0, err
	}
	req.Header = buildCodexTransportHeaders(record, codexTransportContextFromContext(ctx))
	req.Header.Del(openAIBetaHeader)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	providerdiagnostics.RecordContext(ctx, providerdiagnostics.Event{Provider: "codex", Operation: "responses.http", Stage: "websocket_handshake_fallback"})
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	provideriface.ReportAttemptActivity(ctx, provideriface.AttemptActivityResponseRead)
	if resp.StatusCode >= http.StatusBadRequest {
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxCodexResponseBodyBytes))
		if err != nil {
			return nil, resp.StatusCode, err
		}
		return annotateCodexTransportMetadata(map[string]any{"raw_body": sanitizeDiagnosticText(string(body))}, codexTransportResponsesHTTP, false), resp.StatusCode, nil
	}
	var decoded map[string]any
	if strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
		decoded, err = parseEventStreamReader(resp.Body, onEvent)
	} else {
		var body []byte
		body, err = io.ReadAll(io.LimitReader(resp.Body, maxCodexResponseBodyBytes))
		if err == nil {
			decoded, err = parseOpenAIResponsesBody(body, resp.Header.Get("Content-Type"), onEvent)
		}
	}
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return annotateCodexTransportMetadata(decoded, codexTransportResponsesHTTP, false), resp.StatusCode, nil
}

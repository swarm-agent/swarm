package google

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	providerdiagnostics "swarm/packages/swarmd/internal/provider/diagnostics"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
)

// generateContentSurface is one HTTP surface that serves the Gemini
// generateContent schema: the Gemini API (API key header) or Vertex AI (OAuth
// bearer token from the machine's Google identity). Request building, response
// and stream parsing, and the retry policy are shared; only the endpoint,
// authorization and the labels used for diagnostics and usage differ.
type generateContentSurface struct {
	// providerID labels diagnostics, error messages and usage ("google", "vertex").
	providerID string
	// usageSource is the TokenUsage.Source this surface reports.
	usageSource string
	// endpoint returns the URL for a model and method ("generateContent" or
	// "streamGenerateContent").
	endpoint func(modelID, method string) (string, error)
	// authorize sets credentials on one attempt's request.
	authorize func(ctx context.Context, req *http.Request) error
	// refreshAuth drops cached credentials after a 401 so one more attempt
	// fetches fresh ones. Nil when credentials are static (an API key).
	refreshAuth func()
	// statusDetail optionally explains a failed status to the operator. It
	// must never include credential material.
	statusDetail func(statusCode int) string
	// serviceTierHeader names the response header carrying the served service
	// tier; empty when the surface has none.
	serviceTierHeader string
}

// generateContentTransport sends Gemini generateContent requests with bounded
// retries. Both the Gemini API runner and the Vertex AI runner embed it.
type generateContentTransport struct {
	httpClient *http.Client
	retryDelay func(attempt int) time.Duration
}

func (s generateContentSurface) statusError(method string, statusCode int, body []byte) error {
	message := fmt.Sprintf("%s %s failed status=%d", s.providerID, method, statusCode)
	if s.statusDetail != nil {
		if hint := strings.TrimSpace(s.statusDetail(statusCode)); hint != "" {
			message += " (" + hint + ")"
		}
	}
	if detail := strings.TrimSpace(sanitizeGoogleText(string(body))); detail != "" {
		message += " body=" + detail
	}
	return errors.New(message)
}

// retryUnauthorized reports whether a 401 earns one more attempt with freshly
// fetched credentials. It refreshes at most once per request.
func (s generateContentSurface) retryUnauthorized(statusCode int, refreshed *bool) bool {
	if statusCode != http.StatusUnauthorized || s.refreshAuth == nil || *refreshed {
		return false
	}
	*refreshed = true
	s.refreshAuth()
	return true
}

func (s generateContentSurface) servedTier(resp *http.Response) string {
	if s.serviceTierHeader == "" || resp == nil {
		return ""
	}
	return resp.Header.Get(s.serviceTierHeader)
}

func (s generateContentSurface) labelUsage(usage *provideriface.TokenUsage) {
	if usage != nil && s.usageSource != "" && strings.TrimSpace(usage.Source) != "" {
		usage.Source = s.usageSource
	}
}

func (t *generateContentTransport) client() *http.Client {
	if t.httpClient != nil {
		return t.httpClient
	}
	return http.DefaultClient
}

func (t *generateContentTransport) generate(ctx context.Context, surface generateContentSurface, modelID string, requestPayload googleRequest) (provideriface.Response, error) {
	const method = "generateContent"
	raw, err := json.Marshal(requestPayload)
	if err != nil {
		return provideriface.Response{}, fmt.Errorf("marshal %s request: %w", surface.providerID, err)
	}
	endpoint, err := surface.endpoint(modelID, method)
	if err != nil {
		return provideriface.Response{}, err
	}
	var (
		resp       *http.Response
		body       []byte
		refreshed  bool
		retryNow   bool
		providerID = surface.providerID
	)
	for attempt := 0; attempt <= googleTransientMaxRetries; attempt++ {
		if attempt > 0 && !retryNow {
			delay := t.retryWait(attempt, resp)
			if err := sleepWithContext(ctx, delay); err != nil {
				return provideriface.Response{}, err
			}
		}
		retryNow = false
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
		if err != nil {
			return provideriface.Response{}, err
		}
		httpReq.Header.Set("Content-Type", "application/json")
		if err := surface.authorize(ctx, httpReq); err != nil {
			return provideriface.Response{}, err
		}

		providerdiagnostics.LogRequest(providerID, method, httpReq, raw)
		resp, err = t.client().Do(httpReq)
		if err != nil {
			providerdiagnostics.LogErrorContext(ctx, providerID, method, err)
			return provideriface.Response{}, sanitizeGoogleError(providerID+" generateContent request failed", err)
		}

		body, err = io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
		resp.Body.Close()
		providerdiagnostics.LogResponse(providerID, method, resp, body)
		if err != nil {
			providerdiagnostics.LogErrorContext(ctx, providerID, method, err)
			return provideriface.Response{}, sanitizeGoogleError("read "+providerID+" generateContent response", err)
		}
		if attempt < googleTransientMaxRetries && surface.retryUnauthorized(resp.StatusCode, &refreshed) {
			retryNow = true
			continue
		}
		if googleTransientStatus(resp.StatusCode) && attempt < googleTransientMaxRetries {
			continue
		}
		if resp.StatusCode >= http.StatusBadRequest {
			return provideriface.Response{}, surface.statusError(method, resp.StatusCode, body)
		}

		var decoded googleResponse
		if err := json.Unmarshal(body, &decoded); err != nil {
			return provideriface.Response{}, sanitizeGoogleError("decode "+providerID+" response", err)
		}
		result := parseGoogleResponse(decoded)
		if googleResponseEmpty(result) && attempt < googleTransientMaxRetries {
			providerdiagnostics.LogErrorContext(ctx, providerID, method, errors.New("empty response; retrying"))
			continue
		}
		annotateGoogleServiceTier(&result.Usage, requestPayload.ServiceTier, surface.servedTier(resp))
		surface.labelUsage(&result.Usage)
		result.Model = modelID
		return result, nil
	}
	return provideriface.Response{}, surface.statusError(method, resp.StatusCode, body)
}

func (t *generateContentTransport) stream(ctx context.Context, surface generateContentSurface, modelID string, requestPayload googleRequest, onEvent func(provideriface.StreamEvent)) (provideriface.Response, error) {
	const method = "streamGenerateContent"
	raw, err := json.Marshal(requestPayload)
	if err != nil {
		return provideriface.Response{}, fmt.Errorf("marshal %s request: %w", surface.providerID, err)
	}
	endpoint, err := surface.endpoint(modelID, method)
	if err != nil {
		return provideriface.Response{}, err
	}
	var (
		resp       *http.Response
		body       []byte
		refreshed  bool
		retryNow   bool
		providerID = surface.providerID
	)
	for attempt := 0; attempt <= googleTransientMaxRetries; attempt++ {
		if attempt > 0 && !retryNow {
			delay := t.retryWait(attempt, resp)
			if err := sleepWithContext(ctx, delay); err != nil {
				return provideriface.Response{}, err
			}
		}
		retryNow = false
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
		if err != nil {
			return provideriface.Response{}, err
		}
		httpReq.Header.Set("Accept", "text/event-stream")
		httpReq.Header.Set("Content-Type", "application/json")
		query := httpReq.URL.Query()
		query.Set("alt", "sse")
		httpReq.URL.RawQuery = query.Encode()
		if err := surface.authorize(ctx, httpReq); err != nil {
			return provideriface.Response{}, err
		}

		providerdiagnostics.LogRequest(providerID, method, httpReq, raw)
		resp, err = t.client().Do(httpReq)
		if err != nil {
			providerdiagnostics.LogErrorContext(ctx, providerID, method, err)
			return provideriface.Response{}, sanitizeGoogleError(providerID+" streamGenerateContent request failed", err)
		}

		if attempt < googleTransientMaxRetries && surface.retryUnauthorized(resp.StatusCode, &refreshed) {
			body, _ = io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
			resp.Body.Close()
			providerdiagnostics.LogResponse(providerID, method, resp, body)
			retryNow = true
			continue
		}
		if googleTransientStatus(resp.StatusCode) && attempt < googleTransientMaxRetries {
			body, _ = io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
			resp.Body.Close()
			providerdiagnostics.LogResponse(providerID, method, resp, body)
			continue
		}

		if resp.StatusCode >= http.StatusBadRequest {
			body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
			resp.Body.Close()
			providerdiagnostics.LogResponse(providerID, method, resp, body)
			if readErr != nil {
				providerdiagnostics.LogErrorContext(ctx, providerID, method, readErr)
				return provideriface.Response{}, sanitizeGoogleError("read "+providerID+" streamGenerateContent error response", readErr)
			}
			return provideriface.Response{}, surface.statusError(method, resp.StatusCode, body)
		}

		providerdiagnostics.LogResponse(providerID, method, resp, nil)
		headerServiceTier := surface.servedTier(resp)
		accumulator := newGoogleStreamAccumulator(modelID)
		// Answer text already shown to the caller rules out a retry; reasoning
		// summaries alone do not.
		textStreamed := false
		forward := onEvent
		if onEvent != nil {
			forward = func(event provideriface.StreamEvent) {
				if event.Type == provideriface.StreamEventOutputTextDelta && event.Delta != "" {
					textStreamed = true
				}
				onEvent(event)
			}
		}
		parseErr := parseGoogleEventStream(resp.Body, func(payload string) error {
			providerdiagnostics.LogStreamChunkContext(ctx, providerID, method, []byte(payload))
			return accumulator.applyPayload(payload, forward)
		})
		resp.Body.Close()
		if parseErr != nil {
			providerdiagnostics.LogErrorContext(ctx, providerID, method, parseErr)
			return provideriface.Response{}, sanitizeGoogleError("decode "+providerID+" stream response", parseErr)
		}
		if !accumulator.finished {
			return provideriface.Response{}, errors.New(providerID + " stream ended without a finish reason")
		}
		if !textStreamed && attempt < googleTransientMaxRetries && googleResponseEmpty(accumulator.response()) {
			providerdiagnostics.LogErrorContext(ctx, providerID, method, errors.New("empty response; retrying"))
			continue
		}
		servedTier, err := reconcileGoogleServiceTierSignals(headerServiceTier, accumulator.serviceTier)
		if err != nil {
			providerdiagnostics.LogErrorContext(ctx, providerID, method, err)
			return provideriface.Response{}, err
		}
		result := accumulator.response()
		annotateGoogleServiceTier(&result.Usage, requestPayload.ServiceTier, servedTier)
		surface.labelUsage(&result.Usage)
		return result, nil
	}
	return provideriface.Response{}, surface.statusError(method, resp.StatusCode, body)
}

// googleTransientStatus reports provider statuses worth retrying before any
// output exists: rate limiting and transient server or gateway failures.
// Request, auth and not-found errors are returned at once.
func googleTransientStatus(code int) bool {
	switch code {
	case http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

// googleResponseEmpty reports a successful Gemini reply with nothing in it: no
// text, no assistant message and no function call. Gemini occasionally answers
// that way; the run would then fail with "no final assistant content", so the
// runner retries it (bounded, like transient statuses) while no answer text has
// reached the caller.
func googleResponseEmpty(resp provideriface.Response) bool {
	if strings.TrimSpace(resp.Text) != "" || len(resp.FunctionCalls) > 0 || resp.RestartTurn {
		return false
	}
	for _, message := range resp.AssistantMessages {
		if strings.TrimSpace(message.Text) != "" {
			return false
		}
	}
	return true
}

func (t *generateContentTransport) retryWait(attempt int, resp *http.Response) time.Duration {
	if t != nil && t.retryDelay != nil {
		return t.retryDelay(attempt)
	}
	if resp != nil {
		if retryAfter := resp.Header.Get("Retry-After"); retryAfter != "" {
			if secs, err := strconv.Atoi(strings.TrimSpace(retryAfter)); err == nil && secs > 0 && secs <= 30 {
				return time.Duration(secs) * time.Second
			}
		}
	}
	multiplier := 1 << uint(attempt-1)
	if multiplier <= 0 || multiplier > 16 {
		multiplier = 16
	}
	return googleTransientBaseDelay * time.Duration(multiplier)
}

func sleepWithContext(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

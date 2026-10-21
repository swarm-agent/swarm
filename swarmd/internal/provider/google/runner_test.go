package google

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type googleRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn googleRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func newGoogleTransportTestRunner(t *testing.T, transport http.RoundTripper) (*Runner, context.Context) {
	t.Helper()
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "google-transport.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	authStore := pebblestore.NewAuthStore(store)
	if _, err := authStore.UpsertCredential(pebblestore.AuthCredentialInput{AccountScopeID: "account-1", ID: "credential-1", Provider: "google", Type: pebblestore.AuthTypeAPI, APIKey: "test-key", SetActive: true}); err != nil {
		t.Fatalf("seed Google credential: %v", err)
	}
	runner := NewRunner(authStore)
	runner.httpClient = &http.Client{Transport: transport}
	ctx := identity.ContextWithPrincipal(context.Background(), identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user-1", AccountScopeID: "account-1"})
	return runner, ctx
}

func googleStatusResponse(req *http.Request, status int, header http.Header) *http.Response {
	if header == nil {
		header = http.Header{}
	}
	header.Set("Content-Type", "application/json")
	return &http.Response{
		StatusCode: status,
		Header:     header,
		Body:       io.NopCloser(strings.NewReader(fmt.Sprintf(`{"error":{"code":%d,"message":"transient test failure"}}`, status))),
		Request:    req,
	}
}

func TestGoogleTransientStatusesRetryBeforeOutput(t *testing.T) {
	// Purpose:
	// - Requirement: rate limiting (429) and transient server/gateway failures (500, 502, 503, 504)
	//   from Gemini are retried, bounded by googleTransientMaxRetries, for unary and streaming calls.
	// - Threat/regression: a public chat agent on Gemini fails a visitor's message on the first
	//   429/5xx even though the provider recovers within seconds (previously only 503 retried).
	// - Boundary/authority: googleTransientStatus, Runner.createResponse and
	//   Runner.createStreamingResponse in provider/google/runner.go.
	// - Narrowest test layer: Runner round-trip transport; the retry decision is entirely inside the
	//   runner, before any response bytes reach the caller.
	for _, status := range []int{http.StatusTooManyRequests, http.StatusInternalServerError,
		http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout} {
		for _, streaming := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/streaming=%v", status, streaming), func(t *testing.T) {
				calls := 0
				runner, ctx := newGoogleTransportTestRunner(t, googleRoundTripFunc(func(req *http.Request) (*http.Response, error) {
					calls++
					if calls < 3 {
						return googleStatusResponse(req, status, nil), nil
					}
					if streaming {
						return &http.Response{StatusCode: http.StatusOK, Request: req,
							Header: http.Header{"Content-Type": []string{"text/event-stream"}},
							Body:   io.NopCloser(strings.NewReader("data: {\"candidates\":[{\"finishReason\":\"STOP\",\"content\":{\"parts\":[{\"text\":\"recovered\"}]}}]}\n\n"))}, nil
					}
					return &http.Response{StatusCode: http.StatusOK, Request: req,
						Header: http.Header{"Content-Type": []string{"application/json"}},
						Body:   io.NopCloser(strings.NewReader(`{"candidates":[{"finishReason":"STOP","content":{"parts":[{"text":"recovered"}]}}]}`))}, nil
				}))
				runner.retryDelay = func(int) time.Duration { return 0 }
				request := provideriface.Request{Model: "gemini-test", Input: []map[string]any{{"role": "user", "content": "hello"}}}
				var resp provideriface.Response
				var err error
				if streaming {
					resp, err = runner.CreateResponseStreaming(ctx, request, nil)
				} else {
					resp, err = runner.CreateResponse(ctx, request)
				}
				if err != nil {
					t.Fatalf("request failed after transient %d: %v", status, err)
				}
				if calls != 3 || resp.Text != "recovered" {
					t.Fatalf("calls=%d text=%q, want 3 calls and the recovered response", calls, resp.Text)
				}
			})
		}
	}
}

func TestGoogleEmptyRepliesRetryBeforeOutput(t *testing.T) {
	// Purpose:
	// - Requirement: a successful Gemini reply with no text, assistant message or function call is
	//   retried (bounded by googleTransientMaxRetries) for unary and streaming calls, while no answer
	//   text has reached the caller; a reply that only calls a tool is returned as is.
	// - Threat/regression: on a live public chat about 1 in 30 Gemini runs came back empty and the
	//   visitor's message failed with "google returned no final assistant content".
	// - Boundary/authority: googleResponseEmpty and both Runner request loops in runner.go.
	// - Narrowest test layer: Runner round-trip transport counting attempts.
	const empty = `{"candidates":[{"finishReason":"STOP","content":{"role":"model","parts":[]}}]}`
	const recovered = `{"candidates":[{"finishReason":"STOP","content":{"parts":[{"text":"recovered"}]}}]}`
	const toolOnly = `{"candidates":[{"finishReason":"STOP","content":{"parts":[{"functionCall":{"name":"get_playbook","args":{"topic":"pricing"}}}]}}]}`
	reply := func(req *http.Request, streaming bool, payload string) *http.Response {
		if streaming {
			return &http.Response{StatusCode: http.StatusOK, Request: req,
				Header: http.Header{"Content-Type": []string{"text/event-stream"}},
				Body:   io.NopCloser(strings.NewReader("data: " + payload + "\n\n"))}
		}
		return &http.Response{StatusCode: http.StatusOK, Request: req,
			Header: http.Header{"Content-Type": []string{"application/json"}},
			Body:   io.NopCloser(strings.NewReader(payload))}
	}
	request := provideriface.Request{Model: "gemini-test", Input: []map[string]any{{"role": "user", "content": "hello"}}}
	call := func(runner *Runner, ctx context.Context, streaming bool) (provideriface.Response, error) {
		if streaming {
			return runner.CreateResponseStreaming(ctx, request, func(provideriface.StreamEvent) {})
		}
		return runner.CreateResponse(ctx, request)
	}
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprintf("recovers/streaming=%v", streaming), func(t *testing.T) {
			calls := 0
			runner, ctx := newGoogleTransportTestRunner(t, googleRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				if calls < 3 {
					return reply(req, streaming, empty), nil
				}
				return reply(req, streaming, recovered), nil
			}))
			runner.retryDelay = func(int) time.Duration { return 0 }
			resp, err := call(runner, ctx, streaming)
			if err != nil || calls != 3 || resp.Text != "recovered" {
				t.Fatalf("calls=%d text=%q err=%v, want 3 calls and the recovered reply", calls, resp.Text, err)
			}
		})
		t.Run(fmt.Sprintf("bounded/streaming=%v", streaming), func(t *testing.T) {
			calls := 0
			runner, ctx := newGoogleTransportTestRunner(t, googleRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				return reply(req, streaming, empty), nil
			}))
			runner.retryDelay = func(int) time.Duration { return 0 }
			resp, err := call(runner, ctx, streaming)
			if err != nil || calls != googleTransientMaxRetries+1 || !googleResponseEmpty(resp) {
				t.Fatalf("calls=%d err=%v, want %d calls then the empty reply for the run to classify", calls, err, googleTransientMaxRetries+1)
			}
		})
		t.Run(fmt.Sprintf("tool call is not retried/streaming=%v", streaming), func(t *testing.T) {
			calls := 0
			runner, ctx := newGoogleTransportTestRunner(t, googleRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				return reply(req, streaming, toolOnly), nil
			}))
			runner.retryDelay = func(int) time.Duration { return 0 }
			resp, err := call(runner, ctx, streaming)
			if err != nil || calls != 1 || len(resp.FunctionCalls) != 1 {
				t.Fatalf("calls=%d function_calls=%d err=%v, want one call returning the tool call", calls, len(resp.FunctionCalls), err)
			}
		})
	}
}

func TestGooglePermanentStatusesAreNotRetried(t *testing.T) {
	// Purpose:
	// - Requirement: request, auth, permission and not-found failures are returned on the first
	//   attempt, with their real status, for unary and streaming calls.
	// - Threat/regression: widening retries to 429/5xx must not make a bad key or invalid request
	//   cost four provider calls and four delays per visitor message, or hide the real status.
	// - Boundary/authority: googleTransientStatus and both Runner request loops.
	// - Narrowest test layer: Runner round-trip transport counting attempts.
	for _, status := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound} {
		for _, streaming := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/streaming=%v", status, streaming), func(t *testing.T) {
				calls := 0
				runner, ctx := newGoogleTransportTestRunner(t, googleRoundTripFunc(func(req *http.Request) (*http.Response, error) {
					calls++
					return googleStatusResponse(req, status, nil), nil
				}))
				runner.retryDelay = func(int) time.Duration { return 0 }
				request := provideriface.Request{Model: "gemini-test", Input: []map[string]any{{"role": "user", "content": "hello"}}}
				var err error
				if streaming {
					_, err = runner.CreateResponseStreaming(ctx, request, nil)
				} else {
					_, err = runner.CreateResponse(ctx, request)
				}
				if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("status=%d", status)) {
					t.Fatalf("err = %v, want status=%d", err, status)
				}
				if calls != 1 {
					t.Fatalf("calls = %d, want 1 (no retry for %d)", calls, status)
				}
			})
		}
	}
}

func TestGoogleRetryWaitHonorsBoundedRetryAfter(t *testing.T) {
	// Purpose:
	// - Requirement: a 429/5xx Retry-After of 1..30 seconds sets the wait; anything else falls back
	//   to bounded exponential backoff, so a provider cannot stall a run indefinitely.
	// - Threat/regression: ignoring Retry-After on 429 re-hits a rate limit immediately; trusting an
	//   unbounded value would hold a visitor's message far past the sealed run deadline.
	// - Boundary/authority: Runner.retryWait in provider/google/runner.go.
	// - Narrowest test layer: direct call with a constructed response; no transport needed.
	runner := &Runner{}
	for _, tc := range []struct {
		header  string
		attempt int
		want    time.Duration
	}{
		{"7", 1, 7 * time.Second},
		{"30", 2, 30 * time.Second},
		{"31", 1, googleTransientBaseDelay},
		{"soon", 2, 2 * googleTransientBaseDelay},
		{"", 3, 4 * googleTransientBaseDelay},
	} {
		got := runner.retryWait(tc.attempt, googleStatusResponse(nil, http.StatusTooManyRequests, http.Header{"Retry-After": []string{tc.header}}))
		if got != tc.want {
			t.Fatalf("Retry-After %q attempt %d: wait = %v, want %v", tc.header, tc.attempt, got, tc.want)
		}
	}
}

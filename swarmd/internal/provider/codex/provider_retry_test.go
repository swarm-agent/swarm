package codex

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"syscall"
	"testing"
	"time"

	store "swarm/packages/swarmd/internal/store/pebble"
)

type retryTransport func(*http.Request) (*http.Response, error)

func (f retryTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Purpose: sendRequest owns same-turn recovery, not worker re-execution. A fake
// HTTP transport is the narrowest boundary proving fallback 503 recovery,
// bounded exhaustion, non-transient rejection, redacted visibility and immediate
// cancellation without provider credentials or real backoff delays.
func TestProviderTurnRetryHTTP(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		recover bool
		cancel  bool
		calls   int
	}{
		{"recover", 503, true, false, 2}, {"exhaust", 503, false, false, 4},
		{"unauthorized", 401, false, false, 1}, {"forbidden", 403, false, false, 1},
		{"cancel", 503, false, true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			calls, waits := 0, 0
			var notices []string
			c := &Client{responsesWSURL: "https://provider.invalid/responses"}
			c.sendWSFn = func(context.Context, store.CodexAuthRecord, []byte, func(StreamEvent)) (map[string]any, int, error) {
				return map[string]any{"_swarm_websocket_handshake_failed": true}, 426, nil
			}
			c.reconnectWaitFn = func(ctx context.Context, d time.Duration) error {
				waits++
				if d != 40*time.Second {
					t.Errorf("capped Retry-After = %s", d)
				}
				if tc.cancel {
					cancel()
					return sleepWithContext(ctx, d)
				}
				return nil
			}
			c.httpClient = &http.Client{Transport: retryTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				status := tc.status
				body := "private error body"
				if tc.recover && calls > 1 {
					status = 200
					body = "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n"
				}
				return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"text/event-stream"}, "Retry-After": []string{"999"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
			})}
			_, status, err := c.sendRequest(ctx, store.CodexAuthRecord{Type: store.CodexAuthTypeOAuth}, Request{Model: "test-model", Input: []map[string]any{{"role": "user", "content": "hello"}}}, func(e StreamEvent) {
				if e.Type == StreamEventAssistantCommentary {
					notices = append(notices, e.Delta)
				}
			})
			if calls != tc.calls {
				t.Errorf("calls=%d want %d", calls, tc.calls)
			}
			if tc.cancel {
				if !errors.Is(err, context.Canceled) {
					t.Errorf("cancel error=%v", err)
				}
			} else if tc.name == "exhaust" {
				if err == nil || err.Error() != "provider unavailable (HTTP 503) after 4 attempts" {
					t.Errorf("exhaustion=%v", err)
				}
			} else if tc.recover {
				if err != nil || status != 200 {
					t.Errorf("recovery=%d %v", status, err)
				}
			} else if err != nil || status != tc.status {
				t.Errorf("non-transient=%d %v", status, err)
			}
			if len(notices) != waits {
				t.Errorf("notices=%v waits=%d", notices, waits)
			}
			for i, n := range notices {
				if strings.Contains(n, "private") || !strings.Contains(n, "HTTP 503") || !strings.Contains(n, fmt.Sprintf("attempt %d/4", i+2)) {
					t.Errorf("unsafe/missing retry notice %q", n)
				}
			}
		})
	}
}

// Purpose: sendRequest must preserve its existing replay fence across outer
// retries: duplicate text is suppressed, tool construction only commits once,
// and divergent replay fails closed. Injecting WebSocket stream events is the
// narrowest test of the turn boundary without executing tools or changing Git.
func TestProviderTurnRetryReplay(t *testing.T) {
	for _, diverge := range []bool{false, true} {
		t.Run(fmt.Sprint(diverge), func(t *testing.T) {
			calls := 0
			var text string
			tools := 0
			c := &Client{reconnectWaitFn: func(context.Context, time.Duration) error { return nil }}
			c.sendWSFn = func(ctx context.Context, _ store.CodexAuthRecord, _ []byte, emit func(StreamEvent)) (map[string]any, int, error) {
				calls++
				if calls == 1 {
					emit(StreamEvent{Type: StreamEventOutputTextDelta, Delta: "partial"})
					emit(StreamEvent{Type: StreamEventToolCallCompleted})
					return nil, 0, syscall.ECONNRESET
				}
				delta := "partial done"
				if diverge {
					delta = "different"
				}
				emit(StreamEvent{Type: StreamEventOutputTextDelta, Delta: delta})
				emit(StreamEvent{Type: StreamEventToolCallCompleted})
				return map[string]any{"status": "completed"}, 200, nil
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_, _, err := c.sendRequest(ctx, store.CodexAuthRecord{Type: store.CodexAuthTypeOAuth}, Request{Model: "test-model", Input: []map[string]any{{"role": "user", "content": "hello"}}}, func(e StreamEvent) {
				if e.Type == StreamEventOutputTextDelta {
					text += e.Delta
				}
				if e.Type == StreamEventToolCallCompleted {
					tools++
				}
			})
			if diverge {
				if err == nil || tools != 0 || text != "partial" {
					t.Fatalf("unsafe divergence: text=%q tools=%d err=%v", text, tools, err)
				}
			} else if err != nil || tools != 1 || text != "partial done" {
				t.Fatalf("replay: text=%q tools=%d err=%v", text, tools, err)
			}
		})
	}
}

// Purpose: transientProviderFailure and providerRetryDelay own the retry policy.
// Table tests prevent permanent failures from looping and cap Retry-After while
// checking transport interruptions; this is the narrowest classification layer.
func TestProviderTurnRetryPolicy(t *testing.T) {
	for _, status := range []int{400, 401, 403, 404, 413, 422, 429, 500, 501, 502, 503, 504} {
		want := status == 429 || status == 500 || status == 502 || status == 503 || status == 504
		if got := transientProviderFailure(status, nil); got != want {
			t.Errorf("status %d retry=%v", status, got)
		}
	}
	for _, err := range []error{io.EOF, io.ErrUnexpectedEOF, syscall.ECONNRESET, errResponseStreamIncomplete} {
		if !transientProviderFailure(0, err) {
			t.Errorf("not transient: %v", err)
		}
	}
	for _, err := range []error{context.Canceled, context.DeadlineExceeded, errors.New("invalid request"), errors.New("context length exceeded")} {
		if transientProviderFailure(0, err) || transientProviderFailure(503, err) && errors.Is(err, context.Canceled) {
			t.Errorf("retried: %v", err)
		}
	}
	for i, want := range []time.Duration{10 * time.Second, 20 * time.Second, 40 * time.Second} {
		if got := providerRetryDelay(i+1, nil); got != want {
			t.Errorf("delay=%s want=%s", got, want)
		}
	}
	if got := providerRetryDelay(1, map[string]any{providerRetryAfterKey: "2"}); got != 2*time.Second {
		t.Errorf("Retry-After=%s", got)
	}
	if got := providerRetryDelay(1, map[string]any{providerRetryAfterKey: time.Now().Add(time.Hour).UTC().Format(http.TimeFormat)}); got != 40*time.Second {
		t.Errorf("date cap=%s", got)
	}
}

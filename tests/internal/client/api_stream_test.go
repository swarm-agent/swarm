package client

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func TestRunSessionStreamAllowsUnexpectedEOFAfterCompletion(t *testing.T) {
	streamBody := strings.Join([]string{
		`{"type":"assistant.delta","session_id":"session-1","delta":"hello"}`,
		`{"type":"turn.completed","session_id":"session-1","result":{"session_id":"session-1","assistant_message":{"role":"assistant","content":"ok"}}}`,
		`{"type":"assistant.delta"`,
	}, "\n")
	api := newStreamTestAPI(t, streamBody)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := api.RunSessionStream(ctx, "session-1", "test prompt", "", "", nil)
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	if got := strings.TrimSpace(result.SessionID); got != "session-1" {
		t.Fatalf("unexpected session id: %q", got)
	}
}

func TestRunSessionStreamFailsIfUnexpectedEOFBeforeCompletion(t *testing.T) {
	streamBody := strings.Join([]string{
		`{"type":"assistant.delta","session_id":"session-1","delta":"hello"}`,
		`{"type":"assistant.delta"`,
	}, "\n")
	api := newStreamTestAPI(t, streamBody)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := api.RunSessionStream(ctx, "session-1", "test prompt", "", "", nil)
	if err == nil {
		t.Fatalf("expected decode error, got nil")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "decode stream event") {
		t.Fatalf("expected decode stream event error, got: %v", err)
	}
}

func TestRunSessionStreamPersistsClientDecodeErrorMessage(t *testing.T) {
	streamBody := strings.Join([]string{
		`{"type":"assistant.delta","session_id":"session-1","delta":"hello"}`,
		`{"type":"assistant.delta"`,
	}, "\n")

	var persistedCalls atomic.Int32
	var persistedBody string
	api := newStreamTestAPIWithRoundTripper(t, roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/v1/sessions/session-1/run/stream":
			headers := make(http.Header)
			headers.Set("Content-Type", "application/x-ndjson")
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     headers,
				Body:       io.NopCloser(strings.NewReader(streamBody)),
				Request:    req,
			}, nil
		case "/v1/sessions/session-1/messages":
			if req.Method != http.MethodPost {
				return &http.Response{
					StatusCode: http.StatusMethodNotAllowed,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(`{"ok":false,"error":"method not allowed"}`)),
					Request:    req,
				}, nil
			}
			raw, _ := io.ReadAll(req.Body)
			persistedBody = string(raw)
			persistedCalls.Add(1)
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
				Request:    req,
			}, nil
		default:
			return &http.Response{
				StatusCode: http.StatusNotFound,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"ok":false,"error":"not found"}`)),
				Request:    req,
			}, nil
		}
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := api.RunSessionStream(ctx, "session-1", "test prompt", "", "", nil)
	if err == nil {
		t.Fatalf("expected decode error, got nil")
	}
	if got := persistedCalls.Load(); got != 1 {
		t.Fatalf("persistedCalls = %d, want 1", got)
	}
	if !strings.Contains(persistedBody, streamClientErrorPathID+"/decode") {
		t.Fatalf("expected persisted decode path id in body, got %q", persistedBody)
	}
}

func TestRunSessionStreamRecoversFromUnexpectedEOFUsingStoredMessages(t *testing.T) {
	streamBody := strings.Join([]string{
		`{"type":"message.stored","session_id":"session-1","message":{"id":"msg_101","session_id":"session-1","global_seq":101,"role":"user","content":"test prompt","created_at":1000}}`,
		`{"type":"assistant.delta","session_id":"session-1","delta":"hello"}`,
		`{"type":"assistant.delta"`,
	}, "\n")

	var messagesCalled atomic.Int32
	api := newStreamTestAPIWithRoundTripper(t, roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/v1/sessions/session-1/run/stream":
			headers := make(http.Header)
			headers.Set("Content-Type", "application/x-ndjson")
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     headers,
				Body:       io.NopCloser(strings.NewReader(streamBody)),
				Request:    req,
			}, nil
		case "/v1/sessions/session-1/messages":
			messagesCalled.Add(1)
			query := req.URL.Query()
			if got := query.Get("after_seq"); got != "101" {
				return &http.Response{
					StatusCode: http.StatusBadRequest,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(fmt.Sprintf(`{"ok":false,"error":"unexpected after_seq %q"}`, got))),
					Request:    req,
				}, nil
			}
			if got := query.Get("limit"); got == "" {
				return &http.Response{
					StatusCode: http.StatusBadRequest,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(`{"ok":false,"error":"missing limit query"}`)),
					Request:    req,
				}, nil
			}
			body := `{"ok":true,"session_id":"session-1","messages":[{"id":"msg_102","session_id":"session-1","global_seq":102,"role":"assistant","content":"recovered reply","created_at":1002}]}`
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(body)),
				Request:    req,
			}, nil
		default:
			return &http.Response{
				StatusCode: http.StatusNotFound,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"ok":false,"error":"not found"}`)),
				Request:    req,
			}, nil
		}
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := api.RunSessionStream(ctx, "session-1", "test prompt", "", "", nil)
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	if got := strings.TrimSpace(result.AssistantMessage.Content); got != "recovered reply" {
		t.Fatalf("unexpected recovered assistant content: %q", got)
	}
	if got := messagesCalled.Load(); got == 0 {
		t.Fatalf("expected recovery to query session messages")
	}
}

func TestRunSessionStreamRecoversFromEOFAfterAssistantMessageStored(t *testing.T) {
	streamBody := strings.Join([]string{
		`{"type":"message.stored","session_id":"session-1","message":{"id":"msg_101","session_id":"session-1","global_seq":101,"role":"user","content":"test prompt","created_at":1000}}`,
		`{"type":"message.stored","session_id":"session-1","message":{"id":"msg_102","session_id":"session-1","global_seq":102,"role":"assistant","content":"streamed reply","created_at":1002}}`,
	}, "\n")

	var messagesCalled atomic.Int32
	api := newStreamTestAPIWithRoundTripper(t, roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/v1/sessions/session-1/run/stream":
			headers := make(http.Header)
			headers.Set("Content-Type", "application/x-ndjson")
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     headers,
				Body:       io.NopCloser(strings.NewReader(streamBody)),
				Request:    req,
			}, nil
		case "/v1/sessions/session-1/messages":
			messagesCalled.Add(1)
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"ok":true,"session_id":"session-1","messages":[]}`)),
				Request:    req,
			}, nil
		default:
			return &http.Response{
				StatusCode: http.StatusNotFound,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"ok":false,"error":"not found"}`)),
				Request:    req,
			}, nil
		}
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := api.RunSessionStream(ctx, "session-1", "test prompt", "", "", nil)
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	if got := strings.TrimSpace(result.AssistantMessage.Content); got != "streamed reply" {
		t.Fatalf("unexpected recovered assistant content: %q", got)
	}
	if got := messagesCalled.Load(); got != 0 {
		t.Fatalf("expected no recovery message query when assistant message was streamed, got %d", got)
	}
}

func newStreamTestAPI(t *testing.T, streamBody string) *API {
	t.Helper()
	return newStreamTestAPIWithRoundTripper(t, roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/v1/sessions/session-1/run/stream" {
			return &http.Response{
				StatusCode: http.StatusNotFound,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"ok":false,"error":"not found"}`)),
				Request:    req,
			}, nil
		}
		headers := make(http.Header)
		headers.Set("Content-Type", "application/x-ndjson")
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     headers,
			Body:       io.NopCloser(strings.NewReader(streamBody)),
			Request:    req,
		}, nil
	}))
}

func newStreamTestAPIWithRoundTripper(t *testing.T, rt roundTripFunc) *API {
	t.Helper()
	api := New("http://swarm.test")
	api.http = &http.Client{
		Transport: rt,
	}
	return api
}

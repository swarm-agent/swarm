package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestRuntimeSessionsV2RoutesRegisteredFailClosed(t *testing.T) {
	server := &Server{}
	mux := server.apiMux()
	tests := []struct {
		name   string
		method string
		path   string
	}{
		{name: "open", method: http.MethodPost, path: "/v2/internal/runtime-sessions/open"},
		{name: "sync state", method: http.MethodPost, path: "/v2/internal/runtime-sessions/session-123/sync/state"},
		{name: "run", method: http.MethodPost, path: "/v2/internal/runtime-sessions/session-123/run"},
		{name: "stream get", method: http.MethodGet, path: "/v2/internal/runtime-sessions/session-123/run/stream"},
		{name: "stream post", method: http.MethodPost, path: "/v2/internal/runtime-sessions/session-123/run/stream"},
		{name: "mirror batch", method: http.MethodPost, path: "/v2/internal/runtime-sessions/session-123/mirror/batch"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, nil)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusNotImplemented {
				t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusNotImplemented, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "runtime_session_not_implemented") {
				t.Fatalf("body = %s, want not implemented code", rec.Body.String())
			}
		})
	}
}

func TestRuntimeSessionsV2RoutesRejectUnknownPathAndMethod(t *testing.T) {
	server := &Server{}
	mux := server.apiMux()

	tests := []struct {
		name   string
		method string
		path   string
		want   int
	}{
		{name: "unknown action", method: http.MethodPost, path: "/v2/internal/runtime-sessions/session-123/unknown", want: http.StatusNotFound},
		{name: "missing id", method: http.MethodPost, path: "/v2/internal/runtime-sessions/%20/run", want: http.StatusBadRequest},
		{name: "trailing slash not canonicalized", method: http.MethodPost, path: "/v2/internal/runtime-sessions/session-123/run/", want: http.StatusNotFound},
		{name: "wrong method", method: http.MethodGet, path: "/v2/internal/runtime-sessions/open", want: http.StatusMethodNotAllowed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, nil)
			if tt.name == "missing id" {
				req.URL.Path = "/v2/internal/runtime-sessions/ /run"
			}
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d, body=%s", rec.Code, tt.want, rec.Body.String())
			}
		})
	}
}

func TestRuntimeSessionsV2HandlersDoNotCallLegacyRoutedHandlers(t *testing.T) {
	body, err := os.ReadFile("runtime_sessions_v2.go")
	if err != nil {
		t.Fatalf("read runtime_sessions_v2.go: %v", err)
	}
	for _, forbidden := range []string{
		"handlePeerSessionOpen",
		"createSessionFromRequestWithSessionID",
		"proxyRoutedSessionRequest",
		"routedSessionTarget",
	} {
		if strings.Contains(string(body), forbidden) {
			t.Fatalf("runtime_sessions_v2.go contains forbidden legacy symbol %q", forbidden)
		}
	}
}

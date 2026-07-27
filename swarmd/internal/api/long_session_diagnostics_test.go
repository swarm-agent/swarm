package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/longsessiondiag"
	"swarm/packages/swarmd/internal/security"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestLongSessionDiagnosticsEndpointsGateAndValidateSamples(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "diagnostics.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatal(err)
	}
	securitySvc := security.NewService(pebblestore.NewClientAuthStore(store), events)
	if _, err := securitySvc.EnsureAttachAuth(); err != nil {
		t.Fatal(err)
	}
	token, err := securitySvc.RevealAttachToken()
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{security: securitySvc}
	unauthenticated := httptest.NewRecorder()
	server.Handler().ServeHTTP(unauthenticated, httptest.NewRequest(http.MethodGet, LongSessionDiagnosticsConfigPath, nil))
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d body=%s", unauthenticated.Code, unauthenticated.Body.String())
	}
	serve := func(response *httptest.ResponseRecorder, request *http.Request) {
		request.Header.Set("X-Swarm-Token", token)
		server.Handler().ServeHTTP(response, request)
	}
	disabled := httptest.NewRecorder()
	serve(disabled, httptest.NewRequest(http.MethodGet, LongSessionDiagnosticsConfigPath, nil))
	if disabled.Code != http.StatusNotFound || !strings.Contains(disabled.Body.String(), `"enabled":false`) {
		t.Fatalf("disabled config status=%d body=%s", disabled.Code, disabled.Body.String())
	}

	recorder, err := longsessiondiag.Start(longsessiondiag.Options{
		Enabled: true, LogsRoot: t.TempDir(), SampleInterval: time.Hour,
		ProfileInterval: time.Hour, CPUProfileInterval: time.Hour, DiskBudgetBytes: 64 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = recorder.Close() }()
	server.SetLongSessionDiagnostics(recorder)

	config := httptest.NewRecorder()
	serve(config, httptest.NewRequest(http.MethodGet, LongSessionDiagnosticsConfigPath, nil))
	if config.Code != http.StatusOK || !strings.Contains(config.Body.String(), `"enabled":true`) || !strings.Contains(config.Body.String(), `"artifact_location":`) {
		t.Fatalf("enabled config status=%d body=%s", config.Code, config.Body.String())
	}

	malformed := httptest.NewRecorder()
	serve(malformed, httptest.NewRequest(http.MethodPost, LongSessionDiagnosticsSamplePath, strings.NewReader(`{"conversation":"secret"}`)))
	if malformed.Code != http.StatusBadRequest {
		t.Fatalf("unknown-field sample status=%d body=%s", malformed.Code, malformed.Body.String())
	}

	oversized := httptest.NewRecorder()
	serve(oversized, httptest.NewRequest(http.MethodPost, LongSessionDiagnosticsSamplePath, bytes.NewReader(make([]byte, longSessionDiagnosticsMaxBody+1))))
	if oversized.Code != http.StatusBadRequest {
		t.Fatalf("oversized sample status=%d body=%s", oversized.Code, oversized.Body.String())
	}

	capture := httptest.NewRecorder()
	body := `{"timestamp_ms":1,"browser_memory_available":true,"browser_memory_bytes":1024,"v3_sections":{"messages":2},"largest_sessions":[{"session_hash":"0123456789abcdef","estimated_bytes":256,"messages":2}]}`
	serve(capture, httptest.NewRequest(http.MethodPost, LongSessionDiagnosticsCapturePath, strings.NewReader(body)))
	if capture.Code != http.StatusAccepted || !strings.Contains(capture.Body.String(), `"artifacts":[`) || !strings.Contains(capture.Body.String(), `"desktop-samples.jsonl"`) {
		t.Fatalf("manual capture status=%d body=%s", capture.Code, capture.Body.String())
	}

}

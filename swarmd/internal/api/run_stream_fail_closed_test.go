package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestV3PrimaryRunStreamUsesPrimaryReadiness(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSession(t, server, "v3-run-primary-readiness-create", "v3 run primary readiness")
	runner := &primaryV2RunRequestRecordingRunner{emitLifecycle: true}
	server.runner = runner

	savedRunStreams := server.runStreams
	server.runStreams = nil
	req := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+created.ID+"/run/stream", bytes.NewBufferString(`{"type":"run.start","prompt":"hello v3","background":true}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("missing run stream manager status = %d, want %d, body=%s", rec.Code, http.StatusInternalServerError, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "run stream manager not configured") {
		t.Fatalf("body = %s, want primary run stream readiness error", rec.Body.String())
	}
	if calls, _, _, _ := runner.snapshot(); calls != 0 {
		t.Fatalf("runner calls after failed primary readiness = %d, want 0", calls)
	}

	server.runStreams = savedRunStreams
	if server.runStreams == nil {
		server.runStreams = newRunStreamManager()
	}
	req = httptest.NewRequest(http.MethodPost, "/v3/sessions/"+created.ID+"/run/stream", bytes.NewBufferString(`{"type":"run.start","prompt":"hello v3","background":true}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("primary run start status = %d, want %d, body=%s", rec.Code, http.StatusAccepted, rec.Body.String())
	}
	calls, gotSessionID, gotRequest, gotMeta := runner.snapshot()
	if calls != 1 || gotSessionID != created.ID || !gotRequest.Background || gotMeta.RunID == "" {
		t.Fatalf("runner snapshot calls=%d session=%q request=%+v meta=%+v", calls, gotSessionID, gotRequest, gotMeta)
	}
	if gotMeta.OwnerTransport != "background_api" || gotMeta.Principal.AccountScopeID != testPrincipal().AccountScopeID {
		t.Fatalf("run start meta = %+v", gotMeta)
	}
}

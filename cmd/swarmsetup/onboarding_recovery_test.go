package main

import (
	"errors"
	"net/http/httptest"
	"strings"
	"swarm-refactor/swarmtui/internal/ui"
	"testing"
	"time"
)

// Requirement: setup stays available through delayed readiness/failure, GET
// refresh exposes bounded progress without replay, and only explicit retry may
// finish handoff. Handler-level fake actions avoid real accounts/services.
func TestDesktopPrerequisiteReadinessRetry(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	completed := make(chan struct{}, 1)
	passwordDone := false
	calls, handoffs := 0, 0
	actions := ui.PrerequisiteActions{InitialResume: true, InitialCreated: true, PasswordDone: func() bool { return passwordDone }, Status: func() string { return "Waiting for authenticated readiness" }, Destination: func() string { return "http://127.0.0.1:5555" }, Complete: func(p []byte) error {
		calls++
		if calls == 1 {
			passwordDone = true
			close(entered)
			<-release
			return errors.New("readiness deadline reached")
		}
		if len(p) != 0 {
			t.Error("password replayed")
		}
		return nil
	}, Handoff: func() error { handoffs++; return nil }}
	h := prerequisiteHandler("http://127.0.0.1:1234", "cap", actions, completed)
	send := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://127.0.0.1:1234/cap"+path, strings.NewReader(body))
		r.Header.Set("Origin", "http://127.0.0.1:1234")
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	result := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		result <- send("POST", "", `{"action":"password","confirm":true,"password":"test-only","password_confirm":"test-only"}`)
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("no progress")
	}
	status := send("GET", "?status", "")
	if status.Code != 200 || !strings.Contains(status.Body.String(), `"busy":true`) {
		t.Fatal(status.Body.String())
	}
	if w := send("POST", "", `{"action":"retry","confirm":true}`); w.Code != 409 {
		t.Fatal("parallel mutation admitted")
	}
	select {
	case <-completed:
		t.Fatal("closed before readiness")
	default:
	}
	close(release)
	var failed *httptest.ResponseRecorder
	select {
	case failed = <-result:
	case <-time.After(time.Second):
		t.Fatal("request stuck")
	}
	if failed.Code != 422 || !strings.Contains(failed.Body.String(), `"retry":true`) || handoffs != 0 {
		t.Fatal("failure lost")
	}
	state := send("GET", "?status", "")
	if !strings.Contains(state.Body.String(), `"retry":true`) {
		t.Fatal("refresh lost retry")
	}
	// A stale password form must not re-enter Complete after password success.
	if w := send("POST", "", `{"action":"password","confirm":true,"password":"test-only","password_confirm":"test-only"}`); w.Code != 409 || calls != 1 || handoffs != 0 {
		t.Fatal("stale password replay reached the action boundary")
	}
	if w := send("POST", "", `{"action":"retry","confirm":true}`); w.Code != 200 || !strings.Contains(w.Body.String(), `"destination"`) {
		t.Fatal(w.Body.String())
	}
	if calls != 2 || handoffs != 1 {
		t.Fatal("duplicate handoff")
	}
}

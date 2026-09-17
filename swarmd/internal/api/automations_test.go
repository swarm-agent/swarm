package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Purpose: V1 automation HTTP endpoints are disabled for launch and return 404.
func TestAutomationHTTPDisabled(t *testing.T) {
	s := &Server{}
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete} {
		r := httptest.NewRequest(method, AutomationsPath, strings.NewReader(`{}`))
		w := httptest.NewRecorder()
		s.handleAutomations(w, r)
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s expected 404, got %d: %s", method, w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), "automations disabled") {
			t.Fatalf("%s expected 'automations disabled', got %s", method, w.Body.String())
		}
	}
}

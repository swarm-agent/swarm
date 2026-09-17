package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Purpose: automation routes are disabled for launch.
// All /v3/automations and /v3/automations/v2 routes must return 404 disabled.
func TestAutomationV2RoutesDisabled(t *testing.T) {
	s := &Server{}
	h := s.apiMux()
	routes := []string{
		AutomationsV2Path,
		AutomationsV2Path + "/proposal",
		AutomationsV2Path + "/review",
		AutomationsV2Path + "/accept",
		AutomationsV2Path + "/decline",
		AutomationsV2Path + "/control",
		AutomationsV2Path + "/progress",
		AutomationsPath,
		AutomationsPath + "/approve",
		AutomationsPath + "/revoke",
	}
	for _, path := range routes {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			r := httptest.NewRequest(method, path, strings.NewReader(`{}`))
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != http.StatusNotFound {
				t.Fatalf("%s %s expected 404, got %d: %s", method, path, w.Code, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), "automations disabled") {
				t.Fatalf("%s %s expected 'automations disabled', got %s", method, path, w.Body.String())
			}
		}
	}
}

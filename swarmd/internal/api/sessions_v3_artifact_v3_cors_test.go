package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Requirement: opaque native module requests need CORS, but only after exact
// preview capability authentication. The HTTP boundary is the narrowest layer
// proving rejection yields neither module bytes nor permissive CORS headers.
func TestArtifactV3ModuleCORSCapability(t *testing.T) {
	server, service := newArtifactV3APITestServer(t)
	service.artifact = artifactV3APITestArtifact()
	access := httptest.NewRecorder()
	server.Handler().ServeHTTP(access, withTestPrincipal(httptest.NewRequest(http.MethodPost, "/v3/sessions/artifact-v3-api/artifacts-v3/artifact-1/preview/access", strings.NewReader(`{"revision_ref":"rev-root"}`))))
	var result struct {
		URL string `json:"preview_url"`
	}
	if access.Code != http.StatusOK || json.Unmarshal(access.Body.Bytes(), &result) != nil || result.URL == "" {
		t.Fatal("access issuance failed")
	}
	moduleURL := strings.Replace(result.URL, "?revision=", "/files/runtime.js?revision=", 1)
	for _, tc := range []struct {
		name, target, origin string
		allowed              bool
	}{
		{"opaque", moduleURL, "null", true},
		{"foreign-origin", moduleURL, "https://foreign.invalid", false},
		{"wrong-revision", strings.Replace(moduleURL, "rev-root", "rev-other", 1), "null", false},
		{"missing-token", "/v3/sessions/artifact-v3-api/artifacts-v3/artifact-1/preview/files/runtime.js?revision=rev-root", "null", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.target, nil)
			req.Header.Set("Origin", tc.origin)
			rec := httptest.NewRecorder()
			before := service.calls
			// The fixture has no security service; apply the same capability
			// resolver used by authMiddleware before invoking the handler.
			if principal, ok := server.validateSessionV3ArtifactPreviewRequest(req); ok {
				req = requestWithPrincipalContext(req, principal)
			}
			server.Handler().ServeHTTP(rec, req)
			if tc.allowed {
				if rec.Code != http.StatusOK || rec.Header().Get("Access-Control-Allow-Origin") != "null" {
					t.Fatalf("module rejected: %d %s", rec.Code, rec.Body.String())
				}
			} else if rec.Header().Get("Access-Control-Allow-Origin") != "" {
				t.Fatal("unexpected CORS grant")
			}
			if tc.name == "wrong-revision" || tc.name == "missing-token" {
				if rec.Code == http.StatusOK || service.calls != before {
					t.Fatal("unauthorized request reached service")
				}
			}
			if rec.Header().Get("Access-Control-Allow-Credentials") != "" {
				t.Fatal("credentialed CORS enabled")
			}
		})
	}
}

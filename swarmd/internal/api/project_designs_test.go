package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: the registered project route must enforce authenticated ownership,
// reject invalid pagination and omit briefs while including unmounted session
// output. Handler + real store is the narrowest layer proving route wiring.
func TestProjectDesignHTTPDiscoveryIsolation(t *testing.T) {
	s, _ := newArtifactV3APITestServer(t)
	if err := s.sessions.Store().PutProject("account-1", &pebblestore.ProjectRecord{ID: "project", Name: "Project", PrimarySessionID: "artifact-v3-api"}); err != nil {
		t.Fatal(err)
	}
	_, err := s.sessions.DesignStore().SubmitDesignRequest(pebblestore.DesignPrincipal{AccountID: "account-1", PrincipalID: "user-1"}, pebblestore.DesignSubmit{RequestID: "request", IdempotencyKey: "request", ParentSessionID: "artifact-v3-api", ParentRunID: "run", Candidates: []pebblestore.DesignCandidateSpec{{ArtifactID: "design", Kind: "html", Operation: "generate", Brief: strings.Repeat("bounded title ", 20) + "PRIVATE_TAIL"}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		account, user, query string
		status               int
		visible              bool
	}{
		{"account-1", "user-1", "", 200, true},
		{"account-1", "other", "", 200, false},
		{"other", "user-1", "", 404, false},
		{"account-1", "user-1", "?limit=51", 400, false},
		{"account-1", "user-1", "?after=invalid", 400, false},
	} {
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, withAccountPrincipal(httptest.NewRequest(http.MethodGet, "/v3/projects/project/designs"+tc.query, nil), tc.account, tc.user))
		if w.Code != tc.status || strings.Contains(w.Body.String(), `"id":"request"`) != tc.visible || strings.Contains(w.Body.String(), "PRIVATE_TAIL") {
			t.Fatal(tc, w.Code, w.Body.String())
		}
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal(w.Header())
		}
	}
}

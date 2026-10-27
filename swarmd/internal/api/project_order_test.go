package api

import (
	"net/http/httptest"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: handleProjects is the HTTP adapter for account order. Exercise payload
// rejection and the resulting stored order, not merely response status, using the
// narrow handler fixture without providers or a live daemon.
func TestProjectOrderRoute(t *testing.T) {
	s, sessions, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	p := testPrincipal()
	db := sessions.Store()
	for _, id := range []string{"a", "b"} {
		if err := db.PutProject(p.AccountScopeID, &pebblestore.ProjectRecord{ID: id, Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		body   string
		status int
	}{
		{`{"project_ids":["b","a"],"client_request_id":"order-one"}`, 200},
		{`{"project_ids":["a","missing"],"client_request_id":"order-two"}`, 400},
		{`{"project_ids":["a","b"]}`, 400},
	} {
		req := httptest.NewRequest("PUT", ProjectsPath+":order", strings.NewReader(tc.body))
		req = req.WithContext(identity.ContextWithPrincipal(req.Context(), p))
		response := httptest.NewRecorder()
		s.handleProjects(response, req)
		if response.Code != tc.status {
			t.Fatalf("HTTP %d: %s", response.Code, response.Body.String())
		}
		got, err := db.ListProjects(p.AccountScopeID, 100)
		if err != nil || len(got) != 2 || got[0].ID != "b" || got[1].ID != "a" {
			t.Fatalf("order %+v: %v", got, err)
		}
	}
}

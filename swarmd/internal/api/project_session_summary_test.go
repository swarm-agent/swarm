package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: project summary HTTP reads must consume compact scoped rows without
// full sessions or projection hydration, and cannot disclose another user's
// conversation. Deliberately unreadable non-required bodies prove the read path.
func TestProjectSessionSummaryHTTP(t *testing.T) {
	server, store, p := setupDirectMediaTestServer(t)
	project := &pebblestore.ProjectRecord{ID: "summary-project", Name: "Summary"}
	if err := store.PutProject(p.AccountScopeID, project); err != nil {
		t.Fatal(err)
	}
	for _, user := range []string{p.UserID, "foreign-user"} {
		session := pebblestore.SessionSnapshot{ID: user + "-chat", AccountScopeID: p.AccountScopeID, UserID: user, Title: "Conversation", CreatedAt: 1, Metadata: map[string]any{"project_id": project.ID, "agent_name": "system-orchestrator", "private_body": strings.Repeat("secret-context", 100)}}
		if err := store.CreateSession(session); err != nil {
			t.Fatal(err)
		}
		if err := store.Underlying().PutBytes(pebblestore.KeyV3SessionProjection(session.ID), []byte("not-json")); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/v3/sessions?project_id="+project.ID+"&view=summary", nil)
	response := httptest.NewRecorder()
	server.handleSessionsV3PrimaryList(response, req, p)
	if response.Code != 200 {
		t.Fatalf("summary HTTP %d: %s", response.Code, response.Body.String())
	}
	var body struct {
		Sessions []struct {
			Session   pebblestore.SessionSnapshot `json:"session"`
			Attention sessionsV3SessionView       `json:"attention"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Sessions) != 1 || body.Sessions[0].Session.UserID != p.UserID || strings.Contains(response.Body.String(), "private_body") || strings.Contains(response.Body.String(), "foreign-user") {
		t.Fatal("summary ownership/shape violation")
	}
	if body.Sessions[0].Attention.PendingPermissions == nil {
		t.Fatal("missing authoritative empty attention")
	}
}

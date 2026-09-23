package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Requirement: the TUI obtains and decides an exact worker review via the
// authenticated user endpoints, never the session-plan permission resolver.
// Threat: losing scope/revision in transit or swallowing rejection activates
// a foreign or stale proposal. The HTTP boundary is the narrowest proof.
func TestWorkerReviewClientExactScopeAndRejection(t *testing.T) {
	t.Setenv("SWARMD_LOCAL_TRANSPORT_SOCKET", "")
	t.Setenv("DATA_DIR", "")
	review := WorkerReview{ProposalID: "p", Revision: 2, Digest: strings.Repeat("a", 64)}
	decisions := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Swarm-Token") != "fixture" {
			t.Error("missing authentication")
		}
		switch r.URL.Path {
		case "/v3/automations/v2/review":
			if r.Method != "GET" || r.URL.Query().Get("workspace_id") != "workspace" || r.URL.Query().Get("session_id") != "chat" {
				t.Errorf("review request %s %s", r.Method, r.URL.String())
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"proposal": WorkerProposal{WorkerReview: review, WorkspaceID: "workspace", SessionID: "chat"}})
		case "/v3/automations/v2/accept", "/v3/automations/v2/decline":
			decisions++
			var body struct {
				Action      string       `json:"action"`
				WorkspaceID string       `json:"workspace_id"`
				SessionID   string       `json:"session_id"`
				Review      WorkerReview `json:"review"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body.WorkspaceID != "workspace" || body.SessionID != "chat" || body.Review != review || r.Method != "POST" {
				t.Errorf("decision changed: %+v", body)
			}
			if r.URL.Path == "/v3/automations/v2/accept" {
				if body.Action != "accept_automation" {
					t.Error("wrong accept action")
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"record": WorkerProposal{WorkerReview: review, WorkspaceID: "workspace", SessionID: "chat"}})
			} else {
				if body.Action != "decline_automation" {
					t.Error("wrong decline action")
				}
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte(`{"error":"review changed"}`))
			}
		default:
			t.Errorf("unexpected endpoint %s", r.URL.Path)
		}
	}))
	defer server.Close()
	api := New(server.URL)
	api.SetToken("fixture")
	got, err := api.GetWorkerReview(context.Background(), "workspace", "chat")
	if err != nil || got.WorkerReview != review {
		t.Fatalf("review=%+v error=%v", got, err)
	}
	if err = api.DecideWorkerReview(context.Background(), "workspace", "chat", review, true); err != nil {
		t.Fatal(err)
	}
	if err = api.DecideWorkerReview(context.Background(), "workspace", "chat", review, false); err == nil {
		t.Fatal("decline conflict swallowed")
	}
	if err = api.DecideWorkerReview(context.Background(), "workspace", "chat", WorkerReview{}, true); err == nil {
		t.Fatal("empty review accepted")
	}
	if decisions != 2 {
		t.Fatalf("decision requests=%d, want 2", decisions)
	}
}

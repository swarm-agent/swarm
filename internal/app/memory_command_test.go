package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/ui"
)

// Purpose: exercise executeCommand -> authenticated client -> memory HTTP contract
// at the narrow TUI boundary. Prevent unconfirmed deletes, stale overwrite, draft
// loss and metadata clearing; a fake server proves exact requests and refreshes.
func TestMemoryCommandObjects(t *testing.T) {
	doc := client.MemoryDocument{Revision: 4, Entries: []client.MemoryEntry{{"id": "rule", "kind": "rule", "content": "old", "pinned": true, "workspace_id": "workspace"}}}
	posts := 0
	reject := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/memory" {
			t.Errorf("wrong path %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("X-Swarm-Token") == "" {
			t.Error("missing authentication")
		}
		if r.Method == "POST" {
			posts++
			var req struct {
				Action   string             `json:"action"`
				Revision int64              `json:"expected_revision"`
				Entry    client.MemoryEntry `json:"entry"`
				ID       string             `json:"entry_id"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Error(err)
			}
			if req.Revision != doc.Revision {
				t.Errorf("wrong CAS %d", req.Revision)
			}
			if reject {
				http.Error(w, "memory revision conflict", 409)
				return
			}
			if req.Action == "remember" {
				if req.Entry["pinned"] != true || req.Entry["workspace_id"] != "workspace" || req.Entry["content"] != "new  content" {
					t.Errorf("metadata/content lost: %#v", req.Entry)
				}
				doc.Entries[0] = req.Entry
			} else if req.Action == "forget" && req.ID == "rule" {
				doc.Entries = nil
			} else {
				t.Errorf("unexpected mutation %#v", req)
			}
			doc.Revision++
		}
		_ = json.NewEncoder(w).Encode(doc)
	}))
	defer server.Close()
	home := ui.NewHomePage(routedPrimerHomeModel())
	a := &App{api: testAPIWithToken(server.URL), home: home, route: "home"}
	a.executeCommand("/memory")
	if a.memoryDocument == nil || !strings.Contains(strings.Join(home.CommandOverlayLines(), "\n"), "old") {
		t.Fatal("list missing")
	}
	a.executeCommand("/memory forget rule")
	if posts != 0 || !strings.Contains(strings.Join(home.CommandOverlayLines(), "\n"), "cannot be restored") {
		t.Fatal("delete lacked confirmation")
	}
	a.executeCommand("/memory edit rule new  content")
	if a.memoryDraft != "/memory edit rule new  content" || doc.Entries[0]["content"] != "old" {
		t.Fatal("failed mutation changed state or lost draft")
	}
	if !strings.Contains(strings.Join(home.CommandOverlayLines(), "\n"), "revision conflict") {
		t.Fatal("missing actionable error")
	}
	reject = false
	a.executeCommand("/memory refresh")
	a.executeCommand(a.memoryDraft)
	if a.memoryDraft != "" || a.memoryDocument.Revision != 5 || a.memoryDocument.Entries[0]["content"] != "new  content" {
		t.Fatal("successful edit did not refresh")
	}
	a.executeCommand("/memory forget rule confirm")
	if len(a.memoryDocument.Entries) != 0 || a.memoryDocument.Revision != 6 {
		t.Fatal("forget did not refresh")
	}
}

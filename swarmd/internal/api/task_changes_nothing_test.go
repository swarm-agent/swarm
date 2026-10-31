package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: get_task_changes for work with no commits says so in a sentence
// (note: "no commits since base <sha>: nothing to show") and names where the
// task's report is read (next_action: read_task_session / get_task), instead
// of answering commits [], files [] and diff "" with base == head for the
// caller to interpret. Work that did commit carries neither.
// Threat (GPUI-vs-MCP parity rerun): an Orchestrator or MCP caller reading a
// read-only task's changes gets empty lists and no next step, and either
// reports "the task did nothing" or keeps calling for a diff that does not
// exist.
// Boundary: catalogTaskChanges (the get_task_changes catalog handler) over
// controlMCPGetChanges, with the session read from the real store and real
// Git in a job clone of a real folder. No model provider is involved.
// Narrowest layer: the handler is where the changes answer is built for both
// surfaces (the Orchestrator's catalog call and Swarm Control's).
func TestTaskChangesWithNoCommitsSaysNothingToShow(t *testing.T) {
	r := newRebaseFixture(t)

	// A read-only job: cloned and recorded like allocation does, no commit.
	dir := filepath.Join(t.TempDir(), "readonly")
	outboxPinGit(t, filepath.Dir(dir), "clone", "-q", r.folder, dir)
	outboxPinGit(t, dir, "checkout", "-q", "-b", "agent/readonly")
	base := outboxPinGit(t, dir, "rev-parse", "HEAD")
	p := testPrincipal()
	if err := r.f.server.sessions.Store().CreateSession(pebblestore.SessionSnapshot{ID: "sess-readonly", UserID: p.UserID, AccountScopeID: p.AccountScopeID,
		WorkspacePath: r.folder, Title: "Finder job", WorktreeEnabled: true, WorktreeRootPath: dir, WorktreeBranch: "agent/readonly", WorktreeBaseBranch: "dev",
		Metadata: map[string]any{"base_commit": base, "swarm_v3_source_workspace_path": r.folder}, CreatedAt: 1, UpdatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	// A job that committed, for the negative case.
	r.job("worked")

	changes := func(session string) map[string]any {
		t.Helper()
		req := requestWithScopedToken(withTestPrincipal(httptest.NewRequest(http.MethodPost, "/mcp", nil)), &pebblestore.ScopedTokenRecord{Scopes: []string{"swarm:read", "sessions:read"}})
		sessionRoute := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			session, found, err := r.f.server.sessions.Store().GetSession(strings.TrimPrefix(req.URL.Path, "/v3/sessions/"))
			if err != nil || !found || req.Method != http.MethodGet {
				http.NotFound(w, req)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"session": session})
		})
		out, err := catalogTaskChanges(&controlMCPCall{request: req, next: sessionRoute, server: r.f.server}, map[string]any{"session_id": session})
		if err != nil {
			t.Fatalf("get_task_changes %s: %v", session, err)
		}
		return controlMCPMap(out)
	}

	empty := changes("sess-readonly")
	if commits, _ := empty["commits"].([]string); len(commits) != 0 || empty["base"] != base || empty["head"] != base {
		t.Fatalf("fixture: want no commits with base == head %s: %v", base, empty)
	}
	if note, _ := empty["note"].(string); note != "no commits since base "+base+": nothing to show" {
		t.Fatalf("no-commit answer has no sentence: note=%q", empty["note"])
	}
	if next, _ := empty["next_action"].(string); !strings.Contains(next, "read_task_session") || !strings.Contains(next, "report") {
		t.Fatalf("no-commit answer does not name read_task_session for the report: next_action=%q", empty["next_action"])
	}

	worked := changes("sess-worked")
	if commits, _ := worked["commits"].([]string); len(commits) != 1 {
		t.Fatalf("fixture: want the job's one commit: %v", worked)
	}
	if _, ok := worked["next_action"]; ok {
		t.Fatalf("work with commits was told there is nothing to show: %v", worked["next_action"])
	}
	if note, _ := worked["note"].(string); strings.Contains(note, "nothing to show") {
		t.Fatalf("work with commits was told there is nothing to show: %q", note)
	}
}

package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func companionTestCall(t *testing.T, server *Server, method, path string, body any) (int, map[string]any) {
	t.Helper()
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		raw, _ := json.Marshal(body)
		reader = bytes.NewReader(raw)
	}
	r := withTestPrincipal(httptest.NewRequest(method, path, reader))
	w := httptest.NewRecorder()
	switch {
	case path == CompanionPath:
		server.handleCompanion(w, r)
	case strings.HasPrefix(path, CompanionContextPath):
		server.handleCompanionContext(w, r)
	case path == "/v3/sessions:delete":
		server.handleSessionsV3Delete(w, r)
	case path == "/v3/sessions:archive":
		server.handleSessionsV3PrimaryArchiveBatch(w, r, testPrincipal())
	default:
		server.handleSessionV3PrimaryByID(w, r)
	}
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func companionTestView(t *testing.T, body map[string]any) CompanionView {
	t.Helper()
	raw, _ := json.Marshal(body["companion"])
	var view CompanionView
	if err := json.Unmarshal(raw, &view); err != nil {
		t.Fatalf("companion view: %v (%v)", err, body)
	}
	return view
}

// Purpose: the Companion is one persistent account/user Swarm session that is
// independent of any project. Requirement: POST /v3/companion is an idempotent
// get-or-create (also under concurrent callers and after a daemon restart over
// the same store), creates an ordinary Swarm-agent session (so it keeps the
// orchestrator's tools, permissions and account model) without a project, does
// not import or mark any existing chat, and hides itself from chat lists.
// Threat: duplicate Companions from racing clients or a restart, a Companion
// bound to the selected project, or old chats silently pulled in.
// Boundary: handleCompanion/handleCompanionEnsure over createSessionsV3Primary
// and SessionStore.EnsureCompanion. Narrowest layer: the HTTP handlers over a
// real Pebble store reopened at the same path.
func TestCompanionEnsureIsSingleProjectFreeAndSurvivesRestart(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "companion.pebble")
	workspace := filepath.Join(t.TempDir(), "scratch")
	var first CompanionView
	var chatID string
	t.Run("first daemon", func(t *testing.T) {
		server, sessions, _, _, _ := newRoutedSessionTestServerAt(t, dbPath, false)
		bindingID := seedSessionsV3PrimaryAuthority(t, server, workspace)
		// An ordinary chat that must stay a chat.
		raw, _ := json.Marshal(map[string]any{"client_request_id": "chat-1", "workspace_path": workspace, "workspace_binding_id": bindingID, "title": "A chat", "mode": "auto", "agent_name": agentruntime.SwarmAgentID})
		w := httptest.NewRecorder()
		server.handleSessionsV3PrimaryCreate(w, withTestPrincipal(httptest.NewRequest(http.MethodPost, "/v3/sessions", bytes.NewReader(raw))), testPrincipal())
		var created map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &created)
		chatID, _ = created["session_id"].(string)
		if w.Code != http.StatusOK || chatID == "" {
			t.Fatalf("chat create: %d %s", w.Code, w.Body.String())
		}

		if code, body := companionTestCall(t, server, http.MethodGet, CompanionPath, nil); code != http.StatusOK || body["companion"] != nil {
			t.Fatalf("get before create: %d %v", code, body)
		}
		const callers = 6
		views := make([]CompanionView, callers)
		createdCount := 0
		var mu sync.Mutex
		var wg sync.WaitGroup
		for i := 0; i < callers; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				code, body := companionTestCall(t, server, http.MethodPost, CompanionPath, map[string]any{"workspace_path": workspace})
				if code != http.StatusOK {
					t.Errorf("ensure %d: %d %v", i, code, body)
					return
				}
				mu.Lock()
				defer mu.Unlock()
				views[i] = companionTestView(t, body)
				if body["created"] == true {
					createdCount++
				}
			}(i)
		}
		wg.Wait()
		if t.Failed() {
			return
		}
		if createdCount != 1 {
			t.Fatalf("created %d Companions, want 1", createdCount)
		}
		first = views[0]
		for _, view := range views {
			if view.SessionID != first.SessionID {
				t.Fatalf("concurrent ensure diverged: %s vs %s", view.SessionID, first.SessionID)
			}
		}
		if first.State != companionStateReady || first.Generation != 1 || first.SessionID == chatID || first.Routes.Messages != "/v3/sessions/"+first.SessionID+"/messages" {
			t.Fatalf("view %+v", first)
		}
		session, ok, err := sessions.GetSession(first.SessionID)
		if err != nil || !ok {
			t.Fatalf("companion session: %v %v", ok, err)
		}
		// The Companion runs as the code-owned Orchestrator, with no project.
		if !sessionsV3IsCompanion(session) || session.Metadata["navigation_hidden"] != true || session.Metadata["agent_name"] != agentruntime.SwarmOrchestratorAgentID || session.Metadata["resolved_agent_name"] != agentruntime.SwarmOrchestratorAgentID || session.Metadata["project_id"] != nil || session.Metadata["swarm_v3_project_id"] != nil {
			t.Fatalf("companion metadata %v", session.Metadata)
		}
		if session.AccountScopeID != testPrincipal().AccountScopeID || session.UserID != testPrincipal().UserID {
			t.Fatalf("companion owner %s/%s", session.AccountScopeID, session.UserID)
		}
		chat, _, _ := sessions.GetSession(chatID)
		if sessionsV3IsCompanion(chat) || chat.Metadata["navigation_hidden"] == true {
			t.Fatalf("existing chat changed: %v", chat.Metadata)
		}
		// Another user of the same account has no Companion yet.
		other := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user-2", AccountScopeID: testPrincipal().AccountScopeID}
		w = httptest.NewRecorder()
		server.handleCompanionGet(w, other)
		if !strings.Contains(w.Body.String(), `"companion":null`) {
			t.Fatalf("other user sees a Companion: %s", w.Body.String())
		}
	})
	if t.Failed() {
		return
	}
	t.Run("after restart", func(t *testing.T) {
		server, _, _, _, _ := newRoutedSessionTestServerAt(t, dbPath, true)
		code, body := companionTestCall(t, server, http.MethodPost, CompanionPath, nil)
		view := companionTestView(t, body)
		if code != http.StatusOK || body["created"] != false || view.SessionID != first.SessionID || view.State != companionStateReady {
			t.Fatalf("ensure after restart: %d %v", code, body)
		}
	})
}

// Purpose: ordinary chat controls cannot close, archive, delete, end, switch
// or unmark the Companion, while the person can still delete it (and its
// history) deliberately. Requirement: archive, delete, batch archive, batch
// delete by id, end and agent switch on the Companion are refused with 409
// and change nothing; chat bulk delete never selects it, by id or by age; generic
// metadata cannot add or remove the Companion marker or unhide it; DELETE
// /v3/companion needs confirm=true and the current session_id, deletes through
// the canonical tombstone path and makes the next ensure start a new, empty
// Companion. Threat: losing the Companion to a stray chat action, a client
// minting a second "Companion" by metadata, or a stale retried delete wiping
// the replacement. Boundary: rejectCompanionLifecycleMutation at
// handleSessionV3PrimaryTombstone, handleSessionsV3PrimaryArchiveBatch,
// handleSessionV3End and handleSessionV3PrimaryAgent; the navigation_hidden
// filter of the visible-session list used by handleSessionsV3Delete;
// isProtectedSessionsV3MetadataKey/mergeSessionsV3MetadataUpdate;
// handleCompanionDelete. Narrowest layer: the HTTP handlers over a real store.
func TestCompanionRefusesChatLifecycleControlsButAllowsExplicitDelete(t *testing.T) {
	server, sessions, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	workspace := filepath.Join(t.TempDir(), "scratch")
	seedSessionsV3PrimaryAuthority(t, server, workspace)
	code, body := companionTestCall(t, server, http.MethodPost, CompanionPath, map[string]any{"workspace_path": workspace})
	if code != http.StatusOK {
		t.Fatalf("ensure: %d %v", code, body)
	}
	companion := companionTestView(t, body)
	id := companion.SessionID
	before, _, _ := sessions.GetSession(id)
	sessionPath := "/v3/sessions/" + id

	refusals := []struct {
		name, method, path string
		body               any
	}{
		{"archive", http.MethodPost, sessionPath + "/archive", nil},
		{"delete", http.MethodDelete, sessionPath, nil},
		{"end", http.MethodPost, sessionPath + "/end", map[string]any{"client_request_id": "end-1"}},
		{"agent switch", http.MethodPost, sessionPath + "/agent", map[string]any{"agent_name": agentruntime.SwarmAgentID, "client_request_id": "agent-1"}},
		{"settings agent switch", http.MethodPatch, sessionPath + "/settings", map[string]any{"agent_name": agentruntime.SwarmAgentID, "client_request_id": "settings-1"}},
		{"batch archive", http.MethodPost, "/v3/sessions:archive", map[string]any{"session_ids": []string{id}}},
	}
	for _, refusal := range refusals {
		if code, body := companionTestCall(t, server, refusal.method, refusal.path, refusal.body); code != http.StatusConflict {
			t.Fatalf("%s: %d %v, want 409", refusal.name, code, body)
		}
	}
	after, ok, err := sessions.GetSession(id)
	if err != nil || !ok || after.UpdatedAt != before.UpdatedAt || !sessionsV3IsCompanion(after) || sessionEndedState(after) != nil {
		t.Fatalf("a refused control changed the Companion: ok=%v err=%v %v", ok, err, after.Metadata)
	}
	// Chat bulk delete never sees the Companion (it is navigation_hidden, which
	// the visible-session list skips): by id it is not a chat, and by age it is
	// never selected.
	if code, body := companionTestCall(t, server, http.MethodPost, "/v3/sessions:delete", map[string]any{"session_ids": []string{id}, "global": true, "dry_run": true}); code != http.StatusNotFound {
		t.Fatalf("bulk delete by id: %d %v, want 404", code, body)
	}
	future := time.Now().Add(time.Hour).UnixMilli()
	if code, body := companionTestCall(t, server, http.MethodPost, "/v3/sessions:delete", map[string]any{"updated_before": future, "global": true, "dry_run": true}); code != http.StatusOK || strings.Contains(mustJSON(t, body), id) {
		t.Fatalf("bulk delete preview: %d %v", code, body)
	}
	// Metadata cannot strip the marker or unhide the Companion.
	if code, body := companionTestCall(t, server, http.MethodPost, sessionPath+"/metadata", map[string]any{"client_request_id": "meta-1", "metadata": map[string]any{"navigation_hidden": false, pebblestore.SessionCompanionMetadataKey: nil}}); code != http.StatusOK {
		t.Fatalf("metadata update: %d %v", code, body)
	}
	if current, _, _ := sessions.GetSession(id); !sessionsV3IsCompanion(current) || current.Metadata["navigation_hidden"] != true {
		t.Fatalf("metadata unmarked the Companion: %v", current.Metadata)
	}
	// A generic create cannot mint a Companion.
	raw, _ := json.Marshal(map[string]any{"client_request_id": "fake", "workspace_path": workspace, "metadata": map[string]any{pebblestore.SessionCompanionMetadataKey: map[string]any{"generation": 1}}})
	w := httptest.NewRecorder()
	server.handleSessionsV3PrimaryCreate(w, withTestPrincipal(httptest.NewRequest(http.MethodPost, "/v3/sessions", bytes.NewReader(raw))), testPrincipal())
	if w.Code != http.StatusBadRequest {
		t.Fatalf("generic create with Companion marker: %d %s", w.Code, w.Body.String())
	}

	// Explicit delete: confirmation and the current id are required.
	if code, _ := companionTestCall(t, server, http.MethodDelete, CompanionPath, map[string]any{"session_id": id}); code != http.StatusBadRequest {
		t.Fatalf("unconfirmed delete: %d", code)
	}
	if code, _ := companionTestCall(t, server, http.MethodDelete, CompanionPath, map[string]any{"session_id": "companion_stale", "confirm": true}); code != http.StatusConflict {
		t.Fatalf("stale delete: %d", code)
	}
	if _, ok, _ := sessions.GetSession(id); !ok {
		t.Fatal("refused delete removed the Companion")
	}
	code, body = companionTestCall(t, server, http.MethodDelete, CompanionPath, map[string]any{"session_id": id, "confirm": true})
	if code != http.StatusOK || body["deleted_session_id"] != id {
		t.Fatalf("confirmed delete: %d %v", code, body)
	}
	retired := companionTestView(t, body)
	if retired.Generation != 2 || retired.SessionID == id || retired.State != companionStateUncreated {
		t.Fatalf("retired view %+v", retired)
	}
	if _, ok, _ := sessions.GetSession(id); ok {
		t.Fatal("deleted Companion session is still live")
	}
	if code, _ := companionTestCall(t, server, http.MethodDelete, CompanionPath, map[string]any{"session_id": id, "confirm": true}); code != http.StatusConflict {
		t.Fatalf("retried delete: %d", code)
	}
	code, body = companionTestCall(t, server, http.MethodPost, CompanionPath, map[string]any{"workspace_path": workspace})
	fresh := companionTestView(t, body)
	if code != http.StatusOK || body["created"] != true || fresh.SessionID != retired.SessionID || fresh.State != companionStateReady {
		t.Fatalf("ensure after delete: %d %v", code, body)
	}
}

// Purpose: the Companion operating contract is added only to the Companion.
// Requirement: companionInstructions is empty for every other session, so the
// existing orchestrator prompt is unchanged, and for the Companion it allows
// direct help without mandatory task/project creation (help on the person's
// own computer through the machine tools, not delegated repository work),
// names the delegation and context bank tools it really has, and states that
// it never widens policy. Threat: Companion policy leaking into ordinary chats
// or project orchestrators, or a contract promising tools the Companion does
// not get. Boundary: companionInstructions, appended in
// composeSessionV3ResolvedInstructions. Narrowest layer: the pure composer
// (the tool names it uses are checked against the catalog in
// TestManageCompanionNativeMCPAndRouteAgree).
func TestCompanionInstructionsApplyOnlyToTheCompanion(t *testing.T) {
	plain := pebblestore.SessionSnapshot{Metadata: map[string]any{"agent_name": agentruntime.SwarmAgentID}}
	project := pebblestore.SessionSnapshot{Metadata: map[string]any{"agent_name": agentruntime.SwarmOrchestratorAgentID, "project_id": "p1"}}
	for _, session := range []pebblestore.SessionSnapshot{plain, project, {}} {
		if got := companionInstructions(session); got != "" {
			t.Fatalf("non-Companion session got Companion instructions: %q", got)
		}
	}
	companion := pebblestore.SessionSnapshot{Metadata: map[string]any{pebblestore.SessionCompanionMetadataKey: map[string]any{"generation": 1}}}
	got := companionInstructions(companion)
	for _, want := range []string{"never overrides system policy or permissions", "do not need a task, project or plan", "use your machine tools", "not delegated repository work", "manage_projects propose_task", "manage_companion", "record_decision", "retrieved context bank", "Never save it to account memory unless the person explicitly asks", "follow delegated tasks with watch_tasks", "a running command or device action is not interrupted", "When it could mean more than one task, ask which"} {
		if !strings.Contains(got, want) {
			t.Fatalf("Companion instructions missing %q: %q", want, got)
		}
	}
}

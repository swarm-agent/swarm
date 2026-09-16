package tool

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/automation"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/session"
	store "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/workspace"
)

// Purpose: executeManageAutomation must treat implicit review/context on a new
// owned conversation as absence, not an invalid record. Real workspace/session
// services and Pebble exercise the exact tool path through approved-plan pinning
// and a non-applied save proposal. Explicit bad IDs, foreign sessions, absent or
// unapproved instructions and malformed expiry must not create automation state.
func TestAutomationFreshConversationWorkflow(t *testing.T) {
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	principal := identity.Principal{Type: "user", UserID: "owner", AccountScopeID: "account"}
	root := t.TempDir()
	gitCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, args := range [][]string{{"init", "-b", "dev", root}, {"-C", root, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-m", "fixture"}} {
		if output, err := exec.CommandContext(gitCtx, "git", args...).CombinedOutput(); err != nil {
			t.Fatalf("fixture Git: %s: %v", output, err)
		}
	}
	workspaces := workspace.NewService(store.NewWorkspaceStore(db))
	ws, err := workspaces.AddForPrincipal(principal, root, "workspace", "", false)
	if err != nil {
		t.Fatal(err)
	}
	events, err := store.NewEventLog(db)
	if err != nil {
		t.Fatal(err)
	}
	sessions := session.NewService(store.NewSessionStore(db), events)
	for _, entry := range []struct{ id, account string }{{"chat", "account"}, {"foreign", "other"}} {
		_, err := sessions.ApplySessionMutation(session.SessionMutationInput{SessionID: entry.id, UserID: "owner", AccountScopeID: entry.account, Kind: session.SessionMutationCreateSession, Session: &store.SessionSnapshot{ID: entry.id, UserID: "owner", AccountScopeID: entry.account, WorkspacePath: root}, IdempotencyKey: "create-" + entry.id, PayloadHash: "create-" + entry.id})
		if err != nil {
			t.Fatal(err)
		}
	}
	now := time.Unix(1789238436, 0)
	domain, err := automation.New(db, sessions, automationToolAccess{}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	runtime := &Runtime{automations: domain, sessions: sessions, workspace: workspaces}
	scope := WorkspaceScope{SessionID: "chat", Principal: principal, SourceWorkspacePath: root, PrimaryPath: root, Roots: []string{root}}
	call := func(args map[string]any) (map[string]any, error) {
		raw, err := runtime.executeManageAutomation(context.Background(), scope, args)
		if err != nil {
			return nil, err
		}
		var out map[string]any
		err = json.Unmarshal([]byte(raw), &out)
		return out, err
	}
	for _, action := range []string{"review", "context"} {
		out, err := call(map[string]any{"action": action})
		if err != nil {
			t.Fatalf("fresh %s failed: %v", action, err)
		}
		if out["found"] != false || out["state"] != "not_created" || out["instruction"] == "" {
			t.Fatalf("not an actionable empty state: %v", out)
		}
		if _, err := call(map[string]any{"action": action, "id": "missing"}); err == nil {
			t.Fatal("explicit unknown ID hidden as empty discovery")
		}
	}
	definition := map[string]any{"name": "Hourly archive check", "enabled": false, "schedule": map[string]any{"kind": "interval", "interval_seconds": 3600, "missed_policy": "coalesce", "overlap_policy": "serialize"}, "authorization": map[string]any{"mode": "approval_required", "expires_at": 1791830436000}}
	save := map[string]any{"action": "save", "mutation_id": "hourly-check", "definition": definition}
	if _, err := call(save); err == nil || !strings.Contains(err.Error(), "complete structured executable plan") {
		t.Fatalf("missing instructions not diagnosed: %v", err)
	}
	doc := &store.SessionPlanDocument{Title: "Archive check", Info: store.SessionPlanInfo{Goal: "Archive only integrated clean sessions"}, Checkpoints: []store.SessionPlanCheckpoint{{ID: "check", Title: "Check", Status: "pending", Order: 1, Tasks: []string{"Check integration; leave dirty and ambiguous work untouched"}, AcceptanceCriteria: []string{"Only integrated clean sessions qualify"}}}}
	var proposedDefinition store.AutomationDefinition
	var proposedID string
	for _, approval := range []string{"pending", "approved"} {
		prepared, err := sessions.PreparePlanSaveWithMetadata("chat", "plan", doc.Title, "", approval, approval, true, session.PlanSaveMetadata{Document: doc})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := sessions.CommitPreparedPlanSave(prepared, sessions.ApplySessionMutation); err != nil {
			t.Fatal(err)
		}
		out, err := call(save)
		if approval == "pending" {
			if err == nil || !strings.Contains(err.Error(), "explicit user approval") {
				t.Fatalf("unapproved instructions not diagnosed: %v", err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("save with approved instructions: %v", err)
		}
		result := out["result"].(map[string]any)
		proposal := result["proposal"].(map[string]any)
		body := proposal["body"].(map[string]any)
		proposed := body["definition"].(map[string]any)
		encoded, err := json.Marshal(proposed)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(encoded, &proposedDefinition); err != nil {
			t.Fatal(err)
		}
		proposedID = body["id"].(string)
		if result["applied"] != false || result["status"] != "requires_user_approval" || proposal["path"] != "/v3/automations" || body["expected_revision"] != float64(0) || proposed["session_id"] != "chat" || proposed["enabled"] != false {
			t.Fatalf("incorrect proposal: %v", out)
		}
		plans := proposed["plans"].([]any)
		ref := plans[0].(map[string]any)["plan"].(map[string]any)
		if len(plans) != 1 || ref["session_id"] != "chat" || ref["plan_id"] != "plan" || ref["document_sha256"] == "" {
			t.Fatalf("lost executable pins: %v", plans)
		}
		again, err := call(save)
		if err != nil {
			t.Fatal(err)
		}
		if again["result"].(map[string]any)["proposal"].(map[string]any)["body"].(map[string]any)["id"] != body["id"] {
			t.Fatal("retry changed proposal identity")
		}
	}
	definition["authorization"].(map[string]any)["expires_at"] = 1791830436
	if _, err := call(save); err == nil || !strings.Contains(err.Error(), "milliseconds") {
		t.Fatalf("seconds expiry not diagnosed: %v", err)
	}
	scope.SessionID = "foreign"
	if _, err := call(map[string]any{"action": "review"}); err == nil {
		t.Fatal("foreign conversation disclosed")
	}
	rows, _, err := db.SearchAutomationRecords(store.AutomationSearch{Scope: store.AutomationScope{AccountID: "account", WorkspaceID: ws.WorkspaceID}, Kind: "definition", Limit: 10})
	if err != nil || len(rows) != 0 {
		t.Fatal("discovery or proposal persisted automation", err)
	}
	scope.SessionID = "chat"
	runtime.sessions = nil
	if _, err := call(map[string]any{"action": "review"}); err == nil {
		t.Fatal("unavailable session authority hidden as empty state")
	}
	for _, binding := range []*store.SessionAutomationBinding{{AutomationID: "missing", WorkspaceID: ws.WorkspaceID}, {AutomationID: "missing", WorkspaceID: "other"}, {WorkspaceID: ws.WorkspaceID}} {
		runtime.sessions = automationBindingSessionFixture{manageSessionService: sessions, binding: binding}
		for _, action := range []string{"review", "context"} {
			if _, err := call(map[string]any{"action": action}); err == nil {
				t.Fatalf("invalid binding hidden as empty state: %+v", binding)
			}
		}
	}
	runtime.sessions = sessions
	// The separately authenticated user-save boundary persists the proposal;
	// explicit review must then expose the exact paused, unapproved definition.
	canonical := store.AutomationScope{AccountID: "account", WorkspaceID: ws.WorkspaceID}
	userCtx, err := automation.BindRuntimeIdentity(context.Background(), principal, "user", "")
	if err != nil {
		t.Fatal(err)
	}
	user, err := automation.RuntimePrincipal(userCtx)
	if err != nil {
		t.Fatal(err)
	}
	saved, _, err := domain.SaveDefinition(userCtx, user, canonical, proposedID, "user-save", 0, proposedDefinition)
	if err != nil {
		t.Fatal(err)
	}
	out, err := call(map[string]any{"action": "review", "id": proposedID})
	if err != nil {
		t.Fatal(err)
	}
	review := out["review"].(map[string]any)
	if review["state"] != "pending_approval" || review["definition_revision"] != float64(saved.Revision) || review["automation_id"] != proposedID || saved.Definition.Enabled || saved.Definition.Authorization.ApprovalReference != "" {
		t.Fatalf("review misrepresents saved state: %v", out)
	}
}

// Embed the real service for every operation except injecting an inconsistent
// saved binding. This tests that stale/cross-workspace identity cannot become a
// successful fresh-session empty state.
type automationBindingSessionFixture struct {
	manageSessionService
	binding *store.SessionAutomationBinding
}

func (f automationBindingSessionFixture) GetSession(id string) (store.SessionSnapshot, bool, error) {
	s, found, err := f.manageSessionService.GetSession(id)
	s.Automation = f.binding
	return s, found, err
}

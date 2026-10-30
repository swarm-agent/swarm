package run

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	store "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

type recordingWorkflowLifecycle struct {
	calls     int
	principal identity.Principal
	session   string
	args      map[string]any
}

func (r *recordingWorkflowLifecycle) ManageWorkflows(_ context.Context, p identity.Principal, session string, args map[string]any) (any, error) {
	r.calls++
	r.args = args
	r.principal = p
	r.session = session
	return map[string]any{"automation": map[string]any{"name": "fixture"}}, nil
}

// Purpose: the Orchestrator's manage_workflows runs through the catalog
// handler (tool.Runtime, the one Swarm Control runs) with the authenticated
// session identity, never argument-supplied ownership, and answers in the
// catalog envelope. Non-Orchestrator profiles and a task's own session are
// refused before the lifecycle is called; trigger arguments reach the
// service unchanged; unsupported rotation fails without a proposal instead
// of silently succeeding. Boundary: Service.executeManageWorkflowsTool ->
// tool.Runtime.executeManageWorkflows with a recording lifecycle, the
// narrowest layer that holds both the run-side and catalog-side checks.
func TestManageWorkflowsLifecycle(t *testing.T) {
	s, _, db := taskFallbackFixture(t)
	sessions := store.NewSessionStore(db)
	s.sessions = sessionruntime.NewService(sessions, nil)
	runtime := tool.NewRuntime(1)
	t.Cleanup(func() { _ = runtime.Close() })
	runtime.SetManageSessionService(s.sessions)
	s.tools = runtime
	if err := sessions.CreateSession(store.SessionSnapshot{ID: "author", AccountScopeID: "account-a", UserID: "owner"}); err != nil {
		t.Fatal(err)
	}
	if err := sessions.CreateSession(store.SessionSnapshot{ID: "task-session", AccountScopeID: "account-a", UserID: "owner", Metadata: map[string]any{"task_id": "t1"}}); err != nil {
		t.Fatal(err)
	}
	lifecycle := &recordingWorkflowLifecycle{}
	s.SetWorkflowLifecycleService(lifecycle)
	ctx := context.Background()
	out, err := s.executeManageWorkflowsTool(ctx, "author", `{"action":"propose","account":"foreign","session_id":"foreign","on":{"event":{"source":"webhook","id":"id"},"start":{"worker":"worker-id","brief":"Handle event"}}}`, store.AgentProfile{Name: "orchestrator"})
	if err != nil {
		t.Fatal(err)
	}
	if err := tool.CheckEnvelope(out); err != nil {
		t.Fatalf("answer is not a catalog envelope: %v\n%s", err, out)
	}
	var envelope map[string]any
	if err := json.Unmarshal([]byte(out), &envelope); err != nil || envelope["tool"] != "manage_workflows" || envelope["action"] != "propose" || envelope["state"] != "ok" {
		t.Fatalf("envelope: %s %v", out, err)
	}
	if lifecycle.calls != 1 || lifecycle.principal.AccountScopeID != "account-a" || lifecycle.principal.UserID != "owner" || lifecycle.session != "author" {
		t.Fatal("incorrect lifecycle identity", lifecycle)
	}
	if lifecycle.args["on"].(map[string]any)["start"].(map[string]any)["worker"] != "worker-id" {
		t.Fatal("trigger lost")
	}
	if _, err := s.executeManageWorkflowsTool(ctx, "author", `{"action":"propose"}`, store.AgentProfile{Name: "coder"}); err == nil {
		t.Fatal("coder allowed")
	}
	if _, err := s.executeManageWorkflowsTool(ctx, "task-session", `{"action":"list"}`, store.AgentProfile{Name: "orchestrator"}); err == nil || !strings.Contains(err.Error(), "home's Orchestrator") {
		t.Fatalf("a task's own session reached workflows: %v", err)
	}
	if _, err := s.executeManageWorkflowsTool(ctx, "author", `{"action":"propose","rotate_key":true}`, store.AgentProfile{Name: "orchestrator"}); err == nil {
		t.Fatal("rotation silently ignored")
	}
	if _, err := s.executeManageWorkflowsTool(ctx, "author", `{"action":"accept","id":"x"}`, store.AgentProfile{Name: "orchestrator"}); err == nil {
		t.Fatal("an undeclared action ran")
	}
	if lifecycle.calls != 1 {
		t.Fatalf("refused calls reached the lifecycle: %d", lifecycle.calls)
	}
}

package run

import (
	"context"
	"encoding/json"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	store "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

type recordingJobsService struct {
	recordingWorkflowLifecycle
	calls     int
	principal identity.Principal
	agent     string
	args      map[string]any
}

func (r *recordingJobsService) ManageJobs(_ context.Context, p identity.Principal, session, agent string, args map[string]any) (any, error) {
	r.calls++
	r.principal = p
	r.agent = agent
	r.args = args
	return map[string]any{"session": session}, nil
}

// Purpose: the native control-plane boundary must use the executing session's
// principal and resolved tool contract, not model-supplied identity, and run
// through the catalog handler (tool.Runtime.executeManageJobs, the one Swarm
// Control runs), answering in the catalog envelope. Recording the shared
// service is the narrowest layer proving foreign/disabled calls and
// undeclared actions have no service effects while custom agents and Swarm
// can use the registered tool. Boundary: Service.executeManageJobsTool ->
// tool.Runtime -> tool.JobsService.
func TestManageJobsNativeBoundary(t *testing.T) {
	s, _, db := taskFallbackFixture(t)
	ss := store.NewSessionStore(db)
	s.sessions = sessionruntime.NewService(ss, nil)
	runtime := tool.NewRuntime(1)
	t.Cleanup(func() { _ = runtime.Close() })
	runtime.SetManageSessionService(s.sessions)
	s.tools = runtime
	if err := ss.CreateSession(store.SessionSnapshot{ID: "watch", AccountScopeID: "account", UserID: "owner"}); err != nil {
		t.Fatal(err)
	}
	svc := &recordingJobsService{}
	s.SetWorkflowLifecycleService(svc)
	p := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "account", UserID: "owner"}
	profile := store.AgentProfile{Name: "watcher", ToolContract: &store.AgentToolContract{Tools: map[string]store.AgentToolConfig{"manage_jobs": {Enabled: store.BoolPtr(true)}}}}
	args := `{"action":"list_runs","account":"foreign","agent":"swarm"}`
	out, err := s.executeManageJobsTool(context.Background(), p, "watch", args, profile)
	if err != nil {
		t.Fatal(err)
	}
	if svc.calls != 1 || svc.principal != p || svc.agent != "watcher" {
		t.Fatal("identity mismatch")
	}
	if err := tool.CheckEnvelope(out); err != nil {
		t.Fatalf("answer is not a catalog envelope: %v\n%s", err, out)
	}
	var envelope map[string]any
	if err := json.Unmarshal([]byte(out), &envelope); err != nil || envelope["tool"] != "manage_jobs" || envelope["action"] != "list_runs" || envelope["state"] != "ok" || envelope["data"].(map[string]any)["session"] != "watch" {
		t.Fatalf("envelope: %s %v", out, err)
	}
	// An undeclared action never reaches the service.
	if _, err := s.executeManageJobsTool(context.Background(), p, "watch", `{"action":"delete_everything"}`, profile); err == nil {
		t.Fatal("an undeclared action ran")
	}
	foreign := p
	foreign.UserID = "other"
	if _, err := s.executeManageJobsTool(context.Background(), foreign, "watch", args, profile); err == nil {
		t.Fatal("foreign principal accepted")
	}
	profile.ToolContract = nil
	if _, err := s.executeManageJobsTool(context.Background(), p, "watch", args, profile); err == nil {
		t.Fatal("disabled tool accepted")
	}
	if svc.calls != 1 {
		t.Fatal("rejected call reached service")
	}
}

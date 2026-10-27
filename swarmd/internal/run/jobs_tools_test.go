package run

import (
	"context"
	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	store "swarm/packages/swarmd/internal/store/pebble"
	"testing"
)

type recordingJobsService struct {
	recordingWorkflowLifecycle
	calls     int
	principal identity.Principal
	agent     string
}

func (r *recordingJobsService) ManageJobs(_ context.Context, p identity.Principal, session, agent string, args map[string]any) (any, error) {
	r.calls++
	r.principal = p
	r.agent = agent
	return map[string]any{"session": session}, nil
}

// Purpose: the native control-plane boundary must use the executing session's
// principal and resolved tool contract, not model-supplied identity. Recording
// the shared service is the narrowest layer proving foreign/disabled calls have
// no service effects while custom agents and Swarm can use the registered tool.
func TestManageJobsNativeBoundary(t *testing.T) {
	s, _, db := taskFallbackFixture(t)
	ss := store.NewSessionStore(db)
	s.sessions = sessionruntime.NewService(ss, nil)
	if err := ss.CreateSession(store.SessionSnapshot{ID: "watch", AccountScopeID: "account", UserID: "owner"}); err != nil {
		t.Fatal(err)
	}
	svc := &recordingJobsService{}
	s.SetWorkflowLifecycleService(svc)
	p := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "account", UserID: "owner"}
	profile := store.AgentProfile{Name: "watcher", ToolContract: &store.AgentToolContract{Tools: map[string]store.AgentToolConfig{"manage_jobs": {Enabled: store.BoolPtr(true)}}}}
	args := `{"action":"list_runs","account":"foreign","agent":"swarm"}`
	if _, err := s.executeManageJobsTool(context.Background(), p, "watch", args, profile); err != nil {
		t.Fatal(err)
	}
	if svc.calls != 1 || svc.principal != p || svc.agent != "watcher" {
		t.Fatal("identity mismatch")
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

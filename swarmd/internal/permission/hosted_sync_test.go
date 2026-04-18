package permission

import (
	"context"
	"path/filepath"
	"testing"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

func TestHostedPermissionsCreateAndWaitMirrorLocalState(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "hosted-permission-sync.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}
	sessionSvc := sessionruntime.NewService(pebblestore.NewSessionStore(store), events)
	session, _, err := sessionSvc.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		SessionID:     "session-routed",
		Title:         "Hosted Session",
		WorkspacePath: "/runtime/workspace",
		WorkspaceName: "workspace",
		Mode:          sessionruntime.ModePlan,
		Preference: &pebblestore.ModelPreference{
			Provider: "codex",
			Model:    "gpt-5.4",
			Thinking: "medium",
		},
		Metadata: sessionruntime.HostedSessionDescriptor{
			HostSwarmID:          "host-swarm",
			HostBackendURL:       "http://host.invalid",
			HostWorkspacePath:    "/host/workspace",
			RuntimeWorkspacePath: "/runtime/workspace",
			ChildSwarmID:         "child-swarm",
		}.WithMetadata(nil),
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	svc := NewService(pebblestore.NewPermissionStore(store), nil, nil)
	svc.SetSessionResolver(sessionSvc)
	sync := &fakeHostedPermissionSync{
		createRecord: pebblestore.PermissionRecord{
			ID:                  "perm-1",
			SessionID:           session.ID,
			RunID:               "run-1",
			CallID:              "call-1",
			ToolName:            "bash",
			ToolArguments:       `{"cmd":"pwd"}`,
			Requirement:         "tool",
			Mode:                "plan",
			Status:              pebblestore.PermissionStatusPending,
			PermissionRequested: 100,
			CreatedAt:           100,
			UpdatedAt:           100,
		},
		waitRecord: pebblestore.PermissionRecord{
			ID:                  "perm-1",
			SessionID:           session.ID,
			RunID:               "run-1",
			CallID:              "call-1",
			ToolName:            "bash",
			ToolArguments:       `{"cmd":"pwd"}`,
			Requirement:         "tool",
			Mode:                "plan",
			Status:              pebblestore.PermissionStatusApproved,
			Decision:            DecisionApprove,
			Reason:              "approved",
			PermissionRequested: 100,
			ResolvedAt:          200,
			CreatedAt:           100,
			UpdatedAt:           200,
			ExecutionStatus:     pebblestore.PermissionExecQueued,
		},
	}
	svc.SetHostedSync(sync)

	record, err := svc.CreatePending(CreateInput{
		SessionID:     session.ID,
		RunID:         "run-1",
		CallID:        "call-1",
		ToolName:      "bash",
		ToolArguments: `{"cmd":"pwd"}`,
		Requirement:   "tool",
		Mode:          "plan",
	})
	if err != nil {
		t.Fatalf("create pending: %v", err)
	}
	if sync.createCalls != 1 {
		t.Fatalf("create calls = %d, want 1", sync.createCalls)
	}
	if record.ID != "perm-1" {
		t.Fatalf("record id = %q, want perm-1", record.ID)
	}

	pending, err := svc.ListPermissions(session.ID, 10)
	if err != nil {
		t.Fatalf("list permissions: %v", err)
	}
	if len(pending) != 1 || pending[0].Status != pebblestore.PermissionStatusPending {
		t.Fatalf("pending permissions = %+v, want single pending record", pending)
	}

	resolved, err := svc.WaitForResolution(context.Background(), session.ID, record.ID)
	if err != nil {
		t.Fatalf("wait for resolution: %v", err)
	}
	if sync.waitCalls != 1 {
		t.Fatalf("wait calls = %d, want 1", sync.waitCalls)
	}
	if resolved.Status != pebblestore.PermissionStatusApproved {
		t.Fatalf("resolved status = %q, want approved", resolved.Status)
	}

	stored, ok, err := pebblestore.NewPermissionStore(store).GetPermission(session.ID, record.ID)
	if err != nil {
		t.Fatalf("get mirrored permission: %v", err)
	}
	if !ok {
		t.Fatalf("mirrored permission missing")
	}
	if stored.Status != pebblestore.PermissionStatusApproved {
		t.Fatalf("stored status = %q, want approved", stored.Status)
	}
}

func TestHostOwnedRoutedSessionPermissionsStayLocalOnHost(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "host-owned-routed-permission.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}
	sessionSvc := sessionruntime.NewService(pebblestore.NewSessionStore(store), events)
	sessionSvc.SetLocalSwarmIDResolver(func() string { return "host-swarm" })
	session, _, err := sessionSvc.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		SessionID:     "session-host-owned",
		Title:         "Host Owned Routed Session",
		WorkspacePath: "/host/workspace",
		WorkspaceName: "workspace",
		Mode:          sessionruntime.ModePlan,
		Preference: &pebblestore.ModelPreference{
			Provider: "codex",
			Model:    "gpt-5.4",
			Thinking: "medium",
		},
		Metadata: sessionruntime.HostedSessionDescriptor{
			HostSwarmID:          "host-swarm",
			HostBackendURL:       "http://127.0.0.1:8781",
			HostWorkspacePath:    "/host/workspace",
			RuntimeWorkspacePath: "/runtime/workspace",
			ChildSwarmID:         "child-swarm",
		}.WithMetadata(nil),
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	svc := NewService(pebblestore.NewPermissionStore(store), nil, nil)
	svc.SetSessionResolver(sessionSvc)
	svc.SetLocalSwarmIDResolver(func() string { return "host-swarm" })
	sync := &fakeHostedPermissionSync{}
	svc.SetHostedSync(sync)

	record, err := svc.CreatePending(CreateInput{
		SessionID:     session.ID,
		RunID:         "run-1",
		CallID:        "call-1",
		ToolName:      "bash",
		ToolArguments: `{"cmd":"pwd"}`,
		Requirement:   "tool",
		Mode:          "plan",
	})
	if err != nil {
		t.Fatalf("create pending: %v", err)
	}
	if sync.createCalls != 0 {
		t.Fatalf("create calls = %d, want 0 for host-owned routed session", sync.createCalls)
	}
	if record.Status != pebblestore.PermissionStatusPending {
		t.Fatalf("status = %q, want pending", record.Status)
	}
}

type fakeHostedPermissionSync struct {
	createRecord pebblestore.PermissionRecord
	waitRecord   pebblestore.PermissionRecord
	createCalls  int
	waitCalls    int
}

func (f *fakeHostedPermissionSync) CreatePending(context.Context, sessionruntime.HostedSessionDescriptor, CreateInput) (pebblestore.PermissionRecord, error) {
	f.createCalls++
	return f.createRecord, nil
}

func (f *fakeHostedPermissionSync) WaitForResolution(context.Context, sessionruntime.HostedSessionDescriptor, string, string) (pebblestore.PermissionRecord, error) {
	f.waitCalls++
	return f.waitRecord, nil
}

func (f *fakeHostedPermissionSync) CancelRunPending(context.Context, sessionruntime.HostedSessionDescriptor, string, string, string) ([]pebblestore.PermissionRecord, error) {
	return nil, nil
}

func (f *fakeHostedPermissionSync) MarkToolStarted(context.Context, sessionruntime.HostedSessionDescriptor, string, string, string, int, int64) (pebblestore.PermissionRecord, bool, error) {
	return pebblestore.PermissionRecord{}, false, nil
}

func (f *fakeHostedPermissionSync) MarkToolCompleted(context.Context, sessionruntime.HostedSessionDescriptor, string, string, string, int, tool.Result, int64) (pebblestore.PermissionRecord, bool, error) {
	return pebblestore.PermissionRecord{}, false, nil
}

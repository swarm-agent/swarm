package permission

import (
	"path/filepath"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestBashAuthorizationRecordsPreserveExactArgumentsAndHonestState(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "bash-authorizations.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(pebblestore.NewPermissionStore(store), events, nil)
	arguments := `{"command":"printf '%s\\n' exact","explanation":"Print the requested value."}`

	pending, err := svc.CreatePending(CreateInput{
		SessionID: "session", RunID: "run", CallID: "pending", ToolName: "bash",
		ToolArguments: arguments, ToolCallArguments: arguments, Requirement: "bash", Mode: "auto",
		AuthorizationSource: "default", Reason: "approval required",
	})
	if err != nil {
		t.Fatal(err)
	}
	if pending.Status != pebblestore.PermissionStatusPending || pending.Decision != string(AuthorizationPending) || pending.AuthorizationSource != "default" || pending.Reason != "approval required" || pending.ExecutionStatus != pebblestore.PermissionExecWaitingApproval {
		t.Fatalf("pending authorization state = %#v", pending)
	}
	if pending.ToolCallArguments != arguments || pending.ToolArguments != arguments {
		t.Fatalf("pending arguments changed: tool=%q call=%q", pending.ToolArguments, pending.ToolCallArguments)
	}

	authorized, err := svc.RecordAuthorization(CreateInput{
		SessionID: "session", RunID: "run", CallID: "automatic", ToolName: "bash",
		ToolArguments: arguments, ToolCallArguments: arguments, Requirement: "bash", Mode: "auto",
		Status: pebblestore.PermissionStatusNotRequired, Decision: string(AuthorizationApprove),
		AuthorizationSource: "bypass_permissions", Reason: "permissions are bypassed",
		ExecutionStatus: pebblestore.PermissionExecQueued,
	})
	if err != nil {
		t.Fatal(err)
	}
	if authorized.Status != pebblestore.PermissionStatusNotRequired || authorized.Decision != string(AuthorizationApprove) || authorized.AuthorizationSource != "bypass_permissions" || authorized.PermissionRequested != 0 || authorized.ExecutionStatus != pebblestore.PermissionExecQueued {
		t.Fatalf("automatic authorization state = %#v", authorized)
	}
	if authorized.ToolCallArguments != arguments || authorized.ToolArguments != arguments {
		t.Fatalf("automatic arguments changed: tool=%q call=%q", authorized.ToolArguments, authorized.ToolCallArguments)
	}

	waiting, err := svc.store.ListRunWaits("session", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(waiting) != 1 || len(waiting[0].PendingPermissionIDs) != 1 || waiting[0].PendingPermissionIDs[0] != pending.ID {
		t.Fatalf("automatic authorization created an approval wait: %#v", waiting)
	}
}

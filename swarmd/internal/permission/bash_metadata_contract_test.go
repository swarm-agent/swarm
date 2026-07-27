package permission

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestAuthorizeBashPermissionPreservesDesktopAndTUIMetadataContract(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "bash-permission-contract.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}
	sessions := sessionruntime.NewService(pebblestore.NewSessionStore(store), events)
	session, _, err := sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		SessionID:      "session-bash-contract",
		Title:          "Bash contract",
		WorkspacePath:  "/workspace",
		WorkspaceName:  "workspace",
		Mode:           sessionruntime.ModeAuto,
		UserID:         "user-bash-contract",
		AccountScopeID: "account-bash-contract",
		Preference: &pebblestore.ModelPreference{
			Provider: "test-provider",
			Model:    "test-model",
			Thinking: "medium",
		},
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	arguments := `{"command":"python3 listener.py","explanation":["Start a listener on TCP port 8080.","Expose it on public network interfaces."],"category":"write","critical":true}`
	service := NewService(pebblestore.NewPermissionStore(store), events, nil)
	service.SetSessionResolver(sessions)
	authorized, err := service.AuthorizeToolCall(AuthorizationInput{
		SessionID:      session.ID,
		AccountScopeID: session.AccountScopeID,
		RunID:          "run-bash-contract",
		CallID:         "call-bash-contract",
		ToolName:       "bash",
		ToolArguments:  arguments,
		Mode:           sessionruntime.ModeAuto,
	})
	if err != nil {
		t.Fatalf("authorize Bash: %v", err)
	}
	if authorized.Decision != AuthorizationPending || authorized.Record == nil {
		t.Fatalf("authorization = %#v, want pending record", authorized)
	}

	var got map[string]any
	if err := json.Unmarshal([]byte(authorized.Record.ToolArguments), &got); err != nil {
		t.Fatalf("decode stored arguments: %v", err)
	}
	if got["command"] != "python3 listener.py" || got["category"] != "write" || got["critical"] != true {
		t.Fatalf("stored Bash metadata = %#v", got)
	}
	if want := []any{"Start a listener on TCP port 8080.", "Expose it on public network interfaces."}; !reflect.DeepEqual(got["explanation"], want) {
		t.Fatalf("stored explanation = %#v, want %#v", got["explanation"], want)
	}
	if authorized.Record.ToolName != "bash" || authorized.Record.Status != pebblestore.PermissionStatusPending || authorized.Record.ExecutionStatus != pebblestore.PermissionExecWaitingApproval {
		t.Fatalf("stored permission record = %#v", authorized.Record)
	}
}

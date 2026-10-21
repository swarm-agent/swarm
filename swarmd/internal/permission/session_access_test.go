package permission

import (
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type sessionAccessLookup map[string]pebblestore.SessionSnapshot

func (l sessionAccessLookup) GetSession(sessionID string) (pebblestore.SessionSnapshot, bool, error) {
	session, found := l[sessionID]
	return session, found, nil
}

func TestSessionTasksFreeApprovesOnlyDelegationInThatSession(t *testing.T) {
	service, _ := openSubagentReservationTestServices(t)
	service.SetSessionResolver(sessionAccessLookup{
		"free": {ID: "free", Metadata: map[string]any{pebblestore.SessionAccessMetadataKey: map[string]any{"tasks": "free"}}},
		"ask":  {ID: "ask", Metadata: map[string]any{pebblestore.SessionAccessMetadataKey: map[string]any{"tasks": "ask"}}},
		"none": {ID: "none", Metadata: map[string]any{}},
	})
	authorize := func(sessionID, args string) AuthorizationResult {
		t.Helper()
		result, err := service.AuthorizeToolCall(AuthorizationInput{SessionID: sessionID, AccountScopeID: "acct", RunID: "run-" + sessionID, CallID: "call-" + sessionID + args, ToolName: "manage_projects", ToolArguments: args, Mode: "auto"})
		if err != nil {
			t.Fatalf("authorize %s: %v", sessionID, err)
		}
		return result
	}
	delegate := `{"action":"propose_task","project_id":"p","agent":"coder","title":"Fix it","auto_approve":true}`
	if got := authorize("free", delegate); got.Decision != AuthorizationApprove || got.Source != "session_access" {
		t.Fatalf("free session delegation = %+v, want approved by session_access", got)
	}
	for _, sessionID := range []string{"ask", "none"} {
		if got := authorize(sessionID, delegate); got.Decision == AuthorizationApprove {
			t.Fatalf("%s session started a task without approval: %+v", sessionID, got)
		}
	}
	// Media and other launches keep their own approval even in a free session.
	media := `{"action":"propose_task","project_id":"p","agent":"image","title":"Logo","auto_approve":true}`
	if got := authorize("free", media); got.Source == "session_access" {
		t.Fatalf("free session approved media generation: %+v", got)
	}
	remove := `{"action":"delete_task","project_id":"p","task_id":"t"}`
	if got := authorize("free", remove); got.Source == "session_access" {
		t.Fatalf("free session approved a removal: %+v", got)
	}
}

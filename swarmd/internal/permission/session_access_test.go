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

func TestSessionTasksFreeApprovesOnlyTasksInThatSession(t *testing.T) {
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
	// A media task is delegation too in a free session, and nowhere else.
	media := `{"action":"propose_task","project_id":"p","agent":"image","title":"Logo","auto_approve":true}`
	if got := authorize("free", media); got.Source != "session_access" {
		t.Fatalf("free session did not start the media task: %+v", got)
	}
	for _, sessionID := range []string{"ask", "none"} {
		if got := authorize(sessionID, media); got.Decision == AuthorizationApprove {
			t.Fatalf("%s session started a media task without approval: %+v", sessionID, got)
		}
	}
	remove := `{"action":"delete_task","project_id":"p","task_id":"t"}`
	if got := authorize("free", remove); got.Source == "session_access" {
		t.Fatalf("free session approved a removal: %+v", got)
	}
}

func TestMachineToolsFollowTheSessionAccess(t *testing.T) {
	service, _ := openSubagentReservationTestServices(t)
	access := func(machine string) pebblestore.SessionSnapshot {
		return pebblestore.SessionSnapshot{Metadata: map[string]any{pebblestore.SessionAccessMetadataKey: map[string]any{"machine": machine}}}
	}
	service.SetSessionResolver(sessionAccessLookup{"never": {}, "none": access("none"), "ask": access("ask"), "full": access("full")})
	cases := []struct {
		session, tool string
		refused       bool
	}{
		{"never", pebblestore.MachineToolRun, true},
		{"none", pebblestore.MachineToolRun, true},
		{"none", pebblestore.MachineToolWrite, true},
		{"none", pebblestore.MachineToolRequestAccess, false},
		{"ask", pebblestore.MachineToolRequestAccess, true},
		{"ask", pebblestore.MachineToolRun, false},
		{"full", pebblestore.MachineToolRead, false},
		{"full", pebblestore.MachineToolRequestAccess, true},
	}
	for _, c := range cases {
		result, err := service.AuthorizeToolCall(AuthorizationInput{SessionID: c.session, AccountScopeID: "acct", RunID: "run", CallID: c.session + c.tool, ToolName: c.tool, ToolArguments: `{}`, Mode: "auto+bypass_permissions", InputBoundary: true})
		if err != nil {
			t.Fatalf("%s %s: %v", c.session, c.tool, err)
		}
		if refused := result.Decision == AuthorizationDeny; refused != c.refused {
			t.Fatalf("%s %s: decision %+v, refused want %v", c.session, c.tool, result, c.refused)
		}
		if !c.refused && result.Decision != AuthorizationPending {
			t.Fatalf("%s %s: allowed call must wait for the app, got %+v", c.session, c.tool, result)
		}
	}
}

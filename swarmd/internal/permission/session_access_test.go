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

// Purpose: legacy tasks metadata must never grant authority. The service gate
// must retain normal approval requirements for both code and media delegation.
func TestLegacySessionTasksCannotApprove(t *testing.T) {
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
	if got := authorize("free", delegate); got.Decision == AuthorizationApprove {
		t.Fatalf("free session delegation = %+v, want approved by session_access", got)
	}
	for _, sessionID := range []string{"ask", "none"} {
		if got := authorize(sessionID, delegate); got.Decision == AuthorizationApprove {
			t.Fatalf("%s session started a task without approval: %+v", sessionID, got)
		}
	}
	// A media task is delegation too in a free session, and nowhere else.
	media := `{"action":"propose_task","project_id":"p","agent":"image","title":"Logo","auto_approve":true}`
	if got := authorize("free", media); got.Decision == AuthorizationApprove {
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

// Purpose: legacy device grants cannot authorize a disconnected device or grant request.
func TestLegacyMachineAccessCannotApprove(t *testing.T) {
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
		{"none", pebblestore.MachineToolRequestAccess, true},
		{"ask", pebblestore.MachineToolRequestAccess, true},
		{"ask", pebblestore.MachineToolRun, true},
		{"full", pebblestore.MachineToolRead, true},
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

// Purpose: AuthorizeToolCall must ignore legacy fleet grants even with bypass.
// Missing account entries default to ask and fixed forbidden operations deny.
func TestFleetSessionAccessGate(t *testing.T) {
	service, _ := openSubagentReservationTestServices(t)
	lookup := sessionAccessLookup{}
	for _, level := range []string{"none", "ask", "full"} {
		lookup[level] = pebblestore.SessionSnapshot{ID: level, AccountScopeID: "acct", Metadata: map[string]any{pebblestore.SessionAccessMetadataKey: pebblestore.SessionAccessMetadata(pebblestore.SessionAccess{Tasks: "free", Fleet: map[string]string{"box": level}})}}
	}
	service.SetSessionResolver(lookup)
	for _, tc := range []struct {
		session, action string
		want            AuthorizationDecision
	}{{"none", "propose_task", AuthorizationPending}, {"ask", "propose_task", AuthorizationPending}, {"full", "propose_task", AuthorizationPending}, {"full", "integrate_task", AuthorizationDeny}, {"full", "report_task", AuthorizationDeny}, {"ask", "report_task", AuthorizationDeny}} {
		args := `{"machine":"box","action":"` + tc.action + `","title":"Fix it","agent":"coder"}`
		got, err := service.AuthorizeToolCall(AuthorizationInput{SessionID: tc.session, AccountScopeID: "acct", RunID: "run", CallID: tc.session + tc.action, ToolName: "manage_projects", ToolArguments: args, Mode: "auto+bypass_permissions"})
		if err != nil || got.Decision != tc.want {
			t.Fatalf("%+v: %+v %v", tc, got, err)
		}
		if tc.want == AuthorizationPending {
			if got.Record == nil || got.Record.ToolArguments != permissionStoredArguments(args) {
				t.Fatalf("missing exact pending call: %+v", got)
			}
		} else if got.Record != nil {
			t.Fatal("unexpected pending record")
		}
	}
}

// Purpose: the account linked-work boundary in AuthorizeToolCall must gate
// workflow/worker mutations even under bypass, while ask reads need no card.
// This service-layer test checks exact pending arguments and no card on denial.
func TestFleetWorkRules(t *testing.T) {
	db, err := pebblestore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	service := NewService(pebblestore.NewPermissionStore(db), nil, nil)
	service.SetSessionResolver(sessionAccessLookup{"home": {ID: "home", AccountScopeID: "acct"}})
	for _, level := range []string{"off", "ask", "allowed"} {
		account, err := service.store.GetAccountPermissions("acct")
		if err != nil {
			t.Fatal(err)
		}
		account.Linked["build-box"] = pebblestore.LinkedPermissionRule{Work: level}
		if _, err := db.SetAccountPermissions("acct", account.Revision, account); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"manage_workflows", "manage_workers"} {
			for _, action := range []string{"list", "get", "delete", "secrets", "permissions"} {
				want := AuthorizationApprove
				if action == "secrets" || action == "permissions" || level == "off" {
					want = AuthorizationDeny
				} else if action == "delete" && level == "ask" {
					want = AuthorizationPending
				}
				args := `{"machine":"build-box","action":"` + action + `","id":"record"}`
				got, err := service.AuthorizeToolCall(AuthorizationInput{SessionID: "home", AccountScopeID: "acct", RunID: "run", CallID: level + name + action, ToolName: name, ToolArguments: args, Mode: "auto+bypass_permissions"})
				if err != nil || got.Decision != want {
					t.Fatalf("%s %s %s: %+v %v", level, name, action, got, err)
				}
				if want == AuthorizationPending {
					if got.Record == nil || got.Record.ToolArguments != permissionStoredArguments(args) {
						t.Fatal("missing exact pending call")
					}
				} else if got.Record != nil {
					t.Fatal("unexpected pending card")
				}
			}
		}
	}
}

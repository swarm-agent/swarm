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

// Purpose: legacy tasks metadata must never grant authority: the account Start
// tasks rule alone decides. Under approve, starting a task waits for the person
// in every session (free, ask or none), even with bypass; creating a task needs
// no approval because the runtime makes it wait. Removal keeps its own policy.
func TestLegacySessionTasksCannotApprove(t *testing.T) {
	db, err := pebblestore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	p := pebblestore.DefaultAccountPermissions()
	p.Tasks = "approve"
	if _, err := db.SetAccountPermissions("acct", 0, p); err != nil {
		t.Fatal(err)
	}
	service := NewService(pebblestore.NewPermissionStore(db), nil, nil)
	service.SetSessionResolver(sessionAccessLookup{
		"free": {ID: "free", Metadata: map[string]any{pebblestore.SessionAccessMetadataKey: map[string]any{"tasks": "free"}}},
		"ask":  {ID: "ask", Metadata: map[string]any{pebblestore.SessionAccessMetadataKey: map[string]any{"tasks": "ask"}}},
		"none": {ID: "none", Metadata: map[string]any{}},
	})
	authorize := func(sessionID, args, mode string) AuthorizationResult {
		t.Helper()
		result, err := service.AuthorizeToolCall(AuthorizationInput{SessionID: sessionID, AccountScopeID: "acct", RunID: "run-" + sessionID, CallID: "call-" + sessionID + args + mode, ToolName: "manage_projects", ToolArguments: args, Mode: mode})
		if err != nil {
			t.Fatalf("authorize %s: %v", sessionID, err)
		}
		return result
	}
	for _, sessionID := range []string{"free", "ask", "none"} {
		for _, mode := range []string{"auto", "auto+bypass_permissions"} {
			start := `{"action":"approve_task","project_id":"p","task_id":"t"}`
			if got := authorize(sessionID, start, mode); got.Decision != AuthorizationPending || got.Source != "account_permissions" {
				t.Fatalf("%s %s: a task started without approval under approve: %+v", sessionID, mode, got)
			}
			for _, create := range []string{`{"action":"propose_task","project_id":"p","agent":"coder","title":"Fix it","auto_approve":true}`, `{"action":"propose_task","project_id":"p","agent":"image","title":"Logo","auto_approve":true}`} {
				if got := authorize(sessionID, create, mode); got.Decision != AuthorizationApprove || got.Source != "account_permissions" {
					t.Fatalf("%s %s: create must not ask (the task waits as a card): %+v", sessionID, mode, got)
				}
			}
		}
	}
	remove := `{"action":"delete_task","project_id":"p","task_id":"t"}`
	if got := authorize("free", remove, "auto"); got.Source == "session_access" || got.Source == "account_permissions" {
		t.Fatalf("free session approved a removal: %+v", got)
	}
}

// Purpose: the tool policy no longer asks the person to start or integrate a
// home task when Start tasks is auto, with or without bypass; integrating never
// asks; under approve, approve/accept/deploy wait for the person. The account
// record in a temporary store is the narrowest layer proving the single gate.
func TestStartTasksRuleIsTheOnlyTaskGate(t *testing.T) {
	db, err := pebblestore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	service := NewService(pebblestore.NewPermissionStore(db), nil, nil)
	service.SetSessionResolver(sessionAccessLookup{"home": {ID: "home", AccountScopeID: "acct"}})
	for _, rule := range []string{"auto", "approve"} {
		p, err := db.GetAccountPermissions("acct")
		if err != nil {
			t.Fatal(err)
		}
		p.Tasks = rule
		if _, err := db.SetAccountPermissions("acct", p.Revision, p); err != nil {
			t.Fatal(err)
		}
		for _, mode := range []string{"auto", "auto+bypass_permissions"} {
			for _, action := range []string{"approve_task", "accept_task", "deploy_task", "integrate_task", "create_task"} {
				args := `{"action":"` + action + `","project_id":"p","task_id":"t","agent":"image","title":"x","auto_approve":false}`
				got, err := service.AuthorizeToolCall(AuthorizationInput{SessionID: "home", AccountScopeID: "acct", RunID: "run", CallID: rule + mode + action, ToolName: "manage_projects", ToolArguments: args, Mode: mode})
				if err != nil {
					t.Fatal(err)
				}
				want := AuthorizationApprove
				if rule == "approve" && (action == "approve_task" || action == "accept_task" || action == "deploy_task") {
					want = AuthorizationPending
				}
				if got.Decision != want {
					t.Fatalf("%s %s %s: %+v, want %s", rule, mode, action, got, want)
				}
			}
		}
	}
	// Removal keeps its own approval.
	got, err := service.AuthorizeToolCall(AuthorizationInput{SessionID: "home", AccountScopeID: "acct", RunID: "run", CallID: "delete", ToolName: "manage_projects", ToolArguments: `{"action":"delete_task","project_id":"p","task_id":"t"}`, Mode: "auto"})
	if err != nil || got.Decision != AuthorizationPending {
		t.Fatalf("delete_task: %+v %v", got, err)
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
// Missing account entries default to auto (a new link runs without asking) and
// fixed forbidden operations deny.
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
	}{{"none", "propose_task", AuthorizationApprove}, {"ask", "propose_task", AuthorizationApprove}, {"full", "propose_task", AuthorizationApprove}, {"full", "integrate_task", AuthorizationDeny}, {"full", "report_task", AuthorizationDeny}, {"ask", "report_task", AuthorizationDeny}} {
		args := `{"machine":"box","action":"` + tc.action + `","title":"Fix it","agent":"coder"}`
		got, err := service.AuthorizeToolCall(AuthorizationInput{SessionID: tc.session, AccountScopeID: "acct", RunID: "run", CallID: tc.session + tc.action, ToolName: "manage_projects", ToolArguments: args, Mode: "auto+bypass_permissions"})
		if err != nil || got.Decision != tc.want {
			t.Fatalf("%+v: %+v %v", tc, got, err)
		}
		if got.Record != nil {
			t.Fatal("unexpected pending record")
		}
	}
}

// Purpose: the account linked-work boundary in AuthorizeToolCall gates every
// linked action even under bypass: off denies, and ask waits for the person
// only before a task is sent or started on the machine; workflow and worker
// changes, reads and other task actions run. auto (and the retired allowed,
// stored as auto) runs everything. Pending calls keep their exact arguments.
func TestFleetWorkRules(t *testing.T) {
	db, err := pebblestore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	service := NewService(pebblestore.NewPermissionStore(db), nil, nil)
	service.SetSessionResolver(sessionAccessLookup{"home": {ID: "home", AccountScopeID: "acct"}})
	for _, level := range []string{"off", "ask", "auto", "allowed"} {
		account, err := service.store.GetAccountPermissions("acct")
		if err != nil {
			t.Fatal(err)
		}
		account.Linked["build-box"] = pebblestore.LinkedPermissionRule{Work: level}
		if _, err := db.SetAccountPermissions("acct", account.Revision, account); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"manage_workflows", "manage_workers", "manage_projects"} {
			actions := []string{"list", "get", "delete", "secrets", "permissions"}
			switch name {
			case "manage_workflows":
				actions = append(actions, "propose", "enable", "disable")
			case "manage_workers":
				actions = append(actions, "pause", "resume", "archive")
			default:
				actions = []string{"list", "get", "list_tasks", "get_task", "archive_task", "propose_task", "create_task", "approve_task", "accept_task", "reopen_task", "refine_task", "integrate_task"}
			}
			for _, action := range actions {
				want := AuthorizationApprove
				if action == "secrets" || action == "permissions" || action == "integrate_task" || (name == "manage_projects" && action == "delete") || level == "off" {
					want = AuthorizationDeny
				} else if level == "ask" && name == "manage_projects" && action != "list" && action != "get" && action != "list_tasks" && action != "get_task" && action != "archive_task" {
					want = AuthorizationPending
				}
				args := `{"machine":"build-box","action":"` + action + `","id":"record","project_id":"p","task_id":"t","agent":"coder","title":"Fix it"}`
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
	stored, err := db.GetAccountPermissions("acct")
	if err != nil || stored.Linked["build-box"].Work != "auto" {
		t.Fatalf("allowed must be stored as auto: %+v %v", stored, err)
	}
}

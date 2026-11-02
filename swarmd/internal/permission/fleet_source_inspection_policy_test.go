package permission

import (
	"testing"

	store "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// Purpose: manage_projects list_sources, inspect_source and inspect_files
// with a machine are reads: they pass the linked-machine gate under ask or
// auto without a pending approval (they never send or start work there), a
// machine whose linked work is off is denied, and an action still not served
// on a linked machine stays refused. Threat: the reported defect (the source
// actions denied before dispatch as "not available on a linked machine"),
// a read held for an approval as if it were work, or the new entries letting
// other actions reach a linked machine. Boundary: IsManageProjectsReadOnlyAction
// and Service.AuthorizeToolCall's fleet gate (tool.FleetWorkActionAllowed over
// tool.FleetSourceInspectionActions). Narrowest layer: the permission service
// over a temporary store.
func TestLinkedSourceInspectionIsAReadOnEveryMachine(t *testing.T) {
	for _, rule := range []string{"off", "ask", "auto"} {
		t.Run(rule, func(t *testing.T) {
			db, err := store.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			p := store.DefaultAccountPermissions()
			p.Linked["box"] = store.LinkedPermissionRule{Work: rule}
			if _, err := db.SetAccountPermissions("a", 0, p); err != nil {
				t.Fatal(err)
			}
			svc := NewService(store.NewPermissionStore(db), nil, nil)
			authorize := func(args string) AuthorizationResult {
				t.Helper()
				got, err := svc.AuthorizeToolCall(AuthorizationInput{SessionID: "s", RunID: "r", AccountScopeID: "a", CallID: "c-" + args, ToolName: "manage_projects", ToolArguments: args, Mode: "auto"})
				if err != nil {
					t.Fatal(err)
				}
				return got
			}
			want := map[string]AuthorizationDecision{"off": AuthorizationDeny, "ask": AuthorizationApprove, "auto": AuthorizationApprove}[rule]
			for _, action := range tool.FleetSourceInspectionActions {
				args := `{"action":"` + action + `","machine":"box","project_id":"p"}`
				if !IsManageProjectsReadOnlyAction(args) {
					t.Fatalf("%s: not a read", action)
				}
				if got := authorize(args); got.Decision != want || got.Record != nil {
					t.Fatalf("%s under %s: %+v", action, rule, got)
				}
			}
			if refused := authorize(`{"action":"integrate_task","machine":"box"}`); refused.Decision != AuthorizationDeny {
				t.Fatalf("%s: integrate_task reached a linked machine: %+v", rule, refused)
			}
		})
	}
}

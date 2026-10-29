package permission

import (
	"testing"

	store "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: manage_projects action=agents_at_work is a read: with no machine
// it needs no approval (manageProjectsReadOnlyActions), and with a machine it
// passes the linked-machine gate under ask or auto without a pending
// approval, while a machine whose linked work is off is still denied and an
// unlisted action with a machine is still refused. Threat: the read blocked
// on an approval, or the new allowlist entry letting other actions reach a
// linked machine. Boundary: IsManageProjectsReadOnlyAction and
// Service.AuthorizeToolCall's fleet gate (tool.FleetAgentsReadAllowed).
func TestAgentsAtWorkIsAReadOnEveryMachine(t *testing.T) {
	if !IsManageProjectsReadOnlyAction(`{"action":"agents_at_work"}`) || !IsManageProjectsReadOnlyAction(`{"action":"agents_at_work","machine":"box"}`) {
		t.Fatal("agents_at_work is not a read")
	}
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
			got := authorize(`{"action":"agents_at_work","machine":"box"}`)
			want := map[string]AuthorizationDecision{"off": AuthorizationDeny, "ask": AuthorizationApprove, "auto": AuthorizationApprove}[rule]
			if got.Decision != want || got.Record != nil {
				t.Fatalf("%s: %+v", rule, got)
			}
			if refused := authorize(`{"action":"integrate_task","machine":"box"}`); refused.Decision != AuthorizationDeny {
				t.Fatalf("%s: integrate_task reached a linked machine: %+v", rule, refused)
			}
		})
	}
}

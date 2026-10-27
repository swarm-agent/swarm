package permission

import (
	store "swarm/packages/swarmd/internal/store/pebble"
	"testing"
)

// Purpose: linked work must obey the account rule rather than generic policy
// or session metadata. The service/store layer proves pending versus execution
// without requiring a remote machine, and off must create no executable record.
func TestAccountLinkedWork(t *testing.T) {
	for _, rule := range []string{"off", "ask", "allowed"} {
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
			got, err := svc.AuthorizeToolCall(AuthorizationInput{SessionID: "s", RunID: "r", AccountScopeID: "a", CallID: "c", ToolName: "manage_projects", ToolArguments: `{"action":"list","machine":"box"}`, Mode: "auto"})
			if err != nil {
				t.Fatal(err)
			}
			want := map[string]AuthorizationDecision{"off": AuthorizationDeny, "ask": AuthorizationPending, "allowed": AuthorizationApprove}[rule]
			if got.Decision != want {
				t.Fatalf("%s: %+v", rule, got)
			}
			if rule == "ask" && got.Record == nil {
				t.Fatal("missing pending permission")
			}
			if rule == "off" && got.Record != nil {
				t.Fatal("off created permission")
			}
		})
	}
}

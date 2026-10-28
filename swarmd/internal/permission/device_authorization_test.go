package permission

import (
	store "swarm/packages/swarmd/internal/store/pebble"
	"testing"
	"time"
)

// Purpose: AuthorizeToolCall must apply account device rules before generic
// policy, bind execution to the message sender, and leave no pending work on
// refusal. Device calls never wait on a person: allowed records a ready call,
// off (and a stored retired ask, which reads off) denies without a record.
// Temporary durable fixtures exercise the production gate directly.
func TestDeviceAccountAuthorization(t *testing.T) {
	for _, tool := range []string{"machine_read", "machine_write", "machine_run", "manage_browser"} {
		for _, rule := range []string{"off", "stored-ask", "allowed"} {
			t.Run(tool+"/"+rule, func(t *testing.T) {
				db, err := store.Open(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				put := func(key string, v any) {
					t.Helper()
					if err := db.PutJSON(key, v); err != nil {
						t.Fatal(err)
					}
				}
				put(store.KeySession("s"), store.SessionSnapshot{ID: "s", AccountScopeID: "a"})
				put(store.KeyV3SessionRunIntent("s", "r"), store.V3SessionRunIntent{SessionID: "s", RunID: "r", SendingClientSessionID: "d"})
				now := time.Now().UnixMilli()
				client := store.ClientSessionRecord{SessionID: "d", AccountScopeID: "a", Device: "computer", ExpiresAt: now + 60000, LastSeenAt: now}
				put(store.KeyIdentityClientSession("d"), client)
				p := store.DefaultAccountPermissions()
				if rule == "stored-ask" {
					// A record written before ask was removed.
					put("account_permissions/a", map[string]any{"tasks": "auto", "devices": map[string]any{"d": map[string]any{"files": "ask", "browser": "ask"}}})
				} else {
					p.Devices["d"] = store.DevicePermissionRule{Files: rule, Browser: rule}
					if _, err := db.SetAccountPermissions("a", 0, p); err != nil {
						t.Fatal(err)
					}
				}
				svc := NewService(store.NewPermissionStore(db), nil, nil)
				in := AuthorizationInput{SessionID: "s", RunID: "r", AccountScopeID: "a", CallID: "c", ToolName: tool, ToolArguments: `{}`, Mode: "auto"}
				got, err := svc.AuthorizeToolCall(in)
				if err != nil {
					t.Fatal(err)
				}
				if rule != "allowed" {
					if got.Decision != AuthorizationDeny || got.Record != nil {
						t.Fatalf("off: %+v", got)
					}
				} else {
					if got.Decision != AuthorizationPending || got.Record == nil || got.Record.TargetDevice != "d" || got.Record.Execution != "ready" {
						t.Fatalf("gate: %+v", got)
					}
				}
				used, err := db.GetAccountPermissions("a")
				if err != nil {
					t.Fatal(err)
				}
				if (used.Device("d").LastUsed > 0) != (rule == "allowed") {
					t.Fatalf("unexpected usage: %+v", used)
				}
				in.CallID = "disconnected"
				in.ToolArguments = `{"device":"missing"}`
				got, err = svc.AuthorizeToolCall(in)
				if err != nil || got.Decision != AuthorizationDeny || got.Record != nil {
					t.Fatalf("disconnected: %+v %v", got, err)
				}
			})
		}
	}
}

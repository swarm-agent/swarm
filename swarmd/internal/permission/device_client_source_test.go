package permission

import (
	"strings"
	"testing"
	"time"

	store "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: device calls must find connected devices where the daemon keeps
// them. The daemon opens the identity session store (client sessions, i.e.
// devices) on the secrets database and the PermissionStore on the main
// database (runtime/daemon.go). Threat: reading client sessions from the main
// database, which has none, so every device call was refused with "no device
// is connected" while /v3/permissions showed the device connected. Boundary:
// Service.SetClientSessionLister and authorizeDeviceCall. Narrowest layer: the
// production gate (AuthorizeToolCall) over two temporary stores wired exactly
// as the daemon wires them. Negative cases: a device recorded only in the main
// database is not a connected device, and a service with no client session
// source fails with an error and records nothing.
func TestDeviceCallFindsDevicesInTheSecretsStore(t *testing.T) {
	main, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer main.Close()
	secrets, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer secrets.Close()
	put := func(db *store.Store, key string, v any) {
		t.Helper()
		if err := db.PutJSON(key, v); err != nil {
			t.Fatal(err)
		}
	}
	put(main, store.KeySession("s"), store.SessionSnapshot{ID: "s", AccountScopeID: "a"})
	put(main, store.KeyV3SessionRunIntent("s", "r"), store.V3SessionRunIntent{SessionID: "s", RunID: "r", SendingClientSessionID: "pc-client"})
	now := time.Now().UnixMilli()
	put(secrets, store.KeyIdentityClientSession("pc-client"), store.ClientSessionRecord{SessionID: "pc-client", AccountScopeID: "a", Device: "pc", ExpiresAt: now + 60000, LastSeenAt: now})
	// A record only in the main database: not where devices live.
	put(main, store.KeyIdentityClientSession("main-only"), store.ClientSessionRecord{SessionID: "main-only", AccountScopeID: "a", Device: "laptop", ExpiresAt: now + 60000, LastSeenAt: now})
	p := store.DefaultAccountPermissions()
	p.Devices["pc-client"] = store.DevicePermissionRule{Files: store.RuleAllowed, Browser: store.RuleAllowed}
	p.Devices["main-only"] = store.DevicePermissionRule{Files: store.RuleAllowed, Browser: store.RuleAllowed}
	if _, err := main.SetAccountPermissions("a", 0, p); err != nil {
		t.Fatal(err)
	}

	unwired := NewService(store.NewPermissionStore(main), nil, nil)
	in := AuthorizationInput{SessionID: "s", RunID: "r", AccountScopeID: "a", CallID: "unwired", ToolName: "machine_run", ToolArguments: `{"command":"true"}`, Mode: "auto"}
	if got, err := unwired.AuthorizeToolCall(in); err == nil || got.Record != nil {
		t.Fatalf("no client session source: %+v %v", got, err)
	}

	svc := NewService(store.NewPermissionStore(main), nil, nil)
	svc.SetClientSessionLister(store.NewIdentitySessionStore(secrets))
	in.CallID = "routed"
	got, err := svc.AuthorizeToolCall(in)
	if err != nil {
		t.Fatal(err)
	}
	if got.Decision != AuthorizationPending || got.Record == nil || got.Record.TargetDevice != "pc-client" || got.Record.Execution != "ready" {
		t.Fatalf("device call not routed to the sending device: %+v", got)
	}

	in.CallID = "main-only"
	in.ToolArguments = `{"command":"true","device":"laptop"}`
	got, err = svc.AuthorizeToolCall(in)
	if err != nil || got.Decision != AuthorizationDeny || got.Record != nil || !strings.Contains(got.Reason, "not connected") {
		t.Fatalf("main-database device treated as connected: %+v %v", got, err)
	}
	records, err := svc.ListPermissions("s", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].CallID != "routed" {
		t.Fatalf("records = %+v, want only the routed call", records)
	}
}

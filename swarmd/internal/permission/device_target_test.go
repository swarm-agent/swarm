package permission

import (
	"strings"
	"testing"
	"time"

	store "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: a device tool call must reach a connected device of the session's
// account even when the run has no sending device (a delegated or started
// run, or an API message), and a request for access must be answered from
// that device's account rule instead of a blanket refusal.
// Regression: runs without a sender were refused with `target device "" is
// not connected`, and request_browser_access was refused even when the
// device's browser rule was allowed.
// Owners: Service.AuthorizeToolCall, resolveDeviceTarget,
// authorizeDeviceCall and deviceAccessRequestAnswer over the durable
// PermissionStore. The permission service with a temporary Pebble store is the
// narrowest layer that holds the run intent, client sessions and account rules.
func TestDeviceTargetResolution(t *testing.T) {
	type device struct{ id, name, rule string }
	setup := func(t *testing.T, sender string, devices ...device) *Service {
		t.Helper()
		db, err := store.Open(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		put := func(key string, v any) {
			t.Helper()
			if err := db.PutJSON(key, v); err != nil {
				t.Fatal(err)
			}
		}
		put(store.KeySession("s"), store.SessionSnapshot{ID: "s", AccountScopeID: "a"})
		put(store.KeyV3SessionRunIntent("s", "r"), store.V3SessionRunIntent{SessionID: "s", RunID: "r", SendingClientSessionID: sender})
		now := time.Now().UnixMilli()
		p := store.DefaultAccountPermissions()
		for _, d := range devices {
			put(store.KeyIdentityClientSession(d.id), store.ClientSessionRecord{SessionID: d.id, AccountScopeID: "a", Device: d.name, ExpiresAt: now + 60000, LastSeenAt: now})
			p.Devices[d.id] = store.DevicePermissionRule{Files: d.rule, Browser: d.rule}
		}
		// Another account's connected device is never a fallback target.
		put(store.KeyIdentityClientSession("other"), store.ClientSessionRecord{SessionID: "other", AccountScopeID: "b", Device: "theirs", ExpiresAt: now + 60000, LastSeenAt: now})
		if _, err := db.SetAccountPermissions("a", 0, p); err != nil {
			t.Fatal(err)
		}
		return NewService(store.NewPermissionStore(db), nil, nil)
	}
	call := func(t *testing.T, svc *Service, callID, tool, args string) AuthorizationResult {
		t.Helper()
		got, err := svc.AuthorizeToolCall(AuthorizationInput{SessionID: "s", RunID: "r", AccountScopeID: "a", CallID: callID, ToolName: tool, ToolArguments: args, Mode: "auto"})
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	noPending := func(t *testing.T, svc *Service) {
		t.Helper()
		pending, err := svc.ListPending("s", 10)
		if err != nil || len(pending) != 0 {
			t.Fatalf("refusal left pending work: %+v %v", pending, err)
		}
	}

	t.Run("no sender goes to the only connected device", func(t *testing.T) {
		svc := setup(t, "", device{"d", "computer", "allowed"})
		got := call(t, svc, "c", "manage_browser", `{"action":"tabs"}`)
		if got.Decision != AuthorizationPending || got.Record == nil || got.Record.TargetDevice != "d" || got.Record.Execution != "ready" {
			t.Fatalf("manage_browser without a sender: %+v", got)
		}
	})
	t.Run("no sender and no connected device is refused", func(t *testing.T) {
		svc := setup(t, "")
		got := call(t, svc, "c", "manage_browser", `{"action":"tabs"}`)
		if got.Decision != AuthorizationDeny || got.Record != nil || !strings.Contains(got.Reason, "no device is connected") {
			t.Fatalf("no device: %+v", got)
		}
		noPending(t, svc)
	})
	t.Run("no sender with several devices must name one", func(t *testing.T) {
		svc := setup(t, "", device{"d1", "laptop", "allowed"}, device{"d2", "desktop", "allowed"})
		got := call(t, svc, "c", "machine_read", `{"path":"~/x"}`)
		if got.Decision != AuthorizationDeny || got.Record != nil || !strings.Contains(got.Reason, `"desktop", "laptop"`) {
			t.Fatalf("several devices: %+v", got)
		}
		noPending(t, svc)
		got = call(t, svc, "c2", "machine_read", `{"path":"~/x","device":"laptop"}`)
		if got.Decision != AuthorizationPending || got.Record == nil || got.Record.TargetDevice != "d1" {
			t.Fatalf("named device: %+v", got)
		}
	})
	t.Run("a disconnected sender is not rerouted", func(t *testing.T) {
		svc := setup(t, "gone", device{"d", "computer", "allowed"})
		got := call(t, svc, "c", "manage_browser", `{"action":"tabs"}`)
		if got.Decision != AuthorizationDeny || got.Record != nil || !strings.Contains(got.Reason, `"gone" is not connected`) {
			t.Fatalf("disconnected sender: %+v", got)
		}
		noPending(t, svc)
	})
	for _, c := range []struct{ tool, rule, want, tools string }{
		{"request_browser_access", "allowed", `browser rule for device "computer" is allowed`, "manage_browser"},
		{"request_browser_access", "ask", `browser rule for device "computer" is ask`, "manage_browser"},
		{"request_browser_access", "off", `browser rule for device "computer" is off`, "Permissions page"},
		{"request_machine_access", "allowed", `files rule for device "computer" is allowed`, "machine_run"},
		{"request_machine_access", "off", `files rule for device "computer" is off`, "Permissions page"},
	} {
		t.Run(c.tool+"/"+c.rule, func(t *testing.T) {
			svc := setup(t, "", device{"d", "computer", c.rule})
			got := call(t, svc, "c", c.tool, `{"reason":"r","sites":["example.com"]}`)
			if got.Decision != AuthorizationDeny || got.Record != nil || !strings.Contains(got.Reason, c.want) || !strings.Contains(got.Reason, c.tools) {
				t.Fatalf("%s with rule %s: %+v", c.tool, c.rule, got)
			}
			if strings.Contains(got.Reason, "cannot grant") != (c.rule == "off") {
				t.Fatalf("answer misreports the rule: %q", got.Reason)
			}
			noPending(t, svc)
		})
	}
}

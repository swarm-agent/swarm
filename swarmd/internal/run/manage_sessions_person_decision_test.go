package run

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/permission"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// Purpose: in a conversation, manage_sessions resolve_permission and
// set_model follow the account's permission bypass switch exactly. With the
// switch off they always wait for the person: Service.gateToolCalls gives
// them their own approval identities (manageSessionsPersonDecisionIdentity),
// which the policy never allows by default, and drops a bypass suffix in the
// mode text (personDecisionMode), so only the real switch can skip the
// prompt. With the switch on they are approved like every other tool call,
// as the MCP tool they replace (swarm_resolve_permission) is. Threat: an
// agent, or injected session content steering it, approving a child's Bash
// command or commit with allow_once and no prompt while the person has bypass
// off (including through mode text that says bypass while the switch is off,
// or a decoy key that differs from "action" only in case: the gate reads the
// exact key and treats any such call as a person's decision). Negative case:
// with the switch off, while the prompt waits and after the person denies
// it, the child's request is still pending and the call is not approved.
// Control: manage_sessions get is not gated. Boundary: gateToolCalls over a
// real permission.Service (its SetBypassPermissions switch) and a temp Pebble
// store; the layer where every native tool call is authorized before it runs.
func TestManageSessionsPersonDecisionsWaitForThePerson(t *testing.T) {
	db, err := pebblestore.Open(filepath.Join(t.TempDir(), "permissions"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	perms := permission.NewService(pebblestore.NewPermissionStore(db), nil, nil)
	s := &Service{permissions: perms}

	command, _ := json.Marshal(map[string]any{"command": "git push"})
	child, err := perms.CreatePending(permission.CreateInput{SessionID: "child", RunID: "run-child", CallID: "child-call", ToolName: "bash", ToolArguments: string(command), Requirement: "tool", Mode: "auto"})
	if err != nil {
		t.Fatal(err)
	}
	childPending := func(stage string) {
		t.Helper()
		record, ok, err := perms.GetPermission("child", child.ID)
		if err != nil || !ok || record.Status != pebblestore.PermissionStatusPending {
			t.Fatalf("%s: the child's request changed: %+v %v", stage, record, err)
		}
	}
	resolve := map[string]any{"action": tool.ManageSessionsResolvePermissionAction, "session_id": "child", "permission_id": child.ID, "decision": "allow_once"}
	setModel := map[string]any{"action": tool.ManageSessionsSetModelAction, "session_id": "child", "provider": "p", "model": "m"}
	turn := 0
	gate := func(mode string, args map[string]any) (chan []bool, context.CancelFunc) {
		turn++
		raw, _ := json.Marshal(args)
		call := tool.Call{CallID: fmt.Sprintf("person-%d", turn), Name: "manage-sessions", Arguments: string(raw)}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		done := make(chan []bool, 1)
		go func(step int) {
			_, _, _, mask, _, err := s.gateToolCalls(ctx, "orch", "run-orch", step, mode, []tool.Call{call}, nil, nil)
			if err != nil {
				t.Errorf("gate: %v", err)
			}
			done <- mask
		}(turn)
		return done, cancel
	}

	// Reads stay prompt-free.
	perms.SetBypassPermissions(false)
	got, cancel := gate("auto", map[string]any{"action": "get", "session_id": "child"})
	if mask := <-got; len(mask) != 1 || !mask[0] {
		t.Fatalf("manage_sessions get was gated: %v", mask)
	}
	cancel()

	// Switch off: every call waits for the person, also when the mode text
	// carries a bypass suffix the switch does not back.
	for _, tc := range []struct {
		identity, mode string
		args           map[string]any
	}{
		{sessionPermissionAnswerIdentity, "auto", resolve},
		{sessionModelChangeIdentity, "auto", setModel},
		{sessionPermissionAnswerIdentity, "auto+bypass_permissions", resolve},
		{sessionModelChangeIdentity, "yolo+bypass_permissions", setModel},
		// A decoy key that differs from "action" only in case: a struct
		// decoder would read the decoy (get), the handler the exact key.
		{sessionPermissionAnswerIdentity, "auto", map[string]any{"action": tool.ManageSessionsResolvePermissionAction, "session_id": "child", "permission_id": child.ID, "decision": "allow_once", "ACTION": "get"}},
		{sessionPermissionAnswerIdentity, "auto", map[string]any{"action": tool.ManageSessionsSetModelAction, "session_id": "child", "provider": "p", "model": "m", "Action": "get"}},
		{sessionPermissionAnswerIdentity, "auto", map[string]any{"action": "get", "session_id": "child", "permission_id": child.ID, "decision": "allow_once", "aCtIoN": tool.ManageSessionsResolvePermissionAction}},
	} {
		done, cancel := gate(tc.mode, tc.args)
		var prompt pebblestore.PermissionRecord
		for deadline := time.Now().Add(10 * time.Second); ; {
			pending, err := perms.ListPending("orch", 10)
			if err != nil {
				t.Fatal(err)
			}
			if len(pending) == 1 {
				prompt = pending[0]
				break
			}
			select {
			case mask := <-done:
				t.Fatalf("%s (%s) ran without a prompt with bypass off: approved=%v", tc.identity, tc.mode, mask)
			default:
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s (%s): no prompt for the person", tc.identity, tc.mode)
			}
			time.Sleep(10 * time.Millisecond)
		}
		if prompt.ToolName != tc.identity {
			t.Fatalf("prompt identity %q, want %q", prompt.ToolName, tc.identity)
		}
		childPending("while the prompt waits")
		if _, err := perms.Resolve("orch", prompt.ID, "deny", "not now"); err != nil {
			t.Fatal(err)
		}
		mask := <-done
		cancel()
		if len(mask) != 1 || mask[0] {
			t.Fatalf("%s (%s) approved after the person denied it: %v", tc.identity, tc.mode, mask)
		}
		childPending("after the person denied")
	}

	// Switch on: bypass means bypass; both calls are approved with no prompt,
	// like every other tool call.
	perms.SetBypassPermissions(true)
	if !perms.BypassPermissions() {
		t.Fatal("the bypass switch did not turn on")
	}
	for _, args := range []map[string]any{resolve, setModel} {
		done, cancel := gate("auto", args)
		mask := <-done
		cancel()
		if len(mask) != 1 || !mask[0] {
			t.Fatalf("%v was not approved with bypass on: %v", args["action"], mask)
		}
		if pending, _ := perms.ListPending("orch", 10); len(pending) != 0 {
			t.Fatalf("%v prompted with bypass on: %+v", args["action"], pending)
		}
	}
}

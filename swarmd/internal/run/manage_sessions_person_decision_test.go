package run

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/permission"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// Purpose: an Orchestrator (or any agent) run cannot answer another
// session's pending permission request, or switch a session's model,
// without the person: Service.gateToolCalls gives manage_sessions
// resolve_permission and set_model their own approval identities
// (manageSessionsPersonDecisionIdentity), which the policy never allows by
// default, and strips the generic bypass suffix (personDecisionMode), so the
// call waits on a prompt in the calling conversation. Threat: an agent, or
// injected session content steering it, approving a child's Bash command or
// commit with allow_once and no prompt (the gap in stage 4's first cut).
// Negative case: while the prompt waits, and after the person denies it, the
// child's request is still pending and the call is not approved. Control:
// manage_sessions get is not gated. Boundary: gateToolCalls over a real
// permission.Service and temp Pebble store; the layer where every native
// tool call is authorized before it runs.
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

	// Reads stay prompt-free.
	get, _ := json.Marshal(map[string]any{"action": "get", "session_id": "child"})
	_, _, _, mask, _, err := s.gateToolCalls(context.Background(), "orch", "run-orch", 1, "auto+bypass_permissions", []tool.Call{{CallID: "get-1", Name: "manage_sessions", Arguments: string(get)}}, nil, nil)
	if err != nil || !mask[0] {
		t.Fatalf("manage_sessions get was gated: %v %v", mask, err)
	}

	for i, tc := range []struct {
		identity string
		args     map[string]any
	}{
		{sessionPermissionAnswerIdentity, map[string]any{"action": tool.ManageSessionsResolvePermissionAction, "session_id": "child", "permission_id": child.ID, "decision": "allow_once"}},
		{sessionModelChangeIdentity, map[string]any{"action": tool.ManageSessionsSetModelAction, "session_id": "child", "provider": "p", "model": "m"}},
	} {
		raw, _ := json.Marshal(tc.args)
		call := tool.Call{CallID: "person-" + tc.identity, Name: "manage-sessions", Arguments: string(raw)}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		type gated struct {
			mask []bool
			err  error
		}
		done := make(chan gated, 1)
		go func() {
			_, _, _, mask, _, err := s.gateToolCalls(ctx, "orch", "run-orch", 2+i, "auto+bypass_permissions", []tool.Call{call}, nil, nil)
			done <- gated{mask, err}
		}()
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
			case result := <-done:
				t.Fatalf("%s ran without a prompt: approved=%v err=%v", tc.identity, result.mask, result.err)
			default:
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: no prompt for the person", tc.identity)
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
		result := <-done
		cancel()
		if result.err != nil || len(result.mask) != 1 || result.mask[0] {
			t.Fatalf("%s approved after the person denied it: %v %v", tc.identity, result.mask, result.err)
		}
		childPending("after the person denied")
	}
}

package run

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/permission"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// Purpose:
//   - Requirement: in a conversation, every manage_setup change waits for
//     the person's decision every time: with permission bypass on, an allow
//     rule for manage_setup, and after the person chose "always allow", the
//     gate still records a pending request and runs nothing until the person
//     answers; a denial runs nothing; an approval runs the exact call the
//     person saw (its own arguments, the run's proof of the decision). A
//     manage_setup read is not held. An explicit deny answers without asking.
//   - Threat/regression: an allow rule, bypass or a stored "always" letting
//     the Orchestrator change models, limits, permissions or keys without the
//     person, or edited arguments running in place of the approved call.
//   - Boundary/authority: Service.gateToolCalls with a real permission
//     service and store (AuthorizeToolCall, CreatePending,
//     WaitForResolution, Resolve).
//   - Narrowest layer: the run's gate is where a decision is required and
//     recorded; nothing narrower sees rules, bypass and the pending record.
func TestManageSetupChangeAlwaysWaitsForThePerson(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "setup-gate.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatal(err)
	}
	permissions := permission.NewService(pebblestore.NewPermissionStore(store), events, nil)
	if _, err := permissions.UpsertRule(permission.PolicyRule{Kind: permission.PolicyRuleKindTool, Decision: permission.PolicyDecisionAllow, Tool: "manage_setup"}); err != nil {
		t.Fatal(err)
	}
	permissions.SetBypassPermissions(true)
	svc := NewService(nil, nil, nil, tool.NewRuntime(1), permissions, nil, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	const session = "orch-setup"
	type gated struct {
		approved bool
		feedback []PermissionFeedback
		result   tool.Result
	}
	gate := func(callID, arguments string) <-chan gated {
		out := make(chan gated, 1)
		go func() {
			results, _, _, mask, feedback, err := svc.gateToolCalls(ctx, session, "run-1", 1, "auto+bypass_permissions", []tool.Call{{CallID: callID, Name: "manage_setup", Arguments: arguments}}, nil, nil)
			if err != nil {
				t.Errorf("gate %s: %v", callID, err)
			}
			out <- gated{approved: len(mask) == 1 && mask[0], feedback: feedback, result: results[0]}
		}()
		return out
	}
	pending := func(callID string) pebblestore.PermissionRecord {
		t.Helper()
		for ctx.Err() == nil {
			records, err := permissions.ListPending(session, 10)
			if err != nil {
				t.Fatal(err)
			}
			for _, record := range records {
				if record.CallID == callID {
					return record
				}
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("no pending request for %s", callID)
		return pebblestore.PermissionRecord{}
	}

	// A read is not held.
	if read := <-gate("read", `{"action":"models"}`); !read.approved {
		t.Fatalf("a manage_setup read was held: %+v", read)
	}
	if records, _ := permissions.ListPending(session, 10); len(records) != 0 {
		t.Fatalf("a read made a request: %+v", records)
	}

	change := `{"action":"set_agent_model","role":"all","provider":"p","model":"m"}`
	// Denied: nothing runs.
	denied := gate("deny", change)
	record := pending("deny")
	if record.ToolName != "manage_setup" {
		t.Fatalf("pending request %+v", record)
	}
	select {
	case early := <-denied:
		t.Fatalf("the change ran before the person decided: %+v", early)
	case <-time.After(100 * time.Millisecond):
	}
	if _, err := permissions.Resolve(session, record.ID, "deny", "not now"); err != nil {
		t.Fatal(err)
	}
	if got := <-denied; got.approved || !strings.Contains(got.result.Error, "denied") {
		t.Fatalf("a denied change: %+v", got)
	}

	// Approved, even with edited arguments and "always": the call runs as
	// the person saw it.
	approved := gate("approve", change)
	record = pending("approve")
	if _, err := permissions.ResolveWithArguments(session, record.ID, "approve_always", "", `{"action":"set_agent_model","role":"all","provider":"other","model":"other"}`); err != nil {
		t.Fatal(err)
	}
	got := <-approved
	if !got.approved || len(got.feedback) != 1 || got.feedback[0].ApprovedArguments != change {
		t.Fatalf("an approved change: %+v", got)
	}

	// After "always allow", the next change still waits for the person.
	again := gate("again", change)
	record = pending("again")
	if _, err := permissions.Resolve(session, record.ID, "deny", ""); err != nil {
		t.Fatal(err)
	}
	if got := <-again; got.approved {
		t.Fatalf("an always-allowed change ran without the person: %+v", got)
	}

	// An explicit deny answers without asking.
	if _, err := permissions.UpsertRule(permission.PolicyRule{Kind: permission.PolicyRuleKindTool, Decision: permission.PolicyDecisionDeny, Tool: "manage_setup"}); err != nil {
		t.Fatal(err)
	}
	if got := <-gate("denied-by-rule", change); got.approved {
		t.Fatalf("a denied-by-rule change ran: %+v", got)
	}
	if records, _ := permissions.ListPending(session, 10); len(records) != 0 {
		t.Fatalf("a denied-by-rule change asked: %+v", records)
	}
}

// Purpose:
//   - Requirement: the run runs a conversation's manage_setup change only
//     with the person's decision for that exact call (its approved
//     arguments from the gate) and only for the Orchestrator; without it, or
//     with arguments other than the approved ones, the change is refused and
//     nothing runs.
//   - Threat/regression: a control-plane path (a provider-managed call, a
//     replay) running a setup change that never passed the gate, or another
//     agent or a worker reaching machine setup.
//   - Boundary/authority: Service.executeControlPlaneTool ->
//     executeManageSetupTool.
//   - Narrowest layer: the run's dispatch, before any service runs.
func TestManageSetupRunRefusesUndecidedChanges(t *testing.T) {
	svc := NewService(nil, nil, nil, tool.NewRuntime(1), nil, nil, nil, nil)
	orchestrator := pebblestore.AgentProfile{Name: agentruntime.SwarmOrchestratorAgentID}
	change := tool.Call{CallID: "c1", Name: "manage_setup", Arguments: `{"action":"set_limits","expected_revision":1,"limits":{}}`}
	for _, approved := range []string{"", `{"action":"set_limits","expected_revision":1,"limits":{"job_tokens":0}}`} {
		handled, _, err := svc.executeControlPlaneTool(context.Background(), "orch", "auto", orchestrator, 1, change, approved, nil)
		if !handled || err == nil || !strings.Contains(err.Error(), "needs the person's decision") {
			t.Fatalf("undecided change (approved %q): %v %v", approved, handled, err)
		}
	}
	for _, agent := range []string{"system-coder", "worker_w1", "custom-agent"} {
		handled, _, err := svc.executeControlPlaneTool(context.Background(), "orch", "auto", pebblestore.AgentProfile{Name: agent}, 1, tool.Call{CallID: "c2", Name: "manage_setup", Arguments: `{"action":"models"}`}, "", nil)
		if !handled || err == nil || !strings.Contains(err.Error(), "Orchestrator's") {
			t.Fatalf("agent %s read setup: %v %v", agent, handled, err)
		}
	}
}

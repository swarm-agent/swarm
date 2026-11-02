package api

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/jobautomation"
	"swarm/packages/swarmd/internal/permission"
	sessionruntime "swarm/packages/swarmd/internal/session"
	store "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement: a session agent's manage_jobs action without the owner's
// exact grant is refused on the machine the conversation runs on, before
// anything is forwarded to a linked machine, and the refusal says so: the
// exact rule (manage_jobs.<agent>.<action>) and agent, that it was checked
// here before linked machine <name>, that nothing ran or changed, that only
// the owner can add it (this machine's Permissions page or the owner
// permission-rule API), and that start-task approval, linked work rules
// and permission bypass do not grant it. A deny rule (exact or manage_jobs)
// is named as a deny rule and still wins over an exact allow. The grant
// check itself is unchanged: the agent identity comes from the session,
// a spoofed account is refused, and no delivery changes.
// Threat: a refusal that reads like a retryable glitch, that sends the
// person to the linked machine (where no grant helps), or that implies an
// account bypass or start-task setting satisfies it; or a rewording that
// loosens the check.
// Owners: Server.ManageJobs, nativeJobActionGrant, JobGrantRequiredError.
// Layer: the job service every surface (native tool, linked route) runs,
// over real Pebble permission and delivery stores.
func TestManageJobsGrantRefusalNamesRuleOriginAndOwnerStep(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "jobs.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	sessions := store.NewSessionStore(db)
	p := testPrincipal()
	current := store.SessionSnapshot{ID: "orchestrator", AccountScopeID: p.AccountScopeID, UserID: p.UserID}
	if err := sessions.CreateSession(current); err != nil {
		t.Fatal(err)
	}
	perms := permission.NewService(store.NewPermissionStore(db), nil, nil)
	ds := jobautomation.NewStore(db)
	s := &Server{sessions: sessionruntime.NewService(sessions, nil), perm: perms}
	s.SetJobAutomations(jobautomation.NewRunner(jobautomation.Options{Store: ds}))
	a, _, err := ds.Put(p.AccountScopeID, jobautomation.Automation{Name: "gate", Scope: jobautomation.Scope{Folder: t.TempDir()}, Enabled: true,
		Steps: []jobautomation.Step{{ID: "review", Type: jobautomation.StepAgent, Worker: "reviewer", Brief: "Review"}}})
	if err != nil {
		t.Fatal(err)
	}
	d, _, err := ds.CreateDelivery(a, jobautomation.Result{JobID: "worker/other/run", Kind: jobautomation.KindWorker, Status: jobautomation.StatusSucceeded})
	if err != nil {
		t.Fatal(err)
	}
	d.State, d.Steps[0].State, d.Steps[0].Reason = jobautomation.StateFailed, jobautomation.StateFailed, "review failed"
	if err := ds.SaveDelivery(d); err != nil {
		t.Fatal(err)
	}
	args := func(machine string) map[string]any {
		out := map[string]any{"action": "rerun_failed_steps", "job_id": d.JobID}
		if machine != "" {
			out["machine"] = machine
		}
		return out
	}
	refusal := func(machine string) *JobGrantRequiredError {
		t.Helper()
		out, err := s.ManageJobs(context.Background(), p, current.ID, "orchestrator-agent", args(machine))
		var grant *JobGrantRequiredError
		if !errors.As(err, &grant) || out != nil {
			t.Fatalf("machine %q: want a grant refusal, got %#v %v", machine, out, err)
		}
		return grant
	}

	// Linked: refused here, before forwarding (the machine is not even
	// linked, so a forwarded call would have failed differently).
	grant := refusal("linked-a")
	msg := grant.Error()
	for _, want := range []string{
		"owner must allow manage_jobs.orchestrator-agent.rerun_failed_steps in account permission rules",
		"refused on this machine (where the conversation runs) before anything was sent to linked machine linked-a",
		"no permission rule allowing it for agent orchestrator-agent",
		"nothing ran or changed",
		"tool manage_jobs.orchestrator-agent.rerun_failed_steps on this machine's Permissions page",
		"Start-task approval, linked work rules, permission bypass",
		"no agent or tool can grant it",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("refusal %q lacks %q", msg, want)
		}
	}
	if grant.Denied || grant.Agent != "orchestrator-agent" || grant.Action != "rerun_failed_steps" || grant.Machine != "linked-a" {
		t.Fatalf("refusal fields = %+v", grant)
	}
	// Local: the same refusal without a linked machine.
	if msg := refusal("").Error(); strings.Contains(msg, "linked machine") || !strings.Contains(msg, "refused on this machine") {
		t.Fatalf("local refusal = %q", msg)
	}

	// A grant for another agent, or a generic manage_jobs allow, does not
	// satisfy it.
	for _, tool := range []string{"manage_jobs.other-agent.rerun_failed_steps", "manage_jobs"} {
		if _, err := perms.UpsertRuleForAccount(p.AccountScopeID, permission.PolicyRule{Kind: permission.PolicyRuleKindTool, Decision: permission.PolicyDecisionAllow, Tool: tool}); err != nil {
			t.Fatal(err)
		}
	}
	refusal("")

	// A deny wins over the exact allow, and is named as a deny.
	if _, err := perms.UpsertRuleForAccount(p.AccountScopeID, permission.PolicyRule{Kind: permission.PolicyRuleKindTool, Decision: permission.PolicyDecisionDeny, Tool: "manage_jobs.orchestrator-agent.rerun_failed_steps"}); err != nil {
		t.Fatal(err)
	}
	denied := refusal("linked-a")
	if !denied.Denied || !strings.Contains(denied.Error(), "an owner deny rule (manage_jobs.orchestrator-agent.rerun_failed_steps or manage_jobs) refuses it") ||
		!strings.Contains(denied.Error(), "only the owner can remove that deny rule") {
		t.Fatalf("deny refusal = %q", denied.Error())
	}

	// A spoofed account never reaches the grant check.
	foreign := p
	foreign.AccountScopeID = "foreign"
	if _, err := s.ManageJobs(context.Background(), foreign, current.ID, "orchestrator-agent", args("")); err == nil || strings.Contains(err.Error(), "owner must allow") {
		t.Fatalf("foreign principal: %v", err)
	}
	after, err := ds.GetDelivery(p.AccountScopeID, d.ID)
	if err != nil || !reflect.DeepEqual(after, d) {
		t.Fatalf("a refused action changed the delivery: %v", err)
	}
}

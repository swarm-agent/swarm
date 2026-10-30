package api

import (
	"context"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/permission"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// catalogParityAgent is the Orchestrator's agent name in the parity
// harness: the owner's manage_jobs grants name it.
const catalogParityAgent = "swarm"

// seedJobs gives the home and the linked machine the same worker
// ("parity-worker") with one running job ("job:parity", its run
// parity-run) recorded in its own worker-execution session, as admission
// records it, and grants the home's Orchestrator every manage_jobs change
// (manage_jobs.swarm.<action>), as the owner does on the Permissions page.
func (h *catalogParityHarness) seedJobs(t *testing.T) {
	t.Helper()
	h.jobs = map[string]map[string]string{}
	for _, machine := range []struct {
		name          string
		s             *Server
		account, user string
	}{{"", h.home.s, h.home.account, h.home.user}, {"box", h.remote.s, h.remote.account, h.remote.user}} {
		worker, err := machine.s.sessions.CreateWorker(context.Background(), machine.account, machine.user, pebblestore.CreateWorkerRequest{Name: "parity-worker", Role: "coder"})
		if err != nil {
			t.Fatal(err)
		}
		session := pebblestore.SessionSnapshot{ID: "worker-execution-parity-run", WorkspacePath: t.TempDir(), AccountScopeID: machine.account, UserID: machine.user,
			Metadata: map[string]any{"worker_id": worker.ID, "worker_execution_run_id": "parity-run"}}
		if err := machine.s.sessions.Store().CreateSessionForAccount(session, machine.user, machine.account); err != nil {
			t.Fatal(err)
		}
		run := pebblestore.WorkerRunRecord{ID: "parity-run", WorkerID: worker.ID, UserID: machine.user, Role: pebblestore.WorkerRoleCoder, SessionID: session.ID,
			Status: "running", RequestSource: "direct", Input: map[string]any{"prompt": "Parity job."}, CreatedAt: 1, StartedAt: 1}
		if _, err := machine.s.sessions.Store().WorkerStore().RecordWorkerRun(machine.account, run); err != nil {
			t.Fatal(err)
		}
		h.jobs[machine.name] = map[string]string{"parity": controlMCPJobID(controlMCPJobWorker, worker.ID, "parity-run"), "worker": worker.ID}
	}
	perms := permission.NewService(pebblestore.NewPermissionStore(h.home.s.sessions.Store().Underlying()), nil, nil)
	for action := range nativeJobMutations {
		if _, err := perms.UpsertRuleForAccount(h.home.account, permission.PolicyRule{Kind: permission.PolicyRuleKindTool, Decision: permission.PolicyDecisionAllow, Tool: "manage_jobs." + catalogParityAgent + "." + action}); err != nil {
			t.Fatal(err)
		}
	}
	h.home.s.perm = perms
}

// resolveJob puts a seeded job's id where a case names it as "job:NAME".
func (h *catalogParityHarness) resolveJob(machine, text string) string {
	name, ok := strings.CutPrefix(text, "job:")
	if !ok || h.jobs == nil {
		return text
	}
	if id, ok := h.jobs[machine][name]; ok {
		return id
	}
	return text
}

// manageJobsParityCases holds a case for every manage_jobs action, here and
// on the linked machine box. Reads run on the seeded job and the seeded
// workflow; changes run on jobs and workers that refuse them the same way on
// both surfaces (an unknown job, a worker that does not exist, a running job
// with no question, no delivery and nothing to merge, a stop this harness's
// daemons cannot carry out), so neither call changes what the other reads.
// The end-to-end flow on a real worker is TestManageJobsEndToEndFromBothSurfaces.
func manageJobsParityCases() map[string][]catalogParityCase {
	at := func(machine string, args ...any) map[string]any {
		out := parityArgs(args...)
		if machine != "" {
			out["machine"] = machine
		}
		return out
	}
	everywhere := func(args ...any) []catalogParityCase {
		var out []catalogParityCase
		for _, machine := range []string{"", "box"} {
			call := at(machine, args...)
			out = append(out, catalogParityCase{orchestrator: call, control: call, compare: parityExact})
		}
		return out
	}
	project := map[string]string{"": "home-project", "box": "remote-project"}
	projectCases := func(args ...any) []catalogParityCase {
		var out []catalogParityCase
		for _, machine := range []string{"", "box"} {
			call := at(machine, append(append([]any(nil), args...), "project", project[machine])...)
			out = append(out, catalogParityCase{orchestrator: call, control: call, compare: parityExact})
		}
		return out
	}
	return map[string][]catalogParityCase{
		"list_runs":          everywhere("action", "list_runs", "limit", float64(5)),
		"get_job":            everywhere("action", "get_job", "job_id", "job:parity"),
		"deliveries":         everywhere("action", "deliveries", "id", "wf:Parity"),
		"rerun_failed_steps": everywhere("action", "rerun_failed_steps", "job_id", "job:parity"),
		"give_job":           projectCases("action", "give_job", "worker", "no-such-worker", "brief", "Parity"),
		"message_job":        everywhere("action", "message_job", "job_id", "worker/no-such-worker/no-such-run", "message", "more"),
		"answer_job":         everywhere("action", "answer_job", "job_id", "job:parity", "answer", "yes"),
		"deliver_job":        everywhere("action", "deliver_job", "job_id", "job:parity"),
		"stop_job":           everywhere("action", "stop_job", "job_id", "job:parity"),
		"schedule_job":       projectCases("action", "schedule_job", "worker", "no-such-worker", "brief", "Parity", "schedule", map[string]any{"kind": "interval", "interval_seconds": float64(3600)}),
	}
}

package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"swarm/packages/swarmd/internal/jobautomation"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: an outside AI using Swarm Control (MCP) sees why a workflow
// review sent its task back, as the native get_task shows it:
// swarm_task_result and swarm_wait_task carry progress.workflow with the
// deciding delivery's delivery_id, round and state, the blocking step and
// verdict, and the findings capped to 4000 runes on a rune boundary with
// findings_truncated and a more hint naming this server's call for the full
// text (swarm_list_workflows delivery_id=<id>). A task without a workflow
// gets no progress field.
// Threat: the MCP task tools kept a fixed field set without progress, so an
// outside AI could not learn why its task was sent back, while
// swarm_list_workflows pointed to a field no MCP tool returned.
// Boundary: controlMCPTaskResult and controlMCPWaitTask
// (controlMCPTaskWorkflow over tool.ProjectTaskWorkflow) reading the real
// task route, whose progress comes from taskProgressFor over a real
// jobautomation.Store delivery and evaluation in a temporary Pebble store;
// the MCP tool functions are the narrowest layer that proves what the
// outside AI receives. No provider runs.
func TestControlMCPTaskToolsShowWhyAReviewSentTheTaskBack(t *testing.T) {
	f := newFleetTestSwarm(t, "mcp")
	t.Cleanup(f.cleanup)
	db := f.s.sessions.Store()
	automations := jobautomation.NewStore(db.Underlying())
	f.s.SetJobAutomations(jobautomation.NewRunner(jobautomation.Options{Store: automations}))
	if err := db.PutProject(f.account, &pebblestore.ProjectRecord{ID: "mcp-project", Name: "MCP project"}); err != nil {
		t.Fatal(err)
	}
	for _, task := range []*pebblestore.ProjectTaskRecord{
		{ID: "task-sent-back", ProjectID: "mcp-project", Title: "Sent back", Agent: "coder", Status: "completed", WorktreeBranch: "agent/task-sent-back", UnintegratedCommits: 1},
		{ID: "task-plain", ProjectID: "mcp-project", Title: "No workflow", Agent: "coder", Status: "completed"},
	} {
		if err := db.PutProjectTask(f.account, task); err != nil {
			t.Fatal(err)
		}
	}

	// A real delivery of the sent-back task: review verdict block with long
	// findings (multi-byte runes at the 4000-rune cut), then a send-back.
	folder := t.TempDir()
	a, _, err := automations.Put(f.account, jobautomation.Automation{Name: "ship", Scope: jobautomation.Scope{Folder: folder, Role: "coder"}, Enabled: true, Steps: []jobautomation.Step{
		{ID: "review", Type: jobautomation.StepAgent, Worker: "reviewer", Brief: "Review the changes."},
		{ID: "fix", Type: jobautomation.StepReopen, From: "review", MaxRounds: 2, If: []jobautomation.Condition{{Step: "review", Verdict: jobautomation.VerdictBlock}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	jobID := taskRunJobID("mcp-project", "task-sent-back")
	d, created, err := automations.CreateDelivery(a, jobautomation.Result{V: jobautomation.ResultVersion, JobID: jobID, ProjectID: "mcp-project", Kind: jobautomation.KindTask, Role: "coder", Folder: folder, Status: jobautomation.StatusSucceeded})
	if err != nil || !created {
		t.Fatalf("create delivery: %v %v", created, err)
	}
	findings := "1. high parser.go:12 comments are dropped. " + strings.Repeat("é界", 3000)
	d.State, d.Round, d.SentBack = jobautomation.StateSentBack, 1, &jobautomation.SentBack{Step: "fix", Round: 1, Reason: "review verdict block"}
	d.Steps[0].State, d.Steps[0].Outcome = jobautomation.StateSucceeded, map[string]any{"verdict": jobautomation.VerdictBlock, "findings": findings}
	d.Steps[1].State = jobautomation.StateSucceeded
	if err := automations.SaveDelivery(d); err != nil {
		t.Fatal(err)
	}
	if err := automations.RecordEvaluation(f.account, "mcp-project", jobID, "", 7, 4000, []string{d.ID}); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	request = requestWithTestPrincipalForAccount(request, f.user, f.account)
	request = requestWithScopedToken(request, &pebblestore.ScopedTokenRecord{Scopes: []string{"swarm:read", "sessions:read", "projects:read"}})
	call := &controlMCPCall{request: request, next: f.s.internalRouteHandler(), server: f.s}

	check := func(name string, out map[string]any) {
		t.Helper()
		wf := controlMCPMap(controlMCPMap(out["progress"])["workflow"])
		if wf == nil || wf["delivery_id"] != d.ID || controlMCPTestNumber(wf["round"]) != 1 || wf["state"] != "sent_back" || wf["reason"] != "review verdict block" || wf["findings_step"] != "review" || wf["verdict"] != jobautomation.VerdictBlock {
			t.Fatalf("%s workflow = %v", name, wf)
		}
		got, _ := wf["findings"].(string)
		if !utf8.ValidString(got) || utf8.RuneCountInString(got) != 4000 || !strings.HasPrefix(findings, got) {
			t.Fatalf("%s findings: valid %v, %d runes, prefix %v", name, utf8.ValidString(got), utf8.RuneCountInString(got), strings.HasPrefix(findings, got))
		}
		if wf["findings_truncated"] != true || wf["more"] != "swarm_list_workflows delivery_id="+d.ID {
			t.Fatalf("%s truncated %v more %v", name, wf["findings_truncated"], wf["more"])
		}
	}
	result, err := controlMCPTaskResult(call, map[string]any{"project_id": "mcp-project", "task_id": "task-sent-back"})
	if err != nil {
		t.Fatal(err)
	}
	check("swarm_task_result", controlMCPMap(result))
	wait, err := controlMCPWaitTask(call, map[string]any{"project_id": "mcp-project", "task_id": "task-sent-back", "wait_seconds": float64(1)})
	if err != nil {
		t.Fatal(err)
	}
	check("swarm_wait_task", controlMCPMap(wait))

	// A task without a workflow: no progress field on either tool.
	for name, read := range map[string]func() (any, error){
		"swarm_task_result": func() (any, error) {
			return controlMCPTaskResult(call, map[string]any{"project_id": "mcp-project", "task_id": "task-plain"})
		},
		"swarm_wait_task": func() (any, error) {
			return controlMCPWaitTask(call, map[string]any{"project_id": "mcp-project", "task_id": "task-plain", "wait_seconds": float64(1)})
		},
	} {
		out, err := read()
		if err != nil {
			t.Fatal(err)
		}
		if p, ok := controlMCPMap(out)["progress"]; ok {
			t.Fatalf("%s for a task without a workflow has progress %v", name, p)
		}
	}
}

package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/jobautomation"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// seedWorkflowListing saves workflows like the forge review loop, each with
// deliveries whose steps recorded long findings, reports and outputs, and
// returns one delivery id per workflow.
func seedWorkflowListing(t *testing.T, store *jobautomation.Store, workflows, deliveries int) []string {
	t.Helper()
	account := testPrincipal().AccountScopeID
	folder := t.TempDir()
	findings := "1. high a.go:1 the limit is not checked. " + strings.Repeat("FINDINGTEXT ", 1500) // ~18 KB
	ids := []string{}
	for w := 0; w < workflows; w++ {
		a, _, err := store.Put(account, jobautomation.Automation{Name: "review loop " + strconv.Itoa(w), Scope: jobautomation.Scope{Folder: folder, Role: "coder"}, Enabled: true, Steps: []jobautomation.Step{
			{ID: "review", Type: jobautomation.StepAgent, Worker: "reviewer", Brief: "Review the changes."},
			{ID: "send-back", Type: jobautomation.StepReopen, From: "review", MaxRounds: 2, If: []jobautomation.Condition{{Step: "review", Verdict: jobautomation.VerdictBlock}}},
			{ID: "push", Type: jobautomation.StepRun, Command: "/bin/true", If: []jobautomation.Condition{{Step: "review", Verdict: jobautomation.VerdictOK}}},
			{ID: "rebase", Type: jobautomation.StepReopen, From: "review", Message: "Rebase onto dev.", MaxRounds: 2, If: []jobautomation.Condition{{Step: "review", Verdict: jobautomation.VerdictOK}, {Step: "push", Status: jobautomation.StateFailed}}},
		}})
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < deliveries; i++ {
			job := "task/proj-1/task-" + strconv.Itoa(w) + "-" + strconv.Itoa(i)
			result := jobautomation.Result{V: jobautomation.ResultVersion, JobID: job, Kind: jobautomation.KindTask, Role: "coder", Folder: folder, Status: jobautomation.StatusSucceeded,
				Report: strings.Repeat("REPORTTEXT ", 1000), Git: &jobautomation.ResultGit{Head: strings.Repeat(strconv.Itoa(i%10), 40), Commits: []jobautomation.ResultCommit{{SHA: strings.Repeat("c", 40), Subject: "change"}}}}
			d, created, err := store.CreateDelivery(a, result)
			if err != nil || !created {
				t.Fatalf("create delivery: %v %v", created, err)
			}
			exit := 1
			d.State, d.Round, d.BlockedReason = jobautomation.StateBlocked, 2, jobautomation.RoundsExhaustedReason("rebase")
			d.Steps[0].State, d.Steps[0].Outcome = jobautomation.StateSucceeded, map[string]any{"job_id": "task/proj-1/review-" + strconv.Itoa(i), "verdict": jobautomation.VerdictOK, "findings": findings, "report": strings.Repeat("REVIEWREPORT ", 800)}
			d.Steps[1].State, d.Steps[1].Reason = jobautomation.StateSkipped, "condition not met: step review verdict is ok, not block"
			d.Steps[2].State, d.Steps[2].ExitCode, d.Steps[2].Output = jobautomation.StateFailed, &exit, strings.Repeat("PUSHOUTPUT ", 400)
			d.Steps[3].State, d.Steps[3].Output, d.Steps[3].Reason = jobautomation.StateFailed, "2 of 2 reopens of step rebase used", jobautomation.RoundsExhaustedReason("rebase")
			for f := 0; f < 10; f++ {
				d.Steps[2].Failures = append(d.Steps[2].Failures, jobautomation.AttemptFailure{Attempt: f + 1, Reason: strings.Repeat("FAILREASON ", 300)})
			}
			if err := store.SaveDelivery(d); err != nil {
				t.Fatal(err)
			}
			if i == deliveries-1 {
				ids = append(ids, d.ID)
			}
		}
	}
	return ids
}

// Requirement: a workflow listing is bounded. By default it lists each
// workflow's definition (name, scope, when, steps, revision, enabled) and its
// last 3 deliveries one line each (job, state, round, head, blocked reason,
// each step's state and verdict) with no findings, outputs or reports; detail
// comes only with an explicit delivery_id or detail "deliveries" and is still
// bounded: detail "deliveries" cuts findings to 1500 characters, and one
// delivery_id returns each step's findings whole up to the review record's
// own bound (pebblestore.MaxJobReviewFindingsBytes), since get_task points
// there for the full text. The MCP route, HTTP and the native
// manage_workflows list/deliveries read the same listing.
// Threat (forge, 2026-10-28): swarm_list_workflows returned 159 KB for 4
// workflows because every recent delivery embedded every step's findings and
// outputs; a delivery_id from another account must not be readable.
// Owners: Server.ListWorkflows, workflowDeliveryLine, workflowDeliveryDetail,
// workflowListOptions, controlMCPListAutomations, handleJobAutomations and
// Server.ManageWorkflows over the real jobautomation store.
// Layer: the MCP tool dispatched through the real HTTP handler is the
// narrowest layer that measures what the outside AI actually receives.
func TestWorkflowListingIsCompactByDefaultAndBoundedInDetail(t *testing.T) {
	server, store := newRunOwnerServer(t)
	ids := seedWorkflowListing(t, store, 4, 10)
	c := &controlMCPCall{server: server, request: requestWithScopedToken(withTestPrincipal(httptest.NewRequest("POST", "/mcp", nil)), &pebblestore.ScopedTokenRecord{ID: "key-read", Scopes: scopesForLevel(t, "read")})}
	wireWorkflowRoutes(c)
	size := func(v any) (int, string) {
		data, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return len(data), string(data)
	}

	out, err := controlMCPListAutomations(c, nil)
	if err != nil {
		t.Fatal(err)
	}
	n, text := size(out)
	t.Logf("default listing = %d bytes", n)
	if n > 12*1024 {
		t.Fatalf("default listing of 4 workflows x 10 deliveries = %d bytes, want <= 12 KB", n)
	}
	for _, leaked := range []string{"FINDINGTEXT", "REPORTTEXT", "REVIEWREPORT", "PUSHOUTPUT", "FAILREASON", "condition not met", `"outcome"`} {
		if strings.Contains(text, leaked) {
			t.Fatalf("default listing contains %q", leaked)
		}
	}
	list := out.(map[string]any)["automations"].([]any)
	if len(list) != 4 {
		t.Fatalf("workflows = %d, want 4", len(list))
	}
	for _, raw := range list {
		w := raw.(map[string]any)
		for _, key := range []string{"name", "scope", "when", "steps", "revision", "enabled"} {
			if _, ok := w[key]; !ok {
				t.Fatalf("workflow lacks %s: %v", key, w)
			}
		}
		lines := w["latest_deliveries"].([]any)
		if len(lines) != 3 {
			t.Fatalf("deliveries listed = %d, want the last 3", len(lines))
		}
		line := lines[0].(map[string]any)
		if line["state"] != jobautomation.StateBlocked || line["round"] != float64(2) || line["blocked_reason"] != "rounds_exhausted: step rebase" || line["job_id"] == "" || len(line["head"].(string)) != 40 || line["delivery_id"] == "" {
			t.Fatalf("delivery line = %v", line)
		}
		steps := line["steps"].([]any)
		if review := steps[0].(map[string]any); len(steps) != 4 || review["state"] != jobautomation.StateSucceeded || review["verdict"] != jobautomation.VerdictOK || len(review) != 3 {
			t.Fatalf("step lines = %v", steps)
		}
	}

	// One delivery in bounded detail.
	one, err := controlMCPListAutomations(c, map[string]any{"delivery_id": ids[0]})
	if err != nil {
		t.Fatal(err)
	}
	if n, text := size(one); n > 32*1024 || !strings.Contains(text, "FINDINGTEXT") || strings.Contains(text, `"automations"`) {
		t.Fatalf("one delivery = %d bytes", n)
	}
	detail := one.(map[string]any)["delivery"].(map[string]any)
	review := detail["steps"].([]any)[0].(map[string]any)
	findings := review["outcome"].(map[string]any)["findings"].(string)
	// The seeded findings (~18 KB) exceed the record bound, so the full
	// text is cut there, and only there.
	if n := len([]rune(findings)); !strings.HasPrefix(findings, "1. high a.go:1 the limit is not checked.") || !strings.Contains(findings, "[truncated ") || n < pebblestore.MaxJobReviewFindingsBytes || n > pebblestore.MaxJobReviewFindingsBytes+60 {
		t.Fatalf("findings in one delivery = %d characters, want the record bound %d", n, pebblestore.MaxJobReviewFindingsBytes)
	}
	// Other texts of the one delivery stay bounded.
	if report, _ := review["outcome"].(map[string]any)["report"].(string); len([]rune(report)) > maxWorkflowDetailText+60 {
		t.Fatalf("report in one delivery = %d characters", len([]rune(report)))
	}
	if failures := detail["steps"].([]any)[2].(map[string]any)["failures"].([]any); len(failures) != maxWorkflowDetailFailures {
		t.Fatalf("failures in detail = %d, want the newest %d", len(failures), maxWorkflowDetailFailures)
	}

	// Every workflow's last deliveries in bounded detail.
	all, err := controlMCPListAutomations(c, map[string]any{"detail": "deliveries"})
	if err != nil {
		t.Fatal(err)
	}
	if n, text := size(all); n > 160*1024 || !strings.Contains(text, `"outcome"`) || strings.Contains(text, strings.Repeat("FINDINGTEXT ", 200)) {
		t.Fatalf("detail listing = %d bytes", n)
	}

	// Refusals: an unknown detail, and another account's delivery.
	if _, err := controlMCPListAutomations(c, map[string]any{"detail": "everything"}); err == nil || !strings.Contains(err.Error(), "detail must be") {
		t.Fatalf("unknown detail: %v", err)
	}
	other := &controlMCPCall{server: server, request: requestWithScopedToken(withTestPrincipal(httptest.NewRequest("POST", "/mcp", nil)), &pebblestore.ScopedTokenRecord{ID: "key-other", Scopes: scopesForLevel(t, "read")})}
	principal := testPrincipal()
	principal.AccountScopeID = "other-account"
	other.request = other.request.WithContext(context.WithValue(other.request.Context(), productPrincipalRequestContextKey, principal))
	wireWorkflowRoutes(other)
	if got, err := controlMCPListAutomations(other, map[string]any{"delivery_id": ids[0]}); err == nil || len(got.(map[string]any)) != 0 || !strings.Contains(err.Error(), "no delivery") {
		t.Fatalf("cross-account delivery: %v %v", got, err)
	}

	// The native manage_workflows list and deliveries read the same bounded views.
	native, err := server.ManageWorkflows(context.Background(), testPrincipal(), "session-1", map[string]any{"action": "list"})
	if err != nil {
		t.Fatal(err)
	}
	if n, text := size(native); n > 12*1024 || strings.Contains(text, "FINDINGTEXT") {
		t.Fatalf("native list = %d bytes", n)
	}
	deliveries, err := server.ManageWorkflows(context.Background(), testPrincipal(), "session-1", map[string]any{"action": "deliveries"})
	if err != nil {
		t.Fatal(err)
	}
	if n, text := size(deliveries); strings.Contains(text, "FINDINGTEXT") || n > 32*1024 || len(deliveries.(map[string]any)["deliveries"].([]map[string]any)) != 40 {
		t.Fatalf("native deliveries = %d bytes", n)
	}
	nativeOne, err := server.ManageWorkflows(context.Background(), testPrincipal(), "session-1", map[string]any{"action": "deliveries", "delivery_id": ids[1]})
	if err != nil {
		t.Fatal(err)
	}
	if _, text := size(nativeOne); !strings.Contains(text, "FINDINGTEXT") || !strings.Contains(text, "[truncated ") {
		t.Fatalf("native delivery detail = %s", text[:min(len(text), 300)])
	}
	var notFound controlMCPToolError
	if _, err := server.ManageWorkflows(context.Background(), principal, "session-2", map[string]any{"action": "list", "delivery_id": ids[1]}); !errors.As(err, &notFound) {
		t.Fatalf("native cross-account delivery: %v", err)
	}
}

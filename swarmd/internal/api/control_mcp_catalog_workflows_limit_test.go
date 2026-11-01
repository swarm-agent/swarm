package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/jobautomation"
	"swarm/packages/swarmd/internal/tool"
)

// longReviewLoop is a portable step list of n steps: n-1 native reviews and
// a rebase-after-fix reopen that runs only when the first one failed saying
// "rebase needed" (no run step: those stay the machine owner's).
func longReviewLoop(n int) []any {
	steps := make([]any, 0, n)
	for i := 1; i < n; i++ {
		steps = append(steps, map[string]any{"id": fmt.Sprintf("review-%d", i), "type": "agent", "worker": "reviewer", "brief": "Review the change"})
	}
	return append(steps, map[string]any{"id": "rebase-after-fix", "type": "reopen", "message": "The gate needs a rebase.", "max_rounds": float64(2),
		"if": []any{map[string]any{"step": "review-1", "status": "failed", "output_contains": "rebase needed"}}})
}

// Purpose: the step limit (jobautomation.MaxSteps, 40) and a condition's
// output_contains are one contract on every surface: the Orchestrator's
// manage_workflows and Swarm Control's (MCP), here and on a linked machine,
// accept a workflow of more than 20 and up to 40 steps whose reopen step
// carries output_contains, store the condition exactly as sent once the
// person accepts it, and refuse 41 steps and an output_contains on a verdict
// with the same reason and no proposal saved. The declared schema (maxItems,
// the if items' output_contains) and the help guide name the same bound and
// the rebase-after-fix example.
// Threat: one surface dropping output_contains (a reopen that then fires on
// every failure), a linked machine still refusing more than 20 steps, or a
// schema/help that drifts from the bound validation enforces.
// Boundary: tool.Runtime.executeManageWorkflows and the Swarm Control tool
// over api.Server.ManageWorkflows (and the linked machine's routes) on two
// in-process daemons; acceptance is the person's HTTP accept route; the
// catalog declaration (tool.CatalogToolNamed). No provider runs.
func TestManageWorkflowsStepLimitAndOutputContainsParity(t *testing.T) {
	h := newCatalogParityHarness(t)

	// The declared schema and the guide carry the bound and the field.
	catalog, ok := tool.CatalogToolNamed("manage_workflows")
	if !ok {
		t.Fatal("manage_workflows is not in the catalog")
	}
	field, ok := catalog.Field("steps")
	if !ok {
		t.Fatal("manage_workflows declares no steps")
	}
	if field.Schema["maxItems"] != jobautomation.MaxSteps {
		t.Fatalf("steps maxItems = %v, want %d", field.Schema["maxItems"], jobautomation.MaxSteps)
	}
	condition := field.Schema["items"].(map[string]any)["properties"].(map[string]any)["if"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)
	if contains, _ := condition["output_contains"].(map[string]any); contains == nil || contains["maxLength"] != jobautomation.MaxOutputContainsBytes {
		t.Fatalf("if items lack output_contains with its bound: %v", condition)
	}

	for _, machine := range []string{"", "box"} {
		t.Run("machine="+machine, func(t *testing.T) {
			m := h.workflowFixesMachine(t, machine)
			at := func(args map[string]any) map[string]any {
				if machine != "" {
					args["machine"] = machine
				}
				return args
			}
			surfaces := map[string]func(map[string]any) (map[string]any, string){
				"orchestrator": func(args map[string]any) (map[string]any, string) {
					t.Helper()
					out, err := h.orchestratorTool(t, "manage_workflows", at(args))
					if err != nil {
						return nil, err.Error()
					}
					return catalogEnvelopeCheck(t, fmt.Sprint(args["action"]), out), ""
				},
				"control": func(args map[string]any) (map[string]any, string) {
					t.Helper()
					out, refused := h.controlTool(t, catalogParityFullKey, "manage_workflows", at(args))
					if refused != "" {
						return nil, catalogFailureReason(t, refused)
					}
					return catalogEnvelopeCheck(t, fmt.Sprint(args["action"]), out), ""
				},
			}
			proposals := func() int {
				list, _ := m.store.List(m.account)
				n := 0
				for _, a := range list {
					if a.Proposed {
						n++
					}
				}
				return n
			}
			for _, surface := range []string{"orchestrator", "control"} {
				call := surfaces[surface]
				if machine == "" {
					envelope, refused := call(map[string]any{"action": "help"})
					guide := fmt.Sprint(envelopeDataMap(envelope)["guide"])
					if refused != "" || !strings.Contains(guide, "1 to 40 steps") || !strings.Contains(guide, "rebase-after-fix") || !strings.Contains(guide, `output_contains:"rebase needed"`) {
						t.Fatalf("%s help: %q %s", surface, refused, guide)
					}
				}
				name := "Long loop " + surface
				envelope, refused := call(map[string]any{"action": "propose", "name": name, "project": m.project, "scope": map[string]any{"role": "coder"}, "steps": longReviewLoop(jobautomation.MaxSteps)})
				if refused != "" {
					t.Fatalf("%s: %d steps refused: %s", surface, jobautomation.MaxSteps, refused)
				}
				proposal := envelopeDataMap(envelope)["automation"].(map[string]any)
				req := httptest.NewRequest(http.MethodPost, JobAutomationsPath+"/"+proposal["id"].(string)+"/accept", strings.NewReader(fmt.Sprintf(`{"expected_revision":%v}`, proposal["revision"])))
				w := httptest.NewRecorder()
				m.server.handleJobAutomations(w, requestWithTestPrincipalForAccount(req, m.user, m.account))
				if w.Code != http.StatusOK {
					t.Fatalf("%s: the person's accept: %d %s", surface, w.Code, w.Body)
				}
				saved, err := m.store.FindByName(m.account, name)
				if err != nil || len(saved.Steps) != jobautomation.MaxSteps {
					t.Fatalf("%s: saved %d steps, %v", surface, len(saved.Steps), err)
				}
				last := saved.Steps[len(saved.Steps)-1]
				if len(last.If) != 1 || last.If[0] != (jobautomation.Condition{Step: "review-1", Status: jobautomation.StateFailed, OutputContains: "rebase needed"}) {
					t.Fatalf("%s: stored condition = %+v", surface, last.If)
				}

				before := proposals()
				if _, refused := call(map[string]any{"action": "propose", "name": name + " too long", "project": m.project, "scope": map[string]any{"role": "coder"}, "steps": longReviewLoop(jobautomation.MaxSteps + 1)}); !strings.Contains(refused, "an automation needs 1 to 40 steps") {
					t.Fatalf("%s: %d steps: %q", surface, jobautomation.MaxSteps+1, refused)
				}
				onVerdict := []any{
					map[string]any{"id": "review", "type": "agent", "worker": "reviewer", "brief": "Review the change"},
					map[string]any{"id": "back", "type": "reopen", "from": "review", "if": []any{map[string]any{"step": "review", "verdict": "block", "output_contains": "rebase needed"}}},
				}
				if _, refused := call(map[string]any{"action": "propose", "name": name + " verdict", "project": m.project, "scope": map[string]any{"role": "coder"}, "steps": onVerdict}); !strings.Contains(refused, "output_contains on review goes with a status") {
					t.Fatalf("%s: output_contains on a verdict: %q", surface, refused)
				}
				if after := proposals(); after != before {
					t.Fatalf("%s: a refused propose saved a proposal: %d -> %d", surface, before, after)
				}
			}
		})
	}
}

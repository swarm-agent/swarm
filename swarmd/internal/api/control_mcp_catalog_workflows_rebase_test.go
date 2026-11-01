package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/jobautomation"
)

// rebaseWorkflowSteps is a portable step list with Swarm's explicit rebase
// step: rebase onto dev, then a native review (no run step: those stay the
// machine owner's).
func rebaseWorkflowSteps() []any {
	return []any{
		map[string]any{"id": "rebase", "type": "rebase", "onto": "dev"},
		map[string]any{"id": "review", "type": "agent", "worker": "reviewer", "brief": "Review the change"},
	}
}

// Purpose: the workflow rebase setting (rebase: auto|off, rebase_rounds) and
// the {type: "rebase", onto} step are one contract on every surface: the
// Orchestrator's manage_workflows and Swarm Control's (MCP), here and on a
// linked machine (fleet_work's forwarded propose body), accept them, store
// them exactly as sent once the person accepts the proposal, read them back
// alike through get, and refuse rebase:"sometimes", rebase_rounds 6 and onto
// on a non-rebase step with the same reason on both surfaces and no proposal
// saved. A proposal that changes only the rebase setting names it in its
// change summary (workflowChangeSummary). The declared schema carries both
// fields.
// Threat: a surface (or the linked forwarding) dropping rebase or
// rebase_rounds, so an opt-out silently turns into auto; a misspelt setting
// or an out-of-bound round count accepted on one surface; a change the person
// accepts without being told it changes the rebase setting.
// Boundary: tool.Runtime.executeManageWorkflows and the Swarm Control tool
// over api.Server.ManageWorkflows (and the linked machine's routes) on two
// in-process daemons; acceptance is the person's HTTP accept route. No
// provider runs.
func TestManageWorkflowsRebaseSettingAndStepParity(t *testing.T) {
	h := newCatalogParityHarness(t)
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
			refusals := map[string]map[string]string{}
			for _, surface := range []string{"orchestrator", "control"} {
				call := surfaces[surface]
				name := "Rebase setting " + surface
				envelope, refused := call(map[string]any{"action": "propose", "name": name, "project": m.project, "scope": map[string]any{"role": "coder"},
					"rebase": "off", "rebase_rounds": float64(3), "steps": rebaseWorkflowSteps()})
				if refused != "" {
					t.Fatalf("%s: propose refused: %s", surface, refused)
				}
				proposal := envelopeDataMap(envelope)["automation"].(map[string]any)
				req := httptest.NewRequest(http.MethodPost, JobAutomationsPath+"/"+proposal["id"].(string)+"/accept", strings.NewReader(fmt.Sprintf(`{"expected_revision":%v}`, proposal["revision"])))
				w := httptest.NewRecorder()
				m.server.handleJobAutomations(w, requestWithTestPrincipalForAccount(req, m.user, m.account))
				if w.Code != http.StatusOK {
					t.Fatalf("%s: the person's accept: %d %s", surface, w.Code, w.Body)
				}
				saved, err := m.store.FindByName(m.account, name)
				if err != nil || saved.Rebase != jobautomation.RebaseOff || saved.RebaseRounds != 3 || len(saved.Steps) != 2 ||
					saved.Steps[0].Type != jobautomation.StepRebase || saved.Steps[0].Onto != "dev" {
					t.Fatalf("%s: stored %+v, %v", surface, saved, err)
				}
				got, refused := call(map[string]any{"action": "get", "id": saved.ID})
				view, _ := envelopeDataMap(got)["automation"].(map[string]any)
				if refused != "" || view["rebase"] != jobautomation.RebaseOff || fmt.Sprint(view["rebase_rounds"]) != "3" {
					t.Fatalf("%s: get: %q %v", surface, refused, view)
				}

				// Only the rebase setting changes: the person is told.
				base := jobAutomationView(saved)
				changed := saved
				changed.Rebase, changed.RebaseRounds = "", 0
				if _, changes := workflowChangeSummary(base, jobAutomationView(changed)); !slices.Equal(changes["fields_changed"].([]string), []string{"rebase", "rebase_rounds"}) {
					t.Fatalf("%s: change summary fields = %v", surface, changes["fields_changed"])
				}

				before := proposals()
				bad := map[string]map[string]any{
					"rebase sometimes":      {"rebase": "sometimes", "steps": rebaseWorkflowSteps()},
					"rebase_rounds 6":       {"rebase_rounds": float64(6), "steps": rebaseWorkflowSteps()},
					"onto on an agent step": {"steps": []any{map[string]any{"id": "review", "type": "agent", "worker": "reviewer", "brief": "Review the change", "onto": "dev"}}},
				}
				refusals[surface] = map[string]string{}
				for label, fields := range bad {
					args := map[string]any{"action": "propose", "name": name + " " + label, "project": m.project, "scope": map[string]any{"role": "coder"}}
					for k, v := range fields {
						args[k] = v
					}
					_, refused := call(args)
					if refused == "" || !strings.Contains(refused, "rebase") && !strings.Contains(refused, "onto") {
						t.Fatalf("%s: %s accepted or refused for another reason: %q", surface, label, refused)
					}
					refusals[surface][label] = refused
				}
				if after := proposals(); after != before {
					t.Fatalf("%s: a refused propose saved a proposal: %d -> %d", surface, before, after)
				}
			}
			for label, reason := range refusals["orchestrator"] {
				if control := refusals["control"][label]; control != reason {
					t.Fatalf("%s refused differently:\n orchestrator %q\n control      %q", label, reason, control)
				}
			}
		})
	}
}

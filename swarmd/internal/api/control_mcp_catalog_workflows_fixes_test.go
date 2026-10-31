package api

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/jobautomation"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// workflowFixesMachine is one machine of the workflow fixes test: its store,
// account, server, user and the project (with its folder) workflows bind to.
type workflowFixesMachine struct {
	machine, account, user, project, projectName, folder string
	store                                                *jobautomation.Store
	server                                               *Server
}

func (h *catalogParityHarness) workflowFixesMachine(t *testing.T, machine string) workflowFixesMachine {
	t.Helper()
	m := workflowFixesMachine{machine: machine, account: h.home.account, user: h.home.user, project: "wf-project", store: h.homeWorkflows, server: h.home.s}
	if machine != "" {
		m.account, m.user, m.project, m.store, m.server = h.remote.account, h.remote.user, "remote-project", h.remoteWorkflows, h.remote.s
	}
	record, found, err := m.server.sessions.Store().GetProject(m.account, m.project)
	if err != nil || !found || len(record.Workspaces) == 0 {
		t.Fatalf("project %s on %q: %v %v", m.project, machine, found, err)
	}
	m.projectName, m.folder = record.Name, record.Workspaces[0].Path
	return m
}

// disabledDefinition is a portable, disabled one-step workflow.
func disabledDefinition(name string) map[string]any {
	return map[string]any{"swarm_workflow": float64(1), "name": name, "enabled": false, "steps": []any{map[string]any{"type": "agent", "worker": "reviewer", "brief": "x"}}}
}

// Purpose: manage_workflows rewrites shared refusals into catalog terms
// without changing their type, and the swarm_* tools' shared texts keep
// their own contract. Threat: an Orchestrator sent to swarm_create_project,
// which it does not have; or a refusal that stops being a tool refusal (MCP
// isError) once rewritten. Boundary: workflowCatalogError over place's
// refusal text; the narrowest layer that owns the mapping.
func TestWorkflowCatalogErrorRewritesSharedRefusals(t *testing.T) {
	for legacy, want := range map[string]string{
		"there is no project yet: ask the person which folders their work is in and create one with swarm_create_project (a name and those folders)": "manage_projects create",
		`several projects are named "p": pass the project id from swarm_list_projects`:                                                               "manage_projects list",
		"project p has no folder: add one with swarm_set_project_workspaces":                                                                         "manage_projects update (workspaces)",
		"/x is not a folder of project p (/y): add it with swarm_set_project_workspaces":                                                             "manage_projects update (workspaces)",
	} {
		got := workflowCatalogError(controlMCPToolFailure("%s", legacy))
		var refusal controlMCPToolError
		if !errors.As(got, &refusal) || !strings.Contains(got.Error(), want) || workflowLegacyToolName.MatchString(got.Error()) {
			t.Fatalf("%q -> %q (%T)", legacy, got, got)
		}
	}
	wrapped := fmt.Errorf("linked machine box refused import: %w", jobautomation.ErrNotFound)
	if got := workflowCatalogError(wrapped); got != wrapped {
		t.Fatalf("an error without a swarm_* name changed: %v", got)
	}
	plain := fmt.Errorf("remote: no project \"p\": or create it with swarm_create_project: %w", jobautomation.ErrNotFound)
	if got := workflowCatalogError(plain); !errors.Is(got, jobautomation.ErrNotFound) || !strings.Contains(got.Error(), "manage_projects create") {
		t.Fatalf("a linked refusal: %v", got)
	}
	if got := workflowCatalogError(controlMCPToolFailure("swarm_workflow must be version 1")); got.Error() != "swarm_workflow must be version 1" {
		t.Fatalf("the format key changed: %q", got)
	}
	if workflowCatalogError(nil) != nil {
		t.Fatal("nil error")
	}
}

// workflowLegacyToolName matches a swarm_* tool name; swarm_workflow is the
// portable format's version key, not a tool.
var workflowLegacyToolName = regexp.MustCompile(`swarm_[a-z_]+`)

// Purpose: the seven manage_workflows defects the parity run found stay
// fixed, here and on a linked machine, through the Orchestrator and Swarm
// Control alike:
//  1. a workflow has a description (propose, get, list, export, import,
//     history) and propose refuses fields it does not take, naming them;
//  2. propose with a workflow's id or name merges only the given fields
//     onto its current revision (description, a step's brief by its id, a
//     new name, enabled), expected_revision refuses a stale base, and
//     replace: true sends a whole definition;
//  3. get, enable, disable, delete, export, history and restore take a
//     name or an id, and an ambiguous name is refused naming each match;
//  4. answers name catalog tools only and a disabled import says it does
//     not run until enabled;
//  5. export carries the scope (the project) so an import binds the same
//     folder, refuses (never widens) a project the machine lacks, and
//     history lists pending proposals with their state;
//  6. propose's answer has a next_action: the person accepts it, in their
//     app;
//  7. a run-step import is an envelope (state refused, reason,
//     next_action) and saves nothing, while the owner's import route still
//     answers 403 to a key without admin.
//
// Threat: a field silently dropped, an edit that needs the whole workflow
// resent or overwrites a newer revision, a delete that cannot find its
// workflow on a linked machine, an answer that sends the AI to a tool it does
// not have or misdescribes a disabled workflow, a fork that silently loses or
// widens its folder, and a refusal a client reads as plain text. Boundary:
// tool.Runtime.executeManageWorkflows over api.Server.ManageWorkflows (and
// a linked machine's routes) on two in-process daemons; acceptance is the
// person's HTTP accept route. No provider runs.
func TestManageWorkflowsParityFixes(t *testing.T) {
	h := newCatalogParityHarness(t)
	for _, machine := range []string{"", "box"} {
		t.Run("machine="+machine, func(t *testing.T) {
			m := h.workflowFixesMachine(t, machine)
			var answers []string
			at := func(args map[string]any) map[string]any {
				if machine != "" {
					args["machine"] = machine
				}
				return args
			}
			orchestrator := func(args map[string]any) (map[string]any, string) {
				t.Helper()
				out, err := h.orchestratorTool(t, "manage_workflows", at(args))
				if err != nil {
					answers = append(answers, err.Error())
					return nil, err.Error()
				}
				answers = append(answers, out)
				return catalogEnvelopeCheck(t, fmt.Sprint(args["action"]), out), ""
			}
			control := func(args map[string]any) (map[string]any, string) {
				t.Helper()
				out, refused := h.controlTool(t, catalogParityFullKey, "manage_workflows", at(args))
				if refused != "" {
					answers = append(answers, refused)
					return nil, refused
				}
				answers = append(answers, out)
				return catalogEnvelopeCheck(t, fmt.Sprint(args["action"]), out), ""
			}
			must := func(name string, envelope map[string]any, refused string) map[string]any {
				t.Helper()
				if refused != "" {
					t.Fatalf("%s refused: %s", name, refused)
				}
				return envelopeDataMap(envelope)
			}
			ok := func(name string, call func(map[string]any) (map[string]any, string), args map[string]any) map[string]any {
				t.Helper()
				envelope, refused := call(args)
				return must(name, envelope, refused)
			}
			accept := func(proposal map[string]any) {
				t.Helper()
				req := httptest.NewRequest(http.MethodPost, JobAutomationsPath+"/"+proposal["id"].(string)+"/accept", strings.NewReader(fmt.Sprintf(`{"expected_revision":%v}`, proposal["revision"])))
				w := httptest.NewRecorder()
				m.server.handleJobAutomations(w, requestWithTestPrincipalForAccount(req, m.user, m.account))
				if w.Code != http.StatusOK {
					t.Fatalf("the person's accept: %d %s", w.Code, w.Body)
				}
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
			base, _, err := m.store.Put(m.account, jobautomation.Automation{Name: "Desc", Scope: jobautomation.Scope{Folder: m.folder}, Enabled: true, Steps: parityWorkflowSteps("Review v1")})
			if err != nil {
				t.Fatal(err)
			}

			// 1, 2, 6: a description alone, on the named workflow's current
			// revision (the Orchestrator, named by name in id).
			envelope, refused := orchestrator(map[string]any{"action": "propose", "id": "Desc", "description": "Reviews every change", "expected_revision": float64(base.Revision)})
			data := must("propose description", envelope, refused)
			proposal := data["automation"].(map[string]any)
			if envelope["state"] != "ok" || !strings.HasPrefix(fmt.Sprint(envelope["next_action"]), "The person accepts or discards this proposal in their Swarm app") {
				t.Fatalf("propose envelope: %v", envelope)
			}
			if proposal["description"] != "Reviews every change" || !strings.Contains(mustJSON(t, proposal["steps"]), "Review v1") || !strings.Contains(mustJSON(t, data["changes"]), "description") || strings.Contains(mustJSON(t, data["changes"]), "steps") {
				t.Fatalf("a description change did not merge onto the current revision: %v", data)
			}
			if current, _ := m.store.Get(m.account, base.ID); current.Description != "" || current.Revision != base.Revision {
				t.Fatalf("a proposal changed the workflow: %+v", current)
			}
			accept(proposal)
			if current, _ := m.store.Get(m.account, base.ID); current.Description != "Reviews every change" || current.Steps[0].Brief != "Review v1" {
				t.Fatalf("accepted description: %+v", current)
			}
			// Shown by get (by name) and list.
			data = ok("get", control, map[string]any{"action": "get", "name": "Desc"})
			if data["automation"].(map[string]any)["description"] != "Reviews every change" {
				t.Fatalf("get: %v", data)
			}
			data = ok("list", orchestrator, map[string]any{"action": "list"})
			if !strings.Contains(mustJSON(t, data["automations"]), `"description":"Reviews every change"`) {
				t.Fatalf("list lacks the description: %v", data)
			}
			// 2: one step's brief, by its id (Swarm Control, by name).
			data = ok("propose brief", control, map[string]any{"action": "propose", "name": "Desc", "steps": []any{map[string]any{"id": "review", "brief": "Review v2"}}})
			accept(data["automation"].(map[string]any))
			current, _ := m.store.Get(m.account, base.ID)
			if current.Steps[0].Brief != "Review v2" || current.Steps[0].Worker != "reviewer" || current.Description != "Reviews every change" {
				t.Fatalf("a step's brief did not merge: %+v", current)
			}
			// A stale expected_revision and an unknown step are refused;
			// no proposal is saved.
			before := proposals()
			if _, refused := orchestrator(map[string]any{"action": "propose", "id": base.ID, "enabled": false, "expected_revision": float64(base.Revision)}); !strings.Contains(refused, fmt.Sprintf("is at revision %d, not %d", current.Revision, base.Revision)) {
				t.Fatalf("stale expected_revision: %q", refused)
			}
			if _, refused := control(map[string]any{"action": "propose", "id": base.ID, "steps": []any{map[string]any{"id": "nope", "brief": "x"}}}); !strings.Contains(refused, `no step "nope"`) || !strings.Contains(refused, "replace: true") {
				t.Fatalf("unknown step: %q", refused)
			}
			// 1: fields propose does not take are refused, naming them.
			if _, refused := orchestrator(map[string]any{"action": "propose", "name": "Desc", "summary": "x", "revision": float64(1)}); !strings.Contains(refused, "propose does not take revision, summary") {
				t.Fatalf("unknown fields: %q", refused)
			}
			if after := proposals(); after != before {
				t.Fatalf("a refused propose saved a proposal: %d -> %d", before, after)
			}
			// 2: a new name for the workflow id names keeps its id.
			data = ok("propose rename", control, map[string]any{"action": "propose", "id": base.ID, "name": "Desc renamed", "expected_revision": float64(current.Revision)})
			accept(data["automation"].(map[string]any))
			if renamed, _ := m.store.Get(m.account, base.ID); renamed.Name != "Desc renamed" || renamed.Steps[0].Brief != "Review v2" {
				t.Fatalf("rename: %+v", renamed)
			}
			if _, err := m.store.FindByName(m.account, "Desc"); err == nil {
				t.Fatal("the old name still names a workflow")
			}
			// replace: true sends the whole definition.
			data = ok("propose replace", orchestrator, map[string]any{"action": "propose", "id": "Desc renamed", "replace": true, "steps": []any{map[string]any{"type": "agent", "worker": "reviewer", "brief": "Only"}}})
			replaced := data["automation"].(map[string]any)
			if data["replaced"] != true || replaced["name"] != "Desc renamed" || !strings.Contains(mustJSON(t, replaced["steps"]), `"brief":"Only"`) || replaced["description"] != "" {
				t.Fatalf("replace: %v", data)
			}

			// 5: history lists the pending proposal with its state.
			data = ok("history", control, map[string]any{"action": "history", "id": "Desc renamed"})
			pending, _ := data["pending_proposals"].([]any)
			if len(pending) != 1 || pending[0].(map[string]any)["state"] != "pending" || pending[0].(map[string]any)["id"] != replaced["id"] {
				t.Fatalf("history pending proposals: %v", data)
			}
			// 3: enable, disable, export by name, here and there.
			for _, call := range []struct {
				action, field string
				enabled       bool
			}{{"disable", "name", false}, {"enable", "id", true}} {
				data = ok(call.action, orchestrator, map[string]any{"action": call.action, call.field: "Desc renamed"})
				if got, _ := m.store.Get(m.account, base.ID); got.Enabled != call.enabled {
					t.Fatalf("%s by name: %+v", call.action, got)
				}
			}
			// 5: export carries the description and the scope (the project).
			exported := ok("export", control, map[string]any{"action": "export", "name": "Desc renamed"})
			if exported["project"] != m.projectName || exported["description"] != "Reviews every change" || strings.Contains(mustJSON(t, exported), m.folder) {
				t.Fatalf("export lacks the scope or description, or names a host path: %v", exported)
			}
			// An import without project binds the exported project's folder.
			data = ok("import copy", orchestrator, map[string]any{"action": "import", "definition": exported, "name": "Desc copy"})
			copied, err := m.store.FindByName(m.account, "Desc copy")
			if err != nil || copied.Scope.Folder != m.folder || copied.Description != "Reviews every change" || copied.ID == base.ID {
				t.Fatalf("import did not reproduce the scope: %+v %v", copied, err)
			}
			// A project this machine lacks is refused, asking for one.
			elsewhere := map[string]any{}
			for key, value := range exported {
				elsewhere[key] = value
			}
			elsewhere["project"] = "Project elsewhere"
			if _, refused := control(map[string]any{"action": "import", "definition": elsewhere, "name": "Desc lost"}); !strings.Contains(refused, `applies to project "Project elsewhere"`) || !strings.Contains(refused, "pass project") {
				t.Fatalf("an unknown project: %q", refused)
			}
			if _, err := m.store.FindByName(m.account, "Desc lost"); err == nil {
				t.Fatal("a refused import made a workflow")
			}
			// 4: place's refusals (a project the call names that this
			// machine lacks) name catalog tools, on import and propose,
			// through both surfaces (the final check below finds no
			// swarm_* name in them).
			for _, call := range []func(map[string]any) (map[string]any, string){orchestrator, control} {
				for _, args := range []map[string]any{
					{"action": "import", "definition": disabledDefinition("Lost import"), "project": "Project elsewhere"},
					{"action": "propose", "name": "Lost proposal", "project": "Project elsewhere", "steps": []any{map[string]any{"type": "agent", "worker": "reviewer", "brief": "x"}}},
				} {
					if _, refused := call(args); !strings.Contains(refused, `no project "Project elsewhere"`) || !strings.Contains(refused, "manage_projects create") {
						t.Fatalf("%s with an unknown project: %q", args["action"], refused)
					}
				}
			}
			// 4: a disabled import says it does not run until enabled.
			disabled := disabledDefinition("Off copy")
			data = ok("import disabled", control, map[string]any{"action": "import", "definition": disabled, "project": m.project})
			if next := fmt.Sprint(data["next"]); !strings.Contains(next, "does not run until it is enabled") || strings.Contains(next, "runs when") {
				t.Fatalf("disabled import next: %q", next)
			}

			// 3: delete and restore by name, through both surfaces.
			for i, call := range []func(map[string]any) (map[string]any, string){orchestrator, control} {
				name := []string{"Del-a", "Del-b"}[i]
				data = ok("delete", call, map[string]any{"action": "delete", "name": name})
				if _, err := m.store.FindByName(m.account, name); err == nil || data["name"] != name {
					t.Fatalf("delete by name %s: %v %v", name, data, err)
				}
				ok("restore", call, map[string]any{"action": "restore", "name": name, "revision": float64(1)})
				if _, err := m.store.FindByName(m.account, name); err != nil {
					t.Fatalf("restore by name %s: %v", name, err)
				}
			}
			// An ambiguous name is refused naming each match.
			twin := jobautomation.Automation{Name: "Twin", Scope: jobautomation.Scope{Folder: m.folder}, Enabled: true, Steps: parityWorkflowSteps("x")}
			first, err := m.store.Propose(m.account, "session-1", twin)
			if err != nil {
				t.Fatal(err)
			}
			second, err := m.store.Propose(m.account, "session-2", twin)
			if err != nil {
				t.Fatal(err)
			}
			for _, call := range []func(map[string]any) (map[string]any, string){orchestrator, control} {
				if _, refused := call(map[string]any{"action": "get", "name": "Twin"}); !strings.Contains(refused, `"Twin" names 2 workflows`) || !strings.Contains(refused, first.ID) || !strings.Contains(refused, second.ID) {
					t.Fatalf("ambiguous name: %q", refused)
				}
			}

			// 7: a run step's import is an envelope, refused, saving nothing.
			runStep := map[string]any{"swarm_workflow": float64(1), "name": "Host", "steps": []any{map[string]any{"type": "run", "command": "deliver/push.sh"}}}
			for _, call := range []func(map[string]any) (map[string]any, string){orchestrator, control} {
				envelope, refused := call(map[string]any{"action": "import", "definition": runStep, "project": m.project})
				data := must("run-step import", envelope, refused)
				if envelope["state"] != "refused" || !strings.Contains(fmt.Sprint(data["reason"]), "run step") || !strings.HasPrefix(fmt.Sprint(envelope["next_action"]), "Import it without the run step") {
					t.Fatalf("run-step import envelope: %v", envelope)
				}
			}
			if _, err := m.store.FindByName(m.account, "Host"); err == nil {
				t.Fatal("a refused import made a workflow")
			}
			// The owner's import route keeps its rule: 403 without admin.
			req := httptest.NewRequest(http.MethodPost, JobAutomationsPath+"/import", strings.NewReader(fmt.Sprintf(`{"definition":%s,"project":%q}`, mustJSON(t, runStep), m.project)))
			req = requestWithScopedToken(requestWithTestPrincipalForAccount(req, m.user, m.account), &pebblestore.ScopedTokenRecord{ID: "write-key", Scopes: []string{"automations:read", "automations:write", "projects:read"}})
			w := httptest.NewRecorder()
			m.server.handleJobAutomations(w, req)
			if w.Code != http.StatusForbidden {
				t.Fatalf("the owner's import route: %d %s", w.Code, w.Body)
			}

			// 4: no answer or refusal names a swarm_* tool.
			if len(answers) < 25 {
				t.Fatalf("only %d answers and refusals were checked", len(answers))
			}
			for _, answer := range answers {
				for _, name := range workflowLegacyToolName.FindAllString(answer, -1) {
					if name != "swarm_workflow" {
						t.Fatalf("an answer names %s: %s", name, answer)
					}
				}
			}
		})
	}
}

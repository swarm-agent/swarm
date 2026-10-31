package api

import (
	"regexp"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/tool"
)

// catalogRequiredNamed finds the inputs a refusal says a call needs, in the
// handlers' wordings: "X is required", "X and Y are required", "X required",
// "... requires X and Y", "... needs X". A list naming a choice ("X or Y is
// required", "requires title or prompt") names no required input.
var (
	catalogRequiredAfter  = regexp.MustCompile(`\b(?:requires|needs) `)
	catalogRequiredBefore = regexp.MustCompile(`(?: is| are)? required\b`)
	catalogRequiredIdent  = regexp.MustCompile(`^[a-z][a-z0-9_]*`)
	catalogRequiredLast   = regexp.MustCompile(`[a-z][a-z0-9_]*$`)
)

func catalogRequiredNamed(reason string) map[string]bool {
	named := map[string]bool{}
	add := func(list []string, choice bool) {
		if !choice {
			for _, name := range list {
				named[name] = true
			}
		}
	}
	for _, m := range catalogRequiredAfter.FindAllStringIndex(reason, -1) {
		rest, list, choice := reason[m[1]:], []string(nil), false
		for {
			name := catalogRequiredIdent.FindString(rest)
			if name == "" {
				break
			}
			list, rest = append(list, name), rest[len(name):]
			if strings.HasPrefix(rest, " or ") || strings.HasPrefix(rest, ", or ") {
				choice = true
				break
			}
			sep := ""
			for _, candidate := range []string{", and ", " and ", ", "} {
				if strings.HasPrefix(rest, candidate) {
					sep = candidate
					break
				}
			}
			if sep == "" {
				break
			}
			rest = rest[len(sep):]
		}
		add(list, choice)
	}
	for _, m := range catalogRequiredBefore.FindAllStringIndex(reason, -1) {
		rest, list, choice := reason[:m[0]], []string(nil), false
		for {
			name := catalogRequiredLast.FindString(rest)
			if name == "" {
				break
			}
			list, rest = append(list, name), rest[:len(rest)-len(name)]
			if strings.HasSuffix(rest, " or ") || strings.HasSuffix(rest, ", or ") {
				choice = true
				break
			}
			sep := ""
			for _, candidate := range []string{", and ", " and ", ", "} {
				if strings.HasSuffix(rest, candidate) {
					sep = candidate
					break
				}
			}
			if sep == "" {
				break
			}
			rest = rest[:len(rest)-len(sep)]
		}
		add(list, choice)
	}
	return named
}

// catalogRequiredConditional are inputs a handler refuses a call without only
// for some callers or calls, so they are not declared required. Each says why.
var catalogRequiredConditional = map[string]string{
	"manage_workers context worker_id":      "needed only by a caller with no conversation holding pending proposals",
	"manage_workers progress worker_id":     "needed only by a caller with no conversation holding pending proposals",
	"manage_workers review worker_id":       "needed only by a caller with no conversation holding pending proposals",
	"manage_workers request prompt":         "the Orchestrator's request needs a prompt; Swarm Control may send input instead",
	"manage_workflows propose name":         "a new workflow needs a name; a change to an existing one keeps its name",
	"manage_projects get id":                "the project is named by id or project_id",
	"manage_projects propose_task title":    "title or prompt here; a linked machine's propose route asks for a title",
	"manage_sessions create workspace_path": "the Orchestrator's session at home defaults to its own workspace; a Swarm Control caller (or a linked machine) must name one",
}

// catalogRequiredKey is the key a tool's probes call Swarm Control with: one
// that may call every action, so the required check, not a scope refusal,
// answers.
func catalogRequiredKey(toolName string) []string {
	if toolName == "manage_setup" {
		return catalogParitySetupKey
	}
	return catalogParityFullKey
}

// catalogRequiredProbeSkip are inputs a probe never removes: removing a
// timing input only makes the call wait longer, and removing a sign-in or
// update step starts the flow it names instead of reading its status.
var catalogRequiredProbeSkip = map[string]bool{"action": true, "wait_seconds": true, "max_seconds": true}

// catalogRequiredProbeSkipAction are actions whose inputs are never removed:
// without its step a sign-in starts, without its bound an update drain does.
var catalogRequiredProbeSkipAction = map[string]bool{"manage_setup connect_chatgpt": true, "manage_setup connect_claude_code": true, "manage_setup request_update": true, "manage_setup cancel_update": true}

func catalogWithout(args map[string]any, key string) map[string]any {
	out := make(map[string]any, len(args))
	for k, v := range args {
		if k != key {
			out[k] = v
		}
	}
	return out
}

// Purpose: every catalog action states the inputs it cannot run without, and
// both surfaces refuse a call missing one the same way, for every catalog
// tool, here and on a linked machine. For each action's parity case:
//   - with a declared required input left out, the Orchestrator refuses with
//     tool.CheckCatalogRequired's reason and Swarm Control with the failure
//     envelope carrying that reason;
//   - with any other input it takes left out, neither surface's handler
//     refuses with "X is required" for it: a handler that needs an input has
//     it declared (CatalogAction.Required), so a future action cannot skip it.
//
// Threat: the parity defect where actions needed inputs no description
// declared ("project_id is required" over MCP on a linked machine), and the
// review finding that only manage_projects declared them. Authority: the
// catalog declarations, Runtime.executeOne's and run.Service.gateToolCalls'
// required check (the Orchestrator) and controlMCPCall.callCatalogTool's
// (Swarm Control), over the parity harness's real handlers.
func TestCatalogRequiredInputsEveryAction(t *testing.T) {
	h := newCatalogParityHarness(t)
	all := catalogParityCases()
	declaredAny := 0
	for _, toolName := range catalogParityToolNames(all) {
		catalog, ok := tool.CatalogToolNamed(toolName)
		if !ok {
			t.Fatalf("%s is not a catalog tool", toolName)
		}
		for _, actionName := range catalogParityActionNames(all[toolName]) {
			action, ok := catalog.Action(actionName)
			if !ok {
				continue
			}
			name := toolName + " " + action.Action
			for _, c := range all[toolName][actionName] {
				c = h.resolveCase(t, c)
				where := name
				if machine, _ := c.orchestrator["machine"].(string); machine != "" {
					where += "@" + machine
				}
				// A declared required input left out: the same refusal.
				for _, input := range action.Required {
					declaredAny++
					orchArgs := catalogWithout(c.orchestrator, input)
					want := tool.CheckCatalogRequired(action, orchArgs)
					if want == nil {
						t.Fatalf("%s: CheckCatalogRequired accepted a call without %s", where, input)
					}
					if _, err := h.orchestratorTool(t, toolName, orchArgs); err == nil || err.Error() != want.Error() {
						t.Errorf("%s without %s: Orchestrator %v, want %q", where, input, err, want)
					}
					if action.ControlCallable() {
						if _, refused := h.controlTool(t, catalogRequiredKey(toolName), toolName, catalogWithout(c.control, input)); refused == "" || catalogFailureReason(t, refused) != want.Error() {
							t.Errorf("%s without %s: Swarm Control %q, want %q", where, input, refused, want)
						}
					}
				}
				// Any other input left out: no handler says it is required.
				if catalogRequiredProbeSkipAction[name] {
					continue
				}
				declared := map[string]bool{}
				for _, input := range action.Required {
					declared[input] = true
				}
				skip := func(key string) bool {
					if catalogRequiredProbeSkip[key] || declared[key] || catalogRequiredConditional[name+" "+key] != "" {
						return true
					}
					// A task action finds its project from the task id.
					return key == "project_id" && tool.TaskProjectFromTaskID(action)
				}
				for key := range c.orchestrator {
					if skip(key) {
						continue
					}
					if _, err := h.orchestratorTool(t, toolName, catalogWithout(c.orchestrator, key)); err != nil && catalogRequiredNamed(err.Error())[key] {
						t.Errorf("%s: the Orchestrator's handler refuses a call without %s (%q) but %s is not declared required", where, key, err, key)
					}
				}
				if !action.ControlCallable() {
					continue
				}
				for key := range c.control {
					if skip(key) {
						continue
					}
					if _, refused := h.controlTool(t, catalogRequiredKey(toolName), toolName, catalogWithout(c.control, key)); refused != "" && catalogRequiredNamed(catalogFailureReason(t, refused))[key] {
						t.Errorf("%s: Swarm Control refuses a call without %s (%q) but %s is not declared required", where, key, refused, key)
					}
				}
			}
		}
	}
	if declaredAny == 0 {
		t.Fatal("no declared required input was exercised")
	}
}

func TestCatalogRequiredNamed(t *testing.T) {
	for reason, want := range map[string]string{
		"task_id is required":                                        "task_id",
		"brief is required: what to do":                              "brief",
		"session_id or session_ids is required":                      "",
		"project_id and task_id are required":                        "project_id,task_id",
		"machine, project_id and task_id are required":               "machine,project_id,task_id",
		"machine is required; known machines: box":                   "machine",
		"project id is required for get":                             "id",
		"linked machine box refused: name is required (1)":           "name",
		"worker_id and expected_revision required":                   "expected_revision,worker_id",
		"set_model requires provider and model":                      "model,provider",
		"manage_designs edit needs edit_path, the .html file":        "edit_path",
		"propose_task requires title or prompt":                      "",
		"archive requires session_id, session_ids, category, or all": "",
		"manage_media requires a session with a project":             "",
	} {
		var got []string
		for _, key := range []string{"brief", "edit_path", "expected_revision", "id", "machine", "model", "name", "project", "project_id", "provider", "session", "session_id", "session_ids", "task_id", "title", "worker_id"} {
			if catalogRequiredNamed(reason)[key] {
				got = append(got, key)
			}
		}
		if strings.Join(got, ",") != want {
			t.Fatalf("%q named %v, want %s", reason, got, want)
		}
	}
}

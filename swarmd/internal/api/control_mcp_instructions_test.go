package api

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/tool"
)

// controlMCPGuidanceRoot is the repository root as seen from this package.
const controlMCPGuidanceRoot = "../../.."

// controlMCPGuidanceSwarmNames are the swarm_* identifiers the plugin may
// still name because they are not Swarm Control tools of this daemon, each
// with what it is. The server instructions name none.
var controlMCPGuidanceSwarmNames = map[string]string{
	"swarm_list_machines": "swarm-fleet's and the relay's own tool (packages/swarm-fleet, packages/swarm-relay)",
	"swarm_has_mcp":       "a shell function of plugins/swarm/bin/swarm-mcp",
	"swarm_name":          "the startup config and onboarding field",
}

// controlMCPGuidanceDocSwarmNames are the further swarm_* identifiers docs
// may name: none is a Swarm Control tool of this daemon.
var controlMCPGuidanceDocSwarmNames = map[string]string{
	"swarm_pair_machine":       "the relay's own tool (packages/swarm-relay)",
	"swarm_remove_machine":     "the relay's own tool (packages/swarm-relay)",
	"swarm_design":             "the Designer's and the Orchestrator's own agent tool, run inside their run (not Swarm Control's)",
	"swarm_id":                 "the notifications query field",
	"swarm_progress":           "the installer's progress line key",
	"swarm_children":           "a box limit name",
	"swarm_workflow":           "a workflow result field",
	"swarm_desktop_session":    "the Desktop session cookie",
	"swarm_require_go":         "a shell function of the browser smoke script",
	"swarm_targets_test":       "a Go source file name",
	"swarm_mode_settings":      "a Go source file name",
	"swarm_mode_settings_test": "a Go source file name",
}

// controlMCPGuidanceDocSwarmNameAllowed adds the docs' own identifiers.
func controlMCPGuidanceDocSwarmNameAllowed(name string) bool {
	_, ok := controlMCPGuidanceDocSwarmNames[name]
	return ok || controlMCPGuidanceSwarmNameAllowed(name)
}

// controlMCPGuidanceSwarmPrefixes are swarm_* name families that are not
// Swarm Control tools of this daemon: swarm-fleet's own tools.
var controlMCPGuidanceSwarmPrefixes = []string{"swarm_fleet_"}

// controlMCPToolCatalogHistory are the retired swarm_* tool names that
// docs/tool-catalog.md keeps as the record of the move (its mapping and rename
// tables). No other file may name them.
var controlMCPToolCatalogHistory = map[string]bool{
	"swarm_accept_review": true, "swarm_activity": true, "swarm_add_workspace": true, "swarm_agents_at_work": true,
	"swarm_answer_job": true, "swarm_approve_task": true, "swarm_assign_worker_task": true, "swarm_connect_chatgpt": true,
	"swarm_connect_claude_code": true, "swarm_create_key": true, "swarm_create_workspace": true, "swarm_define_agent": true,
	"swarm_deliver_job": true, "swarm_deploy_worker": true, "swarm_design": true, "swarm_get_changes": true,
	"swarm_get_environment": true, "swarm_get_job": true, "swarm_get_session": true, "swarm_get_theme": true,
	"swarm_get_worker": true, "swarm_give_job": true, "swarm_integrate_task": true, "swarm_list_agents": true,
	"swarm_list_runs": true, "swarm_list_workflows": true, "swarm_list_workspaces": true, "swarm_manage_worker": true,
	"swarm_media_models": true, "swarm_message_job": true, "swarm_read_file": true, "swarm_read_media": true,
	"swarm_request_media": true, "swarm_resolve_permission": true, "swarm_restore_workflow": true, "swarm_run_plan": true,
	"swarm_run_task": true, "swarm_save_media": true, "swarm_schedule_job": true, "swarm_send_message": true,
	"swarm_set_session_access": true, "swarm_set_session_model": true, "swarm_start_session": true, "swarm_status": true,
	"swarm_stop_job": true, "swarm_stop_run": true, "swarm_task_result": true, "swarm_workflow_history": true,
	"swarm_create_worker": true,
}

var (
	controlMCPGuidanceSwarmName = regexp.MustCompile(`\bswarm_[a-z_]+\b`)
	controlMCPGuidanceCatalog   = regexp.MustCompile(`\bmanage_[a-z_]+\b`)
	// "manage_projects action propose_task", "`manage_projects` with
	// `action: propose_task`", "manage_jobs actions give_job" and
	// "`manage_jobs` `give_job`".
	controlMCPGuidanceAction = regexp.MustCompile("\\b(manage_[a-z_]+)`?\\s+(?:with\\s+)?`?actions?:?\\s+`?\"?([a-z_]+)")
	controlMCPGuidancePair   = regexp.MustCompile("`(manage_[a-z_]+)`\\s+`([a-z_]+)`")
)

// controlMCPGuidanceSwarmNameAllowed reports a swarm_* identifier that is
// not a Swarm Control tool of this daemon.
func controlMCPGuidanceSwarmNameAllowed(name string) bool {
	if _, ok := controlMCPGuidanceSwarmNames[name]; ok {
		return true
	}
	for _, prefix := range controlMCPGuidanceSwarmPrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// controlMCPServedCatalog is the catalog's MCP tool list: each catalog tool
// with the actions Swarm Control serves (tools/list with tools=all).
func controlMCPServedCatalog() map[string]map[string]bool {
	served := map[string]map[string]bool{}
	for _, catalog := range tool.Catalog() {
		for _, action := range catalog.Actions {
			if !action.ControlCallable() {
				continue
			}
			if served[catalog.Name] == nil {
				served[catalog.Name] = map[string]bool{}
			}
			served[catalog.Name][action.Action] = true
		}
	}
	return served
}

// controlMCPGuidanceProblems lists every tool name in text that is not in
// the catalog's MCP tool list: a swarm_* name (unless allowed), a manage_*
// word that is not a served catalog tool, or a catalog tool named with an
// action Swarm Control does not serve.
func controlMCPGuidanceProblems(where, text string, served map[string]map[string]bool, allowed func(string) bool, catalogNames bool) []string {
	var problems []string
	for _, name := range controlMCPGuidanceSwarmName.FindAllString(text, -1) {
		if !allowed(name) {
			problems = append(problems, where+": names "+name+", which is not a catalog tool")
		}
	}
	if !catalogNames {
		return problems
	}
	for _, name := range controlMCPGuidanceCatalog.FindAllString(text, -1) {
		if served[name] == nil {
			problems = append(problems, where+": names "+name+", which is not in the catalog's MCP tool list")
		}
	}
	for _, re := range []*regexp.Regexp{controlMCPGuidanceAction, controlMCPGuidancePair} {
		for _, match := range re.FindAllStringSubmatch(text, -1) {
			if actions := served[match[1]]; actions != nil && !actions[match[2]] {
				problems = append(problems, where+": names "+match[1]+" action "+match[2]+", which Swarm Control does not serve")
			}
		}
	}
	return problems
}

// Purpose:
//   - Requirement: everything that tells an AI which Swarm Control tool to
//     call (the plugin's skills, references, evals and README, the server
//     instructions sent at initialize, and the docs) names catalog tools and
//     actions that Swarm Control serves, never a swarm_* tool. Only
//     docs/tool-catalog.md keeps the retired names, as the record of the move.
//   - Threat/regression: deleting the swarm_* tools (or renaming a catalog
//     action) while a skill, a doc or the initialize text still tells the AI
//     to call the old name, which then fails at call time; or guidance naming
//     an action Swarm Control never serves (a task-card step the person takes
//     in their app).
//   - Boundary/authority: tool.Catalog and CatalogAction.ControlCallable (the
//     catalog's MCP tool list, as tools/list serves it) against
//     controlMCPCoreInstructionLines, controlMCPInstructionLines and the
//     checked-in plugins/swarm and docs trees.
//   - Narrowest layer: the instruction lines are package data and the
//     guidance is checked-in text; no daemon needs to run. Every instruction
//     line is checked whatever key would see it.
func TestGuidanceNamesOnlyCatalogTools(t *testing.T) {
	served := controlMCPServedCatalog()
	if len(served) == 0 {
		t.Fatal("the catalog serves no tool to Swarm Control")
	}
	var problems []string

	// The server instructions name no swarm_* identifier at all.
	none := func(string) bool { return false }
	for name, set := range map[string][]controlMCPInstruction{"core": controlMCPCoreInstructionLines, "tools=all": controlMCPInstructionLines} {
		for i, line := range set {
			problems = append(problems, controlMCPGuidanceProblems(fmt.Sprintf("server instruction %s #%d", name, i), line.text, served, none, true)...)
		}
	}

	// The plugin: every file under plugins/swarm.
	plugin := filepath.Join(controlMCPGuidanceRoot, "plugins", "swarm")
	files := 0
	err := filepath.WalkDir(plugin, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files++
		rel, _ := filepath.Rel(controlMCPGuidanceRoot, path)
		problems = append(problems, controlMCPGuidanceProblems(filepath.ToSlash(rel), string(raw), served, controlMCPGuidanceSwarmNameAllowed, true)...)
		return nil
	})
	if err != nil {
		t.Fatalf("read %s: %v", plugin, err)
	}
	if files == 0 {
		t.Fatalf("no plugin files under %s", plugin)
	}

	// The docs: no swarm_* tool name, except docs/tool-catalog.md's record.
	// Docs describe Orchestrator-only actions too, so only swarm_* names are
	// checked there.
	docs := filepath.Join(controlMCPGuidanceRoot, "docs")
	catalogDoc := ""
	err = filepath.WalkDir(docs, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".md") {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(controlMCPGuidanceRoot, path)
		rel = filepath.ToSlash(rel)
		allowed := controlMCPGuidanceDocSwarmNameAllowed
		if rel == "docs/tool-catalog.md" {
			catalogDoc = string(raw)
			allowed = func(name string) bool {
				return controlMCPToolCatalogHistory[name] || controlMCPGuidanceDocSwarmNameAllowed(name)
			}
		}
		problems = append(problems, controlMCPGuidanceProblems(rel, string(raw), served, allowed, false)...)
		return nil
	})
	if err != nil {
		t.Fatalf("read %s: %v", docs, err)
	}
	if catalogDoc == "" {
		t.Fatal("docs/tool-catalog.md is missing; it keeps the record of the swarm_* tools")
	}

	sort.Strings(problems)
	if len(problems) > 0 {
		t.Fatalf("guidance names tools outside the catalog's MCP tool list:\n%s", strings.Join(problems, "\n"))
	}
}

// Purpose:
//   - Requirement: the guidance check itself catches what it guards, so a
//     passing TestGuidanceNamesOnlyCatalogTools means something.
//   - Threat/regression: a pattern that silently stops matching (a swarm_*
//     name, an unknown manage_* tool, an action Swarm Control does not serve)
//     and lets a stale name through.
//   - Boundary/authority: controlMCPGuidanceProblems over tool.Catalog.
//   - Narrowest layer: the checker on fixed strings.
func TestGuidanceCheckCatchesStaleNames(t *testing.T) {
	served := controlMCPServedCatalog()
	none := func(string) bool { return false }
	for _, tc := range []struct {
		text string
		bad  bool
	}{
		{"Call swarm_run_task with a role.", true},
		{"Call `manage_projects` with `action: propose_task`.", false},
		{"Use manage_projects action propose_task with an agent.", false},
		{"Use `manage_jobs` `give_job`.", false},
		{"Use manage_projects action approve_task.", true}, // declared, not served to Swarm Control
		{"Use `manage_projects` `no_such_action`.", true},  // not declared
		{"Use manage_widgets action list.", true},          // not a catalog tool
		{"Use manage_sessions actions create and send_message.", false},
	} {
		got := len(controlMCPGuidanceProblems("fixture", tc.text, served, none, true)) > 0
		if got != tc.bad {
			t.Errorf("%q: flagged = %v, want %v", tc.text, got, tc.bad)
		}
	}
	if !controlMCPGuidanceSwarmNameAllowed("swarm_fleet_status") || controlMCPGuidanceSwarmNameAllowed("swarm_status") {
		t.Fatal("swarm_* allowance is wrong: swarm-fleet's own tools are allowed, swarmd's swarm_* tools are not")
	}
	// No retired Swarm Control tool is allowed outside docs/tool-catalog.md;
	// swarm_design is also the agents' own tool, which only docs name.
	for name := range controlMCPToolCatalogHistory {
		if controlMCPGuidanceSwarmNameAllowed(name) || (controlMCPGuidanceDocSwarmNameAllowed(name) && name != "swarm_design") {
			t.Fatalf("%s is a retired Swarm Control tool but is allowed outside docs/tool-catalog.md", name)
		}
	}
}

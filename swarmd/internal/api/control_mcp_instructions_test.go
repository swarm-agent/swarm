package api

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
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
			// A gate naming an action Swarm Control does not serve hides
			// the line from every caller.
			for _, gate := range line.tools {
				toolName, actionName, _ := strings.Cut(strings.TrimPrefix(gate, "!"), " ")
				if actions := served[toolName]; actions == nil || (actionName != "" && !actions[actionName]) {
					problems = append(problems, fmt.Sprintf("server instruction %s #%d: gated on %q, which is not in the catalog's MCP tool list", name, i, gate))
				}
			}
		}
	}

	// Only tracked files are guidance: docs/ also holds ignored historical
	// and scratch notes that a local checkout may have.
	tracked, source, err := controlMCPGuidanceTrackedFiles(controlMCPGuidanceRoot, "plugins/swarm", "docs")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("guidance read from %s (%d files)", source, len(tracked))

	// The plugin: every tracked file under plugins/swarm.
	files := 0
	for rel, raw := range tracked {
		if !strings.HasPrefix(rel, "plugins/swarm/") {
			continue
		}
		files++
		problems = append(problems, controlMCPGuidanceProblems(rel, raw, served, controlMCPGuidanceSwarmNameAllowed, true)...)
	}
	if files == 0 {
		t.Fatal("no tracked plugin files under plugins/swarm")
	}

	// The docs: no swarm_* tool name, except docs/tool-catalog.md's record.
	// Docs describe Orchestrator-only actions too, so only swarm_* names are
	// checked there.
	catalogDoc := ""
	for rel, raw := range tracked {
		if !strings.HasPrefix(rel, "docs/") || !strings.HasSuffix(rel, ".md") {
			continue
		}
		allowed := controlMCPGuidanceDocSwarmNameAllowed
		if rel == "docs/tool-catalog.md" {
			catalogDoc = raw
			allowed = func(name string) bool {
				return controlMCPToolCatalogHistory[name] || controlMCPGuidanceDocSwarmNameAllowed(name)
			}
		}
		problems = append(problems, controlMCPGuidanceProblems(rel, raw, served, allowed, false)...)
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

const (
	controlMCPGuidanceFromIndex = "the Git index"
	controlMCPGuidanceFromDisk  = "the files on disk (the checkout has no Git metadata Git can open)"
)

// controlMCPGuidanceGitEnv is the environment for reading root's own index:
// a Git hook exports repository-routing variables (GIT_DIR and others) that
// would point git -C root at another repository or index.
func controlMCPGuidanceGitEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		switch strings.SplitN(kv, "=", 2)[0] {
		case "GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_NAMESPACE", "GIT_PREFIX":
			continue
		}
		env = append(env, kv)
	}
	return env
}

// controlMCPGuidanceTrackedFiles reads the guidance files under dirs of the
// checkout at root, keyed by their slash path relative to root, and names
// where the list came from.
//
// When Git can open the checkout's repository, the list is the files Git
// tracks: ignored and untracked files are not read, and a tracked file
// deleted from the working tree is left out. Ignore rules alone cannot
// stand in for the index, as tracked docs are force-added past /docs/*.
//
// A checkout whose Git metadata cannot be opened (git exits 128: no .git,
// or a linked checkout whose metadata is not mounted, as in a sandboxed or
// exported checkout of a commit) has no index to consult; then every file
// on disk under dirs is read, and the caller logs that mode. Any other
// failure is an error.
func controlMCPGuidanceTrackedFiles(root string, dirs ...string) (map[string]string, string, error) {
	cmd := exec.Command("git", append([]string{"-C", root, "ls-files", "-z", "--"}, dirs...)...)
	cmd.Env = controlMCPGuidanceGitEnv()
	out, err := cmd.Output()
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 128 {
		files, err := controlMCPGuidanceDiskFiles(root, dirs...)
		return files, controlMCPGuidanceFromDisk, err
	}
	if err != nil {
		return nil, "", fmt.Errorf("list tracked files under %v in %s: %w", dirs, root, err)
	}
	files := map[string]string{}
	for _, rel := range strings.Split(string(out), "\x00") {
		if rel == "" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, "", fmt.Errorf("read tracked %s: %w", rel, err)
		}
		files[rel] = string(raw)
	}
	return files, controlMCPGuidanceFromIndex, nil
}

// controlMCPGuidanceDiskFiles reads every regular file under dirs of root.
// A missing dir contributes nothing; the caller requires the files it needs.
func controlMCPGuidanceDiskFiles(root string, dirs ...string) (map[string]string, error) {
	files := map[string]string{}
	for _, dir := range dirs {
		err := filepath.WalkDir(filepath.Join(root, filepath.FromSlash(dir)), func(path string, entry fs.DirEntry, err error) error {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			if err != nil || !entry.Type().IsRegular() {
				return err
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			files[filepath.ToSlash(rel)] = string(raw)
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("read %s in %s: %w", dir, root, err)
		}
	}
	return files, nil
}

// Purpose:
//   - Requirement: TestGuidanceNamesOnlyCatalogTools judges only checked-in
//     guidance; docs/ is ignored by default (.gitignore /docs/*) and holds
//     ignored historical and scratch notes in ordinary checkouts.
//   - Threat/regression: a local ignored or untracked note that names a
//     swarm_* tool fails the guard in one checkout and passes in a clean one
//     (the test depends on untracked local files); or the listing silently
//     drops tracked guidance.
//     A checkout with no Git metadata Git can open (a sandboxed or exported
//     checkout, where the gate runs) must still be checked, not fail or
//     check nothing; a GIT_DIR exported by a hook must not redirect the
//     listing to another repository.
//   - Boundary/authority: controlMCPGuidanceTrackedFiles over git ls-files,
//     and controlMCPGuidanceDiskFiles when there is no index.
//   - Narrowest layer: a throwaway Git repository with one tracked, one
//     ignored and one untracked file, then the same tree without usable
//     Git metadata.
func TestGuidanceTrackedFilesSkipIgnoredFiles(t *testing.T) {
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		cmd.Env = append(controlMCPGuidanceGitEnv(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(rel, text string) {
		t.Helper()
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-q")
	write(".gitignore", "/docs/*\n!/docs/kept.md\n")
	write("docs/kept.md", "Use `manage_projects` `propose_task`.\n")
	write("plugins/swarm/skill.md", "Use `manage_jobs` `give_job`.\n")
	git("add", "-A")
	// Written after the add: an ignored note and an untracked plugin file,
	// both naming a retired tool.
	write("docs/scratch.md", "Call swarm_run_task.\n")
	write("plugins/swarm/draft.md", "Call swarm_run_task.\n")

	list := func(root string) ([]string, map[string]string, string) {
		t.Helper()
		files, source, err := controlMCPGuidanceTrackedFiles(root, "plugins/swarm", "docs")
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for rel := range files {
			got = append(got, rel)
		}
		sort.Strings(got)
		return got, files, source
	}
	trackedWant := "docs/kept.md,plugins/swarm/skill.md"

	got, files, source := list(root)
	if source != controlMCPGuidanceFromIndex || strings.Join(got, ",") != trackedWant {
		t.Fatalf("files = %v from %s, want %s from the index (ignored and untracked files must be skipped)", got, source, trackedWant)
	}
	if files["docs/kept.md"] != "Use `manage_projects` `propose_task`.\n" {
		t.Fatalf("docs/kept.md content = %q", files["docs/kept.md"])
	}
	// The skipped files exist and would be flagged if read.
	for _, rel := range []string{"docs/scratch.md", "plugins/swarm/draft.md"} {
		raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		if len(controlMCPGuidanceProblems(rel, string(raw), controlMCPServedCatalog(), controlMCPGuidanceDocSwarmNameAllowed, false)) == 0 {
			t.Fatalf("%s would not be flagged; the fixture proves nothing", rel)
		}
	}

	// A hook's GIT_DIR naming another repository does not redirect the
	// listing: root's own index is still read.
	other := t.TempDir()
	initOther := exec.Command("git", "init", "-q", other)
	initOther.Env = controlMCPGuidanceGitEnv()
	if out, err := initOther.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	t.Setenv("GIT_DIR", filepath.Join(other, ".git"))
	if got, _, source := list(root); source != controlMCPGuidanceFromIndex || strings.Join(got, ",") != trackedWant {
		t.Fatalf("with a hook's GIT_DIR: files = %v from %s, want %s from root's index", got, source, trackedWant)
	}
	os.Unsetenv("GIT_DIR")

	// Without Git metadata Git can open, every file on disk is read: the
	// guard still checks the tree instead of failing or checking nothing.
	// First a linked checkout whose metadata is not there (a .git file
	// naming a missing gitdir), then no .git at all.
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(root))
	if err := os.RemoveAll(filepath.Join(root, ".git")); err != nil {
		t.Fatal(err)
	}
	write(".git", "gitdir: "+filepath.Join(other, "missing")+"\n")
	diskWant := "docs/kept.md,docs/scratch.md,plugins/swarm/draft.md,plugins/swarm/skill.md"
	for _, layout := range []string{"unmounted linked metadata", "no .git"} {
		if layout == "no .git" {
			if err := os.Remove(filepath.Join(root, ".git")); err != nil {
				t.Fatal(err)
			}
		}
		got, files, source := list(root)
		if source != controlMCPGuidanceFromDisk || strings.Join(got, ",") != diskWant {
			t.Fatalf("%s: files = %v from %s, want %s from disk", layout, got, source, diskWant)
		}
		if files["docs/scratch.md"] != "Call swarm_run_task.\n" {
			t.Fatalf("%s: docs/scratch.md content = %q", layout, files["docs/scratch.md"])
		}
	}
}

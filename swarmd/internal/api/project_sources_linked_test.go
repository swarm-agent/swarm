package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/permission"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	"swarm/packages/swarmd/internal/workspace"
)

// linkedSourceFixture is a home linked to "box", whose account has a
// registered Git source in project "remote-project" holding a file only the
// box has.
type linkedSourceFixture struct {
	h          *remoteWaitHarness
	folder     string
	workspace  string
	generation int64
	head       string
}

const linkedSourceMarker = "box-only-source-marker"

func newLinkedSourceFixture(t *testing.T) linkedSourceFixture {
	t.Helper()
	h := newRemoteWaitHarness(t)
	h.remote.s.workspace = workspace.NewService(pebblestore.NewWorkspaceStore(h.remote.s.sessions.Store().Underlying()))
	// The box runs inspection operations with its own tool runtime, as the
	// daemon wires it (runtime.daemon: SetToolCatalogRuntime).
	boxRuntime := tool.NewRuntime(1)
	t.Cleanup(func() { _ = boxRuntime.Close() })
	h.remote.s.SetToolCatalogRuntime(boxRuntime)
	folder := t.TempDir()
	outboxPinGit(t, folder, "init", "-q", "-b", "dev")
	if err := os.WriteFile(filepath.Join(folder, "notes.txt"), []byte(linkedSourceMarker+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	outboxPinGit(t, folder, "add", "notes.txt")
	outboxPinGit(t, folder, "commit", "-q", "-m", "start")
	head := strings.TrimSpace(outboxPinGit(t, folder, "rev-parse", "HEAD"))
	added, err := h.remote.s.workspace.AddForPrincipal(identity.Principal{Type: identity.PrincipalTypeUser, UserID: h.remote.user, AccountScopeID: h.remote.account}, folder, "project", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.remote.s.sessions.Store().PutProject(h.remote.account, &pebblestore.ProjectRecord{ID: "remote-project", Name: "Remote project", Workspaces: []pebblestore.ProjectWorkspaceRef{{WorkspaceID: added.WorkspaceID, Path: folder, Role: "primary_code"}}}); err != nil {
		t.Fatal(err)
	}
	return linkedSourceFixture{h: h, folder: folder, workspace: added.WorkspaceID, generation: added.WorkspaceGeneration, head: head}
}

// Purpose:
//   - Requirement: manage_projects list_sources, inspect_source and
//     inspect_files with machine=<linked machine> read that machine's own
//     registered sources through its project source routes, with the same
//     source authority, generation check, read-only operations and
//     workspace-root confinement as on the home, and the answer keeps the
//     machine and the exact target.reference for further reads.
//   - Threat/regression: the actions denied before dispatch on a linked
//     machine (the reported defect); a linked read silently substituting the
//     home's own checkout or another source; a stale generation, an
//     unregistered path, an unknown task result, a mutation operation or a
//     path outside the source root being read; a peer whose linked work is
//     off or that is not linked being reached.
//   - Boundary/authority: the native manage_projects runtime
//     (executeFleetProjects), Server.CallFleetProject (fleetSourceRequest,
//     verifyFleetSourceAnswer), the box's real link handler and project
//     source routes (handleProjectSources, InspectProjectSources,
//     ResolveProjectInspectionForPrincipal) and tool.Runtime.RunProjectInspection.
//   - Narrowest layer: two in-process daemons over the real pinned link; no
//     provider runs.
func TestLinkedProjectSourceInspection(t *testing.T) {
	f := newLinkedSourceFixture(t)
	h := f.h
	call := func(args map[string]any) (map[string]any, error) {
		t.Helper()
		return linkedTaskCall(t, h, remoteWaitGoal, args)
	}
	// The home has no such project or source: nothing can come from its own
	// checkout.
	if _, found, _ := h.home.s.sessions.Store().GetProject(h.home.account, "remote-project"); found {
		t.Fatal("fixture: the home holds the remote project")
	}

	listed, err := call(map[string]any{"action": "list_sources", "machine": "box", "project_id": "remote-project"})
	if err != nil {
		t.Fatalf("linked list_sources: %v", err)
	}
	var sources struct {
		Sources []pebblestore.ProjectTaskSource `json:"sources"`
	}
	raw, _ := json.Marshal(envelopeDataMap(listed))
	if err := json.Unmarshal(raw, &sources); err != nil || listed["machine"] != "box" || listed["untrusted_content"] != true || len(sources.Sources) != 1 || sources.Sources[0].Path != f.folder || sources.Sources[0].WorkspaceID != f.workspace || sources.Sources[0].WorkspaceGeneration != f.generation {
		t.Fatalf("linked list_sources answer: %v %v", listed, err)
	}

	inspected, err := call(map[string]any{"action": "inspect_source", "machine": "box", "project_id": "remote-project", "workspace_path": f.folder, "workspace_generation": float64(f.generation)})
	if err != nil {
		t.Fatalf("linked inspect_source: %v", err)
	}
	data := envelopeDataMap(inspected)
	repo, _ := data["repository"].(map[string]any)
	if inspected["machine"] != "box" || data["ready"] != true || data["clean"] != true || data["branch"] != "dev" || repo == nil || repo["head_commit"] != f.head {
		t.Fatalf("linked inspect_source answer: %v", inspected)
	}

	read, err := call(map[string]any{"action": "inspect_files", "machine": "box", "project_id": "remote-project", "workspace_id": f.workspace, "inspection": map[string]any{"tool": "read", "arguments": map[string]any{"path": "notes.txt"}}})
	if err != nil {
		t.Fatalf("linked inspect_files: %v", err)
	}
	data = envelopeDataMap(read)
	target, _ := data["target"].(map[string]any)
	reference, _ := target["reference"].(map[string]any)
	encoded, _ := json.Marshal(data["inspection"])
	if read["machine"] != "box" || reference["project_id"] != "remote-project" || reference["workspace_path"] != f.folder || reference["workspace_id"] != f.workspace || reference["workspace_generation"] != float64(f.generation) || !strings.Contains(string(encoded), linkedSourceMarker) {
		t.Fatalf("linked inspect_files answer: %v", read)
	}

	refusals := []struct {
		name string
		args map[string]any
		want string
	}{
		{"stale generation", map[string]any{"action": "inspect_files", "machine": "box", "project_id": "remote-project", "workspace_path": f.folder, "workspace_generation": float64(f.generation + 7)}, "stale"},
		{"stale source generation", map[string]any{"action": "inspect_source", "machine": "box", "project_id": "remote-project", "workspace_path": f.folder, "workspace_generation": float64(f.generation + 7)}, "stale"},
		{"unregistered path", map[string]any{"action": "inspect_source", "machine": "box", "project_id": "remote-project", "workspace_path": t.TempDir()}, "refused inspect_source"},
		{"unknown project", map[string]any{"action": "list_sources", "machine": "box", "project_id": "no-such-project"}, "not found"},
		{"unknown task result", map[string]any{"action": "inspect_files", "machine": "box", "project_id": "remote-project", "task_id": "no-task", "attempt_id": "a", "source_session_id": "s"}, "unavailable"},
		{"task without attempt", map[string]any{"action": "inspect_files", "machine": "box", "project_id": "remote-project", "task_id": "no-task"}, "attempt_id"},
		{"mutation operation", map[string]any{"action": "inspect_files", "machine": "box", "project_id": "remote-project", "workspace_path": f.folder, "inspection": map[string]any{"tool": "bash", "arguments": map[string]any{"command": "touch x"}}}, "read, list, search or find"},
		{"outside the source root", map[string]any{"action": "inspect_files", "machine": "box", "project_id": "remote-project", "workspace_path": f.folder, "inspection": map[string]any{"tool": "read", "arguments": map[string]any{"path": "../outside.txt"}}}, "escapes workspace scope"},
		{"unlinked peer", map[string]any{"action": "list_sources", "machine": "nobox", "project_id": "remote-project"}, "no linked machine"},
	}
	for _, refusal := range refusals {
		out, err := call(refusal.args)
		if err == nil {
			t.Fatalf("%s: answered %v", refusal.name, out)
		}
		if refusal.want != "" && !strings.Contains(err.Error(), refusal.want) {
			t.Fatalf("%s: %v", refusal.name, err)
		}
		t.Logf("%s refused: %v", refusal.name, err)
	}
	if entries, err := os.ReadDir(f.folder); err != nil || len(entries) != 2 {
		t.Fatalf("the box's source changed: %v %v", entries, err)
	}

	// Linked work off: refused before anything reaches the box.
	fleetActivityTestSetWork(t, h.home, "box", pebblestore.RuleOff)
	if out, err := call(map[string]any{"action": "list_sources", "machine": "box", "project_id": "remote-project"}); err == nil || !strings.Contains(err.Error(), "off") {
		t.Fatalf("linked work off: %v %v", out, err)
	}
}

// Purpose:
//   - Requirement: the home never takes a linked source answer for another
//     identity than it asked for: inspect_source must name the selected
//     source, and inspect_files must keep the requested project, source and,
//     for a committed task result, the exact task, attempt, session and a
//     head_commit (the requested one when given).
//   - Threat/regression: a linked machine (stale, buggy or hostile) answering
//     a pinned committed-result read with its current branch, another
//     source, or another head, which the caller would take as the result.
//   - Boundary/authority: verifyFleetSourceAnswer, the check CallFleetProject
//     runs on every linked inspect_source and inspect_files answer.
//   - Narrowest layer: the check itself over crafted answers; the wire path
//     that calls it is TestLinkedProjectSourceInspection's.
func TestVerifyFleetSourceAnswerRefusesAnotherIdentity(t *testing.T) {
	task := map[string]any{"project_id": "p", "task_id": "t", "attempt_id": "a", "source_session_id": "s", "head_commit": "h1"}
	answer := func(reference map[string]any) json.RawMessage {
		raw, _ := json.Marshal(map[string]any{"target": map[string]any{"reference": reference, "workspace_path": "/root"}})
		return raw
	}
	exact := map[string]any{"project_id": "p", "workspace_path": "/src", "workspace_id": "w", "workspace_generation": 2, "task_id": "t", "attempt_id": "a", "source_session_id": "s", "head_commit": "h1"}
	if err := verifyFleetSourceAnswer("box", "inspect_files", task, answer(exact)); err != nil {
		t.Fatalf("exact task result refused: %v", err)
	}
	with := func(changes map[string]any) map[string]any {
		out := map[string]any{}
		for key, value := range exact {
			out[key] = value
		}
		for key, value := range changes {
			if value == nil {
				delete(out, key)
			} else {
				out[key] = value
			}
		}
		return out
	}
	for name, reference := range map[string]map[string]any{
		"current source instead of the task result": with(map[string]any{"task_id": nil, "attempt_id": nil, "source_session_id": nil, "head_commit": nil}),
		"another head":      with(map[string]any{"head_commit": "h2"}),
		"no head":           with(map[string]any{"head_commit": nil}),
		"another attempt":   with(map[string]any{"attempt_id": "a2"}),
		"another session":   with(map[string]any{"source_session_id": "s2"}),
		"another project":   with(map[string]any{"project_id": "p2"}),
		"no source binding": with(map[string]any{"workspace_id": nil}),
	} {
		if err := verifyFleetSourceAnswer("box", "inspect_files", task, answer(reference)); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
	if err := verifyFleetSourceAnswer("box", "inspect_files", task, json.RawMessage(`{"inspection":{}}`)); err == nil {
		t.Fatal("an answer without its target was accepted")
	}
	source := map[string]any{"project_id": "p", "workspace_path": "/src", "workspace_generation": float64(2)}
	if err := verifyFleetSourceAnswer("box", "inspect_files", source, answer(with(map[string]any{"task_id": nil, "attempt_id": nil, "source_session_id": nil, "head_commit": nil}))); err != nil {
		t.Fatalf("exact source refused: %v", err)
	}
	if err := verifyFleetSourceAnswer("box", "inspect_files", source, answer(with(map[string]any{"task_id": nil, "attempt_id": nil, "source_session_id": nil, "head_commit": nil, "workspace_path": "/other"}))); err == nil {
		t.Fatal("another source accepted for inspect_files")
	}
	selected := map[string]any{"project_id": "p", "workspace_path": "/src", "workspace_id": "w", "workspace_generation": float64(2)}
	if err := verifyFleetSourceAnswer("box", "inspect_source", selected, json.RawMessage(`{"source":{"path":"/src","workspace_id":"w","workspace_generation":2}}`)); err != nil {
		t.Fatalf("selected source refused: %v", err)
	}
	for name, raw := range map[string]string{
		"another path":       `{"source":{"path":"/other","workspace_id":"w","workspace_generation":2}}`,
		"another workspace":  `{"source":{"path":"/src","workspace_id":"w2","workspace_generation":2}}`,
		"another generation": `{"source":{"path":"/src","workspace_id":"w","workspace_generation":3}}`,
		"no source":          `{"ready":true}`,
	} {
		if err := verifyFleetSourceAnswer("box", "inspect_source", selected, json.RawMessage(raw)); err == nil {
			t.Fatalf("inspect_source %s: accepted", name)
		}
	}
}

// Purpose:
//   - Requirement: the source discovery and inspection actions are one set
//     on every surface: the catalog (machine reach any, read level, machine
//     input), the linked dispatch and the permission gate (they reach a
//     linked machine), the read-only permission classification, the linked
//     forwarder's route (a project source route bound in mcpRouteSpecs), the
//     home proxy's refusal of that route, the MCP home-action list (no
//     home-only entry), and the TypeScript SDK, which must not call the
//     routes: they answer only a linked home over the link, so a client
//     method could only ever be refused.
//   - Threat/regression: a surface adding, dropping or reclassifying one of
//     them, so a linked call is refused before dispatch (the reported
//     defect), treated as work, forwarded where no route serves it, reachable
//     through the home's raw proxy, or advertised to SDK clients as if they
//     could call it.
//   - Boundary/authority: tool.FleetSourceInspectionActions, tool.Catalog,
//     tool.FleetProjectActionAllowed, tool.FleetWorkActionAllowed,
//     tool.FleetTaskStart, permission.IsManageProjectsReadOnlyAction,
//     fleetSourceRequest, fleetSourceProxyRefused, mcpRouteSpecs,
//     mcpHomeActions and the SDK's projects namespace.
//   - Narrowest layer: the declarations and dispatch functions; no daemon
//     runs.
func TestProjectSourceActionsMatchAcrossSurfaces(t *testing.T) {
	catalog, _ := tool.CatalogToolNamed("manage_projects")
	var declared []string
	for _, action := range catalog.Actions {
		if action.Action == "list_sources" || action.Action == "inspect_source" || action.Action == "inspect_files" {
			declared = append(declared, action.Action)
		}
	}
	slices.Sort(declared)
	served := append([]string(nil), tool.FleetSourceInspectionActions...)
	slices.Sort(served)
	if !slices.Equal(declared, served) {
		t.Fatalf("catalog source actions %v, linked source actions %v", declared, served)
	}
	sdk, err := os.ReadFile(filepath.Join("..", "..", "..", "packages", "sdk", "src", "projects.ts"))
	if err != nil {
		t.Fatal(err)
	}
	routes := map[string]string{}
	for _, name := range served {
		action, ok := catalog.Action(name)
		if !ok || action.Reach != tool.CatalogReachAny || action.Level != tool.CatalogLevelRead || !slices.Contains(action.Inputs, "machine") || !slices.Contains(action.Required, "project_id") {
			t.Fatalf("%s: catalog %+v", name, action)
		}
		if !tool.FleetProjectActionAllowed(name) || !tool.FleetWorkActionAllowed("manage_projects", name) || tool.FleetTaskStart("manage_projects", name) {
			t.Fatalf("%s: linked dispatch or permission gate disagrees with the catalog", name)
		}
		if !permission.IsManageProjectsReadOnlyAction(`{"action":"` + name + `","machine":"box"}`) {
			t.Fatalf("%s: not classified read-only", name)
		}
		if _, home := mcpHomeActions["manage_projects "+name]; home {
			t.Fatalf("%s: listed as a home-only MCP action", name)
		}
		path, method, _, _, err := fleetSourceRequest(name, ProjectsPath+"/pf-p/"+projectSourcesSegment, map[string]any{"project_id": "pf-p"})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		shape := mcpRouteShape(method, path)
		if spec, ok := mcpRouteSpecs[shape]; !ok || spec.Func != "handleProjectSources" {
			t.Fatalf("%s: forwarded to %s, which no project source route spec binds", name, shape)
		}
		if !fleetSourceProxyRefused(strings.SplitN(path, "?", 2)[0]) {
			t.Fatalf("%s: the home proxy does not refuse %s", name, path)
		}
		routes[name] = strings.TrimPrefix(path, ProjectsPath+"/pf-p")
	}
	if strings.Contains(string(sdk), "/"+projectSourcesSegment) {
		t.Fatal("the SDK calls a project source route, which answers only a linked home over the link")
	}
	if len(routes) != 3 || routes["list_sources"] == routes["inspect_source"] || routes["inspect_source"] == routes["inspect_files"] {
		t.Fatalf("source routes: %v", routes)
	}
}

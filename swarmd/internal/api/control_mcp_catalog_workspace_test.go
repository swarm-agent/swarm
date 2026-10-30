package api

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/permission"
	"swarm/packages/swarmd/internal/run"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	topologyruntime "swarm/packages/swarmd/internal/topology"
)

// seedWorkspaces gives each machine a run service as its manage_workspace
// (the home's is the Orchestrator's; the linked machine's serves calls a
// home sends with machine=), and what the parity cases change: twin
// registered workspaces to rename and to unlink, twin unregistered folders
// to register, twin source media folders to remove, and the Workspace Map
// revision. Cases name them by token: ws.KEY.id / ws.KEY.gen, dir.KEY,
// media.KEY and maprev.N (rws..., rdir..., rmedia..., rmaprev... on the
// linked machine).
func (h *catalogParityHarness) seedWorkspaces(t *testing.T) {
	t.Helper()
	h.catalogTokens = map[string]any{}
	for _, machine := range []struct {
		prefix  string
		swarm   *fleetTestSwarm
		runtime *tool.Runtime
		folder  string
	}{{"", h.home, h.runtime, h.sessionFolders["home"]}, {"r", h.remote, h.remoteRuntime, h.sessionFolders["remote"]}} {
		// Workspace registration and session workspace routing need the
		// topology authority, as daemon startup wires it.
		if machine.swarm.s.topology == nil {
			underlying := machine.swarm.s.sessions.Store().Underlying()
			topologyStore := pebblestore.NewTopologyStore(underlying)
			swarmStore := machine.swarm.s.swarmStore
			if swarmStore == nil {
				swarmStore = pebblestore.NewSwarmStore(underlying, topologyStore)
				machine.swarm.s.SetSwarmStore(swarmStore)
			}
			if _, ok, err := swarmStore.GetLocalNode(); err != nil {
				t.Fatal(err)
			} else if !ok {
				if _, err := swarmStore.PutLocalNode(pebblestore.SwarmLocalNodeRecord{SwarmID: "parity-" + machine.swarm.account, Name: "Local", Role: "host"}); err != nil {
					t.Fatal(err)
				}
			}
			machine.swarm.s.SetTopologyService(topologyruntime.NewService(topologyStore, swarmStore))
		}
		// The machine's permission service (seedSessions): the run service's
		// gate asks it, as in the daemon.
		perms, _ := machine.swarm.s.perm.(*permission.Service)
		svc := run.NewService(machine.swarm.s.sessions, nil, nil, machine.runtime, perms, nil, nil, nil)
		svc.SetWorkspaceService(machine.swarm.s.workspace)
		svc.SetWorkspaceMapService(pebblestore.NewWorkspaceMapService(pebblestore.NewWorkspaceMapStore(machine.swarm.s.sessions.Store().Underlying())))
		svc.SetSessionWorkspaceCanonicalizer(machine.swarm.s.CanonicalizeSessionWorkspace)
		if machine.prefix == "" {
			h.workspaceRun = svc
			// The Orchestrator's conversation works in a workspace, as in the
			// daemon: the run service's gate resolves its scope from it.
			if _, _, err := machine.swarm.s.sessions.SetWorkspacePath("home-orch", machine.folder); err != nil {
				t.Fatal(err)
			}
		}
		p := identity.Principal{Type: identity.PrincipalTypeUser, UserID: machine.swarm.user, AccountScopeID: machine.swarm.account}
		root := t.TempDir()
		folder := func(name string) string {
			path := filepath.Join(root, name)
			if err := os.MkdirAll(path, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(path, "README.md"), []byte("# "+name+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			// A workspace is a Git repository with a first commit.
			outboxPinGit(t, path, "init", "-q", "-b", "dev")
			outboxPinGit(t, path, "add", "README.md")
			outboxPinGit(t, path, "commit", "-q", "-m", "start")
			return path
		}
		for _, key := range []string{"up-a", "up-b", "del-a", "del-b", "def"} {
			added, err := machine.swarm.s.workspace.CreateCatalogEntryForPrincipal(p, folder(key), key, "")
			if err != nil {
				t.Fatal(err)
			}
			h.catalogTokens[machine.prefix+"ws."+key+".id"] = added.WorkspaceID
			h.catalogTokens[machine.prefix+"ws."+key+".gen"] = float64(added.WorkspaceGeneration)
		}
		for _, key := range []string{"new-a", "new-b"} {
			h.catalogTokens[machine.prefix+"dir."+key] = folder(key)
		}
		// The sessions folder (seedSessions) is each machine's default and
		// holds the source media folders.
		sessions, ok, err := machine.swarm.s.workspace.GetByWorkspaceIDForPrincipal(p, workspaceIDForPath(t, machine.swarm, p, machine.folder))
		if err != nil || !ok {
			t.Fatalf("sessions workspace: %v", err)
		}
		h.catalogTokens[machine.prefix+"ws.sessions.id"] = sessions.WorkspaceID
		h.catalogTokens[machine.prefix+"ws.sessions.gen"] = float64(sessions.WorkspaceGeneration)
		h.catalogTokens[machine.prefix+"ws.sessions.path"] = machine.folder
		for _, key := range []string{"add-a", "add-b", "rm-a", "rm-b"} {
			path := filepath.Join(machine.folder, "media-"+key)
			if err := os.MkdirAll(path, 0o755); err != nil {
				t.Fatal(err)
			}
			h.catalogTokens[machine.prefix+"media."+key] = path
		}
		for _, key := range []string{"rm-a", "rm-b"} {
			if _, err := machine.swarm.s.workspace.AddSourceMediaDirectoryForPrincipal(p, machine.folder, h.catalogTokens[machine.prefix+"media."+key].(string)); err != nil {
				t.Fatal(err)
			}
		}
		current, err := pebblestore.NewWorkspaceMapService(pebblestore.NewWorkspaceMapStore(machine.swarm.s.sessions.Store().Underlying())).GetOrCreateDefault(machine.swarm.account)
		if err != nil {
			t.Fatal(err)
		}
		h.catalogTokens[machine.prefix+"maprev.0"] = float64(current.Revision)
		h.catalogTokens[machine.prefix+"maprev.1"] = float64(current.Revision + 1)
	}
}

// workspaceIDForPath is the registered workspace id of path.
func workspaceIDForPath(t *testing.T, f *fleetTestSwarm, p identity.Principal, path string) string {
	t.Helper()
	entries, err := f.s.workspace.ListKnownForPrincipal(p, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if filepath.Clean(entry.Path) == filepath.Clean(path) {
			return entry.WorkspaceID
		}
	}
	t.Fatalf("%s is not registered", path)
	return ""
}

// orchestratorWorkspace runs a manage_workspace call as the home's
// Orchestrator conversation does after gateToolCalls: through
// run.Service.ExecuteManageWorkspace with the session's mutation publisher,
// and, for a change on this machine, with the approval the person gives (the
// call's arguments bound to the action's permission scope and an intent, as
// the approval card carries them).
func (h *catalogParityHarness) orchestratorWorkspace(ctx context.Context, session string, args map[string]any) (string, error) {
	raw, _ := json.Marshal(args)
	approved := ""
	if machine, _ := args["machine"].(string); machine == "" {
		action, _ := args["action"].(string)
		scope := ""
		switch action {
		case "create", "update", "delete":
			scope = "workspace_" + action
		case "update_map":
			scope = "workspace_map_update"
		}
		if scope != "" {
			arguments := map[string]any{}
			for key, value := range args {
				arguments[key] = value
			}
			arguments["permission_scope"] = scope
			if _, ok := arguments["intent"]; !ok {
				arguments["intent"] = "Parity change."
			}
			payload, _ := json.Marshal(map[string]any{"approved_arguments": arguments})
			approved = string(payload)
		}
	}
	return h.workspaceRun.ExecuteManageWorkspace(ctx, session, h.p, tool.Call{Name: "manage_workspace", Arguments: string(raw)}, approved, h.home.s.sessions.ApplySessionMutation)
}

// manageWorkspaceParityCases: every manage_workspace action, here and with
// machine= on the linked machine. Changes run on twin records; the
// Workspace Map has one revision line, so the Orchestrator writes revision
// N and Swarm Control N+1. The Swarm agent's session tooling is not served.
func manageWorkspaceParityCases() map[string][]catalogParityCase {
	args := func(machine bool, action string, more ...any) map[string]any {
		out := parityArgs(append([]any{"action", action}, more...)...)
		if machine {
			out["machine"] = "box"
		}
		return out
	}
	// tokens: KEY in more is prefixed with "r" on the linked machine.
	on := func(machine bool, more []any) []any {
		out := append([]any{}, more...)
		if !machine {
			return out
		}
		for i := 1; i < len(out); i += 2 {
			if text, ok := out[i].(string); ok && (strings.HasPrefix(text, "ws.") || strings.HasPrefix(text, "dir.") || strings.HasPrefix(text, "media.") || strings.HasPrefix(text, "maprev.")) {
				out[i] = "r" + text
			}
		}
		return out
	}
	both := func(compare catalogParityCompare, action string, more ...any) []catalogParityCase {
		return []catalogParityCase{
			{orchestrator: args(false, action, on(false, more)...), control: args(false, action, on(false, more)...), compare: compare},
			{orchestrator: args(true, action, on(true, more)...), control: args(true, action, on(true, more)...), compare: compare},
		}
	}
	twin := func(action string, orch, control []any) []catalogParityCase {
		return []catalogParityCase{
			{orchestrator: args(false, action, on(false, orch)...), control: args(false, action, on(false, control)...), compare: parityShape},
			{orchestrator: args(true, action, on(true, orch)...), control: args(true, action, on(true, control)...), compare: parityShape},
		}
	}
	notServed := func(action string) []catalogParityCase {
		a := args(false, action)
		return []catalogParityCase{{orchestrator: a, control: a, compare: parityNotServed}}
	}
	return map[string][]catalogParityCase{
		"inspect":                       both(parityExact, "inspect"),
		"list":                          both(parityExact, "list"),
		"inspect_map":                   both(parityExact, "inspect_map"),
		"get_map":                       both(parityExact, "get_map"),
		"update_map":                    twin("update_map", []any{"expected_revision", "maprev.0", "content", "# Workspace Map\n\nParity one.\n", "intent", "Parity."}, []any{"expected_revision", "maprev.1", "content", "# Workspace Map\n\nParity two.\n", "intent", "Parity."}),
		"create":                        twin("create", []any{"workspace_path", "dir.new-a", "workspace_name", "New A"}, []any{"workspace_path", "dir.new-b", "workspace_name", "New B"}),
		"update":                        twin("update", []any{"workspace_id", "ws.up-a.id", "workspace_generation", "ws.up-a.gen", "workspace_name", "Renamed"}, []any{"workspace_id", "ws.up-b.id", "workspace_generation", "ws.up-b.gen", "workspace_name", "Renamed"}),
		"delete":                        twin("delete", []any{"workspace_id", "ws.del-a.id", "workspace_generation", "ws.del-a.gen"}, []any{"workspace_id", "ws.del-b.id", "workspace_generation", "ws.del-b.gen"}),
		"set_default":                   both(parityExact, "set_default", "workspace_id", "ws.def.id", "workspace_generation", "ws.def.gen"),
		"add_source_media_directory":    twin("add_source_media_directory", []any{"workspace_path", "ws.sessions.path", "directory_path", "media.add-a"}, []any{"workspace_path", "ws.sessions.path", "directory_path", "media.add-b"}),
		"list_source_media_directories": both(parityExact, "list_source_media_directories", "workspace_path", "ws.sessions.path"),
		"remove_source_media_directory": twin("remove_source_media_directory", []any{"workspace_path", "ws.sessions.path", "directory_path", "media.rm-a"}, []any{"workspace_path", "ws.sessions.path", "directory_path", "media.rm-b"}),
		"set_session":                   notServed("set_session"),
		"adopt_worktree":                notServed("adopt_worktree"),
		"discover_worktrees":            notServed("discover_worktrees"),
		"reclaim_worktree":              notServed("reclaim_worktree"),
		"copy_worktree":                 notServed("copy_worktree"),
		"cancel_worktree_recovery":      notServed("cancel_worktree_recovery"),
	}
}

// Purpose: the Orchestrator's manage_workspace definition is generated from
// the catalog's orchestrator profile: the twelve actions it saw before, the
// same fields it was shown plus machine, and none of the Swarm agent's
// session or worktree tooling; the Swarm agent keeps the runtime's full
// definition. Threat: the hand-written trim coming back as a second schema,
// or the Orchestrator shown worktree recovery. Boundary:
// tool.OrchestratorDefinitions over the runtime's definitions.
func TestManageWorkspaceOrchestratorDefinitionIsTheCatalogs(t *testing.T) {
	catalog, ok := tool.CatalogToolNamed("manage_workspace")
	if !ok {
		t.Fatal("manage_workspace is not a catalog tool")
	}
	var shown, full tool.Definition
	for _, definition := range (*tool.Runtime)(nil).Definitions() {
		if definition.Name == "manage_workspace" {
			full = definition
		}
	}
	for _, definition := range tool.OrchestratorDefinitions((*tool.Runtime)(nil).Definitions()) {
		if definition.Name == "manage_workspace" {
			shown = definition
		}
	}
	a, _ := json.Marshal(shown)
	b, _ := json.Marshal(catalog.OrchestratorDefinition())
	if string(a) != string(b) {
		t.Fatalf("the Orchestrator's manage_workspace is not the catalog's:\n%s\n%s", a, b)
	}
	properties := shown.Parameters["properties"].(map[string]any)
	if got := strings.Join(properties["action"].(map[string]any)["enum"].([]string), ","); got != "inspect,list,inspect_map,get_map,update_map,create,update,delete,set_default,add_source_media_directory,list_source_media_directories,remove_source_media_directory" {
		t.Fatalf("Orchestrator actions: %s", got)
	}
	var fields []string
	for name := range properties {
		fields = append(fields, name)
	}
	for _, want := range []string{"action", "machine", "workspace_id", "workspace_generation", "workspace_path", "workspace_name", "intent", "expected_revision", "content", "directory_path"} {
		if _, ok := properties[want]; !ok {
			t.Fatalf("the Orchestrator lost field %q: %v", want, fields)
		}
	}
	if len(properties) != 10 {
		t.Fatalf("the Orchestrator's manage_workspace shows other fields: %v", fields)
	}
	for _, banned := range []string{"set_session", "adopt_worktree", "reclaim_worktree", "copy_worktree", "worktree_path", "owner_session_id"} {
		if strings.Contains(string(a), banned) {
			t.Fatalf("the Orchestrator's manage_workspace shows %q", banned)
		}
	}
	if !strings.Contains(mustJSON(t, full), "reclaim_worktree") {
		t.Fatal("the Swarm agent's manage_workspace lost its worktree actions")
	}
	for _, action := range catalog.Actions {
		if action.Listed(tool.CatalogProfileOrchestrator) != action.ControlCallable() {
			t.Fatalf("%s: the Orchestrator and Swarm Control see different actions", action.Action)
		}
	}
}

// Purpose: an MCP client manages workspaces through manage_workspace alone,
// at the key's level: a read key lists them and is refused create, update
// and delete before anything changes; a write key registers an existing
// folder (with swarm_add_workspace's checks: an empty folder is refused)
// and renames it; delete needs manage. A caller cannot hand in its own
// permission_scope, and the Swarm agent's session tooling is not served.
// Threat: a read or write key changing or unlinking workspaces through the
// catalog, a caller forging the approval binding, or a session action run
// with no session. Boundary: controlMCPCall.callTool ->
// Runtime.ExecuteControlCatalog -> run.Service.ManageWorkspaceControl over
// a real workspace store.
func TestManageWorkspaceOverSwarmControlByKeyLevel(t *testing.T) {
	h := newCatalogParityHarness(t)
	p := h.p
	readKey := []string{"swarm:read", "sessions:read"}
	writeKey := []string{"swarm:read", "swarm:write", "sessions:read", "sessions:write"}
	registered := func() int {
		entries, err := h.home.s.workspace.ListKnownForPrincipal(p, 200)
		if err != nil {
			t.Fatal(err)
		}
		return len(entries)
	}
	before := registered()
	folder := h.catalogTokens["dir.new-a"].(string)
	if out, refused := h.controlTool(t, readKey, "manage_workspace", map[string]any{"action": "list"}); refused != "" || !strings.Contains(out, folder[:len(filepath.Dir(folder))]) {
		t.Fatalf("read key list: %s %q", out, refused)
	}
	for _, args := range []map[string]any{
		{"action": "create", "workspace_path": folder},
		{"action": "update", "workspace_id": h.catalogTokens["ws.up-a.id"], "workspace_generation": h.catalogTokens["ws.up-a.gen"], "workspace_name": "No"},
		{"action": "delete", "workspace_id": h.catalogTokens["ws.del-a.id"], "workspace_generation": h.catalogTokens["ws.del-a.gen"]},
	} {
		if _, refused := h.controlTool(t, readKey, "manage_workspace", args); !strings.Contains(refused, "needs swarm:") {
			t.Fatalf("read key ran %v: %q", args["action"], refused)
		}
	}
	if _, refused := h.controlTool(t, writeKey, "manage_workspace", map[string]any{"action": "delete", "workspace_id": h.catalogTokens["ws.del-a.id"], "workspace_generation": h.catalogTokens["ws.del-a.gen"]}); !strings.Contains(refused, "needs swarm:manage") {
		t.Fatalf("write key unlinked a workspace: %q", refused)
	}
	if registered() != before {
		t.Fatal("a refused call changed the catalog")
	}
	if _, refused := h.controlTool(t, writeKey, "manage_workspace", map[string]any{"action": "create", "workspace_path": folder, "permission_scope": "workspace_create"}); !strings.Contains(refused, "set by Swarm") {
		t.Fatalf("a caller set its own permission_scope: %q", refused)
	}
	empty := t.TempDir()
	if _, refused := h.controlTool(t, writeKey, "manage_workspace", map[string]any{"action": "create", "workspace_path": empty}); refused == "" {
		t.Fatal("an empty folder was registered")
	}
	out, refused := h.controlTool(t, writeKey, "manage_workspace", map[string]any{"action": "create", "workspace_path": folder, "workspace_name": "From MCP"})
	if refused != "" {
		t.Fatalf("create: %s", refused)
	}
	envelope := catalogEnvelopeCheck(t, "manage_workspace create", out)
	target, _ := envelopeDataMap(envelope)["target"].(map[string]any)
	if envelope["state"] != tool.EnvelopeStateOK || target["workspace_name"] != "From MCP" || registered() != before+1 {
		t.Fatalf("create: %s", out)
	}
	if _, refused := h.controlTool(t, writeKey, "manage_workspace", map[string]any{"action": "set_session", "workspace_id": target["workspace_id"]}); !strings.Contains(refused, "for Swarm Control") {
		t.Fatalf("set_session served: %q", refused)
	}
}

// Purpose: manage_workspace with machine= reaches the linked machine's own
// handler only while the home's linked work rule is on, and the
// Orchestrator's linked call runs exactly as Swarm Control's. Threat: a
// home changing a machine's workspace catalog its person turned off.
// Boundary: run.Service.ExecuteManageWorkspace and Runtime.ExecuteControlCatalog
// -> Server.CallFleetCatalog -> the linked machine's handleCatalogLinked.
func TestManageWorkspaceLinkedWorkRule(t *testing.T) {
	h := newCatalogParityHarness(t)
	list := map[string]any{"action": "list", "machine": "box"}
	out, refused := h.controlTool(t, catalogParityFullKey, "manage_workspace", list)
	if refused != "" || !strings.Contains(out, `"machine":"box"`) || !strings.Contains(out, h.sessionFolders["remote"]) {
		t.Fatalf("linked list: %s %q", out, refused)
	}
	fleetActivityTestSetWork(t, h.home, "box", pebblestore.RuleOff)
	if _, refused := h.controlTool(t, catalogParityFullKey, "manage_workspace", list); !strings.Contains(refused, "is off") {
		t.Fatalf("off reached the linked machine: %q", refused)
	}
	if _, err := h.orchestratorTool(t, "manage_workspace", list); err == nil || !strings.Contains(err.Error(), "is off") {
		t.Fatalf("the Orchestrator reached a machine that is off: %v", err)
	}
}

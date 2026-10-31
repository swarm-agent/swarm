package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/modelprofile"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// Purpose:
//   - Requirement: manage_sessions answers the fields the swarm_* session
//     tools answered from the code those tools use, for the Orchestrator and
//     Swarm Control alike: create and send_message (the runtime's own run
//     start) answer started, session and messages as swarm_start_session and
//     swarm_send_message did; get answers session and messages
//     (swarm_get_session) and takes messages; list answers sessions
//     (swarm_list_sessions) and takes project_id; stop answers run_status,
//     summary and session (swarm_stop_run's report). send_message takes
//     content as prompt. A Swarm Control create with access is refused
//     exactly as swarm_start_session refuses it, attachments on a note
//     (trigger false) are refused, and a conversation's create never
//     reaches the swarm_* route (its plan and access are not its inputs).
//   - Threat/regression: a client losing an answer field or an input when
//     the swarm_* tools go; per-session access set again through the
//     catalog; a Swarm Control input run for a conversation.
//   - Boundary/authority: Runtime.executeManageSessions
//     (manageSessionsSwarmFields, manageSessionsSwarmRoute,
//     CatalogConversationArgs) over Server.ManageSessionSwarm, with the
//     run controller stand-in (no provider runs); the session store is the
//     observable state.
//   - Narrowest layer: Swarm Control's tools/call and the Orchestrator's
//     runtime call on the in-process harness; the swarm_* route that
//     creates a folder session needs services the harness lacks, so the
//     runtime's own create is the one run here.
func TestManageSessionsRuntimeAnswersCarryTheSwarmFields(t *testing.T) {
	h := newCatalogParityHarness(t)
	controller := &catalogSessionController{t: t, f: h.home}
	h.runtime.SetManageSessionController(controller)
	store := h.home.s.sessions.Store()
	h.home.s.SetModelProfileService(modelprofile.NewService(pebblestore.NewModelProfileStore(store.Underlying())))
	control := func(args map[string]any) (map[string]any, string) {
		t.Helper()
		out, refused := h.controlTool(t, catalogParityFullKey, "manage_sessions", args)
		if refused != "" {
			return nil, refused
		}
		data, _ := parityDecode(t, out)["data"].(map[string]any)
		return data, ""
	}
	orchestrator := func(args map[string]any) map[string]any {
		t.Helper()
		out, err := h.agentToolCall(t, agentruntime.SwarmOrchestratorAgentID, "home-orch", "manage_sessions", args)
		if err != nil {
			t.Fatalf("Orchestrator %v: %v", args["action"], err)
		}
		data, _ := parityDecode(t, out)["data"].(map[string]any)
		return data
	}
	carries := func(label string, data map[string]any, fields ...string) {
		t.Helper()
		for _, field := range fields {
			if data[field] == nil {
				t.Fatalf("%s lacks %s: %v", label, field, data)
			}
		}
		if data["state_error"] != nil {
			t.Fatalf("%s could not read the session: %v", label, data["state_error"])
		}
	}
	folder := h.sessionFolders["home"]

	// create with a prompt: the runtime's own create starts the run and
	// answers swarm_start_session's started, session and messages.
	for _, caller := range []string{"control", "orchestrator"} {
		args := map[string]any{"action": "create", "title": "Fields " + caller, "workspace_path": folder, "prompt": "Write it."}
		var data map[string]any
		if caller == "control" {
			var refused string
			if data, refused = control(args); refused != "" {
				t.Fatalf("create: %s", refused)
			}
		} else {
			data = orchestrator(args)
		}
		carries(caller+" create", data, "session_id", "run_id", "started", "session", "messages")
		if started, _ := data["started"].(map[string]any); started["run_id"] != data["run_id"] {
			t.Fatalf("%s create: started %v is not the run %v", caller, data["started"], data["run_id"])
		}
	}
	if len(controller.enqueued) != 2 {
		t.Fatalf("the two creates did not each start one run through the runtime: %v", controller.enqueued)
	}

	// send_message with content (swarm_send_message's name for prompt) and
	// no client_request_id: the runtime's own run start, with the fields.
	data, refused := control(map[string]any{"action": "send_message", "session_id": "ms-msg-1", "content": "Go on."})
	if refused != "" {
		t.Fatalf("send_message content: %s", refused)
	}
	carries("send_message", data, "run_id", "started", "session", "messages")
	if len(controller.enqueued) != 3 {
		t.Fatalf("send_message did not start its run through the runtime: %v", controller.enqueued)
	}
	// Attachments go with a message that starts a run, never with a note.
	if _, refused = control(map[string]any{"action": "send_message", "session_id": "ms-msg-2", "prompt": "Note.", "trigger": false, "attachments": []any{map[string]any{"name": "a.txt"}}}); !strings.Contains(refused, "attachments go with a message that starts a run") {
		t.Fatalf("attachments on a note were not refused: %q", refused)
	}

	// get: session and messages, messages bounding how many.
	data, refused = control(map[string]any{"action": "get", "session_id": "ms-read", "messages": float64(1)})
	if refused != "" {
		t.Fatalf("get: %s", refused)
	}
	carries("get", data, "session", "messages")
	if messages, _ := data["messages"].([]any); len(messages) != 1 {
		t.Fatalf("get messages 1 answered %d messages", len(messages))
	}
	carries("Orchestrator get", orchestrator(map[string]any{"action": "get", "session_id": "ms-read"}), "session", "messages")

	// list: sessions, for every caller; project_id is taken.
	data, refused = control(map[string]any{"action": "list", "project_id": "home-project"})
	if refused != "" {
		t.Fatalf("list project_id: %s", refused)
	}
	carries("list", data, "items", "sessions")

	// stop: swarm_stop_run's report on the runtime's own stop.
	running := controller.enqueued[2]
	data, refused = control(map[string]any{"action": "stop", "session_id": "ms-msg-1", "reason": "done"})
	if refused != "" {
		t.Fatalf("stop: %s", refused)
	}
	carries("stop", data, "status", "run_status", "summary", "session")
	if data["run_id"] != running || len(controller.cancelled) != 1 {
		t.Fatalf("stop did not stop the run through the runtime: %v (cancelled %v)", data, controller.cancelled)
	}
	data, refused = control(map[string]any{"action": "stop", "session_id": "ms-read"})
	if refused != "" || data["run_status"] != "not_running" {
		t.Fatalf("stop with no run: %q %v", refused, data)
	}

	// access: refused as swarm_start_session refuses it, and nothing is
	// created.
	if _, refused = control(map[string]any{"action": "create", "title": "Access probe", "workspace_path": folder, "prompt": "Go.", "access": map[string]any{"tasks": "free"}}); !strings.Contains(refused, retiredSessionAccess) {
		t.Fatalf("create with access was not refused as swarm_start_session refuses it: %q", refused)
	}
	if data, refused = control(map[string]any{"action": "list", "query": "Access probe"}); refused != "" || len(data["items"].([]any)) != 0 {
		t.Fatalf("a refused create made a session: %q %v", refused, data["items"])
	}
	// A conversation's create never carries plan or access: they are
	// Swarm Control inputs, so its create is its own (no refusal, no plan).
	before := len(controller.enqueued)
	data = orchestrator(map[string]any{"action": "create", "title": "Orchestrator access", "workspace_path": folder, "prompt": "Go.", "access": map[string]any{"tasks": "free"}, "plan": map[string]any{"title": "P"}})
	if data["session_id"] == nil || data["started"] == nil || len(controller.enqueued) != before+1 {
		t.Fatalf("a conversation's create took a Swarm Control input: %v", data)
	}
}

// Purpose:
//   - Requirement: manage_workspace create over Swarm Control takes
//     swarm_add_workspace's path and name and swarm_create_workspace's name
//     (run by those tools' handlers), and refuses them mixed with its own
//     workspace_path, workspace_name or intent, naming those, with nothing
//     registered; a conversation's create never takes them (its approval
//     covers its own arguments only).
//   - Threat/regression: an input silently dropped or a folder registered
//     from a half-applied call; a conversation registering a folder outside
//     the arguments the person approved.
//   - Boundary/authority: Runtime.ManageWorkspaceCatalog
//     (CatalogConversationArgs) and manageWorkspaceSwarmCreate over
//     Server.ManageWorkspaceSwarm; the workspace catalog is the observable
//     state.
//   - Narrowest layer: Swarm Control's tools/call on the in-process harness
//     and the conversation filter itself.
func TestManageWorkspaceCreateTakesTheSwarmToolInputs(t *testing.T) {
	h := newCatalogParityHarness(t)
	folder := t.TempDir()
	if err := os.WriteFile(filepath.Join(folder, "README.md"), []byte("probe\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	registered := func() bool {
		t.Helper()
		entries, err := h.home.s.workspace.ListKnownForPrincipal(h.p, 2000)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if filepath.Clean(entry.Path) == filepath.Clean(folder) {
				return true
			}
		}
		return false
	}
	for _, args := range []map[string]any{
		{"action": "create", "path": folder, "workspace_path": folder},
		{"action": "create", "path": folder, "intent": "Register it."},
		{"action": "create", "name": "probe-new", "workspace_name": "Probe"},
	} {
		_, refused := h.controlTool(t, catalogParityFullKey, "manage_workspace", args)
		if !strings.Contains(refused, "do not go with path or name") {
			t.Fatalf("create %v was not refused: %q", args, refused)
		}
		for _, key := range []string{"workspace_path", "intent", "workspace_name"} {
			if _, given := args[key]; given && !strings.Contains(refused, key) {
				t.Fatalf("the refusal does not name %s: %q", key, refused)
			}
		}
	}
	if registered() {
		t.Fatal("a refused create registered the folder")
	}
	out, refused := h.controlTool(t, catalogParityFullKey, "manage_workspace", map[string]any{"action": "create", "path": folder, "name": "Probe"})
	if refused != "" {
		t.Fatalf("create path: %s", refused)
	}
	data, _ := parityDecode(t, out)["data"].(map[string]any)
	if data["added"] != true || data["workspace"] == nil || !registered() {
		t.Fatalf("create path did not register the folder as swarm_add_workspace does: %v", data)
	}
	catalog, _ := tool.CatalogToolNamed("manage_workspace")
	create, _ := catalog.Action("create")
	conversation := tool.CatalogConversationArgs(create, false, map[string]any{"action": "create", "path": folder, "name": "x", "workspace_path": folder})
	if _, kept := conversation["path"]; kept || conversation["name"] != nil || conversation["workspace_path"] != folder {
		t.Fatalf("a conversation's create kept a Swarm Control input: %v", conversation)
	}
	// A linked create with a plan starts a run there: under the ask rule it
	// waits for the person, as a prompt does.
	if !catalogLinkedStartsRun("manage_sessions", map[string]any{"action": "create", "plan": map[string]any{"title": "P"}}) {
		t.Fatal("a linked create with a plan is not counted as starting a run")
	}
}

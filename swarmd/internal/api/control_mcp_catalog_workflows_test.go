package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/jobautomation"
	"swarm/packages/swarmd/internal/remote"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// parityWorkflowSteps are the steps of every seeded workflow: one native
// review, no run step (run steps stay the machine owner's).
func parityWorkflowSteps(brief string) []jobautomation.Step {
	return []jobautomation.Step{{ID: "review", Type: jobautomation.StepAgent, Worker: "reviewer", Brief: brief}}
}

// seedWorkflows gives the home and the linked machine their workflow store
// and the same named workflows: Parity (read), Hist-a/b/c (two revisions
// each), Toggle-a/b (enabled), Off-a/b (disabled), Del-a/b. The home gets a
// project with a folder (wf-project) that proposals and imports bind to;
// the linked machine's is remote-project.
func (h *catalogParityHarness) seedWorkflows(t *testing.T) {
	t.Helper()
	h.homeWorkflows = jobautomation.NewStore(h.home.s.sessions.Store().Underlying())
	h.home.s.SetJobAutomations(jobautomation.NewRunner(jobautomation.Options{Store: h.homeWorkflows}))
	h.remoteWorkflows = jobautomation.NewStore(h.remote.s.sessions.Store().Underlying())
	h.remote.s.SetJobAutomations(jobautomation.NewRunner(jobautomation.Options{Store: h.remoteWorkflows}))
	h.runtime.SetWorkflowLifecycleService(h.home.s)
	h.workflowFolder = t.TempDir()
	if err := h.home.s.sessions.Store().PutProject(h.home.account, &pebblestore.ProjectRecord{ID: "wf-project", AccountID: h.home.account, Name: "Workflow project", Workspaces: []pebblestore.ProjectWorkspaceRef{{Path: h.workflowFolder, Role: "primary_code"}}}); err != nil {
		t.Fatal(err)
	}
	remoteFolder := t.TempDir()
	for _, machine := range []struct {
		store   *jobautomation.Store
		account string
		folder  string
	}{{h.homeWorkflows, h.home.account, h.workflowFolder}, {h.remoteWorkflows, h.remote.account, remoteFolder}} {
		put := func(name string, enabled bool, brief string) {
			if _, _, err := machine.store.Put(machine.account, jobautomation.Automation{Name: name, Scope: jobautomation.Scope{Folder: machine.folder}, Enabled: enabled, Steps: parityWorkflowSteps(brief)}); err != nil {
				t.Fatal(err)
			}
		}
		for _, name := range []string{"Parity", "Toggle-a", "Toggle-b", "Del-a", "Del-b"} {
			put(name, true, "Review the change")
		}
		for _, name := range []string{"Off-a", "Off-b"} {
			put(name, false, "Review the change")
		}
		for _, name := range []string{"Hist-a", "Hist-b", "Hist-c"} {
			put(name, true, "Review v1")
			put(name, true, "Review v2")
		}
	}
}

// resolveCase puts each workflow's id where a case names it as "wf:NAME",
// from the store of the machine the case runs on, in the arguments and the
// twin map.
func (h *catalogParityHarness) resolveCase(t *testing.T, c catalogParityCase) catalogParityCase {
	t.Helper()
	if h.homeWorkflows == nil {
		return c
	}
	machine, _ := c.orchestrator["machine"].(string)
	store, account := h.homeWorkflows, h.home.account
	if machine != "" {
		store, account = h.remoteWorkflows, h.remote.account
	}
	resolve := func(text string) string {
		if strings.HasPrefix(text, "job:") {
			return h.resolveJob(machine, text)
		}
		if folder, ok := strings.CutPrefix(text, "folder:"); ok && h.sessionFolders[folder] != "" {
			return h.sessionFolders[folder]
		}
		if session, ok := strings.CutPrefix(text, "perm:"); ok && h.sessionPermissions[session] != "" {
			return h.sessionPermissions[session]
		}
		name, ok := strings.CutPrefix(text, "wf:")
		if !ok {
			return text
		}
		a, err := store.FindByName(account, name)
		if err != nil {
			t.Fatalf("workflow %q on %q: %v", name, machine, err)
		}
		return a.ID
	}
	var walk func(any) any
	walk = func(value any) any {
		switch v := value.(type) {
		case string:
			if value, ok := h.catalogTokens[v]; ok {
				return value
			}
			return resolve(v)
		case map[string]any:
			out := make(map[string]any, len(v))
			for key, item := range v {
				out[key] = walk(item)
			}
			return out
		case []any:
			out := make([]any, len(v))
			for i, item := range v {
				out[i] = walk(item)
			}
			return out
		}
		return value
	}
	resolved := catalogParityCase{compare: c.compare, orchestrator: walk(c.orchestrator).(map[string]any), control: walk(c.control).(map[string]any)}
	if c.twin != nil {
		resolved.twin = map[string]string{}
		for from, to := range c.twin {
			resolved.twin[resolve(from)] = resolve(to)
		}
	}
	return resolved
}

// manageWorkflowsParityCases holds a case for every manage_workflows action,
// here and (all but help) on the linked machine box. Mutations run on twin
// workflows; "wf:NAME" is resolved to the workflow's id (resolveCase).
func manageWorkflowsParityCases() map[string][]catalogParityCase {
	both := func(local, linked map[string]any) []catalogParityCase {
		return []catalogParityCase{{orchestrator: local, control: local, compare: parityExact}, {orchestrator: linked, control: linked, compare: parityExact}}
	}
	at := func(machine string, args ...any) map[string]any {
		out := parityArgs(args...)
		if machine != "" {
			out["machine"] = machine
		}
		return out
	}
	// twins runs a mutation on the a-workflow (Orchestrator) and its
	// b-twin (Swarm Control), here and on box.
	twins := func(action, field, a, b string, more ...any) []catalogParityCase {
		var out []catalogParityCase
		for _, machine := range []string{"", "box"} {
			out = append(out, catalogParityCase{
				orchestrator: at(machine, append([]any{"action", action, field, a}, more...)...),
				control:      at(machine, append([]any{"action", action, field, b}, more...)...),
				compare:      parityShape, twin: map[string]string{b: a, strings.TrimPrefix(b, "wf:"): strings.TrimPrefix(a, "wf:")},
			})
		}
		return out
	}
	steps := []any{map[string]any{"id": "review", "type": "agent", "worker": "reviewer", "brief": "Review the change"}}
	definition := func(name string) map[string]any {
		return map[string]any{"swarm_workflow": float64(1), "name": name, "steps": steps}
	}
	project := map[string]string{"": "wf-project", "box": "remote-project"}
	var imports, proposals []catalogParityCase
	for _, machine := range []string{"", "box"} {
		imports = append(imports, catalogParityCase{
			orchestrator: at(machine, "action", "import", "definition", definition("Imported-a"), "project", project[machine]),
			control:      at(machine, "action", "import", "definition", definition("Imported-b"), "project", project[machine]),
			compare:      parityShape, twin: map[string]string{"Imported-b": "Imported-a"},
		})
		proposals = append(proposals, catalogParityCase{
			orchestrator: at(machine, "action", "propose", "name", "Proposed", "project", project[machine], "scope", map[string]any{"role": "coder"}, "steps", steps),
			control:      at(machine, "action", "propose", "name", "Proposed", "project", project[machine], "scope", map[string]any{"role": "coder"}, "steps", steps),
			compare:      parityShape,
		})
	}
	return map[string][]catalogParityCase{
		"list":       both(at("", "action", "list"), at("box", "action", "list")),
		"get":        both(at("", "action", "get", "id", "wf:Parity"), at("box", "action", "get", "id", "wf:Parity")),
		"deliveries": both(at("", "action", "deliveries", "id", "wf:Parity"), at("box", "action", "deliveries", "id", "wf:Parity")),
		"history":    both(at("", "action", "history", "name", "Hist-c"), at("box", "action", "history", "name", "Hist-c", "revision", float64(1))),
		"export":     both(at("", "action", "export", "id", "wf:Parity"), at("box", "action", "export", "id", "wf:Parity")),
		"help":       {{orchestrator: at("", "action", "help"), control: at("", "action", "help"), compare: parityExact}},
		"stop_all":   both(at("", "action", "stop_all", "stopped", false), at("box", "action", "stop_all", "stopped", false)),
		"disable":    twins("disable", "id", "wf:Toggle-a", "wf:Toggle-b"),
		"enable":     twins("enable", "id", "wf:Off-a", "wf:Off-b"),
		"delete":     twins("delete", "id", "wf:Del-a", "wf:Del-b"),
		"restore":    twins("restore", "name", "Hist-a", "Hist-b", "revision", float64(1)),
		"import":     imports,
		"propose":    proposals,
	}
}

// workflowEnvelopeData runs a manage_workflows call and returns its data,
// after checking the envelope contract.
func workflowEnvelopeData(t *testing.T, name, out, refused string) map[string]any {
	t.Helper()
	if refused != "" {
		t.Fatalf("%s refused: %s", name, refused)
	}
	return envelopeDataMap(catalogEnvelopeCheck(t, name, out))
}

// Purpose: workflows can be forked, reused and rolled back from both
// surfaces, here and on a linked machine, through the one service: export a
// workflow (Swarm Control), import it under a new name as a copy
// (Orchestrator), propose an edit of the copy (Orchestrator) that changes
// nothing until the person accepts it in their app, list the copy's history
// (Swarm Control) and restore its earlier revision (Swarm Control), which
// the Orchestrator then reads back. The original is never touched. A read
// key cannot import, and an import with a run step is refused even for a
// full key (run steps stay the machine owner's) with no workflow made.
// Threat: export/import/restore reachable from one surface only, a fork
// that edits its original, a proposal that self-applies, a rollback that
// does not restore the definition, or an AI key creating host commands.
// Boundary: tool.Runtime.executeManageWorkflows over api.Server.ManageWorkflows
// (and the linked agent route) on two in-process daemons; the person's
// acceptance is the HTTP accept route. No provider runs.
func TestManageWorkflowsForkAndRollBack(t *testing.T) {
	h := newCatalogParityHarness(t)
	for _, machine := range []string{"", "box"} {
		t.Run("machine="+machine, func(t *testing.T) {
			store, account, server, user, project := h.homeWorkflows, h.home.account, h.home.s, h.home.user, "wf-project"
			if machine != "" {
				store, account, server, user, project = h.remoteWorkflows, h.remote.account, h.remote.s, h.remote.user, "remote-project"
			}
			call := func(args map[string]any) map[string]any {
				if machine != "" {
					args["machine"] = machine
				}
				return args
			}
			origin, _, err := store.Put(account, jobautomation.Automation{Name: "Origin", Scope: jobautomation.Scope{Folder: t.TempDir()}, Enabled: true, Steps: parityWorkflowSteps("Review v1")})
			if err != nil {
				t.Fatal(err)
			}
			// Export (Swarm Control).
			out, refused := h.controlTool(t, catalogParityFullKey, "manage_workflows", call(map[string]any{"action": "export", "name": "Origin"}))
			exported := workflowEnvelopeData(t, "export", out, refused)
			if exported["swarm_workflow"] != float64(1) || exported["name"] != "Origin" {
				t.Fatalf("export: %s", out)
			}
			// Import as a copy (Orchestrator).
			fork := map[string]any{"action": "import", "definition": exported, "name": "Fork", "project": project}
			out, err = h.orchestratorTool(t, "manage_workflows", call(fork))
			if err != nil {
				t.Fatal(err)
			}
			if data := workflowEnvelopeData(t, "import", out, ""); data["created"] != true {
				t.Fatalf("import: %s", out)
			}
			copied, err := store.FindByName(account, "Fork")
			if err != nil || copied.ID == origin.ID || copied.Steps[0].Brief != "Review v1" || copied.Revision != 1 {
				t.Fatalf("fork: %+v %v", copied, err)
			}
			// Propose an edit (Orchestrator): nothing changes until the
			// person accepts.
			out, err = h.orchestratorTool(t, "manage_workflows", call(map[string]any{"action": "propose", "name": "Fork", "project": project, "steps": []any{map[string]any{"id": "review", "type": "agent", "worker": "reviewer", "brief": "Review v2"}}}))
			if err != nil {
				t.Fatal(err)
			}
			proposal, _ := workflowEnvelopeData(t, "propose", out, "")["automation"].(map[string]any)
			if unchanged, _ := store.FindByName(account, "Fork"); unchanged.Revision != 1 || unchanged.Steps[0].Brief != "Review v1" {
				t.Fatalf("a proposal changed the workflow: %+v", unchanged)
			}
			accept := httptest.NewRequest(http.MethodPost, JobAutomationsPath+"/"+proposal["id"].(string)+"/accept", strings.NewReader(fmt.Sprintf(`{"expected_revision":%v}`, proposal["revision"])))
			w := httptest.NewRecorder()
			server.handleJobAutomations(w, requestWithTestPrincipalForAccount(accept, user, account))
			if w.Code != http.StatusOK {
				t.Fatalf("the person's accept: %d %s", w.Code, w.Body)
			}
			if edited, _ := store.FindByName(account, "Fork"); edited.Revision != 2 || edited.Steps[0].Brief != "Review v2" || edited.ID != copied.ID {
				t.Fatalf("accepted edit: %+v", edited)
			}
			// History (Swarm Control): both revisions, newest first.
			out, refused = h.controlTool(t, catalogParityFullKey, "manage_workflows", call(map[string]any{"action": "history", "name": "Fork"}))
			records, _ := workflowEnvelopeData(t, "history", out, refused)["history"].([]any)
			if len(records) < 2 || records[0].(map[string]any)["revision"] != float64(2) || records[len(records)-1].(map[string]any)["revision"] != float64(1) {
				t.Fatalf("history: %s", out)
			}
			// Restore revision 1 (Swarm Control); the Orchestrator reads it.
			out, refused = h.controlTool(t, catalogParityFullKey, "manage_workflows", call(map[string]any{"action": "restore", "name": "Fork", "revision": float64(1)}))
			workflowEnvelopeData(t, "restore", out, refused)
			out, err = h.orchestratorTool(t, "manage_workflows", call(map[string]any{"action": "get", "id": copied.ID}))
			if err != nil {
				t.Fatal(err)
			}
			got := workflowEnvelopeData(t, "get", out, "")["automation"].(map[string]any)
			if got["revision"] != float64(3) || !strings.Contains(mustJSON(t, got["steps"]), "Review v1") {
				t.Fatalf("restored: %s", out)
			}
			if kept, _ := store.Get(account, origin.ID); kept.Revision != origin.Revision || kept.Steps[0].Brief != "Review v1" {
				t.Fatalf("the fork changed its original: %+v", kept)
			}
			// A read key cannot import; a run step is refused even for a
			// full key. Neither makes a workflow.
			if _, refused := h.controlTool(t, []string{"swarm:read", "automations:read", "automations:write"}, "manage_workflows", call(map[string]any{"action": "import", "definition": exported, "name": "Refused", "project": project})); !strings.Contains(refused, "needs swarm:write") {
				t.Fatalf("read key import: %q", refused)
			}
			// The run-step refusal is an envelope: state refused, the
			// reason and what to do instead.
			runStep := map[string]any{"swarm_workflow": float64(1), "name": "Host", "steps": []any{map[string]any{"type": "run", "command": "deliver/push.sh"}}}
			out, refused = h.controlTool(t, catalogParityFullKey, "manage_workflows", call(map[string]any{"action": "import", "definition": runStep, "project": project}))
			if refused != "" || catalogEnvelopeCheck(t, "import", out)["state"] != "refused" {
				t.Fatalf("an AI key's run-step import: %s %s", out, refused)
			}
			for _, name := range []string{"Refused", "Host"} {
				if _, err := store.FindByName(account, name); err == nil {
					t.Fatalf("a refused import made %s", name)
				}
			}
		})
	}
}

// Purpose: Swarm Control lists and runs manage_workflows per action at the
// key's level and with the API scopes of the route behind each action: it
// is in the default listing; a read key lists only read actions and is
// refused a change; a write key is refused stop_all (manage) and the switch
// stays off; a key without automations scopes neither lists nor calls it;
// a relay client is held to its forwarded levels. Threat: a key turning
// the machine-wide stop switch, changing workflows below its level, or
// reaching the workflow routes without their scopes. Boundary:
// controlMCPCall.catalogTools / callCatalogTool and
// tool.Runtime.ExecuteControlCatalog.
func TestManageWorkflowsLevelsAndScopes(t *testing.T) {
	h := newCatalogParityHarness(t)
	listed := func(c *controlMCPCall) map[string]bool {
		for _, candidate := range c.visibleTools() {
			if candidate.Name == "manage_workflows" {
				out := map[string]bool{}
				for _, action := range candidate.InputSchema["properties"].(map[string]any)["action"].(map[string]any)["enum"].([]string) {
					out[action] = true
				}
				return out
			}
		}
		return nil
	}
	full := listed(&controlMCPCall{request: h.controlRequest(catalogParityFullKey, ""), server: h.home.s})
	workflows, _ := tool.CatalogToolNamed("manage_workflows")
	for _, action := range workflows.Actions {
		if !full[action.Action] {
			t.Fatalf("default listing lacks manage_workflows %s: %v", action.Action, full)
		}
	}
	readKey := []string{"swarm:read", "automations:read"}
	read := listed(&controlMCPCall{request: h.controlRequest(readKey, ""), server: h.home.s})
	if !read["list"] || !read["get"] || !read["deliveries"] || read["history"] || read["propose"] || read["import"] || read["restore"] || read["stop_all"] || read["export"] {
		t.Fatalf("read key listing: %v", read)
	}
	before, _ := h.homeWorkflows.FindByName(h.home.account, "Toggle-a")
	if _, refused := h.controlTool(t, readKey, "manage_workflows", map[string]any{"action": "disable", "id": before.ID}); !strings.Contains(refused, "needs swarm:write") {
		t.Fatalf("read key disable: %q", refused)
	}
	if after, _ := h.homeWorkflows.Get(h.home.account, before.ID); after.Revision != before.Revision || !after.Enabled {
		t.Fatalf("a refused disable changed the workflow: %+v", after)
	}
	writeKey := []string{"swarm:read", "swarm:write", "automations:read", "automations:write"}
	if _, refused := h.controlTool(t, writeKey, "manage_workflows", map[string]any{"action": "stop_all", "stopped": true}); !strings.Contains(refused, "needs swarm:manage") {
		t.Fatalf("write key stop_all: %q", refused)
	}
	if stop, err := h.homeWorkflows.StopState(); err != nil || stop.Stopped {
		t.Fatalf("a refused stop_all turned the switch: %+v %v", stop, err)
	}
	// A manage key turns it, and turns it back.
	manageKey := append(append([]string(nil), writeKey...), "swarm:manage")
	for _, stopped := range []bool{true, false} {
		out, refused := h.controlTool(t, manageKey, "manage_workflows", map[string]any{"action": "stop_all", "stopped": stopped})
		data := workflowEnvelopeData(t, "stop_all", out, refused)
		if stop, err := h.homeWorkflows.StopState(); err != nil || stop.Stopped != stopped || data["stop"].(map[string]any)["stopped"] != stopped {
			t.Fatalf("stop_all %v: %s %+v %v", stopped, out, stop, err)
		}
	}
	noScopes := []string{"swarm:read", "swarm:write", "sessions:read", "sessions:write", "projects:read", "projects:write"}
	if got := listed(&controlMCPCall{request: h.controlRequest(noScopes, ""), server: h.home.s}); got != nil {
		t.Fatalf("a key without automations scopes lists manage_workflows: %v", got)
	}
	if _, refused := h.controlTool(t, noScopes, "manage_workflows", map[string]any{"action": "list"}); !strings.Contains(refused, "automations:read or automations:write") {
		t.Fatalf("a key without automations scopes: %q", refused)
	}
	// A relay client: its forwarded levels bind per action.
	relay := func(levels []string, args map[string]any) string {
		req := h.controlRequest(relayDeviceScopes, "")
		req.Header.Set(remote.ClientLevelsHeader, strings.Join(levels, ","))
		c := &controlMCPCall{request: req, server: h.home.s}
		params, _ := json.Marshal(map[string]any{"name": "manage_workflows", "arguments": args})
		result, rpcErr := c.callTool(params)
		if rpcErr != nil {
			t.Fatalf("tools/call: %+v", rpcErr)
		}
		text, _ := result["content"].([]map[string]any)[0]["text"].(string)
		if result["isError"] == true {
			return "refused: " + text
		}
		return text
	}
	if out := relay([]string{remote.ScopeRead}, map[string]any{"action": "list"}); strings.HasPrefix(out, "refused") {
		t.Fatalf("read relay client list: %s", out)
	}
	if out := relay([]string{remote.ScopeRead, remote.ScopeWrite}, map[string]any{"action": "stop_all", "stopped": true}); !strings.Contains(out, "this key is limited to swarm:read, swarm:write") {
		t.Fatalf("write relay client stop_all: %s", out)
	}
	if stop, _ := h.homeWorkflows.StopState(); stop.Stopped {
		t.Fatal("a refused relay stop_all turned the switch")
	}
}

// Purpose: manage_workflows export reads at automations:read, but its path
// writes a file into a project folder, which the export route and
// swarm_export_workflow allow only with automations:write. Through the
// catalog the key's own scopes must bind the same way: a key with only
// automations:read and no swarm:* levels (unbound levels allow every
// action) is refused export with path, here and on a linked machine (whose
// route sees the link's credentials, not the key), and no file is written;
// its export without path still reads. The service function itself
// (ManageWorkflows) also refuses when its context carries only read: it
// never hands a Swarm Control caller automations:write the key lacks. A key
// with automations:write writes the file. Threat: a read-only scoped key
// creating or overwriting files through the catalog. Boundary:
// controlMCPCall.callCatalogTool -> tool.Runtime.executeManageWorkflows ->
// api.Server.ManageWorkflows -> ExportWorkflow, on two in-process daemons.
func TestManageWorkflowsExportPathNeedsTheKeysWriteScope(t *testing.T) {
	h := newCatalogParityHarness(t)
	readOnly := []string{"automations:read"}
	for _, machine := range []string{"", "box"} {
		store, account := h.homeWorkflows, h.home.account
		if machine != "" {
			store, account = h.remoteWorkflows, h.remote.account
		}
		parity, err := store.FindByName(account, "Parity")
		if err != nil {
			t.Fatal(err)
		}
		args := func(more ...any) map[string]any {
			out := parityArgs(append([]any{"action", "export", "id", parity.ID}, more...)...)
			if machine != "" {
				out["machine"] = machine
			}
			return out
		}
		target := filepath.Join(parity.Scope.Folder, "exports", "parity.json")
		for _, path := range []string{"exports/parity.json", ""} {
			if _, refused := h.controlTool(t, readOnly, "manage_workflows", args("path", path)); !strings.Contains(refused, "automations:write") {
				t.Fatalf("machine %q: read-only key export path %q: %q", machine, path, refused)
			}
		}
		for _, written := range []string{target, filepath.Join(parity.Scope.Folder, ".swarm", "workflows", "Parity.json")} {
			if _, err := os.Stat(written); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("machine %q: a refused export wrote %s: %v", machine, written, err)
			}
		}
		out, refused := h.controlTool(t, readOnly, "manage_workflows", args())
		if data := workflowEnvelopeData(t, "export", out, refused); data["name"] != "Parity" {
			t.Fatalf("machine %q: read-only export: %s", machine, out)
		}
	}
	// The service boundary: a Swarm Control caller holding only read.
	parity, _ := h.homeWorkflows.FindByName(h.home.account, "Parity")
	ctx := tool.WithControlScopes(context.Background(), []string{"automations:read", "sessions:read", "swarm:read"})
	if _, err := h.home.s.ManageWorkflows(ctx, h.p, "key:read", map[string]any{"action": "export", "id": parity.ID, "path": "exports/service.json"}); err == nil || !strings.Contains(err.Error(), "automations:write") {
		t.Fatalf("ManageWorkflows gave a read-only key write: %v", err)
	}
	if _, err := os.Stat(filepath.Join(parity.Scope.Folder, "exports", "service.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a refused service export wrote a file: %v", err)
	}
	// A key with automations:write writes it.
	out, refused := h.controlTool(t, catalogParityFullKey, "manage_workflows", map[string]any{"action": "export", "id": parity.ID, "path": "exports/parity.json"})
	workflowEnvelopeData(t, "export", out, refused)
	if _, err := os.Stat(filepath.Join(parity.Scope.Folder, "exports", "parity.json")); err != nil {
		t.Fatalf("a write key's export wrote no file: %v", err)
	}
}

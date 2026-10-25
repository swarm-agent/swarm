package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement: portable files round trip across project folders without runtime
// fields; invalid files and weaker keys cannot save commands or escape folders.
// Owners: controlMCPExportWorkflow, controlMCPImportWorkflow, os.Root and the
// shared handleJobAutomations/SaveWorkflow boundary via MCP dispatch. Handler-level calls
// over the real store are the narrowest proof of both results and no mutation.
func TestWorkflowTransfer(t *testing.T) {
	server, store := newRunOwnerServer(t)
	folder := t.TempDir()
	target := t.TempDir()
	c := &controlMCPCall{server: server, request: requestWithScopedToken(withTestPrincipal(httptest.NewRequest("POST", "/mcp", nil)), &pebblestore.ScopedTokenRecord{Scopes: scopesForLevel(t, "full")})}
	c.next = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v3/projects" {
			t.Errorf("unexpected route %s", r.URL.Path)
		}
		writeJSON(w, 200, map[string]any{"projects": []any{map[string]any{"id": "proj-source", "name": "Source", "workspaces": []any{map[string]any{"path": folder, "workspace_id": "ws-source", "role": "primary_code"}}}, map[string]any{"id": "proj-1", "name": "Site", "workspaces": []any{map[string]any{"path": target, "workspace_id": "ws-1", "role": "primary_code"}}}}})
	})
	wireWorkflowRoutes(c)
	if _, err := controlMCPSetAutomation(c, map[string]any{"name": "review", "scope": map[string]any{"folder": folder}, "steps": []any{map[string]any{"type": "agent", "worker": "reviewer", "brief": "review"}}}); err != nil {
		t.Fatal(err)
	}
	exported, err := controlMCPExportWorkflow(c, map[string]any{"name": "review", "path": ""})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(exported)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), folder) || strings.Contains(string(data), `"folder"`) || strings.Contains(string(data), `"revision"`) {
		t.Fatalf("nonportable: %s", data)
	}
	original := storedAutomations(t, store)[0]
	imported, err := controlMCPImportWorkflow(c, map[string]any{"definition": exported, "project": "Site"})
	if err != nil {
		t.Fatal(err)
	}
	if imported.(map[string]any)["created"] != false {
		t.Fatal(imported)
	}
	rebound := storedAutomations(t, store)[0]
	if rebound.Scope.Folder != target {
		t.Fatalf("folder not rebound: %+v", rebound)
	}
	comparison := rebound
	comparison.Scope.Folder = folder
	if !workflowDefinitionsEqual(original, comparison) {
		t.Fatalf("round trip changed definition: %+v", rebound)
	}
	if err := os.WriteFile(filepath.Join(target, "good.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "bad.json"), []byte(`{"swarm_workflow":99}`), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := controlMCPImportWorkflow(c, map[string]any{"path": ".", "project": "Site"})
	if err != nil {
		t.Fatal(err)
	}
	// Normalize refusals must survive both directory and inline MCP boundaries,
	// without changing the stored workflow (controlMCPSaveAutomationChange).
	var invalid map[string]any
	if err := json.Unmarshal(data, &invalid); err != nil {
		t.Fatal(err)
	}
	invalid["name"] = ""
	badData, err := json.Marshal(invalid)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "invalid.json"), badData, 0600); err != nil {
		t.Fatal(err)
	}
	value, refusal := controlMCPImportWorkflow(c, map[string]any{"definition": invalid, "project": "Site"})
	mcp := controlMCPToolResult(value, refusal)
	text := mcp["content"].([]map[string]any)[0]["text"].(string)
	if mcp["isError"] != true || !strings.HasPrefix(text, "name is required") {
		t.Fatal(mcp)
	}
	result, err = controlMCPImportWorkflow(c, map[string]any{"path": ".", "project": "Site"})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, raw := range result.(map[string]any)["files"].([]any) {
		f := raw.(map[string]any)
		if f["path"] == "invalid.json" {
			found = true
			if f["status"] != "refused" || f["error"] != text {
				t.Fatal(f)
			}
		}
	}
	if !found {
		t.Fatal("missing invalid file result")
	}
	statuses := map[string]string{}
	for _, raw := range result.(map[string]any)["files"].([]any) {
		f := raw.(map[string]any)
		statuses[f["path"].(string)] = f["status"].(string)
	}
	if statuses["good.json"] != "unchanged" || statuses["bad.json"] != "refused" {
		t.Fatal(statuses)
	}
	if got := storedAutomations(t, store)[0]; got.Revision != rebound.Revision {
		t.Fatal("unchanged import revised workflow")
	}
	for _, bad := range []string{`{"swarm_workflow":1,"unknown":true}`, `{"swarm_workflow":1,"scope":{"unknown":true}}`, `{"swarm_workflow":1,"steps":[{"unknown":true}]}`} {
		var d any
		_ = json.Unmarshal([]byte(bad), &d)
		if _, err := controlMCPImportWorkflow(c, map[string]any{"definition": d}); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "outside.json")
	if err := os.WriteFile(outsideFile, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(target, "escape")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"../outside.json", outsideFile, "escape/outside.json"} {
		if _, err := controlMCPImportWorkflow(c, map[string]any{"path": path, "project": "Site"}); err == nil {
			t.Fatalf("import escaped: %s (target %s outside %s)", path, target, outside)
		}
		if _, err := controlMCPExportWorkflow(c, map[string]any{"name": "review", "path": path, "overwrite": true}); err == nil {
			t.Fatalf("export escaped: %s", path)
		}
	}
	after, _ := os.ReadFile(outsideFile)
	if string(after) != string(data) {
		t.Fatal("outside file changed")
	}
	if err := os.WriteFile(filepath.Join(target, "different.json"), []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := controlMCPExportWorkflow(c, map[string]any{"name": "review", "path": "different.json"}); err == nil {
		t.Fatal("overwrote different file")
	}
	if got, _ := os.ReadFile(filepath.Join(target, "different.json")); string(got) != "preserve" {
		t.Fatal("file changed")
	}
	if _, err := controlMCPExportWorkflow(c, map[string]any{"name": "review", "path": "different.json", "overwrite": true}); err != nil {
		t.Fatal(err)
	}
	if len(storedAutomations(t, store)) != 1 {
		t.Fatal("refused import stored workflow")
	}
	if err := store.Delete(testPrincipal().AccountScopeID, rebound.ID); err != nil {
		t.Fatal(err)
	}
	result, err = controlMCPImportWorkflow(c, map[string]any{"path": ".", "project": "Site"})
	if err != nil {
		t.Fatal(err)
	}
	created := false
	for _, raw := range result.(map[string]any)["files"].([]any) {
		f := raw.(map[string]any)
		if f["status"] == "created" {
			created = true
		}
	}
	if !created || len(storedAutomations(t, store)) != 1 {
		t.Fatal("directory did not create valid definition")
	}
}

// Requirement/threat/owners/layer: same transfer boundary as above, specifically
// runStepOwnerError must reject write-key imports before any persisted mutation.
func TestWorkflowImportRunOwnerHTTP(t *testing.T) {
	server, store := newRunOwnerServer(t)
	body := `{"definition":{"swarm_workflow":1,"name":"shell","scope":{"folder":"/srv/site"},"steps":[` + shellRunStep + `]}}`
	r := requestWithScopedToken(withTestPrincipal(httptest.NewRequest("POST", JobAutomationsPath+"/import", strings.NewReader(body))), &pebblestore.ScopedTokenRecord{Scopes: scopesForLevel(t, "write")})
	w := httptest.NewRecorder()
	server.handleJobAutomations(w, r)
	if w.Code != 403 || !strings.Contains(w.Body.String(), "only the machine's owner") {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if len(storedAutomations(t, store)) != 0 {
		t.Fatal("refused command saved")
	}
	owner := withTestPrincipal(httptest.NewRequest("POST", JobAutomationsPath+"/import", strings.NewReader(body)))
	w = httptest.NewRecorder()
	server.handleJobAutomations(w, owner)
	if w.Code != 200 || len(storedAutomations(t, store)) != 1 {
		t.Fatalf("local owner: %d %s", w.Code, w.Body)
	}
}

// Requirement: export writes belong only to account project folders, not any
// absolute scope accepted by set. Prevent write keys from creating directories
// or replacing host files. controlMCPExportWorkflow/workflowExportFolder own
// this boundary; real-store MCP calls prove rejection and filesystem postconditions.
func TestWorkflowExportRequiresProjectFolder(t *testing.T) {
	server, _ := newRunOwnerServer(t)
	outside, project := t.TempDir(), t.TempDir()
	c := &controlMCPCall{server: server, request: requestWithScopedToken(withTestPrincipal(httptest.NewRequest("POST", "/mcp", nil)), &pebblestore.ScopedTokenRecord{Scopes: scopesForLevel(t, "write")})}
	c.next = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v3/projects" {
			t.Fatalf("unexpected route %s", r.URL.Path)
		}
		writeJSON(w, 200, map[string]any{"projects": []any{map[string]any{"id": "proj-1", "workspaces": []any{map[string]any{"path": project}}}}})
	})
	wireWorkflowRoutes(c)
	if _, err := controlMCPSetAutomation(c, map[string]any{"name": "outside", "scope": map[string]any{"folder": outside}, "steps": []any{map[string]any{"type": "agent", "worker": "reviewer", "brief": "review"}}}); err != nil {
		t.Fatal(err)
	}
	existing := filepath.Join(outside, "existing.json")
	if err := os.WriteFile(existing, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"", "new/nested/workflow.json", "existing.json"} {
		if _, err := controlMCPExportWorkflow(c, map[string]any{"name": "outside", "path": path, "overwrite": true}); err == nil || !strings.Contains(err.Error(), "not an account project folder") {
			t.Fatalf("path %q: %v", path, err)
		}
	}
	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "existing.json" {
		t.Fatalf("export created files or directories: %v", entries)
	}
	data, err := os.ReadFile(existing)
	if err != nil || string(data) != "preserve" {
		t.Fatalf("export changed existing file: %q, %v", data, err)
	}
	// Returning JSON alone does not grant filesystem access and remains allowed.
	if _, err := controlMCPExportWorkflow(c, map[string]any{"name": "outside"}); err != nil {
		t.Fatal(err)
	}
}

// Requirement: write-key exports may only target JSON outside Git metadata.
// Threat: overwrite could corrupt source or repository authority. The real-store
// controlMCPExportWorkflow boundary is the narrowest layer proving rejection and
// byte-for-byte preservation, including .git files and nested .git components.
func TestWorkflowExportProtectsTargets(t *testing.T) {
	server, _ := newRunOwnerServer(t)
	folder := t.TempDir()
	c := &controlMCPCall{server: server, request: requestWithScopedToken(withTestPrincipal(httptest.NewRequest("POST", "/mcp", nil)), &pebblestore.ScopedTokenRecord{Scopes: scopesForLevel(t, "write")})}
	c.next = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"projects": []any{map[string]any{"id": "proj-1", "workspaces": []any{map[string]any{"path": folder}}}}})
	})
	wireWorkflowRoutes(c)
	if _, err := controlMCPSetAutomation(c, map[string]any{"name": "protected", "scope": map[string]any{"folder": folder}, "steps": []any{map[string]any{"type": "agent", "worker": "reviewer", "brief": "review"}}}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{".git/config", ".git/HEAD", ".git/workflow.json", "nested/.git/config.json", "worktree/.git", "source.go"} {
		t.Run(path, func(t *testing.T) {
			full := filepath.Join(folder, path)
			if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
				t.Fatal(err)
			}
			original := []byte("original contents\n")
			if err := os.WriteFile(full, original, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := controlMCPExportWorkflow(c, map[string]any{"name": "protected", "path": path, "overwrite": true}); err == nil {
				t.Fatal("unsafe target accepted")
			}
			got, err := os.ReadFile(full)
			if err != nil || string(got) != string(original) {
				t.Fatalf("target changed: %q %v", got, err)
			}
			if _, err := os.Stat(full + ".swarm-new"); !os.IsNotExist(err) {
				t.Fatalf("temporary export left behind: %v", err)
			}
		})
	}
}

// Requirement: HTTP JSON export has the same read-scope contract as MCP, but
// file output still needs write scope. handleJobAutomations/handleWorkflowTransfer
// own this boundary; handler calls verify the definition and denied-write
// postcondition without involving transport or an unrelated server lifecycle.
func TestWorkflowExportReadScopeHTTP(t *testing.T) {
	server, _ := newRunOwnerServer(t)
	folder := t.TempDir()
	owner := &controlMCPCall{server: server, request: requestWithScopedToken(withTestPrincipal(httptest.NewRequest("POST", "/mcp", nil)), &pebblestore.ScopedTokenRecord{Scopes: scopesForLevel(t, "write")})}
	wireWorkflowRoutes(owner)
	if _, err := controlMCPSetAutomation(owner, map[string]any{"name": "readable", "scope": map[string]any{"folder": folder}, "steps": []any{map[string]any{"type": "agent", "worker": "reviewer", "brief": "review"}}}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		body   string
		status int
	}{
		{`{"name":"readable"}`, http.StatusOK},
		{`{"name":"readable","path":""}`, http.StatusForbidden},
		{`{"name":"readable","path":"workflow.json","overwrite":true}`, http.StatusForbidden},
	} {
		r := requestWithScopedToken(withTestPrincipal(httptest.NewRequest("POST", JobAutomationsPath+"/export", strings.NewReader(tc.body))), &pebblestore.ScopedTokenRecord{Scopes: []string{"automations:read"}})
		w := httptest.NewRecorder()
		server.handleJobAutomations(w, r)
		if w.Code != tc.status {
			t.Fatalf("%s: %d %s", tc.body, w.Code, w.Body)
		}
		if tc.status == http.StatusOK {
			d, err := decodeWorkflow(w.Body.Bytes())
			if err != nil || d.Name != "readable" || d.Scope.Folder != "" {
				t.Fatalf("invalid portable export: %+v %v", d, err)
			}
		}
	}
	entries, err := os.ReadDir(folder)
	if err != nil || len(entries) != 0 {
		t.Fatalf("read-only export wrote files: %v %v", entries, err)
	}
}

// Purpose: ImportWorkflow must reject instance overrides on directory imports
// before saving any file, preventing silent name collisions and partial writes.
// MCP dispatch over the real store is the narrowest shared-service proof; the
// subsequent unbound directory import proves distinct definitions stay distinct.
func TestWorkflowDirectoryImportRefusesOverrides(t *testing.T) {
	server, store := newRunOwnerServer(t)
	folder := t.TempDir()
	c := &controlMCPCall{server: server, request: requestWithScopedToken(withTestPrincipal(httptest.NewRequest("POST", "/mcp", nil)), &pebblestore.ScopedTokenRecord{Scopes: scopesForLevel(t, "full")})}
	c.next = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v3/projects" {
			t.Errorf("unexpected route %s", r.URL.Path)
		}
		writeJSON(w, 200, map[string]any{"projects": []any{map[string]any{"id": "proj-1", "name": "Site", "workspaces": []any{map[string]any{"path": folder, "workspace_id": "ws-1", "role": "primary_code"}}}}})
	})
	wireWorkflowRoutes(c)
	for _, name := range []string{"first", "second"} {
		data, err := json.Marshal(map[string]any{"swarm_workflow": 1, "name": name, "scope": map[string]any{}, "steps": []any{map[string]any{"type": "agent", "worker": "coder", "brief": name}}})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(folder, name+".json"), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, field := range []string{"name", "with"} {
		args := map[string]any{"path": ".", "project": "Site"}
		if field == "name" {
			args[field] = "merged"
		} else {
			args[field] = map[string]string{"brand": "swarm"}
		}
		_, err := controlMCPImportWorkflow(c, args)
		if err == nil || !strings.Contains(err.Error(), "single-file") {
			t.Fatalf("expected directory override refusal: %v", err)
		}
		if len(storedAutomations(t, store)) != 0 {
			t.Fatal("refused import mutated store")
		}
	}
	if _, err := controlMCPImportWorkflow(c, map[string]any{"path": ".", "project": "Site"}); err != nil {
		t.Fatal(err)
	}
	saved := storedAutomations(t, store)
	if len(saved) != 2 {
		t.Fatalf("expected two workflows: %+v", saved)
	}
	names := map[string]bool{}
	for _, a := range saved {
		names[a.Name] = true
		if a.Steps[0].Brief != a.Name {
			t.Fatal("definition overwritten")
		}
	}
	if !names["first"] || !names["second"] {
		t.Fatal(names)
	}
}

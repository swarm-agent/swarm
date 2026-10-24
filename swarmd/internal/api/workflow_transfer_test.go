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
// shared controlMCPSaveAutomation/automationSteps boundary. Handler-level calls
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
		writeJSON(w, 200, map[string]any{"projects": []any{map[string]any{"id": "proj-1", "name": "Site", "workspaces": []any{map[string]any{"path": target, "workspace_id": "ws-1", "role": "primary_code"}}}}})
	})
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
	statuses := map[string]string{}
	for _, f := range result.(map[string]any)["files"].([]map[string]any) {
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
	for _, f := range result.(map[string]any)["files"].([]map[string]any) {
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

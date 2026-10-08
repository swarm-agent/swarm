package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"swarm/packages/swarmd/internal/identity"
	topologyruntime "swarm/packages/swarmd/internal/topology"
	"syscall"
	"testing"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: projectSourceInput is the narrow filesystem boundary for provider
// context. Bounded regular-file reads must reject symlink/FIFO escapes, tolerate
// missing docs and non-Git roots, and never write project.md to source folders.
func TestProjectContextBoundedSources(t *testing.T) {
	root := t.TempDir()
	text, err := projectSourceInput(root, 2500)
	if err != nil || !strings.Contains(text, "not present") {
		t.Fatalf("missing docs: %q %v", text, err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte(strings.Repeat("x", 100000)), 0600); err != nil {
		t.Fatal(err)
	}
	text, err = projectSourceInput(root, 2500)
	if err != nil || len(text) > 2500 {
		t.Fatalf("bound: %d %v", len(text), err)
	}
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("never read"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "AGENTS.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := projectSourceInput(root, 2500); err == nil {
		t.Fatal("symlink accepted")
	}
	fifoRoot := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(fifoRoot, "README.md"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := projectSourceInput(fifoRoot, 2500); err == nil {
		t.Fatal("FIFO accepted")
	}
	if _, err := os.Stat(filepath.Join(root, "project.md")); !os.IsNotExist(err) {
		t.Fatal("source mutated")
	}
}

// Purpose: handleProjects/CreateProject must reject unauthorized source bindings
// before reservation/provider calls, and publish only configured provider output.
// HTTP+real temporary store with a recording provider is the narrow deterministic
// contract test, not a provider-backed benchmark or live generation claim.
func TestProjectContextCreationHTTP(t *testing.T) {
	fixture, _, _ := newWorkspaceOverviewTopologyTestServer(t)
	server := &Server{sessions: fixture.sessions}
	runner := &sessionRouterRecordingRunner{id: "recording", response: provideriface.Response{Text: "# Generated project\nEvidence-based context."}}
	root := t.TempDir()
	configured, p, entries := newSessionRouterTestServer(t, runner, []sessionRouterWorkspace{{root, "Docs", "workspace output unchanged"}})
	server.workspace = configured.workspace
	server.providers = configured.providers
	server.model = configured.model
	server.agents = configured.agents
	server.agentModelSettings = configured.agentModelSettings
	refs := []pebblestore.ProjectWorkspaceRef{{Path: root, WorkspaceID: entries[0].WorkspaceID}}
	foreign := p
	foreign.AccountScopeID = "foreign"
	for _, principal := range []bool{false, true} {
		who := p
		bad := []pebblestore.ProjectWorkspaceRef{{Path: t.TempDir()}}
		if principal {
			who = foreign
			bad = refs
		}
		w := projectIdentityRequest(t, server, who, http.MethodPost, "", map[string]any{"name": "Denied", "client_request_id": "denied", "workspaces": bad})
		if w.Code != 400 {
			t.Fatalf("unauthorized: %d %s", w.Code, w.Body.String())
		}
		records, err := server.sessions.Store().ListProjects(who.AccountScopeID, 100)
		if err != nil || len(records) != 0 || runner.createCalls != 0 {
			t.Fatal("unauthorized side effect")
		}
	}
	body := map[string]any{"name": "Project", "client_request_id": "create-once", "workspaces": refs, "project_context": "fabricated"}
	w := projectIdentityRequest(t, server, p, http.MethodPost, "", body)
	if w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var response struct {
		Project pebblestore.ProjectRecord `json:"project"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	server.runWG.Wait()
	ready, _, err := server.sessions.Store().GetProject(p.AccountScopeID, response.Project.ID)
	if err != nil || ready.ProjectContext != runner.response.Text || ready.ContextGeneration.Status != "ready" || runner.createCalls != 1 {
		t.Fatalf("generation: %+v %v", ready, err)
	}
	if runner.requests[0].Model != "router-model" || runner.requests[0].ToolChoice != "none" || len(runner.requests[0].Tools) != 0 {
		t.Fatal("configured tool-free execution lost")
	}
	w = projectIdentityRequest(t, server, p, http.MethodPost, "", body)
	server.runWG.Wait()
	if w.Code != 201 || runner.createCalls != 1 {
		t.Fatalf("duplicate generation: %d calls=%d", w.Code, runner.createCalls)
	}
	body["name"] = "Conflict"
	w = projectIdentityRequest(t, server, p, http.MethodPost, "", body)
	if w.Code != 409 || runner.createCalls != 1 {
		t.Fatal("idempotency conflict not rejected")
	}
	// Provider errors remain durable and retryable; no fake success or new project.
	runner.err = errors.New("provider unavailable")
	body["client_request_id"] = "failure"
	w = projectIdentityRequest(t, server, p, http.MethodPost, "", body)
	server.runWG.Wait()
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &response) != nil {
		t.Fatal(w.Body.String())
	}
	failed, _, _ := server.sessions.Store().GetProject(p.AccountScopeID, response.Project.ID)
	if failed.ContextGeneration.Status != "failed" || failed.ProjectContext != "" {
		t.Fatalf("false success: %+v", failed)
	}
	runner.err = nil
	w = projectIdentityRequest(t, server, p, http.MethodPost, "/"+failed.ID+"/context:retry", map[string]any{"expected_attempt": 1})
	server.runWG.Wait()
	retried, _, _ := server.sessions.Store().GetProject(p.AccountScopeID, failed.ID)
	if w.Code != 202 || retried.ContextGeneration.Status != "ready" || retried.ContextGeneration.Attempt != 2 {
		t.Fatalf("retry: %s %+v", w.Body.String(), retried)
	}
	all, _ := server.sessions.Store().ListProjects(p.AccountScopeID, 100)
	if len(all) != 2 {
		t.Fatalf("duplicates: %d", len(all))
	}
	// Workspace updates also reject raw/foreign roots without changing bindings.
	w = projectIdentityRequest(t, server, p, http.MethodPatch, "/"+ready.ID, map[string]any{"workspaces": []pebblestore.ProjectWorkspaceRef{{Path: t.TempDir()}}, "name": "unauthorized"})
	unchanged, _, _ := server.sessions.Store().GetProject(p.AccountScopeID, ready.ID)
	if w.Code != 400 || unchanged.Name != ready.Name || len(unchanged.Workspaces) != 1 || unchanged.Workspaces[0].WorkspaceID != entries[0].WorkspaceID {
		t.Fatal("rejected workspace patch changed state")
	}
	w = projectIdentityRequest(t, server, foreign, http.MethodPost, "/"+failed.ID+"/context:retry", map[string]any{"expected_attempt": 2})
	if w.Code != 404 {
		t.Fatal("cross-account retry admitted")
	}
	// Non-Git context sources never gain coding admission.
	if _, err := server.resolveProjectTaskSource(p, ready, root, entries[0].WorkspaceID, 0, true); err == nil {
		t.Fatal("non-Git coding admitted")
	}
	if _, err := server.buildProjectContextInput(p, ready); err != nil {
		t.Fatal(err)
	}
	if _, err := server.CreateProject(context.Background(), p, pebblestore.ProjectRecord{ID: ready.ID, Name: "overwrite"}, "overwrite"); err == nil {
		t.Fatal("caller ID overwrite")
	}
}

// Purpose: explicit context-only registration uses handleWorkspaceAdd and the
// catalog/local-binding authority, without Git initialization or selection. Mixed
// sources remain readable, while non-Git coding admission stays rejected.
func TestProjectContextFolderRegistration(t *testing.T) {
	server, repo, db := newWorkspaceOverviewTopologyTestServer(t)
	swarmStore := pebblestore.NewSwarmStore(db)
	if _, err := swarmStore.PutLocalNode(pebblestore.SwarmLocalNodeRecord{SwarmID: "host-swarm-id", Name: "host", Role: "host"}); err != nil {
		t.Fatal(err)
	}
	server.SetTopologyService(topologyruntime.NewService(pebblestore.NewTopologyStore(db), swarmStore))
	p := testPrincipal()
	folder := t.TempDir()
	payload, _ := json.Marshal(map[string]any{"path": folder, "name": "Reference", "context_only": true, "make_current": false})
	r := httptest.NewRequest(http.MethodPost, "/api/workspaces/add", bytes.NewReader(payload))
	ctx := context.WithValue(r.Context(), productPrincipalRequestContextKey, p)
	ctx = context.WithValue(ctx, productScopedTokenRequestContextKey, &pebblestore.ScopedTokenRecord{AccountScopeID: p.AccountScopeID, UserID: p.UserID, Scopes: []string{"workspaces:write"}})
	w := httptest.NewRecorder()
	server.handleWorkspaceAdd(w, r.WithContext(ctx))
	if w.Code != 200 {
		t.Fatalf("registration: %d %s", w.Code, w.Body.String())
	}
	scope, err := server.workspace.ScopeForPathForPrincipal(p, folder)
	if err != nil || !scope.Matched || scope.WorkspaceID == "" {
		t.Fatalf("catalog: %+v %v", scope, err)
	}
	if _, err := os.Stat(filepath.Join(folder, ".git")); !os.IsNotExist(err) {
		t.Fatal("implicit Git initialization")
	}
	refs, err := server.authorizeProjectWorkspaces(p, []pebblestore.ProjectWorkspaceRef{{Path: folder}, {Path: repo}})
	if err != nil || len(refs) != 2 {
		t.Fatalf("mixed sources: %+v %v", refs, err)
	}
	project := &pebblestore.ProjectRecord{Name: "Mixed", Workspaces: refs}
	if _, err := server.buildProjectContextInput(p, project); err != nil {
		t.Fatal(err)
	}
	if _, err := server.resolveProjectTaskSource(p, project, folder, scope.WorkspaceID, 0, true); err == nil {
		t.Fatal("non-Git task admitted")
	}
	if _, err := server.resolveProjectTaskSource(p, project, repo, "", 0, true); err != nil {
		t.Fatal(err)
	}
}

// Purpose: project-only Router fallback uses canonical account Swarm settings
// and returns a visible warning; other Router consumers retain fail-closed behavior.
// Recording provider requests prove model/tool selection without live credentials.
func TestProjectContextRouterFallback(t *testing.T) {
	runner := &sessionRouterRecordingRunner{id: "recording", response: provideriface.Response{Text: "# Context"}}
	server, p, _ := newSessionRouterTestServer(t, runner, nil)
	settings, err := server.agentModelSettings.GetForAccount(p.AccountScopeID)
	if err != nil {
		t.Fatal(err)
	}
	settings.SystemAgents.Router.Provider = "missing"
	settings.SystemAgents.Router.Model = "missing"
	settings.Swarm.Action = settings.SystemAgents.Router
	settings.Swarm.Action.Provider = "recording"
	settings.Swarm.Action.Model = "router-model"
	ctx := identity.ContextWithPrincipal(context.Background(), p)
	if _, err := server.agentModelSettings.UpdateSwarmSlot(ctx, "action", settings.Swarm.Action); err != nil {
		t.Fatal(err)
	}
	if _, err := server.agentModelSettings.UpdateSystemAgent(ctx, "router", settings.SystemAgents.Router); err != nil {
		t.Fatal(err)
	}
	output, err := server.invokeConfiguredRouterOnce(context.Background(), p, "Generate project context", "project", 12000, true)
	if err != nil || output.RouterAlert == "" || runner.createCalls != 1 || runner.requests[0].Model != "router-model" {
		t.Fatalf("fallback: %+v %v", output, err)
	}
}

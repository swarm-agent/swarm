package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type routingLockCheckingRunner struct {
	*sessionRouterRecordingRunner
	server *Server
}

func (r *routingLockCheckingRunner) CreateResponse(ctx context.Context, request provideriface.Request) (provideriface.Response, error) {
	if !r.server.projectTaskCreateMu.TryLock() {
		return provideriface.Response{}, errors.New("provider invoked under broad task creation lock")
	}
	r.server.projectTaskCreateMu.Unlock()
	return r.sessionRouterRecordingRunner.CreateResponse(ctx, request)
}

func routingFixture(t *testing.T) (*matrixTestFixture, identity.Principal, *pebblestore.ProjectRecord, pebblestore.ProjectTaskSource, *sessionRouterRecordingRunner) {
	t.Helper()
	f := setupMatrixTestFixture(t)
	t.Cleanup(func() { f.db.Close() })
	projectID := f.createProject(t)
	p := identity.Principal{Type: identity.PrincipalTypeUser, UserID: f.userID, AccountScopeID: f.accountID}
	proj, found, err := f.server.sessions.Store().GetProject(f.accountID, projectID)
	if err != nil || !found {
		t.Fatalf("project: %v", err)
	}
	contextPath := filepath.Join(f.dir, "context")
	if err := os.Mkdir(contextPath, 0700); err != nil {
		t.Fatal(err)
	}
	entry, err := pebblestore.NewWorkspaceStore(f.db).AddForAccount(f.accountID, contextPath, "context")
	if err != nil {
		t.Fatal(err)
	}
	proj.Workspaces = append(proj.Workspaces, pebblestore.ProjectWorkspaceRef{WorkspaceID: entry.WorkspaceID, Path: contextPath})
	if err := f.server.sessions.Store().PutProject(f.accountID, proj); err != nil {
		t.Fatal(err)
	}
	source, err := f.server.resolveProjectTaskSource(p, proj, proj.Workspaces[0].Path, proj.Workspaces[0].WorkspaceID, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	contextSource, err := f.server.resolveProjectTaskSource(p, proj, contextPath, entry.WorkspaceID, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{"source": source, "context": []pebblestore.ProjectTaskSource{contextSource}})
	if err != nil {
		t.Fatal(err)
	}
	runner := &sessionRouterRecordingRunner{id: "recording", response: provideriface.Response{Text: string(raw)}}
	configureManualNamingRouter(t, f, p, runner)
	f.server.providers.RegisterRunner(&routingLockCheckingRunner{sessionRouterRecordingRunner: runner, server: f.server})
	return f, p, proj, source, runner
}

// Purpose: CreateProjectTask must admit the configured Router's exact authorized
// source for an audit, retain read-only context and user options, and leave confirmation pending.
// Coding tasks instead require an explicit source, covered by TestProjectCodingSourceAdmission.
// Threat: preview/retry could call providers, change the source, or launch before
// approval. This hermetic API/store fixture proves the real admission boundary.
func TestAutomaticProjectTaskRoutingAdmissionAndReplay(t *testing.T) {
	f, p, proj, source, runner := routingFixture(t)
	if _, err := f.server.agentModelSettings.UpdateSystemAgent(identity.ContextWithPrincipal(context.Background(), p), "finder", pebblestore.AgentModelAssignment{Provider: "recording", Model: "router-model", Thinking: "high", ServiceTier: "priority"}); err != nil {
		t.Fatal(err)
	}
	preview := f.callAPI(http.MethodPost, "/"+proj.ID+"/tasks:preview", map[string]any{"agent": "finder", "prompt": "Fix repo error"}, p)
	if preview.Code != http.StatusOK || !strings.Contains(preview.Body.String(), `"model":"router-model"`) || runner.createCalls != 0 || f.wt.allocCalls != 0 {
		t.Fatalf("preview routed or allocated: %d %s calls=%d allocations=%d", preview.Code, preview.Body.String(), runner.createCalls, f.wt.allocCalls)
	}
	for _, selection := range []map[string]any{
		{"workspace_path": filepath.Join(f.dir, "unknown")},
		{"workspace_path": source.Path, "workspace_id": source.WorkspaceID, "workspace_generation": source.WorkspaceGeneration + 1},
	} {
		selection["agent"], selection["prompt"] = "finder", "Fix repo error"
		w := f.callAPI(http.MethodPost, "/"+proj.ID+"/tasks:preview", selection, p)
		var response struct {
			Diagnostic string           `json:"workspace_diagnostic"`
			Preview    TaskModelPreview `json:"model_preview"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || w.Code != http.StatusOK || response.Diagnostic == "" || response.Preview.ResolvedModel == nil || response.Preview.ResolvedModel.Model != "router-model" || runner.createCalls != 0 {
			t.Fatalf("workspace hid configured model: %d %s err=%v", w.Code, w.Body.String(), err)
		}
	}
	foreign := p
	foreign.AccountScopeID = "foreign-account"
	if w := f.callAPI(http.MethodPost, "/"+proj.ID+"/tasks:preview", map[string]any{"agent": "finder"}, foreign); w.Code == http.StatusOK {
		t.Fatal("cross-account project preview accepted")
	}
	body := map[string]any{"id": "automatic-routing", "title": "Fix error", "prompt": "Fix the repo error exactly as reported", "agent": "finder", "feature_size": "small", "auto_approve": false}
	created := requireMatrixTaskResponse(t, f.callAPI(http.MethodPost, "/"+proj.ID+"/tasks", body, p), http.StatusCreated)
	if created["status"] != "pending_approval" || runner.createCalls != 1 {
		t.Fatalf("confirmation/calls: %+v %d", created, runner.createCalls)
	}
	saved, found, err := f.server.sessions.Store().GetProjectTask(f.accountID, proj.ID, "automatic-routing")
	if err != nil || !found || saved.SourceWorkspace.Path != source.Path || saved.SourceWorkspace.Provenance != "router" || len(saved.ContextSources) != 1 || len(saved.ProgramSources) != 0 || saved.Description != body["prompt"] || saved.Agent != "finder" || saved.FeatureSize != "small" {
		t.Fatalf("admission: %+v found=%v err=%v", saved, found, err)
	}
	if len(saved.WorkspacesInvolved) != 2 || !strings.Contains(saved.ContextPoolSummary, "read-only context") {
		t.Fatalf("review omitted involved workspaces: %+v", saved)
	}
	if saved.SessionID != "" {
		intents, err := f.server.sessions.Store().ListRunIntents(saved.SessionID, 10)
		if err != nil || len(intents) != 0 {
			t.Fatalf("unapproved run: %+v %v", intents, err)
		}
	}
	staleContext := *saved
	staleContext.ContextSources = append([]pebblestore.ProjectTaskSource(nil), saved.ContextSources...)
	staleContext.ContextSources[0].WorkspaceGeneration++
	if err := f.server.revalidateProjectTaskSource(p, proj, &staleContext); err == nil {
		t.Fatal("launch accepted stale context binding")
	}
	runner.err = errors.New("Router unavailable after admission")
	replayed := requireMatrixTaskResponse(t, f.callAPI(http.MethodPost, "/"+proj.ID+"/tasks", body, p), http.StatusCreated)
	if replayed["id"] != created["id"] || runner.createCalls != 1 {
		t.Fatalf("rerouted replay: %+v calls=%d", replayed, runner.createCalls)
	}
	body["prompt"] = "Changed contract"
	if w := f.callAPI(http.MethodPost, "/"+proj.ID+"/tasks", body, p); w.Code != http.StatusBadRequest {
		t.Fatalf("changed replay accepted: %d %s", w.Code, w.Body.String())
	}
	body["id"], body["workspace_path"], body["workspace_id"] = "manual-routing", source.Path, source.WorkspaceID
	requireMatrixTaskResponse(t, f.callAPI(http.MethodPost, "/"+proj.ID+"/tasks", body, p), http.StatusCreated)
	if runner.createCalls != 1 {
		t.Fatal("manual source called workspace Router")
	}
}

// Purpose: Router proposals are never source authority. Unknown, incomplete,
// stale and conflicting bindings (including context) must reject before any
// reservation/allocation. API/store is the narrowest layer proving postconditions.
func TestAutomaticProjectTaskRoutingRejectsInvalidWithoutReservation(t *testing.T) {
	for _, name := range []string{"unknown", "stale", "conflict", "missing", "context", "uncertain", "provider"} {
		t.Run(name, func(t *testing.T) {
			f, p, proj, source, runner := routingFixture(t)
			selection := map[string]any{"source": source}
			switch name {
			case "unknown":
				source.Path = filepath.Join(f.dir, "unregistered")
			case "stale":
				source.WorkspaceGeneration++
			case "conflict":
				source.WorkspaceID = proj.Workspaces[1].WorkspaceID
			case "missing":
				source.WorkspaceGeneration = 0
			case "context":
				selection["context"] = []pebblestore.ProjectTaskSource{{WorkspaceID: "foreign", WorkspaceGeneration: 1, Path: filepath.Join(f.dir, "foreign")}}
			case "uncertain":
				selection["diagnostic"] = "Choose which repository to modify"
			case "provider":
				runner.err = errors.New("unavailable")
			}
			selection["source"] = source
			raw, err := json.Marshal(selection)
			if err != nil {
				t.Fatal(err)
			}
			runner.response.Text = string(raw)
			w := f.callAPI(http.MethodPost, "/"+proj.ID+"/tasks", map[string]any{"id": "invalid-route", "title": "Fix", "prompt": "Fix error", "agent": "finder"}, p)
			if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "workspace routing") {
				t.Fatalf("invalid route: %d %s", w.Code, w.Body.String())
			}
			if _, found, err := f.server.sessions.Store().GetProjectTask(f.accountID, proj.ID, "invalid-route"); err != nil || found || f.wt.allocCalls != 0 {
				t.Fatalf("partial state: found=%v err=%v allocations=%d", found, err, f.wt.allocCalls)
			}
		})
	}
}

// Purpose: duplicate automatic submissions serialize one routing/admission while
// unrelated identities remain independently routable. Threat: concurrent AI
// choices can retarget retries or duplicate sessions. Real API/store postconditions
// are required, rather than testing a mutex in isolation.
func TestAutomaticProjectTaskRoutingConcurrentDuplicate(t *testing.T) {
	f, p, proj, _, runner := routingFixture(t)
	body := map[string]any{"id": "duplicate-routing", "title": "Fix", "prompt": "Fix repo", "agent": "finder"}
	start := make(chan struct{})
	results := make(chan *httpResponseForRouting, 2)
	for i := 0; i < 2; i++ {
		go func() {
			<-start
			w := f.callAPI(http.MethodPost, "/"+proj.ID+"/tasks", body, p)
			results <- &httpResponseForRouting{code: w.Code, body: w.Body.String()}
		}()
	}
	close(start)
	for i := 0; i < 2; i++ {
		select {
		case result := <-results:
			if result.code != http.StatusCreated {
				t.Fatalf("duplicate: %d %s", result.code, result.body)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("duplicate admission did not complete")
		}
	}
	if runner.createCalls != 1 {
		t.Fatalf("duplicate called Router %d times", runner.createCalls)
	}
	saved, found, err := f.server.sessions.Store().GetProjectTask(f.accountID, proj.ID, "duplicate-routing")
	if err != nil || !found || saved.Status != "pending_approval" {
		t.Fatalf("durable admission: %+v %v", saved, err)
	}
}

type httpResponseForRouting struct {
	code int
	body string
}

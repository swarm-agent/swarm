package api

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"swarm-refactor/swarmtui/pkg/startupconfig"
	agentruntime "swarm/packages/swarmd/internal/agent"
	integrationruntime "swarm/packages/swarmd/internal/integration"
	"swarm/packages/swarmd/internal/permission"
	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/stream"
	swarmruntime "swarm/packages/swarmd/internal/swarm"
	"swarm/packages/swarmd/internal/tool"
)

func TestIntegrationWorkspaceOpenCreatesLatestChildAndContext(t *testing.T) {
	server, sessions, closeStore := newIntegrationSessionTestServer(t, nil)
	defer closeStore()
	handler := withTargetedAgentTestPrincipal(server.Handler())

	var response struct {
		OK        bool                                   `json:"ok"`
		Workspace pebblestore.IntegrationWorkspaceRecord `json:"workspace"`
		Session   pebblestore.SessionSnapshot            `json:"session"`
		Sessions  []integrationWorkspaceChildSession     `json:"sessions"`
	}
	status := doJSONRequestLocal(t, handler, http.MethodPost, "/v1/integrations/workspaces", map[string]any{
		"workspace_id":     "Spotify Draft",
		"display_name":     "Spotify integration",
		"pack_id":          "Spotify",
		"draft_version_id": "Draft",
		"create_child":     true,
		"title":            "Review Spotify",
		"preference":       map[string]any{"provider": "codex", "model": "gpt-5.4", "thinking": "medium"},
	}, &response)
	if status != http.StatusOK || !response.OK {
		t.Fatalf("status=%d response=%+v", status, response)
	}
	if response.Workspace.WorkspaceID != "spotify draft" || response.Workspace.LatestChildSessionID != response.Session.ID {
		t.Fatalf("workspace latest child not set: workspace=%+v session=%+v", response.Workspace, response.Session)
	}
	if len(response.Sessions) != 1 || response.Sessions[0].Session.ID != response.Session.ID {
		t.Fatalf("child list = %+v, want created session", response.Sessions)
	}

	created, ok, err := sessions.GetSession(response.Session.ID)
	if err != nil || !ok {
		t.Fatalf("created session lookup ok=%v err=%v", ok, err)
	}
	if got := created.Metadata["source"]; got != integrationBuilderSessionSource {
		t.Fatalf("session source metadata = %v", got)
	}
	if got := created.Metadata[integrationSessionContextKeyWorkspaceID]; got != "spotify draft" {
		t.Fatalf("workspace context metadata = %v", got)
	}
	if got := created.Metadata[integrationSessionContextKeyPackID]; got != "spotify" {
		t.Fatalf("pack context metadata = %v", got)
	}
	if got := created.Metadata[integrationSessionContextKeyDraftVersionID]; got != "draft" {
		t.Fatalf("draft context metadata = %v", got)
	}
}

func TestIntegrationWorkspaceSessionNewSwitchAndRunContext(t *testing.T) {
	runner := &recordingRunService{}
	server, _, closeStore := newIntegrationSessionTestServer(t, runner)
	defer closeStore()
	handler := withTargetedAgentTestPrincipal(server.Handler())

	var opened struct {
		Session pebblestore.SessionSnapshot `json:"session"`
	}
	status := doJSONRequestLocal(t, handler, http.MethodPost, "/v1/integrations/workspaces", map[string]any{
		"workspace_id":     "Demo",
		"display_name":     "Demo integration",
		"pack_id":          "DemoPack",
		"draft_version_id": "DraftOne",
		"create_child":     true,
		"preference":       map[string]any{"provider": "codex", "model": "gpt-5.4", "thinking": "medium"},
	}, &opened)
	if status != http.StatusOK || opened.Session.ID == "" {
		t.Fatalf("open status=%d response=%+v", status, opened)
	}

	var created struct {
		Session pebblestore.SessionSnapshot `json:"session"`
	}
	status = doJSONRequestLocal(t, handler, http.MethodPost, "/v1/integrations/workspaces/demo/sessions", map[string]any{
		"action":     "new",
		"title":      "Second child",
		"preference": map[string]any{"provider": "codex", "model": "gpt-5.4", "thinking": "medium"},
	}, &created)
	if status != http.StatusOK || created.Session.ID == "" || created.Session.ID == opened.Session.ID {
		t.Fatalf("new child status=%d opened=%q created=%q", status, opened.Session.ID, created.Session.ID)
	}

	var snapshot struct {
		Session  pebblestore.SessionSnapshot        `json:"session"`
		Sessions []integrationWorkspaceChildSession `json:"sessions"`
	}
	status = doJSONRequestLocal(t, handler, http.MethodGet, "/v1/integrations/workspaces/demo", nil, &snapshot)
	if status != http.StatusOK {
		t.Fatalf("snapshot status=%d", status)
	}
	if snapshot.Session.ID != created.Session.ID || len(snapshot.Sessions) != 2 || snapshot.Sessions[0].Session.ID != created.Session.ID {
		t.Fatalf("latest selection/list = session:%q children:%+v", snapshot.Session.ID, snapshot.Sessions)
	}

	var switched struct {
		Session pebblestore.SessionSnapshot `json:"session"`
	}
	status = doJSONRequestLocal(t, handler, http.MethodGet, "/v1/integrations/workspaces/demo/sessions/"+opened.Session.ID, nil, &switched)
	if status != http.StatusOK || switched.Session.ID != opened.Session.ID {
		t.Fatalf("switch status=%d session=%+v", status, switched.Session)
	}

	var runResp map[string]any
	status = doJSONRequestLocal(t, handler, http.MethodPost, "/v1/sessions/"+created.Session.ID+"/run", map[string]any{"prompt": "review this draft"}, &runResp)
	if status != http.StatusOK {
		t.Fatalf("run status=%d response=%+v", status, runResp)
	}
	if runner.sessionID != created.Session.ID || !runner.meta.IntegrationFlow {
		t.Fatalf("runner session/meta = %q %+v", runner.sessionID, runner.meta)
	}
	if runner.request.AgentName != agentruntime.IntegrationBuilderAgentID {
		t.Fatalf("runner agent = %q", runner.request.AgentName)
	}
	if !strings.Contains(runner.request.Instructions, "workspace_id: demo") || !strings.Contains(runner.request.Instructions, "pack_id: demopack") || !strings.Contains(runner.request.Instructions, "draft_version_id: draftone") {
		t.Fatalf("runner instructions missing integration context: %q", runner.request.Instructions)
	}
}

func newIntegrationSessionTestServer(t *testing.T, runner runService) (*Server, *sessionruntime.Service, func()) {
	t.Helper()
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "integration-sessions.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		_ = store.Close()
		t.Fatalf("event log: %v", err)
	}
	sessions := sessionruntime.NewService(pebblestore.NewSessionStore(store), events)
	server := NewServer("desktop", nil, nil, nil, runner, sessions, nil, nil, nil, nil, nil, nil, events, stream.NewHub(events))
	startupPath := filepath.Join(t.TempDir(), "startup.json")
	cfg := startupconfig.Default(startupPath)
	cfg.SwarmName = "local-swarm"
	if err := startupconfig.Write(cfg); err != nil {
		_ = store.Close()
		t.Fatalf("write startup config: %v", err)
	}
	server.SetStartupConfigPath(startupPath)
	server.SetSwarmService(fakeAgentAPISwarmService{state: swarmruntime.LocalState{Node: swarmruntime.LocalNodeState{SwarmID: "local-swarm-id", Name: "local-swarm", Role: "master"}}})
	server.SetIntegrationService(integrationruntime.NewService(pebblestore.NewIntegrationStore(store)))
	return server, sessions, func() { _ = store.Close() }
}

type recordingRunService struct {
	sessionID string
	request   runruntime.RunRequest
	meta      runruntime.RunStartMeta
}

func (r *recordingRunService) RunTurn(_ context.Context, sessionID string, request runruntime.RunRequest, meta runruntime.RunStartMeta) (runruntime.RunResult, error) {
	r.sessionID = sessionID
	r.request = request
	r.meta = meta
	return runruntime.RunResult{SessionID: sessionID}, nil
}

func (r *recordingRunService) RunTurnStreaming(_ context.Context, sessionID string, request runruntime.RunRequest, meta runruntime.RunStartMeta, onEvent runruntime.StreamHandler) (runruntime.RunResult, error) {
	r.sessionID = sessionID
	r.request = request
	r.meta = meta
	return runruntime.RunResult{SessionID: sessionID}, nil
}

func (r *recordingRunService) StopSessionRun(sessionID, runID, reason string) error { return nil }
func (r *recordingRunService) ExecuteToolForSessionScope(_ context.Context, workspacePath string, call tool.Call) (string, error) {
	return "{}", nil
}
func (r *recordingRunService) ListAgentToolDefinitions() []tool.Definition { return nil }
func (r *recordingRunService) ListAgentToolDefinitionsForAccount(accountScopeID string) []tool.Definition {
	return nil
}
func (r *recordingRunService) ResolveAgentToolContract(profile pebblestore.AgentProfile) (runruntime.ResolvedAgentToolContract, *permission.Policy, map[string]bool, error) {
	return runruntime.ResolvedAgentToolContract{}, nil, nil, nil
}
func (r *recordingRunService) ResolveAgentToolContractForAccount(accountScopeID string, profile pebblestore.AgentProfile) (runruntime.ResolvedAgentToolContract, *permission.Policy, map[string]bool, error) {
	return runruntime.ResolvedAgentToolContract{}, nil, nil, nil
}

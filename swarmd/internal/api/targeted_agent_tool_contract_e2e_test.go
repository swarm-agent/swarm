package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"swarm-refactor/swarmtui/pkg/startupconfig"
	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/model"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	"swarm/packages/swarmd/internal/provider/registry"
	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/stream"
	swarmruntime "swarm/packages/swarmd/internal/swarm"
	"swarm/packages/swarmd/internal/tool"
)

func TestTargetedScopedSubagentToolContractAPIEndToEnd(t *testing.T) {
	const allowedCommand = "printf scoped-e2e"

	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "targeted-scoped-subagent-api-e2e.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() {
		_ = store.Close()
	}()

	eventLog, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}
	hub := stream.NewHub(nil)

	modelSvc := model.NewService(pebblestore.NewModelStore(store), eventLog, nil)
	if _, _, err := modelSvc.SetGlobalPreference("codex", "gpt-5-codex", "high"); err != nil {
		t.Fatalf("set global preference: %v", err)
	}

	sessionSvc := sessionruntime.NewService(pebblestore.NewSessionStore(store), eventLog)
	agentSvc := agentruntime.NewService(pebblestore.NewAgentStore(store), eventLog)
	if err := agentSvc.EnsureDefaults(); err != nil {
		t.Fatalf("ensure default agents: %v", err)
	}

	runner := &scopedToolProbeRunner{id: "codex", allowedCommand: allowedCommand}
	providers := registry.New()
	providers.RegisterRunner(runner)

	runSvc := runruntime.NewService(sessionSvc, modelSvc, providers, tool.NewRuntime(2), nil, agentSvc, nil, eventLog)
	server := NewServer("test", nil, agentSvc, modelSvc, runSvc, sessionSvc, nil, nil, nil, providers, nil, nil, eventLog, hub)
	server.SetStartupConfigPath(writeLocalStartupConfig(t))
	server.SetSwarmService(fakeAgentAPISwarmService{
		state: swarmruntime.LocalState{
			Node: swarmruntime.LocalNodeState{
				SwarmID: "local-swarm-id",
				Name:    "local-swarm",
				Role:    "master",
			},
		},
		token: "peer-token",
	})
	handler := server.Handler()

	upsertScopedAgentV2(t, handler, "scopedsub-e2e", "subagent", allowedCommand)
	upsertScopedAgentV2(t, handler, "scopedbg-e2e", "background", allowedCommand)

	resolved := getResolvedToolContractV2(t, handler, "scopedsub-e2e")
	bashState, ok := resolved.Resolved.Tools["bash"]
	if !ok {
		t.Fatalf("resolved tool contract missing bash entry: %+v", resolved.Resolved.Tools)
	}
	if !bashState.Enabled {
		t.Fatalf("resolved bash tool is disabled: %+v", bashState)
	}
	if !containsString(bashState.BashPrefixes, allowedCommand) {
		t.Fatalf("resolved bash prefixes = %v, want %q", bashState.BashPrefixes, allowedCommand)
	}

	workspacePath := t.TempDir()
	session := createSessionWithPreferenceViaAPI(t, handler, workspacePath)

	runner.reset()
	backgroundResult := runTargetedAgentViaAPI(t, handler, session.Session.ID, map[string]any{
		"prompt":      "probe scoped background bash",
		"target_kind": "background",
		"target_name": "scopedbg-e2e",
	})
	backgroundRequests := runner.requestsSnapshot()
	if len(backgroundRequests) == 0 {
		t.Fatalf("expected provider request for targeted background run")
	}
	backgroundTools := toolNamesFromRequest(backgroundRequests[0])
	if !requestContainsTool(backgroundRequests[0], "bash") {
		t.Fatalf("control targeted background run did not advertise bash; tools=%v assistant=%q", backgroundTools, backgroundResult.Result.AssistantMessage.Content)
	}
	if backgroundResult.Result.ToolCallCount != 1 {
		t.Fatalf("control targeted background run tool_call_count=%d, want 1; assistant=%q tools=%v", backgroundResult.Result.ToolCallCount, backgroundResult.Result.AssistantMessage.Content, backgroundTools)
	}
	if !strings.Contains(backgroundResult.Result.AssistantMessage.Content, allowedCommand) {
		t.Fatalf("control targeted background run assistant=%q, want %q", backgroundResult.Result.AssistantMessage.Content, allowedCommand)
	}

	runner.reset()
	subagentResult := runTargetedAgentViaAPI(t, handler, session.Session.ID, map[string]any{
		"prompt":      "probe scoped subagent bash",
		"target_kind": "subagent",
		"target_name": "scopedsub-e2e",
	})
	subagentRequests := runner.requestsSnapshot()
	if len(subagentRequests) == 0 {
		t.Fatalf("expected provider request for targeted subagent child run")
	}
	childTools := toolNamesFromRequest(subagentRequests[0])
	if !requestContainsTool(subagentRequests[0], "bash") {
		t.Fatalf("targeted subagent child request omitted bash despite saved scoped tool_contract\nresolved_bash_prefixes=%v\nbackground_tools=%v\nsubagent_child_session=%s\nsubagent_child_tools=%v\nsubagent_tool_call_count=%d\nsubagent_assistant=%q",
			bashState.BashPrefixes,
			backgroundTools,
			strings.TrimSpace(subagentRequests[0].SessionID),
			childTools,
			subagentResult.Result.ToolCallCount,
			subagentResult.Result.AssistantMessage.Content,
		)
	}
}

func TestTargetedBackgroundFullBashToolContractAPIEndToEnd(t *testing.T) {
	const allowedCommand = "pwd"

	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "targeted-full-bash-api-e2e.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() {
		_ = store.Close()
	}()

	eventLog, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}
	hub := stream.NewHub(nil)

	modelSvc := model.NewService(pebblestore.NewModelStore(store), eventLog, nil)
	if _, _, err := modelSvc.SetGlobalPreference("codex", "gpt-5-codex", "high"); err != nil {
		t.Fatalf("set global preference: %v", err)
	}

	sessionSvc := sessionruntime.NewService(pebblestore.NewSessionStore(store), eventLog)
	agentSvc := agentruntime.NewService(pebblestore.NewAgentStore(store), eventLog)
	if err := agentSvc.EnsureDefaults(); err != nil {
		t.Fatalf("ensure default agents: %v", err)
	}

	runner := &scopedToolProbeRunner{id: "codex", allowedCommand: allowedCommand}
	providers := registry.New()
	providers.RegisterRunner(runner)

	runSvc := runruntime.NewService(sessionSvc, modelSvc, providers, tool.NewRuntime(2), nil, agentSvc, nil, eventLog)
	server := NewServer("test", nil, agentSvc, modelSvc, runSvc, sessionSvc, nil, nil, nil, providers, nil, nil, eventLog, hub)
	server.SetStartupConfigPath(writeLocalStartupConfig(t))
	server.SetSwarmService(fakeAgentAPISwarmService{
		state: swarmruntime.LocalState{
			Node: swarmruntime.LocalNodeState{
				SwarmID: "local-swarm-id",
				Name:    "local-swarm",
				Role:    "master",
			},
		},
		token: "peer-token",
	})
	handler := server.Handler()

	upsertFullBashAgentV2(t, handler, "fullbashbg-e2e", "background")
	resolved := getResolvedToolContractV2(t, handler, "fullbashbg-e2e")
	bashState, ok := resolved.Resolved.Tools["bash"]
	if !ok {
		t.Fatalf("resolved tool contract missing bash entry: %+v", resolved.Resolved.Tools)
	}
	if !bashState.Enabled {
		t.Fatalf("resolved bash tool is disabled: %+v", bashState)
	}
	if len(bashState.BashPrefixes) != 0 {
		t.Fatalf("resolved bash prefixes = %v, want unrestricted bash", bashState.BashPrefixes)
	}

	workspacePath := t.TempDir()
	session := createSessionWithPreferenceViaAPI(t, handler, workspacePath)

	runner.reset()
	backgroundResult := runTargetedAgentViaAPI(t, handler, session.Session.ID, map[string]any{
		"prompt":      "run pwd via unrestricted bash and report only the output",
		"target_kind": "background",
		"target_name": "fullbashbg-e2e",
	})
	backgroundRequests := runner.requestsSnapshot()
	if len(backgroundRequests) == 0 {
		t.Fatalf("expected provider request for targeted background run")
	}
	if !requestContainsTool(backgroundRequests[0], "bash") {
		t.Fatalf("targeted background run did not advertise bash; tools=%v assistant=%q", toolNamesFromRequest(backgroundRequests[0]), backgroundResult.Result.AssistantMessage.Content)
	}
	if backgroundResult.Result.ToolCallCount != 1 {
		t.Fatalf("targeted background run tool_call_count=%d, want 1; assistant=%q", backgroundResult.Result.ToolCallCount, backgroundResult.Result.AssistantMessage.Content)
	}
	if strings.Contains(strings.ToLower(backgroundResult.Result.AssistantMessage.Content), "unavailable") {
		t.Fatalf("targeted background run still complained about bash availability: %q", backgroundResult.Result.AssistantMessage.Content)
	}
	if !strings.Contains(backgroundResult.Result.AssistantMessage.Content, workspacePath) {
		t.Fatalf("targeted background run assistant=%q, want workspace path %q", backgroundResult.Result.AssistantMessage.Content, workspacePath)
	}
}

func TestDefaultMemoryAgentOwnsCommitToolContract(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "memory-commit-defaults.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	eventLog, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}
	hub := stream.NewHub(nil)
	modelSvc := model.NewService(pebblestore.NewModelStore(store), eventLog, nil)
	if _, _, err := modelSvc.SetGlobalPreference("codex", "gpt-5-codex", "high"); err != nil {
		t.Fatalf("set global preference: %v", err)
	}
	sessionSvc := sessionruntime.NewService(pebblestore.NewSessionStore(store), eventLog)
	agentSvc := agentruntime.NewService(pebblestore.NewAgentStore(store), eventLog)
	if err := agentSvc.EnsureDefaults(); err != nil {
		t.Fatalf("ensure default agents: %v", err)
	}
	providers := registry.New()
	runSvc := runruntime.NewService(sessionSvc, modelSvc, providers, tool.NewRuntime(2), nil, agentSvc, nil, eventLog)
	server := NewServer("test", nil, agentSvc, modelSvc, runSvc, sessionSvc, nil, nil, nil, providers, nil, nil, eventLog, hub)
	handler := server.Handler()

	if _, ok, err := pebblestore.NewAgentStore(store).GetProfile("commit"); err != nil {
		t.Fatalf("get commit profile: %v", err)
	} else if ok {
		t.Fatalf("built-in commit profile should not be created")
	}

	memory := getAgentV2(t, handler, "memory")
	if memory.Mode != "subagent" {
		t.Fatalf("memory mode = %q, want subagent", memory.Mode)
	}
	if memory.ToolContract == nil {
		t.Fatalf("memory missing tool contract")
	}
	resolved := getResolvedToolContractV2(t, handler, "memory")
	for _, name := range []string{"git_status", "git_diff", "git_add", "git_commit"} {
		if !requestResolvedToolEnabled(resolved, name) {
			t.Fatalf("memory resolved tool %s disabled: %+v", name, resolved.Resolved.Tools[name])
		}
	}
	for _, name := range []string{"websearch", "webfetch", "skill_use", "plan_manage", "ask_user", "bash", "write", "edit", "task", "exit_plan_mode"} {
		if requestResolvedToolEnabled(resolved, name) {
			t.Fatalf("memory resolved tool %s enabled, want disabled", name)
		}
	}
}

func TestAgentAndCustomToolsManageableViaV2API(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "agent-custom-tools-v2-api.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	eventLog, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}
	hub := stream.NewHub(nil)
	modelSvc := model.NewService(pebblestore.NewModelStore(store), eventLog, nil)
	if _, _, err := modelSvc.SetGlobalPreference("codex", "gpt-5-codex", "high"); err != nil {
		t.Fatalf("set global preference: %v", err)
	}
	sessionSvc := sessionruntime.NewService(pebblestore.NewSessionStore(store), eventLog)
	agentSvc := agentruntime.NewService(pebblestore.NewAgentStore(store), eventLog)
	if err := agentSvc.EnsureDefaults(); err != nil {
		t.Fatalf("ensure default agents: %v", err)
	}
	providers := registry.New()
	runSvc := runruntime.NewService(sessionSvc, modelSvc, providers, tool.NewRuntime(2), nil, agentSvc, nil, eventLog)
	server := NewServer("test", nil, agentSvc, modelSvc, runSvc, sessionSvc, nil, nil, nil, providers, nil, nil, eventLog, hub)
	handler := server.Handler()

	putCustomToolV2(t, handler, "git_status_short", map[string]any{
		"kind":        "fixed_bash",
		"description": "git status short",
		"command":     "git status --short",
	})
	putAgentV2(t, handler, "api-commit-only", map[string]any{
		"mode":                "background",
		"description":         "API-created commit helper",
		"prompt":              "Use the saved tool contract.",
		"execution_setting":   "read",
		"assign_custom_tools": []string{"git_status_short"},
	})

	profile := getAgentV2(t, handler, "api-commit-only")
	if profile.ToolContract == nil || profile.ToolContract.Tools == nil {
		t.Fatalf("agent profile missing tool contract after assignment: %+v", profile)
	}
	toolState, ok := profile.ToolContract.Tools["git_status_short"]
	if !ok || toolState.Enabled == nil || !*toolState.Enabled {
		t.Fatalf("assigned custom tool missing or disabled in tool contract: %+v", profile.ToolContract.Tools)
	}

	tools := listCustomToolsV2(t, handler)
	if len(tools) != 1 || tools[0].Name != "git_status_short" {
		t.Fatalf("custom tools = %+v, want git_status_short", tools)
	}

	resolved := getResolvedToolContractV2(t, handler, "api-commit-only")
	if _, ok := resolved.Resolved.Tools["git_status_short"]; !ok {
		t.Fatalf("resolved tool contract missing assigned custom tool: %+v", resolved.Resolved.Tools)
	}

	status := doJSONRequestLocal(t, handler, http.MethodDelete, "/v2/agents/api-commit-only/custom-tools/git_status_short", nil, &map[string]any{})
	if status != http.StatusOK {
		t.Fatalf("DELETE custom tool assignment status=%d", status)
	}
	profile = getAgentV2(t, handler, "api-commit-only")
	if profile.ToolContract != nil && profile.ToolContract.Tools != nil {
		if _, ok := profile.ToolContract.Tools["git_status_short"]; ok {
			t.Fatalf("custom tool still assigned after delete: %+v", profile.ToolContract.Tools)
		}
	}

	status = doJSONRequestLocal(t, handler, http.MethodDelete, "/v2/custom-tools/git_status_short", nil, &map[string]any{})
	if status != http.StatusOK {
		t.Fatalf("DELETE /v2/custom-tools status=%d", status)
	}
	tools = listCustomToolsV2(t, handler)
	if len(tools) != 0 {
		t.Fatalf("custom tools after delete = %+v, want empty", tools)
	}
}

type scopedToolProbeRunner struct {
	id             string
	allowedCommand string

	mu       sync.Mutex
	requests []provideriface.Request
}

func (r *scopedToolProbeRunner) ID() string {
	return r.id
}

func (r *scopedToolProbeRunner) CreateResponse(ctx context.Context, req provideriface.Request) (provideriface.Response, error) {
	return r.CreateResponseStreaming(ctx, req, nil)
}

func (r *scopedToolProbeRunner) CreateResponseStreaming(_ context.Context, req provideriface.Request, _ func(provideriface.StreamEvent)) (provideriface.Response, error) {
	r.mu.Lock()
	r.requests = append(r.requests, req)
	r.mu.Unlock()

	if hasFunctionCallOutput(req.Input) {
		return provideriface.Response{Text: "tool-output:" + lastFunctionCallOutput(req.Input)}, nil
	}
	if requestContainsTool(req, "bash") {
		return provideriface.Response{FunctionCalls: []provideriface.FunctionCall{{
			CallID:    "bash_1",
			Name:      "bash",
			Arguments: `{"command":"` + r.allowedCommand + `","timeout_ms":1000}`,
		}}}, nil
	}
	return provideriface.Response{Text: "bash-unavailable-from-tools"}, nil
}

func (r *scopedToolProbeRunner) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests = nil
}

func (r *scopedToolProbeRunner) requestsSnapshot() []provideriface.Request {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]provideriface.Request, len(r.requests))
	copy(out, r.requests)
	return out
}

type fakeAgentAPISwarmService struct {
	state swarmruntime.LocalState
	token string
}

func (f fakeAgentAPISwarmService) EnsureLocalState(input swarmruntime.EnsureLocalStateInput) (swarmruntime.LocalState, error) {
	return f.state, nil
}
func (f fakeAgentAPISwarmService) ListGroupsForSwarm(string, int) ([]swarmruntime.GroupState, string, error) {
	return nil, "", nil
}
func (f fakeAgentAPISwarmService) UpsertGroup(input swarmruntime.UpsertGroupInput) (swarmruntime.Group, error) {
	return swarmruntime.Group{}, nil
}
func (f fakeAgentAPISwarmService) DeleteGroup(string) error { return nil }
func (f fakeAgentAPISwarmService) SetCurrentGroup(string, string) (swarmruntime.GroupState, error) {
	return swarmruntime.GroupState{}, nil
}
func (f fakeAgentAPISwarmService) OutgoingPeerAuthToken(string) (string, bool, error) {
	if strings.TrimSpace(f.token) == "" {
		return "", false, nil
	}
	return f.token, true, nil
}
func (f fakeAgentAPISwarmService) ValidateIncomingPeerAuth(string, string) (bool, error) {
	return true, nil
}
func (f fakeAgentAPISwarmService) UpsertGroupMember(input swarmruntime.UpsertGroupMemberInput) (swarmruntime.GroupMember, error) {
	return swarmruntime.GroupMember{}, nil
}
func (f fakeAgentAPISwarmService) RemoveGroupMember(input swarmruntime.RemoveGroupMemberInput) error {
	return nil
}
func (f fakeAgentAPISwarmService) CreateInvite(input swarmruntime.CreateInviteInput) (swarmruntime.Invite, error) {
	return swarmruntime.Invite{}, nil
}
func (f fakeAgentAPISwarmService) SubmitEnrollment(input swarmruntime.SubmitEnrollmentInput) (swarmruntime.Enrollment, error) {
	return swarmruntime.Enrollment{}, nil
}
func (f fakeAgentAPISwarmService) ListPendingEnrollments(int) ([]swarmruntime.Enrollment, error) {
	return nil, nil
}
func (f fakeAgentAPISwarmService) DecideEnrollment(input swarmruntime.DecideEnrollmentInput) (swarmruntime.Enrollment, []swarmruntime.TrustedPeer, error) {
	return swarmruntime.Enrollment{}, nil, nil
}
func (f fakeAgentAPISwarmService) PrepareRemoteBootstrapParentPeer(input swarmruntime.PrepareRemoteBootstrapParentPeerInput) error {
	return nil
}
func (f fakeAgentAPISwarmService) ApproveManagedPairing(input swarmruntime.ApproveManagedPairingInput) (swarmruntime.PairingState, error) {
	return swarmruntime.PairingState{}, nil
}
func (f fakeAgentAPISwarmService) UpdateLocalPairingFromConfig(cfg startupconfig.FileConfig, transports []swarmruntime.TransportSummary) (swarmruntime.PairingState, error) {
	return swarmruntime.PairingState{}, nil
}
func (f fakeAgentAPISwarmService) DetachToStandalone(string) error { return nil }

type resolvedToolContractResponse struct {
	OK              bool                                 `json:"ok"`
	Agent           string                               `json:"agent"`
	RawToolContract *pebblestore.AgentToolContract       `json:"raw_tool_contract"`
	Resolved        runruntime.ResolvedAgentToolContract `json:"resolved"`
}

func getResolvedToolContractV2(t *testing.T, handler http.Handler, name string) resolvedToolContractResponse {
	t.Helper()
	resp := resolvedToolContractResponse{}
	status := doJSONRequestLocal(t, handler, http.MethodGet, "/v2/agents/"+name+"/tool-contract", nil, &resp)
	if status != http.StatusOK {
		t.Fatalf("GET /v2/agents/%s/tool-contract status=%d", name, status)
	}
	return resp
}

func upsertScopedAgentV2(t *testing.T, handler http.Handler, name, mode, allowedCommand string) {
	t.Helper()
	status := doJSONRequestLocal(t, handler, http.MethodPut, "/v2/agents/"+name, map[string]any{
		"mode":              mode,
		"description":       "Scoped bash contract agent",
		"prompt":            "Only use the allowed bash command.",
		"execution_setting": "read",
		"tool_contract": map[string]any{
			"tools": map[string]any{
				"bash": map[string]any{
					"enabled":       true,
					"bash_prefixes": []string{allowedCommand},
				},
			},
		},
	}, &map[string]any{})
	if status != http.StatusOK {
		t.Fatalf("PUT /v2/agents/%s status=%d", name, status)
	}
}

func upsertFullBashAgentV2(t *testing.T, handler http.Handler, name, mode string) {
	t.Helper()
	status := doJSONRequestLocal(t, handler, http.MethodPut, "/v2/agents/"+name, map[string]any{
		"mode":              mode,
		"description":       "Unrestricted bash contract agent",
		"prompt":            "Use bash when needed.",
		"execution_setting": "read",
		"tool_contract": map[string]any{
			"tools": map[string]any{
				"bash": map[string]any{
					"enabled": true,
				},
			},
		},
	}, &map[string]any{})
	if status != http.StatusOK {
		t.Fatalf("PUT /v2/agents/%s status=%d", name, status)
	}
}

type sessionCreateWithPreferenceResponse struct {
	OK      bool `json:"ok"`
	Session struct {
		ID string `json:"id"`
	} `json:"session"`
}

func createSessionWithPreferenceViaAPI(t *testing.T, handler http.Handler, workspacePath string) sessionCreateWithPreferenceResponse {
	t.Helper()
	resp := sessionCreateWithPreferenceResponse{}
	status := doJSONRequestLocal(t, handler, http.MethodPost, "/v1/sessions", map[string]any{
		"title":          "Scoped Agent Tool Contract E2E",
		"workspace_path": workspacePath,
		"workspace_name": filepath.Base(workspacePath),
		"preference": map[string]any{
			"provider": "codex",
			"model":    "gpt-5-codex",
			"thinking": "high",
		},
	}, &resp)
	if status != http.StatusOK {
		t.Fatalf("POST /v1/sessions status=%d", status)
	}
	if strings.TrimSpace(resp.Session.ID) == "" {
		t.Fatalf("session create response missing id: %+v", resp)
	}
	return resp
}

type targetedRunResponse struct {
	OK     bool `json:"ok"`
	Result struct {
		SessionID        string `json:"session_id"`
		TargetKind       string `json:"target_kind"`
		TargetName       string `json:"target_name"`
		ToolCallCount    int    `json:"tool_call_count"`
		AssistantMessage struct {
			Content string `json:"content"`
		} `json:"assistant_message"`
	} `json:"result"`
}

func runTargetedAgentViaAPI(t *testing.T, handler http.Handler, sessionID string, payload map[string]any) targetedRunResponse {
	t.Helper()
	resp := targetedRunResponse{}
	status := doJSONRequestLocal(t, handler, http.MethodPost, "/v1/sessions/"+sessionID+"/run", payload, &resp)
	if status != http.StatusOK {
		t.Fatalf("POST /v1/sessions/%s/run status=%d payload=%v", sessionID, status, payload)
	}
	return resp
}

func requestResolvedToolEnabled(resp resolvedToolContractResponse, name string) bool {
	toolState, ok := resp.Resolved.Tools[strings.ToLower(strings.TrimSpace(name))]
	return ok && toolState.Enabled
}

func requestContainsTool(req provideriface.Request, want string) bool {
	want = strings.ToLower(strings.TrimSpace(want))
	for _, definition := range req.Tools {
		if strings.EqualFold(strings.TrimSpace(definition.Name), want) {
			return true
		}
	}
	return false
}

func toolNamesFromRequest(req provideriface.Request) []string {
	if len(req.Tools) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(req.Tools))
	out := make([]string, 0, len(req.Tools))
	for _, definition := range req.Tools {
		name := strings.ToLower(strings.TrimSpace(definition.Name))
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func writeLocalStartupConfig(t *testing.T) string {
	t.Helper()
	startupPath := filepath.Join(t.TempDir(), "swarm.conf")
	cfg := startupconfig.Default(startupPath)
	cfg.SwarmMode = false
	cfg.SwarmName = "local-swarm"
	cfg.Host = "127.0.0.1"
	cfg.AdvertiseHost = "127.0.0.1"
	cfg.Port = 7781
	cfg.AdvertisePort = 7781
	if err := startupconfig.Write(cfg); err != nil {
		t.Fatalf("write startup config: %v", err)
	}
	return startupPath
}

func hasFunctionCallOutput(input []map[string]any) bool {
	for _, item := range input {
		if strings.EqualFold(strings.TrimSpace(stringMapValue(item, "type")), "function_call_output") {
			return true
		}
	}
	return false
}

func lastFunctionCallOutput(input []map[string]any) string {
	for i := len(input) - 1; i >= 0; i-- {
		if !strings.EqualFold(strings.TrimSpace(stringMapValue(input[i], "type")), "function_call_output") {
			continue
		}
		return strings.TrimSpace(stringMapValue(input[i], "output"))
	}
	return ""
}

func stringMapValue(values map[string]any, key string) string {
	if values == nil {
		return ""
	}
	value, _ := values[key].(string)
	return value
}

func doJSONRequestLocal(t *testing.T, handler http.Handler, method, path string, payload any, out any) int {
	t.Helper()

	var bodyReader io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal request body: %v", err)
		}
		bodyReader = bytes.NewReader(raw)
	}

	req, err := http.NewRequest(method, path, bodyReader)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)

	if out != nil {
		decoder := json.NewDecoder(recorder.Body)
		if err := decoder.Decode(out); err != nil {
			t.Fatalf("decode response body: %v", err)
		}
	}
	return recorder.Code
}

func putCustomToolV2(t *testing.T, handler http.Handler, name string, payload map[string]any) {
	t.Helper()
	status := doJSONRequestLocal(t, handler, http.MethodPut, "/v2/custom-tools/"+name, payload, &map[string]any{})
	if status != http.StatusOK {
		t.Fatalf("PUT /v2/custom-tools/%s status=%d", name, status)
	}
}

func putAgentV2(t *testing.T, handler http.Handler, name string, payload map[string]any) {
	t.Helper()
	status := doJSONRequestLocal(t, handler, http.MethodPut, "/v2/agents/"+name, payload, &map[string]any{})
	if status != http.StatusOK {
		t.Fatalf("PUT /v2/agents/%s status=%d", name, status)
	}
}

func getAgentV2(t *testing.T, handler http.Handler, name string) pebblestore.AgentProfile {
	t.Helper()
	var resp struct {
		OK      bool                     `json:"ok"`
		Profile pebblestore.AgentProfile `json:"profile"`
	}
	status := doJSONRequestLocal(t, handler, http.MethodGet, "/v2/agents/"+name, nil, &resp)
	if status != http.StatusOK {
		t.Fatalf("GET /v2/agents/%s status=%d", name, status)
	}
	return resp.Profile
}

func listCustomToolsV2(t *testing.T, handler http.Handler) []pebblestore.AgentCustomToolDefinition {
	t.Helper()
	var resp struct {
		OK          bool                                    `json:"ok"`
		CustomTools []pebblestore.AgentCustomToolDefinition `json:"custom_tools"`
	}
	status := doJSONRequestLocal(t, handler, http.MethodGet, "/v2/custom-tools", nil, &resp)
	if status != http.StatusOK {
		t.Fatalf("GET /v2/custom-tools status=%d", status)
	}
	return resp.CustomTools
}

func containsString(values []string, want string) bool {
	want = strings.TrimSpace(want)
	for _, value := range values {
		if strings.TrimSpace(value) == want {
			return true
		}
	}
	return false
}

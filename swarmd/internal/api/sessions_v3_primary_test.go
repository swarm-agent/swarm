package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	agentruntime "swarm/packages/swarmd/internal/agent"

	gorillaws "github.com/gorilla/websocket"

	"swarm/packages/swarmd/internal/discovery"
	modelruntime "swarm/packages/swarmd/internal/model"
	"swarm/packages/swarmd/internal/permission"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	"swarm/packages/swarmd/internal/provider/registry"
	runruntime "swarm/packages/swarmd/internal/run"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/stream"
	"swarm/packages/swarmd/internal/tool"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

func TestSessionsV3PrimaryHandlersDoNotUseRuntimeDispatchOrRoutes(t *testing.T) {
	body, err := os.ReadFile("sessions_v3_primary.go")
	if err != nil {
		t.Fatalf("read sessions_v3_primary.go: %v", err)
	}
	for _, required := range []string{"ApplySessionMutation", "SessionMutationCreateSession", "SessionMutationAppendMessage", "RunIntentDispatchBlocked", "ReplaySessionEvents", "HydrateSessionSnapshot"} {
		if !strings.Contains(string(body), required) {
			t.Fatalf("sessions_v3_primary.go missing required V3 primary storage symbol %q", required)
		}
	}
	for _, forbidden := range []string{"proxyRoutedSessionRequest", "dispatchRuntime", "RuntimeSession", "routedSessionTarget", "SessionRoute", "CreateSessionWithOptions", "AppendMessage("} {
		if strings.Contains(string(body), forbidden) {
			t.Fatalf("sessions_v3_primary.go contains forbidden runtime/route/legacy write symbol %q", forbidden)
		}
	}
}

func TestSessionsV3PrimaryWorktreeOnRequiresExplicitBranch(t *testing.T) {
	server, _, _, _, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	seedSessionsV2PrimaryAuthority(t, server, swarmStore, "host-swarm-id", "binding-v3-worktree", "/host/swarm-go")

	body := `{"client_request_id":"v3-wt-missing-branch","swarm_id":"host-swarm-id","workspace_binding_id":"binding-v3-worktree","title":"v3 wt","mode":"auto","agent_name":"swarm","worktree_mode":"on","preference":{"provider":"codex","model":"gpt-5.4","thinking":"medium"}}`
	req := httptest.NewRequest(http.MethodPost, "/v3/sessions", strings.NewReader(body))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "worktree_branch_name is required") {
		t.Fatalf("body missing branch requirement: %s", rec.Body.String())
	}
}

func TestSessionsV3PrimaryWorktreeOnCreatesRequestedBranchSession(t *testing.T) {
	server, _, _, _, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	seedSessionsV2PrimaryAuthority(t, server, swarmStore, "host-swarm-id", "binding-v3-worktree", "/host/swarm-go")
	fake := &fakeWorktreeService{allocation: worktreeruntime.Allocation{WorkspacePath: "/data/swarm/worktrees/swarm-go/ws_v3wtcreate", RepoRoot: "/host/swarm-go", BaseBranch: "main", BranchName: "agent/v3-requested", WorkspaceID: "ws_v3wtcreate"}}
	server.SetWorktreeService(fake)

	body := `{"session_id":"v3-wt-create","client_request_id":"v3-wt-create","swarm_id":"host-swarm-id","workspace_binding_id":"binding-v3-worktree","title":"v3 wt","mode":"auto","agent_name":"swarm","worktree_mode":"on","worktree_base_branch":"dev","worktree_branch_name":"agent/v3-requested","preference":{"provider":"codex","model":"gpt-5.4","thinking":"medium"}}`
	req := httptest.NewRequest(http.MethodPost, "/v3/sessions", strings.NewReader(body))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload struct {
		Session pebblestore.SessionSnapshot `json:"session"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if fake.lastWorkspace != "/host/swarm-go" || fake.lastNameSeed != "v3-wt-create" || fake.lastBaseBranch != "dev" || fake.lastBranchName != "agent/v3-requested" {
		t.Fatalf("allocation request workspace=%q seed=%q base=%q branch=%q", fake.lastWorkspace, fake.lastNameSeed, fake.lastBaseBranch, fake.lastBranchName)
	}
	if !payload.Session.WorktreeEnabled || payload.Session.WorkspacePath != "/data/swarm/worktrees/swarm-go/ws_v3wtcreate" || payload.Session.WorktreeRootPath != payload.Session.WorkspacePath || payload.Session.WorktreeBaseBranch != "dev" || payload.Session.WorktreeBranch != "agent/v3-requested" {
		t.Fatalf("session worktree facts = %+v", payload.Session)
	}
}

func TestSessionsV3PrimaryWorktreeCreateReplayDoesNotReallocate(t *testing.T) {
	server, _, _, _, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	seedSessionsV2PrimaryAuthority(t, server, swarmStore, "host-swarm-id", "binding-v3-worktree", "/host/swarm-go")
	fake := &fakeWorktreeService{allocation: worktreeruntime.Allocation{WorkspacePath: "/data/swarm/worktrees/swarm-go/ws_v3wtreplay", RepoRoot: "/host/swarm-go", BaseBranch: "main", BranchName: "agent/v3-replay", WorkspaceID: "ws_v3wtreplay"}}
	server.SetWorktreeService(fake)
	body := `{"session_id":"v3-wt-replay","client_request_id":"v3-wt-replay","swarm_id":"host-swarm-id","workspace_binding_id":"binding-v3-worktree","title":"v3 wt","mode":"auto","agent_name":"swarm","worktree_mode":"on","worktree_branch_name":"agent/v3-replay","preference":{"provider":"codex","model":"gpt-5.4","thinking":"medium"}}`

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/v3/sessions", strings.NewReader(body))
		rec := httptest.NewRecorder()
		server.Handler().ServeHTTP(rec, withTestPrincipal(req))
		if rec.Code != http.StatusOK {
			t.Fatalf("attempt %d status = %d, want %d, body=%s", i+1, rec.Code, http.StatusOK, rec.Body.String())
		}
	}
	if fake.lastNameSeed != "v3-wt-replay" || fake.lastBranchName != "agent/v3-replay" {
		t.Fatalf("unexpected allocation tracking seed=%q branch=%q", fake.lastNameSeed, fake.lastBranchName)
	}
}

func TestSessionsV3PrimaryModeAndPreferenceUseV3PrimaryMutation(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSessionWithPreference(t, server, "mode-pref-create", "mode preference", pebblestore.ModelPreference{Provider: "codex", Model: "gpt-5.4", Thinking: "medium"})
	now := time.Now().UnixMilli()
	turnUsage := pebblestore.SessionTurnUsageSnapshot{
		SessionID:     created.ID,
		RunID:         "run-session-usage",
		Provider:      "codex",
		Model:         "gpt-5.4",
		Source:        "codex_api_usage",
		ContextWindow: 1000,
		InputTokens:   200,
		OutputTokens:  50,
		TotalTokens:   250,
	}
	payloadHash, err := sessionV3UsagePayloadHash(created.ID, turnUsage.RunID, turnUsage)
	if err != nil {
		t.Fatalf("hash usage payload: %v", err)
	}
	if _, err := server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID:       created.ID,
		UserID:          testPrincipal().UserID,
		AccountScopeID:  testPrincipal().AccountScopeID,
		ClientRequestID: "session-usage-record",
		IdempotencyKey:  "session-usage-record",
		PayloadHash:     payloadHash,
		RequestHash:     payloadHash,
		Kind:            sessionruntime.SessionMutationRecordUsage,
		EventType:       "run.usage.updated",
		TurnUsage:       &turnUsage,
		NowUnixMs:       now,
	}); err != nil {
		t.Fatalf("record usage: %v", err)
	}

	usageReq := httptest.NewRequest(http.MethodGet, "/v3/sessions/"+created.ID+"/usage", nil)
	usageRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(usageRec, withTestPrincipal(usageReq))
	if usageRec.Code != http.StatusOK {
		t.Fatalf("usage get status = %d, want %d, body=%s", usageRec.Code, http.StatusOK, usageRec.Body.String())
	}
	var usagePayload struct {
		SessionID         string                          `json:"session_id"`
		HasUsageSummary   bool                            `json:"has_usage_summary"`
		UsageSummary      pebblestore.SessionUsageSummary `json:"usage_summary"`
		UnexpectedSession map[string]any                  `json:"session"`
	}
	if err := json.Unmarshal(usageRec.Body.Bytes(), &usagePayload); err != nil {
		t.Fatalf("decode usage get response: %v", err)
	}
	if usagePayload.SessionID != created.ID || !usagePayload.HasUsageSummary || usagePayload.UsageSummary.RemainingTokens != 750 || usagePayload.UnexpectedSession != nil {
		t.Fatalf("usage get payload = %+v", usagePayload)
	}

	prefGetReq := httptest.NewRequest(http.MethodGet, "/v3/sessions/"+created.ID+"/preference", nil)
	prefGetRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(prefGetRec, withTestPrincipal(prefGetReq))
	if prefGetRec.Code != http.StatusOK {
		t.Fatalf("preference get status = %d, want %d, body=%s", prefGetRec.Code, http.StatusOK, prefGetRec.Body.String())
	}
	var prefGetPayload struct {
		SessionID         string                      `json:"session_id"`
		Preference        pebblestore.ModelPreference `json:"preference"`
		ContextWindow     int                         `json:"context_window"`
		MaxOutputTokens   int                         `json:"max_output_tokens"`
		UnexpectedSession map[string]any              `json:"session"`
	}
	if err := json.Unmarshal(prefGetRec.Body.Bytes(), &prefGetPayload); err != nil {
		t.Fatalf("decode preference get response: %v", err)
	}
	if prefGetPayload.SessionID != created.ID || prefGetPayload.Preference.Model != "gpt-5.4" || prefGetPayload.UnexpectedSession != nil {
		t.Fatalf("preference get payload = %+v", prefGetPayload)
	}

	modeReq := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+created.ID+"/mode", bytes.NewBufferString(`{"mode":"plan"}`))
	modeReq.Header.Set("Content-Type", "application/json")
	modeRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(modeRec, withTestPrincipal(modeReq))
	if modeRec.Code != http.StatusOK {
		t.Fatalf("mode status = %d, want %d, body=%s", modeRec.Code, http.StatusOK, modeRec.Body.String())
	}
	var modePayload struct {
		Session  pebblestore.SessionSnapshot          `json:"session"`
		Events   []sessionruntime.SessionEvent        `json:"events"`
		Mutation sessionruntime.SessionMutationResult `json:"mutation"`
	}
	if err := json.Unmarshal(modeRec.Body.Bytes(), &modePayload); err != nil {
		t.Fatalf("decode mode response: %v", err)
	}
	if modePayload.Session.Mode != "plan" || modePayload.Mutation.Event.EventType != "session.mode.updated" {
		t.Fatalf("mode payload = %+v", modePayload)
	}
	var modeEventPayload map[string]any
	if err := json.Unmarshal(modePayload.Mutation.Event.Payload, &modeEventPayload); err != nil {
		t.Fatalf("decode mode event payload: %v", err)
	}
	if modeEventPayload["mode"] != "plan" {
		t.Fatalf("mode event payload = %+v", modeEventPayload)
	}

	prefReq := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+created.ID+"/preference", bytes.NewBufferString(`{"provider":"codex","model":"gpt-5.4","thinking":"high","service_tier":"fast"}`))
	prefReq.Header.Set("Content-Type", "application/json")
	prefRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(prefRec, withTestPrincipal(prefReq))
	if prefRec.Code != http.StatusOK {
		t.Fatalf("preference status = %d, want %d, body=%s", prefRec.Code, http.StatusOK, prefRec.Body.String())
	}
	var prefPayload struct {
		Session         pebblestore.SessionSnapshot          `json:"session"`
		Preference      pebblestore.ModelPreference          `json:"preference"`
		Events          []sessionruntime.SessionEvent        `json:"events"`
		ContextWindow   int                                  `json:"context_window"`
		MaxOutputTokens int                                  `json:"max_output_tokens"`
		Mutation        sessionruntime.SessionMutationResult `json:"mutation"`
	}
	if err := json.Unmarshal(prefRec.Body.Bytes(), &prefPayload); err != nil {
		t.Fatalf("decode preference response: %v", err)
	}
	if prefPayload.Session.Preference.Thinking != "high" || prefPayload.Preference.Thinking != "high" || prefPayload.Mutation.Event.EventType != "session.preference.updated" {
		t.Fatalf("preference payload = %+v", prefPayload)
	}
	var prefEventPayload struct {
		Preference pebblestore.ModelPreference `json:"preference"`
	}
	if err := json.Unmarshal(prefPayload.Mutation.Event.Payload, &prefEventPayload); err != nil {
		t.Fatalf("decode preference event payload: %v", err)
	}
	if prefEventPayload.Preference.Thinking != "high" || prefEventPayload.Preference.ServiceTier != "fast" {
		t.Fatalf("preference event payload = %+v", prefEventPayload)
	}
}

func TestSessionsV3PrimaryCreateListHydrateUsesPrimaryStoreOnly(t *testing.T) {
	server, sessionSvc, _, routeStore, _ := newRoutedSessionTestServerWithSwarmStore(t)

	bindingID := seedSessionsV3PrimaryAuthority(t, server, "/workspace/v3")
	body := `{"client_request_id":"create-v3-1","workspace_path":"/workspace/v3","workspace_name":"v3","swarm_id":"host-swarm-id","workspace_binding_id":"` + bindingID + `","target_kind":"host","target_relationship":"self","title":"V3 Primary","mode":"auto","agent_name":"swarm","preference":{"provider":"codex","model":"gpt-5.4","thinking":"medium"},"metadata":{"purpose":"cp3"}}`
	createReq := httptest.NewRequest(http.MethodPost, "/v3/sessions", bytes.NewBufferString(body))
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(createRec, withTestPrincipal(createReq))
	if createRec.Code != http.StatusOK {
		t.Fatalf("create status = %d, want %d, body=%s", createRec.Code, http.StatusOK, createRec.Body.String())
	}

	var createPayload struct {
		OK         bool                                 `json:"ok"`
		Session    pebblestore.SessionSnapshot          `json:"session"`
		Projection sessionruntime.SessionProjection     `json:"projection"`
		Events     []sessionruntime.SessionEvent        `json:"events"`
		Messages   []pebblestore.MessageSnapshot        `json:"messages"`
		Mutation   sessionruntime.SessionMutationResult `json:"mutation"`
	}
	if err := json.Unmarshal(createRec.Body.Bytes(), &createPayload); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if !createPayload.OK || strings.TrimSpace(createPayload.Session.ID) == "" {
		t.Fatalf("create payload = %+v", createPayload)
	}
	if createPayload.Session.AccountScopeID != testPrincipal().AccountScopeID || createPayload.Session.UserID != testPrincipal().UserID {
		t.Fatalf("session principal = %+v", createPayload.Session)
	}
	if createPayload.Session.Title != "V3 Primary" || createPayload.Session.WorkspacePath != "/workspace/v3" || createPayload.Session.Metadata["purpose"] != "cp3" {
		t.Fatalf("session = %+v", createPayload.Session)
	}
	if createPayload.Session.Metadata["agent_name"] != "swarm" || createPayload.Session.Metadata["resolved_agent_name"] != "swarm" || createPayload.Session.Metadata["agent_mode"] != "primary" || createPayload.Session.Metadata["runtime_mode"] != pebblestore.AgentRuntimeModePlanAuto {
		t.Fatalf("server-owned agent metadata = %+v", createPayload.Session.Metadata)
	}
	if createPayload.Projection.LastEventSeq != 1 || len(createPayload.Events) != 0 || len(createPayload.Messages) != 0 {
		t.Fatalf("projection/events/messages = %+v %+v %+v", createPayload.Projection, createPayload.Events, createPayload.Messages)
	}
	if createPayload.Mutation.FirstSeq != 1 || createPayload.Mutation.LastSeq != 1 || createPayload.Mutation.ResponseStatus != pebblestore.V3SessionMutationStatusCompleted || createPayload.Mutation.Event.EventType != "session.created" {
		t.Fatalf("mutation result = %+v", createPayload.Mutation)
	}
	if _, ok, err := sessionSvc.GetSessionProjection(createPayload.Session.ID); err != nil || !ok {
		t.Fatalf("stored projection ok=%t err=%v", ok, err)
	}
	if routes, err := routeStore.List(10); err != nil || len(routes) != 0 {
		t.Fatalf("routes = %+v err=%v, want none", routes, err)
	}
	if _, ok, err := server.topology.GetSessionRouteForAccount(testPrincipal().AccountScopeID, createPayload.Session.ID); err != nil {
		t.Fatalf("get topology route: %v", err)
	} else if ok {
		t.Fatalf("unexpected topology session route for V3 primary session")
	}

	hydrateReq := httptest.NewRequest(http.MethodGet, "/v3/sessions/"+createPayload.Session.ID, nil)
	hydrateRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(hydrateRec, withTestPrincipal(hydrateReq))
	if hydrateRec.Code != http.StatusOK {
		t.Fatalf("hydrate status = %d, want %d, body=%s", hydrateRec.Code, http.StatusOK, hydrateRec.Body.String())
	}
	var hydrated struct {
		OK       bool                          `json:"ok"`
		Session  pebblestore.SessionSnapshot   `json:"session"`
		Events   []sessionruntime.SessionEvent `json:"events"`
		Messages []pebblestore.MessageSnapshot `json:"messages"`
	}
	if err := json.Unmarshal(hydrateRec.Body.Bytes(), &hydrated); err != nil {
		t.Fatalf("decode hydrate response: %v", err)
	}
	if !hydrated.OK || hydrated.Session.ID != createPayload.Session.ID || len(hydrated.Events) != 0 || len(hydrated.Messages) != 0 {
		t.Fatalf("hydrated = %+v", hydrated)
	}

	listReq := httptest.NewRequest(http.MethodGet, "/v3/sessions?limit=10", nil)
	listRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(listRec, withTestPrincipal(listReq))
	if listRec.Code != http.StatusOK {
		t.Fatalf("list status = %d, want %d, body=%s", listRec.Code, http.StatusOK, listRec.Body.String())
	}
	var listed struct {
		OK       bool `json:"ok"`
		Sessions []struct {
			Session pebblestore.SessionSnapshot `json:"session"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(listRec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if !listed.OK || len(listed.Sessions) != 1 || listed.Sessions[0].Session.ID != createPayload.Session.ID {
		t.Fatalf("listed = %+v", listed)
	}
}

func TestSessionsV3PrimaryHydratesAfterStoreRestart(t *testing.T) {
	t.Setenv("SWARM_API_NO_AUTH", "1")
	storePath := filepath.Join(t.TempDir(), "sessions-v3-primary.pebble")
	server, _, closeStore := newSessionsV3PrimaryAPITestServer(t, storePath)

	bindingID := seedSessionsV3PrimaryAuthority(t, server, "/workspace/restart")
	createReq := httptest.NewRequest(http.MethodPost, "/v3/sessions", bytes.NewBufferString(`{"client_request_id":"restart-create","workspace_path":"/workspace/restart","swarm_id":"host-swarm-id","workspace_binding_id":"`+bindingID+`","target_kind":"host","target_relationship":"self","title":"Restarted V3","agent_name":"swarm","preference":{"provider":"codex","model":"gpt-5.4","thinking":"medium"}}`))
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(createRec, withTestPrincipal(createReq))
	if createRec.Code != http.StatusOK {
		t.Fatalf("create status = %d, want %d, body=%s", createRec.Code, http.StatusOK, createRec.Body.String())
	}
	var created struct {
		Session pebblestore.SessionSnapshot `json:"session"`
	}
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if err := closeStore(); err != nil {
		t.Fatalf("close first store: %v", err)
	}

	restarted, _, closeRestarted := newSessionsV3PrimaryAPITestServer(t, storePath)
	defer func() { _ = closeRestarted() }()
	hydrateReq := httptest.NewRequest(http.MethodGet, "/v3/sessions/"+created.Session.ID, nil)
	hydrateRec := httptest.NewRecorder()
	restarted.Handler().ServeHTTP(hydrateRec, withTestPrincipal(hydrateReq))
	if hydrateRec.Code != http.StatusOK {
		t.Fatalf("hydrate after restart status = %d, want %d, body=%s", hydrateRec.Code, http.StatusOK, hydrateRec.Body.String())
	}
	var hydrated struct {
		Session    pebblestore.SessionSnapshot      `json:"session"`
		Projection sessionruntime.SessionProjection `json:"projection"`
		Events     []sessionruntime.SessionEvent    `json:"events"`
	}
	if err := json.Unmarshal(hydrateRec.Body.Bytes(), &hydrated); err != nil {
		t.Fatalf("decode hydrate response: %v", err)
	}
	if hydrated.Session.ID != created.Session.ID || hydrated.Projection.ProjectionHighWatermarkSeq != 1 || len(hydrated.Events) != 0 {
		t.Fatalf("hydrated after restart = %+v", hydrated)
	}
}

func TestSessionsV3PrimaryCreateRejectsProtectedAuthorityMetadata(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{name: "authority", body: `{"client_request_id":"protected-metadata","workspace_path":"/workspace/v3","agent_name":"swarm","metadata":{"runtime_swarm_id":"container-swarm"}}`},
		{name: "agent spoof", body: `{"client_request_id":"protected-agent-metadata","workspace_path":"/workspace/v3","agent_name":"swarm","metadata":{"agent_name":"spoof"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, sessionSvc, _, routeStore, _ := newRoutedSessionTestServerWithSwarmStore(t)
			req := httptest.NewRequest(http.MethodPost, "/v3/sessions", bytes.NewBufferString(tc.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			server.Handler().ServeHTTP(rec, withTestPrincipal(req))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "reserved") {
				t.Fatalf("body = %s, want reserved metadata error", rec.Body.String())
			}
			if sessions, err := sessionSvc.ListSessionsForAccount(testPrincipal().AccountScopeID, 10); err != nil || len(sessions) != 0 {
				t.Fatalf("sessions = %+v err=%v, want none", sessions, err)
			}
			if routes, err := routeStore.List(10); err != nil || len(routes) != 0 {
				t.Fatalf("routes = %+v err=%v, want none", routes, err)
			}
		})
	}
}

func TestSessionsV3PrimaryMessagesCommitUserMessageAndPendingExecutorIntent(t *testing.T) {
	server, sessionSvc, _, routeStore, _ := newRoutedSessionTestServerWithSwarmStore(t)
	bindingID := seedSessionsV3PrimaryAuthority(t, server, "/workspace/cp4")
	create := httptest.NewRequest(http.MethodPost, "/v3/sessions", bytes.NewBufferString(`{"client_request_id":"cp4-create","workspace_path":"/workspace/cp4","swarm_id":"host-swarm-id","workspace_binding_id":"`+bindingID+`","target_kind":"host","target_relationship":"self","title":"CP4","agent_name":"swarm"}`))
	create.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(createRec, withTestPrincipal(create))
	if createRec.Code != http.StatusOK {
		t.Fatalf("create status = %d, want %d, body=%s", createRec.Code, http.StatusOK, createRec.Body.String())
	}
	var created struct {
		Session pebblestore.SessionSnapshot `json:"session"`
	}
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}

	messageReq := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+created.Session.ID+"/messages", bytes.NewBufferString(`{"client_request_id":"cp4-message","role":"user","content":"hello cp4","metadata":{"purpose":"cp4"}}`))
	messageReq.Header.Set("Content-Type", "application/json")
	messageRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(messageRec, withTestPrincipal(messageReq))
	if messageRec.Code != http.StatusOK {
		t.Fatalf("message status = %d, want %d, body=%s", messageRec.Code, http.StatusOK, messageRec.Body.String())
	}
	var payload struct {
		OK         bool                                 `json:"ok"`
		Session    pebblestore.SessionSnapshot          `json:"session"`
		Projection sessionruntime.SessionProjection     `json:"projection"`
		Message    *pebblestore.MessageSnapshot         `json:"message"`
		RunIntent  *sessionruntime.SessionRunIntent     `json:"run_intent"`
		Messages   []pebblestore.MessageSnapshot        `json:"messages"`
		Events     []sessionruntime.SessionEvent        `json:"events"`
		Mutation   sessionruntime.SessionMutationResult `json:"mutation"`
	}
	if err := json.Unmarshal(messageRec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode message response: %v", err)
	}
	if !payload.OK || payload.Message == nil || payload.Message.Content != "hello cp4" || payload.Message.Role != "user" {
		t.Fatalf("message payload = %+v", payload)
	}
	if payload.Session.MessageCount != 1 || payload.Projection.LastEventSeq != 2 || len(payload.Events) != 0 || len(payload.Messages) != 1 {
		t.Fatalf("session/projection/events/messages = %+v %+v %+v %+v", payload.Session, payload.Projection, payload.Events, payload.Messages)
	}
	if payload.RunIntent == nil || payload.RunIntent.Status != sessionruntime.RunIntentPendingExecutor || payload.RunIntent.BlockedReason != "" || payload.RunIntent.EventSeq != 2 {
		t.Fatalf("run intent = %+v", payload.RunIntent)
	}
	if payload.Mutation.FirstSeq != 2 || payload.Mutation.Message == nil || payload.Mutation.RunIntent == nil || payload.Mutation.Event.EventType != "session.message.appended" {
		t.Fatalf("mutation = %+v", payload.Mutation)
	}
	if routes, err := routeStore.List(10); err != nil || len(routes) != 0 {
		t.Fatalf("routes = %+v err=%v, want none", routes, err)
	}
	messages, err := sessionSvc.ListSessionMessages(created.Session.ID, 0, 10)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 1 || messages[0].Content != "hello cp4" || messages[0].Metadata["purpose"] != "cp4" {
		t.Fatalf("stored messages = %+v", messages)
	}
	getReq := httptest.NewRequest(http.MethodGet, "/v3/sessions/"+created.Session.ID+"/messages?after_seq=0&limit=10", nil)
	getRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(getRec, withTestPrincipal(getReq))
	if getRec.Code != http.StatusOK {
		t.Fatalf("get messages status = %d, want %d, body=%s", getRec.Code, http.StatusOK, getRec.Body.String())
	}
	var listed struct {
		OK       bool                          `json:"ok"`
		Messages []pebblestore.MessageSnapshot `json:"messages"`
	}
	if err := json.Unmarshal(getRec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode get messages response: %v", err)
	}
	if !listed.OK || len(listed.Messages) != 1 || listed.Messages[0].ID != messages[0].ID {
		t.Fatalf("listed messages = %+v", listed)
	}
}

func TestSessionsV3PrimaryMessageWithInvalidDispatchAuthorityStillCommitsAndBlocks(t *testing.T) {
	server, sessionSvc, _, routeStore, _ := newRoutedSessionTestServerWithSwarmStore(t)
	bindingID := seedSessionsV3PrimaryAuthority(t, server, "/workspace/cp4")
	create := httptest.NewRequest(http.MethodPost, "/v3/sessions", bytes.NewBufferString(`{"client_request_id":"cp4-invalid-authority-create","workspace_path":"/workspace/cp4","swarm_id":"host-swarm-id","workspace_binding_id":"`+bindingID+`","target_kind":"host","target_relationship":"self","title":"CP4","agent_name":"swarm"}`))
	create.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(createRec, withTestPrincipal(create))
	if createRec.Code != http.StatusOK {
		t.Fatalf("create status = %d, want %d, body=%s", createRec.Code, http.StatusOK, createRec.Body.String())
	}
	var created struct {
		Session pebblestore.SessionSnapshot `json:"session"`
	}
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}

	messageReq := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+created.Session.ID+"/messages", bytes.NewBufferString(`{"client_request_id":"cp4-invalid-authority-message","role":"user","content":"durable despite invalid authority","dispatch_authority":{"runtime_swarm_id":"container-swarm","authority_container_id":"container-1"}}`))
	messageReq.Header.Set("Content-Type", "application/json")
	messageRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(messageRec, withTestPrincipal(messageReq))
	if messageRec.Code != http.StatusOK {
		t.Fatalf("message status = %d, want %d, body=%s", messageRec.Code, http.StatusOK, messageRec.Body.String())
	}
	var payload struct {
		RunIntent *sessionruntime.SessionRunIntent `json:"run_intent"`
		Message   *pebblestore.MessageSnapshot     `json:"message"`
	}
	if err := json.Unmarshal(messageRec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode message response: %v", err)
	}
	if payload.Message == nil || payload.Message.Content != "durable despite invalid authority" {
		t.Fatalf("message payload = %+v", payload.Message)
	}
	if payload.RunIntent == nil || payload.RunIntent.Status != sessionruntime.RunIntentDispatchBlocked || !strings.Contains(payload.RunIntent.BlockedReason, "runtime placement not found") {
		t.Fatalf("run intent = %+v", payload.RunIntent)
	}
	messages, err := sessionSvc.ListSessionMessages(created.Session.ID, 0, 10)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 1 || messages[0].Content != "durable despite invalid authority" {
		t.Fatalf("messages = %+v", messages)
	}
	if routes, err := routeStore.List(10); err != nil || len(routes) != 0 {
		t.Fatalf("routes = %+v err=%v, want none", routes, err)
	}
}

func TestSessionsV3PrimaryDispatchAuthorityRecordsSpecificBlockedReasonsWithoutBlockingMessage(t *testing.T) {
	baseAuthority := `"runtime_swarm_id":"host-swarm-id","runtime_kind":"host","workspace_binding_id":"binding-primary-v2","authority_host_swarm_id":"host-swarm-id","placement_generation":1,"binding_generation":1,"source_workspace_path":"/workspace/cp8","runtime_workspace_path":"/workspace/cp8"`
	tests := []struct {
		name       string
		authority  string
		mutate     func(t *testing.T, server *Server)
		wantStatus string
		want       string
	}{
		{
			name:       "accepted authority records pending executor intent",
			authority:  baseAuthority,
			wantStatus: sessionruntime.RunIntentPendingExecutor,
		},
		{
			name:      "stale binding generation",
			authority: baseAuthority,
			mutate: func(t *testing.T, server *Server) {
				mutateSessionsV2WorkspaceBinding(t, server, "binding-primary-v2", func(binding *pebblestore.TopologyWorkspaceBindingRecord) {
					binding.BindingGeneration = 2
				})
			},
			want: "workspace binding generation mismatch",
		},
		{
			name:      "unavailable target",
			authority: strings.ReplaceAll(baseAuthority, `"runtime_swarm_id":"host-swarm-id"`, `"runtime_swarm_id":"missing-swarm"`),
			want:      "runtime placement not found",
		},
		{
			name:      "placement authority mismatch",
			authority: strings.ReplaceAll(baseAuthority, `"authority_host_swarm_id":"host-swarm-id"`, `"authority_host_swarm_id":"other-host"`),
			want:      "placement authority host mismatch",
		},
		{
			name:      "account mismatch",
			authority: baseAuthority + `,"account_scope_id":"other-account"`,
			want:      "account mismatch",
		},
		{
			name:      "runtime kind mismatch",
			authority: strings.ReplaceAll(baseAuthority, `"runtime_kind":"host"`, `"runtime_kind":"container"`),
			want:      "runtime kind mismatch",
		},
		{
			name:      "missing executor runtime",
			authority: `"workspace_binding_id":"binding-primary-v2"`,
			want:      "missing executor runtime",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wantStatus := tt.wantStatus
			if wantStatus == "" {
				wantStatus = sessionruntime.RunIntentDispatchBlocked
			}
			server, sessionSvc, _, routeStore, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
			seedSessionsV2PrimaryAuthority(t, server, swarmStore, "host-swarm-id", "binding-primary-v2", "/workspace/cp8")
			if tt.mutate != nil {
				tt.mutate(t, server)
			}
			created := createSessionsV3PrimaryTestSessionWithWorkspace(t, server, "cp8-create-"+strings.NewReplacer(" ", "-", "/", "-", "_", "-").Replace(tt.name), "CP8", "/workspace/cp8")
			body := fmt.Sprintf(`{"client_request_id":%q,"role":"user","content":"cp8 durable","dispatch_authority":{%s}}`, "cp8-message-"+strings.NewReplacer(" ", "-", "/", "-", "_", "-").Replace(tt.name), tt.authority)
			req := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+created.ID+"/messages", bytes.NewBufferString(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			server.Handler().ServeHTTP(rec, withTestPrincipal(req))
			if rec.Code != http.StatusOK {
				t.Fatalf("message status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
			}
			var payload struct {
				RunIntent *sessionruntime.SessionRunIntent `json:"run_intent"`
				Message   *pebblestore.MessageSnapshot     `json:"message"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
				t.Fatalf("decode message response: %v", err)
			}
			if payload.Message == nil || payload.Message.Content != "cp8 durable" {
				t.Fatalf("message payload = %+v", payload.Message)
			}
			if payload.RunIntent == nil || payload.RunIntent.Status != wantStatus || (tt.want != "" && !strings.Contains(payload.RunIntent.BlockedReason, tt.want)) {
				t.Fatalf("run intent = %+v, want status %q blocked reason containing %q", payload.RunIntent, wantStatus, tt.want)
			}
			messages, err := sessionSvc.ListSessionMessages(created.ID, 0, 10)
			if err != nil {
				t.Fatalf("list messages: %v", err)
			}
			if len(messages) != 1 || messages[0].Content != "cp8 durable" {
				t.Fatalf("messages = %+v", messages)
			}
			if routes, err := routeStore.List(10); err != nil || len(routes) != 0 {
				t.Fatalf("routes = %+v err=%v, want none", routes, err)
			}
		})
	}
}

func TestSessionsV3PrimaryAgentSwitchUpdatesStoredProfileAndRuntime(t *testing.T) {
	server, sessionSvc, closeStore := newSessionsV3PrimaryAPITestServer(t, filepath.Join(t.TempDir(), "agent-switch.pebble"))
	defer func() { _ = closeStore() }()
	if _, _, _, err := server.agents.UpsertForAccount(testPrincipal().AccountScopeID, agentruntime.UpsertInput{
		Name:                "explorer",
		Mode:                agentruntime.ModeSubagent,
		Provider:            "test-provider",
		Model:               "test-model",
		Thinking:            "high",
		RuntimeMode:         pebblestore.AgentRuntimeModeRead,
		ExitPlanModeEnabled: pebblestore.BoolPtr(false),
		ToolContract: &pebblestore.AgentToolContract{Preset: "custom", Tools: map[string]pebblestore.AgentToolConfig{
			"search": {Enabled: pebblestore.BoolPtr(true)},
		}},
		Enabled: pebblestore.BoolPtr(true),
		Prompt:  "Explorer switched prompt",
	}); err != nil {
		t.Fatalf("create explorer agent: %v", err)
	}
	created := createSessionsV3PrimaryTestSessionWithPreference(t, server, "agent-switch-create", "agent switch", pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"})

	req := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+created.ID+"/agent", bytes.NewBufferString(`{"agent_name":"explorer"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("agent switch status = %d body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Session pebblestore.SessionSnapshot `json:"session"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Session.Metadata["agent_name"] != "explorer" || payload.Session.Metadata["resolved_agent_name"] != "explorer" || payload.Session.Metadata["agent_mode"] != agentruntime.ModeSubagent || payload.Session.Metadata["runtime_mode"] != pebblestore.AgentRuntimeModeRead {
		t.Fatalf("switched metadata = %+v", payload.Session.Metadata)
	}
	var hydratePolicy struct {
		Preference       pebblestore.ModelPreference `json:"preference"`
		AgentModelPolicy sessionsV3AgentModelPolicy  `json:"agent_model_policy"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &hydratePolicy); err != nil {
		t.Fatalf("decode model policy: %v", err)
	}
	if !hydratePolicy.AgentModelPolicy.Locked || hydratePolicy.AgentModelPolicy.Source != "agent_preset" || hydratePolicy.AgentModelPolicy.Preference.Model != "test-model" || hydratePolicy.AgentModelPolicy.Preference.Thinking != "high" {
		t.Fatalf("agent model policy = %+v", hydratePolicy.AgentModelPolicy)
	}
	if hydratePolicy.Preference.Model != "test-model" || hydratePolicy.Preference.Thinking != "high" {
		t.Fatalf("hydrated effective preference = %+v", hydratePolicy.Preference)
	}
	worksetReq := httptest.NewRequest(http.MethodPost, "/v3/sessions:workset", bytes.NewBufferString(`{"session_ids":["`+created.ID+`"]}`))
	worksetReq.Header.Set("Content-Type", "application/json")
	worksetRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(worksetRec, withTestPrincipal(worksetReq))
	if worksetRec.Code != http.StatusOK {
		t.Fatalf("workset status = %d body=%s", worksetRec.Code, worksetRec.Body.String())
	}
	var worksetPayload struct {
		PreferencesBySession map[string]struct {
			Preference pebblestore.ModelPreference `json:"preference"`
		} `json:"preferences_by_session"`
		AgentModelPolicyBySession map[string]sessionsV3AgentModelPolicy `json:"agent_model_policy_by_session"`
	}
	if err := json.Unmarshal(worksetRec.Body.Bytes(), &worksetPayload); err != nil {
		t.Fatalf("decode workset response: %v", err)
	}
	worksetPreference, ok := worksetPayload.PreferencesBySession[created.ID]
	if !ok || worksetPreference.Preference.Model != "test-model" || worksetPreference.Preference.Thinking != "high" {
		t.Fatalf("workset preference = %+v ok=%t", worksetPayload.PreferencesBySession[created.ID], ok)
	}
	worksetPolicy, ok := worksetPayload.AgentModelPolicyBySession[created.ID]
	if !ok || !worksetPolicy.Locked || worksetPolicy.Source != "agent_preset" || worksetPolicy.Preference.Model != "test-model" || worksetPolicy.Preference.Thinking != "high" {
		t.Fatalf("workset agent model policy = %+v ok=%t", worksetPayload.AgentModelPolicyBySession[created.ID], ok)
	}
	prefReq := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+created.ID+"/preference", bytes.NewBufferString(`{"provider":"codex","model":"gpt-5.4","thinking":"medium"}`))
	prefReq.Header.Set("Content-Type", "application/json")
	prefRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(prefRec, withTestPrincipal(prefReq))
	if prefRec.Code != http.StatusBadRequest || !strings.Contains(prefRec.Body.String(), "Default") {
		t.Fatalf("locked preference status = %d body=%s", prefRec.Code, prefRec.Body.String())
	}
	if _, ok := payload.Session.Metadata["subagent"]; ok {
		t.Fatalf("agent switch left client-side subagent override in metadata: %+v", payload.Session.Metadata)
	}
	profile, err := sessionV3AgentProfileFromMetadata(payload.Session.Metadata)
	if err != nil {
		t.Fatalf("switched profile missing: %v", err)
	}
	if profile.Name != "explorer" || profile.Mode != agentruntime.ModeSubagent || profile.ToolContract == nil || profile.ToolContract.Tools["search"].Enabled == nil || !*profile.ToolContract.Tools["search"].Enabled {
		t.Fatalf("switched profile = %+v", profile)
	}
	stored, ok, err := sessionSvc.GetSession(created.ID)
	if err != nil || !ok {
		t.Fatalf("get switched session ok=%v err=%v", ok, err)
	}
	if stored.Metadata["agent_name"] != "explorer" {
		t.Fatalf("stored switched metadata = %+v", stored.Metadata)
	}
	events, err := sessionSvc.ListSessionEvents(created.ID, 0, 20)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	seenAgentEvent := false
	for _, event := range events {
		if event.EventType == "session.agent.updated" {
			seenAgentEvent = true
		}
	}
	if !seenAgentEvent {
		t.Fatalf("missing session.agent.updated event: %+v", events)
	}

	exec := newSessionV3Executor(server)
	resolved, err := exec.resolveSessionV3Runtime(sessionV3ExecutorJob{Principal: testPrincipal(), SessionID: created.ID, RunID: "agent-switch-runtime"})
	if err != nil {
		t.Fatalf("resolve switched runtime: %v", err)
	}
	if resolved.AgentProfile.Name != "explorer" || !strings.Contains(resolved.Instructions, "- name: explorer") || !strings.Contains(resolved.Instructions, "Explorer switched prompt") {
		t.Fatalf("resolved switched instructions/profile = %+v instructions=%s", resolved.AgentProfile, resolved.Instructions)
	}
	toolNames := sessionsV3ProviderRequestToolNames(resolved.Tools)
	if !toolNames["search"] || toolNames["read"] {
		t.Fatalf("resolved switched tools = %v", toolNames)
	}
}

func TestSessionsV3ExecutorUsesStoredAgentProfileSnapshotOnly(t *testing.T) {
	server, sessionSvc, closeStore := newSessionsV3PrimaryAPITestServer(t, filepath.Join(t.TempDir(), "stored-profile.pebble"))
	defer func() { _ = closeStore() }()
	created := createSessionsV3PrimaryTestSessionWithPreference(t, server, "stored-profile-create", "stored profile", pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"})
	storedProfile, err := sessionV3AgentProfileFromMetadata(created.Metadata)
	if err != nil {
		t.Fatalf("stored profile missing from created session metadata: %v", err)
	}
	if storedProfile.ToolContract == nil || storedProfile.ToolContract.Tools["read"].Enabled == nil || !*storedProfile.ToolContract.Tools["read"].Enabled {
		t.Fatalf("created session did not persist selected saved ToolContract snapshot: %+v", storedProfile.ToolContract)
	}
	if _, _, _, err := server.agents.UpsertForAccount(testPrincipal().AccountScopeID, agentruntime.UpsertInput{
		Name:    "swarm",
		Mode:    agentruntime.ModePrimary,
		Enabled: pebblestore.BoolPtr(true),
		Prompt:  "MUTATED PROMPT THAT MUST NOT BE USED",
		ToolContract: &pebblestore.AgentToolContract{Preset: "custom", Tools: map[string]pebblestore.AgentToolConfig{
			"search": {Enabled: pebblestore.BoolPtr(true)},
		}},
	}); err != nil {
		t.Fatalf("mutate saved agent after session create: %v", err)
	}
	runner := installSessionsV3TestProvider(server, "snapshot provider response")
	server.v3SessionExecutor = newSessionV3Executor(server)
	postSessionsV3PrimaryTestMessage(t, server, created.ID, "stored-profile-message", "use stored profile")
	waitForSessionsV3MessageCount(t, sessionSvc, created.ID, 2)
	if runner.callCount != 1 {
		t.Fatalf("provider call count = %d, want 1", runner.callCount)
	}
	if !strings.Contains(runner.lastRequest.Instructions, "Swarm test primary prompt") {
		t.Fatalf("provider instructions did not use stored profile prompt; instructions=%q", runner.lastRequest.Instructions)
	}
	if strings.Contains(runner.lastRequest.Instructions, "MUTATED PROMPT") {
		t.Fatalf("provider instructions used re-resolved mutated agent profile; instructions=%q", runner.lastRequest.Instructions)
	}
	toolNames := sessionsV3ProviderRequestToolNames(runner.lastRequest.Tools)
	if !toolNames["read"] {
		t.Fatalf("provider tools = %v, want stored read tool", toolNames)
	}
	if toolNames["search"] {
		t.Fatalf("provider tools = %v, used mutated saved profile instead of stored snapshot", toolNames)
	}
}

func TestSessionsV3PrimaryProviderVerticalSlicePersistsAssistantOnce(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	runner := &sessionsV3RecordingProviderRunner{text: "provider assistant response"}
	providers := registry.New()
	providers.RegisterRunner(runner)
	server.providers = providers
	server.v3SessionExecutor = newSessionV3Executor(server)
	created := createSessionsV3PrimaryTestSessionWithPreference(t, server, "provider-vertical-create", "provider model", pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"})
	body := `{"client_request_id":"provider-vertical-message","role":"user","content":"hello provider model"}`
	post := func() {
		req := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+created.ID+"/messages", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		server.Handler().ServeHTTP(rec, withTestPrincipal(req))
		if rec.Code != http.StatusOK {
			t.Fatalf("post message status = %d body=%s", rec.Code, rec.Body.String())
		}
	}
	post()
	waitForSessionsV3MessageCount(t, sessionSvc, created.ID, 2)
	post()
	if !server.WaitForInFlightRuns(2 * time.Second) {
		t.Fatalf("executor did not drain")
	}
	messages, err := sessionSvc.ListSessionMessages(created.ID, 0, 10)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 2 || messages[0].Role != "user" || messages[1].Role != "assistant" || messages[1].Content != "provider assistant response" {
		t.Fatalf("messages = %+v", messages)
	}
	listed := getSessionsV3PrimaryTestMessages(t, server, created.ID, 0, 10)
	if len(listed) != 2 || listed[0].Role != "user" || listed[0].Content != "hello provider model" || listed[1].Role != "assistant" || listed[1].Content != "provider assistant response" {
		t.Fatalf("GET /v3/sessions/{id}/messages listed = %+v", listed)
	}
	events, err := sessionSvc.ListSessionEvents(created.ID, 0, 10)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	var assistantStarted, assistantCompleted int
	for _, event := range events {
		switch event.EventType {
		case "session.assistant.started":
			assistantStarted++
		case "session.assistant.completed":
			assistantCompleted++
		}
	}
	if assistantStarted != 1 || assistantCompleted != 1 {
		t.Fatalf("assistant events started=%d completed=%d events=%+v", assistantStarted, assistantCompleted, events)
	}
}

func TestSessionsV3ExecutorRejectsDuplicateEnqueueRun(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	exec := newSessionV3Executor(server)
	exec.startDelay = 250 * time.Millisecond
	server.v3SessionExecutor = exec
	job := sessionV3ExecutorJob{Principal: testPrincipal(), SessionID: "session-duplicate-enqueue", RunID: "run-duplicate"}
	if !exec.EnqueueRun(job) {
		t.Fatalf("first enqueue returned false")
	}
	if exec.EnqueueRun(job) {
		t.Fatalf("duplicate enqueue returned true while original run was still queued/in-flight")
	}
	server.CancelInFlightRuns()
	if !server.WaitForInFlightRuns(2 * time.Second) {
		t.Fatalf("executor did not drain after cancellation")
	}
}

func TestSessionsV3ExecutorRunsDifferentSessionsConcurrently(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	exec := newSessionV3Executor(server)
	exec.startDelay = 500 * time.Millisecond
	server.v3SessionExecutor = exec

	if !exec.EnqueueRun(sessionV3ExecutorJob{Principal: testPrincipal(), SessionID: "session-concurrent-a", RunID: "run-concurrent-a"}) {
		t.Fatalf("first enqueue returned false")
	}
	if !exec.EnqueueRun(sessionV3ExecutorJob{Principal: testPrincipal(), SessionID: "session-concurrent-b", RunID: "run-concurrent-b"}) {
		t.Fatalf("second enqueue returned false")
	}

	deadline := time.After(2 * time.Second)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		if server.ActiveRunCount() >= 2 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("active runs = %d, want at least 2 concurrent runs", server.ActiveRunCount())
		case <-tick.C:
		}
	}

	server.CancelInFlightRuns()
	if !server.WaitForInFlightRuns(2 * time.Second) {
		t.Fatalf("executor did not drain after cancellation")
	}
}

func TestSessionsV3PrimaryDuplicatePostReplayDoesNotDuplicateAssistantOutput(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	installSessionsV3TestProvider(server, "deduped provider answer")
	exec := newSessionV3Executor(server)
	exec.startDelay = 150 * time.Millisecond
	server.v3SessionExecutor = exec
	created := createSessionsV3PrimaryTestSessionWithPreference(t, server, "duplicate-post-create", "duplicate post", pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"})
	body := `{"client_request_id":"duplicate-post-message","role":"user","content":"dedupe this turn"}`
	post := func() {
		req := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+created.ID+"/messages", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		server.Handler().ServeHTTP(rec, withTestPrincipal(req))
		if rec.Code != http.StatusOK {
			t.Fatalf("post status = %d body=%s", rec.Code, rec.Body.String())
		}
	}
	post()
	post()
	waitForSessionsV3MessageCount(t, sessionSvc, created.ID, 2)
	messages, err := sessionSvc.ListSessionMessages(created.ID, 0, 10)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 2 || messages[0].Role != "user" || messages[1].Role != "assistant" {
		t.Fatalf("messages after duplicate POST replay = %+v", messages)
	}
	events, err := sessionSvc.ListSessionEvents(created.ID, 0, 20)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	var started, completed int
	for _, event := range events {
		switch event.EventType {
		case "session.assistant.started":
			started++
		case "session.assistant.completed":
			completed++
		}
	}
	if started != 1 || completed != 1 {
		t.Fatalf("assistant events started=%d completed=%d events=%+v", started, completed, events)
	}
}

func TestSessionsV3PrimaryDispatchBlockedNeverInvokesExecutorModel(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	runner := &sessionsV3RecordingProviderRunner{text: "should not run"}
	providers := registry.New()
	providers.RegisterRunner(runner)
	server.providers = providers
	exec := newSessionV3Executor(server)
	exec.startDelay = 0
	server.v3SessionExecutor = exec
	created := createSessionsV3PrimaryTestSessionWithPreference(t, server, "dispatch-blocked-no-model-create", "blocked no model", pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model"})
	body := `{"client_request_id":"dispatch-blocked-no-model-message","role":"user","content":"do not execute","dispatch_authority":{"runtime_swarm_id":"missing-swarm","authority_container_id":"container-1"}}`
	req := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+created.ID+"/messages", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("post status = %d body=%s", rec.Code, rec.Body.String())
	}
	intent := waitForSessionsV3RunIntentStatus(t, sessionSvc, created.ID, sessionruntime.RunIntentDispatchBlocked)
	if !strings.Contains(intent.BlockedReason, "runtime placement not found") {
		t.Fatalf("run intent = %+v", intent)
	}
	if !server.WaitForInFlightRuns(2 * time.Second) {
		t.Fatalf("executor did not drain")
	}
	if runner.callCount != 0 {
		t.Fatalf("provider call count = %d, want 0 for dispatch_blocked", runner.callCount)
	}
	messages, err := sessionSvc.ListSessionMessages(created.ID, 0, 10)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 1 || messages[0].Role != "user" || messages[0].Content != "do not execute" {
		t.Fatalf("messages after dispatch_blocked = %+v", messages)
	}
}

func TestSessionsV3ExecutorFailsStaleRunningRunAfterRestartWithoutResume(t *testing.T) {
	t.Setenv("SWARM_API_NO_AUTH", "1")
	storePath := filepath.Join(t.TempDir(), "sessions-v3-stale-running.pebble")
	server, sessionSvc, closeStore := newSessionsV3PrimaryAPITestServer(t, storePath)
	created := createSessionsV3PrimaryTestSessionWithPreference(t, server, "stale-running-create", "stale running", pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model"})
	postSessionsV3PrimaryTestMessage(t, server, created.ID, "stale-running-message", "interrupted turn")
	intents, err := sessionSvc.Store().ListV3SessionRunIntents(created.ID, 0, 10)
	if err != nil {
		t.Fatalf("list run intents: %v", err)
	}
	if len(intents) != 1 || intents[0].Status != sessionruntime.RunIntentPendingExecutor {
		t.Fatalf("run intents before stale mark = %+v", intents)
	}
	runID := intents[0].RunID
	staleAt := time.Now().Add(-10 * time.Minute).UnixMilli()
	payloadHash, err := sessionV3ExecutorPayloadHash(created.ID, runID, sessionruntime.RunIntentRunning, "", "session.assistant.started", "")
	if err != nil {
		t.Fatalf("payload hash: %v", err)
	}
	if _, err := server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID:       created.ID,
		UserID:          testPrincipal().UserID,
		AccountScopeID:  testPrincipal().AccountScopeID,
		ClientRequestID: sessionV3ExecutorClientRequestID("session.assistant.started", runID),
		IdempotencyKey:  sessionV3ExecutorClientRequestID("session.assistant.started", runID),
		PayloadHash:     payloadHash,
		RequestHash:     payloadHash,
		Kind:            sessionruntime.SessionMutationRecordRunIntent,
		EventType:       "session.assistant.started",
		RunIntent:       &pebblestore.V3SessionRunIntent{RunID: runID, Status: sessionruntime.RunIntentRunning, UpdatedAt: staleAt},
		NowUnixMs:       staleAt,
	}); err != nil {
		t.Fatalf("mark run running: %v", err)
	}
	if err := closeStore(); err != nil {
		t.Fatalf("close first store: %v", err)
	}

	restarted, restartedSessions, closeRestarted := newSessionsV3PrimaryAPITestServer(t, storePath)
	defer func() { _ = closeRestarted() }()
	runner := &sessionsV3RecordingProviderRunner{text: "should not resume"}
	providers := registry.New()
	providers.RegisterRunner(runner)
	restarted.providers = providers
	exec := newSessionV3Executor(restarted)
	exec.startDelay = 0
	restarted.v3SessionExecutor = exec
	intent := waitForSessionsV3RunIntentStatus(t, restartedSessions, created.ID, sessionruntime.RunIntentInterrupted)
	if !strings.Contains(intent.BlockedReason, "executor interrupted during daemon restart") {
		t.Fatalf("interrupted intent = %+v", intent)
	}
	if runner.callCount != 0 {
		t.Fatalf("provider call count = %d, want 0 for stale interrupted run", runner.callCount)
	}
	messages, err := restartedSessions.ListSessionMessages(created.ID, 0, 10)
	if err != nil {
		t.Fatalf("list messages after restart: %v", err)
	}
	if len(messages) != 1 || messages[0].Role != "user" || messages[0].Content != "interrupted turn" {
		t.Fatalf("messages after stale running recovery = %+v", messages)
	}
}

func TestSessionsV3PrimaryRunStopCancelsActiveExecutorAndSuppressesLateOutput(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	entered := make(chan struct{})
	releaseLate := make(chan struct{})
	runner := &sessionsV3RecordingProviderRunner{
		handler: func(ctx context.Context, req provideriface.Request, onEvent func(provideriface.StreamEvent)) (provideriface.Response, error) {
			close(entered)
			<-ctx.Done()
			<-releaseLate
			if onEvent != nil {
				onEvent(provideriface.StreamEvent{Type: provideriface.StreamEventOutputTextDelta, Delta: "late output"})
			}
			return provideriface.Response{Text: "late assistant"}, nil
		},
	}
	providers := registry.New()
	providers.RegisterRunner(runner)
	server.providers = providers
	exec := newSessionV3Executor(server)
	exec.startDelay = 0
	server.v3SessionExecutor = exec
	created := createSessionsV3PrimaryTestSessionWithPreference(t, server, "active-cancel-create", "active cancel", pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"})
	postSessionsV3PrimaryTestMessage(t, server, created.ID, "active-cancel-message", "cancel active run")
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatalf("provider was not entered")
	}
	intent := waitForSessionsV3RunIntentStatus(t, sessionSvc, created.ID, sessionruntime.RunIntentRunning)
	req := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+created.ID+"/run/stop", bytes.NewBufferString(fmt.Sprintf(`{"type":"run.stop","run_id":%q,"target_swarm_id":"host-swarm-id","reason":"stop from test"}`, intent.RunID)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("stop status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var stopResp struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &stopResp); err != nil || stopResp.Status != sessionruntime.RunIntentCancelled {
		t.Fatalf("stop response status = %+v err=%v body=%s", stopResp, err, rec.Body.String())
	}
	cancelled := waitForSessionsV3RunIntentStatus(t, sessionSvc, created.ID, sessionruntime.RunIntentCancelled)
	if cancelled.RunID != intent.RunID || cancelled.BlockedReason != "stop from test" {
		t.Fatalf("cancelled intent = %+v", cancelled)
	}
	if active, ok, err := sessionSvc.GetSessionActiveRunIntent(created.ID); err != nil || ok {
		t.Fatalf("active run after cancellation = %+v ok=%v err=%v, want inactive", active, ok, err)
	}
	if state, ok, err := sessionSvc.GetSessionRunState(created.ID); err != nil || !ok || state.Active || state.Status != sessionruntime.RunIntentCancelled || state.RunID != intent.RunID {
		t.Fatalf("canonical run state after cancellation = %+v ok=%v err=%v", state, ok, err)
	}
	assertSessionsV3CancelledRunEvent(t, sessionSvc, created.ID, intent.RunID, "stop from test")
	close(releaseLate)
	if !server.WaitForInFlightRuns(2 * time.Second) {
		t.Fatalf("executor did not drain after active cancel")
	}
	messages, err := sessionSvc.ListSessionMessages(created.ID, 0, 10)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 1 || messages[0].Role != "user" {
		t.Fatalf("messages after cancel = %+v", messages)
	}
	events, err := sessionSvc.ListSessionEvents(created.ID, 0, 20)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	for _, event := range events {
		if event.EventType == "session.assistant.delta" || event.EventType == "session.assistant.completed" {
			t.Fatalf("late assistant event persisted after cancel: %+v", event)
		}
	}
}

func TestSessionsV3PrimaryRunStopCancelsBeforeExecutorStart(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	runner := &sessionsV3RecordingProviderRunner{text: "should not run"}
	providers := registry.New()
	providers.RegisterRunner(runner)
	server.providers = providers
	exec := newSessionV3Executor(server)
	exec.startDelay = 500 * time.Millisecond
	server.v3SessionExecutor = exec
	created := createSessionsV3PrimaryTestSessionWithPreference(t, server, "queued-cancel-create", "queued cancel", pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"})
	postSessionsV3PrimaryTestMessage(t, server, created.ID, "queued-cancel-message", "cancel before start")
	intent := waitForSessionsV3RunIntentStatus(t, sessionSvc, created.ID, sessionruntime.RunIntentPendingExecutor)
	req := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+created.ID+"/run/stop", bytes.NewBufferString(fmt.Sprintf(`{"type":"run.stop","run_id":%q,"target_swarm_id":"host-swarm-id"}`, intent.RunID)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("queued stop status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var stopResp struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &stopResp); err != nil || stopResp.Status != sessionruntime.RunIntentCancelled {
		t.Fatalf("queued stop response status = %+v err=%v body=%s", stopResp, err, rec.Body.String())
	}
	cancelled := waitForSessionsV3RunIntentStatus(t, sessionSvc, created.ID, sessionruntime.RunIntentCancelled)
	if cancelled.RunID != intent.RunID || cancelled.BlockedReason != sessionV3RunStopDefaultReason {
		t.Fatalf("cancelled intent = %+v", cancelled)
	}
	assertSessionsV3CancelledRunEvent(t, sessionSvc, created.ID, intent.RunID, sessionV3RunStopDefaultReason)
	if !server.WaitForInFlightRuns(2 * time.Second) {
		t.Fatalf("executor did not drain after queued cancel")
	}
	if runner.callCount != 0 {
		t.Fatalf("provider call count = %d, want 0 for queued cancel", runner.callCount)
	}
	messages, err := sessionSvc.ListSessionMessages(created.ID, 0, 10)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 1 || messages[0].Role != "user" {
		t.Fatalf("messages after queued cancel = %+v", messages)
	}
}

func TestSessionsV3PrimaryRunStopRequiresPrimaryTarget(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	runner := &sessionsV3RecordingProviderRunner{text: "should not run"}
	providers := registry.New()
	providers.RegisterRunner(runner)
	server.providers = providers
	exec := newSessionV3Executor(server)
	exec.startDelay = 500 * time.Millisecond
	server.v3SessionExecutor = exec
	created := createSessionsV3PrimaryTestSessionWithPreference(t, server, "target-stop-create", "target stop", pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"})
	postSessionsV3PrimaryTestMessage(t, server, created.ID, "target-stop-message", "validate stop target")
	intent := waitForSessionsV3RunIntentStatus(t, sessionSvc, created.ID, sessionruntime.RunIntentPendingExecutor)

	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{name: "missing target", body: fmt.Sprintf(`{"type":"run.stop","run_id":%q}`, intent.RunID), want: "target_swarm_id is required"},
		{name: "wrong target", body: fmt.Sprintf(`{"type":"run.stop","run_id":%q,"target_swarm_id":"child-swarm"}`, intent.RunID), want: "child-swarm"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+created.ID+"/run/stop", bytes.NewBufferString(tc.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			server.Handler().ServeHTTP(rec, withTestPrincipal(req))
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), tc.want) {
				t.Fatalf("stop status/body = %d %s, want 400 containing %q", rec.Code, rec.Body.String(), tc.want)
			}
		})
	}
	current, ok, err := sessionSvc.GetSessionRunIntent(created.ID, intent.RunID)
	if err != nil || !ok {
		t.Fatalf("get run intent after rejected stop ok=%v err=%v", ok, err)
	}
	if current.Status != sessionruntime.RunIntentPendingExecutor {
		t.Fatalf("run status after rejected stop = %q, want pending_executor", current.Status)
	}
}

func TestSessionsV3PrimaryPostReturnsBeforeModelCompletion(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	installSessionsV3TestProvider(server, "nonblocking provider answer")
	exec := newSessionV3Executor(server)
	exec.startDelay = 0
	exec.modelDelay = 500 * time.Millisecond
	server.v3SessionExecutor = exec
	created := createSessionsV3PrimaryTestSessionWithPreference(t, server, "nonblocking-create", "nonblocking", pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"})
	body := `{"client_request_id":"nonblocking-message","role":"user","content":"do not block on model"}`
	req := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+created.ID+"/messages", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	started := time.Now()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	elapsed := time.Since(started)
	if rec.Code != http.StatusOK {
		t.Fatalf("post message status = %d body=%s", rec.Code, rec.Body.String())
	}
	if elapsed >= 250*time.Millisecond {
		t.Fatalf("POST blocked for %s; want return before provider delay", elapsed)
	}
	messages, err := sessionSvc.ListSessionMessages(created.ID, 0, 10)
	if err != nil {
		t.Fatalf("list messages after POST: %v", err)
	}
	if len(messages) != 1 || messages[0].Role != "user" || messages[0].Content != "do not block on model" {
		t.Fatalf("messages immediately after POST = %+v, want committed user only", messages)
	}
	waitForSessionsV3MessageCount(t, sessionSvc, created.ID, 2)
	listed := getSessionsV3PrimaryTestMessages(t, server, created.ID, 0, 10)
	if len(listed) != 2 || listed[0].Role != "user" || listed[1].Role != "assistant" {
		t.Fatalf("GET /messages after completion = %+v", listed)
	}
}

func TestSessionsV3ExecutorEnabledByNormalServerStartup(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "normal-startup.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()
	eventLog, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}
	sessionSvc := sessionruntime.NewService(pebblestore.NewSessionStore(store), eventLog)
	server := NewServer(nil, nil, nil, nil, sessionSvc, nil, nil, nil, nil, nil, nil, eventLog, stream.NewHub(eventLog))
	defer func() {
		server.CancelInFlightRuns()
		server.WaitForInFlightRuns(2 * time.Second)
	}()
	if server.v3SessionExecutor == nil {
		t.Fatalf("normal NewServer startup with session service did not enable V3 session executor")
	}
}

func waitForSessionsV3MessageCount(t *testing.T, sessionSvc *sessionruntime.Service, sessionID string, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		messages, err := sessionSvc.ListSessionMessages(sessionID, 0, 10)
		if err != nil {
			t.Fatalf("list messages: %v", err)
		}
		if len(messages) >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	messages, _ := sessionSvc.ListSessionMessages(sessionID, 0, 10)
	t.Fatalf("message count = %d, want %d: %+v", len(messages), want, messages)
}

func waitForSessionsV3RunIntentStatus(t *testing.T, sessionSvc *sessionruntime.Service, sessionID, want string) sessionruntime.SessionRunIntent {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		intents, err := sessionSvc.Store().ListV3SessionRunIntents(sessionID, 0, 10)
		if err != nil {
			t.Fatalf("list run intents: %v", err)
		}
		for _, intent := range intents {
			if intent.Status == want {
				return intent
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	intents, _ := sessionSvc.Store().ListV3SessionRunIntents(sessionID, 0, 10)
	t.Fatalf("run intent status %q not found: %+v", want, intents)
	return sessionruntime.SessionRunIntent{}
}

func waitForSessionsV3SpecificRunIntentStatus(t *testing.T, sessionSvc *sessionruntime.Service, sessionID, runID, want string) sessionruntime.SessionRunIntent {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		intents, err := sessionSvc.Store().ListV3SessionRunIntents(sessionID, 0, 20)
		if err != nil {
			t.Fatalf("list run intents: %v", err)
		}
		for _, intent := range intents {
			if intent.RunID == runID && intent.Status == want {
				return intent
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	intents, _ := sessionSvc.Store().ListV3SessionRunIntents(sessionID, 0, 20)
	t.Fatalf("run intent %q status %q not found: %+v", runID, want, intents)
	return sessionruntime.SessionRunIntent{}
}

func currentSessionsV3RunIntentStatus(t *testing.T, sessionSvc *sessionruntime.Service, sessionID string) string {
	t.Helper()
	intents, err := sessionSvc.Store().ListV3SessionRunIntents(sessionID, 0, 10)
	if err != nil {
		t.Fatalf("list run intents: %v", err)
	}
	if len(intents) == 0 {
		t.Fatalf("no run intents for session %q", sessionID)
	}
	return intents[0].Status
}

func assertSessionsV3CancelledRunEvent(t *testing.T, sessionSvc *sessionruntime.Service, sessionID, runID, reason string) {
	t.Helper()
	events, err := sessionSvc.ListSessionEvents(sessionID, 0, 50)
	if err != nil {
		t.Fatalf("list cancellation events: %v", err)
	}
	for _, event := range events {
		if event.EventType != "session.run.cancelled" {
			continue
		}
		var payload struct {
			RunID     string `json:"run_id"`
			Status    string `json:"status"`
			Error     string `json:"error"`
			RunIntent struct {
				RunID         string `json:"run_id"`
				Status        string `json:"status"`
				BlockedReason string `json:"blocked_reason"`
				EventSeq      uint64 `json:"event_seq"`
			} `json:"run_intent"`
		}
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatalf("decode cancellation payload: %v payload=%s", err, string(event.Payload))
		}
		if payload.RunID == runID && payload.Status == sessionruntime.RunIntentCancelled && payload.RunIntent.RunID == runID && payload.RunIntent.Status == sessionruntime.RunIntentCancelled && payload.RunIntent.BlockedReason == reason && payload.RunIntent.EventSeq == event.Seq {
			return
		}
		t.Fatalf("unexpected cancellation payload: event=%+v payload=%+v want_run=%q want_reason=%q", event, payload, runID, reason)
	}
	t.Fatalf("session.run.cancelled event for run %q not found in %+v", runID, events)
}

func waitForSessionsV3Title(t *testing.T, sessionSvc *sessionruntime.Service, sessionID, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		session, ok, err := sessionSvc.GetSession(sessionID)
		if err != nil {
			t.Fatalf("get session: %v", err)
		}
		if ok && session.Title == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	session, _, _ := sessionSvc.GetSession(sessionID)
	t.Fatalf("session title = %q, want %q", session.Title, want)
}

func sessionsV3EventsContainType(events []sessionruntime.SessionEvent, eventType string) bool {
	for _, event := range events {
		if event.EventType == eventType {
			return true
		}
	}
	return false
}

func TestSessionsV3PrimaryMessageIsIdempotent(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	bindingID := seedSessionsV3PrimaryAuthority(t, server, "/workspace/cp4")
	create := httptest.NewRequest(http.MethodPost, "/v3/sessions", bytes.NewBufferString(`{"client_request_id":"cp4-idempotent-create","workspace_path":"/workspace/cp4","swarm_id":"host-swarm-id","workspace_binding_id":"`+bindingID+`","target_kind":"host","target_relationship":"self","title":"CP4","agent_name":"swarm"}`))
	create.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(createRec, withTestPrincipal(create))
	if createRec.Code != http.StatusOK {
		t.Fatalf("create status = %d, want %d, body=%s", createRec.Code, http.StatusOK, createRec.Body.String())
	}
	var created struct {
		Session pebblestore.SessionSnapshot `json:"session"`
	}
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	body := `{"client_request_id":"cp4-same-message","role":"user","content":"idempotent cp4"}`
	post := func() (int, string) {
		req := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+created.Session.ID+"/messages", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		server.Handler().ServeHTTP(rec, withTestPrincipal(req))
		return rec.Code, rec.Body.String()
	}
	status, firstBody := post()
	if status != http.StatusOK {
		t.Fatalf("first status = %d body=%s", status, firstBody)
	}
	status, secondBody := post()
	if status != http.StatusOK {
		t.Fatalf("second status = %d body=%s", status, secondBody)
	}
	var second struct {
		Mutation sessionruntime.SessionMutationResult `json:"mutation"`
		Events   []sessionruntime.SessionEvent        `json:"events"`
	}
	if err := json.Unmarshal([]byte(secondBody), &second); err != nil {
		t.Fatalf("decode second response: %v", err)
	}
	if !second.Mutation.Replayed || second.Mutation.PrimarySeq != 2 {
		t.Fatalf("second mutation = %+v", second.Mutation)
	}
	events, err := sessionSvc.ListSessionEvents(created.Session.ID, 0, 10)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(events) < 2 || len(second.Events) != 0 || events[1].EventType != "session.message.appended" || second.Mutation.Event.EventType != "session.message.appended" {
		t.Fatalf("events persisted=%+v second=%+v mutation=%+v", events, second.Events, second.Mutation)
	}
}

func TestSessionsV3PrimaryConcurrentDistinctMessagesAllocateContiguousSeq(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	bindingID := seedSessionsV3PrimaryAuthority(t, server, "/workspace/cp4")
	create := httptest.NewRequest(http.MethodPost, "/v3/sessions", bytes.NewBufferString(`{"client_request_id":"cp4-concurrent-create","workspace_path":"/workspace/cp4","swarm_id":"host-swarm-id","workspace_binding_id":"`+bindingID+`","target_kind":"host","target_relationship":"self","title":"CP4","agent_name":"swarm"}`))
	create.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(createRec, withTestPrincipal(create))
	if createRec.Code != http.StatusOK {
		t.Fatalf("create status = %d, want %d, body=%s", createRec.Code, http.StatusOK, createRec.Body.String())
	}
	var created struct {
		Session pebblestore.SessionSnapshot `json:"session"`
	}
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}

	const workers = 12
	var wg sync.WaitGroup
	errs := make(chan string, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			body := fmt.Sprintf(`{"client_request_id":"cp4-concurrent-%02d","role":"user","content":"message %02d"}`, i, i)
			req := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+created.Session.ID+"/messages", bytes.NewBufferString(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			server.Handler().ServeHTTP(rec, withTestPrincipal(req))
			if rec.Code != http.StatusOK {
				errs <- fmt.Sprintf("message %d status = %d body=%s", i, rec.Code, rec.Body.String())
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}

	events, err := sessionSvc.ListSessionEvents(created.Session.ID, 0, workers+2)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(events) != workers+1 {
		t.Fatalf("events = %d, want %d: %+v", len(events), workers+1, events)
	}
	for i, event := range events {
		wantSeq := uint64(i + 1)
		if event.Seq != wantSeq {
			t.Fatalf("event[%d].seq=%d want %d events=%+v", i, event.Seq, wantSeq, events)
		}
	}
	messages, err := sessionSvc.ListSessionMessages(created.Session.ID, 0, workers+1)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != workers {
		t.Fatalf("messages = %d, want %d: %+v", len(messages), workers, messages)
	}
	for i, message := range messages {
		wantSeq := uint64(i + 2)
		if message.GlobalSeq != wantSeq {
			t.Fatalf("message[%d].global_seq=%d want %d messages=%+v", i, message.GlobalSeq, wantSeq, messages)
		}
	}
}

func TestSessionsV3PrimaryEventsReplayCursorAndRestart(t *testing.T) {
	t.Setenv("SWARM_API_NO_AUTH", "1")
	storePath := filepath.Join(t.TempDir(), "sessions-v3-events.pebble")
	server, _, closeStore := newSessionsV3PrimaryAPITestServer(t, storePath)

	bindingID := seedSessionsV3PrimaryAuthority(t, server, "/workspace/cp5")
	createReq := httptest.NewRequest(http.MethodPost, "/v3/sessions", bytes.NewBufferString(`{"client_request_id":"cp5-create","workspace_path":"/workspace/cp5","swarm_id":"host-swarm-id","workspace_binding_id":"`+bindingID+`","target_kind":"host","target_relationship":"self","title":"CP5","agent_name":"swarm"}`))
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(createRec, withTestPrincipal(createReq))
	if createRec.Code != http.StatusOK {
		t.Fatalf("create status = %d, want %d, body=%s", createRec.Code, http.StatusOK, createRec.Body.String())
	}
	var created struct {
		Session pebblestore.SessionSnapshot `json:"session"`
	}
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	for i := 0; i < 2; i++ {
		body := fmt.Sprintf(`{"client_request_id":"cp5-message-%d","role":"user","content":"cp5 message %d"}`, i, i)
		messageReq := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+created.Session.ID+"/messages", bytes.NewBufferString(body))
		messageReq.Header.Set("Content-Type", "application/json")
		messageRec := httptest.NewRecorder()
		server.Handler().ServeHTTP(messageRec, withTestPrincipal(messageReq))
		if messageRec.Code != http.StatusOK {
			t.Fatalf("message %d status = %d, want %d, body=%s", i, messageRec.Code, http.StatusOK, messageRec.Body.String())
		}
	}
	if err := closeStore(); err != nil {
		t.Fatalf("close first store: %v", err)
	}

	restarted, _, closeRestarted := newSessionsV3PrimaryAPITestServer(t, storePath)
	defer func() { _ = closeRestarted() }()
	replayReq := httptest.NewRequest(http.MethodGet, "/v3/sessions/"+created.Session.ID+"/events?after_seq=1&limit=1", nil)
	replayRec := httptest.NewRecorder()
	restarted.Handler().ServeHTTP(replayRec, withTestPrincipal(replayReq))
	if replayRec.Code != http.StatusOK {
		t.Fatalf("events status = %d, want %d, body=%s", replayRec.Code, http.StatusOK, replayRec.Body.String())
	}
	var replay struct {
		OK               bool                              `json:"ok"`
		Events           []sessionruntime.SessionEvent     `json:"events"`
		Projection       sessionruntime.SessionProjection  `json:"projection"`
		Messages         []pebblestore.MessageSnapshot     `json:"messages"`
		RunIntents       []sessionruntime.SessionRunIntent `json:"run_intents"`
		HighWatermarkSeq uint64                            `json:"high_watermark_seq"`
		NextSeq          uint64                            `json:"next_seq"`
	}
	if err := json.Unmarshal(replayRec.Body.Bytes(), &replay); err != nil {
		t.Fatalf("decode replay response: %v", err)
	}
	if !replay.OK || len(replay.Events) != 1 || replay.Events[0].Seq != 2 || replay.HighWatermarkSeq != 2 || replay.NextSeq != 2 {
		t.Fatalf("replay page = %+v", replay)
	}
	if replay.Projection.LastEventSeq != 3 || replay.Projection.ProjectionHighWatermarkSeq != 3 {
		t.Fatalf("projection = %+v", replay.Projection)
	}
	if len(replay.Messages) != 1 || replay.Messages[0].Content != "cp5 message 0" {
		t.Fatalf("messages = %+v", replay.Messages)
	}
	if len(replay.RunIntents) != 1 || replay.RunIntents[0].EventSeq != 2 {
		t.Fatalf("run intents = %+v", replay.RunIntents)
	}
}

func TestSessionsV3PrimaryCreateIsIdempotent(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	bindingID := seedSessionsV3PrimaryAuthority(t, server, "/workspace/v3")
	body := `{"client_request_id":"same-create","workspace_path":"/workspace/v3","swarm_id":"host-swarm-id","workspace_binding_id":"` + bindingID + `","target_kind":"host","target_relationship":"self","title":"V3 Idempotent","agent_name":"swarm","preference":{"provider":"codex","model":"gpt-5.4","thinking":"medium"}}`

	post := func() (int, string) {
		req := httptest.NewRequest(http.MethodPost, "/v3/sessions", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		server.Handler().ServeHTTP(rec, withTestPrincipal(req))
		return rec.Code, rec.Body.String()
	}
	status, firstBody := post()
	if status != http.StatusOK {
		t.Fatalf("first status = %d body=%s", status, firstBody)
	}
	status, secondBody := post()
	if status != http.StatusOK {
		t.Fatalf("second status = %d body=%s", status, secondBody)
	}

	var first, second struct {
		Session  pebblestore.SessionSnapshot          `json:"session"`
		Events   []sessionruntime.SessionEvent        `json:"events"`
		Mutation sessionruntime.SessionMutationResult `json:"mutation"`
	}
	if err := json.Unmarshal([]byte(firstBody), &first); err != nil {
		t.Fatalf("decode first: %v", err)
	}
	if err := json.Unmarshal([]byte(secondBody), &second); err != nil {
		t.Fatalf("decode second: %v", err)
	}
	if first.Session.ID != second.Session.ID || !second.Mutation.Replayed {
		t.Fatalf("idempotent responses first=%+v second=%+v", first, second)
	}
	events, err := sessionSvc.ListSessionEvents(first.Session.ID, 0, 10)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(events) != 1 || len(second.Events) != 0 || second.Mutation.Event.EventType != "session.created" {
		t.Fatalf("events persisted=%+v second=%+v mutation=%+v", events, second.Events, second.Mutation)
	}
}

func TestSessionsV3PrimaryStreamReplaysDurableEventsAfterRestart(t *testing.T) {
	t.Setenv("SWARM_API_NO_AUTH", "1")
	storePath := filepath.Join(t.TempDir(), "sessions-v3-stream.pebble")
	server, _, closeStore := newSessionsV3PrimaryAPITestServer(t, storePath)
	created := createSessionsV3PrimaryTestSession(t, server, "cp6-create", "CP6")
	postSessionsV3PrimaryTestMessage(t, server, created.ID, "cp6-message-0", "cp6 message 0")
	postSessionsV3PrimaryTestMessage(t, server, created.ID, "cp6-message-1", "cp6 message 1")
	if err := closeStore(); err != nil {
		t.Fatalf("close first store: %v", err)
	}

	restarted, _, closeRestarted := newSessionsV3PrimaryAPITestServer(t, storePath)
	defer func() { _ = closeRestarted() }()
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		restarted.Handler().ServeHTTP(w, withTestPrincipal(r))
	}))
	defer httpServer.Close()

	conn := dialSessionsV3PrimaryStream(t, httpServer.URL, created.ID, "after_seq=1")
	defer conn.Close()
	started := readSessionsV3PrimaryStreamFrame(t, conn)
	if started.Type != "replay.started" || !started.OK || started.HighWatermarkSeq != 3 {
		t.Fatalf("replay started = %+v", started)
	}
	first := readSessionsV3PrimaryStreamFrame(t, conn)
	second := readSessionsV3PrimaryStreamFrame(t, conn)
	if first.Type != "event" || first.Event == nil || first.Event.Seq != 2 || second.Event == nil || second.Event.Seq != 3 {
		t.Fatalf("stream replay events first=%+v second=%+v", first, second)
	}
	complete := readSessionsV3PrimaryStreamFrame(t, conn)
	if complete.Type != "replay.complete" || complete.LastSeq != 3 || complete.NextSeq != 3 {
		t.Fatalf("replay complete = %+v", complete)
	}
}

func TestSessionsV3PrimaryStreamTransitionsFromReplayToLiveEvents(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSession(t, server, "cp6-live-create", "CP6 live")
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		server.Handler().ServeHTTP(w, withTestPrincipal(r))
	}))
	defer httpServer.Close()

	conn := dialSessionsV3PrimaryStream(t, httpServer.URL, created.ID, "after_seq=1")
	defer conn.Close()
	started := readSessionsV3PrimaryStreamFrame(t, conn)
	complete := readSessionsV3PrimaryStreamFrame(t, conn)
	if started.Type != "replay.started" || complete.Type != "replay.complete" || complete.LastSeq != 1 {
		t.Fatalf("initial frames started=%+v complete=%+v", started, complete)
	}

	postSessionsV3PrimaryTestMessage(t, server, created.ID, "cp6-live-message", "cp6 live message")
	live := readSessionsV3PrimaryStreamFrame(t, conn)
	if live.Type != "event" || live.Event == nil || live.Event.Seq != 2 || live.Event.EventType != "session.message.appended" {
		t.Fatalf("live event = %+v", live)
	}
}

func TestSessionsV3PrimaryParentStreamDeliversLiveChildEvents(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	parent := createSessionsV3PrimaryTestSession(t, server, "cp9-parent-live-create", "CP9 parent live")
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		server.Handler().ServeHTTP(w, withTestPrincipal(r))
	}))
	defer httpServer.Close()

	conn := dialSessionsV3PrimaryStream(t, httpServer.URL, parent.ID, "after_seq=1")
	defer conn.Close()
	started := readSessionsV3PrimaryStreamFrame(t, conn)
	complete := readSessionsV3PrimaryStreamFrame(t, conn)
	if started.Type != "replay.started" || complete.Type != "replay.complete" || complete.LastSeq != 1 {
		t.Fatalf("initial parent stream frames started=%+v complete=%+v", started, complete)
	}

	principal := testPrincipal()
	child := pebblestore.SessionSnapshot{
		ID:             "cp9-child-live",
		UserID:         principal.UserID,
		AccountScopeID: principal.AccountScopeID,
		WorkspacePath:  parent.WorkspacePath,
		WorkspaceName:  parent.WorkspaceName,
		Title:          "CP9 child live",
		Mode:           "auto",
		Metadata: map[string]any{
			"parent_session_id": parent.ID,
			"lineage_kind":      "delegated_subagent",
		},
	}
	if _, err := server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{SessionID: child.ID, UserID: principal.UserID, AccountScopeID: principal.AccountScopeID, ClientRequestID: "cp9-child-live-create", IdempotencyKey: "cp9-child-live-create", PayloadHash: "hash-cp9-child-live-create", Kind: sessionruntime.SessionMutationCreateSession, Session: &child, NowUnixMs: time.Now().UnixMilli()}); err != nil {
		t.Fatalf("create child session mutation: %v", err)
	}
	created := readSessionsV3PrimaryStreamFrame(t, conn)
	if created.Type != "event" || created.Relation != "child" || created.ParentSessionID != parent.ID || created.SessionID != child.ID || created.LineageKind != "delegated_subagent" || created.Event == nil || created.Event.SessionID != child.ID || created.Event.Seq != 1 || created.Event.EventType != "session.created" || created.AfterSeq != 1 || created.LastSeq != 1 {
		t.Fatalf("child created frame = %+v", created)
	}

	message := pebblestore.MessageSnapshot{ID: "cp9-child-live-message", SessionID: child.ID, UserID: principal.UserID, AccountScopeID: principal.AccountScopeID, Role: "user", Content: "child live progress", CreatedAt: time.Now().UnixMilli()}
	if _, err := server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{SessionID: child.ID, UserID: principal.UserID, AccountScopeID: principal.AccountScopeID, ClientRequestID: "cp9-child-live-message", IdempotencyKey: "cp9-child-live-message", PayloadHash: "hash-cp9-child-live-message", Kind: sessionruntime.SessionMutationAppendMessage, Message: &message, NowUnixMs: time.Now().UnixMilli()}); err != nil {
		t.Fatalf("append child message mutation: %v", err)
	}
	progress := readSessionsV3PrimaryStreamFrame(t, conn)
	if progress.Type != "event" || progress.Relation != "child" || progress.ParentSessionID != parent.ID || progress.SessionID != child.ID || progress.Event == nil || progress.Event.SessionID != child.ID || progress.Event.Seq != 2 || progress.Event.EventType != "session.message.appended" || progress.AfterSeq != 1 || progress.LastSeq != 1 {
		t.Fatalf("child progress frame = %+v", progress)
	}
}

func TestSessionsV3PrimaryParentStreamReplaysKnownChildEvents(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	parent := createSessionsV3PrimaryTestSession(t, server, "cp9-parent-replay-create", "CP9 parent replay")
	principal := testPrincipal()
	child := pebblestore.SessionSnapshot{
		ID:             "cp9-child-replay",
		UserID:         principal.UserID,
		AccountScopeID: principal.AccountScopeID,
		WorkspacePath:  parent.WorkspacePath,
		WorkspaceName:  parent.WorkspaceName,
		Title:          "CP9 child replay",
		Mode:           "auto",
		Metadata: map[string]any{
			"parent_session_id": parent.ID,
			"lineage_kind":      "delegated_subagent",
		},
	}
	if _, err := server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{SessionID: child.ID, UserID: principal.UserID, AccountScopeID: principal.AccountScopeID, ClientRequestID: "cp9-child-replay-create", IdempotencyKey: "cp9-child-replay-create", PayloadHash: "hash-cp9-child-replay-create", Kind: sessionruntime.SessionMutationCreateSession, Session: &child, NowUnixMs: time.Now().UnixMilli()}); err != nil {
		t.Fatalf("create child session mutation: %v", err)
	}
	message := pebblestore.MessageSnapshot{ID: "cp9-child-replay-message", SessionID: child.ID, UserID: principal.UserID, AccountScopeID: principal.AccountScopeID, Role: "assistant", Content: "child replay progress", CreatedAt: time.Now().UnixMilli()}
	if _, err := server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{SessionID: child.ID, UserID: principal.UserID, AccountScopeID: principal.AccountScopeID, ClientRequestID: "cp9-child-replay-message", IdempotencyKey: "cp9-child-replay-message", PayloadHash: "hash-cp9-child-replay-message", Kind: sessionruntime.SessionMutationAppendMessage, Message: &message, NowUnixMs: time.Now().UnixMilli()}); err != nil {
		t.Fatalf("append child message mutation: %v", err)
	}
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		server.Handler().ServeHTTP(w, withTestPrincipal(r))
	}))
	defer httpServer.Close()

	conn := dialSessionsV3PrimaryStream(t, httpServer.URL, parent.ID, "after_seq=1")
	defer conn.Close()
	started := readSessionsV3PrimaryStreamFrame(t, conn)
	childCreated := readSessionsV3PrimaryStreamFrame(t, conn)
	childProgress := readSessionsV3PrimaryStreamFrame(t, conn)
	complete := readSessionsV3PrimaryStreamFrame(t, conn)
	if started.Type != "replay.started" || started.HighWatermarkSeq != 1 {
		t.Fatalf("replay started = %+v", started)
	}
	if childCreated.Type != "event" || childCreated.Relation != "child" || childCreated.SessionID != child.ID || childCreated.Event == nil || childCreated.Event.Seq != 1 || childCreated.Event.EventType != "session.created" || childCreated.LastSeq != 1 {
		t.Fatalf("child replay created = %+v", childCreated)
	}
	if childProgress.Type != "event" || childProgress.Relation != "child" || childProgress.SessionID != child.ID || childProgress.Event == nil || childProgress.Event.Seq != 2 || childProgress.Event.EventType != "session.message.appended" || childProgress.LastSeq != 1 {
		t.Fatalf("child replay progress = %+v", childProgress)
	}
	if complete.Type != "replay.complete" || complete.LastSeq != 1 {
		t.Fatalf("replay complete = %+v", complete)
	}
}

func TestSessionsV3PrimaryStreamPublishesExecutorCommittedEventsAndReplaysThem(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	installSessionsV3TestProvider(server, "stream committed assistant")
	exec := newSessionV3Executor(server)
	exec.startDelay = 0
	server.v3SessionExecutor = exec
	created := createSessionsV3PrimaryTestSessionWithPreference(t, server, "cp4-outbox-create", "CP4 outbox", pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"})
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		server.Handler().ServeHTTP(w, withTestPrincipal(r))
	}))
	defer httpServer.Close()

	conn := dialSessionsV3PrimaryStream(t, httpServer.URL, created.ID, "after_seq=1")
	defer conn.Close()
	started := readSessionsV3PrimaryStreamFrame(t, conn)
	complete := readSessionsV3PrimaryStreamFrame(t, conn)
	if started.Type != "replay.started" || complete.Type != "replay.complete" || complete.LastSeq != 1 {
		t.Fatalf("initial frames started=%+v complete=%+v", started, complete)
	}

	postSessionsV3PrimaryTestMessage(t, server, created.ID, "cp4-outbox-message", "stream committed assistant")
	wantLive := []string{"session.message.appended", "session.assistant.started", "session.assistant.delta", "session.assistant.completed"}
	for i, wantType := range wantLive {
		frame := readSessionsV3PrimaryStreamFrame(t, conn)
		wantSeq := uint64(i + 2)
		if frame.Type != "event" || frame.Event == nil || frame.Event.Seq != wantSeq || frame.Event.EventType != wantType {
			t.Fatalf("live frame %d = %+v, want seq=%d type=%s", i, frame, wantSeq, wantType)
		}
		if _, ok, err := sessionSvc.Store().GetV3SessionEvent(created.ID, frame.Event.Seq); err != nil || !ok {
			t.Fatalf("live event seq %d was not durable before publish: ok=%t err=%v", frame.Event.Seq, ok, err)
		}
	}

	messages := getSessionsV3PrimaryTestMessages(t, server, created.ID, 0, 10)
	if len(messages) != 2 || messages[1].Role != "assistant" || !strings.Contains(messages[1].Content, "stream committed assistant") {
		t.Fatalf("messages after executor completion = %+v", messages)
	}

	replay := dialSessionsV3PrimaryStream(t, httpServer.URL, created.ID, "after_seq=2")
	defer replay.Close()
	replayStarted := readSessionsV3PrimaryStreamFrame(t, replay)
	if replayStarted.Type != "replay.started" || replayStarted.HighWatermarkSeq != 5 {
		t.Fatalf("replay started = %+v", replayStarted)
	}
	for i, wantType := range []string{"session.assistant.started", "session.assistant.delta", "session.assistant.completed"} {
		frame := readSessionsV3PrimaryStreamFrame(t, replay)
		wantSeq := uint64(i + 3)
		if frame.Type != "event" || frame.Event == nil || frame.Event.Seq != wantSeq || frame.Event.EventType != wantType {
			t.Fatalf("replay frame %d = %+v, want seq=%d type=%s", i, frame, wantSeq, wantType)
		}
	}
	replayComplete := readSessionsV3PrimaryStreamFrame(t, replay)
	if replayComplete.Type != "replay.complete" || replayComplete.LastSeq != 5 {
		t.Fatalf("replay complete = %+v", replayComplete)
	}
}

func TestSessionsV3PrimaryStreamCarriesProviderReasoningEventsAndMessage(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	runner := installSessionsV3TestProvider(server, "final answer after reasoning")
	runner.handler = func(ctx context.Context, req provideriface.Request, onEvent func(provideriface.StreamEvent)) (provideriface.Response, error) {
		if onEvent != nil {
			onEvent(provideriface.StreamEvent{Type: provideriface.StreamEventReasoningSummaryDelta, ReasoningKey: "summary-1", Delta: "Inspecting"})
			onEvent(provideriface.StreamEvent{Type: provideriface.StreamEventReasoningSummaryDelta, ReasoningKey: "summary-1", Delta: "Inspecting files"})
			onEvent(provideriface.StreamEvent{Type: provideriface.StreamEventOutputTextDelta, Delta: "final answer after reasoning"})
		}
		return provideriface.Response{Text: "final answer after reasoning", StopReason: "stop"}, nil
	}
	exec := newSessionV3Executor(server)
	exec.startDelay = 0
	exec.deltaFlushMaxDelay = time.Hour
	server.v3SessionExecutor = exec
	created := createSessionsV3PrimaryTestSessionWithPreference(t, server, "reasoning-create", "reasoning stream", pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"})
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		server.Handler().ServeHTTP(w, withTestPrincipal(r))
	}))
	defer httpServer.Close()

	conn := dialSessionsV3PrimaryStream(t, httpServer.URL, created.ID, "after_seq=1")
	defer conn.Close()
	_ = readSessionsV3PrimaryStreamFrame(t, conn)
	_ = readSessionsV3PrimaryStreamFrame(t, conn)

	postSessionsV3PrimaryTestMessage(t, server, created.ID, "reasoning-message", "think then answer")
	seen := map[string]int{}
	var reasoningDeltaPayload struct {
		RunID        string `json:"run_id"`
		Delta        string `json:"delta"`
		ReasoningKey string `json:"reasoning_key"`
	}
	for {
		frame := readSessionsV3PrimaryStreamFrame(t, conn)
		if frame.Type != "event" || frame.Event == nil {
			continue
		}
		seen[frame.Event.EventType]++
		if frame.Event.EventType == "session.reasoning.delta" {
			var payload struct {
				RunID        string `json:"run_id"`
				Delta        string `json:"delta"`
				ReasoningKey string `json:"reasoning_key"`
			}
			if err := json.Unmarshal(frame.Event.Payload, &payload); err != nil {
				t.Fatalf("decode reasoning delta payload: %v", err)
			}
			reasoningDeltaPayload = payload
		}
		if frame.Event.EventType == "session.assistant.completed" {
			break
		}
	}
	for _, want := range []string{"session.reasoning.started", "session.reasoning.delta", "session.reasoning.completed"} {
		if seen[want] == 0 {
			t.Fatalf("missing %s in live stream; seen=%v", want, seen)
		}
	}
	for _, unwanted := range []string{"session.tool.started", "session.tool.delta", "session.tool.completed"} {
		if seen[unwanted] != 0 {
			t.Fatalf("unexpected synthetic reasoning tool event %s in live stream; seen=%v", unwanted, seen)
		}
	}
	if reasoningDeltaPayload.RunID == "" || reasoningDeltaPayload.Delta != "Inspecting files" || reasoningDeltaPayload.ReasoningKey != "summary-1" {
		t.Fatalf("unexpected reasoning delta payload: %+v", reasoningDeltaPayload)
	}
	messages, err := sessionSvc.ListSessionMessages(created.ID, 0, 10)
	if err != nil {
		t.Fatalf("list messages after reasoning run: %v", err)
	}
	for i := range messages {
		if messages[i].Role == "tool" && messages[i].Metadata["timeline_kind"] == "thinking" {
			t.Fatalf("unexpected synthetic thinking tool message: %+v; all messages=%+v", messages[i], messages)
		}
	}
}

func TestSessionsV3PrimaryLiveStreamPublishesProviderToolProgressAndCommittedCompletion(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	workspace := t.TempDir()
	runner := &sessionsV3RecordingProviderRunner{responses: []provideriface.Response{
		{FunctionCalls: []provideriface.FunctionCall{{CallID: "call-live-bash", Name: "bash", Arguments: `{"command":"printf 'live-tool-progress\n'","timeout_ms":1000}`}}},
		{Text: "final answer after live tool progress"},
	}}
	providers := registry.New()
	providers.RegisterRunner(runner)
	server.providers = providers
	runSvc := runruntime.NewService(sessionSvc, server.model, providers, tool.NewRuntime(1), server.perm.(*permission.Service), server.agents, nil, nil)
	server.runner = runSvc
	server.SetBypassPermissions(true)
	if _, _, _, err := server.agents.UpsertForAccount(testPrincipal().AccountScopeID, agentruntime.UpsertInput{Name: "swarm", Mode: agentruntime.ModePrimary, Provider: "test-provider", Model: "test-model", Thinking: "medium", RuntimeMode: pebblestore.AgentRuntimeModePlanAuto, ExitPlanModeEnabled: pebblestore.BoolPtr(true), ToolContract: &pebblestore.AgentToolContract{Tools: map[string]pebblestore.AgentToolConfig{"bash": {Enabled: pebblestore.BoolPtr(true)}}}, Enabled: pebblestore.BoolPtr(true), Prompt: "Swarm prompt"}); err != nil {
		t.Fatalf("upsert bash-enabled swarm agent: %v", err)
	}
	exec := newSessionV3Executor(server)
	exec.startDelay = 0
	server.v3SessionExecutor = exec
	created := createSessionsV3PrimaryTestSessionWithWorkspaceAndPreference(t, server, "live-tool-stream-create", "live tool stream", workspace, pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"})
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		server.Handler().ServeHTTP(w, withTestPrincipal(r))
	}))
	defer httpServer.Close()

	conn := dialSessionsV3PrimaryStream(t, httpServer.URL, created.ID, "after_seq=1")
	defer conn.Close()
	started := readSessionsV3PrimaryStreamFrame(t, conn)
	complete := readSessionsV3PrimaryStreamFrame(t, conn)
	if started.Type != "replay.started" || complete.Type != "replay.complete" || complete.LastSeq != 1 {
		t.Fatalf("initial stream frames started=%+v complete=%+v", started, complete)
	}

	postSessionsV3PrimaryTestMessage(t, server, created.ID, "live-tool-stream-message", "run a live bash tool")
	want := []string{"session.tool.started", "session.tool.delta", "session.tool.completed", "session.assistant.completed"}
	seen := make([]string, 0, len(want))
	for len(seen) < len(want) {
		frame := readSessionsV3PrimaryStreamFrame(t, conn)
		if frame.Type != "event" || frame.Event == nil {
			t.Fatalf("live stream frame = %+v, want event", frame)
		}
		eventType := strings.TrimSpace(frame.Event.EventType)
		if eventType == "session.message.appended" || eventType == "session.assistant.started" || eventType == "session.assistant.delta" {
			continue
		}
		seen = append(seen, eventType)
		if eventType == "session.tool.started" || eventType == "session.tool.delta" || eventType == "session.tool.completed" {
			var payload map[string]any
			if err := json.Unmarshal(frame.Event.Payload, &payload); err != nil {
				t.Fatalf("decode tool event payload: %v", err)
			}
			if payload["run_id"] == "" || payload["tool_name"] != "bash" || payload["call_id"] != "call-live-bash" || payload["step_id"] != "step-1" || payload["tool_instance_id"] != "step-1:call-live-bash" {
				t.Fatalf("tool event payload for %s = %+v", eventType, payload)
			}
		}
	}
	for i, wantType := range want {
		if seen[i] != wantType {
			t.Fatalf("live tool event order = %+v, want %+v", seen, want)
		}
	}
}

func TestSessionsV3PrimaryLiveStreamPublishesPermissionEventsBeforeProviderToolStart(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	workspace := t.TempDir()
	runner := &sessionsV3RecordingProviderRunner{responses: []provideriface.Response{
		{FunctionCalls: []provideriface.FunctionCall{{CallID: "call-permission-ask", Name: "ask-user", Arguments: `{"question":"Continue?","options":["yes","no"]}`}}},
		{Text: "final answer after permission approval"},
	}}
	providers := registry.New()
	providers.RegisterRunner(runner)
	server.providers = providers
	runSvc := runruntime.NewService(sessionSvc, server.model, providers, tool.NewRuntime(1), server.perm.(*permission.Service), server.agents, nil, nil)
	server.runner = runSvc
	if _, _, _, err := server.agents.UpsertForAccount(testPrincipal().AccountScopeID, agentruntime.UpsertInput{Name: "swarm", Mode: agentruntime.ModePrimary, Provider: "test-provider", Model: "test-model", Thinking: "medium", RuntimeMode: pebblestore.AgentRuntimeModePlanAuto, ExitPlanModeEnabled: pebblestore.BoolPtr(true), ToolContract: &pebblestore.AgentToolContract{Tools: map[string]pebblestore.AgentToolConfig{"ask_user": {Enabled: pebblestore.BoolPtr(true)}}}, Enabled: pebblestore.BoolPtr(true), Prompt: "Swarm prompt"}); err != nil {
		t.Fatalf("upsert ask-user-enabled swarm agent: %v", err)
	}
	exec := newSessionV3Executor(server)
	exec.startDelay = 0
	server.v3SessionExecutor = exec

	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		server.Handler().ServeHTTP(w, withTestPrincipal(r))
	}))
	defer httpServer.Close()

	created := createSessionsV3PrimaryHTTPTestSession(t, httpServer.URL, "permission-stream-create", "permission stream", workspace, pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"})
	conn := dialSessionsV3PrimaryStream(t, httpServer.URL, created.ID, "after_seq=1")
	defer conn.Close()
	replayStarted := readSessionsV3PrimaryStreamFrame(t, conn)
	replayComplete := readSessionsV3PrimaryStreamFrame(t, conn)
	if replayStarted.Type != "replay.started" || replayComplete.Type != "replay.complete" {
		t.Fatalf("initial stream replay frames = %+v %+v", replayStarted, replayComplete)
	}

	postSessionsV3PrimaryHTTPTestMessage(t, httpServer.URL, created.ID, "permission-stream-message", "ask before continuing")

	var permissionID string
	seen := make([]string, 0, 8)
	for permissionID == "" {
		frame := readSessionsV3PrimaryStreamFrame(t, conn)
		if frame.Type != "event" || frame.Event == nil {
			continue
		}
		eventType := strings.TrimSpace(frame.Event.EventType)
		seen = append(seen, eventType)
		if eventType == "session.tool.started" {
			t.Fatalf("session.tool.started was published before permission.requested; seen=%v", seen)
		}
		if eventType != "permission.requested" {
			continue
		}
		var payload struct {
			RunID          string                        `json:"run_id"`
			SessionID      string                        `json:"session_id"`
			Step           int                           `json:"step"`
			ToolName       string                        `json:"tool_name"`
			CallID         string                        `json:"call_id"`
			ToolInstanceID string                        `json:"tool_instance_id"`
			Permission     *pebblestore.PermissionRecord `json:"permission"`
		}
		if err := json.Unmarshal(frame.Event.Payload, &payload); err != nil {
			t.Fatalf("decode permission.requested payload: %v", err)
		}
		if payload.Permission == nil || strings.TrimSpace(payload.Permission.ID) == "" || payload.Permission.Status != pebblestore.PermissionStatusPending {
			t.Fatalf("permission.requested payload missing pending permission: %+v", payload)
		}
		if payload.SessionID != created.ID || payload.ToolName != "ask-user" || payload.CallID != "call-permission-ask" || payload.Step != 1 || payload.ToolInstanceID != "step-1:call-permission-ask" {
			t.Fatalf("permission.requested payload identity = %+v", payload)
		}
		permissionID = payload.Permission.ID
	}

	resolvePayload := []byte(`{"action":"allow_once","reason":"ok","approved_arguments":"yes"}`)
	resp, err := http.Post(httpServer.URL+"/v3/sessions/"+created.ID+"/permissions/"+permissionID+"/resolve", "application/json", bytes.NewReader(resolvePayload))
	if err != nil {
		t.Fatalf("resolve permission over HTTP: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("resolve permission status = %d", resp.StatusCode)
	}

	wantSuffix := []string{"permission.updated", "session.tool.started", "session.tool.completed", "session.assistant.completed"}
	seenSuffix := make([]string, 0, len(wantSuffix))
	for len(seenSuffix) < len(wantSuffix) {
		frame := readSessionsV3PrimaryStreamFrame(t, conn)
		if frame.Type != "event" || frame.Event == nil {
			continue
		}
		eventType := strings.TrimSpace(frame.Event.EventType)
		if eventType == "session.message.appended" || eventType == "session.assistant.started" || eventType == "session.assistant.delta" {
			continue
		}
		seenSuffix = append(seenSuffix, eventType)
		if eventType == "permission.updated" {
			var payload struct {
				Permission *pebblestore.PermissionRecord `json:"permission"`
			}
			if err := json.Unmarshal(frame.Event.Payload, &payload); err != nil {
				t.Fatalf("decode permission.updated payload: %v", err)
			}
			if payload.Permission == nil || payload.Permission.ID != permissionID || payload.Permission.Status != pebblestore.PermissionStatusApproved {
				t.Fatalf("permission.updated payload = %+v", payload)
			}
		}
	}
	for i, wantType := range wantSuffix {
		if seenSuffix[i] != wantType {
			t.Fatalf("event order after resolve = %v, want suffix %v", seenSuffix, wantSuffix)
		}
	}
	if runner.callCount != 2 {
		t.Fatalf("provider call count = %d, want tool request plus final", runner.callCount)
	}
}

func TestSessionsV3PrimaryStreamDoesNotRepublishReplayedMutations(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSession(t, server, "cp4-no-republish-create", "CP4 no republish")
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		server.Handler().ServeHTTP(w, withTestPrincipal(r))
	}))
	defer httpServer.Close()

	conn := dialSessionsV3PrimaryStream(t, httpServer.URL, created.ID, "after_seq=1")
	defer conn.Close()
	started := readSessionsV3PrimaryStreamFrame(t, conn)
	complete := readSessionsV3PrimaryStreamFrame(t, conn)
	if started.Type != "replay.started" || complete.Type != "replay.complete" || complete.LastSeq != 1 {
		t.Fatalf("initial frames started=%+v complete=%+v", started, complete)
	}

	postSessionsV3PrimaryTestMessage(t, server, created.ID, "cp4-no-republish-message", "publish once")
	live := readSessionsV3PrimaryStreamFrame(t, conn)
	if live.Type != "event" || live.Event == nil || live.Event.Seq != 2 || live.Event.EventType != "session.message.appended" {
		t.Fatalf("live event = %+v", live)
	}
	postSessionsV3PrimaryTestMessage(t, server, created.ID, "cp4-no-republish-message", "publish once")
	if frame, ok := readOptionalSessionsV3PrimaryStreamFrame(t, conn, 150*time.Millisecond); ok {
		t.Fatalf("duplicate idempotency replay unexpectedly republished frame: %+v", frame)
	}
}

func TestSessionsV3PrimaryStreamCursorErrors(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSession(t, server, "cp6-cursor-create", "CP6 cursor")
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		server.Handler().ServeHTTP(w, withTestPrincipal(r))
	}))
	defer httpServer.Close()

	ahead := dialSessionsV3PrimaryStream(t, httpServer.URL, created.ID, "after_seq=2")
	aheadFrame := readSessionsV3PrimaryStreamFrame(t, ahead)
	_ = ahead.Close()
	if aheadFrame.Type != "cursor.error" || aheadFrame.OK || !strings.Contains(aheadFrame.Error, "refetch required") {
		t.Fatalf("ahead cursor frame = %+v", aheadFrame)
	}

	malformed := dialSessionsV3PrimaryStream(t, httpServer.URL, created.ID, "after_seq=not-a-number")
	malformedFrame := readSessionsV3PrimaryStreamFrame(t, malformed)
	_ = malformed.Close()
	if malformedFrame.Type != "error" || !strings.Contains(malformedFrame.Error, "after_seq") {
		t.Fatalf("malformed cursor frame = %+v", malformedFrame)
	}
}

func TestSessionsV3PrimaryStandalonePathsWorkWithDispatchServicesDisabled(t *testing.T) {
	t.Setenv("SWARM_API_NO_AUTH", "1")
	storePath := filepath.Join(t.TempDir(), "sessions-v3-standalone.pebble")
	server, _, closeStore := newSessionsV3PrimaryAPITestServer(t, storePath)
	defer func() { _ = closeStore() }()

	created := createSessionsV3PrimaryTestSession(t, server, "cp8-standalone-create", "CP8 standalone")
	postSessionsV3PrimaryTestMessage(t, server, created.ID, "cp8-standalone-message", "standalone message")
	for _, path := range []string{
		"/v3/sessions?limit=10",
		"/v3/sessions/" + created.ID,
		"/v3/sessions/" + created.ID + "/messages?after_seq=0&limit=10",
		"/v3/sessions/" + created.ID + "/events?after_seq=0&limit=10",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		server.Handler().ServeHTTP(rec, withTestPrincipal(req))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d, want %d, body=%s", path, rec.Code, http.StatusOK, rec.Body.String())
		}
	}

	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		server.Handler().ServeHTTP(w, withTestPrincipal(r))
	}))
	defer httpServer.Close()
	conn := dialSessionsV3PrimaryStream(t, httpServer.URL, created.ID, "after_seq=1")
	defer conn.Close()
	started := readSessionsV3PrimaryStreamFrame(t, conn)
	event := readSessionsV3PrimaryStreamFrame(t, conn)
	complete := readSessionsV3PrimaryStreamFrame(t, conn)
	if started.Type != "replay.started" || event.Type != "event" || event.Event == nil || event.Event.Seq != 2 || complete.Type != "replay.complete" {
		t.Fatalf("stream frames started=%+v event=%+v complete=%+v", started, event, complete)
	}
}

func TestSessionsV3PrimaryStreamCapturesRealProviderMultiToolLoopContinuity(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "one.txt"), []byte("first durable stream result"), 0o644); err != nil {
		t.Fatalf("write first tool file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "two.txt"), []byte("second durable stream result"), 0o644); err != nil {
		t.Fatalf("write second tool file: %v", err)
	}

	runner := &sessionsV3RecordingProviderRunner{}
	runner.handler = func(_ context.Context, req provideriface.Request, onEvent func(provideriface.StreamEvent)) (provideriface.Response, error) {
		switch runner.callCount {
		case 1:
			if req.ToolInvoker == nil || req.ToolChoice != "auto" {
				return provideriface.Response{}, fmt.Errorf("initial provider request tool invoker=%v tool_choice=%q, want real tool loop", req.ToolInvoker != nil, req.ToolChoice)
			}
			if len(req.Input) != 1 || !sessionsV3TraceInputContains(req.Input, "read both files before answering") {
				return provideriface.Response{}, fmt.Errorf("initial provider input = %+v, want committed user prompt only", req.Input)
			}
			return provideriface.Response{FunctionCalls: []provideriface.FunctionCall{{CallID: "call-read-one", Name: "read", Arguments: `{"path":"one.txt"}`}}}, nil
		case 2:
			if !sessionsV3TraceInputContains(req.Input, "first durable stream result") || sessionsV3TraceInputContains(req.Input, "second durable stream result") {
				return provideriface.Response{}, fmt.Errorf("first continuation input = %+v, want first tool result only", req.Input)
			}
			return provideriface.Response{FunctionCalls: []provideriface.FunctionCall{{CallID: "call-read-two", Name: "read", Arguments: `{"path":"two.txt"}`}}}, nil
		case 3:
			if !sessionsV3TraceInputContains(req.Input, "first durable stream result") || !sessionsV3TraceInputContains(req.Input, "second durable stream result") {
				return provideriface.Response{}, fmt.Errorf("final continuation input = %+v, want both durable tool results", req.Input)
			}
			if onEvent != nil {
				onEvent(provideriface.StreamEvent{Type: provideriface.StreamEventOutputTextDelta, Delta: "final stream trace answer"})
			}
			return provideriface.Response{Text: "final stream trace answer", StopReason: "stop"}, nil
		default:
			return provideriface.Response{}, fmt.Errorf("unexpected provider call count %d", runner.callCount)
		}
	}
	providers := registry.New()
	providers.RegisterRunner(runner)
	server.providers = providers
	runSvc := runruntime.NewService(sessionSvc, server.model, providers, tool.NewRuntime(1), server.perm.(*permission.Service), server.agents, nil, nil)
	server.runner = runSvc
	server.SetBypassPermissions(true)
	if _, _, _, err := server.agents.UpsertForAccount(testPrincipal().AccountScopeID, agentruntime.UpsertInput{Name: "swarm", Mode: agentruntime.ModePrimary, Provider: "test-provider", Model: "test-model", Thinking: "medium", RuntimeMode: pebblestore.AgentRuntimeModePlanAuto, ExitPlanModeEnabled: pebblestore.BoolPtr(true), ToolContract: &pebblestore.AgentToolContract{Tools: map[string]pebblestore.AgentToolConfig{"read": {Enabled: pebblestore.BoolPtr(true)}}}, Enabled: pebblestore.BoolPtr(true), Prompt: "Swarm prompt"}); err != nil {
		t.Fatalf("upsert tool-enabled swarm agent: %v", err)
	}
	exec := newSessionV3Executor(server)
	exec.startDelay = 0
	exec.deltaFlushMaxDelay = time.Hour
	server.v3SessionExecutor = exec

	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		server.Handler().ServeHTTP(w, withTestPrincipal(r))
	}))
	defer httpServer.Close()

	created := createSessionsV3PrimaryHTTPTestSession(t, httpServer.URL, "stream-trace-create", "stream trace", workspace, pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"})
	conn := dialSessionsV3PrimaryStream(t, httpServer.URL, created.ID, "after_seq=1")
	defer conn.Close()
	replayStarted := readSessionsV3PrimaryStreamFrame(t, conn)
	replayComplete := readSessionsV3PrimaryStreamFrame(t, conn)
	if replayStarted.Type != "replay.started" || replayComplete.Type != "replay.complete" {
		t.Fatalf("initial stream replay frames = %+v %+v", replayStarted, replayComplete)
	}

	postSessionsV3PrimaryHTTPTestMessage(t, httpServer.URL, created.ID, "stream-trace-message", "read both files before answering")

	var captured []sessionsV3RawStreamFrame
	for {
		capture := readSessionsV3PrimaryStreamRawFrame(t, conn, 3*time.Second)
		captured = append(captured, capture)
		if capture.Frame.Type == "event" && capture.Frame.Event != nil && capture.Frame.Event.EventType == "session.assistant.completed" {
			break
		}
	}

	intent := waitForSessionsV3RunIntentStatus(t, sessionSvc, created.ID, sessionruntime.RunIntentCompleted)
	messages, err := sessionSvc.ListSessionMessages(created.ID, 0, 20)
	if err != nil {
		t.Fatalf("list messages after stream trace: %v", err)
	}
	events, err := sessionSvc.ListSessionEvents(created.ID, 0, 80)
	if err != nil {
		t.Fatalf("list events after stream trace: %v", err)
	}
	intents, err := sessionSvc.Store().ListV3SessionRunIntents(created.ID, 0, 10)
	if err != nil {
		t.Fatalf("list run intents after stream trace: %v", err)
	}

	for i, capture := range captured {
		t.Logf("ws_frame[%02d]=%s", i, capture.Raw)
	}
	for i, event := range events {
		t.Logf("db_event[%02d]=seq=%d type=%s payload=%s", i, event.Seq, event.EventType, string(event.Payload))
	}
	for i, message := range messages {
		t.Logf("db_message[%02d]=seq=%d role=%s id=%s content=%q metadata=%+v", i, message.GlobalSeq, message.Role, message.ID, message.Content, message.Metadata)
	}
	for i, runIntent := range intents {
		t.Logf("db_run_intent[%02d]=run_id=%s status=%s blocked_reason=%q updated_at=%d", i, runIntent.RunID, runIntent.Status, runIntent.BlockedReason, runIntent.UpdatedAt)
	}

	if intent.BlockedReason != "" {
		t.Fatalf("completed run has blocked reason: %+v", intent)
	}
	if runner.callCount != 3 {
		t.Fatalf("provider call count = %d, want assistant tool1 -> continuation tool2 -> final", runner.callCount)
	}
	if len(messages) != 4 || messages[0].Role != "user" || messages[1].Role != "tool" || messages[2].Role != "tool" || messages[3].Role != "assistant" {
		t.Fatalf("messages after stream trace = %+v, want user/tool/tool/assistant", messages)
	}
	if !strings.Contains(messages[1].Content, "first durable stream result") || !strings.Contains(messages[2].Content, "second durable stream result") || messages[3].Content != "final stream trace answer" {
		t.Fatalf("message contents after stream trace = %+v", messages)
	}

	dbLiveTypes, dbLiveSeqs := sessionsV3TraceEventTypesAfterSeq(events, 1)
	wsTypes, wsSeqs := sessionsV3TraceStreamEventTypes(captured)
	if strings.Join(wsTypes, "|") != strings.Join(dbLiveTypes, "|") {
		t.Fatalf("websocket event order = %v, DB live event order = %v", wsTypes, dbLiveTypes)
	}
	if fmt.Sprint(wsSeqs) != fmt.Sprint(dbLiveSeqs) {
		t.Fatalf("websocket seqs = %v, DB live seqs = %v", wsSeqs, dbLiveSeqs)
	}
	for _, want := range []string{"session.message.appended", "session.assistant.started", "session.tool.started", "session.tool.completed", "session.assistant.delta", "session.assistant.completed"} {
		if sessionsV3TraceIndex(dbLiveTypes, want) < 0 {
			t.Fatalf("DB live event order missing %q: %v", want, dbLiveTypes)
		}
	}
	toolStartedSteps := sessionsV3TraceEventSteps(events, "session.tool.started")
	toolCompletedSteps := sessionsV3TraceEventSteps(events, "session.tool.completed")
	if fmt.Sprint(toolStartedSteps) != "[1 2]" || fmt.Sprint(toolCompletedSteps) != "[1 2]" {
		t.Fatalf("tool event steps started=%v completed=%v, want [1 2] for both", toolStartedSteps, toolCompletedSteps)
	}
	sessionsV3AssertToolIdentity(t, events, "session.tool.started")
	sessionsV3AssertToolIdentity(t, events, "session.tool.completed")
	completedIndex := sessionsV3TraceIndex(dbLiveTypes, "session.assistant.completed")
	if completedIndex < 0 {
		t.Fatalf("missing assistant completion in live DB event order: %v", dbLiveTypes)
	}
	for i, eventType := range dbLiveTypes[:completedIndex] {
		if eventType == "session.assistant.completed" || eventType == "session.run.failed" {
			t.Fatalf("terminal event %q emitted before final assistant completion at live index %d: %v", eventType, i, dbLiveTypes)
		}
	}
	sessionsV3AssertStreamStillOpenAfterCompletion(t, conn)
}

func TestSessionsV3PrimaryToolFailurePersistsDurableTerminalEvent(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	workspace := t.TempDir()
	runner := &sessionsV3RecordingProviderRunner{responses: []provideriface.Response{
		{FunctionCalls: []provideriface.FunctionCall{{CallID: "call-read-missing", Name: "read", Arguments: `{"path":"missing.txt"}`}}},
		{Text: "final answer after failed tool"},
	}}
	providers := registry.New()
	providers.RegisterRunner(runner)
	server.providers = providers
	runSvc := runruntime.NewService(sessionSvc, server.model, providers, tool.NewRuntime(1), server.perm.(*permission.Service), server.agents, nil, nil)
	server.runner = runSvc
	server.SetBypassPermissions(true)
	if _, _, _, err := server.agents.UpsertForAccount(testPrincipal().AccountScopeID, agentruntime.UpsertInput{Name: "swarm", Mode: agentruntime.ModePrimary, Provider: "test-provider", Model: "test-model", Thinking: "medium", RuntimeMode: pebblestore.AgentRuntimeModePlanAuto, ExitPlanModeEnabled: pebblestore.BoolPtr(true), ToolContract: &pebblestore.AgentToolContract{Tools: map[string]pebblestore.AgentToolConfig{"read": {Enabled: pebblestore.BoolPtr(true)}}}, Enabled: pebblestore.BoolPtr(true), Prompt: "Swarm prompt"}); err != nil {
		t.Fatalf("upsert read-enabled swarm agent: %v", err)
	}
	exec := newSessionV3Executor(server)
	exec.startDelay = 0
	server.v3SessionExecutor = exec

	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		server.Handler().ServeHTTP(w, withTestPrincipal(r))
	}))
	defer httpServer.Close()

	created := createSessionsV3PrimaryTestSessionWithWorkspaceAndPreference(t, server, "tool-failure-create", "tool failure", workspace, pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"})
	snapshotCursor := hydrateV3RealtimeSnapshotEndpointCursor(t, server, created.ID)

	postSessionsV3PrimaryTestMessage(t, server, created.ID, "tool-failure-message", "read a missing file then answer")
	waitForSessionsV3RunIntentStatus(t, sessionSvc, created.ID, sessionruntime.RunIntentCompleted)

	conn := dialV3RealtimeStreamWithQuery(t, httpServer.URL, "endpoint_cursor="+snapshotCursor+"&sessions="+created.ID)
	defer conn.Close()
	startedReplay := readV3RealtimeFrame(t, conn)
	assertV3RealtimeFrame(t, startedReplay, V3RealtimeKindReplayStart, created.ID, 0)
	seen := make(map[string]int)
	for {
		frame := readV3RealtimeFrame(t, conn)
		if frame.Kind == V3RealtimeKindReplayDone {
			break
		}
		if frame.Kind != V3RealtimeKindEvent || frame.Event == nil {
			continue
		}
		eventType := strings.TrimSpace(frame.Event.EventType)
		seen[eventType]++
		if eventType == "session.tool.failed" {
			var payload map[string]any
			if err := json.Unmarshal(frame.Event.Payload, &payload); err != nil {
				t.Fatalf("decode failed tool payload: %v", err)
			}
			if payload["run_id"] == "" || payload["tool_name"] != "read" || payload["call_id"] != "call-read-missing" || payload["step_id"] != "step-1" || payload["tool_instance_id"] != "step-1:call-read-missing" || payload["status"] != "failed" || payload["recorded_at"] == nil || strings.TrimSpace(fmt.Sprint(payload["error"])) == "" {
				t.Fatalf("failed tool payload = %+v", payload)
			}
		}
	}
	if seen["session.tool.started"] != 1 || seen["session.tool.failed"] != 1 || seen["session.tool.completed"] != 0 {
		t.Fatalf("tool lifecycle counts = %+v, want one started and one failed terminal only", seen)
	}
	events, err := sessionSvc.ListSessionEvents(created.ID, 0, 80)
	if err != nil {
		t.Fatalf("list events after failed tool: %v", err)
	}
	sessionsV3AssertToolIdentity(t, events, "session.tool.started")
	sessionsV3AssertToolIdentity(t, events, "session.tool.failed")
}

func TestSessionsV3PrimaryStreamDisambiguatesReusedProviderToolCallIDs(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "one.txt"), []byte("first reused-call stream result"), 0o644); err != nil {
		t.Fatalf("write first file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "two.txt"), []byte("second reused-call stream result"), 0o644); err != nil {
		t.Fatalf("write second file: %v", err)
	}
	runner := &sessionsV3RecordingProviderRunner{responses: []provideriface.Response{
		{FunctionCalls: []provideriface.FunctionCall{{CallID: "call-reused", Name: "read", Arguments: `{"path":"one.txt"}`}}},
		{FunctionCalls: []provideriface.FunctionCall{{CallID: "call-reused", Name: "read", Arguments: `{"path":"two.txt"}`}}},
		{Text: "final answer after reused call IDs"},
	}}
	providers := registry.New()
	providers.RegisterRunner(runner)
	server.providers = providers
	runSvc := runruntime.NewService(sessionSvc, server.model, providers, tool.NewRuntime(1), server.perm.(*permission.Service), server.agents, nil, nil)
	server.runner = runSvc
	server.SetBypassPermissions(true)
	if _, _, _, err := server.agents.UpsertForAccount(testPrincipal().AccountScopeID, agentruntime.UpsertInput{Name: "swarm", Mode: agentruntime.ModePrimary, Provider: "test-provider", Model: "test-model", Thinking: "medium", RuntimeMode: pebblestore.AgentRuntimeModePlanAuto, ExitPlanModeEnabled: pebblestore.BoolPtr(true), ToolContract: &pebblestore.AgentToolContract{Tools: map[string]pebblestore.AgentToolConfig{"read": {Enabled: pebblestore.BoolPtr(true)}}}, Enabled: pebblestore.BoolPtr(true), Prompt: "Swarm prompt"}); err != nil {
		t.Fatalf("upsert read-enabled swarm agent: %v", err)
	}
	exec := newSessionV3Executor(server)
	exec.startDelay = 0
	server.v3SessionExecutor = exec

	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		server.Handler().ServeHTTP(w, withTestPrincipal(r))
	}))
	defer httpServer.Close()

	created := createSessionsV3PrimaryHTTPTestSession(t, httpServer.URL, "stream-reused-call-create", "stream reused call IDs", workspace, pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"})
	conn := dialSessionsV3PrimaryStream(t, httpServer.URL, created.ID, "after_seq=1")
	defer conn.Close()
	startedReplay := readSessionsV3PrimaryStreamFrame(t, conn)
	completedReplay := readSessionsV3PrimaryStreamFrame(t, conn)
	if startedReplay.Type != "replay.started" || completedReplay.Type != "replay.complete" || completedReplay.LastSeq != 1 {
		t.Fatalf("initial stream replay frames = %+v %+v", startedReplay, completedReplay)
	}

	postSessionsV3PrimaryHTTPTestMessage(t, httpServer.URL, created.ID, "stream-reused-call-message", "read both files before answering")

	type toolIdentity struct {
		Seq            uint64
		EventType      string
		Step           int    `json:"step"`
		StepID         string `json:"step_id"`
		CallID         string `json:"call_id"`
		ToolInstanceID string `json:"tool_instance_id"`
		ToolName       string `json:"tool_name"`
	}
	var startedTools []toolIdentity
	var completedTools []toolIdentity
	for {
		frame := readSessionsV3PrimaryStreamFrame(t, conn)
		if frame.Type != "event" || frame.Event == nil {
			continue
		}
		eventType := strings.TrimSpace(frame.Event.EventType)
		if eventType == "session.tool.started" || eventType == "session.tool.completed" {
			var identity toolIdentity
			if err := json.Unmarshal(frame.Event.Payload, &identity); err != nil {
				t.Fatalf("decode %s payload seq=%d: %v", eventType, frame.Event.Seq, err)
			}
			identity.Seq = frame.Event.Seq
			identity.EventType = eventType
			if identity.CallID != "call-reused" || identity.ToolName != "read" || identity.StepID == "" || identity.ToolInstanceID == "" {
				t.Fatalf("unexpected reused-call tool identity for %s seq=%d: %+v", eventType, frame.Event.Seq, identity)
			}
			if eventType == "session.tool.started" {
				startedTools = append(startedTools, identity)
			} else {
				completedTools = append(completedTools, identity)
			}
		}
		if eventType == "session.assistant.completed" {
			break
		}
	}
	if runner.callCount != 3 {
		t.Fatalf("provider call count = %d, want two tool steps plus final", runner.callCount)
	}
	if len(startedTools) != 2 || len(completedTools) != 2 {
		t.Fatalf("tool event identities started=%+v completed=%+v, want two of each", startedTools, completedTools)
	}
	if fmt.Sprint([]string{startedTools[0].StepID, startedTools[1].StepID}) != "[step-1 step-2]" || fmt.Sprint([]string{completedTools[0].StepID, completedTools[1].StepID}) != "[step-1 step-2]" {
		t.Fatalf("tool step IDs started=%+v completed=%+v, want step-1 then step-2", startedTools, completedTools)
	}
	wantToolInstanceIDs := []string{"step-1:call-reused", "step-2:call-reused"}
	for i := range startedTools {
		if startedTools[i].ToolInstanceID != completedTools[i].ToolInstanceID {
			t.Fatalf("tool instance changed between started/completed for step %d: started=%+v completed=%+v", i+1, startedTools[i], completedTools[i])
		}
		if startedTools[i].ToolInstanceID != wantToolInstanceIDs[i] {
			t.Fatalf("tool instance for step %d = %q, want %q; started=%+v completed=%+v", i+1, startedTools[i].ToolInstanceID, wantToolInstanceIDs[i], startedTools, completedTools)
		}
	}
	if startedTools[0].ToolInstanceID == startedTools[1].ToolInstanceID || completedTools[0].ToolInstanceID == completedTools[1].ToolInstanceID {
		t.Fatalf("backend reused tool_instance_id when provider reused call_id; started=%+v completed=%+v", startedTools, completedTools)
	}

	messages, err := sessionSvc.ListSessionMessages(created.ID, 0, 20)
	if err != nil {
		t.Fatalf("list messages after reused call ID run: %v", err)
	}
	var persistedToolInstanceIDs []string
	for _, message := range messages {
		if strings.TrimSpace(message.Role) != "tool" {
			continue
		}
		var content struct {
			CallID         string `json:"call_id"`
			ToolInstanceID string `json:"tool_instance_id"`
		}
		if err := json.Unmarshal([]byte(message.Content), &content); err != nil {
			t.Fatalf("decode persisted tool message content: %v content=%q", err, message.Content)
		}
		metadataToolInstanceID := strings.TrimSpace(fmt.Sprint(message.Metadata["tool_instance_id"]))
		if content.CallID != "call-reused" || content.ToolInstanceID == "" || metadataToolInstanceID != content.ToolInstanceID {
			t.Fatalf("persisted tool message missing stable instance ID content=%+v metadata=%+v", content, message.Metadata)
		}
		persistedToolInstanceIDs = append(persistedToolInstanceIDs, content.ToolInstanceID)
	}
	if fmt.Sprint(persistedToolInstanceIDs) != "[step-1:call-reused step-2:call-reused]" {
		t.Fatalf("persisted tool instance IDs = %v, want per-step IDs", persistedToolInstanceIDs)
	}
}

func TestSessionsV3PrimaryStreamHandlerDoesNotUseV2RunStreamOrRuntime(t *testing.T) {
	body, err := os.ReadFile("sessions_v3_stream_ws.go")
	if err != nil {
		t.Fatalf("read sessions_v3_stream_ws.go: %v", err)
	}
	for _, required := range []string{"ListRealtimeOutboxAfter", "ListRealtimeOutboxForSessionAfterSeq", "handleSessionV3PrimaryStream", "v3RealtimeOutbox"} {
		if !strings.Contains(string(body), required) {
			t.Fatalf("sessions_v3_stream_ws.go missing required outbox-backed V3 stream symbol %q", required)
		}
	}
	for _, forbidden := range []string{"ReplaySessionEvents", "sessionV3StreamHub", "runStreamManager", "handleRunStream", "proxyManagedHostRunStream", "dispatchRemoteRuntime", "routedSessionTarget", "gorillaws"} {
		if strings.Contains(string(body), forbidden) {
			t.Fatalf("sessions_v3_stream_ws.go contains forbidden runtime/v2 stream symbol %q", forbidden)
		}
	}
}

func installSessionsV3TestProvider(server *Server, text string) *sessionsV3RecordingProviderRunner {
	runner := &sessionsV3RecordingProviderRunner{text: text}
	providers := registry.New()
	providers.RegisterRunner(runner)
	server.providers = providers
	return runner
}

func seedSessionsV3PrimaryAuthority(t *testing.T, server *Server, workspacePath string) string {
	t.Helper()
	if server == nil || server.topology == nil || server.swarmStore == nil {
		t.Fatal("server topology and swarm store are required")
	}
	workspacePath = strings.TrimSpace(workspacePath)
	if workspacePath == "" {
		t.Fatal("workspace path is required")
	}
	now := time.Now().UnixMilli()
	if _, err := server.swarmStore.PutLocalNode(pebblestore.SwarmLocalNodeRecord{SwarmID: "host-swarm-id", Name: "host-swarm", Role: "master", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("put local node: %v", err)
	}
	if err := server.topology.UpsertRuntime(pebblestore.TopologyRuntimeRecord{SwarmID: "host-swarm-id", UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, Name: "host-swarm", Role: "master", Relationship: "self", Status: "online", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("upsert local runtime: %v", err)
	}
	if _, err := server.topology.EnsureLocalSelfPlacementForPrincipal(testPrincipal().AccountScopeID, testPrincipal().UserID); err != nil {
		t.Fatalf("ensure self placement: %v", err)
	}
	bindingID := "binding-v3-primary-" + strings.NewReplacer("/", "-", " ", "-", "_", "-").Replace(strings.Trim(workspacePath, "/"))
	if _, err := server.topology.UpsertWorkspaceBinding(pebblestore.TopologyWorkspaceBindingRecord{
		BindingID:                       bindingID,
		UserID:                          testPrincipal().UserID,
		AccountScopeID:                  testPrincipal().AccountScopeID,
		SourceWorkspaceID:               "workspace-v3-" + bindingID,
		SourceWorkspaceGeneration:       1,
		SourceWorkspacePath:             workspacePath,
		SourceWorkspaceName:             filepath.Base(workspacePath),
		DestinationRuntimeSwarmID:       "host-swarm-id",
		DestinationAuthorityHostSwarmID: "host-swarm-id",
		DestinationHostSwarmID:          "host-swarm-id",
		DestinationRuntimeKind:          pebblestore.TopologyRuntimeKindHost,
		DestinationWorkspacePath:        workspacePath,
		PlacementGeneration:             1,
		BindingGeneration:               1,
		State:                           pebblestore.TopologyWorkspaceBindingStateBound,
		AccessMode:                      pebblestore.TopologyWorkspaceBindingAccessModeReadWrite,
		MaterializationKind:             pebblestore.TopologyWorkspaceBindingMaterializationSource,
		AttestedByHostSwarmID:           "host-swarm-id",
		Writable:                        true,
	}); err != nil {
		t.Fatalf("upsert v3 binding: %v", err)
	}
	return bindingID
}

func createSessionsV3PrimaryTestSession(t *testing.T, server *Server, clientRequestID, title string) pebblestore.SessionSnapshot {
	t.Helper()
	return createSessionsV3PrimaryTestSessionWithWorkspace(t, server, clientRequestID, title, "/workspace/cp6")
}

func createSessionsV3PrimaryTestSessionWithWorkspace(t *testing.T, server *Server, clientRequestID, title, workspacePath string) pebblestore.SessionSnapshot {
	t.Helper()
	bindingID := seedSessionsV3PrimaryAuthority(t, server, workspacePath)
	body := fmt.Sprintf(`{"client_request_id":%q,"workspace_path":%q,"swarm_id":"host-swarm-id","workspace_binding_id":%q,"target_kind":"host","target_relationship":"self","title":%q,"agent_name":"swarm"}`, clientRequestID, workspacePath, bindingID, title)
	req := httptest.NewRequest(http.MethodPost, "/v3/sessions", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("create status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var created struct {
		Session pebblestore.SessionSnapshot `json:"session"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if strings.TrimSpace(created.Session.ID) == "" {
		t.Fatalf("created session missing id: %+v", created.Session)
	}
	return created.Session
}

func postSessionsV3PrimaryTestMessage(t *testing.T, server *Server, sessionID, clientRequestID, content string) {
	t.Helper()
	body := fmt.Sprintf(`{"client_request_id":%q,"role":"user","content":%q}`, clientRequestID, content)
	req := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+sessionID+"/messages", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("message status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
}

func appendSessionsV3PrimaryTestUserMessage(t *testing.T, server *Server, sessionID, clientRequestID, content string) {
	t.Helper()
	now := time.Now().UnixMilli()
	message := pebblestore.MessageSnapshot{ID: "test-user-" + clientRequestID, Role: "user", Content: content, CreatedAt: now}
	payloadHash, err := sessionV3ExecutorPayloadHash(sessionID, "test-user", sessionruntime.RunIntentRunning, "", "session.message.appended", clientRequestID+":"+content)
	if err != nil {
		t.Fatalf("hash user message payload: %v", err)
	}
	if _, err := server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID:       sessionID,
		UserID:          testPrincipal().UserID,
		AccountScopeID:  testPrincipal().AccountScopeID,
		ClientRequestID: clientRequestID,
		IdempotencyKey:  clientRequestID,
		PayloadHash:     payloadHash,
		RequestHash:     payloadHash,
		Kind:            sessionruntime.SessionMutationAppendMessage,
		EventType:       "session.message.appended",
		Message:         &message,
		NowUnixMs:       now,
	}); err != nil {
		t.Fatalf("append user message: %v", err)
	}
}

func appendSessionsV3PrimaryTestSystemMessage(t *testing.T, server *Server, sessionID, clientRequestID, content string) {
	t.Helper()
	now := time.Now().UnixMilli()
	message := pebblestore.MessageSnapshot{ID: "test-system-" + clientRequestID, Role: "system", Content: content, CreatedAt: now}
	payloadHash, err := sessionV3ExecutorPayloadHash(sessionID, "test-system", sessionruntime.RunIntentRunning, "", "session.message.appended", clientRequestID+":"+content)
	if err != nil {
		t.Fatalf("hash system message payload: %v", err)
	}
	if _, err := server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID:       sessionID,
		UserID:          testPrincipal().UserID,
		AccountScopeID:  testPrincipal().AccountScopeID,
		ClientRequestID: clientRequestID,
		IdempotencyKey:  clientRequestID,
		PayloadHash:     payloadHash,
		RequestHash:     payloadHash,
		Kind:            sessionruntime.SessionMutationAppendMessage,
		EventType:       "session.message.appended",
		Message:         &message,
		NowUnixMs:       now,
	}); err != nil {
		t.Fatalf("append system message: %v", err)
	}
}

func getSessionsV3PrimaryTestMessages(t *testing.T, server *Server, sessionID string, afterSeq uint64, limit int) []pebblestore.MessageSnapshot {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/v3/sessions/%s/messages?after_seq=%d&limit=%d", sessionID, afterSeq, limit), nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET messages status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload struct {
		OK       bool                          `json:"ok"`
		Messages []pebblestore.MessageSnapshot `json:"messages"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode GET messages response: %v", err)
	}
	if !payload.OK {
		t.Fatalf("GET messages payload not ok: %+v", payload)
	}
	return payload.Messages
}

func dialSessionsV3PrimaryStream(t *testing.T, baseURL, sessionID, rawQuery string) *gorillaws.Conn {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(baseURL, "http") + "/v3/sessions/" + sessionID + "/stream"
	if rawQuery != "" {
		wsURL += "?" + rawQuery
	}
	conn, resp, err := gorillaws.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		if resp != nil {
			t.Fatalf("dial v3 stream: %v status=%d", err, resp.StatusCode)
		}
		t.Fatalf("dial v3 stream: %v", err)
	}
	return conn
}

func readSessionsV3PrimaryStreamFrame(t *testing.T, conn *gorillaws.Conn) sessionV3StreamFrame {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	_, raw, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read v3 stream frame: %v", err)
	}
	var frame sessionV3StreamFrame
	if err := json.Unmarshal(raw, &frame); err != nil {
		t.Fatalf("decode v3 stream frame %s: %v", string(raw), err)
	}
	return frame
}

func readOptionalSessionsV3PrimaryStreamFrame(t *testing.T, conn *gorillaws.Conn, timeout time.Duration) (sessionV3StreamFrame, bool) {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	_, raw, err := conn.ReadMessage()
	if err != nil {
		return sessionV3StreamFrame{}, false
	}
	var frame sessionV3StreamFrame
	if err := json.Unmarshal(raw, &frame); err != nil {
		t.Fatalf("decode v3 stream frame %s: %v", string(raw), err)
	}
	return frame, true
}

type sessionsV3RawStreamFrame struct {
	Raw   string
	Frame sessionV3StreamFrame
}

func readSessionsV3PrimaryStreamRawFrame(t *testing.T, conn *gorillaws.Conn, timeout time.Duration) sessionsV3RawStreamFrame {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	_, raw, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read v3 stream raw frame: %v", err)
	}
	var frame sessionV3StreamFrame
	if err := json.Unmarshal(raw, &frame); err != nil {
		t.Fatalf("decode v3 stream raw frame %s: %v", string(raw), err)
	}
	return sessionsV3RawStreamFrame{Raw: string(raw), Frame: frame}
}

func sessionsV3AssertStreamStillOpenAfterCompletion(t *testing.T, conn *gorillaws.Conn) {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(150 * time.Millisecond)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	_, raw, err := conn.ReadMessage()
	if err == nil {
		t.Fatalf("stream produced unexpected immediate frame after completion instead of staying idle/open: %s", string(raw))
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return
	}
	t.Fatalf("stream closed or errored immediately after assistant completion: %v", err)
}

func createSessionsV3PrimaryHTTPTestSession(t *testing.T, baseURL, clientRequestID, title, workspacePath string, pref pebblestore.ModelPreference) pebblestore.SessionSnapshot {
	t.Helper()
	payload := map[string]any{
		"client_request_id": clientRequestID,
		"workspace_path":    workspacePath,
		"title":             title,
		"agent_name":        "swarm",
		"preference":        pref,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal HTTP create payload: %v", err)
	}
	resp, err := http.Post(baseURL+"/v3/sessions", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("HTTP create v3 session: %v", err)
	}
	defer resp.Body.Close()
	var created struct {
		OK      bool                          `json:"ok"`
		Session pebblestore.SessionSnapshot   `json:"session"`
		Events  []sessionruntime.SessionEvent `json:"events"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode HTTP create response: %v", err)
	}
	if resp.StatusCode != http.StatusOK || !created.OK || strings.TrimSpace(created.Session.ID) == "" || len(created.Events) != 1 || created.Events[0].EventType != "session.created" {
		t.Fatalf("HTTP create response status=%d payload=%+v", resp.StatusCode, created)
	}
	return created.Session
}

func postSessionsV3PrimaryHTTPTestMessage(t *testing.T, baseURL, sessionID, clientRequestID, content string) {
	t.Helper()
	payload := map[string]any{"client_request_id": clientRequestID, "role": "user", "content": content}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal HTTP message payload: %v", err)
	}
	resp, err := http.Post(baseURL+"/v3/sessions/"+sessionID+"/messages", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("HTTP post v3 message: %v", err)
	}
	defer resp.Body.Close()
	var posted struct {
		OK        bool                            `json:"ok"`
		RunIntent *pebblestore.V3SessionRunIntent `json:"run_intent"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&posted); err != nil {
		t.Fatalf("decode HTTP message response: %v", err)
	}
	if resp.StatusCode != http.StatusOK || !posted.OK || posted.RunIntent == nil || posted.RunIntent.Status != sessionruntime.RunIntentPendingExecutor {
		t.Fatalf("HTTP message response status=%d payload=%+v", resp.StatusCode, posted)
	}
}

func sessionsV3TraceInputContains(input []map[string]any, needle string) bool {
	raw, err := json.Marshal(input)
	return err == nil && strings.Contains(string(raw), needle)
}

func sessionsV3ProviderInputHasTopLevelType(input []map[string]any, itemType string) bool {
	itemType = strings.TrimSpace(itemType)
	for _, item := range input {
		if strings.EqualFold(strings.TrimSpace(fmt.Sprint(item["type"])), itemType) {
			return true
		}
	}
	return false
}

func sessionsV3ProviderInputContainsContentText(input []map[string]any, needle string) bool {
	for _, item := range input {
		content, ok := item["content"].([]map[string]any)
		if !ok {
			continue
		}
		for _, part := range content {
			if strings.Contains(fmt.Sprint(part["text"]), needle) {
				return true
			}
		}
	}
	return false
}

func sessionsV3TraceEventTypesAfterSeq(events []sessionruntime.SessionEvent, afterSeq uint64) ([]string, []uint64) {
	types := make([]string, 0, len(events))
	seqs := make([]uint64, 0, len(events))
	for _, event := range events {
		if event.Seq <= afterSeq {
			continue
		}
		types = append(types, event.EventType)
		seqs = append(seqs, event.Seq)
	}
	return types, seqs
}

func sessionsV3TraceStreamEventTypes(captures []sessionsV3RawStreamFrame) ([]string, []uint64) {
	types := make([]string, 0, len(captures))
	seqs := make([]uint64, 0, len(captures))
	for _, capture := range captures {
		if capture.Frame.Type != "event" || capture.Frame.Event == nil {
			continue
		}
		types = append(types, capture.Frame.Event.EventType)
		seqs = append(seqs, capture.Frame.Event.Seq)
	}
	return types, seqs
}

func sessionsV3TraceIndex(items []string, want string) int {
	for i, item := range items {
		if item == want {
			return i
		}
	}
	return -1
}

func sessionsV3TraceEventSteps(events []sessionruntime.SessionEvent, eventType string) []int {
	var steps []int
	for _, event := range events {
		if event.EventType != eventType {
			continue
		}
		var payload struct {
			Step int `json:"step"`
		}
		if err := json.Unmarshal(event.Payload, &payload); err == nil {
			steps = append(steps, payload.Step)
		}
	}
	return steps
}

func sessionsV3AssertToolIdentity(t *testing.T, events []sessionruntime.SessionEvent, eventType string) {
	t.Helper()
	count := 0
	for _, event := range events {
		if event.EventType != eventType {
			continue
		}
		count++
		var payload map[string]any
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatalf("decode %s payload seq=%d: %v", eventType, event.Seq, err)
		}
		step, _ := payload["step"].(float64)
		wantStepID := fmt.Sprintf("step-%d", int(step))
		callID := strings.TrimSpace(fmt.Sprint(payload["call_id"]))
		toolInstanceID := strings.TrimSpace(fmt.Sprint(payload["tool_instance_id"]))
		wantToolInstanceID := wantStepID + ":" + callID
		if step <= 0 || payload["step_id"] != wantStepID || callID == "" || toolInstanceID != wantToolInstanceID {
			t.Fatalf("%s payload missing stable tool identity seq=%d want_instance_id=%q payload=%+v", eventType, event.Seq, wantToolInstanceID, payload)
		}
	}
	if count == 0 {
		t.Fatalf("missing %s events", eventType)
	}
}

func newSessionsV3PrimaryAPITestServer(t *testing.T, storePath string) (*Server, *sessionruntime.Service, func() error) {
	t.Helper()
	store, err := pebblestore.Open(storePath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	eventLog, err := pebblestore.NewEventLog(store)
	if err != nil {
		_ = store.Close()
		t.Fatalf("new event log: %v", err)
	}
	sessionSvc := sessionruntime.NewService(pebblestore.NewSessionStore(store), eventLog)
	agentSvc := agentruntime.NewService(pebblestore.NewAgentStore(store), eventLog)
	if err := agentSvc.EnsureDefaults(); err != nil {
		_ = store.Close()
		t.Fatalf("ensure agent defaults: %v", err)
	}
	if _, _, _, err := agentSvc.UpsertForAccount(testPrincipal().AccountScopeID, agentruntime.UpsertInput{Name: "swarm", Mode: agentruntime.ModePrimary, Enabled: pebblestore.BoolPtr(true), Prompt: "Swarm test primary prompt", ToolContract: &pebblestore.AgentToolContract{Preset: "custom", Tools: map[string]pebblestore.AgentToolConfig{"read": {Enabled: pebblestore.BoolPtr(true)}}}}); err != nil {
		_ = store.Close()
		t.Fatalf("create swarm agent: %v", err)
	}
	modelSvc := modelruntime.NewService(pebblestore.NewModelStore(store), eventLog, nil)
	runner := &sessionsV3RecordingProviderRunner{text: "recovered provider answer"}
	providers := registry.New()
	providers.RegisterRunner(runner)
	permissionSvc := permission.NewService(pebblestore.NewPermissionStore(store), eventLog, nil)
	permissionSvc.SetSessionResolver(sessionSvc)
	runSvc := runruntime.NewService(sessionSvc, modelSvc, providers, tool.NewRuntime(1), permissionSvc, agentSvc, nil, nil)
	server := NewServer(nil, agentSvc, modelSvc, runSvc, sessionSvc, nil, nil, nil, providers, permissionSvc, nil, eventLog, stream.NewHub(eventLog))
	server.v3SessionExecutor = nil
	closeStore := func() error {
		server.CancelInFlightRuns()
		server.WaitForInFlightRuns(2 * time.Second)
		return store.Close()
	}
	return server, sessionSvc, closeStore
}

func TestSessionsV3ExecutorRecoversPendingRunAfterRestart(t *testing.T) {
	t.Setenv("SWARM_API_NO_AUTH", "1")
	storePath := filepath.Join(t.TempDir(), "sessions-v3-executor-recovery.pebble")
	server, _, closeStore := newSessionsV3PrimaryAPITestServer(t, storePath)
	created := createSessionsV3PrimaryTestSessionWithPreference(t, server, "recover-create", "recover pending", pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"})
	postSessionsV3PrimaryTestMessage(t, server, created.ID, "recover-message", "recover me")
	if err := closeStore(); err != nil {
		t.Fatalf("close first store: %v", err)
	}

	restarted, sessionSvc, closeRestarted := newSessionsV3PrimaryAPITestServer(t, storePath)
	defer func() { _ = closeRestarted() }()
	exec := newSessionV3Executor(restarted)
	exec.startDelay = 0
	restarted.v3SessionExecutor = exec
	waitForSessionsV3MessageCount(t, sessionSvc, created.ID, 2)
	messages, err := sessionSvc.ListSessionMessages(created.ID, 0, 10)
	if err != nil {
		t.Fatalf("list messages after recovery: %v", err)
	}
	if len(messages) != 2 || messages[1].Role != "assistant" || messages[1].Content != "recovered provider answer" {
		t.Fatalf("messages after recovery = %+v", messages)
	}
	exec.recoverDurableRuns(restarted.runCtx)
	if !restarted.WaitForInFlightRuns(2 * time.Second) {
		t.Fatalf("executor did not drain after repeated recovery scan")
	}
	messages, err = sessionSvc.ListSessionMessages(created.ID, 0, 10)
	if err != nil {
		t.Fatalf("list messages after repeated recovery: %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("messages after repeated recovery = %+v, want no duplicate assistant", messages)
	}
	events, err := sessionSvc.ListSessionEvents(created.ID, 0, 20)
	if err != nil {
		t.Fatalf("list events after repeated recovery: %v", err)
	}
	var completed int
	for _, event := range events {
		if event.EventType == "session.assistant.completed" {
			completed++
		}
	}
	if completed != 1 {
		t.Fatalf("assistant completed events after repeated recovery = %d events=%+v", completed, events)
	}
}

func TestSessionsV3ExecutorCoalescesProviderDeltas(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	runner := &sessionsV3RecordingProviderRunner{deltas: []string{"hel", "lo", " ", "world"}, text: "hello world"}
	providers := registry.New()
	providers.RegisterRunner(runner)
	server.providers = providers
	exec := newSessionV3Executor(server)
	exec.startDelay = 0
	exec.deltaFlushMaxBytes = 64
	exec.deltaFlushMaxDelay = time.Hour
	server.v3SessionExecutor = exec

	created := createSessionsV3PrimaryTestSessionWithPreference(t, server, "coalesced-provider-create", "coalesced provider", pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"})
	postSessionsV3PrimaryTestMessage(t, server, created.ID, "coalesced-provider-message", "coalesce provider deltas")
	waitForSessionsV3MessageCount(t, sessionSvc, created.ID, 2)

	events, err := sessionSvc.ListSessionEvents(created.ID, 0, 20)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	var deltas []sessionruntime.SessionEvent
	for _, event := range events {
		if event.EventType == "session.assistant.delta" {
			deltas = append(deltas, event)
		}
	}
	if len(deltas) != 1 {
		t.Fatalf("assistant delta events = %d, want one coalesced event events=%+v", len(deltas), events)
	}
	var payload struct {
		RunID      string `json:"run_id"`
		DeltaIndex int    `json:"delta_index"`
		Delta      string `json:"delta"`
	}
	if err := json.Unmarshal(deltas[0].Payload, &payload); err != nil {
		t.Fatalf("decode delta payload: %v", err)
	}
	if payload.DeltaIndex != 1 || payload.Delta != "hello world" {
		t.Fatalf("delta payload = %+v", payload)
	}
}

func TestSessionsV3ExecutorCoalescesProviderReasoningDeltas(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	runner := installSessionsV3TestProvider(server, "final answer after coalesced reasoning")
	runner.handler = func(ctx context.Context, req provideriface.Request, onEvent func(provideriface.StreamEvent)) (provideriface.Response, error) {
		if onEvent != nil {
			onEvent(provideriface.StreamEvent{Type: provideriface.StreamEventReasoningSummaryDelta, ReasoningKey: "summary-1", Delta: "Inspecting"})
			onEvent(provideriface.StreamEvent{Type: provideriface.StreamEventReasoningSummaryDelta, ReasoningKey: "summary-1", Delta: "Inspecting files"})
			onEvent(provideriface.StreamEvent{Type: provideriface.StreamEventReasoningSummaryDelta, ReasoningKey: "summary-1", Delta: "Inspecting files carefully"})
			onEvent(provideriface.StreamEvent{Type: provideriface.StreamEventOutputTextDelta, Delta: "final answer after coalesced reasoning"})
		}
		return provideriface.Response{Text: "final answer after coalesced reasoning", StopReason: "stop"}, nil
	}
	providers := registry.New()
	providers.RegisterRunner(runner)
	server.providers = providers
	exec := newSessionV3Executor(server)
	exec.startDelay = 0
	exec.reasoningDeltaFlushMaxBytes = 1024
	exec.reasoningDeltaFlushMaxDelay = time.Hour
	server.v3SessionExecutor = exec

	created := createSessionsV3PrimaryTestSessionWithPreference(t, server, "coalesced-reasoning-create", "coalesced reasoning", pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"})
	postSessionsV3PrimaryTestMessage(t, server, created.ID, "coalesced-reasoning-message", "coalesce reasoning deltas")
	waitForSessionsV3MessageCount(t, sessionSvc, created.ID, 2)

	events, err := sessionSvc.ListSessionEvents(created.ID, 0, 20)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	var reasoningDeltas []sessionruntime.SessionEvent
	for _, event := range events {
		if event.EventType == "session.reasoning.delta" {
			reasoningDeltas = append(reasoningDeltas, event)
		}
	}
	if len(reasoningDeltas) != 1 {
		t.Fatalf("reasoning delta events = %d, want one coalesced event events=%+v", len(reasoningDeltas), events)
	}
	var payload struct {
		Delta string `json:"delta"`
	}
	if err := json.Unmarshal(reasoningDeltas[0].Payload, &payload); err != nil {
		t.Fatalf("decode reasoning delta payload: %v", err)
	}
	if payload.Delta != "Inspecting files carefully" {
		t.Fatalf("reasoning delta payload = %+v", payload)
	}
}

func TestSessionsV3ExecutorFlushesProviderDeltaAtSizeBoundary(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	runner := &sessionsV3RecordingProviderRunner{deltas: []string{"ab", "cd", "ef"}, text: "abcdef"}
	providers := registry.New()
	providers.RegisterRunner(runner)
	server.providers = providers
	exec := newSessionV3Executor(server)
	exec.startDelay = 0
	exec.deltaFlushMaxBytes = 4
	exec.deltaFlushMaxDelay = time.Hour
	server.v3SessionExecutor = exec

	created := createSessionsV3PrimaryTestSessionWithPreference(t, server, "boundary-provider-create", "boundary provider", pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"})
	postSessionsV3PrimaryTestMessage(t, server, created.ID, "boundary-provider-message", "flush at size boundary")
	waitForSessionsV3MessageCount(t, sessionSvc, created.ID, 2)

	events, err := sessionSvc.ListSessionEvents(created.ID, 0, 20)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	var got []string
	for _, event := range events {
		if event.EventType != "session.assistant.delta" {
			continue
		}
		var payload struct {
			Delta string `json:"delta"`
		}
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatalf("decode delta payload: %v", err)
		}
		got = append(got, payload.Delta)
	}
	if strings.Join(got, "|") != "abcd|ef" {
		t.Fatalf("coalesced deltas = %#v, want [abcd ef]", got)
	}
}

func TestSessionsV3ExecutorFailsNonCompletionProviderStopReason(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	runner := &sessionsV3RecordingProviderRunner{response: provideriface.Response{Text: "partial provider answer", StopReason: "length"}}
	providers := registry.New()
	providers.RegisterRunner(runner)
	server.providers = providers
	exec := newSessionV3Executor(server)
	exec.startDelay = 0
	server.v3SessionExecutor = exec

	created := createSessionsV3PrimaryTestSessionWithPreference(t, server, "noncompletion-provider-create", "noncompletion provider", pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"})
	postSessionsV3PrimaryTestMessage(t, server, created.ID, "noncompletion-provider-message", "hit length stop")
	intent := waitForSessionsV3RunIntentStatus(t, sessionSvc, created.ID, sessionruntime.RunIntentFailed)
	if !strings.Contains(intent.BlockedReason, "length") {
		t.Fatalf("failed intent = %+v, want reason containing length", intent)
	}
	messages, err := sessionSvc.ListSessionMessages(created.ID, 0, 10)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 1 || messages[0].Role != "user" {
		t.Fatalf("messages after non-completion stop = %+v, want only committed user message", messages)
	}
}

func TestSessionsV3ExecutorRecordsFailurePayload(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	runner := &sessionsV3RecordingProviderRunner{err: fmt.Errorf("provider exploded")}
	providers := registry.New()
	providers.RegisterRunner(runner)
	server.providers = providers
	exec := newSessionV3Executor(server)
	exec.startDelay = 0
	server.v3SessionExecutor = exec

	created := createSessionsV3PrimaryTestSessionWithPreference(t, server, "failure-provider-create", "failure provider", pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"})
	postSessionsV3PrimaryTestMessage(t, server, created.ID, "failure-provider-message", "fail provider")
	waitForSessionsV3RunIntentStatus(t, sessionSvc, created.ID, sessionruntime.RunIntentFailed)

	events, err := sessionSvc.ListSessionEvents(created.ID, 0, 20)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	var failure *sessionruntime.SessionEvent
	for _, event := range events {
		if event.EventType == "session.run.failed" {
			copy := event
			failure = &copy
		}
	}
	if failure == nil {
		t.Fatalf("missing run failure event: %+v", events)
	}
	var payload struct {
		RunID  string `json:"run_id"`
		Status string `json:"status"`
		Error  string `json:"error"`
	}
	if err := json.Unmarshal(failure.Payload, &payload); err != nil {
		t.Fatalf("decode failure payload: %v", err)
	}
	if payload.Status != sessionruntime.RunIntentFailed || !strings.Contains(payload.Error, "provider exploded") {
		t.Fatalf("failure payload = %+v", payload)
	}
	messages, err := sessionSvc.ListSessionMessages(created.ID, 0, 10)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 1 || messages[0].Role != "user" {
		t.Fatalf("messages after failure = %+v, want only committed user message", messages)
	}
}

func TestSessionsV3ExecutorUsesProviderFromCommittedHistory(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	runner := &sessionsV3RecordingProviderRunner{
		text: "provider answer",
		response: provideriface.Response{
			ID:         "provider-response-1",
			Model:      "provider-model-resolved",
			StopReason: "stop",
			Usage:      provideriface.TokenUsage{InputTokens: 3, OutputTokens: 4, TotalTokens: 7},
		},
	}
	providers := registry.New()
	providers.RegisterRunner(runner)
	server.providers = providers
	exec := newSessionV3Executor(server)
	exec.startDelay = 0
	server.v3SessionExecutor = exec

	created := createSessionsV3PrimaryTestSessionWithPreference(t, server, "provider-create", "provider", pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"})
	postSessionsV3PrimaryTestMessage(t, server, created.ID, "provider-message", "use real provider")
	waitForSessionsV3MessageCount(t, sessionSvc, created.ID, 2)

	messages, err := sessionSvc.ListSessionMessages(created.ID, 0, 10)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 2 || messages[0].Role != "user" || messages[1].Role != "assistant" || messages[1].Content != "provider answer" {
		t.Fatalf("messages = %+v", messages)
	}
	if messages[1].Metadata["executor_kind"] != "v3_provider" || messages[1].Metadata["provider"] != "test-provider" || messages[1].Metadata["model"] != "provider-model-resolved" || messages[1].Metadata["provider_response_id"] != "provider-response-1" {
		t.Fatalf("assistant metadata = %+v", messages[1].Metadata)
	}
	if runner.callCount != 1 {
		t.Fatalf("provider call count = %d, want 1", runner.callCount)
	}
	events, err := sessionSvc.ListSessionEvents(created.ID, 0, 20)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	var deltas int
	for _, event := range events {
		if event.EventType == "session.assistant.delta" {
			deltas++
		}
	}
	if deltas != 1 {
		t.Fatalf("assistant delta events = %d, want 1 events=%+v", deltas, events)
	}
	if len(runner.lastRequest.Input) != 1 {
		t.Fatalf("provider input = %+v, want committed user message only", runner.lastRequest.Input)
	}
	if runner.lastRequest.Model != "test-model" || runner.lastRequest.Thinking != "medium" || runner.lastRequest.SessionID != created.ID {
		t.Fatalf("provider request = %+v", runner.lastRequest)
	}
	if !strings.Contains(runner.lastRequest.Instructions, "Active agent profile:") || !strings.Contains(runner.lastRequest.Instructions, "- name: swarm") {
		t.Fatalf("provider instructions = %q", runner.lastRequest.Instructions)
	}
}

func TestSessionsV3ExecutorReplaysCodexNativeOutputItems(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	firstOutput := []any{
		map[string]any{
			"type":              "reasoning",
			"id":                "rs_codex_native_1",
			"output_index":      float64(0),
			"content":           []any{},
			"summary":           []any{},
			"encrypted_content": "encrypted-reasoning",
		},
		map[string]any{
			"type":         "message",
			"id":           "msg_codex_native_1",
			"output_index": float64(1),
			"phase":        "final_answer",
			"role":         "assistant",
			"status":       "completed",
			"content": []any{
				map[string]any{
					"type":        "output_text",
					"text":        "first answer",
					"annotations": []any{},
					"logprobs":    []any{},
				},
			},
		},
	}
	runner := &sessionsV3RecordingProviderRunner{
		id: "codex",
		responses: []provideriface.Response{
			{
				ID:         "resp_codex_native_1",
				Model:      "gpt-5.5",
				Text:       "first answer",
				StopReason: "stop",
				Raw: map[string]any{
					"response": map[string]any{
						"id":     "resp_codex_native_1",
						"output": firstOutput,
					},
				},
			},
			{
				ID:         "resp_codex_native_2",
				Model:      "gpt-5.5",
				Text:       "second answer",
				StopReason: "stop",
				Raw: map[string]any{
					"response": map[string]any{
						"id": "resp_codex_native_2",
						"output": []any{
							map[string]any{
								"type": "message",
								"id":   "msg_codex_native_2",
								"role": "assistant",
								"content": []any{
									map[string]any{"type": "output_text", "text": "second answer"},
								},
							},
						},
					},
				},
			},
		},
	}
	providers := registry.New()
	providers.RegisterRunner(runner)
	server.providers = providers
	exec := newSessionV3Executor(server)
	exec.startDelay = 0
	server.v3SessionExecutor = exec

	created := createSessionsV3PrimaryTestSessionWithPreference(t, server, "codex-native-create", "codex native", pebblestore.ModelPreference{Provider: "codex", Model: "gpt-5.5", Thinking: "high"})
	postSessionsV3PrimaryTestMessage(t, server, created.ID, "codex-native-message-1", "first question")
	waitForSessionsV3MessageCount(t, sessionSvc, created.ID, 2)

	messages, err := sessionSvc.ListSessionMessages(created.ID, 0, 10)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 2 || messages[1].Metadata["provider_output_format"] != "responses_api" {
		t.Fatalf("assistant metadata missing native output marker: %+v", messages)
	}
	if items := messages[1].Metadata["provider_output_items"]; len(cloneSessionsV3ProviderItemSlice(items)) != 2 {
		t.Fatalf("assistant metadata native output = %#v, want two items", items)
	}

	postSessionsV3PrimaryTestMessage(t, server, created.ID, "codex-native-message-2", "second question")
	waitForSessionsV3MessageCount(t, sessionSvc, created.ID, 4)

	runner.mu.Lock()
	requests := append([]provideriface.Request(nil), runner.requests...)
	runner.mu.Unlock()
	if len(requests) != 2 {
		t.Fatalf("provider request count = %d, want 2", len(requests))
	}
	secondInput := requests[1].Input
	if len(secondInput) != 4 {
		t.Fatalf("second provider input length = %d, want 4: %#v", len(secondInput), secondInput)
	}
	if secondInput[1]["type"] != "reasoning" || secondInput[1]["id"] != "rs_codex_native_1" || secondInput[1]["encrypted_content"] != "encrypted-reasoning" {
		t.Fatalf("second provider input[1] = %#v, want persisted Codex reasoning item", secondInput[1])
	}
	if _, ok := secondInput[1]["output_index"]; ok {
		t.Fatalf("second provider input[1] includes response-only output_index: %#v", secondInput[1])
	}
	if summary := cloneSessionsV3ProviderItemSlice(secondInput[1]["summary"]); len(summary) != 0 {
		t.Fatalf("second provider input[1] summary = %#v, want empty array", secondInput[1]["summary"])
	}
	if content, ok := secondInput[1]["content"]; !ok || content != nil {
		t.Fatalf("second provider input[1] content = %#v, want explicit null", content)
	}
	if secondInput[2]["type"] != "message" || secondInput[2]["id"] != "msg_codex_native_1" {
		t.Fatalf("second provider input[2] = %#v, want persisted Codex native output item", secondInput[2])
	}
	if _, ok := secondInput[2]["output_index"]; ok {
		t.Fatalf("second provider input[2] includes response-only output_index: %#v", secondInput[2])
	}
	if _, ok := secondInput[2]["phase"]; ok {
		t.Fatalf("second provider input[2] includes Swarm-only phase: %#v", secondInput[2])
	}
	content := cloneSessionsV3ProviderItemSlice(secondInput[2]["content"])
	if len(content) != 1 {
		t.Fatalf("native output content = %#v, want one item", secondInput[2]["content"])
	}
	contentItem, ok := content[0].(map[string]any)
	if !ok || contentItem["text"] != "first answer" {
		t.Fatalf("native output content[0] = %#v, want first answer", content[0])
	}
	if _, ok := contentItem["logprobs"]; ok {
		t.Fatalf("native output content[0] includes response-only logprobs: %#v", contentItem)
	}
	if _, ok := contentItem["annotations"]; ok {
		t.Fatalf("native output content[0] includes response-only annotations: %#v", contentItem)
	}
	if secondInput[3]["role"] != "user" {
		t.Fatalf("second provider input[3] = %#v, want follow-up user message", secondInput[3])
	}
}

func TestSessionsV3ExecutorStreamsAndPersistsCodexUsage(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	runner := &sessionsV3RecordingProviderRunner{
		id:   "codex",
		text: "codex answer",
		response: provideriface.Response{
			ID:         "resp_codex_usage_1",
			Model:      "gpt-5.5",
			StopReason: "stop",
			Usage: provideriface.TokenUsage{
				InputTokens:     180000,
				OutputTokens:    10,
				TotalTokens:     180010,
				CacheReadTokens: 11776,
				Source:          "codex_api_usage",
				Transport:       "websocket",
				ConnectedViaWS:  pebblestore.BoolPtr(true),
				APIUsageRawPath: "response.usage",
			},
		},
	}
	providers := registry.New()
	providers.RegisterRunner(runner)
	server.providers = providers
	exec := newSessionV3Executor(server)
	exec.startDelay = 0
	server.v3SessionExecutor = exec

	workspace := t.TempDir()
	bindingID := seedSessionsV3PrimaryAuthority(t, server, workspace)
	createBody := fmt.Sprintf(`{"client_request_id":"codex-usage-create","workspace_path":%q,"swarm_id":"host-swarm-id","workspace_binding_id":%q,"target_kind":"host","target_relationship":"self","title":"codex usage","mode":"auto","agent_name":"swarm","preference":{"provider":"codex","model":"gpt-5.5","thinking":"high"}}`, workspace, bindingID)
	createReq := httptest.NewRequest(http.MethodPost, "/v3/sessions", bytes.NewBufferString(createBody))
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(createRec, withTestPrincipal(createReq))
	if createRec.Code != http.StatusOK {
		t.Fatalf("create status = %d, want %d, body=%s", createRec.Code, http.StatusOK, createRec.Body.String())
	}
	var createdResp struct {
		Session pebblestore.SessionSnapshot `json:"session"`
	}
	if err := json.Unmarshal(createRec.Body.Bytes(), &createdResp); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	created := createdResp.Session
	postSessionsV3PrimaryTestMessage(t, server, created.ID, "codex-usage-message", "check usage")
	waitForSessionsV3MessageCount(t, sessionSvc, created.ID, 2)

	summary, ok, err := sessionSvc.GetUsageSummary(created.ID)
	if err != nil {
		t.Fatalf("get usage summary: %v", err)
	}
	if !ok {
		t.Fatal("usage summary missing")
	}
	if summary.Provider != "codex" || summary.Model != "gpt-5.5" || summary.Source != "codex_api_usage" {
		t.Fatalf("usage summary identity = %+v", summary)
	}
	if summary.InputTokens != 180000 || summary.OutputTokens != 10 || summary.TotalTokens != 180010 || summary.CacheReadTokens != 11776 {
		t.Fatalf("usage summary tokens = %+v", summary)
	}
	if summary.ContextWindow <= 0 || summary.RemainingTokens != int64(summary.ContextWindow)-summary.TotalTokens {
		t.Fatalf("usage summary remaining should use latest normalized provider usage: %+v", summary)
	}
	_, usageSummary2, _, err := sessionSvc.RecordTurnUsage(created.ID, pebblestore.SessionTurnUsageSnapshot{
		RunID:           "codex-usage-second-response",
		Provider:        "codex",
		Model:           "gpt-5.5",
		Source:          "codex_api_usage",
		Transport:       "websocket",
		ContextWindow:   summary.ContextWindow,
		InputTokens:     116011,
		OutputTokens:    272,
		TotalTokens:     116283,
		CacheReadTokens: 115200,
	})
	if err != nil {
		t.Fatalf("record second usage: %v", err)
	}
	if usageSummary2.TotalTokens != 116283 {
		t.Fatalf("usage summary should keep latest provider snapshot, got %+v", usageSummary2)
	}
	if usageSummary2.RemainingTokens != int64(usageSummary2.ContextWindow)-116283 {
		t.Fatalf("remaining tokens should use latest provider snapshot, got %+v", usageSummary2)
	}

	events, err := sessionSvc.ListSessionEvents(created.ID, 0, 100)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	var usageEvent *sessionruntime.SessionEvent
	for i := range events {
		if events[i].EventType == "run.usage.updated" {
			usageEvent = &events[i]
			break
		}
	}
	if usageEvent == nil {
		t.Fatalf("run.usage.updated event missing: %+v", events)
	}
	var payload struct {
		TurnUsage    pebblestore.SessionTurnUsageSnapshot `json:"turn_usage"`
		UsageSummary pebblestore.SessionUsageSummary      `json:"usage_summary"`
	}
	if err := json.Unmarshal(usageEvent.Payload, &payload); err != nil {
		t.Fatalf("decode usage event payload: %v", err)
	}
	if payload.TurnUsage.InputTokens != 180000 || payload.UsageSummary.TotalTokens != 180010 {
		t.Fatalf("usage event payload = %+v", payload)
	}

	hydrated, ok, err := server.hydrateSessionsV3PrimaryWithLimits(testPrincipal(), created.ID, 10, 100)
	if err != nil || !ok {
		t.Fatalf("hydrate session ok=%v err=%v", ok, err)
	}
	if hydrated.UsageSummary == nil || hydrated.UsageSummary.TotalTokens != usageSummary2.TotalTokens || hydrated.UsageSummary.RemainingTokens != usageSummary2.RemainingTokens {
		t.Fatalf("hydrated usage summary = %+v", hydrated.UsageSummary)
	}
}

func TestSessionsV3ProviderToolLoopRecordsCodexUsagePerProviderStep(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	runner := &sessionsV3RecordingProviderRunner{
		id: "codex",
		responses: []provideriface.Response{
			{
				ID:          "resp_codex_usage_step_1",
				Model:       "gpt-5.5",
				RestartTurn: true,
				Usage: provideriface.TokenUsage{
					InputTokens:     100,
					TotalTokens:     100,
					Source:          "codex_api_usage",
					Transport:       "websocket",
					ConnectedViaWS:  pebblestore.BoolPtr(true),
					APIUsageRawPath: "response.usage",
				},
			},
			{
				ID:         "resp_codex_usage_step_2",
				Model:      "gpt-5.5",
				Text:       "done",
				StopReason: "stop",
				Usage: provideriface.TokenUsage{
					InputTokens:     200,
					OutputTokens:    5,
					TotalTokens:     205,
					Source:          "codex_api_usage",
					Transport:       "websocket",
					ConnectedViaWS:  pebblestore.BoolPtr(true),
					APIUsageRawPath: "response.usage",
				},
			},
		},
	}
	providers := registry.New()
	providers.RegisterRunner(runner)
	server.providers = providers
	exec := newSessionV3Executor(server)
	exec.startDelay = 0
	server.v3SessionExecutor = exec

	created := createSessionsV3PrimaryTestSessionWithPreference(t, server, "codex-step-usage-create", "codex step usage", pebblestore.ModelPreference{Provider: "codex", Model: "gpt-5.5", Thinking: "high"})
	postSessionsV3PrimaryTestMessage(t, server, created.ID, "codex-step-usage-message", "check usage cadence")
	waitForSessionsV3MessageCount(t, sessionSvc, created.ID, 2)
	if runner.callCount != 2 {
		t.Fatalf("provider call count = %d, want 2", runner.callCount)
	}

	events, err := sessionSvc.ListSessionEvents(created.ID, 0, 100)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	var payloads []struct {
		TurnUsage    pebblestore.SessionTurnUsageSnapshot `json:"turn_usage"`
		UsageSummary pebblestore.SessionUsageSummary      `json:"usage_summary"`
	}
	for _, event := range events {
		if event.EventType != "run.usage.updated" {
			continue
		}
		var payload struct {
			TurnUsage    pebblestore.SessionTurnUsageSnapshot `json:"turn_usage"`
			UsageSummary pebblestore.SessionUsageSummary      `json:"usage_summary"`
		}
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatalf("decode usage payload: %v", err)
		}
		payloads = append(payloads, payload)
	}
	if len(payloads) != 2 {
		t.Fatalf("usage event count = %d, want 2 events=%+v", len(payloads), events)
	}
	if payloads[0].TurnUsage.Steps != 1 || payloads[0].TurnUsage.TotalTokens != 100 || payloads[0].UsageSummary.TotalTokens != 100 {
		t.Fatalf("first usage payload = %+v", payloads[0])
	}
	if payloads[1].TurnUsage.Steps != 2 || payloads[1].TurnUsage.TotalTokens != 205 || payloads[1].UsageSummary.TotalTokens != 205 {
		t.Fatalf("second usage payload = %+v", payloads[1])
	}
}

func TestSessionsV3CompactEndpointRunsManualCompactAndResetsUsage(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	const userContent = "v3 compact must hydrate this primary transcript"
	runner := &sessionsV3RecordingProviderRunner{handler: func(ctx context.Context, req provideriface.Request, onEvent func(provideriface.StreamEvent)) (provideriface.Response, error) {
		if !sessionsV3ProviderInputContainsContentText(req.Input, userContent) {
			t.Fatalf("memory compact request did not include v3 transcript content: %+v", req.Input)
		}
		if !strings.Contains(req.Instructions, "memory compact agent") {
			t.Fatalf("compact did not use memory agent instructions: %q", req.Instructions)
		}
		return provideriface.Response{Text: "compact summary", Model: "test-model", StopReason: "stop", Usage: provideriface.TokenUsage{InputTokens: 5, OutputTokens: 2, TotalTokens: 7, Source: "test_usage"}}, nil
	}}
	providers := registry.New()
	providers.RegisterRunner(runner)
	server.providers = providers
	server.runner = runruntime.NewService(sessionSvc, server.model, providers, tool.NewRuntime(1), server.perm.(*permission.Service), server.agents, nil, nil)
	if _, _, _, err := server.agents.UpsertForAccount(testPrincipal().AccountScopeID, agentruntime.UpsertInput{Name: "memory", Mode: agentruntime.ModeSubagent, Provider: "test-provider", Model: "test-model", Thinking: "medium", Enabled: pebblestore.BoolPtr(true), Prompt: "Memory compact prompt", ToolContract: &pebblestore.AgentToolContract{Preset: "read_only"}}); err != nil {
		t.Fatalf("upsert memory agent: %v", err)
	}

	created := createSessionsV3PrimaryTestSessionWithPreference(t, server, "compact-create", "compact", pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"})
	now := time.Now().UnixMilli()
	message := pebblestore.MessageSnapshot{ID: "test-user-compact-v3-message", SessionID: created.ID, UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, Role: "user", Content: userContent, CreatedAt: now}
	payloadHash, err := sessionV3ExecutorPayloadHash(created.ID, "compact-fixture", sessionruntime.RunIntentRunning, "", "session.message.appended", "compact-v3-message:"+userContent)
	if err != nil {
		t.Fatalf("hash compact fixture message payload: %v", err)
	}
	if _, err := server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{SessionID: created.ID, UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, ClientRequestID: "compact-v3-message", IdempotencyKey: "compact-v3-message", PayloadHash: payloadHash, RequestHash: payloadHash, Kind: sessionruntime.SessionMutationAppendMessage, EventType: "session.message.appended", Message: &message, NowUnixMs: now}); err != nil {
		t.Fatalf("append compact fixture message: %v", err)
	}
	_, _, _, err = sessionSvc.RecordTurnUsage(created.ID, pebblestore.SessionTurnUsageSnapshot{RunID: "before-compact", Provider: "test-provider", Model: "test-model", Source: "test_usage", ContextWindow: 1000, TotalTokens: 900})
	if err != nil {
		t.Fatalf("record usage: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+created.ID+"/compact", bytes.NewBufferString(`{"client_request_id":"compact-now","note":"keep constraints"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("compact status = %d, want %d, body=%s", rec.Code, http.StatusAccepted, rec.Body.String())
	}
	var resp struct {
		RunID string `json:"run_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode compact response: %v", err)
	}
	if strings.TrimSpace(resp.RunID) == "" {
		t.Fatalf("compact response missing run id: %s", rec.Body.String())
	}
	waitForSessionsV3SpecificRunIntentStatus(t, sessionSvc, created.ID, resp.RunID, sessionruntime.RunIntentCompleted)
	usageSummary, ok, err := sessionSvc.GetUsageSummary(created.ID)
	if err != nil {
		t.Fatalf("get usage summary: %v", err)
	}
	if !ok || usageSummary.ContextWindow != 1000 || usageSummary.RemainingTokens != 1000 || usageSummary.Source != "context_compaction_reset" {
		t.Fatalf("compact usage summary = %+v", usageSummary)
	}
	if runner.callCount == 0 {
		t.Fatalf("compact did not invoke memory provider")
	}
	messages, err := sessionSvc.ListSessionMessages(created.ID, 0, 20)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	var sawManualCheckpoint bool
	for _, msg := range messages {
		if msg.Role == "system" && strings.Contains(msg.Content, "[context-compact]") {
			if !strings.Contains(msg.Content, "origin=manual") {
				t.Fatalf("compact checkpoint missing manual origin: %s", msg.Content)
			}
			sawManualCheckpoint = true
		}
	}
	if !sawManualCheckpoint {
		t.Fatalf("manual compact checkpoint missing from messages: %+v", messages)
	}
}

func TestSessionsV3ExecutorUsesAutoToolChoiceWhenToolsResolved(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	runner := &sessionsV3RecordingProviderRunner{text: "tool-enabled answer"}
	providers := registry.New()
	providers.RegisterRunner(runner)
	server.providers = providers
	runSvc := runruntime.NewService(sessionSvc, server.model, providers, tool.NewRuntime(1), server.perm.(*permission.Service), server.agents, nil, nil)
	server.runner = runSvc
	if _, _, _, err := server.agents.UpsertForAccount(testPrincipal().AccountScopeID, agentruntime.UpsertInput{Name: "swarm", Mode: agentruntime.ModePrimary, Provider: "test-provider", Model: "test-model", Thinking: "medium", RuntimeMode: pebblestore.AgentRuntimeModePlanAuto, ExitPlanModeEnabled: pebblestore.BoolPtr(true), ToolContract: &pebblestore.AgentToolContract{Tools: map[string]pebblestore.AgentToolConfig{"read": {Enabled: pebblestore.BoolPtr(true)}}}, Enabled: pebblestore.BoolPtr(true), Prompt: "Swarm prompt"}); err != nil {
		t.Fatalf("upsert tool-enabled swarm agent: %v", err)
	}
	exec := newSessionV3Executor(server)
	exec.startDelay = 0
	server.v3SessionExecutor = exec

	created := createSessionsV3PrimaryTestSessionWithPreference(t, server, "provider-tools-create", "provider tools", pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"})
	postSessionsV3PrimaryTestMessage(t, server, created.ID, "provider-tools-message", "list files")
	waitForSessionsV3MessageCount(t, sessionSvc, created.ID, 2)
	if runner.lastRequest.ToolChoice != "auto" || len(runner.lastRequest.Tools) == 0 {
		t.Fatalf("provider request tools=%+v tool_choice=%q, want auto with tools", runner.lastRequest.Tools, runner.lastRequest.ToolChoice)
	}
}

func TestSessionsV3ProviderInputRequiresStructuredToolTranscriptItems(t *testing.T) {
	toolContent, err := json.Marshal(map[string]any{
		"path_id":          "run.v3.provider-tool-result.v1",
		"type":             "v3_provider_tool_result",
		"tool_name":        "read",
		"call_id":          "call-read-facts",
		"tool_instance_id": "step-1:call-read-facts",
		"tool":             "read",
		"arguments":        `{"path":"facts.txt"}`,
		"output":           "tool-loop-file-content",
		"completed_output": "tool-loop-file-content",
		"metadata": map[string]any{
			"executor_kind":    "v3_provider_tool",
			"run_id":           "run-structured-tool-history",
			"step":             1,
			"step_id":          "step-1",
			"tool_instance_id": "step-1:call-read-facts",
		},
	})
	if err != nil {
		t.Fatalf("marshal tool history fixture: %v", err)
	}
	input := sessionsV3ProviderInput([]pebblestore.MessageSnapshot{
		{Role: "user", Content: "read facts.txt before answering"},
		{Role: "tool", Content: string(toolContent), Metadata: map[string]any{
			"executor_kind":    "v3_provider_tool",
			"run_id":           "run-structured-tool-history",
			"step":             1,
			"step_id":          "step-1",
			"tool_instance_id": "step-1:call-read-facts",
		}},
	})

	hasCall := sessionsV3ProviderInputHasTopLevelType(input, "function_call")
	hasOutput := sessionsV3ProviderInputHasTopLevelType(input, "function_call_output")
	hasStringifiedToolResult := sessionsV3ProviderInputContainsContentText(input, "[tool result]")
	if !hasCall || !hasOutput || hasStringifiedToolResult {
		t.Fatalf("provider input = %+v, want structured function_call/function_call_output items and no stringified [tool result] user text (hasCall=%t hasOutput=%t hasStringifiedToolResult=%t)", input, hasCall, hasOutput, hasStringifiedToolResult)
	}
}

func TestSessionsV3ExecutorExecutesProviderToolCallsAndContinuesToFinalAnswer(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "facts.txt"), []byte("tool-loop-file-content"), 0o644); err != nil {
		t.Fatalf("write workspace fact file: %v", err)
	}
	runner := &sessionsV3RecordingProviderRunner{responses: []provideriface.Response{
		{FunctionCalls: []provideriface.FunctionCall{{CallID: "call-read-facts", Name: "read", Arguments: `{"path":"facts.txt"}`}}},
		{Text: "final answer after durable tool result"},
	}}
	providers := registry.New()
	providers.RegisterRunner(runner)
	server.providers = providers
	runSvc := runruntime.NewService(sessionSvc, server.model, providers, tool.NewRuntime(1), server.perm.(*permission.Service), server.agents, nil, nil)
	server.runner = runSvc
	server.SetBypassPermissions(true)
	if _, _, _, err := server.agents.UpsertForAccount(testPrincipal().AccountScopeID, agentruntime.UpsertInput{Name: "swarm", Mode: agentruntime.ModePrimary, Provider: "test-provider", Model: "test-model", Thinking: "medium", RuntimeMode: pebblestore.AgentRuntimeModePlanAuto, ExitPlanModeEnabled: pebblestore.BoolPtr(true), ToolContract: &pebblestore.AgentToolContract{Tools: map[string]pebblestore.AgentToolConfig{"read": {Enabled: pebblestore.BoolPtr(true)}}}, Enabled: pebblestore.BoolPtr(true), Prompt: "Swarm prompt"}); err != nil {
		t.Fatalf("upsert tool-enabled swarm agent: %v", err)
	}
	exec := newSessionV3Executor(server)
	exec.startDelay = 0
	server.v3SessionExecutor = exec

	created := createSessionsV3PrimaryTestSessionWithWorkspaceAndPreference(t, server, "provider-tool-loop-create", "provider tool loop", workspace, pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"})
	postSessionsV3PrimaryTestMessage(t, server, created.ID, "provider-tool-loop-message", "read facts.txt before answering")
	intent := waitForSessionsV3RunIntentStatus(t, sessionSvc, created.ID, sessionruntime.RunIntentCompleted)
	if intent.BlockedReason != "" {
		t.Fatalf("completed run has blocked reason: %+v", intent)
	}
	messages, err := sessionSvc.ListSessionMessages(created.ID, 0, 10)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 3 || messages[0].Role != "user" || messages[1].Role != "tool" || messages[2].Role != "assistant" {
		t.Fatalf("messages after tool loop = %+v, want user/tool/assistant", messages)
	}
	if !strings.Contains(messages[1].Content, "tool-loop-file-content") {
		t.Fatalf("tool message content = %q, want durable tool output", messages[1].Content)
	}
	toolRecord, ok := sessionsV3DecodeProviderToolResultRecord(messages[1].Content)
	if !ok || toolRecord.PathID != "run.v3.provider-tool-result.v1" || toolRecord.ToolName != "read" || toolRecord.CallID != "call-read-facts" || toolRecord.ToolInstanceID != "step-1:call-read-facts" {
		t.Fatalf("tool message content did not persist V3-native provider tool result record: ok=%t record=%+v content=%q", ok, toolRecord, messages[1].Content)
	}
	if messages[2].Content != "final answer after durable tool result" {
		t.Fatalf("assistant final content = %q", messages[2].Content)
	}
	if runner.callCount != 2 {
		t.Fatalf("provider call count = %d, want initial tool request plus continuation", runner.callCount)
	}
	if len(runner.requests) != 2 || len(runner.requests[1].Input) < 2 {
		t.Fatalf("continuation request input = %+v", runner.requests)
	}
	continuationInput := runner.requests[1].Input
	if !sessionsV3ProviderInputHasTopLevelType(continuationInput, "function_call") || !sessionsV3ProviderInputHasTopLevelType(continuationInput, "function_call_output") || sessionsV3ProviderInputContainsContentText(continuationInput, "[tool result]") {
		t.Fatalf("continuation provider input = %+v, want direct structured function_call/function_call_output reinjection instead of DB-reparsed user text", continuationInput)
	}
}

func TestSessionsV3ExecutorCarriesFullContinuationHistoryAcrossMultipleToolSteps(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "one.txt"), []byte("first durable tool result"), 0o644); err != nil {
		t.Fatalf("write first file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "two.txt"), []byte("second durable tool result"), 0o644); err != nil {
		t.Fatalf("write second file: %v", err)
	}
	runner := &sessionsV3RecordingProviderRunner{responses: []provideriface.Response{
		{FunctionCalls: []provideriface.FunctionCall{{CallID: "call-reused", Name: "read", Arguments: `{"path":"one.txt"}`}}},
		{FunctionCalls: []provideriface.FunctionCall{{CallID: "call-reused", Name: "read", Arguments: `{"path":"two.txt"}`}}},
		{Text: "final answer with both tool results"},
	}}
	providers := registry.New()
	providers.RegisterRunner(runner)
	server.providers = providers
	runSvc := runruntime.NewService(sessionSvc, server.model, providers, tool.NewRuntime(1), server.perm.(*permission.Service), server.agents, nil, nil)
	server.runner = runSvc
	server.SetBypassPermissions(true)
	if _, _, _, err := server.agents.UpsertForAccount(testPrincipal().AccountScopeID, agentruntime.UpsertInput{Name: "swarm", Mode: agentruntime.ModePrimary, Provider: "test-provider", Model: "test-model", Thinking: "medium", RuntimeMode: pebblestore.AgentRuntimeModePlanAuto, ExitPlanModeEnabled: pebblestore.BoolPtr(true), ToolContract: &pebblestore.AgentToolContract{Tools: map[string]pebblestore.AgentToolConfig{"read": {Enabled: pebblestore.BoolPtr(true)}}}, Enabled: pebblestore.BoolPtr(true), Prompt: "Swarm prompt"}); err != nil {
		t.Fatalf("upsert tool-enabled swarm agent: %v", err)
	}
	exec := newSessionV3Executor(server)
	exec.startDelay = 0
	server.v3SessionExecutor = exec

	created := createSessionsV3PrimaryTestSessionWithWorkspaceAndPreference(t, server, "provider-multi-tool-create", "provider multi tool", workspace, pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"})
	postSessionsV3PrimaryTestMessage(t, server, created.ID, "provider-multi-tool-message", "read both files before answering")
	waitForSessionsV3RunIntentStatus(t, sessionSvc, created.ID, sessionruntime.RunIntentCompleted)
	messages, err := sessionSvc.ListSessionMessages(created.ID, 0, 10)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 4 || messages[0].Role != "user" || messages[1].Role != "tool" || messages[2].Role != "tool" || messages[3].Role != "assistant" {
		t.Fatalf("messages after multi-step tool loop = %+v, want user/tool/tool/assistant", messages)
	}
	if !strings.Contains(messages[1].Content, "first durable tool result") || !strings.Contains(messages[2].Content, "second durable tool result") {
		t.Fatalf("tool messages did not persist distinct step results: %+v", messages)
	}
	if messages[1].ID == messages[2].ID {
		t.Fatalf("tool messages reused id across steps: %+v", messages)
	}
	if runner.callCount != 3 {
		t.Fatalf("provider call count = %d, want two tool steps plus final", runner.callCount)
	}
	if len(runner.requests) != 3 || len(runner.requests[2].Input) != 5 {
		t.Fatalf("final continuation input = %+v, want full user plus two structured function_call/function_call_output pairs", runner.requests)
	}
	finalInput := runner.requests[2].Input
	if !sessionsV3ProviderInputHasTopLevelType(finalInput, "function_call") || !sessionsV3ProviderInputHasTopLevelType(finalInput, "function_call_output") || sessionsV3ProviderInputContainsContentText(finalInput, "[tool result]") {
		t.Fatalf("final continuation input = %+v, want structured tool reinjection without stringified tool result text", finalInput)
	}
}

func TestSessionsV3ExecutorPersistsInterleavedAssistantSegmentsBeforeTools(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	workspace := t.TempDir()
	for name, content := range map[string]string{
		"one.txt":   "first tool result",
		"two.txt":   "second tool result",
		"three.txt": "third tool result",
	} {
		if err := os.WriteFile(filepath.Join(workspace, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	runner := &sessionsV3RecordingProviderRunner{}
	runner.handler = func(_ context.Context, _ provideriface.Request, onEvent func(provideriface.StreamEvent)) (provideriface.Response, error) {
		switch runner.callCount {
		case 1:
			if onEvent != nil {
				onEvent(provideriface.StreamEvent{Type: provideriface.StreamEventOutputTextDelta, Delta: "SEGMENT A\n\n"})
			}
			return provideriface.Response{FunctionCalls: []provideriface.FunctionCall{{CallID: "call-read-one", Name: "read", Arguments: `{"path":"one.txt"}`}}}, nil
		case 2:
			if onEvent != nil {
				onEvent(provideriface.StreamEvent{Type: provideriface.StreamEventOutputTextDelta, Delta: "SEGMENT B\n\n"})
			}
			return provideriface.Response{FunctionCalls: []provideriface.FunctionCall{{CallID: "call-read-two", Name: "read", Arguments: `{"path":"two.txt"}`}}}, nil
		case 3:
			if onEvent != nil {
				onEvent(provideriface.StreamEvent{Type: provideriface.StreamEventOutputTextDelta, Delta: "SEGMENT C\n\n"})
			}
			return provideriface.Response{FunctionCalls: []provideriface.FunctionCall{{CallID: "call-read-three", Name: "read", Arguments: `{"path":"three.txt"}`}}}, nil
		case 4:
			if onEvent != nil {
				onEvent(provideriface.StreamEvent{Type: provideriface.StreamEventOutputTextDelta, Delta: "FINAL ONLY"})
			}
			return provideriface.Response{Text: "FINAL ONLY"}, nil
		default:
			return provideriface.Response{}, fmt.Errorf("unexpected provider call %d", runner.callCount)
		}
	}
	providers := registry.New()
	providers.RegisterRunner(runner)
	server.providers = providers
	runSvc := runruntime.NewService(sessionSvc, server.model, providers, tool.NewRuntime(1), server.perm.(*permission.Service), server.agents, nil, nil)
	server.runner = runSvc
	server.SetBypassPermissions(true)
	if _, _, _, err := server.agents.UpsertForAccount(testPrincipal().AccountScopeID, agentruntime.UpsertInput{Name: "swarm", Mode: agentruntime.ModePrimary, Provider: "test-provider", Model: "test-model", Thinking: "medium", RuntimeMode: pebblestore.AgentRuntimeModePlanAuto, ExitPlanModeEnabled: pebblestore.BoolPtr(true), ToolContract: &pebblestore.AgentToolContract{Tools: map[string]pebblestore.AgentToolConfig{"read": {Enabled: pebblestore.BoolPtr(true)}}}, Enabled: pebblestore.BoolPtr(true), Prompt: "Swarm prompt"}); err != nil {
		t.Fatalf("upsert tool-enabled swarm agent: %v", err)
	}
	exec := newSessionV3Executor(server)
	exec.startDelay = 0
	server.v3SessionExecutor = exec

	created := createSessionsV3PrimaryTestSessionWithWorkspaceAndPreference(t, server, "provider-interleaved-segment-create", "provider interleaved segments", workspace, pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"})
	postSessionsV3PrimaryTestMessage(t, server, created.ID, "provider-interleaved-segment-message", "interleave assistant text before each list tool call")
	intent := waitForSessionsV3RunIntentStatus(t, sessionSvc, created.ID, sessionruntime.RunIntentCompleted)
	if intent.BlockedReason != "" {
		t.Fatalf("completed run has blocked reason: %+v", intent)
	}

	messages, err := sessionSvc.ListSessionMessages(created.ID, 0, 20)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	wantRoles := []string{"user", "assistant", "tool", "assistant", "tool", "assistant", "tool", "assistant"}
	wantAssistant := map[int]string{1: "SEGMENT A", 3: "SEGMENT B", 5: "SEGMENT C", 7: "FINAL ONLY"}
	if len(messages) != len(wantRoles) {
		t.Fatalf("messages after interleaved tool loop = %d %+v, want %d", len(messages), messages, len(wantRoles))
	}
	for i, wantRole := range wantRoles {
		if messages[i].Role != wantRole {
			t.Fatalf("message[%d] role = %q, want %q; messages=%+v", i, messages[i].Role, wantRole, messages)
		}
		if want, ok := wantAssistant[i]; ok && strings.TrimSpace(messages[i].Content) != want {
			t.Fatalf("message[%d] assistant content = %q, want %q", i, messages[i].Content, want)
		}
	}
	if runner.callCount != 4 {
		t.Fatalf("provider call count = %d, want three tool steps plus final", runner.callCount)
	}
	if len(runner.requests) != 4 || !sessionsV3ProviderInputContainsContentText(runner.requests[1].Input, "SEGMENT A") || !sessionsV3ProviderInputContainsContentText(runner.requests[2].Input, "SEGMENT B") || !sessionsV3ProviderInputContainsContentText(runner.requests[3].Input, "SEGMENT C") {
		t.Fatalf("continuation inputs did not include durable assistant segment history: %+v", runner.requests)
	}
}

func TestSessionsV3ExecutorCompletesAfterPreToolDeltaAndThreeToolCalls(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	workspace := t.TempDir()
	for name, content := range map[string]string{
		"one.txt":   "first tool result",
		"two.txt":   "second tool result",
		"three.txt": "third tool result",
	} {
		if err := os.WriteFile(filepath.Join(workspace, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	runner := &sessionsV3RecordingProviderRunner{}
	runner.handler = func(_ context.Context, _ provideriface.Request, onEvent func(provideriface.StreamEvent)) (provideriface.Response, error) {
		switch runner.callCount {
		case 1:
			if onEvent != nil {
				onEvent(provideriface.StreamEvent{Type: provideriface.StreamEventOutputTextDelta, Delta: "First message"})
			}
			return provideriface.Response{FunctionCalls: []provideriface.FunctionCall{{CallID: "call-read-one", Name: "read", Arguments: `{"path":"one.txt"}`}}}, nil
		case 2:
			return provideriface.Response{FunctionCalls: []provideriface.FunctionCall{{CallID: "call-read-two", Name: "read", Arguments: `{"path":"two.txt"}`}}}, nil
		case 3:
			return provideriface.Response{FunctionCalls: []provideriface.FunctionCall{{CallID: "call-read-three", Name: "read", Arguments: `{"path":"three.txt"}`}}}, nil
		case 4:
			if onEvent != nil {
				onEvent(provideriface.StreamEvent{Type: provideriface.StreamEventOutputTextDelta, Delta: "I've finished testing"})
			}
			return provideriface.Response{Text: "I've finished testing"}, nil
		default:
			return provideriface.Response{}, fmt.Errorf("unexpected provider call %d", runner.callCount)
		}
	}
	providers := registry.New()
	providers.RegisterRunner(runner)
	server.providers = providers
	runSvc := runruntime.NewService(sessionSvc, server.model, providers, tool.NewRuntime(1), server.perm.(*permission.Service), server.agents, nil, nil)
	server.runner = runSvc
	server.SetBypassPermissions(true)
	if _, _, _, err := server.agents.UpsertForAccount(testPrincipal().AccountScopeID, agentruntime.UpsertInput{Name: "swarm", Mode: agentruntime.ModePrimary, Provider: "test-provider", Model: "test-model", Thinking: "medium", RuntimeMode: pebblestore.AgentRuntimeModePlanAuto, ExitPlanModeEnabled: pebblestore.BoolPtr(true), ToolContract: &pebblestore.AgentToolContract{Tools: map[string]pebblestore.AgentToolConfig{"read": {Enabled: pebblestore.BoolPtr(true)}}}, Enabled: pebblestore.BoolPtr(true), Prompt: "Swarm prompt"}); err != nil {
		t.Fatalf("upsert tool-enabled swarm agent: %v", err)
	}
	exec := newSessionV3Executor(server)
	exec.startDelay = 0
	server.v3SessionExecutor = exec

	created := createSessionsV3PrimaryTestSessionWithWorkspaceAndPreference(t, server, "provider-pretool-delta-create", "provider pretool delta", workspace, pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"})
	postSessionsV3PrimaryTestMessage(t, server, created.ID, "provider-pretool-delta-message", `test. start off first message, "First message" do 3 tool calls in a row, then state "I've finished testing"`)
	intent := waitForSessionsV3RunIntentStatus(t, sessionSvc, created.ID, sessionruntime.RunIntentCompleted)
	if intent.BlockedReason != "" {
		t.Fatalf("completed run has blocked reason: %+v", intent)
	}

	messages, err := sessionSvc.ListSessionMessages(created.ID, 0, 10)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 6 || messages[0].Role != "user" || messages[1].Role != "assistant" || messages[2].Role != "tool" || messages[3].Role != "tool" || messages[4].Role != "tool" || messages[5].Role != "assistant" {
		t.Fatalf("messages after pre-tool delta and three tools = %+v, want user/assistant/tool/tool/tool/assistant", messages)
	}
	if strings.TrimSpace(messages[1].Content) != "First message" {
		t.Fatalf("pre-tool assistant content = %q", messages[1].Content)
	}
	if messages[5].Content != "I've finished testing" {
		t.Fatalf("final assistant content = %q", messages[5].Content)
	}

	events, err := sessionSvc.ListSessionEvents(created.ID, 0, 80)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	var deltaText []string
	for _, event := range events {
		if event.EventType == "session.run.failed" {
			t.Fatalf("unexpected run failure event after final assistant text: %+v", event)
		}
		if event.EventType != "session.assistant.delta" {
			continue
		}
		var payload struct {
			DeltaIndex int    `json:"delta_index"`
			Delta      string `json:"delta"`
		}
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatalf("decode delta payload: %v", err)
		}
		deltaText = append(deltaText, payload.Delta)
	}
	if len(deltaText) != 2 || strings.TrimSpace(deltaText[0]) != "First message" || strings.TrimSpace(deltaText[1]) != "I've finished testing" {
		t.Fatalf("assistant deltas = %+v, want pre-tool and final streaming deltas", deltaText)
	}
}

func TestSessionsV3ExecutorCompletesAfterMoreThanEightVariedToolCalls(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	workspace := t.TempDir()
	responses := make([]provideriface.Response, 0, 10)
	for i := 0; i < 9; i++ {
		name := fmt.Sprintf("file-%02d.txt", i)
		if err := os.WriteFile(filepath.Join(workspace, name), []byte(fmt.Sprintf("tool result %02d", i)), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		responses = append(responses, provideriface.Response{FunctionCalls: []provideriface.FunctionCall{{CallID: fmt.Sprintf("call-read-%02d", i), Name: "read", Arguments: fmt.Sprintf(`{"path":"%s"}`, name)}}})
	}
	responses = append(responses, provideriface.Response{Text: "final answer after nine tool calls"})
	runner := &sessionsV3RecordingProviderRunner{responses: responses}
	providers := registry.New()
	providers.RegisterRunner(runner)
	server.providers = providers
	runSvc := runruntime.NewService(sessionSvc, server.model, providers, tool.NewRuntime(1), server.perm.(*permission.Service), server.agents, nil, nil)
	server.runner = runSvc
	server.SetBypassPermissions(true)
	if _, _, _, err := server.agents.UpsertForAccount(testPrincipal().AccountScopeID, agentruntime.UpsertInput{Name: "swarm", Mode: agentruntime.ModePrimary, Provider: "test-provider", Model: "test-model", Thinking: "medium", RuntimeMode: pebblestore.AgentRuntimeModePlanAuto, ExitPlanModeEnabled: pebblestore.BoolPtr(true), ToolContract: &pebblestore.AgentToolContract{Tools: map[string]pebblestore.AgentToolConfig{"read": {Enabled: pebblestore.BoolPtr(true)}}}, Enabled: pebblestore.BoolPtr(true), Prompt: "Swarm prompt"}); err != nil {
		t.Fatalf("upsert tool-enabled swarm agent: %v", err)
	}
	exec := newSessionV3Executor(server)
	exec.startDelay = 0
	server.v3SessionExecutor = exec

	created := createSessionsV3PrimaryTestSessionWithWorkspaceAndPreference(t, server, "provider-varied-tools-create", "provider varied tools", workspace, pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"})
	postSessionsV3PrimaryTestMessage(t, server, created.ID, "provider-varied-tools-message", "read many different files")
	intent := waitForSessionsV3RunIntentStatus(t, sessionSvc, created.ID, sessionruntime.RunIntentCompleted)
	if intent.BlockedReason != "" {
		t.Fatalf("completed intent has blocked reason: %+v", intent)
	}
	messages, err := sessionSvc.ListSessionMessages(created.ID, 0, 20)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 11 || messages[10].Role != "assistant" || messages[10].Content != "final answer after nine tool calls" {
		t.Fatalf("messages after varied tool calls = %+v", messages)
	}
	if runner.callCount != 10 {
		t.Fatalf("provider call count = %d, want nine tool calls plus final", runner.callCount)
	}
}

func TestSessionsV3ExecutorPersistsFailureWhenToolCallRepeatsFiveConsecutiveTimes(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "loop.txt"), []byte("loop tool result"), 0o644); err != nil {
		t.Fatalf("write loop file: %v", err)
	}
	responses := make([]provideriface.Response, 0, sessionV3ProviderIdenticalToolCallLimit)
	for i := 0; i < sessionV3ProviderIdenticalToolCallLimit; i++ {
		responses = append(responses, provideriface.Response{FunctionCalls: []provideriface.FunctionCall{{CallID: fmt.Sprintf("call-reused-%d", i), Name: "read", Arguments: `{"path":"loop.txt"}`}}})
	}
	runner := &sessionsV3RecordingProviderRunner{responses: responses}
	providers := registry.New()
	providers.RegisterRunner(runner)
	server.providers = providers
	runSvc := runruntime.NewService(sessionSvc, server.model, providers, tool.NewRuntime(1), server.perm.(*permission.Service), server.agents, nil, nil)
	server.runner = runSvc
	server.SetBypassPermissions(true)
	if _, _, _, err := server.agents.UpsertForAccount(testPrincipal().AccountScopeID, agentruntime.UpsertInput{Name: "swarm", Mode: agentruntime.ModePrimary, Provider: "test-provider", Model: "test-model", Thinking: "medium", RuntimeMode: pebblestore.AgentRuntimeModePlanAuto, ExitPlanModeEnabled: pebblestore.BoolPtr(true), ToolContract: &pebblestore.AgentToolContract{Tools: map[string]pebblestore.AgentToolConfig{"read": {Enabled: pebblestore.BoolPtr(true)}}}, Enabled: pebblestore.BoolPtr(true), Prompt: "Swarm prompt"}); err != nil {
		t.Fatalf("upsert tool-enabled swarm agent: %v", err)
	}
	exec := newSessionV3Executor(server)
	exec.startDelay = 0
	server.v3SessionExecutor = exec

	created := createSessionsV3PrimaryTestSessionWithWorkspaceAndPreference(t, server, "provider-tool-bound-create", "provider tool bound", workspace, pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"})
	postSessionsV3PrimaryTestMessage(t, server, created.ID, "provider-tool-bound-message", "keep reading forever")
	intent := waitForSessionsV3RunIntentStatus(t, sessionSvc, created.ID, sessionruntime.RunIntentFailed)
	if !strings.Contains(intent.BlockedReason, fmt.Sprintf("repeated identical tool call %d times", sessionV3ProviderIdenticalToolCallLimit)) || !strings.Contains(intent.BlockedReason, `read:{"path":"loop.txt"}`) {
		t.Fatalf("failed intent = %+v", intent)
	}
	messages, err := sessionSvc.ListSessionMessages(created.ID, 0, 20)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != sessionV3ProviderIdenticalToolCallLimit {
		t.Fatalf("messages after repeated-call failure = %d %+v, want user plus four tool results before the fifth repeated call is blocked", len(messages), messages)
	}
	for i, message := range messages[1:] {
		if message.Role != "tool" || !strings.Contains(message.Content, "loop tool result") {
			t.Fatalf("tool message %d = %+v", i, message)
		}
	}
	events, err := sessionSvc.ListSessionEvents(created.ID, 0, 200)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	var failure *sessionruntime.SessionEvent
	for _, event := range events {
		if event.EventType == "session.run.failed" {
			copy := event
			failure = &copy
		}
	}
	if failure == nil {
		t.Fatalf("missing durable run failure event after bounded loop: %+v", events)
	}
}

func TestSessionsV3ExecutorExitPlanModeUsesV3MutationAndRefreshesContinuationRuntime(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	workspace := t.TempDir()
	runner := &sessionsV3RecordingProviderRunner{}
	runner.handler = func(_ context.Context, req provideriface.Request, _ func(provideriface.StreamEvent)) (provideriface.Response, error) {
		switch runner.callCount {
		case 1:
			if req.ToolInvoker == nil {
				return provideriface.Response{}, fmt.Errorf("missing provider-managed tool invoker")
			}
			if !strings.Contains(req.Instructions, "Current session mode: plan.") {
				return provideriface.Response{}, fmt.Errorf("initial instructions did not use plan mode:\n%s", req.Instructions)
			}
			document := map[string]any{"info": map[string]any{"goal": "continue in auto"}, "checkpoints": []map[string]any{{"id": "cp-1", "title": "continue", "status": "pending"}}}
			args := mustSessionsV3TestJSON(t, map[string]any{"title": "Plan: continue", "document": document})
			result, err := req.ToolInvoker.ExecuteTool(context.Background(), provideriface.ToolInvocation{CallID: "call-exit-plan", Name: "exit_plan_mode", Arguments: args})
			if err != nil {
				return provideriface.Response{}, err
			}
			if !result.RestartTurn {
				return provideriface.Response{}, fmt.Errorf("exit_plan_mode result did not request turn restart: %+v", result)
			}
			return provideriface.Response{RestartTurn: true}, nil
		case 2:
			if !strings.Contains(req.Instructions, "Current session mode: auto.") {
				return provideriface.Response{}, fmt.Errorf("continuation instructions did not refresh to auto mode:\n%s", req.Instructions)
			}
			if len(req.Input) != 3 || !sessionsV3ProviderInputHasTopLevelType(req.Input, "function_call") || !sessionsV3ProviderInputHasTopLevelType(req.Input, "function_call_output") {
				return provideriface.Response{}, fmt.Errorf("exit_plan_mode continuation input = %+v, want user plus structured tool call/output", req.Input)
			}
			if req.ToolInvoker == nil {
				return provideriface.Response{}, fmt.Errorf("missing refreshed provider-managed tool invoker")
			}
			writeArgs := mustSessionsV3TestJSON(t, map[string]any{"path": "after-exit.txt", "content": "auto write allowed"})
			writeResult, err := req.ToolInvoker.ExecuteTool(context.Background(), provideriface.ToolInvocation{CallID: "call-write-after-exit", Name: "write", Arguments: writeArgs})
			if err != nil {
				return provideriface.Response{}, err
			}
			if writeResult.Error != "" {
				return provideriface.Response{}, fmt.Errorf("write after exit_plan_mode was not authorized with refreshed auto mode: %+v", writeResult)
			}
			return provideriface.Response{Text: "continued in auto"}, nil
		default:
			return provideriface.Response{}, fmt.Errorf("unexpected provider call %d", runner.callCount)
		}
	}
	providers := registry.New()
	providers.RegisterRunner(runner)
	server.providers = providers
	runSvc := runruntime.NewService(sessionSvc, server.model, providers, tool.NewRuntime(1), server.perm.(*permission.Service), server.agents, nil, nil)
	server.runner = runSvc
	server.SetBypassPermissions(true)
	if _, _, _, err := server.agents.UpsertForAccount(testPrincipal().AccountScopeID, agentruntime.UpsertInput{Name: "swarm", Mode: agentruntime.ModePrimary, Provider: "test-provider", Model: "test-model", Thinking: "medium", RuntimeMode: pebblestore.AgentRuntimeModePlanAuto, ExitPlanModeEnabled: pebblestore.BoolPtr(true), ToolContract: &pebblestore.AgentToolContract{Tools: map[string]pebblestore.AgentToolConfig{"exit_plan_mode": {Enabled: pebblestore.BoolPtr(true)}, "write": {Enabled: pebblestore.BoolPtr(true)}}}, Enabled: pebblestore.BoolPtr(true), Prompt: "Swarm prompt"}); err != nil {
		t.Fatalf("upsert exit-plan swarm agent: %v", err)
	}
	exec := newSessionV3Executor(server)
	exec.startDelay = 0
	server.v3SessionExecutor = exec

	created := createSessionsV3PrimaryTestSessionWithWorkspaceAndPreference(t, server, "provider-exit-plan-restart-create", "provider exit plan restart", workspace, pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"})
	if created.Mode != sessionruntime.ModePlan {
		t.Fatalf("created session mode = %q, want plan", created.Mode)
	}
	postSessionsV3PrimaryTestMessage(t, server, created.ID, "provider-exit-plan-restart-message", "exit plan mode and continue")
	waitForSessionsV3RunIntentStatus(t, sessionSvc, created.ID, sessionruntime.RunIntentCompleted)

	stored, ok, err := sessionSvc.GetSession(created.ID)
	if err != nil || !ok {
		t.Fatalf("get session after exit plan: ok=%t err=%v", ok, err)
	}
	if stored.Mode != sessionruntime.ModeAuto {
		t.Fatalf("session mode after exit_plan_mode = %q, want auto", stored.Mode)
	}
	messages, err := sessionSvc.ListSessionMessages(created.ID, 0, 10)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 4 || messages[0].Role != "user" || messages[1].Role != "tool" || messages[2].Role != "tool" || messages[3].Role != "assistant" || messages[3].Content != "continued in auto" {
		t.Fatalf("messages after exit plan restart = %+v", messages)
	}
	if !strings.Contains(messages[2].Content, "auto write allowed") {
		t.Fatalf("post-exit write tool message = %+v", messages[2])
	}
	events, err := sessionSvc.ListSessionEvents(created.ID, 0, 40)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	seenModeEvent := false
	for _, event := range events {
		if event.EventType == "session.mode.updated" {
			seenModeEvent = true
		}
	}
	if !seenModeEvent {
		t.Fatalf("missing canonical session.mode.updated event after exit_plan_mode: %+v", events)
	}
	if runner.callCount != 2 {
		t.Fatalf("provider call count = %d, want exit plan plus refreshed continuation", runner.callCount)
	}
}

func TestSessionsV3ExecutorContinuesAfterProviderManagedRestartTurn(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "restart.txt"), []byte("restart-turn tool result"), 0o644); err != nil {
		t.Fatalf("write restart file: %v", err)
	}
	runner := &sessionsV3RecordingProviderRunner{}
	runner.handler = func(_ context.Context, req provideriface.Request, _ func(provideriface.StreamEvent)) (provideriface.Response, error) {
		if runner.callCount == 1 {
			if req.ToolInvoker == nil {
				return provideriface.Response{}, fmt.Errorf("missing provider-managed tool invoker")
			}
			if _, err := req.ToolInvoker.ExecuteTool(context.Background(), provideriface.ToolInvocation{CallID: "provider-managed-read", Name: "read", Arguments: `{"path":"restart.txt"}`}); err != nil {
				return provideriface.Response{}, err
			}
			return provideriface.Response{RestartTurn: true}, nil
		}
		if len(req.Input) != 3 || !sessionsV3ProviderInputHasTopLevelType(req.Input, "function_call") || !sessionsV3ProviderInputHasTopLevelType(req.Input, "function_call_output") || sessionsV3ProviderInputContainsContentText(req.Input, "[tool result]") {
			return provideriface.Response{}, fmt.Errorf("restart continuation input = %+v, want user plus structured tool call/output", req.Input)
		}
		return provideriface.Response{Text: "final answer after restart turn"}, nil
	}
	providers := registry.New()
	providers.RegisterRunner(runner)
	server.providers = providers
	runSvc := runruntime.NewService(sessionSvc, server.model, providers, tool.NewRuntime(1), server.perm.(*permission.Service), server.agents, nil, nil)
	server.runner = runSvc
	server.SetBypassPermissions(true)
	if _, _, _, err := server.agents.UpsertForAccount(testPrincipal().AccountScopeID, agentruntime.UpsertInput{Name: "swarm", Mode: agentruntime.ModePrimary, Provider: "test-provider", Model: "test-model", Thinking: "medium", RuntimeMode: pebblestore.AgentRuntimeModePlanAuto, ExitPlanModeEnabled: pebblestore.BoolPtr(true), ToolContract: &pebblestore.AgentToolContract{Tools: map[string]pebblestore.AgentToolConfig{"read": {Enabled: pebblestore.BoolPtr(true)}}}, Enabled: pebblestore.BoolPtr(true), Prompt: "Swarm prompt"}); err != nil {
		t.Fatalf("upsert tool-enabled swarm agent: %v", err)
	}
	exec := newSessionV3Executor(server)
	exec.startDelay = 0
	server.v3SessionExecutor = exec

	created := createSessionsV3PrimaryTestSessionWithWorkspaceAndPreference(t, server, "provider-restart-turn-create", "provider restart turn", workspace, pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"})
	postSessionsV3PrimaryTestMessage(t, server, created.ID, "provider-restart-turn-message", "read then restart")
	waitForSessionsV3RunIntentStatus(t, sessionSvc, created.ID, sessionruntime.RunIntentCompleted)
	messages, err := sessionSvc.ListSessionMessages(created.ID, 0, 10)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 3 || messages[0].Role != "user" || messages[1].Role != "tool" || messages[2].Role != "assistant" {
		t.Fatalf("messages after restart turn = %+v, want user/tool/assistant", messages)
	}
	if !strings.Contains(messages[1].Content, "restart-turn tool result") || messages[2].Content != "final answer after restart turn" {
		t.Fatalf("messages after restart turn = %+v", messages)
	}
	if runner.callCount != 2 {
		t.Fatalf("provider call count = %d, want restart plus continuation", runner.callCount)
	}
}

func TestSessionsV3RuntimeInstructionsUseLegacyWorkspaceAndRuleContext(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	primary := t.TempDir()
	worktreeRoot := t.TempDir()
	linked := t.TempDir()
	if err := os.WriteFile(filepath.Join(primary, "AGENTS.md"), []byte("# Primary rule\nRoot agent rule for V3 parity."), 0o644); err != nil {
		t.Fatalf("write primary AGENTS.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(linked, "AGENTS.md"), []byte("# Linked rule\nLinked root instruction."), 0o644); err != nil {
		t.Fatalf("write linked AGENTS.md: %v", err)
	}
	server.discovery = discovery.NewService()
	exec := newSessionV3Executor(server)
	exec.startDelay = 0
	server.v3SessionExecutor = exec

	now := time.Now().UnixMilli()
	session := pebblestore.SessionSnapshot{
		ID:                      "session-instruction-workspace-redline",
		UserID:                  testPrincipal().UserID,
		AccountScopeID:          testPrincipal().AccountScopeID,
		WorkspacePath:           primary,
		WorkspaceName:           "primary",
		TemporaryWorkspaceRoots: []string{linked},
		WorktreeEnabled:         true,
		WorktreeRootPath:        worktreeRoot,
		WorktreeBranch:          "agent/instruction-parity",
		Title:                   "instruction parity",
		Mode:                    sessionruntime.ModeAuto,
		Preference:              pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"},
		Metadata: map[string]any{
			"agent_name":          "swarm",
			"resolved_agent_name": "swarm",
			"agent_mode":          "primary",
			"runtime_mode":        pebblestore.AgentRuntimeModePlanAuto,
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if _, err := server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID:       session.ID,
		UserID:          session.UserID,
		AccountScopeID:  session.AccountScopeID,
		ClientRequestID: "instruction-workspace-create",
		IdempotencyKey:  "instruction-workspace-create",
		PayloadHash:     "instruction-workspace-create-hash",
		RequestHash:     "instruction-workspace-create-hash",
		Kind:            sessionruntime.SessionMutationCreateSession,
		Session:         &session,
		NowUnixMs:       now,
	}); err != nil {
		t.Fatalf("create session mutation: %v", err)
	}
	if _, _, err := sessionSvc.GetSession(session.ID); err != nil {
		t.Fatalf("get created session: %v", err)
	}

	resolved, err := exec.resolveSessionV3Runtime(sessionV3ExecutorJob{Principal: testPrincipal(), SessionID: session.ID, RunID: "run-instruction-workspace"})
	if err != nil {
		t.Fatalf("resolve runtime: %v", err)
	}
	for _, want := range []string{
		"Master harness prompt (applies to every agent run):",
		"Workspace scope:",
		"- primary_root: " + primary,
		"- linked_root: " + worktreeRoot,
		"- linked_root: " + linked,
		"Workspace runtime policy:",
		"Allowed workspace roots:",
		"Loaded instruction sources:",
		"Root agent rule for V3 parity.",
		"Linked root instruction.",
	} {
		if !strings.Contains(resolved.Instructions, want) {
			t.Fatalf("resolved instructions missing %q:\n%s", want, resolved.Instructions)
		}
	}
}

func TestSessionsV3CompactEndpointSlicesRepeatedCompactionFromLatestCheckpoint(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	const (
		oldRaw  = "session 0 raw transcript must be dropped before compact two"
		newRaw  = "session 1 new slice must be retained before compact two"
		summary = "summary one carries original request"
	)
	runner := &sessionsV3RecordingProviderRunner{handler: func(ctx context.Context, req provideriface.Request, onEvent func(provideriface.StreamEvent)) (provideriface.Response, error) {
		switch req.SessionID {
		default:
		}
		if strings.Contains(req.Instructions, "Master harness prompt") {
			// Memory compaction has its own instructions; this assertion belongs to the main-agent test below.
			t.Fatalf("memory compact unexpectedly used main-agent instructions: %q", req.Instructions)
		}
		if !strings.Contains(req.Instructions, "memory compact agent") {
			t.Fatalf("compact did not use memory agent instructions: %q", req.Instructions)
		}
		if !strings.Contains(fmt.Sprint(req.Input), oldRaw) && !strings.Contains(fmt.Sprint(req.Input), summary) {
			t.Fatalf("compact request missing prior context/checkpoint: %+v", req.Input)
		}
		if strings.Contains(fmt.Sprint(req.Input), newRaw) {
			if strings.Contains(fmt.Sprint(req.Input), oldRaw) {
				t.Fatalf("second compact oversent pre-checkpoint transcript: %+v", req.Input)
			}
			if !strings.Contains(fmt.Sprint(req.Input), summary) {
				t.Fatalf("second compact missing previous checkpoint summary: %+v", req.Input)
			}
			return provideriface.Response{Text: "summary two", Model: "test-model", StopReason: "stop"}, nil
		}
		if !strings.Contains(fmt.Sprint(req.Input), oldRaw) {
			t.Fatalf("first compact missing original raw transcript: %+v", req.Input)
		}
		return provideriface.Response{Text: summary, Model: "test-model", StopReason: "stop"}, nil
	}}
	providers := registry.New()
	providers.RegisterRunner(runner)
	server.providers = providers
	server.runner = runruntime.NewService(sessionSvc, server.model, providers, tool.NewRuntime(1), server.perm.(*permission.Service), server.agents, server.discovery, nil)
	if _, _, _, err := server.agents.UpsertForAccount(testPrincipal().AccountScopeID, agentruntime.UpsertInput{Name: "memory", Mode: agentruntime.ModeSubagent, Provider: "test-provider", Model: "test-model", Thinking: "medium", Enabled: pebblestore.BoolPtr(true), Prompt: "Memory compact prompt", ToolContract: &pebblestore.AgentToolContract{Preset: "read_only"}}); err != nil {
		t.Fatalf("upsert memory agent: %v", err)
	}

	created := createSessionsV3PrimaryTestSessionWithPreference(t, server, "compact-slice-create", "compact slice", pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"})
	appendSessionsV3PrimaryTestUserMessage(t, server, created.ID, "compact-slice-old", oldRaw)
	postSessionsV3CompactTestRequest(t, server, created.ID, "compact-slice-one")
	appendSessionsV3PrimaryTestUserMessage(t, server, created.ID, "compact-slice-new", newRaw)
	postSessionsV3CompactTestRequest(t, server, created.ID, "compact-slice-two")
	if runner.callCount != 2 {
		t.Fatalf("compact provider call count = %d, want 2", runner.callCount)
	}
}

func TestSessionsV3ProviderInputAfterCompactUsesCheckpointAndRuntimePayload(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	primary := t.TempDir()
	if err := os.WriteFile(filepath.Join(primary, "AGENTS.md"), []byte("# Primary compact rule\nPost compact rule payload."), 0o644); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}
	server.discovery = discovery.NewService()
	runner := &sessionsV3RecordingProviderRunner{handler: func(ctx context.Context, req provideriface.Request, onEvent func(provideriface.StreamEvent)) (provideriface.Response, error) {
		inputText := fmt.Sprint(req.Input)
		for _, want := range []string{"[context-compact]", "summary one carries original request", "new user after compact"} {
			if !strings.Contains(inputText, want) {
				t.Fatalf("provider input missing %q: %+v", want, req.Input)
			}
		}
		if strings.Contains(inputText, "old raw before compact") {
			t.Fatalf("provider input retained pre-compact raw transcript: %+v", req.Input)
		}
		for _, want := range []string{"Master harness prompt (applies to every agent run):", "Workspace scope:", "Loaded instruction sources:", "Post compact rule payload."} {
			if !strings.Contains(req.Instructions, want) {
				t.Fatalf("runtime instructions missing %q:\n%s", want, req.Instructions)
			}
		}
		return provideriface.Response{Text: "post compact answer", Model: "test-model", StopReason: "stop"}, nil
	}}
	providers := registry.New()
	providers.RegisterRunner(runner)
	server.providers = providers
	server.runner = runruntime.NewService(sessionSvc, server.model, providers, tool.NewRuntime(1), server.perm.(*permission.Service), server.agents, server.discovery, nil)
	if _, _, _, err := server.agents.UpsertForAccount(testPrincipal().AccountScopeID, agentruntime.UpsertInput{Name: "swarm", Mode: agentruntime.ModePrimary, Provider: "test-provider", Model: "test-model", Thinking: "medium", RuntimeMode: pebblestore.AgentRuntimeModePlanAuto, ExitPlanModeEnabled: pebblestore.BoolPtr(true), Enabled: pebblestore.BoolPtr(true), Prompt: "Swarm prompt"}); err != nil {
		t.Fatalf("upsert swarm agent: %v", err)
	}
	exec := newSessionV3Executor(server)
	server.v3SessionExecutor = exec

	created := createSessionsV3PrimaryTestSessionWithWorkspaceAndPreference(t, server, "compact-runtime-create", "compact runtime", primary, pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"})
	appendSessionsV3PrimaryTestUserMessage(t, server, created.ID, "compact-runtime-old", "old raw before compact")
	appendSessionsV3PrimaryTestSystemMessage(t, server, created.ID, "compact-runtime-checkpoint", "[context-compact] index=2 origin=manual\n\nCompacted recap:\nsummary one carries original request")
	appendSessionsV3PrimaryTestUserMessage(t, server, created.ID, "compact-runtime-new", "new user after compact")
	runID := "compact-runtime-run"
	payloadHash, err := sessionV3ExecutorPayloadHash(created.ID, runID, sessionruntime.RunIntentPendingExecutor, "", "session.message.appended", "compact-runtime-run")
	if err != nil {
		t.Fatalf("hash pending run: %v", err)
	}
	if _, err := server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID:       created.ID,
		UserID:          testPrincipal().UserID,
		AccountScopeID:  testPrincipal().AccountScopeID,
		ClientRequestID: "compact-runtime-run",
		IdempotencyKey:  "compact-runtime-run",
		PayloadHash:     payloadHash,
		RequestHash:     payloadHash,
		Kind:            sessionruntime.SessionMutationRecordRunIntent,
		EventType:       "session.message.appended",
		RunIntent:       &pebblestore.V3SessionRunIntent{RunID: runID, Status: sessionruntime.RunIntentPendingExecutor},
		NowUnixMs:       time.Now().UnixMilli(),
	}); err != nil {
		t.Fatalf("record pending run: %v", err)
	}
	if _, err := exec.assistantResponse(context.Background(), sessionV3ExecutorJob{Principal: testPrincipal(), SessionID: created.ID, RunID: runID}); err != nil {
		t.Fatalf("assistant response: %v", err)
	}
	if runner.callCount != 1 {
		t.Fatalf("provider call count = %d, want 1", runner.callCount)
	}
}

func postSessionsV3CompactTestRequest(t *testing.T, server *Server, sessionID, clientRequestID string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+sessionID+"/compact", bytes.NewBufferString(fmt.Sprintf(`{"client_request_id":%q}`, clientRequestID)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("compact status = %d, want %d, body=%s", rec.Code, http.StatusAccepted, rec.Body.String())
	}
	var resp struct {
		RunID string `json:"run_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode compact response: %v", err)
	}
	if strings.TrimSpace(resp.RunID) == "" {
		t.Fatalf("compact response missing run id: %s", rec.Body.String())
	}
	waitForSessionsV3SpecificRunIntentStatus(t, server.sessions, sessionID, resp.RunID, sessionruntime.RunIntentCompleted)
}

func TestSessionsV3ProviderToolPersistenceUsesApplySessionMutationOnly(t *testing.T) {
	body, err := os.ReadFile("../run/provider_tool_invoker.go")
	if err != nil {
		t.Fatalf("read provider_tool_invoker.go: %v", err)
	}
	source := string(body)
	if !strings.Contains(source, "ApplySessionMutation") {
		t.Fatalf("provider tool invoker must expose an ApplySessionMutation-backed V3 persistence path")
	}
	for _, forbidden := range []string{".AppendMessage(", ".UpdateMetadata("} {
		if strings.Contains(source, forbidden) {
			t.Fatalf("provider tool invoker contains legacy durable write %q; V3 tool persistence must use ApplySessionMutation", forbidden)
		}
	}
}

func TestSessionsV3ExecutorFailsClosedWhenProviderReturnsToolCalls(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	runner := &sessionsV3RecordingProviderRunner{
		text:          "needs a tool",
		functionCalls: []provideriface.FunctionCall{{CallID: "call-1", Name: "bash", Arguments: "{}"}},
	}
	providers := registry.New()
	providers.RegisterRunner(runner)
	server.providers = providers
	exec := newSessionV3Executor(server)
	exec.startDelay = 0
	server.v3SessionExecutor = exec

	created := createSessionsV3PrimaryTestSessionWithPreference(t, server, "provider-tool-call-create", "provider tool call", pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"})
	postSessionsV3PrimaryTestMessage(t, server, created.ID, "provider-tool-call-message", "do something with a tool")
	intent := waitForSessionsV3RunIntentStatus(t, sessionSvc, created.ID, sessionruntime.RunIntentFailed)
	if !strings.Contains(intent.BlockedReason, "tool-loop execution is not supported") {
		t.Fatalf("run intent = %+v", intent)
	}
	if runner.lastRequest.ToolChoice != "none" {
		t.Fatalf("provider tool choice = %q, want none for no resolved tools", runner.lastRequest.ToolChoice)
	}
	messages, err := sessionSvc.ListSessionMessages(created.ID, 0, 10)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 1 || messages[0].Role != "user" {
		t.Fatalf("messages after unsupported tool call = %+v, want only committed user message", messages)
	}
}

func TestSessionsV3ExecutorUpdatesDefaultTitleWithMemoryAgentAfterFirstRun(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	runner := &sessionsV3RecordingProviderRunner{handler: func(ctx context.Context, req provideriface.Request, onEvent func(provideriface.StreamEvent)) (provideriface.Response, error) {
		if strings.Contains(req.Instructions, "You generate deterministic session titles") {
			return provideriface.Response{Text: "Memory Agent Session Title Flow", StopReason: "stop"}, nil
		}
		return provideriface.Response{Text: "assistant answer", StopReason: "stop"}, nil
	}}
	providers := registry.New()
	providers.RegisterRunner(runner)
	server.providers = providers
	if _, _, _, err := server.agents.UpsertForAccount(testPrincipal().AccountScopeID, agentruntime.UpsertInput{Name: "memory", Mode: agentruntime.ModeSubagent, Provider: "test-provider", Model: "title-model", Thinking: "low", Enabled: pebblestore.BoolPtr(true), Prompt: "Memory title prompt", ToolContract: &pebblestore.AgentToolContract{Preset: "read_only"}}); err != nil {
		t.Fatalf("upsert memory agent: %v", err)
	}
	exec := newSessionV3Executor(server)
	exec.startDelay = 0
	server.v3SessionExecutor = exec

	created := createSessionsV3PrimaryTestSessionWithPreference(t, server, "title-update-create", "New Session", pebblestore.ModelPreference{Provider: "test-provider", Model: "chat-model", Thinking: "medium"})
	postSessionsV3PrimaryTestMessage(t, server, created.ID, "title-update-message", "please title this first turn")
	waitForSessionsV3MessageCount(t, sessionSvc, created.ID, 2)
	waitForSessionsV3Title(t, sessionSvc, created.ID, "Memory Agent Session Title Flow")
	if !server.WaitForInFlightRuns(2 * time.Second) {
		t.Fatalf("server did not drain v3 title generation")
	}

	stored, ok, err := sessionSvc.GetSession(created.ID)
	if err != nil || !ok {
		t.Fatalf("load titled session ok=%v err=%v", ok, err)
	}
	if stored.Title != "Memory Agent Session Title Flow" {
		t.Fatalf("session title = %q", stored.Title)
	}
	if runner.callCount != 2 {
		t.Fatalf("provider call count = %d, want memory title + assistant", runner.callCount)
	}
	if !strings.Contains(runner.requests[0].Instructions, "Memory title prompt") || !strings.Contains(runner.requests[0].Instructions, "You generate deterministic session titles") {
		t.Fatalf("memory title instructions = %q", runner.requests[0].Instructions)
	}
	if runner.requests[0].Model != "title-model" || runner.requests[0].Thinking != "low" || runner.requests[0].ToolChoice != "none" {
		t.Fatalf("memory title request = %+v", runner.requests[0])
	}
	events, err := sessionSvc.ListSessionEvents(created.ID, 0, 80)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	var titleEvent *sessionruntime.SessionEvent
	for _, event := range events {
		if event.EventType == "session.title.updated" {
			copy := event
			titleEvent = &copy
		}
	}
	if titleEvent == nil {
		t.Fatalf("missing session.title.updated event: %+v", events)
	}
	var payload struct {
		SessionID string                      `json:"session_id"`
		Title     string                      `json:"title"`
		Stage     string                      `json:"stage"`
		Session   pebblestore.SessionSnapshot `json:"session"`
	}
	if err := json.Unmarshal(titleEvent.Payload, &payload); err != nil {
		t.Fatalf("decode title event payload: %v", err)
	}
	if payload.SessionID != created.ID || payload.Title != "Memory Agent Session Title Flow" || payload.Stage != "final" || payload.Session.Title != "Memory Agent Session Title Flow" {
		t.Fatalf("title event payload = %+v", payload)
	}
	replay, err := sessionSvc.ReplaySessionEvents(created.ID, 0, 80)
	if err != nil {
		t.Fatalf("replay events: %v", err)
	}
	if replay.Session == nil || replay.Session.Title != "Memory Agent Session Title Flow" {
		t.Fatalf("replayed session = %+v", replay.Session)
	}
}

func TestSessionsV3ExecutorStartsTitleBeforeAssistantCompletes(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	assistantStarted := make(chan struct{})
	allowAssistant := make(chan struct{})
	runner := &sessionsV3RecordingProviderRunner{handler: func(ctx context.Context, req provideriface.Request, onEvent func(provideriface.StreamEvent)) (provideriface.Response, error) {
		if strings.Contains(req.Instructions, "You generate deterministic session titles") {
			return provideriface.Response{Text: "Immediate First Message Async Title", StopReason: "stop"}, nil
		}
		close(assistantStarted)
		select {
		case <-allowAssistant:
			return provideriface.Response{Text: "assistant answer", StopReason: "stop"}, nil
		case <-ctx.Done():
			return provideriface.Response{}, ctx.Err()
		}
	}}
	providers := registry.New()
	providers.RegisterRunner(runner)
	server.providers = providers
	if _, _, _, err := server.agents.UpsertForAccount(testPrincipal().AccountScopeID, agentruntime.UpsertInput{Name: "memory", Mode: agentruntime.ModeSubagent, Provider: "test-provider", Model: "title-model", Thinking: "low", Enabled: pebblestore.BoolPtr(true), Prompt: "Memory title prompt", ToolContract: &pebblestore.AgentToolContract{Preset: "read_only"}}); err != nil {
		t.Fatalf("upsert memory agent: %v", err)
	}
	exec := newSessionV3Executor(server)
	exec.startDelay = 0
	server.v3SessionExecutor = exec

	created := createSessionsV3PrimaryTestSessionWithPreference(t, server, "title-immediate-create", "New Session", pebblestore.ModelPreference{Provider: "test-provider", Model: "chat-model", Thinking: "medium"})
	postSessionsV3PrimaryTestMessage(t, server, created.ID, "title-immediate-message", "title immediately before assistant completes")
	select {
	case <-assistantStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("assistant provider call did not start")
	}
	waitForSessionsV3Title(t, sessionSvc, created.ID, "Immediate First Message Async Title")
	if status := currentSessionsV3RunIntentStatus(t, sessionSvc, created.ID); status != sessionruntime.RunIntentRunning {
		t.Fatalf("run status after title = %q, want still running", status)
	}
	close(allowAssistant)
	waitForSessionsV3RunIntentStatus(t, sessionSvc, created.ID, sessionruntime.RunIntentCompleted)
	if !server.WaitForInFlightRuns(2 * time.Second) {
		t.Fatalf("server did not drain v3 run")
	}
	events, err := sessionSvc.ListSessionEvents(created.ID, 0, 80)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if !sessionsV3EventsContainType(events, "session.title.updated") {
		t.Fatalf("missing session.title.updated event: %+v", events)
	}
}

func TestSessionsV3ExecutorStartsTitleFromCommittedMutationHook(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	runner := &sessionsV3RecordingProviderRunner{handler: func(ctx context.Context, req provideriface.Request, onEvent func(provideriface.StreamEvent)) (provideriface.Response, error) {
		if strings.Contains(req.Instructions, "You generate deterministic session titles") {
			return provideriface.Response{Text: "Committed Mutation Hook Title Flow", StopReason: "stop"}, nil
		}
		return provideriface.Response{Text: "assistant answer", StopReason: "stop"}, nil
	}}
	providers := registry.New()
	providers.RegisterRunner(runner)
	server.providers = providers
	if _, _, _, err := server.agents.UpsertForAccount(testPrincipal().AccountScopeID, agentruntime.UpsertInput{Name: "memory", Mode: agentruntime.ModeSubagent, Provider: "test-provider", Model: "title-model", Thinking: "low", Enabled: pebblestore.BoolPtr(true), Prompt: "Memory title prompt", ToolContract: &pebblestore.AgentToolContract{Preset: "read_only"}}); err != nil {
		t.Fatalf("upsert memory agent: %v", err)
	}
	exec := newSessionV3Executor(server)
	exec.startDelay = 0
	server.v3SessionExecutor = exec

	created := createSessionsV3PrimaryTestSessionWithPreference(t, server, "title-hook-create", "New Session", pebblestore.ModelPreference{Provider: "test-provider", Model: "chat-model", Thinking: "medium"})
	now := time.Now().UnixMilli()
	runID := "run-title-hook"
	message := pebblestore.MessageSnapshot{ID: "msg-title-hook", Role: "user", Content: "title this hook committed v3 run", CreatedAt: now}
	intent := pebblestore.V3SessionRunIntent{RunID: runID, Status: sessionruntime.RunIntentPendingExecutor, UpdatedAt: now}
	payloadHash, err := sessionsV3MessagePayloadHash(created.ID, sessionsV3MessageRequest{ClientRequestID: "title-hook-message", Role: "user", Content: message.Content}, message, intent.Status, intent.BlockedReason)
	if err != nil {
		t.Fatalf("hash message payload: %v", err)
	}
	if _, err := server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID:       created.ID,
		UserID:          testPrincipal().UserID,
		AccountScopeID:  testPrincipal().AccountScopeID,
		ClientRequestID: "title-hook-message",
		IdempotencyKey:  "title-hook-message",
		PayloadHash:     payloadHash,
		RequestHash:     payloadHash,
		Kind:            sessionruntime.SessionMutationAppendMessage,
		EventType:       "session.message.appended",
		Message:         &message,
		RunIntent:       &intent,
		NowUnixMs:       now,
	}); err != nil {
		t.Fatalf("apply message mutation: %v", err)
	}

	waitForSessionsV3Title(t, sessionSvc, created.ID, "Committed Mutation Hook Title Flow")
	if !server.WaitForInFlightRuns(2 * time.Second) {
		t.Fatalf("server did not drain v3 title hook run")
	}
	if runner.callCount != 1 {
		t.Fatalf("provider call count = %d, want title", runner.callCount)
	}
}

func TestSessionsV3ExecutorUpdatesTUITitleAfterFirstRun(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	runner := &sessionsV3RecordingProviderRunner{handler: func(ctx context.Context, req provideriface.Request, onEvent func(provideriface.StreamEvent)) (provideriface.Response, error) {
		if strings.Contains(req.Instructions, "You generate deterministic session titles") {
			return provideriface.Response{Text: "TUI V3 Async Title Flow", StopReason: "stop"}, nil
		}
		return provideriface.Response{Text: "assistant answer", StopReason: "stop"}, nil
	}}
	providers := registry.New()
	providers.RegisterRunner(runner)
	server.providers = providers
	if _, _, _, err := server.agents.UpsertForAccount(testPrincipal().AccountScopeID, agentruntime.UpsertInput{Name: "memory", Mode: agentruntime.ModeSubagent, Provider: "test-provider", Model: "title-model", Thinking: "low", Enabled: pebblestore.BoolPtr(true), Prompt: "Memory title prompt", ToolContract: &pebblestore.AgentToolContract{Preset: "read_only"}}); err != nil {
		t.Fatalf("upsert memory agent: %v", err)
	}
	exec := newSessionV3Executor(server)
	exec.startDelay = 0
	server.v3SessionExecutor = exec

	workspace := t.TempDir()
	seedSessionsV3PrimaryAuthority(t, server, workspace)
	createBody := mustSessionsV3TestJSON(t, map[string]any{
		"client_request_id": "title-tui-create",
		"cwd_path":          workspace,
		"title":             "New Session",
		"mode":              sessionruntime.ModeAuto,
		"agent_name":        "swarm",
		"preference":        pebblestore.ModelPreference{Provider: "test-provider", Model: "chat-model", Thinking: "medium"},
	})
	createReq := httptest.NewRequest(http.MethodPost, "/v3/tui/sessions", bytes.NewBufferString(createBody))
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(createRec, withTestPrincipal(createReq))
	if createRec.Code != http.StatusOK {
		t.Fatalf("tui create status = %d, want %d, body=%s", createRec.Code, http.StatusOK, createRec.Body.String())
	}
	var created struct {
		Session pebblestore.SessionSnapshot `json:"session"`
	}
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode tui create response: %v", err)
	}
	postSessionsV3PrimaryTestMessage(t, server, created.Session.ID, "title-tui-message", "please title this tui started run")

	waitForSessionsV3Title(t, sessionSvc, created.Session.ID, "TUI V3 Async Title Flow")
	if !server.WaitForInFlightRuns(2 * time.Second) {
		t.Fatalf("server did not drain v3 tui title run")
	}
	if runner.callCount != 2 {
		t.Fatalf("provider call count = %d, want title + assistant", runner.callCount)
	}
}

func TestSessionsV3ExecutorUpdatesDefaultTitleWithSystemPreludeAfterFirstRun(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	runner := &sessionsV3RecordingProviderRunner{handler: func(ctx context.Context, req provideriface.Request, onEvent func(provideriface.StreamEvent)) (provideriface.Response, error) {
		if strings.Contains(req.Instructions, "You generate deterministic session titles") {
			return provideriface.Response{Text: "System Prelude Session Title Flow", StopReason: "stop"}, nil
		}
		return provideriface.Response{Text: "assistant answer", StopReason: "stop"}, nil
	}}
	providers := registry.New()
	providers.RegisterRunner(runner)
	server.providers = providers
	if _, _, _, err := server.agents.UpsertForAccount(testPrincipal().AccountScopeID, agentruntime.UpsertInput{Name: "memory", Mode: agentruntime.ModeSubagent, Provider: "test-provider", Model: "title-model", Thinking: "low", Enabled: pebblestore.BoolPtr(true), Prompt: "Memory title prompt", ToolContract: &pebblestore.AgentToolContract{Preset: "read_only"}}); err != nil {
		t.Fatalf("upsert memory agent: %v", err)
	}
	exec := newSessionV3Executor(server)
	exec.startDelay = 0
	server.v3SessionExecutor = exec

	created := createSessionsV3PrimaryTestSessionWithPreference(t, server, "title-system-prelude-create", "New Session", pebblestore.ModelPreference{Provider: "test-provider", Model: "chat-model", Thinking: "medium"})
	appendSessionsV3PrimaryTestSystemMessage(t, server, created.ID, "title-system-prelude-message", "Session mode changed to auto.")
	postSessionsV3PrimaryTestMessage(t, server, created.ID, "title-system-prelude-user", "please title this first turn after prelude")
	waitForSessionsV3MessageCount(t, sessionSvc, created.ID, 3)
	waitForSessionsV3Title(t, sessionSvc, created.ID, "System Prelude Session Title Flow")
	if !server.WaitForInFlightRuns(2 * time.Second) {
		t.Fatalf("server did not drain v3 title generation")
	}
	if runner.callCount != 2 {
		t.Fatalf("provider call count = %d, want assistant + memory title", runner.callCount)
	}
	events, err := sessionSvc.ListSessionEvents(created.ID, 0, 80)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if !sessionsV3EventsContainType(events, "session.title.updated") {
		t.Fatalf("missing session.title.updated event: %+v", events)
	}
}

func TestSessionsV3ExecutorUpdatesDefaultTitleAfterToolShapedFirstRun(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "facts.txt"), []byte("title tool fact"), 0o644); err != nil {
		t.Fatalf("write workspace fact file: %v", err)
	}
	var assistantCalls int
	runner := &sessionsV3RecordingProviderRunner{handler: func(ctx context.Context, req provideriface.Request, onEvent func(provideriface.StreamEvent)) (provideriface.Response, error) {
		if strings.Contains(req.Instructions, "You generate deterministic session titles") {
			return provideriface.Response{Text: "Tool Assisted Session Title Flow", StopReason: "stop"}, nil
		}
		assistantCalls++
		if assistantCalls == 1 {
			return provideriface.Response{FunctionCalls: []provideriface.FunctionCall{{CallID: "call-read-title-facts", Name: "read", Arguments: `{"path":"facts.txt"}`}}}, nil
		}
		return provideriface.Response{Text: "assistant answer after title tool", StopReason: "stop"}, nil
	}}
	providers := registry.New()
	providers.RegisterRunner(runner)
	server.providers = providers
	runSvc := runruntime.NewService(sessionSvc, server.model, providers, tool.NewRuntime(1), server.perm.(*permission.Service), server.agents, nil, nil)
	server.runner = runSvc
	server.SetBypassPermissions(true)
	if _, _, _, err := server.agents.UpsertForAccount(testPrincipal().AccountScopeID, agentruntime.UpsertInput{Name: "swarm", Mode: agentruntime.ModePrimary, Provider: "test-provider", Model: "test-model", Thinking: "medium", RuntimeMode: pebblestore.AgentRuntimeModePlanAuto, ExitPlanModeEnabled: pebblestore.BoolPtr(true), ToolContract: &pebblestore.AgentToolContract{Tools: map[string]pebblestore.AgentToolConfig{"read": {Enabled: pebblestore.BoolPtr(true)}}}, Enabled: pebblestore.BoolPtr(true), Prompt: "Swarm prompt"}); err != nil {
		t.Fatalf("upsert tool-enabled swarm agent: %v", err)
	}
	if _, _, _, err := server.agents.UpsertForAccount(testPrincipal().AccountScopeID, agentruntime.UpsertInput{Name: "memory", Mode: agentruntime.ModeSubagent, Provider: "test-provider", Model: "title-model", Thinking: "low", Enabled: pebblestore.BoolPtr(true), Prompt: "Memory title prompt", ToolContract: &pebblestore.AgentToolContract{Preset: "read_only"}}); err != nil {
		t.Fatalf("upsert memory agent: %v", err)
	}
	exec := newSessionV3Executor(server)
	exec.startDelay = 0
	server.v3SessionExecutor = exec

	created := createSessionsV3PrimaryTestSessionWithWorkspaceAndPreference(t, server, "title-tool-run-create", "New Session", workspace, pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"})
	postSessionsV3PrimaryTestMessage(t, server, created.ID, "title-tool-run-message", "read facts.txt before naming this session")
	waitForSessionsV3RunIntentStatus(t, sessionSvc, created.ID, sessionruntime.RunIntentCompleted)
	waitForSessionsV3MessageCount(t, sessionSvc, created.ID, 3)
	waitForSessionsV3Title(t, sessionSvc, created.ID, "Tool Assisted Session Title Flow")
	if !server.WaitForInFlightRuns(2 * time.Second) {
		t.Fatalf("server did not drain v3 title generation")
	}
	if runner.callCount != 3 {
		t.Fatalf("provider call count = %d, want memory title + tool request + continuation", runner.callCount)
	}
	events, err := sessionSvc.ListSessionEvents(created.ID, 0, 80)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if !sessionsV3EventsContainType(events, "session.title.updated") {
		t.Fatalf("missing session.title.updated event: %+v", events)
	}
}

func TestSessionsV3ExecutorDoesNotRetitleExplicitTitle(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	runner := &sessionsV3RecordingProviderRunner{responses: []provideriface.Response{{Text: "assistant answer"}, {Text: "Should Not Apply"}}}
	providers := registry.New()
	providers.RegisterRunner(runner)
	server.providers = providers
	exec := newSessionV3Executor(server)
	exec.startDelay = 0
	server.v3SessionExecutor = exec

	created := createSessionsV3PrimaryTestSessionWithPreference(t, server, "explicit-title-create", "Explicit Title", pebblestore.ModelPreference{Provider: "test-provider", Model: "chat-model", Thinking: "medium"})
	postSessionsV3PrimaryTestMessage(t, server, created.ID, "explicit-title-message", "keep explicit title")
	waitForSessionsV3MessageCount(t, sessionSvc, created.ID, 2)
	if !server.WaitForInFlightRuns(2 * time.Second) {
		t.Fatalf("server did not drain")
	}
	stored, ok, err := sessionSvc.GetSession(created.ID)
	if err != nil || !ok {
		t.Fatalf("load session ok=%v err=%v", ok, err)
	}
	if stored.Title != "Explicit Title" {
		t.Fatalf("session title = %q", stored.Title)
	}
	if runner.callCount != 1 {
		t.Fatalf("provider call count = %d, want only assistant", runner.callCount)
	}
}

func createSessionsV3PrimaryTestSessionWithPreference(t *testing.T, server *Server, clientRequestID, title string, pref pebblestore.ModelPreference) pebblestore.SessionSnapshot {
	t.Helper()
	return createSessionsV3PrimaryTestSessionWithWorkspaceAndPreference(t, server, clientRequestID, title, t.TempDir(), pref)
}

func mustSessionsV3TestJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal json: %v", err)
	}
	return string(raw)
}

func createSessionsV3PrimaryTestSessionWithWorkspaceAndPreference(t *testing.T, server *Server, clientRequestID, title, workspacePath string, pref pebblestore.ModelPreference) pebblestore.SessionSnapshot {
	t.Helper()
	bindingID := seedSessionsV3PrimaryAuthority(t, server, workspacePath)
	payload := map[string]any{
		"client_request_id":    clientRequestID,
		"workspace_path":       workspacePath,
		"workspace_name":       filepath.Base(workspacePath),
		"swarm_id":             "host-swarm-id",
		"workspace_binding_id": bindingID,
		"target_kind":          "host",
		"target_relationship":  "self",
		"title":                title,
		"mode":                 sessionruntime.ModeAuto,
		"agent_name":           "swarm",
		"preference":           pref,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal create payload: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v3/sessions", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("create status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var created struct {
		Session pebblestore.SessionSnapshot `json:"session"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	return created.Session
}

func sessionsV3ProviderRequestToolNames(tools []provideriface.ToolDefinition) map[string]bool {
	out := make(map[string]bool, len(tools))
	for _, tool := range tools {
		name := strings.TrimSpace(tool.Name)
		if name != "" {
			out[name] = true
		}
	}
	return out
}

type sessionsV3RecordingProviderRunner struct {
	mu            sync.Mutex
	id            string
	text          string
	deltas        []string
	err           error
	response      provideriface.Response
	responses     []provideriface.Response
	functionCalls []provideriface.FunctionCall
	handler       func(context.Context, provideriface.Request, func(provideriface.StreamEvent)) (provideriface.Response, error)
	callCount     int
	lastRequest   provideriface.Request
	requests      []provideriface.Request
}

func (r *sessionsV3RecordingProviderRunner) ID() string {
	if strings.TrimSpace(r.id) != "" {
		return strings.TrimSpace(r.id)
	}
	return "test-provider"
}
func (r *sessionsV3RecordingProviderRunner) CreateResponse(ctx context.Context, req provideriface.Request) (provideriface.Response, error) {
	return r.CreateResponseStreaming(ctx, req, nil)
}
func (r *sessionsV3RecordingProviderRunner) CreateResponseStreaming(ctx context.Context, req provideriface.Request, onEvent func(provideriface.StreamEvent)) (provideriface.Response, error) {
	r.mu.Lock()
	r.callCount++
	callCount := r.callCount
	r.lastRequest = req
	r.requests = append(r.requests, req)
	handler := r.handler
	err := r.err
	response := r.response
	if len(r.responses) >= callCount {
		response = r.responses[callCount-1]
	}
	functionCalls := append([]provideriface.FunctionCall(nil), r.functionCalls...)
	deltas := append([]string(nil), r.deltas...)
	text := r.text
	r.mu.Unlock()
	if handler != nil {
		return handler(ctx, req, onEvent)
	}
	if err != nil {
		return provideriface.Response{}, err
	}
	if response.Text == "" {
		response.Text = text
	}
	if len(functionCalls) > 0 {
		response.FunctionCalls = functionCalls
	}
	if response.StopReason == "" && strings.TrimSpace(response.Text) != "" && len(response.FunctionCalls) == 0 && !response.RestartTurn {
		response.StopReason = "stop"
	}
	if onEvent != nil {
		if len(deltas) == 0 {
			deltas = []string{response.Text}
		}
		for _, delta := range deltas {
			onEvent(provideriface.StreamEvent{Type: provideriface.StreamEventOutputTextDelta, Delta: delta})
		}
	}
	return response, nil
}

func TestSessionsV3PrimaryPermissionResolvePublishesImmediateV3Update(t *testing.T) {
	server, sessionSvc, permissionSvc, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	workspace := t.TempDir()
	created := createSessionsV3PrimaryTestSessionWithWorkspaceAndPreference(t, server, "permission-immediate-create", "permission immediate", workspace, pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"})
	runID := "run-permission-immediate"
	now := time.Now().UnixMilli()
	pendingIntent := pebblestore.V3SessionRunIntent{SessionID: created.ID, UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, RunID: runID, Status: sessionruntime.RunIntentPendingExecutor, UpdatedAt: now}
	if _, err := server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{SessionID: created.ID, UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, ClientRequestID: "permission-immediate-pending", IdempotencyKey: "permission-immediate-pending", PayloadHash: "permission-immediate-pending-hash", RequestHash: "permission-immediate-pending-hash", Kind: sessionruntime.SessionMutationRecordRunIntent, EventType: "session.assistant.queued", RunIntent: &pendingIntent, NowUnixMs: now}); err != nil {
		t.Fatalf("record pending intent: %v", err)
	}
	runningIntent := pendingIntent
	runningIntent.Status = sessionruntime.RunIntentRunning
	runningIntent.UpdatedAt = now + 1
	if _, err := server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{SessionID: created.ID, UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, ClientRequestID: "permission-immediate-running", IdempotencyKey: "permission-immediate-running", PayloadHash: "permission-immediate-running-hash", RequestHash: "permission-immediate-running-hash", Kind: sessionruntime.SessionMutationRecordRunIntent, EventType: "session.assistant.started", RunIntent: &runningIntent, NowUnixMs: now + 1}); err != nil {
		t.Fatalf("record running intent: %v", err)
	}

	callArgs := `{"command":"printf immediate\\n","timeout_ms":1000}`
	pending, err := permissionSvc.CreatePending(permission.CreateInput{SessionID: created.ID, RunID: runID, Step: 3, CallID: "call-immediate-permission", ToolName: "bash", ToolArguments: callArgs, ToolCallArguments: callArgs, Requirement: "tool", Mode: sessionruntime.ModeAuto})
	if err != nil {
		t.Fatalf("create pending permission: %v", err)
	}

	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		server.Handler().ServeHTTP(w, withTestPrincipal(r))
	}))
	defer httpServer.Close()

	resolvePayload := []byte(`{"action":"allow_once","reason":"ok"}`)
	resp, err := http.Post(httpServer.URL+"/v3/sessions/"+created.ID+"/permissions/"+pending.ID+"/resolve", "application/json", bytes.NewReader(resolvePayload))
	if err != nil {
		t.Fatalf("resolve permission over HTTP: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("resolve status = %d body=%s", resp.StatusCode, string(body))
	}
	var payload struct {
		Published bool                                 `json:"published"`
		Mutation  sessionruntime.SessionMutationResult `json:"mutation"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode resolve response: %v", err)
	}
	if !payload.Published || payload.Mutation.Event.EventType != "permission.updated" || payload.Mutation.Replayed {
		t.Fatalf("resolve response mutation = published:%t mutation:%+v", payload.Published, payload.Mutation)
	}

	events, err := sessionSvc.ListSessionEvents(created.ID, 0, 20)
	if err != nil {
		t.Fatalf("list session events: %v", err)
	}
	var update *sessionruntime.SessionEvent
	for _, event := range events {
		if event.EventType == "permission.updated" {
			copy := event
			update = &copy
		}
	}
	if update == nil {
		t.Fatalf("missing immediate permission.updated event: %+v", events)
	}
	var updatePayload struct {
		RunID          string                       `json:"run_id"`
		Step           int                          `json:"step"`
		ToolName       string                       `json:"tool_name"`
		CallID         string                       `json:"call_id"`
		ToolInstanceID string                       `json:"tool_instance_id"`
		Arguments      string                       `json:"arguments"`
		Permission     pebblestore.PermissionRecord `json:"permission"`
	}
	if err := json.Unmarshal(update.Payload, &updatePayload); err != nil {
		t.Fatalf("decode permission.updated payload: %v", err)
	}
	if updatePayload.RunID != runID || updatePayload.Step != 3 || updatePayload.ToolName != "bash" || updatePayload.CallID != "call-immediate-permission" || updatePayload.ToolInstanceID != "step-3:call-immediate-permission" || updatePayload.Arguments != callArgs || updatePayload.Permission.ID != pending.ID || updatePayload.Permission.Status != pebblestore.PermissionStatusApproved {
		t.Fatalf("permission.updated payload = %+v", updatePayload)
	}
}

func TestSessionsV3PrimaryPermissionUpdatedDuplicateExecutorAndHTTPReplays(t *testing.T) {
	server, sessionSvc, permissionSvc, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	workspace := t.TempDir()
	created := createSessionsV3PrimaryTestSessionWithWorkspaceAndPreference(t, server, "permission-duplicate-create", "permission duplicate", workspace, pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"})
	runID := "run-permission-duplicate"
	now := time.Now().UnixMilli()
	pendingIntent := pebblestore.V3SessionRunIntent{SessionID: created.ID, UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, RunID: runID, Status: sessionruntime.RunIntentPendingExecutor, UpdatedAt: now}
	if _, err := server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{SessionID: created.ID, UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, ClientRequestID: "permission-duplicate-pending", IdempotencyKey: "permission-duplicate-pending", PayloadHash: "permission-duplicate-pending-hash", RequestHash: "permission-duplicate-pending-hash", Kind: sessionruntime.SessionMutationRecordRunIntent, EventType: "session.assistant.queued", RunIntent: &pendingIntent, NowUnixMs: now}); err != nil {
		t.Fatalf("record pending intent: %v", err)
	}
	runningIntent := pendingIntent
	runningIntent.Status = sessionruntime.RunIntentRunning
	runningIntent.UpdatedAt = now + 1
	if _, err := server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{SessionID: created.ID, UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, ClientRequestID: "permission-duplicate-running", IdempotencyKey: "permission-duplicate-running", PayloadHash: "permission-duplicate-running-hash", RequestHash: "permission-duplicate-running-hash", Kind: sessionruntime.SessionMutationRecordRunIntent, EventType: "session.assistant.started", RunIntent: &runningIntent, NowUnixMs: now + 1}); err != nil {
		t.Fatalf("record running intent: %v", err)
	}

	callArgs := `{"command":"printf duplicate\\n","timeout_ms":1000}`
	pending, err := permissionSvc.CreatePending(permission.CreateInput{SessionID: created.ID, RunID: runID, Step: 2, CallID: "call-duplicate-permission", ToolName: "bash", ToolArguments: callArgs, ToolCallArguments: callArgs, Requirement: "tool", Mode: sessionruntime.ModeAuto})
	if err != nil {
		t.Fatalf("create pending permission: %v", err)
	}
	resolved, _, err := permissionSvc.ResolveWithPolicyAndArguments(created.ID, pending.ID, "allow_once", "ok", "")
	if err != nil {
		t.Fatalf("resolve permission directly: %v", err)
	}

	exec := &sessionV3Executor{server: server}
	if err := exec.recordProviderToolEvent(sessionV3ExecutorJob{Principal: testPrincipal(), SessionID: created.ID, RunID: runID}, runruntime.StreamEvent{Type: runruntime.StreamEventPermissionUpdate, SessionID: created.ID, Step: 2, ToolName: "bash", CallID: "call-duplicate-permission", Arguments: callArgs, Permission: &resolved}, "permission.updated", 0); err != nil {
		t.Fatalf("record executor permission.updated: %v", err)
	}

	mutation, published, err := server.publishSessionV3PermissionUpdatedFromRecord(testPrincipal(), created.ID, resolved)
	if err != nil {
		t.Fatalf("duplicate HTTP permission.updated should replay, got error: %v", err)
	}
	if published || !mutation.Replayed || mutation.Event.EventType != "permission.updated" {
		t.Fatalf("duplicate publish = published:%t mutation:%+v, want replayed permission.updated", published, mutation)
	}

	events, err := sessionSvc.ListSessionEvents(created.ID, 0, 20)
	if err != nil {
		t.Fatalf("list session events: %v", err)
	}
	updates := 0
	for _, event := range events {
		if event.EventType == "permission.updated" {
			updates++
			var payload map[string]any
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				t.Fatalf("decode permission.updated payload: %v", err)
			}
			if _, ok := payload["recorded_at"]; ok {
				t.Fatalf("permission.updated payload should be idempotency-stable and omit recorded_at: %+v", payload)
			}
		}
	}
	if updates != 1 {
		t.Fatalf("permission.updated event count = %d, want 1; events=%+v", updates, events)
	}
}

func TestSessionsV3PrimaryAlwaysAllowPersistsInPermissionsPolicyView(t *testing.T) {
	server, sessionSvc, permissionSvc, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	session, _, err := sessionSvc.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		SessionID:      "session-v3-policy-source",
		Title:          "V3 Policy Source",
		WorkspacePath:  "/host/workspace",
		WorkspaceName:  "workspace",
		Mode:           sessionruntime.ModeAuto,
		UserID:         testPrincipal().UserID,
		AccountScopeID: testPrincipal().AccountScopeID,
		Preference: &pebblestore.ModelPreference{
			Provider: "test-provider",
			Model:    "test-model",
			Thinking: "medium",
		},
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	pending, err := permissionSvc.CreatePending(permission.CreateInput{
		SessionID:     session.ID,
		RunID:         "run-policy-source",
		CallID:        "call-policy-source",
		ToolName:      "bash",
		ToolArguments: `{"command":"git status"}`,
		Requirement:   "tool",
		Mode:          sessionruntime.ModeAuto,
	})
	if err != nil {
		t.Fatalf("create pending permission: %v", err)
	}

	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		server.Handler().ServeHTTP(w, withTestPrincipal(r))
	}))
	defer httpServer.Close()

	resolveBody := []byte(`{"action":"allow_always","reason":"ok"}`)
	resp, err := http.Post(httpServer.URL+"/v3/sessions/"+session.ID+"/permissions/"+pending.ID+"/resolve", "application/json", bytes.NewReader(resolveBody))
	if err != nil {
		t.Fatalf("resolve permission: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("resolve status = %d", resp.StatusCode)
	}

	policyResp, err := http.Get(httpServer.URL + "/v1/permissions")
	if err != nil {
		t.Fatalf("get permissions policy: %v", err)
	}
	defer policyResp.Body.Close()
	if policyResp.StatusCode != http.StatusOK {
		t.Fatalf("policy status = %d", policyResp.StatusCode)
	}
	var payload struct {
		Policy permission.Policy `json:"policy"`
	}
	if err := json.NewDecoder(policyResp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode policy response: %v", err)
	}
	foundAllowRule := false
	for _, rule := range payload.Policy.Rules {
		if rule.Decision == permission.PolicyDecisionAllow && rule.Kind == permission.PolicyRuleKindBashPrefix && rule.Tool == "bash" && rule.Pattern == "git" {
			foundAllowRule = true
			break
		}
	}
	if !foundAllowRule {
		t.Fatalf("policy rules = %+v, want always-allow rule visible in /permissions", payload.Policy.Rules)
	}

	globalPolicy, err := permissionSvc.CurrentPolicy()
	if err != nil {
		t.Fatalf("current global policy: %v", err)
	}
	for _, rule := range globalPolicy.Rules {
		if rule.Decision == permission.PolicyDecisionAllow && rule.Kind == permission.PolicyRuleKindBashPrefix && rule.Tool == "bash" && rule.Pattern == "git" {
			t.Fatalf("global policy rules = %+v, want no leaked V3 rule", globalPolicy.Rules)
		}
	}
}

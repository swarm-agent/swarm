package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

func TestSessionsV2HandlersDoNotWrapLegacySessionCreate(t *testing.T) {
	for _, path := range []string{"sessions_v2_primary.go"} {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for _, forbidden := range []string{"createSessionFromRequest", "handleSessions(", "handleSessionByID", "sessionCreateRequest", "proxyRoutedSessionRequest"} {
			if strings.Contains(string(body), forbidden) {
				t.Fatalf("%s contains forbidden legacy wrapper symbol %q", path, forbidden)
			}
		}
	}
}

func TestSessionsV2PrimaryCreatesLocalSessionFromBindingAuthority(t *testing.T) {
	server, sessionSvc, _, routeStore, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	server.SetWorktreeService(&fakeWorktreeService{config: worktreeruntime.Config{Enabled: true, BaseBranch: "must-not-read", BranchName: "ignored/config"}})
	seedSessionsV2PrimaryAuthority(t, server, swarmStore, "host-swarm-id", "binding-primary-v2", "/host/swarm-go")

	req := httptest.NewRequest(http.MethodPost, "/v2/sessions/primary", bytes.NewBufferString(`{"swarm_id":"host-swarm-id","workspace_binding_id":"binding-primary-v2","title":"primary v2","mode":"auto","agent_name":"swarm","worktree_mode":"off","preference":{"provider":"codex","model":"gpt-5.4","thinking":"medium"},"metadata":{"purpose":"test"}}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, withTestPrincipal(req))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload struct {
		OK               bool                            `json:"ok"`
		Session          pebblestore.SessionSnapshot     `json:"session"`
		SessionExecution sessionruntime.SessionExecution `json:"session_execution"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !payload.OK || strings.TrimSpace(payload.Session.ID) == "" {
		t.Fatalf("payload = %+v", payload)
	}
	if payload.Session.WorkspacePath != "/host/swarm-go" || payload.Session.WorkspaceName != "swarm-go" {
		t.Fatalf("session workspace = path %q name %q", payload.Session.WorkspacePath, payload.Session.WorkspaceName)
	}
	if payload.Session.UserID != testPrincipal().UserID || payload.Session.AccountScopeID != testPrincipal().AccountScopeID {
		t.Fatalf("session principal = %q/%q", payload.Session.UserID, payload.Session.AccountScopeID)
	}
	if payload.Session.Metadata["purpose"] != "test" || payload.Session.Metadata["swarm_v2_execution_class"] != sessionruntime.SessionExecutionClassPrimary || payload.Session.Metadata["swarm_v2_runtime_swarm_id"] != "host-swarm-id" || payload.Session.Metadata["local_workspace_binding_id"] != "binding-primary-v2" {
		t.Fatalf("session metadata = %+v", payload.Session.Metadata)
	}
	if payload.SessionExecution.ExecutionClass != sessionruntime.SessionExecutionClassPrimary || payload.SessionExecution.RuntimeSwarmID != "host-swarm-id" || payload.SessionExecution.AuthorityHostSwarmID != "host-swarm-id" || payload.SessionExecution.WorkspaceBindingID != "binding-primary-v2" || payload.SessionExecution.RuntimeWorkspacePath != "/host/swarm-go" || payload.SessionExecution.PlacementGeneration != 1 || payload.SessionExecution.BindingGeneration != 1 {
		t.Fatalf("session execution = %+v", payload.SessionExecution)
	}
	storedExecution, ok, err := sessionSvc.Store().GetSessionExecutionV2(payload.Session.ID)
	if err != nil || !ok {
		t.Fatalf("stored execution ok=%t err=%v", ok, err)
	}
	if storedExecution.ExecutionClass != sessionruntime.SessionExecutionClassPrimary || storedExecution.WorkspaceBindingID != "binding-primary-v2" {
		t.Fatalf("stored execution = %+v", storedExecution)
	}
	if sessions, err := sessionSvc.ListSessionsForAccount(testPrincipal().AccountScopeID, 10); err != nil || len(sessions) != 1 {
		t.Fatalf("sessions = %+v err=%v, want one", sessions, err)
	}
	if routes, err := routeStore.List(10); err != nil || len(routes) != 0 {
		t.Fatalf("routes = %+v err=%v, want no legacy route for primary v2", routes, err)
	}
	if _, ok, err := server.topology.GetSessionRouteForAccount(testPrincipal().AccountScopeID, payload.Session.ID); err != nil {
		t.Fatalf("get topology session route: %v", err)
	} else if ok {
		t.Fatalf("unexpected topology route for primary v2 session")
	}
	if fake, ok := server.worktrees.(*fakeWorktreeService); ok && fake.configReadCount != 0 {
		t.Fatalf("worktree_mode off read global/workspace config %d times", fake.configReadCount)
	}
}

func TestSessionsV2PrimaryWorktreeOnRealizesPerRequestWorktree(t *testing.T) {
	server, sessionSvc, _, routeStore, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	seedSessionsV2PrimaryAuthority(t, server, swarmStore, "host-swarm-id", "binding-primary-v2", "/host/swarm-go")
	fake := &fakeWorktreeService{
		config: worktreeruntime.Config{Enabled: false, BaseBranch: "ignored-config", BranchName: "ignored/config"},
		allocation: worktreeruntime.Allocation{
			RepoRoot:    "/host/swarm-go",
			BaseBranch:  "dev",
			BranchName:  "agent/session-primary-v2",
			WorkspaceID: "",
		},
	}
	server.SetWorktreeService(fake)

	rec := postSessionsV2Primary(t, server, `{"swarm_id":"host-swarm-id","workspace_binding_id":"binding-primary-v2","title":"primary v2 wt","mode":"auto","agent_name":"swarm","worktree_mode":"on","worktree_use_current_branch":false,"worktree_base_branch":"dev","worktree_branch_name":"agent/session-primary-v2","preference":{"provider":"codex","model":"gpt-5.4","thinking":"medium"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload struct {
		OK               bool                            `json:"ok"`
		Session          pebblestore.SessionSnapshot     `json:"session"`
		SessionExecution sessionruntime.SessionExecution `json:"session_execution"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !payload.OK || strings.TrimSpace(payload.Session.ID) == "" {
		t.Fatalf("payload = %+v", payload)
	}
	if fake.configReadCount != 0 {
		t.Fatalf("primary v2 worktree create read global/workspace config %d times", fake.configReadCount)
	}
	if fake.lastWorkspace != "/host/swarm-go" || fake.lastNameSeed != payload.Session.ID || fake.lastBaseBranch != "dev" || fake.lastBranchName != "agent/session-primary-v2" {
		t.Fatalf("allocation request workspace=%q seed=%q base=%q branch=%q session=%q", fake.lastWorkspace, fake.lastNameSeed, fake.lastBaseBranch, fake.lastBranchName, payload.Session.ID)
	}
	if !strings.HasSuffix(payload.Session.WorkspacePath, worktreeruntime.WorkspaceIdentityForSession(payload.Session.ID)) {
		t.Fatalf("worktree path %q does not use session workspace identity %q", payload.Session.WorkspacePath, worktreeruntime.WorkspaceIdentityForSession(payload.Session.ID))
	}
	if !strings.HasPrefix(payload.Session.WorkspacePath, "/host/swarm-go/.swarm/worktrees/") || !payload.Session.WorktreeEnabled || payload.Session.WorktreeRootPath != payload.Session.WorkspacePath || payload.Session.WorktreeBaseBranch != "dev" || payload.Session.WorktreeBranch != "agent/session-primary-v2" {
		t.Fatalf("session worktree facts = %+v", payload.Session)
	}
	if payload.SessionExecution.SessionID != payload.Session.ID || payload.SessionExecution.SourceWorkspacePath != "/host/swarm-go" || payload.SessionExecution.RuntimeWorkspacePath != payload.Session.WorkspacePath || !payload.SessionExecution.WorktreeEnabled || payload.SessionExecution.WorktreeRootPath != payload.Session.WorkspacePath || payload.SessionExecution.WorktreeBaseBranch != "dev" || payload.SessionExecution.WorktreeBranch != "agent/session-primary-v2" {
		t.Fatalf("execution worktree facts = %+v session=%+v", payload.SessionExecution, payload.Session)
	}
	if payload.Session.Metadata["workspace_id"] != worktreeruntime.WorkspaceIdentityForSession(payload.Session.ID) || payload.Session.Metadata["swarm_v2_runtime_workspace_path"] != payload.Session.WorkspacePath || payload.Session.Metadata["swarm_v2_source_workspace_path"] != "/host/swarm-go" || payload.Session.Metadata["swarm_v2_worktree_enabled"] != true || payload.Session.Metadata["swarm_v2_worktree_root_path"] != payload.Session.WorkspacePath || payload.Session.Metadata["swarm_v2_worktree_base_branch"] != "dev" || payload.Session.Metadata["swarm_v2_worktree_branch"] != "agent/session-primary-v2" {
		t.Fatalf("metadata worktree projection = %+v", payload.Session.Metadata)
	}
	storedExecution, ok, err := sessionSvc.Store().GetSessionExecutionV2(payload.Session.ID)
	if err != nil || !ok {
		t.Fatalf("stored execution ok=%t err=%v", ok, err)
	}
	if storedExecution.RuntimeWorkspacePath != payload.Session.WorkspacePath || storedExecution.SourceWorkspacePath != "/host/swarm-go" || !storedExecution.WorktreeEnabled || storedExecution.WorktreeRootPath != payload.Session.WorkspacePath || storedExecution.WorktreeBaseBranch != "dev" || storedExecution.WorktreeBranch != "agent/session-primary-v2" {
		t.Fatalf("stored execution = %+v", storedExecution)
	}
	if routes, err := routeStore.List(10); err != nil || len(routes) != 0 {
		t.Fatalf("routes = %+v err=%v, want no legacy route for primary v2 worktree", routes, err)
	}
}

func TestSessionsV2PrimaryWorktreeOnRequiresWorktreeService(t *testing.T) {
	server, _, _, routeStore, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	seedSessionsV2PrimaryAuthority(t, server, swarmStore, "host-swarm-id", "binding-primary-v2", "/host/swarm-go")

	rec := postSessionsV2Primary(t, server, `{"swarm_id":"host-swarm-id","workspace_binding_id":"binding-primary-v2","title":"primary v2 wt","mode":"auto","agent_name":"swarm","worktree_mode":"on","preference":{"provider":"codex","model":"gpt-5.4","thinking":"medium"}}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "requires worktree service") {
		t.Fatalf("body = %s, want missing worktree service rejection", rec.Body.String())
	}
	assertNoPrimaryCreateResidue(t, server, routeStore)
}

func TestSessionsV2PrimaryWorktreeOnRejectsAllocationFailure(t *testing.T) {
	server, _, _, routeStore, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	seedSessionsV2PrimaryAuthority(t, server, swarmStore, "host-swarm-id", "binding-primary-v2", "/host/swarm-go")
	server.SetWorktreeService(&fakeWorktreeService{allocationErr: errors.New("boom")})

	rec := postSessionsV2Primary(t, server, `{"swarm_id":"host-swarm-id","workspace_binding_id":"binding-primary-v2","title":"primary v2 wt","mode":"auto","agent_name":"swarm","worktree_mode":"on","preference":{"provider":"codex","model":"gpt-5.4","thinking":"medium"}}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "realize primary sessions v2 worktree") {
		t.Fatalf("body = %s, want allocation failure", rec.Body.String())
	}
	assertNoPrimaryCreateResidue(t, server, routeStore)
}

func TestSessionsV2PrimaryRejectsUnsupportedWorktreeMode(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{name: "inherit", body: `{"swarm_id":"host-swarm-id","workspace_binding_id":"binding-primary-v2","title":"primary v2 wt","mode":"auto","agent_name":"swarm","worktree_mode":"inherit","preference":{"provider":"codex","model":"gpt-5.4","thinking":"medium"}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server, _, _, routeStore, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
			seedSessionsV2PrimaryAuthority(t, server, swarmStore, "host-swarm-id", "binding-primary-v2", "/host/swarm-go")

			rec := postSessionsV2Primary(t, server, tc.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "unsupported worktree_mode") {
				t.Fatalf("body = %s, want unsupported mode rejection", rec.Body.String())
			}
			assertNoPrimaryCreateResidue(t, server, routeStore)
		})
	}
}

func TestSessionsV2PrimaryRejectsExplicitBaseBranchWithoutExplicitBranchMode(t *testing.T) {
	server, _, _, routeStore, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	seedSessionsV2PrimaryAuthority(t, server, swarmStore, "host-swarm-id", "binding-primary-v2", "/host/swarm-go")

	rec := postSessionsV2Primary(t, server, `{"swarm_id":"host-swarm-id","workspace_binding_id":"binding-primary-v2","title":"primary v2 wt","mode":"auto","agent_name":"swarm","worktree_mode":"on","worktree_use_current_branch":false,"preference":{"provider":"codex","model":"gpt-5.4","thinking":"medium"}}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "worktree_base_branch is required") {
		t.Fatalf("body = %s, want base branch required rejection", rec.Body.String())
	}
	assertNoPrimaryCreateResidue(t, server, routeStore)
}

func TestSessionsV2PrimaryRejectsWorktreeFieldsWhenOff(t *testing.T) {
	server, _, _, routeStore, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	seedSessionsV2PrimaryAuthority(t, server, swarmStore, "host-swarm-id", "binding-primary-v2", "/host/swarm-go")

	rec := postSessionsV2Primary(t, server, `{"swarm_id":"host-swarm-id","workspace_binding_id":"binding-primary-v2","title":"primary v2 wt","mode":"auto","agent_name":"swarm","worktree_base_branch":"dev","preference":{"provider":"codex","model":"gpt-5.4","thinking":"medium"}}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "worktree fields are only allowed") && !strings.Contains(rec.Body.String(), "unsupported worktree_mode") {
		t.Fatalf("body = %s, want worktree field rejection", rec.Body.String())
	}
	assertNoPrimaryCreateResidue(t, server, routeStore)
}

func TestSessionsV2PrimaryRejectsLocalContainerRuntime(t *testing.T) {
	server, _, _, routeStore, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	seedSessionsV2PrimaryAuthority(t, server, swarmStore, "host-swarm-id", "binding-primary-v2", "/host/swarm-go")
	if err := server.topology.UpsertRuntime(pebblestore.TopologyRuntimeRecord{
		SwarmID:              "container-swarm",
		UserID:               testPrincipal().UserID,
		AccountScopeID:       testPrincipal().AccountScopeID,
		Name:                 "container",
		Relationship:         "child",
		Status:               "online",
		OwnerHostSwarmID:     "host-swarm-id",
		OwnerHostContainerID: "host-container-1",
	}); err != nil {
		t.Fatalf("upsert container runtime: %v", err)
	}
	if _, err := server.topology.UpsertWorkspaceBinding(pebblestore.TopologyWorkspaceBindingRecord{
		BindingID:                       "binding-container-v2",
		UserID:                          testPrincipal().UserID,
		AccountScopeID:                  testPrincipal().AccountScopeID,
		SourceWorkspaceID:               "workspace-container-v2",
		SourceWorkspaceGeneration:       1,
		SourceWorkspacePath:             "/host/swarm-go",
		SourceWorkspaceName:             "swarm-go",
		DestinationRuntimeSwarmID:       "container-swarm",
		DestinationAuthorityHostSwarmID: "host-swarm-id",
		DestinationHostSwarmID:          "host-swarm-id",
		DestinationRuntimeKind:          pebblestore.TopologyRuntimeKindContainer,
		DestinationContainerID:          "host-container-1",
		DestinationWorkspacePath:        "/workspaces/swarm-go",
		PlacementGeneration:             1,
		BindingGeneration:               1,
		State:                           pebblestore.TopologyWorkspaceBindingStateBound,
		AccessMode:                      pebblestore.TopologyWorkspaceBindingAccessModeReadWrite,
		MaterializationKind:             pebblestore.TopologyWorkspaceBindingMaterializationSource,
		AttestedByHostSwarmID:           "host-swarm-id",
		Writable:                        true,
		LegacyTargetKind:                "local-container",
	}); err != nil {
		t.Fatalf("upsert container binding: %v", err)
	}

	rec := postSessionsV2Primary(t, server, `{"swarm_id":"container-swarm","workspace_binding_id":"binding-container-v2","title":"bad","mode":"auto","agent_name":"swarm","worktree_mode":"off","preference":{"provider":"codex","model":"gpt-5.4","thinking":"medium"}}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "is not the primary runtime") {
		t.Fatalf("body = %s, want primary runtime rejection", rec.Body.String())
	}
	assertNoPrimaryCreateResidue(t, server, routeStore)
}

func TestSessionsV2PrimaryRejectsForbiddenAuthorityFields(t *testing.T) {
	cases := []struct {
		name      string
		fieldJSON string
	}{
		{name: "workspace path", fieldJSON: `"workspace_path":"/tmp/stale"`},
		{name: "host workspace path", fieldJSON: `"host_workspace_path":"/tmp/stale"`},
		{name: "runtime workspace path", fieldJSON: `"runtime_workspace_path":"/tmp/stale"`},
		{name: "workspace name", fieldJSON: `"workspace_name":"swarm-go"`},
		{name: "target swarm", fieldJSON: `"target_swarm_id":"host-swarm-id"`},
		{name: "backend url", fieldJSON: `"backend_url":"http://127.0.0.1:9"`},
		{name: "child backend url", fieldJSON: `"child_backend_url":"http://127.0.0.1:9"`},
		{name: "target backend url", fieldJSON: `"target_backend_url":"http://127.0.0.1:9"`},
		{name: "next hop swarm", fieldJSON: `"next_hop_swarm_id":"next"`},
		{name: "next hop backend", fieldJSON: `"next_hop_backend_url":"http://127.0.0.1:9"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server, _, _, routeStore, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
			seedSessionsV2PrimaryAuthority(t, server, swarmStore, "host-swarm-id", "binding-primary-v2", "/host/swarm-go")
			body := `{"swarm_id":"host-swarm-id","workspace_binding_id":"binding-primary-v2",` + tc.fieldJSON + `,"title":"bad","mode":"auto","agent_name":"swarm","worktree_mode":"off","preference":{"provider":"codex","model":"gpt-5.4","thinking":"medium"}}`
			rec := postSessionsV2Primary(t, server, body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "routing authority field") {
				t.Fatalf("body = %s, want routing authority rejection", rec.Body.String())
			}
			assertNoPrimaryCreateResidue(t, server, routeStore)
		})
	}
}

func TestSessionsV2PrimaryRequiresBindingID(t *testing.T) {
	server, _, _, routeStore, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	seedSessionsV2PrimaryAuthority(t, server, swarmStore, "host-swarm-id", "binding-primary-v2", "/host/swarm-go")

	rec := postSessionsV2Primary(t, server, `{"swarm_id":"host-swarm-id","title":"missing binding","mode":"auto","agent_name":"swarm","worktree_mode":"off","preference":{"provider":"codex","model":"gpt-5.4","thinking":"medium"}}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "workspace_binding_id is required") {
		t.Fatalf("body = %s, want binding required error", rec.Body.String())
	}
	assertNoPrimaryCreateResidue(t, server, routeStore)
}

func TestSessionsV2PrimaryRejectsStaleBindingPlacementGeneration(t *testing.T) {
	server, _, _, routeStore, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	seedSessionsV2PrimaryAuthorityWithBindingGeneration(t, server, swarmStore, "host-swarm-id", "binding-primary-v2", "/host/swarm-go", 1, 2)

	rec := postSessionsV2Primary(t, server, `{"swarm_id":"host-swarm-id","workspace_binding_id":"binding-primary-v2","title":"stale","mode":"auto","agent_name":"swarm","worktree_mode":"off","preference":{"provider":"codex","model":"gpt-5.4","thinking":"medium"}}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusConflict, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "generation does not match placement") {
		t.Fatalf("body = %s, want stale placement rejection", rec.Body.String())
	}
	assertNoPrimaryCreateResidue(t, server, routeStore)
}

func TestSessionsV2PrimaryRejectsWorkspaceNameOnlyBindingLookup(t *testing.T) {
	server, _, _, routeStore, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	seedSessionsV2PrimaryAuthority(t, server, swarmStore, "host-swarm-id", "binding-primary-v2", "/host/swarm-go")

	rec := postSessionsV2Primary(t, server, `{"swarm_id":"host-swarm-id","workspace_name":"swarm-go","title":"name only","mode":"auto","agent_name":"swarm","worktree_mode":"off","preference":{"provider":"codex","model":"gpt-5.4","thinking":"medium"}}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "routing authority field") {
		t.Fatalf("body = %s, want no name-only binding lookup", rec.Body.String())
	}
	assertNoPrimaryCreateResidue(t, server, routeStore)
}

func TestSessionsV2LocalContainerCreatesSessionFromBindingAuthority(t *testing.T) {
	server, sessionSvc, _, routeStore, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	seedSessionsV2LocalContainerAuthority(t, server, swarmStore, "host-swarm-id", "container-swarm", "host-container-1", "binding-container-v2", "/host/swarm-go", "/workspaces/swarm-go")

	req := httptest.NewRequest(http.MethodPost, "/v2/sessions/local-containers", bytes.NewBufferString(`{"swarm_id":"container-swarm","workspace_binding_id":"binding-container-v2","title":"container v2","mode":"auto","agent_name":"swarm","worktree_mode":"off","preference":{"provider":"codex","model":"gpt-5.4","thinking":"medium"},"metadata":{"purpose":"test"}}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload struct {
		OK               bool                            `json:"ok"`
		Session          pebblestore.SessionSnapshot     `json:"session"`
		SessionExecution sessionruntime.SessionExecution `json:"session_execution"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Session.WorkspacePath != "/workspaces/swarm-go" || payload.Session.WorkspaceName != "swarm-go" {
		t.Fatalf("session workspace = path %q name %q", payload.Session.WorkspacePath, payload.Session.WorkspaceName)
	}
	if payload.SessionExecution.ExecutionClass != sessionruntime.SessionExecutionClassLocalContainer || payload.SessionExecution.RuntimeSwarmID != "container-swarm" || payload.SessionExecution.AuthorityHostSwarmID != "host-swarm-id" || payload.SessionExecution.AuthorityContainerID != "host-container-1" || payload.SessionExecution.SourceWorkspacePath != "/host/swarm-go" || payload.SessionExecution.RuntimeWorkspacePath != "/workspaces/swarm-go" {
		t.Fatalf("session execution = %+v", payload.SessionExecution)
	}
	storedExecution, ok, err := sessionSvc.Store().GetSessionExecutionV2(payload.Session.ID)
	if err != nil || !ok {
		t.Fatalf("stored execution ok=%t err=%v", ok, err)
	}
	if storedExecution.SourceWorkspacePath != "/host/swarm-go" || storedExecution.RuntimeWorkspacePath != "/workspaces/swarm-go" {
		t.Fatalf("stored execution = %+v", storedExecution)
	}
	if routes, err := routeStore.List(10); err != nil || len(routes) != 0 {
		t.Fatalf("routes = %+v err=%v, want no legacy route for local-container v2", routes, err)
	}
}

func TestSessionsV2PrimaryCreateEndpointIsNotRejectedAsReservedLifecycleID(t *testing.T) {
	server, _, _, _, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	seedSessionsV2PrimaryAuthority(t, server, swarmStore, "host-swarm-id", "binding-primary-v2", "/host/swarm-go")

	rec := postSessionsV2Primary(t, server, `{"swarm_id":"host-swarm-id","workspace_binding_id":"binding-primary-v2","title":"primary v2","mode":"auto","agent_name":"swarm","worktree_mode":"off","preference":{"provider":"codex","model":"gpt-5.4","thinking":"medium"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "session_v2_bad_request") || strings.Contains(rec.Body.String(), "invalid sessions v2 lifecycle path") {
		t.Fatalf("primary create was rejected as lifecycle path: %s", rec.Body.String())
	}
}

func TestSessionsV2LocalContainerCreateEndpointIsNotRejectedAsReservedLifecycleID(t *testing.T) {
	server, _, _, _, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	seedSessionsV2LocalContainerAuthority(t, server, swarmStore, "host-swarm-id", "container-swarm", "host-container-1", "binding-container-v2", "/host/swarm-go", "/workspaces/swarm-go")

	req := httptest.NewRequest(http.MethodPost, "/v2/sessions/local-containers", bytes.NewBufferString(`{"swarm_id":"container-swarm","workspace_binding_id":"binding-container-v2","title":"container v2","mode":"auto","agent_name":"swarm","worktree_mode":"off","preference":{"provider":"codex","model":"gpt-5.4","thinking":"medium"}}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "session_v2_bad_request") || strings.Contains(rec.Body.String(), "invalid sessions v2 lifecycle path") {
		t.Fatalf("local-container create was rejected as lifecycle path: %s", rec.Body.String())
	}
}

func TestSessionsV2LocalContainerRejectsHostPlacement(t *testing.T) {
	server, _, _, routeStore, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	seedSessionsV2PrimaryAuthority(t, server, swarmStore, "host-swarm-id", "binding-primary-v2", "/host/swarm-go")

	req := httptest.NewRequest(http.MethodPost, "/v2/sessions/local-containers", bytes.NewBufferString(`{"swarm_id":"host-swarm-id","workspace_binding_id":"binding-primary-v2","title":"bad","mode":"auto","agent_name":"swarm","worktree_mode":"off","preference":{"provider":"codex","model":"gpt-5.4","thinking":"medium"}}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	assertNoPrimaryCreateResidue(t, server, routeStore)
}

func postSessionsV2Primary(t *testing.T, server *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v2/sessions/primary", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	return rec
}

func seedSessionsV2PrimaryAuthority(t *testing.T, server *Server, swarmStore *pebblestore.SwarmStore, swarmID, bindingID, workspacePath string) {
	t.Helper()
	seedSessionsV2PrimaryAuthorityWithBindingGeneration(t, server, swarmStore, swarmID, bindingID, workspacePath, 1, 1)
}

func seedSessionsV2PrimaryAuthorityWithBindingGeneration(t *testing.T, server *Server, swarmStore *pebblestore.SwarmStore, swarmID, bindingID, workspacePath string, placementGeneration, bindingPlacementGeneration int) {
	t.Helper()
	if server == nil || server.topology == nil || swarmStore == nil {
		t.Fatal("server topology and swarm store are required")
	}
	now := time.Now().UnixMilli()
	if _, err := swarmStore.PutLocalNode(pebblestore.SwarmLocalNodeRecord{SwarmID: swarmID, Name: "host-swarm", Role: "master", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("put local node: %v", err)
	}
	if err := server.topology.UpsertRuntime(pebblestore.TopologyRuntimeRecord{SwarmID: swarmID, UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, Name: "host-swarm", Role: "master", Relationship: "self", Status: "online", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("upsert local runtime: %v", err)
	}
	if _, err := server.topology.EnsureLocalSelfPlacementForPrincipal(testPrincipal().AccountScopeID, testPrincipal().UserID); err != nil {
		t.Fatalf("ensure self placement: %v", err)
	}
	if placementGeneration != 1 {
		t.Fatalf("test helper only supports active self placement generation 1, got %d", placementGeneration)
	}
	legacyTargetKind := ""
	if bindingPlacementGeneration != placementGeneration {
		legacyTargetKind = "stale-primary-v2-test"
	}
	if _, err := server.topology.UpsertWorkspaceBinding(pebblestore.TopologyWorkspaceBindingRecord{
		BindingID:                       bindingID,
		UserID:                          testPrincipal().UserID,
		AccountScopeID:                  testPrincipal().AccountScopeID,
		SourceWorkspaceID:               "workspace-primary-v2",
		SourceWorkspaceGeneration:       1,
		SourceWorkspacePath:             workspacePath,
		SourceWorkspaceName:             "swarm-go",
		DestinationRuntimeSwarmID:       swarmID,
		DestinationAuthorityHostSwarmID: swarmID,
		DestinationHostSwarmID:          swarmID,
		DestinationRuntimeKind:          pebblestore.TopologyRuntimeKindHost,
		DestinationWorkspacePath:        workspacePath,
		PlacementGeneration:             bindingPlacementGeneration,
		BindingGeneration:               1,
		State:                           pebblestore.TopologyWorkspaceBindingStateBound,
		AccessMode:                      pebblestore.TopologyWorkspaceBindingAccessModeReadWrite,
		MaterializationKind:             pebblestore.TopologyWorkspaceBindingMaterializationSource,
		AttestedByHostSwarmID:           swarmID,
		Writable:                        true,
		LegacyTargetKind:                legacyTargetKind,
	}); err != nil {
		t.Fatalf("upsert binding: %v", err)
	}
}

var _ = sessionruntime.ModeAuto

func seedSessionsV2LocalContainerAuthority(t *testing.T, server *Server, swarmStore *pebblestore.SwarmStore, primarySwarmID, containerSwarmID, authorityContainerID, bindingID, sourceWorkspacePath, runtimeWorkspacePath string) {
	t.Helper()
	seedSessionsV2PrimaryAuthority(t, server, swarmStore, primarySwarmID, "binding-primary-v2-seed", sourceWorkspacePath)
	now := time.Now().UnixMilli()
	if err := server.topology.UpsertRuntime(pebblestore.TopologyRuntimeRecord{
		SwarmID:              containerSwarmID,
		UserID:               testPrincipal().UserID,
		AccountScopeID:       testPrincipal().AccountScopeID,
		Name:                 "container",
		Relationship:         "child",
		Status:               "online",
		OwnerHostSwarmID:     primarySwarmID,
		OwnerHostContainerID: authorityContainerID,
		CreatedAt:            now,
		UpdatedAt:            now,
	}); err != nil {
		t.Fatalf("upsert container runtime: %v", err)
	}
	if _, err := server.topology.PutRuntimePlacementForAccount(testPrincipal().AccountScopeID, pebblestore.TopologyRuntimePlacementRecord{
		RuntimeSwarmID:       containerSwarmID,
		AccountScopeID:       testPrincipal().AccountScopeID,
		AuthorityHostSwarmID: primarySwarmID,
		AuthorityContainerID: authorityContainerID,
		RuntimeKind:          pebblestore.TopologyRuntimeKindContainer,
		PlacementGeneration:  1,
		State:                pebblestore.TopologyRuntimePlacementStateActive,
		CreatedAt:            now,
		UpdatedAt:            now,
	}); err != nil {
		t.Fatalf("put container placement: %v", err)
	}
	if _, err := server.topology.UpsertWorkspaceBinding(pebblestore.TopologyWorkspaceBindingRecord{
		BindingID:                       bindingID,
		UserID:                          testPrincipal().UserID,
		AccountScopeID:                  testPrincipal().AccountScopeID,
		SourceWorkspaceID:               "workspace-container-v2",
		SourceWorkspaceGeneration:       1,
		SourceWorkspacePath:             sourceWorkspacePath,
		SourceWorkspaceName:             "swarm-go",
		DestinationRuntimeSwarmID:       containerSwarmID,
		DestinationAuthorityHostSwarmID: primarySwarmID,
		DestinationHostSwarmID:          primarySwarmID,
		DestinationRuntimeKind:          pebblestore.TopologyRuntimeKindContainer,
		DestinationContainerID:          authorityContainerID,
		DestinationWorkspacePath:        runtimeWorkspacePath,
		PlacementGeneration:             1,
		BindingGeneration:               1,
		State:                           pebblestore.TopologyWorkspaceBindingStateBound,
		AccessMode:                      pebblestore.TopologyWorkspaceBindingAccessModeReadWrite,
		MaterializationKind:             pebblestore.TopologyWorkspaceBindingMaterializationSource,
		AttestedByHostSwarmID:           primarySwarmID,
		Writable:                        true,
		LegacyTargetKind:                "local-container",
	}); err != nil {
		t.Fatalf("upsert container binding: %v", err)
	}
}

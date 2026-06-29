package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"swarm-refactor/swarmtui/pkg/startupconfig"
	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	swarmruntime "swarm/packages/swarmd/internal/swarm"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

func TestSessionsV2HandlersDoNotWrapLegacySessionCreate(t *testing.T) {
	for _, path := range []string{"sessions_v2_primary.go"} {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for _, forbidden := range []string{"createSessionFromRequest", "handleSessions(", "handleSessionByID", "sessionCreateRequest", "proxyRoutedSessionRequest", "/v1/swarm/peer/sessions/open"} {
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
	expectedWorkspaceID, err := worktreeruntime.WorkspaceIdentityForRequestedBranch("agent/session-primary-v2")
	if err != nil {
		t.Fatalf("WorkspaceIdentityForRequestedBranch: %v", err)
	}
	if !strings.HasSuffix(payload.Session.WorkspacePath, expectedWorkspaceID) {
		t.Fatalf("worktree path %q does not use requested branch workspace identity %q", payload.Session.WorkspacePath, expectedWorkspaceID)
	}
	if !strings.HasPrefix(payload.Session.WorkspacePath, "/host/swarm-go/.swarm/worktrees/") || !payload.Session.WorktreeEnabled || payload.Session.WorktreeRootPath != payload.Session.WorkspacePath || payload.Session.WorktreeBaseBranch != "dev" || payload.Session.WorktreeBranch != "agent/session-primary-v2" {
		t.Fatalf("session worktree facts = %+v", payload.Session)
	}
	if payload.SessionExecution.SessionID != payload.Session.ID || payload.SessionExecution.SourceWorkspacePath != "/host/swarm-go" || payload.SessionExecution.RuntimeWorkspacePath != payload.Session.WorkspacePath || !payload.SessionExecution.WorktreeEnabled || payload.SessionExecution.WorktreeRootPath != payload.Session.WorkspacePath || payload.SessionExecution.WorktreeBaseBranch != "dev" || payload.SessionExecution.WorktreeBranch != "agent/session-primary-v2" {
		t.Fatalf("execution worktree facts = %+v session=%+v", payload.SessionExecution, payload.Session)
	}
	if payload.Session.Metadata["workspace_id"] != expectedWorkspaceID || payload.Session.Metadata["swarm_v2_runtime_workspace_path"] != payload.Session.WorkspacePath || payload.Session.Metadata["swarm_v2_source_workspace_path"] != "/host/swarm-go" || payload.Session.Metadata["swarm_v2_worktree_enabled"] != true || payload.Session.Metadata["swarm_v2_worktree_root_path"] != payload.Session.WorkspacePath || payload.Session.Metadata["swarm_v2_worktree_base_branch"] != "dev" || payload.Session.Metadata["swarm_v2_worktree_branch"] != "agent/session-primary-v2" {
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

	rec := postSessionsV2Primary(t, server, `{"swarm_id":"host-swarm-id","workspace_binding_id":"binding-primary-v2","title":"primary v2 wt","mode":"auto","agent_name":"swarm","worktree_mode":"on","worktree_branch_name":"agent/session-primary-v2","preference":{"provider":"codex","model":"gpt-5.4","thinking":"medium"}}`)
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

	rec := postSessionsV2Primary(t, server, `{"swarm_id":"host-swarm-id","workspace_binding_id":"binding-primary-v2","title":"primary v2 wt","mode":"auto","agent_name":"swarm","worktree_mode":"on","worktree_branch_name":"agent/session-primary-v2","preference":{"provider":"codex","model":"gpt-5.4","thinking":"medium"}}`)
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

func TestSessionsV2LocalContainerCreatesViaNativeRuntimeOpen(t *testing.T) {
	hostServer, hostSessionSvc, _, routeStore, hostSwarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	runtimeServer, runtimeSessionSvc, _, _, runtimeSwarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	seedSessionsV2LocalContainerAuthority(t, hostServer, hostSwarmStore, "host-swarm-id", "container-swarm", "host-container-1", "binding-container-v2", "/host/swarm-go", "/workspaces/swarm-go")
	seedRuntimeSessionsV2OpenContainerAuthority(t, runtimeServer, runtimeSwarmStore, "host-swarm-id", "container-swarm", "host-container-1", "binding-container-v2", "/host/swarm-go", "/workspaces/swarm-go")
	setTestServerLocalSwarmID(t, runtimeServer, "container-swarm")
	seedRuntimeSessionsV2Pairing(t, runtimeSwarmStore, "host-swarm-id")

	var openCalls atomic.Int32
	runtimeHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != runtimeSessionsV2OpenPath {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get(peerAuthSwarmIDHeader) != "host-swarm-id" || r.Header.Get(peerAuthTokenHeader) != "peer-token" {
			t.Fatalf("runtime open peer auth headers = %q/%q", r.Header.Get(peerAuthSwarmIDHeader), r.Header.Get(peerAuthTokenHeader))
		}
		if r.Header.Get("X-Swarm-Principal-User-ID") != "" || r.Header.Get("X-Swarm-Principal-Account-Scope-ID") != "" {
			t.Fatalf("runtime open forwarded principal headers")
		}
		openCalls.Add(1)
		r = r.WithContext(context.WithValue(r.Context(), peerAuthAuthorizedContextKey, peerAuthContextValue{SwarmID: "host-swarm-id"}))
		if principal, ok := runtimeServer.trustedPairingPrincipalForPeerRequest(r); ok {
			r = withSessionsV2TestPrincipal(r, principal)
		}
		runtimeServer.Handler().ServeHTTP(w, r)
	}))
	defer runtimeHTTP.Close()
	if err := hostServer.RegisterAuthorityConnection(AuthorityConnection{AuthorityHostSwarmID: "container-swarm", AccountScopeID: testPrincipal().AccountScopeID, TransportKind: authorityConnectionTransportHTTP, TransportRef: runtimeHTTP.URL, Health: AuthorityConnectionHealthOnline}); err != nil {
		t.Fatalf("register runtime authority connection: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v2/sessions/local-containers", bytes.NewBufferString(`{"swarm_id":"container-swarm","workspace_binding_id":"binding-container-v2","title":"container v2","mode":"auto","agent_name":"swarm","worktree_mode":"off","preference":{"provider":"codex","model":"gpt-5.4","thinking":"medium"},"metadata":{"purpose":"test"}}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	hostServer.Handler().ServeHTTP(rec, withTestPrincipal(req))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if openCalls.Load() != 1 {
		t.Fatalf("runtime open calls = %d, want 1", openCalls.Load())
	}
	var payload struct {
		OK                  bool                                      `json:"ok"`
		Session             pebblestore.SessionSnapshot               `json:"session"`
		SessionExecution    sessionruntime.SessionExecution           `json:"session_execution"`
		RuntimeOpenResponse sessionruntime.RuntimeSessionOpenResponse `json:"runtime_open_response"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !payload.OK || strings.TrimSpace(payload.Session.ID) == "" {
		t.Fatalf("payload = %+v", payload)
	}
	if payload.Session.WorkspacePath != "/workspaces/swarm-go" || payload.Session.WorkspaceName != "swarm-go" {
		t.Fatalf("session workspace = path %q name %q", payload.Session.WorkspacePath, payload.Session.WorkspaceName)
	}
	if payload.Session.UserID != testPrincipal().UserID || payload.Session.AccountScopeID != testPrincipal().AccountScopeID {
		t.Fatalf("primary session principal = %q/%q", payload.Session.UserID, payload.Session.AccountScopeID)
	}
	if payload.Session.Metadata["purpose"] != "test" || payload.Session.Metadata["swarm_v2_execution_class"] != sessionruntime.SessionExecutionClassLocalContainer || payload.Session.Metadata["local_workspace_binding_id"] != "binding-container-v2" {
		t.Fatalf("primary session metadata = %+v", payload.Session.Metadata)
	}
	if payload.SessionExecution.SessionID != payload.Session.ID || payload.SessionExecution.ExecutionClass != sessionruntime.SessionExecutionClassLocalContainer || payload.SessionExecution.RuntimeSwarmID != "container-swarm" || payload.SessionExecution.AuthorityHostSwarmID != "host-swarm-id" || payload.SessionExecution.AuthorityContainerID != "host-container-1" || payload.SessionExecution.SourceWorkspaceID != "workspace-container-v2" || payload.SessionExecution.SourceWorkspaceGeneration != 1 || payload.SessionExecution.SourceWorkspacePath != "/host/swarm-go" || payload.SessionExecution.RuntimeWorkspacePath != "/workspaces/swarm-go" || payload.SessionExecution.PlacementGeneration != 1 || payload.SessionExecution.BindingGeneration != 1 {
		t.Fatalf("session execution = %+v session=%+v", payload.SessionExecution, payload.Session)
	}
	if !payload.RuntimeOpenResponse.OK || payload.RuntimeOpenResponse.SessionID != payload.Session.ID || payload.RuntimeOpenResponse.Status != "opened" {
		t.Fatalf("runtime open response = %+v", payload.RuntimeOpenResponse)
	}
	storedExecution, ok, err := hostSessionSvc.Store().GetSessionExecutionV2(payload.Session.ID)
	if err != nil || !ok {
		t.Fatalf("host stored execution ok=%t err=%v", ok, err)
	}
	if storedExecution.SourceWorkspacePath != "/host/swarm-go" || storedExecution.RuntimeWorkspacePath != "/workspaces/swarm-go" || storedExecution.RuntimeSwarmID != "container-swarm" {
		t.Fatalf("host stored execution = %+v", storedExecution)
	}
	runtimeSnapshot, ok, err := runtimeSessionSvc.GetSession(payload.Session.ID)
	if err != nil || !ok {
		t.Fatalf("runtime session ok=%t err=%v", ok, err)
	}
	if runtimeSnapshot.UserID != testPrincipal().UserID || runtimeSnapshot.AccountScopeID != testPrincipal().AccountScopeID {
		t.Fatalf("runtime session principal = %q/%q", runtimeSnapshot.UserID, runtimeSnapshot.AccountScopeID)
	}
	if routes, err := routeStore.List(10); err != nil || len(routes) != 0 {
		t.Fatalf("routes = %+v err=%v, want no legacy route for local-container v2", routes, err)
	}
}

func TestSessionsV2LocalContainerNativeRuntimeOpenFailureFailsCreate(t *testing.T) {
	hostServer, hostSessionSvc, _, routeStore, hostSwarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	seedSessionsV2LocalContainerAuthority(t, hostServer, hostSwarmStore, "host-swarm-id", "container-swarm", "host-container-1", "binding-container-v2", "/host/swarm-go", "/workspaces/swarm-go")
	failureRuntime := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != runtimeSessionsV2OpenPath {
			t.Fatalf("unexpected runtime path %s", r.URL.Path)
		}
		if r.Header.Get(peerAuthSwarmIDHeader) != "host-swarm-id" || r.Header.Get(peerAuthTokenHeader) != "peer-token" {
			t.Fatalf("runtime open failure peer auth headers = %q/%q", r.Header.Get(peerAuthSwarmIDHeader), r.Header.Get(peerAuthTokenHeader))
		}
		if r.Header.Get("X-Swarm-Principal-User-ID") != "" || r.Header.Get("X-Swarm-Principal-Account-Scope-ID") != "" {
			t.Fatalf("runtime open failure path forwarded principal headers")
		}
		writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": "boom"})
	}))
	defer failureRuntime.Close()
	if err := hostServer.RegisterAuthorityConnection(AuthorityConnection{AuthorityHostSwarmID: "container-swarm", AccountScopeID: testPrincipal().AccountScopeID, TransportKind: authorityConnectionTransportHTTP, TransportRef: failureRuntime.URL, Health: AuthorityConnectionHealthOnline}); err != nil {
		t.Fatalf("register runtime authority connection: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v2/sessions/local-containers", bytes.NewBufferString(`{"swarm_id":"container-swarm","workspace_binding_id":"binding-container-v2","title":"container v2","mode":"auto","agent_name":"swarm","worktree_mode":"off","preference":{"provider":"codex","model":"gpt-5.4","thinking":"medium"}}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	hostServer.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusConflict, rec.Body.String())
	}
	if sessions, err := hostSessionSvc.ListSessionsForAccount(testPrincipal().AccountScopeID, 10); err != nil || len(sessions) != 0 {
		t.Fatalf("sessions = %+v err=%v, want no primary sessions after runtime open failure", sessions, err)
	}
	assertNoPrimaryCreateResidue(t, hostServer, routeStore)
}

func TestSessionsV2LocalContainerRejectsWorktreeUntilDedicatedCheckpoint(t *testing.T) {
	server, _, _, routeStore, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	seedSessionsV2LocalContainerAuthority(t, server, swarmStore, "host-swarm-id", "container-swarm", "host-container-1", "binding-container-v2", "/host/swarm-go", "/workspaces/swarm-go")

	req := httptest.NewRequest(http.MethodPost, "/v2/sessions/local-containers", bytes.NewBufferString(`{"swarm_id":"container-swarm","workspace_binding_id":"binding-container-v2","title":"container v2","mode":"auto","agent_name":"swarm","worktree_mode":"on","worktree_use_current_branch":false,"worktree_base_branch":"dev","worktree_branch_name":"agent/session-v2","preference":{"provider":"codex","model":"gpt-5.4","thinking":"medium"}}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "worktree settings are not supported") {
		t.Fatalf("body = %s, want worktree checkpoint rejection", rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/v2/sessions/local-containers", bytes.NewBufferString(`{"swarm_id":"container-swarm","workspace_binding_id":"binding-container-v2","title":"container v2","mode":"auto","agent_name":"swarm","worktree_mode":"off","worktree_use_current_branch":true,"preference":{"provider":"codex","model":"gpt-5.4","thinking":"medium"}}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("use-current status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "worktree settings are not supported") {
		t.Fatalf("body = %s, want explicit worktree setting rejection", rec.Body.String())
	}
	assertNoPrimaryCreateResidue(t, server, routeStore)
}

func TestSessionsV2LocalContainerRequiresBindingID(t *testing.T) {
	server, _, _, routeStore, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	seedSessionsV2LocalContainerAuthority(t, server, swarmStore, "host-swarm-id", "container-swarm", "host-container-1", "binding-container-v2", "/host/swarm-go", "/workspaces/swarm-go")

	rec := postSessionsV2LocalContainer(t, server, `{"swarm_id":"container-swarm","title":"missing binding","mode":"auto","agent_name":"swarm","worktree_mode":"off","preference":{"provider":"codex","model":"gpt-5.4","thinking":"medium"}}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "workspace_binding_id is required") {
		t.Fatalf("body = %s, want binding required error", rec.Body.String())
	}
	assertNoPrimaryCreateResidue(t, server, routeStore)
}

func TestSessionsV2LocalContainerRejectsWorkspacePath(t *testing.T) {
	server, _, _, routeStore, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	seedSessionsV2LocalContainerAuthority(t, server, swarmStore, "host-swarm-id", "container-swarm", "host-container-1", "binding-container-v2", "/host/swarm-go", "/workspaces/swarm-go")

	rec := postSessionsV2LocalContainer(t, server, `{"swarm_id":"container-swarm","workspace_binding_id":"binding-container-v2","workspace_path":"/frontend/guessed","title":"bad path","mode":"auto","agent_name":"swarm","worktree_mode":"off","preference":{"provider":"codex","model":"gpt-5.4","thinking":"medium"}}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "workspace_path") {
		t.Fatalf("body = %s, want workspace_path rejection", rec.Body.String())
	}
	assertNoPrimaryCreateResidue(t, server, routeStore)
}

func TestSessionsV2LocalContainerValidatesRuntimePrincipalScope(t *testing.T) {
	server, _, _, routeStore, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	seedSessionsV2LocalContainerAuthority(t, server, swarmStore, "host-swarm-id", "container-swarm", "host-container-1", "binding-container-v2", "/host/swarm-go", "/workspaces/swarm-go")
	mutateSessionsV2Runtime(t, server, "container-swarm", func(runtime *pebblestore.TopologyRuntimeRecord) {
		runtime.UserID = "other-user"
	})

	rec := postSessionsV2LocalContainer(t, server, `{"swarm_id":"container-swarm","workspace_binding_id":"binding-container-v2","title":"bad runtime scope","mode":"auto","agent_name":"swarm","worktree_mode":"off","preference":{"provider":"codex","model":"gpt-5.4","thinking":"medium"}}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusForbidden, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "runtime user does not match principal") {
		t.Fatalf("body = %s, want runtime principal scope rejection", rec.Body.String())
	}
	assertNoPrimaryCreateResidue(t, server, routeStore)
}

func TestSessionsV2LocalContainerPlacementRequiresAuthorityContainerID(t *testing.T) {
	err := validateLocalContainerSessionV2Placement("container-swarm", "host-swarm-id", pebblestore.TopologyRuntimePlacementRecord{
		RuntimeSwarmID:       "container-swarm",
		AccountScopeID:       testPrincipal().AccountScopeID,
		AuthorityHostSwarmID: "host-swarm-id",
		RuntimeKind:          pebblestore.TopologyRuntimeKindContainer,
		PlacementGeneration:  1,
		State:                pebblestore.TopologyRuntimePlacementStateActive,
	})
	if err == nil || !strings.Contains(err.Error(), "authority container id is required") {
		t.Fatalf("err = %v, want authority container id rejection", err)
	}
}

func TestSessionsV2LocalContainerValidatesLivePlacement(t *testing.T) {
	cases := []struct {
		name       string
		mutate     func(*pebblestore.TopologyRuntimePlacementRecord)
		wantStatus int
		wantBody   string
	}{
		{name: "inactive placement", mutate: func(p *pebblestore.TopologyRuntimePlacementRecord) { p.State = "inactive" }, wantStatus: http.StatusConflict, wantBody: "runtime placement is not active"},
		{name: "authority host not primary", mutate: func(p *pebblestore.TopologyRuntimePlacementRecord) { p.AuthorityHostSwarmID = "other-host" }, wantStatus: http.StatusBadRequest, wantBody: "authority host must be the primary runtime"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server, _, _, routeStore, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
			seedSessionsV2LocalContainerAuthority(t, server, swarmStore, "host-swarm-id", "container-swarm", "host-container-1", "binding-container-v2", "/host/swarm-go", "/workspaces/swarm-go")
			mutateSessionsV2RuntimePlacement(t, server, "container-swarm", tc.mutate)

			rec := postSessionsV2LocalContainer(t, server, `{"swarm_id":"container-swarm","workspace_binding_id":"binding-container-v2","title":"bad placement","mode":"auto","agent_name":"swarm","worktree_mode":"off","preference":{"provider":"codex","model":"gpt-5.4","thinking":"medium"}}`)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d, body=%s", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("body = %s, want %q", rec.Body.String(), tc.wantBody)
			}
			assertNoPrimaryCreateResidue(t, server, routeStore)
		})
	}
}

func TestSessionsV2LocalContainerValidatesBindingContract(t *testing.T) {
	cases := []struct {
		name       string
		mutate     func(*pebblestore.TopologyWorkspaceBindingRecord)
		wantStatus int
		wantBody   string
	}{
		{name: "destination swarm mismatch", mutate: func(b *pebblestore.TopologyWorkspaceBindingRecord) { b.DestinationRuntimeSwarmID = "other-container" }, wantStatus: http.StatusConflict, wantBody: "does not match selected primary authority"},
		{name: "destination kind not container", mutate: func(b *pebblestore.TopologyWorkspaceBindingRecord) {
			b.DestinationRuntimeKind = pebblestore.TopologyRuntimeKindHost
		}, wantStatus: http.StatusBadRequest, wantBody: "destination runtime kind must be container"},
		{name: "destination container mismatch", mutate: func(b *pebblestore.TopologyWorkspaceBindingRecord) { b.DestinationContainerID = "other-container-id" }, wantStatus: http.StatusConflict, wantBody: "destination container id does not match placement"},
		{name: "destination path missing", mutate: func(b *pebblestore.TopologyWorkspaceBindingRecord) { b.DestinationWorkspacePath = "" }, wantStatus: http.StatusConflict, wantBody: "destination workspace path is required"},
		{name: "placement generation mismatch", mutate: func(b *pebblestore.TopologyWorkspaceBindingRecord) { b.PlacementGeneration = 2 }, wantStatus: http.StatusConflict, wantBody: "generation does not match placement"},
		{name: "attesting host mismatch", mutate: func(b *pebblestore.TopologyWorkspaceBindingRecord) { b.AttestedByHostSwarmID = "other-host" }, wantStatus: http.StatusConflict, wantBody: "attesting host does not match authority host"},
		{name: "read only", mutate: func(b *pebblestore.TopologyWorkspaceBindingRecord) {
			b.AccessMode = pebblestore.TopologyWorkspaceBindingAccessModeReadOnly
			b.Writable = false
		}, wantStatus: http.StatusForbidden, wantBody: "read_write and writable"},
		{name: "not bound", mutate: func(b *pebblestore.TopologyWorkspaceBindingRecord) { b.State = "pending" }, wantStatus: http.StatusConflict, wantBody: "workspace binding is not bound"},
		{name: "user mismatch", mutate: func(b *pebblestore.TopologyWorkspaceBindingRecord) { b.UserID = "other-user" }, wantStatus: http.StatusForbidden, wantBody: "workspace binding user does not match principal"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server, _, _, routeStore, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
			seedSessionsV2LocalContainerAuthority(t, server, swarmStore, "host-swarm-id", "container-swarm", "host-container-1", "binding-container-v2", "/host/swarm-go", "/workspaces/swarm-go")
			mutateSessionsV2WorkspaceBinding(t, server, "binding-container-v2", tc.mutate)

			rec := postSessionsV2LocalContainer(t, server, `{"swarm_id":"container-swarm","workspace_binding_id":"binding-container-v2","title":"bad binding","mode":"auto","agent_name":"swarm","worktree_mode":"off","preference":{"provider":"codex","model":"gpt-5.4","thinking":"medium"}}`)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d, body=%s", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("body = %s, want %q", rec.Body.String(), tc.wantBody)
			}
			assertNoPrimaryCreateResidue(t, server, routeStore)
		})
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
	hostServer, _, _, _, hostSwarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	runtimeServer, _, _, _, runtimeSwarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	seedSessionsV2LocalContainerAuthority(t, hostServer, hostSwarmStore, "host-swarm-id", "container-swarm", "host-container-1", "binding-container-v2", "/host/swarm-go", "/workspaces/swarm-go")
	seedRuntimeSessionsV2OpenContainerAuthority(t, runtimeServer, runtimeSwarmStore, "host-swarm-id", "container-swarm", "host-container-1", "binding-container-v2", "/host/swarm-go", "/workspaces/swarm-go")
	setTestServerLocalSwarmID(t, runtimeServer, "container-swarm")
	seedRuntimeSessionsV2Pairing(t, runtimeSwarmStore, "host-swarm-id")
	runtimeHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(context.WithValue(r.Context(), peerAuthAuthorizedContextKey, peerAuthContextValue{SwarmID: "host-swarm-id"}))
		if principal, ok := runtimeServer.trustedPairingPrincipalForPeerRequest(r); ok {
			r = withSessionsV2TestPrincipal(r, principal)
		}
		runtimeServer.Handler().ServeHTTP(w, r)
	}))
	defer runtimeHTTP.Close()
	if err := hostServer.RegisterAuthorityConnection(AuthorityConnection{AuthorityHostSwarmID: "container-swarm", AccountScopeID: testPrincipal().AccountScopeID, TransportKind: authorityConnectionTransportHTTP, TransportRef: runtimeHTTP.URL, Health: AuthorityConnectionHealthOnline}); err != nil {
		t.Fatalf("register runtime authority connection: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v2/sessions/local-containers", bytes.NewBufferString(`{"swarm_id":"container-swarm","workspace_binding_id":"binding-container-v2","title":"container v2","mode":"auto","agent_name":"swarm","worktree_mode":"off","preference":{"provider":"codex","model":"gpt-5.4","thinking":"medium"}}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	hostServer.Handler().ServeHTTP(rec, withTestPrincipal(req))

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

func postSessionsV2LocalContainer(t *testing.T, server *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v2/sessions/local-containers", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	return rec
}

func mutateSessionsV2Runtime(t *testing.T, server *Server, swarmID string, mutate func(*pebblestore.TopologyRuntimeRecord)) {
	t.Helper()
	runtimeRecord, ok, err := server.topology.GetRuntimeForAccount(testPrincipal().AccountScopeID, swarmID)
	if err != nil || !ok {
		t.Fatalf("get runtime ok=%t err=%v", ok, err)
	}
	mutate(&runtimeRecord)
	snapshot, err := server.topology.SnapshotForAccount(testPrincipal().AccountScopeID)
	if err != nil {
		t.Fatalf("snapshot topology: %v", err)
	}
	for i := range snapshot.Runtimes {
		if snapshot.Runtimes[i].SwarmID == swarmID {
			snapshot.Runtimes[i] = runtimeRecord
			if err := server.topology.ReplaceSnapshotForAccount(testPrincipal().AccountScopeID, snapshot); err != nil {
				t.Fatalf("replace runtime: %v", err)
			}
			return
		}
	}
	t.Fatalf("runtime %q missing from snapshot", swarmID)
}

func mutateSessionsV2RuntimePlacement(t *testing.T, server *Server, swarmID string, mutate func(*pebblestore.TopologyRuntimePlacementRecord)) {
	t.Helper()
	placement, ok, err := server.topology.GetRuntimePlacementForAccount(testPrincipal().AccountScopeID, swarmID)
	if err != nil || !ok {
		t.Fatalf("get runtime placement ok=%t err=%v", ok, err)
	}
	mutate(&placement)
	snapshot, err := server.topology.SnapshotForAccount(testPrincipal().AccountScopeID)
	if err != nil {
		t.Fatalf("snapshot topology: %v", err)
	}
	for i := range snapshot.RuntimePlacements {
		if snapshot.RuntimePlacements[i].RuntimeSwarmID == swarmID {
			snapshot.RuntimePlacements[i] = placement
			if err := server.topology.ReplaceSnapshotForAccount(testPrincipal().AccountScopeID, snapshot); err != nil {
				t.Fatalf("replace runtime placement: %v", err)
			}
			return
		}
	}
	t.Fatalf("runtime placement %q missing from snapshot", swarmID)
}

func mutateSessionsV2WorkspaceBinding(t *testing.T, server *Server, bindingID string, mutate func(*pebblestore.TopologyWorkspaceBindingRecord)) {
	t.Helper()
	binding, ok, err := server.topology.GetWorkspaceBindingForAccount(testPrincipal().AccountScopeID, bindingID)
	if err != nil || !ok {
		t.Fatalf("get workspace binding ok=%t err=%v", ok, err)
	}
	mutate(&binding)
	snapshot, err := server.topology.SnapshotForAccount(testPrincipal().AccountScopeID)
	if err != nil {
		t.Fatalf("snapshot topology: %v", err)
	}
	for i := range snapshot.WorkspaceBindings {
		if snapshot.WorkspaceBindings[i].BindingID == bindingID {
			snapshot.WorkspaceBindings[i] = binding
			if err := server.topology.ReplaceSnapshotForAccount(testPrincipal().AccountScopeID, snapshot); err != nil {
				t.Fatalf("replace workspace binding: %v", err)
			}
			return
		}
	}
	t.Fatalf("workspace binding %q missing from snapshot", bindingID)
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

func setTestServerLocalSwarmID(t *testing.T, server *Server, swarmID string) {
	t.Helper()
	if server == nil {
		t.Fatal("server is required")
	}
	server.SetSwarmService(fakeRoutedSwarmService{state: swarmruntime.LocalState{Node: swarmruntime.LocalNodeState{SwarmID: swarmID, Name: "test-swarm", Role: "child"}}, token: "peer-token"})
}

func withSessionsV2TestPrincipal(req *http.Request, principal identity.Principal) *http.Request {
	ctx := req.Context()
	ctx = identity.ContextWithPrincipal(ctx, principal)
	ctx = context.WithValue(ctx, productPrincipalRequestContextKey, principal)
	return req.WithContext(ctx)
}

func seedRuntimeSessionsV2Pairing(t *testing.T, swarmStore *pebblestore.SwarmStore, parentSwarmID string) {
	t.Helper()
	if swarmStore == nil {
		t.Fatal("swarm store is required")
	}
	if _, err := swarmStore.PutLocalPairing(pebblestore.SwarmLocalPairingRecord{ParentSwarmID: parentSwarmID, UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, PairingState: startupconfig.PairingStatePaired}); err != nil {
		t.Fatalf("put runtime pairing: %v", err)
	}
}

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

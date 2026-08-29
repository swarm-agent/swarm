package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

func TestBackgroundRouterSessionStartUsesMandatoryLaneWithoutRouter(t *testing.T) {
	tests := []struct {
		name          string
		planRequested bool
		wantMode      string
	}{
		{name: "auto", planRequested: false, wantMode: "auto"},
		{name: "plan", planRequested: true, wantMode: "plan"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := &sessionRouterRecordingRunner{id: "recording", err: errors.New("Router must not be called")}
			server, sessions, principal := newRoutedSessionAtomicityServer(t, runner, true, true)
			managedPath := t.TempDir()
			worktrees := &routedWorktreeServiceStub{fakeWorktreeService: fakeWorktreeService{
				config:     worktreeruntime.Config{Enabled: true, UseCurrentBranch: true, BranchName: "agent/<id>"},
				allocation: worktreeruntime.Allocation{WorkspacePath: managedPath, RepoRoot: managedPath, BranchName: "agent/background-router", WorkspaceID: "background-router"},
			}}
			server.SetWorktreeService(worktrees)

			response := postBackgroundRouterSessionRequest(t, server, principal, map[string]any{
				"input":               "implement this in the background",
				"client_request_id":   "background-router-" + test.name,
				"plan_mode_requested": test.planRequested,
			})
			if response.Code != http.StatusOK {
				t.Fatalf("background Router start status=%d body=%s", response.Code, response.Body.String())
			}
			created := decodeRoutedSessionAtomicityResponse(t, response)
			if created.StartingMode != test.wantMode {
				t.Fatalf("starting mode=%q want %q", created.StartingMode, test.wantMode)
			}
			stored, ok, err := sessions.GetSession(created.SessionID)
			if err != nil || !ok {
				t.Fatalf("background Router session exists=%t err=%v", ok, err)
			}
			if !stored.WorktreeEnabled || stored.WorktreeRootPath == "" || stored.WorktreeBranch == "" {
				t.Fatalf("background Router worktree was not canonical: %+v", stored)
			}
			if stored.Metadata["background"] != true || stored.Metadata["launch_mode"] != "background" || stored.Metadata["background_router_session"] != true || stored.Metadata["owner_transport"] != "background_router_api" {
				t.Fatalf("background Router metadata=%+v", stored.Metadata)
			}
			if stored.Metadata["routed_worktree_requested"] != true || stored.Metadata["plan_mode_requested"] != test.planRequested {
				t.Fatalf("background Router intent metadata=%+v", stored.Metadata)
			}
			if stored.Metadata["swarm_v3_workspace_binding_id"] != "routed-binding" || stored.Metadata["swarm_v3_runtime_swarm_id"] != "local-swarm" || stored.Metadata["swarm_v3_source_workspace_path"] == "" {
				t.Fatalf("background Router workspace authority=%+v", stored.Metadata)
			}
			if got := stored.Metadata["routed_start_request_hash"]; got == nil || got == "" {
				t.Fatalf("background Router session is missing routed transaction hash: %+v", stored.Metadata)
			}
			if runner.createCalls != 0 || runner.streamingCalls != 0 || worktrees.allocationCalls != 1 || worktrees.lastWorkspace == "" || worktrees.lastNameSeed == "" {
				t.Fatalf("Router calls=%d/%d worktree calls=%d source=%q seed=%q branch=%q", runner.createCalls, runner.streamingCalls, worktrees.allocationCalls, worktrees.lastWorkspace, worktrees.lastNameSeed, worktrees.lastBranchName)
			}
		})
	}
}

func TestBackgroundRouterSessionStartDoesNotConsumeOrdinaryRoutedIdentity(t *testing.T) {
	runner := &sessionRouterRecordingRunner{id: "recording", err: errors.New("Router must not be called")}
	server, _, principal := newRoutedSessionAtomicityServer(t, runner, true, true)
	managedPath := t.TempDir()
	server.SetWorktreeService(&routedWorktreeServiceStub{fakeWorktreeService: fakeWorktreeService{
		config:     worktreeruntime.Config{Enabled: true, UseCurrentBranch: true, BranchName: "agent/<id>"},
		allocation: worktreeruntime.Allocation{WorkspacePath: managedPath, RepoRoot: managedPath, BranchName: "agent/background-router", WorkspaceID: "background-router"},
	}})

	const requestID = "shared-pending-identity"
	response := postBackgroundRouterSessionRequest(t, server, principal, map[string]any{
		"input":               "do this in the background",
		"client_request_id":   requestID,
		"plan_mode_requested": false,
	})
	if response.Code != http.StatusOK {
		t.Fatalf("background Router start status=%d body=%s", response.Code, response.Body.String())
	}
	created := decodeRoutedSessionAtomicityResponse(t, response)
	ordinaryPendingID := stableSessionsV3PrimarySessionID(principal, "routed:"+requestID)
	if created.SessionID == ordinaryPendingID {
		t.Fatalf("background Router session consumed ordinary routed identity %q", ordinaryPendingID)
	}
}

func TestBackgroundRouterSessionStartToleratesRetiredWorktreeOverride(t *testing.T) {
	runner := &sessionRouterRecordingRunner{id: "recording", err: errors.New("Router must not be called")}
	server, _, principal := newRoutedSessionAtomicityServer(t, runner, true, true)
	managedPath := t.TempDir()
	server.SetWorktreeService(&routedWorktreeServiceStub{fakeWorktreeService: fakeWorktreeService{
		config: worktreeruntime.Config{Enabled: true, UseCurrentBranch: true, BranchName: "agent/<id>"},
		allocation: worktreeruntime.Allocation{WorkspacePath: managedPath, RepoRoot: managedPath, BranchName: "agent/background-router", WorkspaceID: "background-router"},
	}})
	response := postBackgroundRouterSessionRequest(t, server, principal, map[string]any{
		"input":                      "do work",
		"client_request_id":          "background-router-worktree-override",
		"plan_mode_requested":        false,
		"managed_worktree_requested": false,
	})
	if response.Code != http.StatusOK {
		t.Fatalf("retired worktree field status=%d body=%s", response.Code, response.Body.String())
	}
	if runner.createCalls != 0 {
		t.Fatalf("retired worktree field unexpectedly reached Router %d times", runner.createCalls)
	}
}

func postBackgroundRouterSessionRequest(t *testing.T, server *Server, principal identity.Principal, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	addRoutedSessionTestAuthority(body)
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("encode background Router request: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, BackgroundRouterSessionsPath, bytes.NewReader(encoded))
	request.Header.Set("Content-Type", "application/json")
	request = request.WithContext(identity.ContextWithPrincipal(request.Context(), principal))
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	return response
}

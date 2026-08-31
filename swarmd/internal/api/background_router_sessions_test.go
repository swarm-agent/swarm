package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

// Requirement: the dedicated /task transport must obtain its title and worktree
// seed from exactly one configured, tool-free Router call after replay checking.
// Threat: bypassing Router regresses sessions to "New Session", while routing
// replay or accepting client naming creates duplicate/model-spoofed authority.
// This handler-level test is the narrowest layer covering the public endpoint,
// atomic mutation, durable title metadata, allocation input, and replay behavior.
func TestBackgroundRouterSessionStartRoutesOnceBeforeMandatoryAllocationAndReplay(t *testing.T) {
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
			runner := &sessionRouterRecordingRunner{id: "recording", response: provideriface.Response{Text: `{"title":"Implement task routing","worktree_name":"task-routing"}`}}
			server, sessions, principal := newRoutedSessionAtomicityServer(t, runner, true, true)
			managedPath := t.TempDir()
			worktrees := &routedWorktreeServiceStub{fakeWorktreeService: fakeWorktreeService{
				config:     worktreeruntime.Config{Enabled: true, UseCurrentBranch: true, BranchName: "agent/<id>"},
				allocation: worktreeruntime.Allocation{WorkspacePath: managedPath, RepoRoot: managedPath, BranchName: "agent/task-routing", WorkspaceID: "background-router"},
			}}
			server.SetWorktreeService(worktrees)

			requestBody := map[string]any{
				"input":               "implement this in the background",
				"client_request_id":   "background-router-" + test.name,
				"plan_mode_requested": test.planRequested,
			}
			response := postBackgroundRouterSessionRequest(t, server, principal, requestBody)
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
			if stored.Title != "Implement task routing" || stored.Metadata["title_source"] != routedSessionTitleSourceRouter || stored.Metadata["title_locked"] != true || stored.Metadata["title_pending"] != false {
				t.Fatalf("background Router title authority=%+v metadata=%+v", stored.Title, stored.Metadata)
			}
			if !stored.WorktreeEnabled || stored.WorktreeRootPath == "" || stored.WorktreeBranch == "" || stored.WorkspacePath == stored.WorktreeRootPath {
				t.Fatalf("background Router worktree/source identity was not canonical: %+v", stored)
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
			if runner.createCalls != 1 || runner.streamingCalls != 0 || worktrees.allocationCalls != 1 || worktrees.lastWorkspace == "" || worktrees.lastNameSeed == "" || worktrees.lastBranchName != "agent/task-routing" {
				t.Fatalf("Router calls=%d/%d worktree calls=%d source=%q seed=%q branch=%q", runner.createCalls, runner.streamingCalls, worktrees.allocationCalls, worktrees.lastWorkspace, worktrees.lastNameSeed, worktrees.lastBranchName)
			}

			replay := postBackgroundRouterSessionRequest(t, server, principal, requestBody)
			if replay.Code != http.StatusOK {
				t.Fatalf("background Router replay status=%d body=%s", replay.Code, replay.Body.String())
			}
			replayed := decodeRoutedSessionAtomicityResponse(t, replay)
			if !replayed.Replayed || replayed.SessionID != stored.ID {
				t.Fatalf("background Router replay=%+v", replayed)
			}
			if runner.createCalls != 1 || worktrees.allocationCalls != 1 {
				t.Fatalf("replay repeated Router/allocation calls=%d/%d", runner.createCalls, worktrees.allocationCalls)
			}
		})
	}
}

// Requirement: Router and allocation failures publish no session, message, run,
// projection, idempotency record, or realtime authority; an allocated checkout is
// rolled back if a later mutation fails. Threat: partial /task failures can leave
// executable orphan state. The endpoint transaction is the owning boundary.
func TestBackgroundRouterSessionStartFailuresLeaveNoDurableAuthority(t *testing.T) {
	t.Run("Router failure precedes allocation", func(t *testing.T) {
		runner := &sessionRouterRecordingRunner{id: "recording", err: errors.New("Router unavailable")}
		server, sessions, principal := newRoutedSessionAtomicityServer(t, runner, true, true)
		worktrees := &routedContractRollbackWorktree{routedWorktreeServiceStub: routedWorktreeServiceStub{fakeWorktreeService: fakeWorktreeService{
			config: worktreeruntime.Config{Enabled: true, UseCurrentBranch: true, BranchName: "agent/<id>"},
		}}}
		server.SetWorktreeService(worktrees)
		const requestID = "background-router-failure"
		response := postBackgroundRouterSessionRequest(t, server, principal, map[string]any{"input": "do work", "client_request_id": requestID, "plan_mode_requested": false})
		if response.Code == http.StatusOK {
			t.Fatalf("Router failure returned success: %s", response.Body.String())
		}
		if runner.createCalls != 1 || worktrees.allocationCalls != 0 || len(worktrees.rollbacks) != 0 {
			t.Fatalf("failure ordering Router=%d allocation=%d rollback=%d", runner.createCalls, worktrees.allocationCalls, len(worktrees.rollbacks))
		}
		assertNoRoutedSessionDurableAuthority(t, sessions, principal, stableSessionsV3PrimarySessionID(principal, "background-router:"+requestID), requestID)
	})

	t.Run("allocation failure follows Router and publishes nothing", func(t *testing.T) {
		runner := &sessionRouterRecordingRunner{id: "recording", response: provideriface.Response{Text: `{"title":"Unavailable task","worktree_name":"unavailable-task"}`}}
		server, sessions, principal := newRoutedSessionAtomicityServer(t, runner, true, true)
		worktrees := &routedContractRollbackWorktree{routedWorktreeServiceStub: routedWorktreeServiceStub{
			fakeWorktreeService: fakeWorktreeService{config: worktreeruntime.Config{Enabled: true, UseCurrentBranch: true, BranchName: "agent/<id>"}},
			allocationErrs:      []error{errors.New("worktree unavailable")},
		}}
		server.SetWorktreeService(worktrees)
		const requestID = "background-router-allocation-failure"
		response := postBackgroundRouterSessionRequest(t, server, principal, map[string]any{"input": "do work", "client_request_id": requestID, "plan_mode_requested": false})
		if response.Code == http.StatusOK {
			t.Fatalf("allocation failure returned success: %s", response.Body.String())
		}
		if runner.createCalls != 1 || worktrees.allocationCalls != 1 || len(worktrees.rollbacks) != 0 {
			t.Fatalf("allocation failure Router=%d allocation=%d rollbacks=%+v", runner.createCalls, worktrees.allocationCalls, worktrees.rollbacks)
		}
		assertNoRoutedSessionDurableAuthority(t, sessions, principal, stableSessionsV3PrimarySessionID(principal, "background-router:"+requestID), requestID)
	})

	t.Run("mutation failure rolls allocated worktree back", func(t *testing.T) {
		fixture := newRoutedMediaTestFixture(t)
		managedPath := t.TempDir()
		worktrees := &routedContractRollbackWorktree{routedWorktreeServiceStub: routedWorktreeServiceStub{fakeWorktreeService: fakeWorktreeService{
			config:     worktreeruntime.Config{Enabled: true, UseCurrentBranch: true, BranchName: "agent/<id>"},
			allocation: worktreeruntime.Allocation{WorkspacePath: managedPath, BranchName: "agent/inspect-staged-image", WorkspaceID: "rollback"},
		}}}
		fixture.server.SetWorktreeService(worktrees)
		staged := fixture.stage(t, fixture.principal.AccountScopeID, "background-router-rollback")
		restore := fixture.sessions.Store().SetMediaStagingBindCommitHookForTest(func(string) error { return errors.New("injected background Router commit failure") })
		defer restore()
		const requestID = "background-router-mutation-failure"
		response := postBackgroundRouterSessionRequest(t, fixture.server, fixture.principal, map[string]any{
			"input": "inspect this image", "client_request_id": requestID, "plan_mode_requested": false,
			"workspace_binding_id": "binding-routed", "swarm_id": "local-swarm", "target_kind": "host", "target_relationship": "self",
			"media": []any{map[string]any{"staging_id": staged.ID, "modality": "image", "file_type": "png"}},
		})
		if response.Code == http.StatusOK {
			t.Fatalf("mutation failure returned success: %s", response.Body.String())
		}
		if fixture.runner.createCalls != 1 || worktrees.allocationCalls != 1 || len(worktrees.rollbacks) != 1 {
			t.Fatalf("mutation failure Router=%d allocation=%d rollbacks=%+v", fixture.runner.createCalls, worktrees.allocationCalls, worktrees.rollbacks)
		}
		assertNoRoutedSessionDurableAuthority(t, fixture.sessions, fixture.principal, stableSessionsV3PrimarySessionID(fixture.principal, "background-router:"+requestID), requestID)
	})
}

func TestBackgroundRouterSessionStartDoesNotConsumeOrdinaryRoutedIdentity(t *testing.T) {
	runner := &sessionRouterRecordingRunner{id: "recording", response: provideriface.Response{Text: `{"title":"Background task","worktree_name":"background-task"}`}}
	server, _, principal := newRoutedSessionAtomicityServer(t, runner, true, true)
	managedPath := t.TempDir()
	server.SetWorktreeService(&routedWorktreeServiceStub{fakeWorktreeService: fakeWorktreeService{
		config:     worktreeruntime.Config{Enabled: true, UseCurrentBranch: true, BranchName: "agent/<id>"},
		allocation: worktreeruntime.Allocation{WorkspacePath: managedPath, BranchName: "agent/background-task", WorkspaceID: "background-router"},
	}})

	const requestID = "shared-pending-identity"
	response := postBackgroundRouterSessionRequest(t, server, principal, map[string]any{"input": "do this in the background", "client_request_id": requestID, "plan_mode_requested": false})
	if response.Code != http.StatusOK {
		t.Fatalf("background Router start status=%d body=%s", response.Code, response.Body.String())
	}
	created := decodeRoutedSessionAtomicityResponse(t, response)
	ordinaryPendingID := stableSessionsV3PrimarySessionID(principal, "routed:"+requestID)
	if created.SessionID == ordinaryPendingID {
		t.Fatalf("background Router session consumed ordinary routed identity %q", ordinaryPendingID)
	}
}

func TestBackgroundRouterSessionStartRejectsClientOwnedRouterName(t *testing.T) {
	runner := &sessionRouterRecordingRunner{id: "recording", err: errors.New("Router must not be called for rejected input")}
	server, sessions, principal := newRoutedSessionAtomicityServer(t, runner, true, true)
	response := postBackgroundRouterSessionRequest(t, server, principal, map[string]any{
		"input": "do work", "client_request_id": "client-router-name", "plan_mode_requested": false, "worktree_name": "spoofed",
	})
	if response.Code != http.StatusBadRequest || runner.createCalls != 0 {
		t.Fatalf("client Router name status=%d calls=%d body=%s", response.Code, runner.createCalls, response.Body.String())
	}
	assertNoRoutedSessionDurableAuthority(t, sessions, principal, stableSessionsV3PrimarySessionID(principal, "background-router:client-router-name"), "client-router-name")
}

func TestBackgroundRouterSessionStartToleratesRetiredWorktreeOverride(t *testing.T) {
	runner := &sessionRouterRecordingRunner{id: "recording", response: provideriface.Response{Text: `{"title":"Do work","worktree_name":"do-work"}`}}
	server, _, principal := newRoutedSessionAtomicityServer(t, runner, true, true)
	managedPath := t.TempDir()
	server.SetWorktreeService(&routedWorktreeServiceStub{fakeWorktreeService: fakeWorktreeService{
		config:     worktreeruntime.Config{Enabled: true, UseCurrentBranch: true, BranchName: "agent/<id>"},
		allocation: worktreeruntime.Allocation{WorkspacePath: managedPath, BranchName: "agent/do-work", WorkspaceID: "background-router"},
	}})
	response := postBackgroundRouterSessionRequest(t, server, principal, map[string]any{
		"input": "do work", "client_request_id": "background-router-worktree-override", "plan_mode_requested": false, "managed_worktree_requested": false,
	})
	if response.Code != http.StatusOK {
		t.Fatalf("retired worktree field status=%d body=%s", response.Code, response.Body.String())
	}
	if runner.createCalls != 1 {
		t.Fatalf("retired worktree field Router calls=%d, want one", runner.createCalls)
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

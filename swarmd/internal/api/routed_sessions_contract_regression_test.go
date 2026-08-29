package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/agentmodelsettings"
	"swarm/packages/swarmd/internal/identity"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

// TestRoutedSessionContractRegression protects the routed-start boundary as one
// server-owned transaction: the client supplies intent and canonical workspace
// authority, while every Git-backed routed session is admitted into an owned lane.
func TestRoutedSessionContractRegression(t *testing.T) {
	t.Run("client cannot supply pre-session route authority", func(t *testing.T) {
		runner := &sessionRouterRecordingRunner{id: "recording", response: provideriface.Response{Text: `{"title":"Router title"}`}}
		server, _, principal := newRoutedSessionAtomicityServer(t, runner, false, true)

		for _, forbidden := range []string{
			`"title":"Client title"`,
			`"mode":"plan"`,
			`"preference":{"provider":"client","model":"client-model","thinking":"high"}`,
			`"model_profile":{"action":{"provider":"client","model":"client-model"}}`,
		} {
			body := `{"input":"route this","client_request_id":"forbidden-authority","plan_mode_requested":false,` + forbidden + `}`
			response := postRoutedSessionRawContractRequest(t, server, principal, body)
			if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "unknown field") {
				t.Fatalf("forbidden authority %s status=%d body=%s", forbidden, response.Code, response.Body.String())
			}
		}
		if runner.createCalls != 0 || runner.streamingCalls != 0 {
			t.Fatalf("client route authority reached Router: create=%d streaming=%d", runner.createCalls, runner.streamingCalls)
		}
		missingPlan := postRoutedSessionRawContractRequest(t, server, principal, `{"input":"route this","client_request_id":"missing-plan"}`)
		if missingPlan.Code != http.StatusBadRequest || !strings.Contains(missingPlan.Body.String(), "plan_mode_requested is required") {
			t.Fatalf("missing Plan intent status=%d body=%s", missingPlan.Code, missingPlan.Body.String())
		}
		foreignBinding := postRoutedSessionRawContractRequest(t, server, principal, `{"input":"route this","client_request_id":"foreign-binding","plan_mode_requested":false,"workspace_binding_id":"foreign-binding"}`)
		if foreignBinding.Code != http.StatusBadRequest || !strings.Contains(foreignBinding.Body.String(), "was not found") {
			t.Fatalf("foreign binding status=%d body=%s", foreignBinding.Code, foreignBinding.Body.String())
		}
		mismatchedPath := postRoutedSessionRawContractRequest(t, server, principal, `{"input":"route this","client_request_id":"mismatched-path","plan_mode_requested":false,"workspace_path":"/other/workspace"}`)
		if mismatchedPath.Code != http.StatusBadRequest || !strings.Contains(mismatchedPath.Body.String(), "does not match workspace binding source") {
			t.Fatalf("mismatched path status=%d body=%s", mismatchedPath.Code, mismatchedPath.Body.String())
		}
		if runner.createCalls != 0 || runner.streamingCalls != 0 {
			t.Fatalf("invalid selected authority reached Router: create=%d streaming=%d", runner.createCalls, runner.streamingCalls)
		}
	})

	t.Run("plain start asynchronously commits Compact title and realtime", func(t *testing.T) {
		routerRunner := &sessionRouterRecordingRunner{id: "router", err: errors.New("Router unavailable")}
		server, sessions, principal := newRoutedSessionAtomicityServer(t, routerRunner, false, true)
		compactRunner := &sessionRouterRecordingRunner{id: "compact", response: provideriface.Response{Text: "Durable Compact Title"}, allowStreaming: true}
		server.providers.RegisterRunner(compactRunner)
		if _, err := server.agentModelSettings.UpdateSystemAgent(identity.ContextWithPrincipal(context.Background(), principal), "compact", agentmodelsettings.Assignment{Provider: "compact", Model: "compact-model", Thinking: "low"}); err != nil {
			t.Fatalf("configure Compact title settings: %v", err)
		}
		server.v3SessionExecutor = newSessionV3Executor(server)
		t.Cleanup(func() {
			server.CancelInFlightRuns()
			waitForRoutedSessionExecutorIdle(t, server.v3SessionExecutor, 2*time.Second)
		})
		for index, candidate := range []struct{ path, name, definition string }{
			{path: t.TempDir(), name: "Distractor Alpha", definition: "confidential alpha routing definition"},
			{path: t.TempDir(), name: "Distractor Beta", definition: "confidential beta routing definition"},
		} {
			resolution, err := server.workspace.AddForPrincipal(principal, candidate.path, candidate.name, "", false)
			if err != nil {
				t.Fatalf("add distractor workspace %d: %v", index, err)
			}
			pending, err := server.workspace.MarkDefinitionPendingForPrincipal(principal, resolution.WorkspacePath)
			if err != nil {
				t.Fatalf("mark distractor definition pending %d: %v", index, err)
			}
			if _, current, err := server.workspace.CompleteDefinitionForPrincipal(principal, resolution.WorkspacePath, pending.DefinitionGeneration, candidate.definition, 1); err != nil || !current {
				t.Fatalf("complete distractor definition %d current=%t err=%v", index, current, err)
			}
		}
		created := postRoutedSessionAtomicityRequest(t, server, principal, map[string]any{
			"input": "name this routed session", "client_request_id": "plain-compact-title",
		})
		if created.Code != http.StatusOK {
			t.Fatalf("plain routed start status=%d body=%s", created.Code, created.Body.String())
		}
		result := decodeRoutedSessionAtomicityResponse(t, created)
		if result.Session.WorkspaceName != "Routed Workspace" || result.Session.Metadata["swarm_v3_workspace_binding_id"] != "routed-binding" || result.Session.Metadata["swarm_v3_runtime_swarm_id"] != "local-swarm" {
			t.Fatalf("plain start did not retain selected binding authority: %+v", result.Session)
		}
		if !result.Session.WorktreeEnabled || result.Session.WorktreeRootPath == "" || result.Session.WorktreeBranch == "" || len(result.Session.WorkspaceGrants) < 4 || len(result.Session.WorkspaceUsage) < 3 {
			t.Fatalf("plain start did not allocate a session-owned worktree with all account workspaces: %+v", result.Session)
		}
		if result.Session.Metadata["swarm_v3_mandatory_worktree"] != true || result.Session.Metadata["swarm_v3_worktree_owner_session_id"] != result.Session.ID || result.Session.Metadata["swarm_v3_runtime_workspace_path"] != result.Session.WorktreeRootPath {
			t.Fatalf("plain start did not persist mandatory worktree lineage: %+v", result.Session.Metadata)
		}
		if result.Mutation.RunIntent == nil || result.Mutation.RunIntent.Status != "pending_executor" {
			t.Fatalf("plain start did not enqueue its run independently of Router: %+v", result.Mutation.RunIntent)
		}
		deadline := time.Now().Add(2 * time.Second)
		for {
			stored, ok, err := sessions.GetSession(result.SessionID)
			if err != nil || !ok {
				t.Fatalf("plain routed session exists=%t err=%v", ok, err)
			}
			if stored.Title == "Durable Compact Title" {
				if stored.Metadata["title_source"] != "compact" || stored.Metadata["title_locked"] != true || stored.Metadata["title_pending"] != false {
					t.Fatalf("Compact title metadata=%+v", stored.Metadata)
				}
				events, eventErr := sessions.ListSessionEvents(result.SessionID, 0, 10)
				outbox, outboxErr := sessions.Store().ListV3RealtimeOutboxForSessionAfterSeq(result.SessionID, 0, 10)
				if eventErr != nil || outboxErr != nil {
					t.Fatalf("read Compact title events/outbox: %v/%v", eventErr, outboxErr)
				}
				foundTitleEvent := false
				for _, event := range events {
					if event.EventType == "session.title.updated" {
						foundTitleEvent = true
					}
				}
				if len(events) < 2 || len(outbox) < 2 || !foundTitleEvent {
					t.Fatalf("Compact durable title events=%+v outbox=%+v errors=%v/%v", events, outbox, eventErr, outboxErr)
				}
				foundTitleOutbox := false
				for _, record := range outbox {
					if record.Event.EventType == "session.title.updated" {
						foundTitleOutbox = true
					}
				}
				if !foundTitleOutbox {
					t.Fatalf("Compact title realtime outbox missing: %+v", outbox)
				}
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("Compact title did not commit; session=%+v streaming_calls=%d", stored, compactRunner.streamingCalls)
			}
			time.Sleep(10 * time.Millisecond)
		}
		if routerRunner.createCalls != 0 || routerRunner.streamingCalls != 0 || compactRunner.streamingCalls != 1 {
			t.Fatalf("provider calls Router=%d/%d Compact streaming=%d", routerRunner.createCalls, routerRunner.streamingCalls, compactRunner.streamingCalls)
		}
		if len(compactRunner.requests) != 1 {
			t.Fatalf("Compact title requests=%d, want one", len(compactRunner.requests))
		}
		compactInstructions := compactRunner.requests[0].Instructions
		for _, forbidden := range []string{"Routed Workspace", "routed-binding", "confidential alpha routing definition", "confidential beta routing definition", "Distractor Alpha", "Distractor Beta"} {
			if strings.Contains(compactInstructions, forbidden) {
				t.Fatalf("Compact title instructions leaked workspace authority %q: %s", forbidden, compactInstructions)
			}
		}
		server.CancelInFlightRuns()
		waitForRoutedSessionExecutorIdle(t, server.v3SessionExecutor, 2*time.Second)
	})

	t.Run("explicit Plan intent owns mode when worktree isolation is explicitly requested", func(t *testing.T) {
		tests := []struct {
			name          string
			planRequested bool
			wantMode      string
		}{
			{name: "Auto", planRequested: false, wantMode: "auto"},
			{name: "Plan", planRequested: true, wantMode: "plan"},
		}
		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				runner := &sessionRouterRecordingRunner{id: "recording", err: errors.New("Router must not be called")}
				server, sessions, principal := newRoutedSessionAtomicityServer(t, runner, true, true)
				managedPath := t.TempDir()
				worktrees := &routedWorktreeServiceStub{fakeWorktreeService: fakeWorktreeService{
					config:     worktreeruntime.Config{Enabled: true, UseCurrentBranch: true, BranchName: "agent/<id>"},
					allocation: worktreeruntime.Allocation{WorkspacePath: managedPath, RepoRoot: managedPath, BranchName: "agent/router-canonical", WorkspaceID: "router-canonical"},
				}}
				server.SetWorktreeService(worktrees)
				created := postRoutedSessionAtomicityRequest(t, server, principal, map[string]any{
					"input": "implement this work", "client_request_id": "router-canonical-" + strings.ToLower(test.name), "plan_mode_requested": test.planRequested, "worktree_name": "router-canonical",
				})
				if created.Code != http.StatusOK {
					t.Fatalf("canonical routed start status=%d body=%s", created.Code, created.Body.String())
				}
				response := decodeRoutedSessionAtomicityResponse(t, created)
				if response.StartingMode != test.wantMode {
					t.Fatalf("starting_mode=%q want %q", response.StartingMode, test.wantMode)
				}
				stored, ok, err := sessions.GetSession(response.SessionID)
				if err != nil || !ok {
					t.Fatalf("canonical routed session exists=%t err=%v", ok, err)
				}
				if stored.Title != sessionV3TitleDefault || stored.Mode != test.wantMode || stored.WorkspaceName != "Routed Workspace" || stored.WorkspacePath == "" {
					t.Fatalf("routed authority was not canonical: %+v", stored)
				}
				if stored.Metadata["title_locked"] != false || stored.Metadata["title_pending"] != true || stored.Metadata["swarm_v3_workspace_binding_id"] != "routed-binding" || stored.Metadata["swarm_v3_runtime_swarm_id"] != "local-swarm" {
					t.Fatalf("managed worktree title/runtime metadata=%+v", stored.Metadata)
				}
				if runner.createCalls != 0 || runner.streamingCalls != 0 || worktrees.allocationCalls != 1 || worktrees.lastWorkspace == "" || worktrees.lastNameSeed == "" || worktrees.lastBranchName != "agent/router-canonical" {
					t.Fatalf("Router calls create=%d streaming=%d allocation=%d source=%q seed=%q branch=%q", runner.createCalls, runner.streamingCalls, worktrees.allocationCalls, worktrees.lastWorkspace, worktrees.lastNameSeed, worktrees.lastBranchName)
				}
			})
		}
	})

	t.Run("media commit failure rolls back the allocated worktree and cannot replay", func(t *testing.T) {
		fixture := newRoutedMediaTestFixture(t)
		fixture.runner.response.Text = `{"title":"Worktree contract"}`
		managedPath := t.TempDir()
		worktrees := &routedContractRollbackWorktree{routedWorktreeServiceStub: routedWorktreeServiceStub{fakeWorktreeService: fakeWorktreeService{
			config:     worktreeruntime.Config{Enabled: true, UseCurrentBranch: false, BaseBranch: "dev", BranchName: "agent/<id>"},
			allocation: worktreeruntime.Allocation{WorkspacePath: managedPath, RepoRoot: managedPath, BaseBranch: "dev", BranchName: "agent/contract-worktree", WorkspaceID: "contract-worktree"},
		}}}
		fixture.server.SetWorktreeService(worktrees)
		staged := fixture.stage(t, fixture.principal.AccountScopeID, "contract-rollback")
		restore := fixture.sessions.Store().SetMediaStagingBindCommitHookForTest(func(string) error {
			return errors.New("injected routed contract commit failure")
		})
		defer restore()

		response := fixture.postWithWorktreeIntent(t, fixture.principal.AccountScopeID, "contract-rollback", staged.ID, map[string]string{"modality": "image", "file_type": "png"}, true)
		if response.Code == http.StatusOK || !strings.Contains(response.Body.String(), "injected routed contract commit failure") {
			t.Fatalf("failed routed transaction status=%d body=%s", response.Code, response.Body.String())
		}
		if len(worktrees.rollbacks) != 1 || worktrees.rollbacks[0].WorkspacePath == "" {
			t.Fatalf("worktree rollbacks=%+v", worktrees.rollbacks)
		}
		fixture.assertNoRoutedSession(t, "contract-rollback")
		assertRoutedMediaStagingState(t, fixture, fixture.principal.AccountScopeID, staged.ID, pebblestore.MediaStagingStateDeleted)
		if fixture.runner.createCalls != 0 {
			t.Fatalf("Router calls=%d, want zero", fixture.runner.createCalls)
		}

		replay := fixture.postWithWorktreeIntent(t, fixture.principal.AccountScopeID, "contract-rollback", staged.ID, map[string]string{"modality": "image", "file_type": "png"}, true)
		if replay.Code == http.StatusOK {
			t.Fatalf("failed routed authority replayed as success: %s", replay.Body.String())
		}
		fixture.assertNoRoutedSession(t, "contract-rollback")
	})
}

type routedContractRollbackWorktree struct {
	routedWorktreeServiceStub
	rollbacks []worktreeruntime.Allocation
}

func (s *routedContractRollbackWorktree) RollbackAllocation(allocation worktreeruntime.Allocation) error {
	s.rollbacks = append(s.rollbacks, allocation)
	return nil
}

func waitForRoutedSessionExecutorIdle(t *testing.T, executor *sessionV3Executor, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if executor == nil {
			return
		}
		executor.mu.Lock()
		inFlight := len(executor.inFlightRuns)
		executor.mu.Unlock()
		if inFlight == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("routed session executor did not become idle; in_flight=%d", inFlight)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func postRoutedSessionRawContractRequest(t *testing.T, server *Server, principal identity.Principal, body string) *httptest.ResponseRecorder {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("decode routed contract request: %v", err)
	}
	addRoutedSessionTestAuthority(payload)
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("encode routed contract request: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, RoutedSessionsPath, strings.NewReader(string(encoded)))
	request = request.WithContext(identity.ContextWithPrincipal(request.Context(), principal))
	response := httptest.NewRecorder()
	server.handleRoutedSessionStart(response, request)
	return response
}

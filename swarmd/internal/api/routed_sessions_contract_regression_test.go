package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

// TestRoutedSessionContractRegression protects the routed-start boundary as one
// server-owned transaction: the client supplies intent, the Router supplies the
// route, and failed media/worktree preparation leaves no replayable authority.
func TestRoutedSessionContractRegression(t *testing.T) {
	t.Run("client cannot supply pre-session route authority", func(t *testing.T) {
		runner := &sessionRouterRecordingRunner{id: "recording", response: provideriface.Response{Text: `{"title":"Router title","mode":"auto"}`}}
		server, _, principal := newRoutedSessionAtomicityServer(t, runner, false, true)

		for _, forbidden := range []string{
			`"title":"Client title"`,
			`"mode":"plan"`,
			`"workspace_path":"/client/workspace"`,
			`"preference":{"provider":"client","model":"client-model","thinking":"high"}`,
			`"model_profile":{"action":{"provider":"client","model":"client-model"}}`,
		} {
			body := `{"input":"route this","client_request_id":"forbidden-authority","managed_worktree_requested":false,` + forbidden + `}`
			response := postRoutedSessionRawContractRequest(t, server, principal, body)
			if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "unknown field") {
				t.Fatalf("forbidden authority %s status=%d body=%s", forbidden, response.Code, response.Body.String())
			}
		}
		if runner.createCalls != 0 || runner.streamingCalls != 0 {
			t.Fatalf("client route authority reached Router: create=%d streaming=%d", runner.createCalls, runner.streamingCalls)
		}
	})

	t.Run("Router owns title mode and workspace while disabled Plan fails closed", func(t *testing.T) {
		planRunner := &sessionRouterRecordingRunner{id: "recording", response: provideriface.Response{Text: `{"title":"Router Plan","mode":"plan"}`}}
		planServer, planSessions, principal := newRoutedSessionAtomicityServer(t, planRunner, false, true)
		const deniedRequestID = "router-plan-disabled-contract"
		denied := postRoutedSessionAtomicityRequest(t, planServer, principal, map[string]any{
			"input": "plan this work", "client_request_id": deniedRequestID,
		})
		if denied.Code != http.StatusBadRequest || !strings.Contains(denied.Body.String(), "was not advertised") {
			t.Fatalf("disabled Plan status=%d body=%s", denied.Code, denied.Body.String())
		}
		assertNoRoutedSessionDurableAuthority(t, planSessions, principal, stableSessionsV3PrimarySessionID(principal, "routed:"+deniedRequestID), deniedRequestID)

		autoRunner := &sessionRouterRecordingRunner{id: "recording", response: provideriface.Response{Text: `{"title":"Router Canonical Title","mode":"auto"}`}}
		autoServer, autoSessions, autoPrincipal := newRoutedSessionAtomicityServer(t, autoRunner, false, true)
		created := postRoutedSessionAtomicityRequest(t, autoServer, autoPrincipal, map[string]any{
			"input": "implement this work", "client_request_id": "router-canonical-contract",
		})
		if created.Code != http.StatusOK {
			t.Fatalf("canonical routed start status=%d body=%s", created.Code, created.Body.String())
		}
		response := decodeRoutedSessionAtomicityResponse(t, created)
		stored, ok, err := autoSessions.GetSession(response.SessionID)
		if err != nil || !ok {
			t.Fatalf("canonical routed session exists=%t err=%v", ok, err)
		}
		if stored.Title != "Router Canonical Title" || stored.Mode != "auto" || stored.WorkspaceName != "Routed Workspace" || stored.WorkspacePath == "" {
			t.Fatalf("Router authority was not canonical: %+v", stored)
		}
		if autoRunner.createCalls != 1 || autoRunner.streamingCalls != 0 {
			t.Fatalf("Router calls create=%d streaming=%d", autoRunner.createCalls, autoRunner.streamingCalls)
		}
	})

	t.Run("media commit failure rolls back the allocated worktree and cannot replay", func(t *testing.T) {
		fixture := newRoutedMediaTestFixture(t)
		name := "Contract Worktree"
		fixture.runner.response.Text = `{"title":"Worktree contract","mode":"auto","worktree":true,"worktree_name":"` + name + `"}`
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
		if fixture.runner.createCalls != 1 {
			t.Fatalf("Router calls=%d, want one", fixture.runner.createCalls)
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

func postRoutedSessionRawContractRequest(t *testing.T, server *Server, principal identity.Principal, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, RoutedSessionsPath, strings.NewReader(body))
	request = request.WithContext(identity.ContextWithPrincipal(request.Context(), principal))
	response := httptest.NewRecorder()
	server.handleRoutedSessionStart(response, request)
	return response
}

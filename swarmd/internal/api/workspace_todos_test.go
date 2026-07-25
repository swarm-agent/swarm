package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/todo"
)

type recordingAITaskEnqueuer struct {
	items []pebblestore.WorkspaceTodoItem
}

func (q *recordingAITaskEnqueuer) Enqueue(item pebblestore.WorkspaceTodoItem) bool {
	q.items = append(q.items, item)
	return true
}

type rejectingAITaskEnqueuer struct{}

func (rejectingAITaskEnqueuer) Enqueue(pebblestore.WorkspaceTodoItem) bool { return false }

func TestWorkspaceTodosRestoresScopedRetrievalAndIdempotentDirectEnqueue(t *testing.T) {
	server, workspacePath, store := newWorkspaceOverviewTopologyTestServer(t)
	server.SetTodoService(todo.NewService(pebblestore.NewWorkspaceTodoStore(store), nil, nil, server.sessions))
	queue := &recordingAITaskEnqueuer{}
	server.SetAITaskEnqueuer(queue)

	body := func(origin, mode string) *bytes.Reader {
		raw, err := json.Marshal(map[string]any{"action": "ai_task", "workspace_path": workspacePath, "owner_kind": "user", "text": "repair task API", "origin_session_id": origin, "mode": mode})
		if err != nil {
			t.Fatalf("marshal request: %v", err)
		}
		return bytes.NewReader(raw)
	}
	submit := func(origin, mode, key string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		request := withTestPrincipal(httptest.NewRequest(http.MethodPost, "/v1/workspace/todos", body(origin, mode)))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Idempotency-Key", key)
		server.Handler().ServeHTTP(recorder, request)
		return recorder
	}

	first := submit("", "", "stable-task-key")
	if first.Code != http.StatusAccepted {
		t.Fatalf("first status=%d body=%s", first.Code, first.Body.String())
	}
	second := submit("", "auto", "stable-task-key")
	if second.Code != http.StatusAccepted {
		t.Fatalf("replay status=%d body=%s", second.Code, second.Body.String())
	}
	if len(queue.items) != 1 || queue.items[0].AIMode != "auto" {
		t.Fatalf("enqueued jobs=%#v, want one auto task", queue.items)
	}
	plan := submit("", "plan", "plan-task-key")
	if plan.Code != http.StatusAccepted || len(queue.items) != 2 || queue.items[1].AIMode != "plan" {
		t.Fatalf("plan acceptance status=%d queue=%#v body=%s", plan.Code, queue.items, plan.Body.String())
	}
	invalid := submit("", "manual", "invalid-mode-key")
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid mode status=%d body=%s", invalid.Code, invalid.Body.String())
	}

	list := httptest.NewRecorder()
	request := withTestPrincipal(httptest.NewRequest(http.MethodGet, "/v1/workspace/todos?workspace_path="+workspacePath+"&owner_kind=user", nil))
	server.Handler().ServeHTTP(list, request)
	if list.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", list.Code, list.Body.String())
	}
	var response struct {
		Items []pebblestore.WorkspaceTodoItem `json:"items"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(response.Items) != 2 || response.Items[0].ID != queue.items[0].ID || response.Items[1].ID != queue.items[1].ID {
		t.Fatalf("listed items=%#v", response.Items)
	}
}

func TestWorkspaceTodosAcceptsWorktreeOriginForCanonicalWorkspaceTask(t *testing.T) {
	server, workspacePath, store := newWorkspaceOverviewTopologyTestServer(t)
	server.SetTodoService(todo.NewService(pebblestore.NewWorkspaceTodoStore(store), nil, nil, server.sessions))
	queue := &recordingAITaskEnqueuer{}
	server.SetAITaskEnqueuer(queue)

	workspaceScope, err := server.workspace.ScopeForPathForPrincipal(testPrincipal(), workspacePath)
	if err != nil || !workspaceScope.Matched || workspaceScope.WorkspaceID == "" {
		t.Fatalf("resolve canonical workspace: scope=%#v err=%v", workspaceScope, err)
	}
	worktreePath := t.TempDir()
	const (
		runtimeID = "runtime-worktree-origin"
		bindingID = "binding-worktree-origin"
	)
	if _, err := server.topology.PutRuntimeForAccount(testPrincipal().AccountScopeID, pebblestore.TopologyRuntimeRecord{SwarmID: runtimeID, UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, Name: runtimeID}); err != nil {
		t.Fatalf("put origin runtime: %v", err)
	}
	if _, err := server.topology.PutRuntimePlacementForAccount(testPrincipal().AccountScopeID, pebblestore.TopologyRuntimePlacementRecord{RuntimeSwarmID: runtimeID, AccountScopeID: testPrincipal().AccountScopeID, AuthorityHostSwarmID: runtimeID, RuntimeKind: pebblestore.TopologyRuntimeKindHost, PlacementGeneration: 1, State: pebblestore.TopologyRuntimePlacementStateActive}); err != nil {
		t.Fatalf("put origin placement: %v", err)
	}
	if _, err := server.topology.PutWorkspaceBindingForAccount(testPrincipal().AccountScopeID, pebblestore.TopologyWorkspaceBindingRecord{
		BindingID: bindingID, UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID,
		SourceWorkspaceID: workspaceScope.WorkspaceID, SourceWorkspaceGeneration: workspaceScope.WorkspaceGeneration, SourceWorkspacePath: workspacePath, SourceWorkspaceName: workspaceScope.WorkspaceName,
		DestinationRuntimeSwarmID: runtimeID, DestinationAuthorityHostSwarmID: runtimeID, DestinationRuntimeKind: pebblestore.TopologyRuntimeKindHost, DestinationHostSwarmID: runtimeID, DestinationWorkspacePath: workspacePath,
		PlacementGeneration: 1, BindingGeneration: 1, State: pebblestore.TopologyWorkspaceBindingStateBound, AccessMode: pebblestore.TopologyWorkspaceBindingAccessModeReadWrite,
		MaterializationKind: pebblestore.TopologyWorkspaceBindingMaterializationSource, AttestedByHostSwarmID: runtimeID, Writable: true,
	}); err != nil {
		t.Fatalf("put origin workspace binding: %v", err)
	}
	origin, _, err := server.sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		UserID:         testPrincipal().UserID,
		AccountScopeID: testPrincipal().AccountScopeID,
		Title:          "Managed worktree origin",
		WorkspacePath:  worktreePath,
		WorkspaceName:  "managed-worktree",
		Mode:           sessionruntime.ModeAuto,
		Worktree: &sessionruntime.CreateSessionWorktree{
			RootPath: worktreePath, BaseBranch: "dev", BranchName: "agent/origin-worktree", WorkspaceID: "worktree-origin",
		},
		Metadata: map[string]any{
			"swarm_v3_workspace_binding_id": bindingID,
			"local_workspace_binding_id":    bindingID,
		},
	})
	if err != nil {
		t.Fatalf("create worktree origin session: %v", err)
	}

	raw, _ := json.Marshal(map[string]any{
		"action": "ai_task", "workspace_path": worktreePath, "owner_kind": "user",
		"text": "launch from canonical workspace", "origin_session_id": origin.ID, "mode": "auto",
	})
	recorder := httptest.NewRecorder()
	request := withTestPrincipal(httptest.NewRequest(http.MethodPost, "/v1/workspace/todos", bytes.NewReader(raw)))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "worktree-origin-task")
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("worktree origin status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if len(queue.items) != 1 {
		t.Fatalf("enqueued jobs=%#v", queue.items)
	}
	queued := queue.items[0]
	if queued.WorkspacePath != workspacePath || queued.WorkspaceID != workspaceScope.WorkspaceID || queued.OriginSessionID != origin.ID {
		t.Fatalf("queued canonical task=%#v", queued)
	}
	if queued.WorkspacePath == worktreePath {
		t.Fatalf("queued task was incorrectly routed to origin worktree %q", worktreePath)
	}

	otherWorkspacePath := t.TempDir()
	if _, err := server.workspace.AddForPrincipal(testPrincipal(), otherWorkspacePath, "other-workspace", "", true); err != nil {
		t.Fatalf("add unrelated workspace: %v", err)
	}
	unrelatedOrigin, _, err := server.sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		UserID:         testPrincipal().UserID,
		AccountScopeID: testPrincipal().AccountScopeID,
		Title:          "Unrelated origin",
		WorkspacePath:  otherWorkspacePath,
		WorkspaceName:  "other-workspace",
		Mode:           sessionruntime.ModeAuto,
	})
	if err != nil {
		t.Fatalf("create unrelated origin session: %v", err)
	}
	unrelatedRaw, _ := json.Marshal(map[string]any{
		"action": "ai_task", "workspace_path": workspacePath, "owner_kind": "user",
		"text": "reject unrelated origin", "origin_session_id": unrelatedOrigin.ID, "mode": "auto",
	})
	unrelatedRecorder := httptest.NewRecorder()
	unrelatedRequest := withTestPrincipal(httptest.NewRequest(http.MethodPost, "/v1/workspace/todos", bytes.NewReader(unrelatedRaw)))
	unrelatedRequest.Header.Set("Content-Type", "application/json")
	unrelatedRequest.Header.Set("Idempotency-Key", "unrelated-origin-task")
	server.Handler().ServeHTTP(unrelatedRecorder, unrelatedRequest)
	if unrelatedRecorder.Code != http.StatusBadRequest || len(queue.items) != 1 {
		t.Fatalf("unrelated origin status=%d queue=%#v body=%s", unrelatedRecorder.Code, queue.items, unrelatedRecorder.Body.String())
	}
}

func TestWorkspaceTodosReportsQueueSaturationWithoutLosingDurableAcceptedTask(t *testing.T) {
	server, workspacePath, store := newWorkspaceOverviewTopologyTestServer(t)
	server.SetTodoService(todo.NewService(pebblestore.NewWorkspaceTodoStore(store), nil, nil, server.sessions))
	server.SetAITaskEnqueuer(rejectingAITaskEnqueuer{})

	raw, _ := json.Marshal(map[string]any{"action": "ai_task", "workspace_path": workspacePath, "owner_kind": "user", "text": "overflow task"})
	recorder := httptest.NewRecorder()
	request := withTestPrincipal(httptest.NewRequest(http.MethodPost, "/v1/workspace/todos", bytes.NewReader(raw)))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "overflow-key")
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("overflow status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	list := httptest.NewRecorder()
	server.Handler().ServeHTTP(list, withTestPrincipal(httptest.NewRequest(http.MethodGet, "/v1/workspace/todos?workspace_path="+workspacePath+"&owner_kind=user", nil)))
	if list.Code != http.StatusOK {
		t.Fatalf("list after overflow status=%d body=%s", list.Code, list.Body.String())
	}
	var response struct {
		Items []pebblestore.WorkspaceTodoItem `json:"items"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &response); err != nil || len(response.Items) != 1 || response.Items[0].AIState != pebblestore.WorkspaceTodoAIStateFailed || response.Items[0].AIError == "" {
		t.Fatalf("durable terminal task after overflow: items=%#v err=%v", response.Items, err)
	}
}

func TestWorkspaceTodosRejectsUnauthorizedWorkspaceAndOrigin(t *testing.T) {
	server, workspacePath, store := newWorkspaceOverviewTopologyTestServer(t)
	server.SetTodoService(todo.NewService(pebblestore.NewWorkspaceTodoStore(store), nil, nil, server.sessions))
	server.SetAITaskEnqueuer(&recordingAITaskEnqueuer{})

	for name, payload := range map[string]map[string]any{
		"workspace": {"action": "ai_task", "workspace_path": t.TempDir(), "owner_kind": "user", "text": "x"},
		"origin":    {"action": "ai_task", "workspace_path": workspacePath, "owner_kind": "user", "text": "x", "origin_session_id": "missing"},
	} {
		t.Run(name, func(t *testing.T) {
			raw, _ := json.Marshal(payload)
			recorder := httptest.NewRecorder()
			request := withTestPrincipal(httptest.NewRequest(http.MethodPost, "/v1/workspace/todos", bytes.NewReader(raw)))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Idempotency-Key", "key-"+name)
			server.Handler().ServeHTTP(recorder, request)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

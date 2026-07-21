package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

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

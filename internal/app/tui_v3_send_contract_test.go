package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/model"
	"swarm-refactor/swarmtui/internal/ui"
)

func TestSendTUIV3ChatMessageWaitsForRealtimeBeforePOST(t *testing.T) {
	resumeGate := make(chan struct{})
	messagePosted := make(chan struct{})
	postBody := make(chan map[string]any, 1)
	worksetRequested := make(chan struct{}, 1)
	var orderMu sync.Mutex
	var callOrder []string
	recordOrder := func(step string) {
		orderMu.Lock()
		defer orderMu.Unlock()
		callOrder = append(callOrder, step)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v3/tui/sessions:workset":
			recordOrder("workset bootstrap")
			worksetRequested <- struct{}{}
			_ = json.NewEncoder(w).Encode(client.SessionV3Workset{
				OK:                     true,
				SnapshotEndpointCursor: "cursor-workset",
				SessionsByID:           map[string]client.SessionSummary{"session-1": {ID: "session-1", WorkspacePath: testWorkspacePath, Title: "Native V3", SessionAPI: "v3"}},
				ProjectionsBySession:   map[string]client.SessionV3Projection{"session-1": {SessionID: "session-1", LastEventSeq: 1}},
				SessionOrder:           []string{"session-1"},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/v3/sessions/session-1/messages":
			recordOrder("message post")
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode message POST body: %v", err)
			} else {
				postBody <- body
			}
			close(messagePosted)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":         true,
				"session":    map[string]any{"id": "session-1", "workspace_path": testWorkspacePath, "workspace_name": "swarm-go", "title": "Native V3", "mode": "auto"},
				"projection": map[string]any{"session_id": "session-1", "last_event_seq": 1, "projection_high_watermark_seq": 1},
				"message":    map[string]any{"id": "msg-user", "session_id": "session-1", "global_seq": 1, "role": "user", "content": "hello v3", "created_at": time.Now().UnixMilli()},
				"run_intent": map[string]any{"session_id": "session-1", "run_id": "run-1", "status": "pending_executor", "event_seq": 1, "created_at": time.Now().UnixMilli(), "updated_at": time.Now().UnixMilli()},
			})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	fakeRealtime := newTUIRealtimeFakeStreamer()
	fakeRealtime.handler = func(index int, ctx context.Context, options client.V3RealtimeResumeOptions, onFrame func(client.V3RealtimeFrame)) error {
		select {
		case <-messagePosted:
			t.Errorf("message POST happened before realtime resume was sent")
		default:
		}
		recordOrder("realtime reconcile")
		<-resumeGate
		if options.OnResumeSent != nil {
			options.OnResumeSent()
		}
		recordOrder("resume sent")
		<-ctx.Done()
		return nil
	}
	frames := make(chan client.V3RealtimeFrame, 8)
	statuses := make(chan tuiRealtimeStatus, 8)
	app := &App{
		api:                 testAPIWithToken(server.URL),
		startupCWD:          testWorkspacePath,
		workspacePath:       testWorkspacePath,
		tuiSessionStore:     newTUISessionStore(),
		tuiRealtime:         newTestTUIRealtimeController(fakeRealtime, frames, statuses),
		tuiRealtimeClientID: "tui:test",
		tuiRealtimeFrames:   frames,
		tuiRealtimeStatuses: statuses,
		homeModel:           model.HomeModel{RecentSessions: []model.SessionSummary{{ID: "session-1", Title: "Native V3", WorkspacePath: testWorkspacePath, SessionAPI: "v3"}}},
	}
	defer app.tuiRealtime.Stop()

	done := make(chan error, 1)
	go func() {
		_, err := app.sendTUIV3ChatMessage(context.Background(), "session-1", ui.ChatSendRequest{Prompt: "hello v3", Instructions: "be brief"})
		done <- err
	}()

	select {
	case <-worksetRequested:
	case <-time.After(2 * time.Second):
		t.Fatal("send did not bootstrap TUI workset before POST")
	}
	call := waitTUIRealtimeCall(t, fakeRealtime)
	if call.EndpointCursor != "cursor-workset" || len(call.Worksets) != 1 {
		t.Fatalf("realtime call before POST = %#v", call)
	}
	select {
	case <-messagePosted:
		t.Fatal("message POST happened before realtime resume was released")
	case <-time.After(25 * time.Millisecond):
	}
	close(resumeGate)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("sendTUIV3ChatMessage() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("send did not complete after realtime resume")
	}
	select {
	case body := <-postBody:
		assertTUIV3MessagePostBody(t, body)
	case <-time.After(2 * time.Second):
		t.Fatal("message POST body was not captured")
	}
	recordOrder("merge result")
	orderMu.Lock()
	gotOrder := append([]string(nil), callOrder...)
	orderMu.Unlock()
	wantOrder := []string{"workset bootstrap", "realtime reconcile", "resume sent", "message post", "merge result"}
	if strings.Join(gotOrder, ",") != strings.Join(wantOrder, ",") {
		t.Fatalf("call order = %v, want %v", gotOrder, wantOrder)
	}
}

func assertTUIV3MessagePostBody(t *testing.T, body map[string]any) {
	t.Helper()
	if got := body["role"]; got != "user" {
		t.Fatalf("role = %v, want user", got)
	}
	if got := body["content"]; got != "hello v3" {
		t.Fatalf("content = %v, want hello v3", got)
	}
	metadata, ok := body["metadata"].(map[string]any)
	if !ok {
		t.Fatalf("metadata = %#v, want object", body["metadata"])
	}
	if _, found := metadata["compact"]; found {
		t.Fatalf("message metadata unexpectedly contains compact flag: %#v", metadata)
	}
	if got := metadata["instructions"]; got != "be brief" {
		t.Fatalf("metadata.instructions = %v, want be brief", got)
	}

	clientRequestID, _ := body["client_request_id"].(string)
	messageID, _ := body["message_id"].(string)
	runID, _ := body["run_id"].(string)
	if clientRequestID == "" || messageID == "" || runID == "" {
		t.Fatalf("operation IDs missing: client_request_id=%q message_id=%q run_id=%q", clientRequestID, messageID, runID)
	}
	if !strings.HasPrefix(clientRequestID, "tui-v3-existing-message:session-1:") {
		t.Fatalf("client_request_id = %q, want tui existing-message prefix", clientRequestID)
	}
	operationID := strings.TrimPrefix(clientRequestID, "tui-v3-existing-message:session-1:")
	if operationID == "" {
		t.Fatalf("client_request_id operation id is empty: %q", clientRequestID)
	}
	if messageID != "tui-v3-message:"+operationID {
		t.Fatalf("message_id = %q, want operation id %q", messageID, operationID)
	}
	if runID != "tui-v3-run:"+operationID {
		t.Fatalf("run_id = %q, want operation id %q", runID, operationID)
	}
}

func TestSendTUIV3ChatMessageRejectsNonAcceptedRunIntent(t *testing.T) {
	tests := []struct {
		name          string
		status        string
		includeStatus bool
		wantError     string
	}{
		{name: "invalid status", status: "dispatch_blocked", includeStatus: true, wantError: "dispatch_blocked"},
		{name: "missing status", includeStatus: false, wantError: "missing phase"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodPost && r.URL.Path == "/v3/tui/sessions:workset":
					_ = json.NewEncoder(w).Encode(client.SessionV3Workset{
						OK:                     true,
						SnapshotEndpointCursor: "cursor-workset",
						SessionsByID:           map[string]client.SessionSummary{"session-1": {ID: "session-1", WorkspacePath: testWorkspacePath, Title: "Native V3", SessionAPI: "v3"}},
						ProjectionsBySession:   map[string]client.SessionV3Projection{"session-1": {SessionID: "session-1", LastEventSeq: 1}},
						SessionOrder:           []string{"session-1"},
					})
				case r.Method == http.MethodPost && r.URL.Path == "/v3/sessions/session-1/messages":
					runIntent := map[string]any{"session_id": "session-1", "run_id": "run-1", "event_seq": 1, "created_at": time.Now().UnixMilli(), "updated_at": time.Now().UnixMilli()}
					if tt.includeStatus {
						runIntent["status"] = tt.status
					}
					_ = json.NewEncoder(w).Encode(map[string]any{
						"ok":         true,
						"session":    map[string]any{"id": "session-1", "workspace_path": testWorkspacePath, "workspace_name": "swarm-go", "title": "Native V3", "mode": "auto"},
						"projection": map[string]any{"session_id": "session-1", "last_event_seq": 1, "projection_high_watermark_seq": 1},
						"message":    map[string]any{"id": "msg-user", "session_id": "session-1", "global_seq": 1, "role": "user", "content": "hello v3", "created_at": time.Now().UnixMilli()},
						"run_intent": runIntent,
					})
				default:
					t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
				}
			}))
			defer server.Close()

			fakeRealtime := newTUIRealtimeFakeStreamer()
			frames := make(chan client.V3RealtimeFrame, 8)
			statuses := make(chan tuiRealtimeStatus, 8)
			app := &App{
				api:                 testAPIWithToken(server.URL),
				startupCWD:          testWorkspacePath,
				workspacePath:       testWorkspacePath,
				tuiSessionStore:     newTUISessionStore(),
				tuiRealtime:         newTestTUIRealtimeController(fakeRealtime, frames, statuses),
				tuiRealtimeClientID: "tui:test",
				tuiRealtimeFrames:   frames,
				tuiRealtimeStatuses: statuses,
				homeModel:           model.HomeModel{RecentSessions: []model.SessionSummary{{ID: "session-1", Title: "Native V3", WorkspacePath: testWorkspacePath, SessionAPI: "v3"}}},
			}
			defer app.tuiRealtime.Stop()

			_, err := app.sendTUIV3ChatMessage(context.Background(), "session-1", ui.ChatSendRequest{Prompt: "hello v3"})
			if err == nil || !strings.Contains(err.Error(), "not accepted") || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("sendTUIV3ChatMessage() error = %v, want non-accepted run intent rejection containing %q", err, tt.wantError)
			}
			if snapshot, ok := app.tuiSessionStore.ChatSnapshot("session-1"); ok && len(snapshot.Messages) != 0 {
				t.Fatalf("non-accepted mutation merged messages into store: %+v", snapshot.Messages)
			}
		})
	}
}

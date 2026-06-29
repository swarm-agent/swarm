package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/model"
	"swarm-refactor/swarmtui/internal/ui"
)

func TestTUIWorkspaceSelectionMarksScopeStaleAndStopsRealtime(t *testing.T) {
	fake := newTUIRealtimeFakeStreamer()
	controller := newTestTUIRealtimeController(fake, make(chan client.V3RealtimeFrame, 4), make(chan tuiRealtimeStatus, 16))
	if err := controller.Reconcile(nil, []client.V3RealtimeWorksetSubscription{{
		WorksetID:             "tui:workspace:/repo-a",
		SubscriptionID:        "tui:test:workset:workspace:/repo-a",
		Surface:               "tui",
		Selector:              client.V3RealtimeWorksetSelector{Kind: "workspace", WorkspacePath: "/repo-a"},
		AutoSubscribeSessions: true,
	}}, "cursor-a"); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	waitTUIRealtimeCall(t, fake)
	activeCtx := waitTUIRealtimeContext(t, fake)

	app := &App{
		api:                 testAPIWithToken("http://127.0.0.1"),
		activePath:          "/repo-a",
		workspacePath:       "/repo-a",
		tuiSessionStore:     newTUISessionStore(),
		tuiRealtime:         controller,
		tuiRealtimeWorkset:  tuiRealtimeWorksetState{ScopeKey: "workspace:/repo-a", WorkspacePaths: []string{"/repo-a"}},
		tuiRealtimeClientID: "tui:test",
		tuiRealtimeFrames:   make(chan client.V3RealtimeFrame, 4),
		tuiRealtimeStatuses: make(chan tuiRealtimeStatus, 4),
		homeModel:           model.EmptyHome(),
		gitStatusCh:         make(chan gitStatusRefreshResult, 1),
		pendingChatRender:   make(chan struct{}, 1),
		pendingStreamReady:  make(chan struct{}, 1),
	}
	app.home = ui.NewHomePage(app.homeModel)
	app.homeModel.Workspaces = []model.Workspace{
		{Name: "Repo A", Path: "/repo-a", Active: true},
		{Name: "Repo B", Path: "/repo-b"},
	}
	app.tuiSessionStore.ResetFromWorkset(client.SessionV3Workset{
		SnapshotEndpointCursor: "cursor-a",
		SessionsByID:           map[string]client.SessionSummary{"session-a": {ID: "session-a", WorkspacePath: "/repo-a", Title: "Repo A", SessionAPI: "v3"}},
		SessionOrder:           []string{"session-a"},
	})
	app.applyTUISessionStoreToHome()

	app.syncActiveWorkspaceSelection(client.WorkspaceResolution{ResolvedPath: "/repo-b", WorkspacePath: "/repo-b", WorkspaceName: "Repo B"})

	stale := app.tuiSessionStore.StaleState()
	if !stale.Stale || !stale.ScopeChanged || stale.Reason != "workspace scope changed" {
		t.Fatalf("stale state = %+v", stale)
	}
	assertContextCanceled(t, activeCtx)
	if app.applyTUIRealtimeFrame(client.V3RealtimeFrame{Kind: "workset.session.updated", SessionID: "session-a", Session: &client.SessionSummary{ID: "session-a", WorkspacePath: "/repo-a", Title: "Repo A stale update", SessionAPI: "v3"}}) {
		t.Fatalf("old scope realtime frame changed visible state while scope was stale")
	}
	if sessions := app.tuiSessionStore.HomeSessions(); len(sessions) != 1 || sessions[0].Title != "Repo A" {
		t.Fatalf("old scope update changed sessions: %#v", sessions)
	}

	app.applyTUISessionWorksetSnapshot(client.SessionV3Workset{
		SnapshotEndpointCursor: "cursor-b",
		SessionsByID:           map[string]client.SessionSummary{"session-b": {ID: "session-b", WorkspacePath: "/repo-b", Title: "Repo B", SessionAPI: "v3"}},
		SessionOrder:           []string{"session-b"},
	}, tuiRealtimeWorksetState{ScopeKey: "workspace:/repo-b", WorkspacePaths: []string{"/repo-b"}})
	app.applyTUISessionStoreToHome()
	if err := app.reconcileTUIRealtime(); err != nil {
		t.Fatalf("reconcileTUIRealtime() error = %v", err)
	}
	call := waitTUIRealtimeCall(t, fake)
	if call.EndpointCursor != "cursor-b" || len(call.Worksets) != 1 || call.Worksets[0].Selector.WorkspacePath != "/repo-b" {
		t.Fatalf("new scope realtime call = %#v", call)
	}
	if sessions := app.homeModel.RecentSessions; len(sessions) != 1 || sessions[0].ID != "session-b" || sessions[0].WorkspacePath != "/repo-b" {
		t.Fatalf("visible sessions after new bootstrap = %#v", sessions)
	}
}

func TestTUIWorkspaceSwitchBootstrapsNewScopeAndResumesRealtime(t *testing.T) {
	requests := make([]client.SessionV3TUIWorksetRequest, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v3/tui/sessions:workset" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var req client.SessionV3TUIWorksetRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		requests = append(requests, req)
		workspace := ""
		if len(req.Scope.WorkspacePaths) > 0 {
			workspace = req.Scope.WorkspacePaths[0]
		}
		cursor := "cursor-a"
		sessionID := "session-a"
		title := "Repo A"
		if workspace == "/repo-b" {
			cursor = "cursor-b"
			sessionID = "session-b"
			title = "Repo B"
		}
		_ = json.NewEncoder(w).Encode(client.SessionV3Workset{
			OK:                     true,
			SnapshotEndpointCursor: cursor,
			SessionsByID:           map[string]client.SessionSummary{sessionID: {ID: sessionID, WorkspacePath: workspace, Title: title, SessionAPI: "v3"}},
			SessionOrder:           []string{sessionID},
		})
	}))
	defer server.Close()

	fake := newTUIRealtimeFakeStreamer()
	controller := newTestTUIRealtimeController(fake, make(chan client.V3RealtimeFrame, 4), make(chan tuiRealtimeStatus, 16))
	app := &App{
		api:                 testAPIWithToken(server.URL),
		activePath:          "/repo-a",
		workspacePath:       "/repo-a",
		tuiSessionStore:     newTUISessionStore(),
		tuiRealtime:         controller,
		tuiRealtimeClientID: "tui:test",
		tuiRealtimeFrames:   make(chan client.V3RealtimeFrame, 4),
		tuiRealtimeStatuses: make(chan tuiRealtimeStatus, 4),
		homeModel:           model.EmptyHome(),
		gitStatusCh:         make(chan gitStatusRefreshResult, 1),
		pendingChatRender:   make(chan struct{}, 1),
		pendingStreamReady:  make(chan struct{}, 1),
	}
	app.home = ui.NewHomePage(app.homeModel)
	app.homeModel.Workspaces = []model.Workspace{{Name: "Repo A", Path: "/repo-a", Active: true}, {Name: "Repo B", Path: "/repo-b"}}

	worksetA, stateA, err := app.bootstrapTUIRealtimeWorkset(context.Background(), tuiSessionWorksetLoadOptions{Limit: 25, WorkspacePaths: []string{"/repo-a"}})
	if err != nil {
		t.Fatalf("bootstrap repo-a: %v", err)
	}
	app.applyTUISessionWorksetSnapshot(worksetA, stateA)
	if err := app.reconcileTUIRealtime(); err != nil {
		t.Fatalf("reconcile repo-a: %v", err)
	}
	callA := waitTUIRealtimeCall(t, fake)
	ctxA := waitTUIRealtimeContext(t, fake)
	if callA.EndpointCursor != "cursor-a" || len(callA.Worksets) != 1 || callA.Worksets[0].Selector.WorkspacePath != "/repo-a" {
		t.Fatalf("repo-a realtime call = %#v", callA)
	}

	app.syncActiveWorkspaceSelection(client.WorkspaceResolution{ResolvedPath: "/repo-b", WorkspacePath: "/repo-b", WorkspaceName: "Repo B"})
	assertContextCanceled(t, ctxA)
	worksetB, stateB, err := app.bootstrapTUIRealtimeWorkset(context.Background(), tuiSessionWorksetLoadOptions{Limit: 25, WorkspacePaths: []string{"/repo-b"}})
	if err != nil {
		t.Fatalf("bootstrap repo-b: %v", err)
	}
	app.applyTUISessionWorksetSnapshot(worksetB, stateB)
	if err := app.reconcileTUIRealtime(); err != nil {
		t.Fatalf("reconcile repo-b: %v", err)
	}
	callB := waitTUIRealtimeCall(t, fake)
	if callB.EndpointCursor != "cursor-b" || len(callB.Worksets) != 1 || callB.Worksets[0].Selector.WorkspacePath != "/repo-b" {
		t.Fatalf("repo-b realtime call = %#v", callB)
	}
	if len(requests) != 2 || len(requests[0].Scope.WorkspacePaths) != 1 || requests[0].Scope.WorkspacePaths[0] != "/repo-a" || len(requests[1].Scope.WorkspacePaths) != 1 || requests[1].Scope.WorkspacePaths[0] != "/repo-b" {
		t.Fatalf("workset requests = %#v", requests)
	}
}

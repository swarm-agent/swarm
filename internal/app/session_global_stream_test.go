package app

import (
	"encoding/json"
	"testing"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/model"
	"swarm-refactor/swarmtui/internal/ui"
)

func TestApplySessionStreamEventCreatesAndDeletesHomeSession(t *testing.T) {
	homeModel := model.HomeModel{RecentSessions: []model.SessionSummary{{ID: "old-session", Title: "Old"}}}
	app := &App{home: ui.NewHomePage(homeModel), homeModel: homeModel}

	createdPayload, err := json.Marshal(client.SessionSummary{ID: "new-session", Title: "New Session", WorkspacePath: testWorkspacePath, WorkspaceName: "swarm-go", Mode: "auto"})
	if err != nil {
		t.Fatalf("marshal create payload: %v", err)
	}
	if !app.applySessionStreamEvent(client.StreamEventEnvelope{Stream: "session:new-session", EventType: "session.created", EntityID: "new-session", Payload: createdPayload}) {
		t.Fatalf("session.created did not report a model change")
	}
	if summary, ok := app.sessionSummaryByID("new-session"); !ok || summary.Title != "New Session" || summary.SessionAPI != "v3" || summary.WorkspacePath != testWorkspacePath {
		t.Fatalf("created summary = %+v ok=%v", summary, ok)
	}

	if !app.applySessionStreamEvent(client.StreamEventEnvelope{Stream: "session:new-session", EventType: "session.deleted", EntityID: "new-session", Payload: createdPayload}) {
		t.Fatalf("session.deleted did not report a model change")
	}
	if _, ok := app.sessionSummaryByID("new-session"); ok {
		t.Fatalf("deleted session remains in home model: %+v", app.homeModel.RecentSessions)
	}
	if _, ok := app.sessionSummaryByID("old-session"); !ok {
		t.Fatalf("unrelated session was removed: %+v", app.homeModel.RecentSessions)
	}
}

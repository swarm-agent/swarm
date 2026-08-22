package v3chat

import (
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/client"
)

func TestFinalHandoffArtifactsRenderingOneAndManyManagedReferences(t *testing.T) {
	singleManaged := []client.PlanFinalHandoffArtifact{
		{
			ID:           "variant-123",
			Label:        "Brainstorming Architecture Doc",
			MediaType:    "text/html",
			Status:       "ready",
			SessionID:    "session-abc",
			CollectionID: "col-456",
			VariantID:    "variant-123",
			EventSeq:     42,
			Previewable:  true,
		},
	}
	lines := finalHandoffDetailsLines(&client.PlanFinalHandoff{
		SchemaVersion: 1,
		Title:         "Brainstorming completed",
		Overview:      "Architecture diagram generated",
		Artifacts:     singleManaged,
	}, "artifacts", "session-abc", 100, testPageStyles())

	var texts []string
	for _, line := range lines {
		texts = append(texts, line.Text)
	}
	joined := strings.Join(texts, "\n")

	for _, want := range []string{
		"1. Brainstorming Architecture Doc",
		"text/html",
		"ready · preview available",
		"Identity: session=session-abc · collection=col-456 · variant=variant-123 · event_seq=42",
		"Route: /v3/sessions/session-abc/artifacts/variant-123",
		"Use the authenticated route through the same local or remote Swarm connection. The path is only a workspace-relative fallback.",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("single managed artifact presentation missing %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "Path:") {
		t.Fatalf("managed artifact forged a workspace path:\n%s", joined)
	}

	manyManaged := []client.PlanFinalHandoffArtifact{
		{
			ID:           "var-1",
			Label:        "Spec Markdown",
			MediaType:    "text/markdown",
			Status:       "ready",
			SessionID:    "sess-1",
			CollectionID: "col-1",
			VariantID:    "var-1",
			EventSeq:     10,
			Previewable:  false,
		},
		{
			ID:           "var-2",
			Label:        "Interactive Mockup",
			MediaType:    "text/html",
			Status:       "ready",
			SessionID:    "sess-1",
			CollectionID: "col-1",
			VariantID:    "var-2",
			EventSeq:     11,
			Previewable:  true,
		},
	}
	manyLines := finalHandoffDetailsLines(&client.PlanFinalHandoff{
		SchemaVersion: 1,
		Title:         "Multiple artifacts",
		Overview:      "Spec and mockup created",
		Artifacts:     manyManaged,
	}, "artifacts", "sess-1", 100, testPageStyles())

	var manyTexts []string
	for _, line := range manyLines {
		manyTexts = append(manyTexts, line.Text)
	}
	manyJoined := strings.Join(manyTexts, "\n")

	for _, want := range []string{
		"1. Spec Markdown  ·  text/markdown  ·  ready",
		"Identity: session=sess-1 · collection=col-1 · variant=var-1 · event_seq=10",
		"Route: /v3/sessions/sess-1/artifacts/var-1",
		"2. Interactive Mockup  ·  text/html  ·  ready · preview available",
		"Identity: session=sess-1 · collection=col-1 · variant=var-2 · event_seq=11",
		"Route: /v3/sessions/sess-1/artifacts/var-2",
	} {
		if !strings.Contains(manyJoined, want) {
			t.Fatalf("many managed artifacts presentation missing %q:\n%s", want, manyJoined)
		}
	}
}

func TestFinalHandoffArtifactsUnavailableStateAndWorkspaceFallback(t *testing.T) {
	artifacts := []client.PlanFinalHandoffArtifact{
		{
			ID:           "var-failed",
			Label:        "Failed Image Gen",
			MediaType:    "image/png",
			Status:       "unavailable",
			SessionID:    "sess-xyz",
			CollectionID: "col-xyz",
			VariantID:    "var-failed",
			Previewable:  false,
		},
		{
			ID:                    "art_workspace_1",
			Label:                 "Generated Report",
			MediaType:             "text/markdown",
			WorkspaceRelativePath: "docs/report.md",
			Previewable:           true,
		},
	}
	lines := finalHandoffDetailsLines(&client.PlanFinalHandoff{
		SchemaVersion: 1,
		Title:         "Mixed artifacts",
		Overview:      "One unavailable, one workspace file",
		Artifacts:     artifacts,
	}, "artifacts", "sess-xyz", 100, testPageStyles())

	var texts []string
	for _, line := range lines {
		texts = append(texts, line.Text)
	}
	joined := strings.Join(texts, "\n")

	if !strings.Contains(joined, "1. Failed Image Gen  ·  image/png  ·  unavailable") {
		t.Fatalf("missing unavailable status:\n%s", joined)
	}
	if !strings.Contains(joined, "2. Generated Report  ·  text/markdown  ·  preview available") {
		t.Fatalf("missing workspace artifact line:\n%s", joined)
	}
	if !strings.Contains(joined, "Path: docs/report.md") {
		t.Fatalf("missing workspace fallback path:\n%s", joined)
	}
}

func TestFinalHandoffArtifactRouteConstructionAndEscaping(t *testing.T) {
	artifacts := []client.PlanFinalHandoffArtifact{
		{
			ID:          "special#variant?1",
			Label:       "Special URI Target",
			MediaType:   "text/html",
			SessionID:   "session/with+special chars",
			Previewable: true,
		},
	}
	lines := finalHandoffDetailsLines(&client.PlanFinalHandoff{
		SchemaVersion: 1,
		Title:         "Escaped URI",
		Overview:      "Check route escaping",
		Artifacts:     artifacts,
	}, "artifacts", "", 100, testPageStyles())

	var texts []string
	for _, line := range lines {
		texts = append(texts, line.Text)
	}
	joined := strings.Join(texts, "\n")

	wantRoute := "Route: /v3/sessions/session%2Fwith+special%20chars/artifacts/special%23variant%3F1"
	if !strings.Contains(joined, wantRoute) {
		t.Fatalf("route escaping incorrect, want %q in:\n%s", wantRoute, joined)
	}
}

func TestFinalHandoffPrimaryRecommendationAndSecondaryPromptsNavigation(t *testing.T) {
	store := NewStore()
	store.Dispatch(HydrateAction{Snapshot: client.SessionV3Hydrated{
		Session: client.SessionSummary{ID: "sess-recommendation"},
		Messages: []client.SessionMessage{
			{
				ID:        "handoff-msg",
				SessionID: "sess-recommendation",
				Role:      "system",
				Metadata: map[string]any{
					"source": finalHandoffSource,
					"final_handoff": map[string]any{
						"schema_version": 1,
						"title":          "Task complete",
						"overview":       "All requirements implemented.",
						"recommendation": map[string]any{
							"decision":     "ship",
							"action":       "review the completed feature",
							"prompt":       "Review the feature and confirm acceptance criteria.",
							"reason":       "All checks passed.",
							"action_state": "ready",
						},
						"suggested_prompts": []any{
							map[string]any{
								"label":  "Ask questions",
								"prompt": "Explain the architecture changes.",
							},
						},
						"artifacts": []any{
							map[string]any{
								"id":          "art-1",
								"label":       "Summary Spec",
								"media_type":  "text/markdown",
								"status":      "ready",
								"session_id":  "sess-recommendation",
								"variant_id":  "art-1",
								"previewable": true,
							},
						},
					},
				},
			},
		},
	}})

	transport := &fakeTransport{}
	page := NewPage(NewRuntime(transport, store, nil), testPageStyles())
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	screen.SetSize(100, 30)
	page.Draw(screen)

	// Focus handoff
	page.HandleKey(tcell.NewEventKey(tcell.KeyTab, 0, tcell.ModNone))
	if !page.handoffFocus || page.handoffControl != 0 {
		t.Fatalf("Tab should focus control 0 (primary recommendation): focus=%t control=%d", page.handoffFocus, page.handoffControl)
	}

	// Move right to control 1 (secondary suggested prompt)
	page.HandleKey(tcell.NewEventKey(tcell.KeyRight, 0, tcell.ModNone))
	if page.handoffControl != 1 {
		t.Fatalf("Right should move to control 1 (suggested prompt): %d", page.handoffControl)
	}

	// Move right to control 2 (artifacts evidence section)
	page.HandleKey(tcell.NewEventKey(tcell.KeyRight, 0, tcell.ModNone))
	if page.handoffControl != 2 {
		t.Fatalf("Right should move to control 2 (artifacts section): %d", page.handoffControl)
	}

	// Move left back to control 0 (primary recommendation)
	page.HandleKey(tcell.NewEventKey(tcell.KeyLeft, 0, tcell.ModNone))
	page.HandleKey(tcell.NewEventKey(tcell.KeyLeft, 0, tcell.ModNone))
	if page.handoffControl != 0 {
		t.Fatalf("Left should return to control 0 (primary recommendation): %d", page.handoffControl)
	}

	// Activate primary recommendation via Enter
	page.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))

	deadline := time.Now().Add(time.Second)
	for {
		transport.mu.Lock()
		request := transport.messageRequest
		transport.mu.Unlock()
		if request.Content != "Review the feature and confirm acceptance criteria." {
			if time.Now().After(deadline) {
				t.Fatalf("recommendation prompt was not sent via ordinary chat Send path: %#v", request)
			}
			time.Sleep(time.Millisecond)
			continue
		}
		if request.Role != "user" || strings.TrimSpace(request.RunID) == "" {
			t.Fatalf("recommendation bypassed ordinary user message semantics: %#v", request)
		}
		break
	}
}

func TestManageArtifactToolPresentationInTimeline(t *testing.T) {
	item := ToolTimelineItem{
		Name:      "manage_artifact",
		Arguments: `{"action":"create","media_type":"text/html","title":"Brainstorming Mockup"}`,
		Output: `{
			"tool":"manage_artifact",
			"action":"create",
			"status":"ok",
			"artifact":{
				"session_id":"sess-1",
				"collection_id":"col-1",
				"id":"var-mockup",
				"event_seq":5,
				"label":"Brainstorming Mockup",
				"media_type":"text/html",
				"status":"ready"
			},
			"reference":{
				"session_id":"sess-1",
				"collection_id":"col-1",
				"variant_id":"var-mockup",
				"event_seq":5
			}
		}`,
	}
	presentation := buildToolPresentation(item)
	if !strings.Contains(presentation.Summary, "manage-artifact create") || !strings.Contains(presentation.Summary, "text/html") {
		t.Fatalf("unexpected summary: %q", presentation.Summary)
	}
	joined := presentationText(presentation)
	for _, want := range []string{
		"Brainstorming Mockup",
		"session=sess-1 · collection=col-1 · variant=var-mockup · event_seq=5",
		"Route: /v3/sessions/sess-1/artifacts/var-mockup",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in presentation:\n%s", want, joined)
		}
	}
}

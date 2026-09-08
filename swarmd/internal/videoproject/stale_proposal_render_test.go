package videoproject

import (
	"context"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement: StartRenderJob blocks the exact pending working cut, not a newer
// confirmed cut. Threat: older initial proposals strand confirmed edits, or a
// workaround silently accepts old proposals. The fake-store service layer proves
// job creation and unchanged proposal/project authority without running a renderer.
func TestStartRenderJobPreservesStalePendingProposal(t *testing.T) {
	store := newFakeSessionStore()
	svc := NewService(store)
	principal := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "acc", UserID: "user"}
	store.sessions["session"] = pebblestore.SessionSnapshot{ID: "session", AccountScopeID: "acc", UserID: "user"}
	store.projects["project"] = pebblestore.VideoProjectSnapshot{ID: "project", AccountScopeID: "acc", UserID: "user", SessionID: "session", CurrentRevisionID: "confirmed"}
	store.revisions["project"] = map[string]pebblestore.VideoProjectRevisionSnapshot{"confirmed": {
		ID: "confirmed", ProjectID: "project", SessionID: "session", AccountScopeID: "acc", UserID: "user",
		Timeline: pebblestore.VideoProjectTimeline{Clips: []pebblestore.VideoTimelineClip{{ID: "intro", SourceKind: pebblestore.VideoClipSourceKindColor, DurationMs: 8000, TimelineEndMs: 8000, Visible: true}}},
	}}
	store.proposals["older"] = pebblestore.VideoEditProposalSnapshot{ID: "older", ProjectID: "project", SessionID: "session", AccountScopeID: "acc", UserID: "user", WorkingRevisionID: "older-working", Status: pebblestore.VideoEditProposalStatusPending}
	if _, err := svc.StartRenderJob(context.Background(), principal, StartRenderJobInput{SessionID: "session", ProjectID: "project", RevisionID: "confirmed", JobID: "job"}); err != nil {
		t.Fatal(err)
	}
	if len(store.jobs) != 1 {
		t.Fatalf("expected one authorized render job, got %d", len(store.jobs))
	}
	if store.proposals["older"].Status != pebblestore.VideoEditProposalStatusPending || store.projects["project"].CurrentRevisionID != "confirmed" {
		t.Fatal("render changed proposal or current cut")
	}
}

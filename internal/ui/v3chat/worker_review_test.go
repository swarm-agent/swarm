package v3chat

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
	"swarm-refactor/swarmtui/internal/client"
)

// Requirement: a pending worker is discoverable without stealing composer keys;
// only an explicit decision on the current server review may activate it.
// Threat: stale/foreign revisions or ordinary Enter could launch an unreviewed worker.
// Boundary: hydrated V3 permission cache -> Page worker review -> authenticated worker API.
type workerReviewFake struct {
	*fakeTransport
	latest        client.WorkerProposal
	getError      error
	decisionError error
	calls         chan bool
}

func (f *workerReviewFake) GetWorkerReview(context.Context, string, string) (client.WorkerProposal, error) {
	return f.latest, f.getError
}
func (f *workerReviewFake) DecideWorkerReview(_ context.Context, workspace, session string, review client.WorkerReview, accept bool) error {
	if workspace != f.latest.WorkspaceID || session != f.latest.SessionID || review != f.latest.WorkerReview {
		return errors.New("foreign worker review")
	}
	f.calls <- accept
	return f.decisionError
}
func workerReviewFixture(t *testing.T, revision uint64) client.PermissionRecord {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"review_kind": "worker_v2", "scope": map[string]any{"workspace_id": "workspace"},
		"worker_review":          client.WorkerReview{ProposalID: "proposal", Revision: revision, Digest: strings.Repeat("a", 64)},
		"document":               map[string]any{"title": "Daily scan", "info": map[string]any{"goal": "Check failures"}, "checkpoints": []any{map[string]any{"title": "Inspect", "tasks": []string{"Find regressions"}, "acceptance_criteria": []string{"Report ready"}}}, "worker_v2": map[string]any{"schedule": map[string]any{"kind": "interval", "interval_seconds": 3600}, "expiration": map[string]any{"kind": "indefinite"}}},
		"acceptance_consequence": "Activates on schedule, not immediately.",
	})
	if err != nil {
		t.Fatal(err)
	}
	return client.PermissionRecord{ID: "permission_proposal", SessionID: "chat", ToolName: "manage_workers", Requirement: "automation_v2_acceptance", Status: "pending", ToolArguments: string(payload)}
}
func waitWorkerDecision(t *testing.T, f *workerReviewFake) bool {
	t.Helper()
	select {
	case action := <-f.calls:
		return action
	case <-time.After(2 * time.Second):
		t.Fatal("worker decision not sent")
		return false
	}
}
func TestWorkerReviewDoesNotStealChatAndRequiresExactExplicitDecision(t *testing.T) {
	for _, accept := range []bool{true, false} {
		t.Run(map[bool]string{true: "accept", false: "decline"}[accept], func(t *testing.T) {
			record := workerReviewFixture(t, 1)
			store := NewStore()
			store.Dispatch(HydrateAction{Snapshot: client.SessionV3Hydrated{Session: client.SessionSummary{ID: "chat"}, PendingPermissions: []client.PermissionRecord{record}}})
			f := &workerReviewFake{fakeTransport: &fakeTransport{}, latest: client.WorkerProposal{WorkerReview: client.WorkerReview{ProposalID: "proposal", Revision: 1, Digest: strings.Repeat("a", 64)}, WorkspaceID: "workspace", SessionID: "chat"}, calls: make(chan bool, 1)}
			f.created = client.SessionV3Hydrated{Session: client.SessionSummary{ID: "chat"}}
			page := NewPage(NewRuntime(f, store, nil), testPageStyles())
			if page.PendingPermissionVisible() {
				t.Fatal("worker review blocked ordinary chat")
			}
			if len(SelectPendingWorkerReviews(store.Snapshot())) != 1 {
				t.Fatal("worker review not discoverable")
			}
			rows := page.renderRows(store.Snapshot(), 80, page.styles)
			found := false
			for _, row := range rows {
				if strings.Contains(row.text, "F6 to inspect") {
					found = true
				}
			}
			if !found {
				t.Fatal("worker review entry missing")
			}
			page.HandleKey(tcell.NewEventKey(tcell.KeyRune, 'h', tcell.ModNone))
			page.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
			select {
			case <-f.calls:
				t.Fatal("ordinary chat activated worker")
			default:
			}
			page.HandleKey(tcell.NewEventKey(tcell.KeyF6, 0, tcell.ModNone))
			if !page.workerReviewOpen {
				t.Fatal("worker details did not open")
			}
			planDistinctionFound := false
			for _, row := range page.renderRows(store.Snapshot(), 80, page.styles) {
				if strings.Contains(row.text, "NOT PLAN APPROVAL") {
					planDistinctionFound = true
				}
			}
			if !planDistinctionFound {
				t.Fatal("worker review confused with plan approval")
			}
			actionsFound := false
			for _, row := range page.renderRows(store.Snapshot(), 80, page.styles) {
				if strings.Contains(row.text, "A Accept worker") && strings.Contains(row.text, "D Decline worker") {
					actionsFound = true
				}
			}
			if !actionsFound {
				t.Fatal("worker actions unavailable")
			}
			page.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
			select {
			case <-f.calls:
				t.Fatal("worker accepted by Enter")
			default:
			}
			key := 'd'
			if accept {
				key = 'a'
			}
			page.HandleKey(tcell.NewEventKey(tcell.KeyRune, key, tcell.ModNone))
			if got := waitWorkerDecision(t, f); got != accept {
				t.Fatalf("decision = %v", got)
			}
			deadline := time.After(2 * time.Second)
			for page.workerReviewVisible() {
				select {
				case <-deadline:
					t.Fatal("resolved review remained open")
				default:
					time.Sleep(time.Millisecond)
				}
			}
			if len(SelectPendingWorkerReviews(store.Snapshot())) != 0 {
				t.Fatal("resolved permission remained cached")
			}
			if page.PendingPermissionVisible() {
				t.Fatal("chat remained blocked")
			}
		})
	}
}
func (p *Page) workerReviewVisible() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.workerReviewVisibleLocked()
}
func TestWorkerReviewRejectsStaleAndForeignBeforeMutation(t *testing.T) {
	for _, tc := range []struct {
		name     string
		proposal client.WorkerProposal
	}{
		{"stale", client.WorkerProposal{WorkerReview: client.WorkerReview{ProposalID: "proposal", Revision: 2, Digest: strings.Repeat("b", 64)}, WorkspaceID: "workspace", SessionID: "chat"}},
		{"foreign", client.WorkerProposal{WorkerReview: client.WorkerReview{ProposalID: "proposal", Revision: 1, Digest: strings.Repeat("a", 64)}, WorkspaceID: "other", SessionID: "chat"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := NewStore()
			store.Dispatch(HydrateAction{Snapshot: client.SessionV3Hydrated{Session: client.SessionSummary{ID: "chat"}, PendingPermissions: []client.PermissionRecord{workerReviewFixture(t, 1)}}})
			f := &workerReviewFake{fakeTransport: &fakeTransport{}, latest: tc.proposal, calls: make(chan bool, 1)}
			page := NewPage(NewRuntime(f, store, nil), testPageStyles())
			page.HandleKey(tcell.NewEventKey(tcell.KeyF6, 0, tcell.ModNone))
			page.HandleKey(tcell.NewEventKey(tcell.KeyRune, 'a', tcell.ModNone))
			deadline := time.After(2 * time.Second)
			for {
				page.mu.Lock()
				busy, errText := page.workerReviewBusy, page.workerReviewError
				page.mu.Unlock()
				if !busy {
					if !strings.Contains(errText, "changed") {
						t.Fatalf("error = %q", errText)
					}
					break
				}
				select {
				case <-deadline:
					t.Fatal("stale decision timed out")
				default:
					time.Sleep(time.Millisecond)
				}
			}
			select {
			case <-f.calls:
				t.Fatal("stale/foreign review activated")
			default:
			}
			if len(SelectPendingWorkerReviews(store.Snapshot())) != 1 {
				t.Fatal("pending review was lost")
			}
			page.HandleKey(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone))
			if page.workerReviewVisible() {
				t.Fatal("escape did not return to chat")
			}
		})
	}
}

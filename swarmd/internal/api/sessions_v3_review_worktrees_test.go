package api

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/uisettings"
)

func TestSessionsV3ReviewRejectsLegacyIntegrationAuthority(t *testing.T) {
	server := &Server{}
	_, err := server.classifySessionsV3ReviewWorktrees(context.Background(), identity.Principal{UserID: "user", AccountScopeID: "account"}, sessionsV3ReviewWorktreesRequest{LegacyIntegrateIDs: []string{"session-lane"}})
	if err == nil || !strings.Contains(err.Error(), "is retired") {
		t.Fatalf("legacy integration error = %v", err)
	}
}

func TestSessionsV3ReviewPromotionContractRequiresExplicitExactLineage(t *testing.T) {
	req := sessionsV3ReviewWorktreesRequest{
		PromoteIDs:   []string{"session-lane"},
		SourceHeads:  map[string]string{"session-lane": "source-head"},
		TargetBranch: "dev",
		TargetHead:   "target-head",
	}
	if len(req.PromoteIDs) != 1 || req.SourceHeads[req.PromoteIDs[0]] == "" || req.TargetBranch == "" || req.TargetHead == "" {
		t.Fatalf("incomplete promotion contract: %+v", req)
	}
	server := &Server{}
	req.Automatic = true
	_, err := server.classifySessionsV3ReviewWorktrees(context.Background(), identity.Principal{UserID: "user", AccountScopeID: "account"}, req)
	if err == nil || !strings.Contains(err.Error(), "explicit user action") {
		t.Fatalf("automatic promotion error = %v", err)
	}
}

// Requirement: an exact promotion targets the source session's captured checkout
// path and base branch, while target_head authenticates the checkout's current
// clean HEAD. Comparing target_head to the historical base commit permanently
// rejects any lane after the captured checkout advances. This helper-level test
// is the narrowest layer that proves current-HEAD movement does not weaken path
// or branch matching before Git preflight performs the remaining lineage checks.
func TestSessionsV3ReviewPromotionMatchesCapturedCheckoutAfterTargetAdvances(t *testing.T) {
	session := pebblestore.SessionSnapshot{
		WorktreeBaseBranch: "dev",
		Metadata: map[string]any{
			"swarm_v3_source_workspace_path": "/repo",
			"base_commit":                    "historical-base-head",
		},
	}
	if !sessionsV3ReviewPromotionMatchesCapturedCheckout(session, "/repo", "dev") {
		t.Fatal("captured checkout should remain promotion-eligible after its current HEAD advances beyond the historical base")
	}
	if sessionsV3ReviewPromotionMatchesCapturedCheckout(session, "/other-repo", "dev") {
		t.Fatal("foreign checkout path unexpectedly matched captured promotion lineage")
	}
	if sessionsV3ReviewPromotionMatchesCapturedCheckout(session, "/repo", "main") {
		t.Fatal("foreign target branch unexpectedly matched captured promotion lineage")
	}
}

func TestSessionsV3ReviewArchiveDeadlineUsesPerSessionMessageActivity(t *testing.T) {
	const doneAt int64 = 1_000_000
	delay := 15 * time.Minute

	tests := []struct {
		name          string
		lastMessageAt int64
		updatedAt     int64
		want          int64
	}{
		{
			name:          "unchanged inactive session retains review completion timing",
			lastMessageAt: doneAt - time.Minute.Milliseconds(),
			updatedAt:     doneAt + 30*time.Minute.Milliseconds(),
			want:          doneAt + delay.Milliseconds(),
		},
		{
			name:          "post-completion message postpones only that session",
			lastMessageAt: doneAt + 5*time.Minute.Milliseconds(),
			updatedAt:     doneAt + 30*time.Minute.Milliseconds(),
			want:          doneAt + 20*time.Minute.Milliseconds(),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			session := pebblestore.SessionSnapshot{LastMessageAt: test.lastMessageAt, UpdatedAt: test.updatedAt}
			if got := sessionsV3ReviewArchiveDeadline(session, doneAt, delay); got != test.want {
				t.Fatalf("deadline = %d, want %d", got, test.want)
			}
		})
	}
}

func TestSessionsV3ReviewArchiveDeadlineIsIndependentPerSession(t *testing.T) {
	const doneAt int64 = 1_000_000
	delay := 15 * time.Minute
	inactive := pebblestore.SessionSnapshot{ID: "inactive", LastMessageAt: doneAt - time.Minute.Milliseconds(), UpdatedAt: doneAt + 30*time.Minute.Milliseconds()}
	active := pebblestore.SessionSnapshot{ID: "active", LastMessageAt: doneAt + 5*time.Minute.Milliseconds(), UpdatedAt: doneAt + 30*time.Minute.Milliseconds()}

	inactiveDeadline := sessionsV3ReviewArchiveDeadline(inactive, doneAt, delay)
	activeDeadline := sessionsV3ReviewArchiveDeadline(active, doneAt, delay)
	if want := doneAt + delay.Milliseconds(); inactiveDeadline != want {
		t.Fatalf("inactive session deadline = %d, want %d", inactiveDeadline, want)
	}
	if want := active.LastMessageAt + delay.Milliseconds(); activeDeadline != want {
		t.Fatalf("active session deadline = %d, want %d", activeDeadline, want)
	}
	if inactiveDeadline == activeDeadline {
		t.Fatalf("sessions with distinct activity unexpectedly share deadline %d", inactiveDeadline)
	}
}

func TestSessionsV3ReviewArchiveDeadlineDisabledOrIncomplete(t *testing.T) {
	session := pebblestore.SessionSnapshot{LastMessageAt: 2_000_000, UpdatedAt: 3_000_000}
	if got := sessionsV3ReviewArchiveDeadline(session, 0, 15*time.Minute); got != 0 {
		t.Fatalf("deadline without review completion = %d, want 0", got)
	}
	if got := sessionsV3ReviewArchiveDeadline(session, 1_000_000, 0); got != 0 {
		t.Fatalf("deadline without delay = %d, want 0", got)
	}
}

func TestReconcileSessionV3ReviewAutoArchiveSchedulesNeedsReviewSession(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "review-auto-archive-reconcile.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	eventLog, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}
	sessionStore := pebblestore.NewSessionStore(store)
	sessionSvc := sessionruntime.NewService(sessionStore, eventLog)
	uiSettingsSvc := uisettings.NewService(pebblestore.NewUISettingsStore(store))
	settings, err := uiSettingsSvc.GetForAccount("account-1")
	if err != nil {
		t.Fatalf("get settings: %v", err)
	}
	settings.Chat.ReviewAutoArchiveMinutes = 15
	if _, err := uiSettingsSvc.SetForAccount("account-1", settings); err != nil {
		t.Fatalf("set settings: %v", err)
	}

	const nowUnixMs int64 = 1_000_000
	session := pebblestore.SessionSnapshot{
		ID:             "session-reconcile",
		AccountScopeID: "account-1",
		UserID:         "user-1",
		WorkspacePath:  t.TempDir(),
		WorkspaceName:  "workspace",
		Title:          "Review session",
		Mode:           "auto",
		CreatedAt:      nowUnixMs - time.Hour.Milliseconds(),
		UpdatedAt:      nowUnixMs,
		LastMessageAt:  nowUnixMs - time.Minute.Milliseconds(),
	}
	if err := sessionStore.CreateSession(session); err != nil {
		t.Fatalf("create session: %v", err)
	}
	planDocument := &pebblestore.SessionPlanDocument{
		ID:     "plan-reconcile",
		Title:  "Review plan",
		Status: "approved",
		ExecutionState: &pebblestore.SessionPlanExecutionState{
			Status: "waiting_review",
		},
	}
	if _, _, err := sessionSvc.SavePlanWithMetadata(session.ID, planDocument.ID, planDocument.Title, "", "approved", "approved", true, sessionruntime.PlanSaveMetadata{Document: planDocument}); err != nil {
		t.Fatalf("save plan: %v", err)
	}

	server := &Server{sessions: sessionSvc, uiSettings: uiSettingsSvc}
	if err := server.reconcileSessionsV3ReviewAutoArchiveForAccount(context.Background(), time.UnixMilli(nowUnixMs), session.AccountScopeID); err != nil {
		t.Fatalf("reconcile reviews: %v", err)
	}

	stored, found, err := sessionSvc.GetSession(session.ID)
	if err != nil || !found {
		t.Fatalf("get session found=%v err=%v", found, err)
	}
	wantDueAt := nowUnixMs + (15 * time.Minute).Milliseconds()
	if got := sessionsV3MetadataInt64(stored.Metadata, "review_auto_archive_after"); got != wantDueAt {
		t.Fatalf("stored deadline = %d, want %d", got, wantDueAt)
	}
	if got := sessionReviewDoneAt(stored); got != nowUnixMs {
		t.Fatalf("review done at = %d, want %d", got, nowUnixMs)
	}
	if due, err := sessionSvc.ListDueReviewAutoArchives(wantDueAt, 10); err != nil || len(due) != 1 || due[0].SessionID != session.ID {
		t.Fatalf("scheduled reviews = %+v err=%v", due, err)
	}
}

func TestArchiveDueSessionsV3ReviewReindexesAfterNewActivity(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "review-auto-archive.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	eventLog, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}
	sessionStore := pebblestore.NewSessionStore(store)
	sessionSvc := sessionruntime.NewService(sessionStore, eventLog)
	uiSettingsSvc := uisettings.NewService(pebblestore.NewUISettingsStore(store))
	settings, err := uiSettingsSvc.GetForAccount("account-1")
	if err != nil {
		t.Fatalf("get settings: %v", err)
	}
	settings.Chat.ReviewAutoArchiveMinutes = 15
	if _, err := uiSettingsSvc.SetForAccount("account-1", settings); err != nil {
		t.Fatalf("set settings: %v", err)
	}

	const doneAt int64 = 1_000_000
	oldDueAt := doneAt + (15 * time.Minute).Milliseconds()
	lastMessageAt := doneAt + (5 * time.Minute).Milliseconds()
	updatedAt := lastMessageAt + time.Minute.Milliseconds()
	newDueAt := lastMessageAt + (15 * time.Minute).Milliseconds()
	session := pebblestore.SessionSnapshot{
		ID:             "session-1",
		AccountScopeID: "account-1",
		WorkspacePath:  t.TempDir(),
		WorkspaceName:  "workspace",
		Title:          "Review session",
		Mode:           "auto",
		Metadata: map[string]any{
			"review_done_at":            doneAt,
			"review_auto_archive_after": oldDueAt,
		},
		CreatedAt:     doneAt,
		UpdatedAt:     updatedAt,
		LastMessageAt: lastMessageAt,
	}
	if err := sessionStore.CreateSession(session); err != nil {
		t.Fatalf("create session: %v", err)
	}

	server := &Server{sessions: sessionSvc, uiSettings: uiSettingsSvc}
	if err := server.archiveDueSessionsV3Review(context.Background(), time.UnixMilli(oldDueAt)); err != nil {
		t.Fatalf("archive due reviews: %v", err)
	}

	stored, found, err := sessionSvc.GetSession(session.ID)
	if err != nil || !found {
		t.Fatalf("get session found=%v err=%v", found, err)
	}
	if got := sessionsV3MetadataInt64(stored.Metadata, "review_auto_archive_after"); got != newDueAt {
		t.Fatalf("stored deadline = %d, want %d", got, newDueAt)
	}
	if due, err := sessionSvc.ListDueReviewAutoArchives(oldDueAt, 10); err != nil || len(due) != 0 {
		t.Fatalf("old deadline still due: due=%+v err=%v", due, err)
	}
	if due, err := sessionSvc.ListDueReviewAutoArchives(newDueAt, 10); err != nil || len(due) != 1 || due[0].SessionID != session.ID || due[0].DueAt != newDueAt {
		t.Fatalf("reindexed deadline = %+v err=%v", due, err)
	}
}

package api

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/uisettings"
)

func TestSessionsV3ReviewArchiveDeadlineUsesLaterActivity(t *testing.T) {
	const doneAt int64 = 1_000_000
	delay := 15 * time.Minute

	tests := []struct {
		name      string
		updatedAt int64
		want      int64
	}{
		{
			name:      "unchanged inactive session retains review completion timing",
			updatedAt: doneAt - time.Minute.Milliseconds(),
			want:      doneAt + delay.Milliseconds(),
		},
		{
			name:      "post-completion activity postpones archival",
			updatedAt: doneAt + 5*time.Minute.Milliseconds(),
			want:      doneAt + 20*time.Minute.Milliseconds(),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			session := pebblestore.SessionSnapshot{UpdatedAt: test.updatedAt}
			if got := sessionsV3ReviewArchiveDeadline(session, doneAt, delay); got != test.want {
				t.Fatalf("deadline = %d, want %d", got, test.want)
			}
		})
	}
}

func TestSessionsV3ReviewArchiveDeadlineDisabledOrIncomplete(t *testing.T) {
	session := pebblestore.SessionSnapshot{UpdatedAt: 2_000_000}
	if got := sessionsV3ReviewArchiveDeadline(session, 0, 15*time.Minute); got != 0 {
		t.Fatalf("deadline without review completion = %d, want 0", got)
	}
	if got := sessionsV3ReviewArchiveDeadline(session, 1_000_000, 0); got != 0 {
		t.Fatalf("deadline without delay = %d, want 0", got)
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
	updatedAt := doneAt + (5 * time.Minute).Milliseconds()
	newDueAt := updatedAt + (15 * time.Minute).Milliseconds()
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
		CreatedAt: doneAt,
		UpdatedAt: updatedAt,
	}
	if err := sessionStore.CreateSession(session); err != nil {
		t.Fatalf("create session: %v", err)
	}

	server := &Server{sessions: sessionSvc, uiSettings: uiSettingsSvc}
	server.archiveDueSessionsV3Review(context.Background(), time.UnixMilli(oldDueAt))

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

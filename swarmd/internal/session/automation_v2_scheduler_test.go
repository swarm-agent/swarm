package session

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	store "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/webhook"
)

type fixtureV2Host struct {
	starts, cancels int
	state           string
	startErr        error
	cancelErr       error
}

func (h *fixtureV2Host) Start(context.Context, store.AutomationV2Occurrence) error {
	h.starts++
	return h.startErr
}
func (h *fixtureV2Host) Cancel(context.Context, store.AutomationV2Occurrence) error {
	h.cancels++
	return h.cancelErr
}
func (h *fixtureV2Host) Outcome(store.AutomationV2Occurrence) (string, string, error) {
	return h.state, "fixture", nil
}

// Purpose: the real V2 scheduler/store boundary honors finite and indefinite
// expiry, missed-slot policy, stop scope and truthful progress. A fixed-clock
// fake host isolates scheduling policy; it is not execution proof (run tests
// exercise the actual host and canonical checkpoint mutation path separately).
func TestAutomationV2SchedulerPolicies(t *testing.T) {
	for _, kind := range []string{"indefinite", "at"} {
		t.Run(kind, func(t *testing.T) {
			db, err := store.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			ids := store.NewIdentityStore(db)
			if _, err = ids.PutUser(store.UserRecord{ID: "owner", Username: "owner"}); err != nil {
				t.Fatal(err)
			}
			if _, err = ids.PutAccountScope(store.AccountScopeRecord{ID: "account", Type: store.AccountScopeTypePersonal, CreatedByUserID: "owner"}); err != nil {
				t.Fatal(err)
			}
			if _, err = ids.PutAccountUser(store.AccountUserRecord{ID: "member", AccountScopeID: "account", UserID: "owner", Status: "active"}); err != nil {
				t.Fatal(err)
			}
			w, err := store.NewWorkspaceStore(db).AddForAccount("account", t.TempDir(), "fixture")
			if err != nil {
				t.Fatal(err)
			}
			ss := store.NewSessionStore(db)
			yes := true
			if err = ss.CreateSession(store.SessionSnapshot{ID: "author", AccountScopeID: "account", UserID: "owner", Mode: "auto", WorkspacePath: w.Path, WorkspaceGrants: []store.WorkspaceGrant{{Kind: store.WorkspaceGrantPrimary, WorkspaceID: w.WorkspaceID, Path: w.Path, Available: &yes}}}); err != nil {
				t.Fatal(err)
			}
			svc := NewService(ss, nil)
			expiration := store.AutomationV2Expiration{Kind: kind}
			if kind == "at" {
				expiration.ExpiresAt = time.Now().Add(2 * time.Minute).UnixMilli()
			}
			doc := store.SessionPlanDocument{Title: "Policy", Info: store.SessionPlanInfo{Goal: "Harmless"}, AutomationV2: &store.AutomationV2Settings{SchemaVersion: 2, Schedule: store.AutomationV2Schedule{Kind: "interval", IntervalSeconds: 60}, Missed: "skip", Overlap: "independent", ActivateOnAccept: true, Expiration: expiration}, Checkpoints: []store.SessionPlanCheckpoint{{ID: "one", Title: "One", Status: "pending", Order: 1, Objective: "Return fixture", AcceptanceCriteria: []string{"Fixture returned"}}}}
			proposal, err := svc.ProposeAutomationV2("account", "owner", w.WorkspaceID, "author", &doc, store.AutomationV2Review{})
			if err != nil {
				t.Fatal(err)
			}
			r, err := svc.AcceptAutomationV2("account", "owner", w.WorkspaceID, "author", proposal.AutomationV2Review)
			if err != nil {
				t.Fatal(err)
			}
			host := &fixtureV2Host{state: "running"}
			scheduler := NewAutomationV2Scheduler(svc, host)
			if err = scheduler.Tick(context.Background(), r, r.NextDueAt); err != nil {
				t.Fatal(err)
			}
			rows, _, err := ss.ListAutomationV2Occurrences("account", "owner", w.WorkspaceID, "author", "", false, 25)
			if err != nil || len(rows) != 1 {
				t.Fatal(err)
			}
			first := rows[0]
			progress, err := svc.AutomationV2Progress("account", "owner", w.WorkspaceID, "author", "UTC", "", r.NextDueAt)
			if err != nil || progress.ForecastIsAdmission || !progress.Complete || len(progress.Occurrences) != 1 {
				t.Fatal("progress", err)
			}
			if _, err = svc.AutomationV2Progress("account", "owner", w.WorkspaceID, "author", "", "", r.NextDueAt); err == nil {
				t.Fatal("implicit display timezone")
			}
			if kind == "at" {
				if err = scheduler.Tick(context.Background(), r, expiration.ExpiresAt); err != nil {
					t.Fatal(err)
				}
				rows, _, _ = ss.ListAutomationV2Occurrences("account", "owner", w.WorkspaceID, "author", "", false, 25)
				if len(rows) != 1 {
					t.Fatal("expiry admitted new work")
				}
				progress, err = svc.AutomationV2Progress("account", "owner", w.WorkspaceID, "author", "UTC", "", expiration.ExpiresAt)
				if err != nil || progress.NoNextReason != "expired" || len(progress.Forecast) != 0 {
					t.Fatal("expired forecast", err)
				}
				current, _, _ := svc.GetAutomationV2Record("account", "owner", w.WorkspaceID, "author")
				if _, err = ss.ControlAutomationV2("account", "owner", w.WorkspaceID, "author", current.Generation, "resume", expiration.ExpiresAt); err == nil {
					t.Fatal("expired resume")
				}
			} else {
				// Long downtime skips an entire backlog, but does not invent expiry.
				future := r.AcceptedAt + int64(90*24*time.Hour/time.Millisecond)
				if err = scheduler.Tick(context.Background(), r, future); err != nil {
					t.Fatal(err)
				}
				current, _, _ := svc.GetAutomationV2Record("account", "owner", w.WorkspaceID, "author")
				if current.NextDueAt <= future || current.Authorization.Kind != "indefinite" {
					t.Fatal("hidden cutoff")
				}
				if err = scheduler.Tick(context.Background(), current, current.NextDueAt); err != nil {
					t.Fatal(err)
				}
				rows, _, _ = ss.ListAutomationV2Occurrences("account", "owner", w.WorkspaceID, "author", "", false, 25)
				if len(rows) != 2 {
					t.Fatal("indefinite stopped")
				}
			}
			current, _, _ := svc.GetAutomationV2Record("account", "owner", w.WorkspaceID, "author")
			futureStopped, err := ss.ControlAutomationV2("account", "owner", w.WorkspaceID, "author", current.Generation, "cancel_future", r.NextDueAt+1)
			if err != nil {
				t.Fatal(err)
			}
			if err = scheduler.Tick(context.Background(), futureStopped, r.NextDueAt+2); err != nil {
				t.Fatal(err)
			}
			if host.cancels != 0 {
				t.Fatal("future-only stop cancelled admitted work")
			}
			stopped, err := ss.ControlAutomationV2("account", "owner", w.WorkspaceID, "author", futureStopped.Generation, "cancel_all", r.NextDueAt+3)
			if err != nil {
				t.Fatal(err)
			}
			host.cancelErr = errors.New("host unavailable")
			if err = scheduler.Tick(context.Background(), stopped, r.NextDueAt+2); err == nil {
				t.Fatal("cancel failure hidden")
			}
			pending, _, _ := ss.ListAutomationV2Occurrences("account", "owner", w.WorkspaceID, "author", "", true, 25)
			if len(pending) == 0 {
				t.Fatal("cancel failure lost pending work")
			}
			host.cancelErr = nil
			scheduler = NewAutomationV2Scheduler(svc, host)
			if err = scheduler.Tick(context.Background(), stopped, r.NextDueAt+3); err != nil {
				t.Fatal(err)
			}
			pending, _, _ = ss.ListAutomationV2Occurrences("account", "owner", w.WorkspaceID, "author", "", true, 25)
			if len(pending) != 0 {
				t.Fatal("cancel recovery incomplete")
			}
			done, found, err := ss.GetAutomationV2Occurrence("account", "owner", w.WorkspaceID, "author", first.ID)
			if err != nil || !found || done.State != "cancelled" {
				t.Fatal("cancel outcome", err)
			}
		})
	}
}

// Purpose: when an automation is scheduled every 5 minutes (interval 300),
// the progress forecast must enumerate all remaining slots for today rather
// than truncating at a hardcoded 5 slots.
func TestAutomationV2SchedulerFiveMinuteForecast(t *testing.T) {
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ids := store.NewIdentityStore(db)
	if _, err = ids.PutUser(store.UserRecord{ID: "owner", Username: "owner"}); err != nil {
		t.Fatal(err)
	}
	if _, err = ids.PutAccountScope(store.AccountScopeRecord{ID: "account", Type: store.AccountScopeTypePersonal, CreatedByUserID: "owner"}); err != nil {
		t.Fatal(err)
	}
	if _, err = ids.PutAccountUser(store.AccountUserRecord{ID: "member", AccountScopeID: "account", UserID: "owner", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	w, err := store.NewWorkspaceStore(db).AddForAccount("account", t.TempDir(), "fixture")
	if err != nil {
		t.Fatal(err)
	}
	ss := store.NewSessionStore(db)
	yes := true
	if err = ss.CreateSession(store.SessionSnapshot{ID: "author", AccountScopeID: "account", UserID: "owner", Mode: "auto", WorkspacePath: w.Path, WorkspaceGrants: []store.WorkspaceGrant{{Kind: store.WorkspaceGrantPrimary, WorkspaceID: w.WorkspaceID, Path: w.Path, Available: &yes}}}); err != nil {
		t.Fatal(err)
	}
	svc := NewService(ss, nil)
	doc := store.SessionPlanDocument{
		Title: "Five Minute Check",
		Info:  store.SessionPlanInfo{Goal: "Check status"},
		AutomationV2: &store.AutomationV2Settings{
			SchemaVersion: 2,
			Schedule: store.AutomationV2Schedule{
				Kind:            "interval",
				IntervalSeconds: 300, // every 5 minutes
			},
			Missed:           "skip",
			Overlap:          "independent",
			ActivateOnAccept: true,
			Expiration:       store.AutomationV2Expiration{Kind: "indefinite"},
		},
		Checkpoints: []store.SessionPlanCheckpoint{{ID: "one", Title: "One", Status: "pending", Order: 1, Objective: "Check", AcceptanceCriteria: []string{"Checked"}}},
	}
	proposal, err := svc.ProposeAutomationV2("account", "owner", w.WorkspaceID, "author", &doc, store.AutomationV2Review{})
	if err != nil {
		t.Fatal(err)
	}
	r, err := svc.AcceptAutomationV2("account", "owner", w.WorkspaceID, "author", proposal.AutomationV2Review)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	// Query progress at current time:
	progress, err := svc.AutomationV2Progress("account", "owner", w.WorkspaceID, "author", "UTC", "", now)
	if err != nil {
		t.Fatal(err)
	}
	if len(progress.Forecast) <= 5 {
		t.Fatalf("expected more than 5 forecast slots for every 5 minute schedule, got %d", len(progress.Forecast))
	}
	loc := time.UTC
	localNow := time.UnixMilli(now).In(loc)
	dayEnd := time.Date(localNow.Year(), localNow.Month(), localNow.Day()+1, 0, 0, 0, 0, loc).UnixMilli()
	expectedRemaining := int((dayEnd-r.NextDueAt)/(300*1000)) + 1
	if expectedRemaining < 5 {
		expectedRemaining = 5
	}
	if len(progress.Forecast) != expectedRemaining {
		t.Fatalf("expected %d forecast slots for 5-minute schedule, got %d", expectedRemaining, len(progress.Forecast))
	}
	_ = r
}

// Purpose: when execution start fails, scheduler applies exponential backoff
// (30s, 60s, 120s, 240s) and does not thrash on 1-second ticks; halts at 5 attempts.
func TestAutomationV2SchedulerStartRetryBackoff(t *testing.T) {
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ids := store.NewIdentityStore(db)
	if _, err = ids.PutUser(store.UserRecord{ID: "owner", Username: "owner"}); err != nil {
		t.Fatal(err)
	}
	if _, err = ids.PutAccountScope(store.AccountScopeRecord{ID: "account", Type: store.AccountScopeTypePersonal, CreatedByUserID: "owner"}); err != nil {
		t.Fatal(err)
	}
	if _, err = ids.PutAccountUser(store.AccountUserRecord{ID: "member", AccountScopeID: "account", UserID: "owner", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	w, err := store.NewWorkspaceStore(db).AddForAccount("account", t.TempDir(), "fixture")
	if err != nil {
		t.Fatal(err)
	}
	ss := store.NewSessionStore(db)
	yes := true
	if err = ss.CreateSession(store.SessionSnapshot{ID: "author", AccountScopeID: "account", UserID: "owner", Mode: "auto", WorkspacePath: w.Path, WorkspaceGrants: []store.WorkspaceGrant{{Kind: store.WorkspaceGrantPrimary, WorkspaceID: w.WorkspaceID, Path: w.Path, Available: &yes}}}); err != nil {
		t.Fatal(err)
	}
	svc := NewService(ss, nil)
	doc := store.SessionPlanDocument{
		Title: "Retry Backoff",
		Info:  store.SessionPlanInfo{Goal: "Retry Backoff"},
		AutomationV2: &store.AutomationV2Settings{
			SchemaVersion:    2,
			Schedule:         store.AutomationV2Schedule{Kind: "interval", IntervalSeconds: 3600},
			Missed:           "skip",
			Overlap:          "serialize",
			ActivateOnAccept: true,
			Expiration:       store.AutomationV2Expiration{Kind: "indefinite"},
		},
		Checkpoints: []store.SessionPlanCheckpoint{{ID: "one", Title: "One", Status: "pending", Order: 1, Objective: "Check", AcceptanceCriteria: []string{"Checked"}}},
	}
	proposal, err := svc.ProposeAutomationV2("account", "owner", w.WorkspaceID, "author", &doc, store.AutomationV2Review{})
	if err != nil {
		t.Fatal(err)
	}
	r, err := svc.AcceptAutomationV2("account", "owner", w.WorkspaceID, "author", proposal.AutomationV2Review)
	if err != nil {
		t.Fatal(err)
	}

	host := &fixtureV2Host{state: "admitted", startErr: errors.New("worktree collision")}
	scheduler := NewAutomationV2Scheduler(svc, host)

	// Tick 1: start fails attempt 1 -> backoff 30s
	now := r.NextDueAt
	if err = scheduler.Tick(context.Background(), r, now); err == nil {
		t.Fatal("expected start failure error")
	}
	if host.starts != 1 {
		t.Fatalf("expected 1 start attempt, got %d", host.starts)
	}

	// Occurrence should be in state "unavailable", AttemptCount=1, NextRetryAt=now+30000
	rows, _, err := ss.ListAutomationV2Occurrences("account", "owner", w.WorkspaceID, "author", "", false, 25)
	if err != nil || len(rows) != 1 {
		t.Fatalf("expected 1 occurrence, got %d (err: %v)", len(rows), err)
	}
	occ := rows[0]
	if occ.State != "unavailable" || occ.AttemptCount != 1 || occ.NextRetryAt != now+30000 {
		t.Fatalf("expected unavailable/1/retry=%d, got state=%s/attempt=%d/retry=%d", now+30000, occ.State, occ.AttemptCount, occ.NextRetryAt)
	}

	// Tick 2: 1 second later -> before NextRetryAt -> Start must NOT be called
	if err = scheduler.Tick(context.Background(), r, now+1000); err != nil {
		t.Fatalf("unexpected error on skipped tick: %v", err)
	}
	if host.starts != 1 {
		t.Fatalf("expected still 1 start attempt (skipped due to backoff), got %d", host.starts)
	}

	// Tick 3: at NextRetryAt -> attempt 2 -> backoff 60s
	retryTime := now + 30000
	if err = scheduler.Tick(context.Background(), r, retryTime); err == nil {
		t.Fatal("expected start failure error on retry")
	}
	if host.starts != 2 {
		t.Fatalf("expected 2 start attempts, got %d", host.starts)
	}
	rows, _, _ = ss.ListAutomationV2Occurrences("account", "owner", w.WorkspaceID, "author", "", false, 25)
	occ = rows[0]
	if occ.AttemptCount != 2 || occ.NextRetryAt != retryTime+60000 {
		t.Fatalf("expected attempt 2 with retry=%d, got attempt=%d/retry=%d", retryTime+60000, occ.AttemptCount, occ.NextRetryAt)
	}

	// Advance through attempts 3, 4, 5
	retryTime = occ.NextRetryAt
	_ = scheduler.Tick(context.Background(), r, retryTime) // attempt 3
	rows, _, _ = ss.ListAutomationV2Occurrences("account", "owner", w.WorkspaceID, "author", "", false, 25)
	retryTime = rows[0].NextRetryAt
	_ = scheduler.Tick(context.Background(), r, retryTime) // attempt 4
	rows, _, _ = ss.ListAutomationV2Occurrences("account", "owner", w.WorkspaceID, "author", "", false, 25)
	retryTime = rows[0].NextRetryAt
	_ = scheduler.Tick(context.Background(), r, retryTime) // attempt 5

	rows, _, _ = ss.ListAutomationV2Occurrences("account", "owner", w.WorkspaceID, "author", "", false, 25)
	occ = rows[0]
	if occ.State != "failed" || occ.AttemptCount != 5 || occ.NextRetryAt != 0 {
		t.Fatalf("expected permanently failed after 5 attempts, got state=%s, attempt=%d, nextRetry=%d", occ.State, occ.AttemptCount, occ.NextRetryAt)
	}
}

// Purpose: when parent session is archived, scheduler cleanly cancels running/pending
// occurrences without ErrAutomationV2Conflict lockup and stops admitting new work.
func TestAutomationV2SchedulerArchivedSessionCancellation(t *testing.T) {
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ids := store.NewIdentityStore(db)
	if _, err = ids.PutUser(store.UserRecord{ID: "owner", Username: "owner"}); err != nil {
		t.Fatal(err)
	}
	if _, err = ids.PutAccountScope(store.AccountScopeRecord{ID: "account", Type: store.AccountScopeTypePersonal, CreatedByUserID: "owner"}); err != nil {
		t.Fatal(err)
	}
	if _, err = ids.PutAccountUser(store.AccountUserRecord{ID: "member", AccountScopeID: "account", UserID: "owner", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	w, err := store.NewWorkspaceStore(db).AddForAccount("account", t.TempDir(), "fixture")
	if err != nil {
		t.Fatal(err)
	}
	ss := store.NewSessionStore(db)
	yes := true
	if err = ss.CreateSession(store.SessionSnapshot{ID: "author", AccountScopeID: "account", UserID: "owner", Mode: "auto", WorkspacePath: w.Path, WorkspaceGrants: []store.WorkspaceGrant{{Kind: store.WorkspaceGrantPrimary, WorkspaceID: w.WorkspaceID, Path: w.Path, Available: &yes}}}); err != nil {
		t.Fatal(err)
	}
	svc := NewService(ss, nil)
	doc := store.SessionPlanDocument{
		Title: "Archive Test",
		Info:  store.SessionPlanInfo{Goal: "Archive Test"},
		AutomationV2: &store.AutomationV2Settings{
			SchemaVersion:    2,
			Schedule:         store.AutomationV2Schedule{Kind: "interval", IntervalSeconds: 60},
			Missed:           "skip",
			Overlap:          "independent",
			ActivateOnAccept: true,
			Expiration:       store.AutomationV2Expiration{Kind: "indefinite"},
		},
		Checkpoints: []store.SessionPlanCheckpoint{{ID: "one", Title: "One", Status: "pending", Order: 1, Objective: "Check", AcceptanceCriteria: []string{"Checked"}}},
	}
	proposal, err := svc.ProposeAutomationV2("account", "owner", w.WorkspaceID, "author", &doc, store.AutomationV2Review{})
	if err != nil {
		t.Fatal(err)
	}
	r, err := svc.AcceptAutomationV2("account", "owner", w.WorkspaceID, "author", proposal.AutomationV2Review)
	if err != nil {
		t.Fatal(err)
	}

	host := &fixtureV2Host{state: "running"}
	scheduler := NewAutomationV2Scheduler(svc, host)

	// Tick 1: admits and starts occurrence
	now := r.NextDueAt
	if err = scheduler.Tick(context.Background(), r, now); err != nil {
		t.Fatalf("tick 1 failed: %v", err)
	}
	rows, _, err := ss.ListAutomationV2Occurrences("account", "owner", w.WorkspaceID, "author", "", true, 25)
	if err != nil || len(rows) != 1 {
		t.Fatalf("expected 1 running occurrence, got %d (err: %v)", len(rows), err)
	}

	// Archive the session
	if err = ss.ArchiveSession("author"); err != nil {
		t.Fatalf("ArchiveSession failed: %v", err)
	}

	// Tick 2: scheduler encounters archived session -> must cancel occurrence cleanly, not fail with conflict
	if err = scheduler.Tick(context.Background(), r, now+1000); err != nil {
		t.Fatalf("tick 2 after archive failed: %v", err)
	}
	if host.cancels != 1 {
		t.Fatalf("expected 1 cancel call on host, got %d", host.cancels)
	}

	// Verify occurrence transitioned to cancelled
	rows, _, err = ss.ListAutomationV2Occurrences("account", "owner", w.WorkspaceID, "author", "", false, 25)
	if err != nil || len(rows) != 1 {
		t.Fatalf("expected 1 occurrence, got %d", len(rows))
	}
	if rows[0].State != "cancelled" {
		t.Fatalf("expected occurrence state 'cancelled', got '%s'", rows[0].State)
	}
}

func TestAutomationV2SchedulerWebhookDispatch(t *testing.T) {
	var mu sync.Mutex
	receivedEvents := make([]string, 0)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		receivedEvents = append(receivedEvents, r.Header.Get("X-Swarm-Event"))
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ids := store.NewIdentityStore(db)
	if _, err = ids.PutUser(store.UserRecord{ID: "owner", Username: "owner"}); err != nil {
		t.Fatal(err)
	}
	if _, err = ids.PutAccountScope(store.AccountScopeRecord{ID: "account", Type: store.AccountScopeTypePersonal, CreatedByUserID: "owner"}); err != nil {
		t.Fatal(err)
	}
	if _, err = ids.PutAccountUser(store.AccountUserRecord{ID: "member", AccountScopeID: "account", UserID: "owner", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	w, err := store.NewWorkspaceStore(db).AddForAccount("account", t.TempDir(), "fixture")
	if err != nil {
		t.Fatal(err)
	}
	ss := store.NewSessionStore(db)
	yes := true
	if err = ss.CreateSession(store.SessionSnapshot{ID: "author", AccountScopeID: "account", UserID: "owner", Mode: "auto", WorkspacePath: w.Path, WorkspaceGrants: []store.WorkspaceGrant{{Kind: store.WorkspaceGrantPrimary, WorkspaceID: w.WorkspaceID, Path: w.Path, Available: &yes}}}); err != nil {
		t.Fatal(err)
	}

	// Register global webhook in store
	if err := ss.PutAutomationV2Webhook("account", &store.AutomationV2GlobalWebhook{
		ID:      "whk_test",
		URL:     server.URL,
		Secret:  "whk-secret",
		Format:  "generic",
		Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}

	svc := NewService(ss, nil)
	doc := store.SessionPlanDocument{
		Title: "Webhook Test Worker",
		Info:  store.SessionPlanInfo{Goal: "Verify webhooks"},
		AutomationV2: &store.AutomationV2Settings{
			SchemaVersion:    2,
			Schedule:         store.AutomationV2Schedule{Kind: "interval", IntervalSeconds: 60},
			Missed:           "skip",
			Overlap:          "independent",
			ActivateOnAccept: true,
			Expiration:       store.AutomationV2Expiration{Kind: "indefinite"},
		},
		Checkpoints: []store.SessionPlanCheckpoint{{ID: "one", Title: "One", Status: "pending", Order: 1, Objective: "Return fixture", AcceptanceCriteria: []string{"Fixture returned"}}},
	}
	proposal, err := svc.ProposeAutomationV2("account", "owner", w.WorkspaceID, "author", &doc, store.AutomationV2Review{})
	if err != nil {
		t.Fatal(err)
	}
	r, err := svc.AcceptAutomationV2("account", "owner", w.WorkspaceID, "author", proposal.AutomationV2Review)
	if err != nil {
		t.Fatal(err)
	}

	host := &fixtureV2Host{state: "running"}
	scheduler := NewAutomationV2Scheduler(svc, host)
	dispatcher := webhook.NewDispatcher(nil)
	defer dispatcher.Close()
	scheduler.SetWebhookDispatcher(dispatcher)

	// Tick 1: occurrence admitted and started
	if err = scheduler.Tick(context.Background(), r, r.NextDueAt); err != nil {
		t.Fatal(err)
	}

	// Wait for async dispatch of EventOccurrenceStarted
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		count := len(receivedEvents)
		mu.Unlock()
		if count >= 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	mu.Lock()
	if len(receivedEvents) < 1 || receivedEvents[0] != webhook.EventOccurrenceStarted {
		t.Fatalf("expected started event, got %v", receivedEvents)
	}
	mu.Unlock()

	// Now simulate completion: host.state = "succeeded"
	host.state = "succeeded"
	if err = scheduler.Tick(context.Background(), r, r.NextDueAt+1000); err != nil {
		t.Fatal(err)
	}

	// Wait for async dispatch of EventOccurrenceSucceeded
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		count := len(receivedEvents)
		mu.Unlock()
		if count >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(receivedEvents) < 2 || receivedEvents[1] != webhook.EventOccurrenceSucceeded {
		t.Fatalf("expected succeeded event, got %v", receivedEvents)
	}
}

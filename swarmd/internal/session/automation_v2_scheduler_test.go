package session

import (
	"context"
	"errors"
	"testing"
	"time"

	store "swarm/packages/swarmd/internal/store/pebble"
)

type fixtureV2Host struct {
	starts, cancels int
	state           string
	cancelErr       error
}

func (h *fixtureV2Host) Start(context.Context, store.AutomationV2Occurrence) error {
	h.starts++
	return nil
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

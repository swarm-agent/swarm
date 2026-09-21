package pebblestore

import (
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/cockroachdb/pebble"
)

// Purpose: acceptance revisions, scheduler admissions and stop fences must share
// V3 transactional authority. Temp-store tests assert rejected writes leave no
// receipt/cursor/policy changes and restart cannot duplicate an admitted slot.
// This layer owns atomicity; run-host execution is separately tested in run.
func TestAutomationV2ExecutionFences(t *testing.T) {
	path := t.TempDir()
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { db.Close() }()
	s := NewSessionStore(db)
	ids := NewIdentityStore(db)
	if _, err = ids.PutUser(UserRecord{ID: "owner", Username: "owner"}); err != nil {
		t.Fatal(err)
	}
	if _, err = ids.PutAccountScope(AccountScopeRecord{ID: "account", Type: AccountScopeTypePersonal, CreatedByUserID: "owner"}); err != nil {
		t.Fatal(err)
	}
	if _, err = ids.PutAccountUser(AccountUserRecord{ID: "member", AccountScopeID: "account", UserID: "owner", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	w, err := NewWorkspaceStore(db).AddForAccount("account", t.TempDir(), "fixture")
	if err != nil {
		t.Fatal(err)
	}
	yes := true
	if err = s.CreateSession(SessionSnapshot{ID: "author", AccountScopeID: "account", UserID: "owner", WorkspacePath: w.Path, WorkspaceGrants: []WorkspaceGrant{{Kind: WorkspaceGrantPrimary, WorkspaceID: w.WorkspaceID, Path: w.Path, Available: &yes}}}); err != nil {
		t.Fatal(err)
	}
	doc := SessionPlanDocument{Title: "Exact", Info: SessionPlanInfo{Goal: "Exact"}, Checkpoints: []SessionPlanCheckpoint{{ID: "one", Title: "One", Objective: "Original instruction", Status: "pending", Order: 1, AcceptanceCriteria: []string{"Original result"}}}, AutomationV2: &AutomationV2Settings{SchemaVersion: 2, Schedule: AutomationV2Schedule{Kind: "interval", IntervalSeconds: 60}, Missed: "coalesce", Overlap: "independent", ActivateOnAccept: true, Expiration: AutomationV2Expiration{Kind: "indefinite"}}}
	proposal, err := s.ProposeAutomationV2("account", "owner", w.WorkspaceID, "author", doc, AutomationV2Review{}, fixtureAutomationV2Validator)
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.AcceptAutomationV2("account", "owner", w.WorkspaceID, "author", proposal.AutomationV2Review, fixtureAutomationV2Validator)
	if err != nil {
		t.Fatal(err)
	}
	read := func() AutomationV2Record {
		t.Helper()
		r, ok, err := s.GetAutomationV2Record("account", "owner", w.WorkspaceID, "author")
		if err != nil || !ok {
			t.Fatal(err)
		}
		return r
	}
	assertNoOccurrence := func() {
		t.Helper()
		rows, _, err := s.ListAutomationV2Occurrences("account", "owner", w.WorkspaceID, "author", "", false, 25)
		if err != nil || len(rows) != 0 {
			t.Fatal("partial occurrence", err)
		}
	}
	restore := s.SetAutomationV2CommitHookForTest(func(string) error { return errors.New("injected atomic failure") })
	if _, err = s.AdmitAutomationV2(r, r.NextDueAt); err == nil {
		t.Fatal("failure ignored")
	}
	restore()
	assertNoOccurrence()
	if !reflect.DeepEqual(read(), r) {
		t.Fatal("failed admission advanced cursor")
	}
	foreign := r
	foreign.UserID = "foreign"
	if _, err = s.AdmitAutomationV2(foreign, r.NextDueAt); !errors.Is(err, ErrAutomationV2Conflict) {
		t.Fatal(err)
	}
	assertNoOccurrence()
	// User-controlled snapshot bytes must never enter the admitted immutable copy.
	altered := r
	altered.Document.Title = "forged"
	if _, err = s.AdmitAutomationV2(altered, r.NextDueAt); err != nil {
		t.Fatal(err)
	}
	occurrences, _, err := s.ListAutomationV2Occurrences("account", "owner", w.WorkspaceID, "author", "", false, 25)
	if err != nil || len(occurrences) != 1 {
		t.Fatal(err)
	}
	first := occurrences[0]
	if first.Record.Document.Title != "Exact" {
		t.Fatal("caller snapshot trusted")
	}
	// Repeat delivery and independent concurrent sweeps of the same instant.
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = s.AdmitAutomationV2(r, r.NextDueAt) }()
	}
	wg.Wait()
	occurrences, _, _ = s.ListAutomationV2Occurrences("account", "owner", w.WorkspaceID, "author", "", false, 25)
	if len(occurrences) != 1 {
		t.Fatal("duplicate slot")
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s = NewSessionStore(db)
	loaded, ok, err := s.GetAutomationV2Occurrence("account", "owner", w.WorkspaceID, "author", first.ID)
	if err != nil || !ok || !reflect.DeepEqual(first, loaded) {
		t.Fatal("restart lost receipt", err)
	}
	// A pending edit leaves old policy active. Acceptance keeps identity, resets
	// the anchor to its accepted instant, and fences new old-revision admissions.
	doc.Checkpoints[0].Objective = "Revised instruction"
	doc.AutomationV2.Schedule.IntervalSeconds = 120
	nextProposal, err := s.ProposeAutomationV2("account", "owner", w.WorkspaceID, "author", doc, proposal.AutomationV2Review, fixtureAutomationV2Validator)
	if err != nil {
		t.Fatal(err)
	}
	if read().Digest != r.Digest {
		t.Fatal("unaccepted policy active")
	}
	beforePause := read()
	pausedDuringReview, err := s.ControlAutomationV2("account", "owner", w.WorkspaceID, "author", beforePause.Generation, "pause", r.AcceptedAt+1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.AcceptAutomationV2("account", "owner", w.WorkspaceID, "author", nextProposal.AutomationV2Review, fixtureAutomationV2Validator); !errors.Is(err, ErrAutomationV2Conflict) {
		t.Fatal("stale review undid pause", err)
	}
	if !reflect.DeepEqual(read(), pausedDuringReview) {
		t.Fatal("stale review changed paused policy")
	}
	nextProposal, err = s.ProposeAutomationV2("account", "owner", w.WorkspaceID, "author", doc, nextProposal.AutomationV2Review, fixtureAutomationV2Validator)
	if err != nil {
		t.Fatal(err)
	}
	restore = s.SetAutomationV2CommitHookForTest(func(string) error { return errors.New("revision failed") })
	if _, err = s.AcceptAutomationV2("account", "owner", w.WorkspaceID, "author", nextProposal.AutomationV2Review, fixtureAutomationV2Validator); err == nil {
		t.Fatal("revision failure hidden")
	}
	restore()
	if read().Digest != r.Digest {
		t.Fatal("partial revised grant")
	}
	revised, err := s.AcceptAutomationV2("account", "owner", w.WorkspaceID, "author", nextProposal.AutomationV2Review, fixtureAutomationV2Validator)
	if err != nil {
		t.Fatal(err)
	}
	if revised.AutomationID != r.AutomationID || revised.Generation <= r.Generation || revised.NextDueAt != revised.AcceptedAt+120000 {
		t.Fatal("revision identity/anchor")
	}
	if _, err = s.AdmitAutomationV2(r, r.NextDueAt+60000); !errors.Is(err, ErrAutomationV2Conflict) {
		t.Fatal("stale admission", err)
	}
	if err = s.WithAutomationV2Dispatch(first, func() error { return nil }); err != nil {
		t.Fatal("accepted edit retroactively revoked admitted snapshot", err)
	}
	paused, err := s.ControlAutomationV2("account", "owner", w.WorkspaceID, "author", revised.Generation, "pause", revised.AcceptedAt+1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.AdmitAutomationV2(paused, paused.NextDueAt); !errors.Is(err, ErrAutomationV2Conflict) {
		t.Fatal("paused admitted")
	}
	resumed, err := s.ControlAutomationV2("account", "owner", w.WorkspaceID, "author", paused.Generation, "resume", paused.NextDueAt+1)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.NextDueAt <= paused.NextDueAt || !resumed.Enabled {
		t.Fatal("resume backlog")
	}
	cancelled, err := s.ControlAutomationV2("account", "owner", w.WorkspaceID, "author", resumed.Generation, "cancel_all", resumed.NextDueAt-1)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	if err = s.WithAutomationV2Dispatch(first, func() error { called = true; return nil }); !errors.Is(err, ErrAutomationV2Conflict) || called {
		t.Fatal("cancel fence bypass")
	}
	if !cancelled.Cancelled || cancelled.Enabled {
		t.Fatal("cancel not durable")
	}
	if err = s.ObserveAutomationV2(first, "cancelled", "host cancellation acknowledged", resumed.NextDueAt); err != nil {
		t.Fatal(err)
	}
	pending, _, err := s.ListAutomationV2Occurrences("account", "owner", w.WorkspaceID, "author", "", true, 25)
	if err != nil || len(pending) != 0 {
		t.Fatal("terminal pending index", err)
	}
	if _, err = s.ControlAutomationV2("account", "owner", w.WorkspaceID, "author", cancelled.Generation, "resume", resumed.NextDueAt); !errors.Is(err, ErrAutomationV2Conflict) {
		t.Fatal("cancel resumed")
	}
}

// Purpose: the V2 pure clock boundary preserves elapsed anchoring, DST, leap
// days and exact finite expiry without cadence approximations or hidden cutoff.
func TestAutomationV2ScheduleClock(t *testing.T) {
	policy := AutomationV2Settings{SchemaVersion: 2, Schedule: AutomationV2Schedule{Kind: "interval", IntervalSeconds: 900}, Missed: "skip", Overlap: "serialize", ActivateOnAccept: true, Expiration: AutomationV2Expiration{Kind: "indefinite"}}
	anchor := time.Date(2026, 1, 1, 0, 0, 13, 0, time.UTC).UnixMilli()
	got, err := AutomationV2NextDue(policy, anchor, anchor+900000)
	if err != nil || got != anchor+1800000 {
		t.Fatal("interval reanchored", got, err)
	}
	for _, tc := range []struct{ cron, zone, after, want string }{
		{"30 2 * * *", "America/New_York", "2026-03-08T06:59:00Z", "2026-03-09T06:30:00Z"},
		{"30 1 * * *", "America/New_York", "2026-11-01T05:30:00Z", "2026-11-01T06:30:00Z"},
		{"0 0 29 2 *", "UTC", "2025-01-01T00:00:00Z", "2028-02-29T00:00:00Z"},
	} {
		after, _ := time.Parse(time.RFC3339, tc.after)
		want, _ := time.Parse(time.RFC3339, tc.want)
		policy.Schedule = AutomationV2Schedule{Kind: "cron", Cron: tc.cron, Timezone: tc.zone}
		got, err := AutomationV2NextDue(policy, after.UnixMilli(), after.UnixMilli())
		if err != nil || got != want.UnixMilli() {
			t.Fatal(tc, got, err)
		}
	}
}

func fixtureSetupSessionStore(t *testing.T) (*SessionStore, *pebble.DB, string, string, string) {
	t.Helper()
	path := t.TempDir()
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s := NewSessionStore(db)
	ids := NewIdentityStore(db)
	if _, err = ids.PutUser(UserRecord{ID: "owner", Username: "owner"}); err != nil {
		t.Fatal(err)
	}
	if _, err = ids.PutAccountScope(AccountScopeRecord{ID: "account", Type: AccountScopeTypePersonal, CreatedByUserID: "owner"}); err != nil {
		t.Fatal(err)
	}
	if _, err = ids.PutAccountUser(AccountUserRecord{ID: "member", AccountScopeID: "account", UserID: "owner", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	w, err := NewWorkspaceStore(db).AddForAccount("account", t.TempDir(), "fixture")
	if err != nil {
		t.Fatal(err)
	}
	return s, db, "account", "owner", w.WorkspaceID
}

// Purpose: cron step */0 must not panic and must return an error.
func TestAutomationV2CronStepZero(t *testing.T) {
	policy := AutomationV2Settings{
		SchemaVersion: 2,
		Schedule: AutomationV2Schedule{
			Kind:     "cron",
			Cron:     "*/0 * * * *",
			Timezone: "UTC",
		},
		Missed:           "skip",
		Overlap:          "serialize",
		ActivateOnAccept: true,
		Expiration:       AutomationV2Expiration{Kind: "indefinite"},
	}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("AutomationV2NextDue panicked on */0: %v", r)
		}
	}()
	_, err := AutomationV2NextDue(policy, time.Now().UnixMilli(), time.Now().UnixMilli())
	if err == nil {
		t.Fatal("expected error for cron step */0, got nil")
	}
}

// Purpose: POSIX cron matching requires that when both DOM and DOW are specified,
// the schedule matches when either DOM or DOW matches.
func TestAutomationV2POSIXCronDOMOrDOW(t *testing.T) {
	policy := AutomationV2Settings{
		SchemaVersion: 2,
		Schedule: AutomationV2Schedule{
			Kind:     "cron",
			Cron:     "0 0 15 * 5", // 15th of month OR Friday
			Timezone: "UTC",
		},
		Missed:           "skip",
		Overlap:          "serialize",
		ActivateOnAccept: true,
		Expiration:       AutomationV2Expiration{Kind: "indefinite"},
	}
	if err := ValidateAutomationV2Settings(&policy, time.Now().UnixMilli()); err != nil {
		t.Fatalf("ValidateAutomationV2Settings rejected POSIX DOM and DOW: %v", err)
	}
	// Case A: 2026-05-16 is Saturday. Next occurrence should be Friday 2026-05-22 (matches DOW=5).
	afterA := time.Date(2026, 5, 16, 0, 0, 0, 0, time.UTC).UnixMilli()
	gotA, err := AutomationV2NextDue(policy, afterA, afterA)
	if err != nil {
		t.Fatalf("AutomationV2NextDue failed for Case A: %v", err)
	}
	wantA := time.Date(2026, 5, 22, 0, 0, 0, 0, time.UTC).UnixMilli()
	if gotA != wantA {
		t.Fatalf("expected Friday 2026-05-22 (%d), got %d (%v)", wantA, gotA, time.UnixMilli(gotA).UTC())
	}

	// Case B: 2026-06-13 is Saturday. Next occurrence should be Monday 2026-06-15 (matches DOM=15).
	afterB := time.Date(2026, 6, 13, 0, 0, 0, 0, time.UTC).UnixMilli()
	gotB, err := AutomationV2NextDue(policy, afterB, afterB)
	if err != nil {
		t.Fatalf("AutomationV2NextDue failed for Case B: %v", err)
	}
	wantB := time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC).UnixMilli()
	if gotB != wantB {
		t.Fatalf("expected Monday 2026-06-15 (%d), got %d (%v)", wantB, gotB, time.UnixMilli(gotB).UTC())
	}
}

// Purpose: DailyRunCap bounds admissions in a calendar day in the configured timezone.
func TestAutomationV2DailyRunCap(t *testing.T) {
	s, db, acct, user, workspaceID := fixtureSetupSessionStore(t)
	defer db.Close()

	yes := true
	sessionID := "daily-cap-session"
	if err := s.CreateSession(SessionSnapshot{
		ID:              sessionID,
		AccountScopeID:  acct,
		UserID:          user,
		WorkspacePath:   t.TempDir(),
		WorkspaceGrants: []WorkspaceGrant{{Kind: WorkspaceGrantPrimary, WorkspaceID: workspaceID, Path: t.TempDir(), Available: &yes}},
	}); err != nil {
		t.Fatal(err)
	}

	// Noon on 2026-06-01 UTC
	anchor := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC).UnixMilli()
	doc := SessionPlanDocument{
		Title: "Cap Test",
		Info:  SessionPlanInfo{Goal: "Cap Test"},
		Checkpoints: []SessionPlanCheckpoint{
			{ID: "cp-1", Title: "Work", Objective: "Work", Status: "pending", Order: 1, AcceptanceCriteria: []string{"Done"}},
		},
		AutomationV2: &AutomationV2Settings{
			SchemaVersion:    2,
			Schedule:         AutomationV2Schedule{Kind: "interval", IntervalSeconds: 60},
			Missed:           "coalesce",
			Overlap:          "independent",
			ActivateOnAccept: true,
			Expiration:       AutomationV2Expiration{Kind: "indefinite"},
			DailyRunCap:      2,
		},
	}
	p, err := s.ProposeAutomationV2(acct, user, workspaceID, sessionID, doc, AutomationV2Review{}, fixtureAutomationV2Validator)
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.AcceptAutomationV2(acct, user, workspaceID, sessionID, p.AutomationV2Review, fixtureAutomationV2Validator)
	if err != nil {
		t.Fatal(err)
	}

	// First admission at anchor -> succeeds
	r.NextDueAt = anchor
	occ1, err := s.AdmitAutomationV2(r, anchor)
	if err != nil {
		t.Fatalf("first admission failed: %v", err)
	}
	if occ1.ID == "" {
		t.Fatal("expected non-empty occurrence 1")
	}

	// Reload record
	r, ok, err := s.GetAutomationV2Record(acct, user, workspaceID, sessionID)
	if err != nil || !ok {
		t.Fatalf("failed to reload record: %v", err)
	}

	// Second admission at anchor + 60s -> succeeds
	occ2, err := s.AdmitAutomationV2(r, r.NextDueAt)
	if err != nil {
		t.Fatalf("second admission failed: %v", err)
	}
	if occ2.ID == "" {
		t.Fatal("expected non-empty occurrence 2")
	}

	// Reload record
	r, ok, err = s.GetAutomationV2Record(acct, user, workspaceID, sessionID)
	if err != nil || !ok {
		t.Fatalf("failed to reload record: %v", err)
	}

	// Third admission on the same day -> must return ErrAutomationV2Conflict because DailyRunCap = 2
	_, err = s.AdmitAutomationV2(r, r.NextDueAt)
	if !errors.Is(err, ErrAutomationV2Conflict) {
		t.Fatalf("expected ErrAutomationV2Conflict on 3rd admission, got: %v", err)
	}

	// Check that r.NextDueAt was deferred to the start of the next day (2026-06-02 00:00:00 UTC)
	r, ok, err = s.GetAutomationV2Record(acct, user, workspaceID, sessionID)
	if err != nil || !ok {
		t.Fatalf("failed to reload record: %v", err)
	}
	expectedNextDay := time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC).UnixMilli()
	if r.NextDueAt != expectedNextDay {
		t.Fatalf("expected NextDueAt deferred to next day %d, got %d", expectedNextDay, r.NextDueAt)
	}

	// Check occurrence count is still 2
	occs, _, err := s.ListAutomationV2Occurrences(acct, user, workspaceID, sessionID, "", false, 25)
	if err != nil || len(occs) != 2 {
		t.Fatalf("expected 2 occurrences, got %d (err: %v)", len(occs), err)
	}
}

// Purpose: ListAutomationV2Occurrences and GetAutomationV2Occurrence must succeed
// when parent session is archived to allow the scheduler to discover and drain running work.
func TestAutomationV2ArchivedSessionOccurrenceListing(t *testing.T) {
	s, db, acct, user, workspaceID := fixtureSetupSessionStore(t)
	defer db.Close()

	yes := true
	sessionID := "archived-session"
	if err := s.CreateSession(SessionSnapshot{
		ID:              sessionID,
		AccountScopeID:  acct,
		UserID:          user,
		WorkspacePath:   t.TempDir(),
		WorkspaceGrants: []WorkspaceGrant{{Kind: WorkspaceGrantPrimary, WorkspaceID: workspaceID, Path: t.TempDir(), Available: &yes}},
	}); err != nil {
		t.Fatal(err)
	}

	doc := SessionPlanDocument{
		Title: "Archive Test",
		Info:  SessionPlanInfo{Goal: "Archive Test"},
		Checkpoints: []SessionPlanCheckpoint{
			{ID: "cp-1", Title: "Work", Objective: "Work", Status: "pending", Order: 1, AcceptanceCriteria: []string{"Done"}},
		},
		AutomationV2: &AutomationV2Settings{
			SchemaVersion:    2,
			Schedule:         AutomationV2Schedule{Kind: "interval", IntervalSeconds: 60},
			Missed:           "coalesce",
			Overlap:          "independent",
			ActivateOnAccept: true,
			Expiration:       AutomationV2Expiration{Kind: "indefinite"},
		},
	}
	p, err := s.ProposeAutomationV2(acct, user, workspaceID, sessionID, doc, AutomationV2Review{}, fixtureAutomationV2Validator)
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.AcceptAutomationV2(acct, user, workspaceID, sessionID, p.AutomationV2Review, fixtureAutomationV2Validator)
	if err != nil {
		t.Fatal(err)
	}

	occ, err := s.AdmitAutomationV2(r, r.NextDueAt)
	if err != nil {
		t.Fatalf("admit failed: %v", err)
	}

	// Archive the session
	if err := s.ArchiveSession(sessionID); err != nil {
		t.Fatalf("archive session failed: %v", err)
	}

	// ListAutomationV2Occurrences MUST succeed for archived sessions
	rows, _, err := s.ListAutomationV2Occurrences(acct, user, workspaceID, sessionID, "", false, 25)
	if err != nil {
		t.Fatalf("expected ListAutomationV2Occurrences to succeed on archived session, got: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != occ.ID {
		t.Fatalf("expected 1 occurrence matching admitted occ, got %+v", rows)
	}

	// GetAutomationV2Occurrence MUST also succeed for archived sessions
	loaded, found, err := s.GetAutomationV2Occurrence(acct, user, workspaceID, sessionID, occ.ID)
	if err != nil || !found {
		t.Fatalf("expected GetAutomationV2Occurrence to succeed, found=%v, err=%v", found, err)
	}
	if loaded.ID != occ.ID {
		t.Fatalf("expected occurrence ID %s, got %s", occ.ID, loaded.ID)
	}
}

// Purpose: ControlAutomationV2 with "delete_automation" permanently removes the accepted record
// and clears the session's automation binding.
func TestAutomationV2ControlDeleteAutomation(t *testing.T) {
	s, db, acct, user, workspaceID := fixtureSetupSessionStore(t)
	defer db.Close()

	yes := true
	sessionID := "delete-auto-session"
	if err := s.CreateSession(SessionSnapshot{
		ID:              sessionID,
		AccountScopeID:  acct,
		UserID:          user,
		WorkspacePath:   t.TempDir(),
		WorkspaceGrants: []WorkspaceGrant{{Kind: WorkspaceGrantPrimary, WorkspaceID: workspaceID, Path: t.TempDir(), Available: &yes}},
	}); err != nil {
		t.Fatal(err)
	}

	doc := SessionPlanDocument{
		Title: "Delete Test",
		Info:  SessionPlanInfo{Goal: "Delete Test"},
		Checkpoints: []SessionPlanCheckpoint{
			{ID: "cp-1", Title: "Work", Objective: "Work", Status: "pending", Order: 1, AcceptanceCriteria: []string{"Done"}},
		},
		AutomationV2: &AutomationV2Settings{
			SchemaVersion:    2,
			Schedule:         AutomationV2Schedule{Kind: "interval", IntervalSeconds: 60},
			Missed:           "coalesce",
			Overlap:          "independent",
			ActivateOnAccept: true,
			Expiration:       AutomationV2Expiration{Kind: "indefinite"},
		},
	}
	p, err := s.ProposeAutomationV2(acct, user, workspaceID, sessionID, doc, AutomationV2Review{}, fixtureAutomationV2Validator)
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.AcceptAutomationV2(acct, user, workspaceID, sessionID, p.AutomationV2Review, fixtureAutomationV2Validator)
	if err != nil {
		t.Fatal(err)
	}

	// Verify session has AutomationV2 binding
	sess, _, err := s.GetSession(sessionID)
	if err != nil || sess.AutomationV2 == nil {
		t.Fatalf("expected session to have AutomationV2 binding, got %+v (err: %v)", sess.AutomationV2, err)
	}

	// Call ControlAutomationV2 with "delete_automation"
	deleted, err := s.ControlAutomationV2(acct, user, workspaceID, sessionID, r.Generation, "delete_automation", time.Now().UnixMilli())
	if err != nil {
		t.Fatalf("ControlAutomationV2 delete_automation failed: %v", err)
	}
	if !deleted.Cancelled || deleted.Enabled || deleted.CancelThrough != int64(r.Generation) {
		t.Fatalf("expected deleted record to be cancelled and disabled, got %+v", deleted)
	}

	// Verify GetAutomationV2Record returns found=false
	_, found, err := s.GetAutomationV2Record(acct, user, workspaceID, sessionID)
	if err != nil {
		t.Fatalf("GetAutomationV2Record error: %v", err)
	}
	if found {
		t.Fatal("expected automation record to be purged/not found after delete_automation")
	}

	// Verify session's AutomationV2 binding was cleared
	sess, _, err = s.GetSession(sessionID)
	if err != nil || sess.AutomationV2 != nil {
		t.Fatalf("expected session AutomationV2 binding to be nil, got %+v", sess.AutomationV2)
	}
}

// Purpose: AutomationV2Occurrence preserves and persists AttemptCount and NextRetryAt
// across updates to support scheduler exponential backoff.
func TestAutomationV2OccurrenceRetryTracking(t *testing.T) {
	s, db, acct, user, workspaceID := fixtureSetupSessionStore(t)
	defer db.Close()

	yes := true
	sessionID := "retry-tracking-session"
	if err := s.CreateSession(SessionSnapshot{
		ID:              sessionID,
		AccountScopeID:  acct,
		UserID:          user,
		WorkspacePath:   t.TempDir(),
		WorkspaceGrants: []WorkspaceGrant{{Kind: WorkspaceGrantPrimary, WorkspaceID: workspaceID, Path: t.TempDir(), Available: &yes}},
	}); err != nil {
		t.Fatal(err)
	}

	doc := SessionPlanDocument{
		Title: "Retry Test",
		Info:  SessionPlanInfo{Goal: "Retry Test"},
		Checkpoints: []SessionPlanCheckpoint{
			{ID: "cp-1", Title: "Work", Objective: "Work", Status: "pending", Order: 1, AcceptanceCriteria: []string{"Done"}},
		},
		AutomationV2: &AutomationV2Settings{
			SchemaVersion:    2,
			Schedule:         AutomationV2Schedule{Kind: "interval", IntervalSeconds: 60},
			Missed:           "coalesce",
			Overlap:          "independent",
			ActivateOnAccept: true,
			Expiration:       AutomationV2Expiration{Kind: "indefinite"},
		},
	}
	p, err := s.ProposeAutomationV2(acct, user, workspaceID, sessionID, doc, AutomationV2Review{}, fixtureAutomationV2Validator)
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.AcceptAutomationV2(acct, user, workspaceID, sessionID, p.AutomationV2Review, fixtureAutomationV2Validator)
	if err != nil {
		t.Fatal(err)
	}

	occ, err := s.AdmitAutomationV2(r, r.NextDueAt)
	if err != nil {
		t.Fatalf("admit failed: %v", err)
	}
	if occ.AttemptCount != 0 || occ.NextRetryAt != 0 {
		t.Fatalf("expected initial 0 attempt count and next retry at, got attempt=%d, next_retry=%d", occ.AttemptCount, occ.NextRetryAt)
	}

	// Update occurrence with retry info: 2 attempts, next retry in 5s
	retryAt := time.Now().UnixMilli() + 5000
	occ.AttemptCount = 2
	occ.NextRetryAt = retryAt

	if err := s.ObserveAutomationV2(occ, "unavailable", "backing off after failure", time.Now().UnixMilli()); err != nil {
		t.Fatalf("ObserveAutomationV2 failed: %v", err)
	}

	// Verify persistence via GetAutomationV2Occurrence
	loaded, found, err := s.GetAutomationV2Occurrence(acct, user, workspaceID, sessionID, occ.ID)
	if err != nil || !found {
		t.Fatalf("failed to get occurrence: found=%v, err=%v", found, err)
	}
	if loaded.AttemptCount != 2 {
		t.Fatalf("expected AttemptCount=2, got %d", loaded.AttemptCount)
	}
	if loaded.NextRetryAt != retryAt {
		t.Fatalf("expected NextRetryAt=%d, got %d", retryAt, loaded.NextRetryAt)
	}

	// Verify persistence via ListAutomationV2Occurrences
	rows, _, err := s.ListAutomationV2Occurrences(acct, user, workspaceID, sessionID, "", false, 25)
	if err != nil || len(rows) != 1 {
		t.Fatalf("expected 1 listed occurrence, got %d (err: %v)", len(rows), err)
	}
	if rows[0].AttemptCount != 2 || rows[0].NextRetryAt != retryAt {
		t.Fatalf("listed occurrence missing retry info: attempt=%d, retryAt=%d", rows[0].AttemptCount, rows[0].NextRetryAt)
	}
}

// Purpose: Session deletion purges all automation occurrences and pending index keys.
func TestAutomationV2PurgeDeletedSession(t *testing.T) {
	s, db, acct, user, workspaceID := fixtureSetupSessionStore(t)
	defer db.Close()

	yes := true
	sessionID := "purge-session"
	if err := s.CreateSession(SessionSnapshot{
		ID:              sessionID,
		AccountScopeID:  acct,
		UserID:          user,
		WorkspacePath:   t.TempDir(),
		WorkspaceGrants: []WorkspaceGrant{{Kind: WorkspaceGrantPrimary, WorkspaceID: workspaceID, Path: t.TempDir(), Available: &yes}},
	}); err != nil {
		t.Fatal(err)
	}

	doc := SessionPlanDocument{
		Title: "Purge Test",
		Info:  SessionPlanInfo{Goal: "Purge Test"},
		Checkpoints: []SessionPlanCheckpoint{
			{ID: "cp-1", Title: "Work", Objective: "Work", Status: "pending", Order: 1, AcceptanceCriteria: []string{"Done"}},
		},
		AutomationV2: &AutomationV2Settings{
			SchemaVersion:    2,
			Schedule:         AutomationV2Schedule{Kind: "interval", IntervalSeconds: 60},
			Missed:           "coalesce",
			Overlap:          "independent",
			ActivateOnAccept: true,
			Expiration:       AutomationV2Expiration{Kind: "indefinite"},
		},
	}
	p, err := s.ProposeAutomationV2(acct, user, workspaceID, sessionID, doc, AutomationV2Review{}, fixtureAutomationV2Validator)
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.AcceptAutomationV2(acct, user, workspaceID, sessionID, p.AutomationV2Review, fixtureAutomationV2Validator)
	if err != nil {
		t.Fatal(err)
	}
	occ, err := s.AdmitAutomationV2(r, r.NextDueAt)
	if err != nil {
		t.Fatalf("admit failed: %v", err)
	}

	// Delete the session permanently
	if err := s.DeleteSessions([]string{sessionID}); err != nil {
		t.Fatalf("delete session failed: %v", err)
	}

	// Verify occurrence key is purged
	occKey := automationV2OccurrenceKey(occ)
	_, closer, err := db.Get([]byte(occKey))
	if closer != nil {
		closer.Close()
	}
	if !errors.Is(err, pebble.ErrNotFound) {
		t.Fatalf("expected pebble.ErrNotFound for purged occurrence key, got: %v", err)
	}

	// Verify pending key is purged
	pendingKey := automationV2PendingKey(occ)
	_, closer, err = db.Get([]byte(pendingKey))
	if closer != nil {
		closer.Close()
	}
	if !errors.Is(err, pebble.ErrNotFound) {
		t.Fatalf("expected pebble.ErrNotFound for purged pending key, got: %v", err)
	}
}

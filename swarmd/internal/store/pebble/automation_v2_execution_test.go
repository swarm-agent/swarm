package pebblestore

import (
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"
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

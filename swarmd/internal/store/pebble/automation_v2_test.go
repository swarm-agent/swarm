package pebblestore

import (
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
)

// Purpose: the private V2 participant in ApplyV3SessionMutation must create
// authorization and binding only with the exact review; a real store proves
// failure rollback, concurrent idempotency and durable reload, without V1 fixtures.
func TestAutomationV2AtomicAcceptance(t *testing.T) {
	for _, expiration := range []AutomationV2Expiration{{Kind: "indefinite"}, {Kind: "at", ExpiresAt: 4102444800000}} {
		t.Run(expiration.Kind, func(t *testing.T) { testAutomationV2AtomicAcceptance(t, expiration) })
	}
}
func testAutomationV2AtomicAcceptance(t *testing.T, expiration AutomationV2Expiration) {
	path := t.TempDir()
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s := NewSessionStore(db)
	identity := NewIdentityStore(db)
	if _, err := identity.PutUser(UserRecord{ID: "owner", Username: "owner"}); err != nil {
		t.Fatal(err)
	}
	if _, err := identity.PutAccountScope(AccountScopeRecord{ID: "account", Type: AccountScopeTypePersonal, CreatedByUserID: "owner"}); err != nil {
		t.Fatal(err)
	}
	if _, err := identity.PutAccountUser(AccountUserRecord{ID: "membership", AccountScopeID: "account", UserID: "owner", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	workspace, err := NewWorkspaceStore(db).AddForAccount("account", t.TempDir(), "Workspace")
	if err != nil {
		t.Fatal(err)
	}
	workspaceID := workspace.WorkspaceID
	available := true
	original := SessionSnapshot{ID: "conversation", AccountScopeID: "account", UserID: "owner", WorkspacePath: t.TempDir(), WorkspaceGrants: []WorkspaceGrant{{Kind: WorkspaceGrantPrimary, WorkspaceID: workspaceID, Path: workspace.Path, Available: &available}}}
	if err := s.CreateSession(original); err != nil {
		t.Fatal(err)
	}
	doc := SessionPlanDocument{Title: "Exact instructions", Info: SessionPlanInfo{Goal: "Preserve these bytes"}, Checkpoints: []SessionPlanCheckpoint{{ID: "cp-1", Title: "Work", Objective: "Implement", Status: "pending", Order: 1, AcceptanceCriteria: []string{"Works"}}}, AutomationV2: &AutomationV2Settings{SchemaVersion: 2, Schedule: AutomationV2Schedule{Kind: "interval", IntervalSeconds: 60}, Missed: "skip", Overlap: "serialize", ActivateOnAccept: true, Expiration: expiration}}
	p, err := s.ProposeAutomationV2("account", "owner", workspaceID, original.ID, doc, AutomationV2Review{}, fixtureAutomationV2Validator)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.GetAutomationV2Record("account", "owner", workspaceID, original.ID); err != nil || ok {
		t.Fatal("pending authorization", ok, err)
	}
	if _, ok, err := s.GetV3SessionActiveRunIntent(original.ID); err != nil || ok {
		t.Fatal("pending run", ok, err)
	}
	bad := p.AutomationV2Review
	bad.Digest = "stale"
	if _, err := s.AcceptAutomationV2("account", "owner", workspaceID, original.ID, bad, fixtureAutomationV2Validator); err == nil {
		t.Fatal("stale accepted")
	}
	if _, err := s.AcceptAutomationV2("account", "foreign", workspaceID, original.ID, p.AutomationV2Review, fixtureAutomationV2Validator); err == nil {
		t.Fatal("foreign accepted")
	}
	restore := s.SetAutomationV2CommitHookForTest(func(string) error { return errors.New("injected") })
	if _, err := s.AcceptAutomationV2("account", "owner", workspaceID, original.ID, p.AutomationV2Review, fixtureAutomationV2Validator); err == nil {
		t.Fatal("failure accepted")
	}
	restore()
	current, _, err := s.GetSession(original.ID)
	if err != nil || current.AutomationV2 != nil {
		t.Fatal("partial binding", err)
	}
	if _, ok, err := s.GetAutomationV2Record("account", "owner", workspaceID, original.ID); err != nil || ok {
		t.Fatal("partial record", err)
	}
	var wg sync.WaitGroup
	results := make(chan AutomationV2Record, 2)
	failures := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, e := s.AcceptAutomationV2("account", "owner", workspaceID, original.ID, p.AutomationV2Review, fixtureAutomationV2Validator)
			results <- r
			failures <- e
		}()
	}
	wg.Wait()
	close(results)
	close(failures)
	for e := range failures {
		if e != nil {
			t.Fatal(e)
		}
	}
	var accepted AutomationV2Record
	for r := range results {
		if accepted.AutomationID != "" && !reflect.DeepEqual(accepted, r) {
			t.Fatal("distinct receipts")
		}
		accepted = r
	}
	if !reflect.DeepEqual(accepted.Document, doc) || accepted.Authorization != expiration || !accepted.Enabled {
		t.Fatal("review changed", accepted)
	}
	// Requirement: accepted authoring chats reject free-form turns atomically.
	// Threat: a late user message starts an ordinary run and contaminates the
	// accepted conversation. The real mutation boundary must publish nothing.
	beforeSeq, err := s.readV3SessionSequence(original.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: original.ID, AccountScopeID: "account", UserID: "owner", Kind: V3SessionMutationAppendMessage, ClientRequestID: "late-user", PayloadHash: "late-user", Message: &MessageSnapshot{ID: "late-user", Role: "user", Content: "Change the task now"}})
	if err == nil {
		t.Fatal("accepted automation accepted free-form conversation")
	}
	afterSeq, err := s.readV3SessionSequence(original.ID)
	if err != nil || afterSeq != beforeSeq {
		t.Fatal("rejected message published state", err)
	}
	if _, exists, err := s.GetV3SessionActiveRunIntent(original.ID); err != nil || exists {
		t.Fatal("rejected message admitted a run", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s = NewSessionStore(db)
	replayed, err := s.AcceptAutomationV2("account", "owner", workspaceID, original.ID, p.AutomationV2Review, fixtureAutomationV2Validator)
	if err != nil || !reflect.DeepEqual(accepted, replayed) {
		t.Fatal("restart replay changed", err)
	}
}

// Purpose: policy validation rejects ambiguous schedules and implicit finite
// grants at the narrow pure validator before any durable mutation is possible.
func TestAutomationV2Policy(t *testing.T) {
	base := AutomationV2Settings{SchemaVersion: 2, Schedule: AutomationV2Schedule{Kind: "interval", IntervalSeconds: 60}, Missed: "coalesce", Overlap: "independent", ActivateOnAccept: true, Expiration: AutomationV2Expiration{Kind: "at", ExpiresAt: 2000}}
	if err := ValidateAutomationV2Settings(&base, 1000); err != nil {
		t.Fatal(err)
	}
	for _, schedule := range []AutomationV2Schedule{{Kind: "interval", IntervalSeconds: 59}, {Kind: "cron", Cron: "0 0 1 * 1", Timezone: "UTC"}, {Kind: "cron", Cron: "0-5 * * * *", Timezone: "UTC"}, {Kind: "cron", Cron: "* * * * *", Timezone: "Local"}} {
		a := base
		a.Schedule = schedule
		if ValidateAutomationV2Settings(&a, 1000) == nil {
			t.Fatal("invalid schedule accepted", schedule)
		}
	}
	if ValidateAutomationV2Settings(&base, 2000) == nil {
		t.Fatal("expired grant accepted")
	}
}

// Purpose: Propose/AcceptAutomationV2 must reject revoked authority, corrupted
// durable reviews and stale/changed replay without ANY database writes. A tiny
// real-store key snapshot proves events, outbox, plans and bindings roll back too.
func TestAutomationV2AuthorityIntegrityReplay(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := NewSessionStore(db)
	identity := NewIdentityStore(db)
	if _, err := identity.PutUser(UserRecord{ID: "owner", Username: "owner"}); err != nil {
		t.Fatal(err)
	}
	if _, err := identity.PutAccountScope(AccountScopeRecord{ID: "account", Type: AccountScopeTypePersonal, CreatedByUserID: "owner"}); err != nil {
		t.Fatal(err)
	}
	member := AccountUserRecord{ID: "membership", AccountScopeID: "account", UserID: "owner", Status: "active"}
	workspace, err := NewWorkspaceStore(db).AddForAccount("account", t.TempDir(), "Workspace")
	if err != nil {
		t.Fatal(err)
	}
	available := true
	if err := s.CreateSession(SessionSnapshot{ID: "conversation", AccountScopeID: "account", UserID: "owner", WorkspacePath: workspace.Path, WorkspaceGrants: []WorkspaceGrant{{Kind: WorkspaceGrantPrimary, WorkspaceID: workspace.WorkspaceID, Path: workspace.Path, Available: &available}}}); err != nil {
		t.Fatal(err)
	}
	doc := SessionPlanDocument{Title: "Review", Info: SessionPlanInfo{Goal: "Work"}, Checkpoints: []SessionPlanCheckpoint{{ID: "cp-1", Title: "Work", Objective: "Implement", Status: "pending", Order: 1, AcceptanceCriteria: []string{"Works"}}}, AutomationV2: &AutomationV2Settings{SchemaVersion: 2, Schedule: AutomationV2Schedule{Kind: "interval", IntervalSeconds: 60}, Missed: "skip", Overlap: "serialize", ActivateOnAccept: true, Expiration: AutomationV2Expiration{Kind: "indefinite"}}}
	snapshot := func() map[string]string {
		t.Helper()
		it, err := db.db.NewIter(nil)
		if err != nil {
			t.Fatal(err)
		}
		defer it.Close()
		out := map[string]string{}
		for ok := it.First(); ok; ok = it.Next() {
			out[string(it.Key())] = string(it.Value())
		}
		if err := it.Error(); err != nil {
			t.Fatal(err)
		}
		return out
	}
	reject := func(call func() error) {
		t.Helper()
		before := snapshot()
		if err := call(); err == nil {
			t.Fatal("operation unexpectedly succeeded")
		}
		if !reflect.DeepEqual(before, snapshot()) {
			t.Fatal("rejection wrote durable state")
		}
	}
	propose := func(expected AutomationV2Review) (AutomationV2Proposal, error) {
		return s.ProposeAutomationV2("account", "owner", workspace.WorkspaceID, "conversation", doc, expected, fixtureAutomationV2Validator)
	}
	reject(func() error { _, err := propose(AutomationV2Review{}); return err }) // absent membership
	if _, err := identity.PutAccountUser(member); err != nil {
		t.Fatal(err)
	}
	// A missing or failing canonical validator must fail before any batch writes.
	reject(func() error {
		_, err := s.ProposeAutomationV2("account", "owner", workspace.WorkspaceID, "conversation", doc, AutomationV2Review{}, nil)
		return err
	})
	reject(func() error {
		_, err := s.ProposeAutomationV2("account", "owner", workspace.WorkspaceID, "conversation", doc, AutomationV2Review{}, func(*SessionPlanDocument) error { return errors.New("invalid executable") })
		return err
	})
	for _, review := range []AutomationV2Review{{ProposalID: "partial"}, {Revision: 1}, {Digest: "partial"}, {ProposalID: "overflow", Revision: ^uint64(0), Digest: "digest"}} {
		reject(func() error { _, err := propose(review); return err })
	}
	p, err := propose(AutomationV2Review{})
	if err != nil {
		t.Fatal(err)
	}
	doc.Title = "Second"
	second, err := propose(p.AutomationV2Review)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := propose(p.AutomationV2Review)
	if err != nil || !reflect.DeepEqual(second, replayed) {
		t.Fatal("replay regenerated proposal", err)
	}
	doc.Title = "Changed duplicate"
	reject(func() error { _, err := propose(p.AutomationV2Review); return err })
	third, err := propose(second.AutomationV2Review)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []AutomationV2Proposal{p, second, third} {
		var historical AutomationV2Proposal
		ok, err := db.GetJSON(automationV2Key("history", "account", "conversation")+fmt.Sprintf("/%x/%020d", want.ProposalID, want.Revision), &historical)
		if err != nil || !ok || !reflect.DeepEqual(want, historical) {
			t.Fatal("history lost", err)
		}
	}
	doc.Title = "Second"
	reject(func() error { _, err := propose(p.AutomationV2Review); return err })
	accept := func() error {
		_, err := s.AcceptAutomationV2("account", "owner", workspace.WorkspaceID, "conversation", third.AutomationV2Review, fixtureAutomationV2Validator)
		return err
	}
	for _, mutate := range []func(*AutomationV2Proposal){
		func(p *AutomationV2Proposal) { p.AccountID = "foreign" },
		func(p *AutomationV2Proposal) { p.UserID = "foreign" },
		func(p *AutomationV2Proposal) { p.WorkspaceID = "foreign" },
		func(p *AutomationV2Proposal) { p.SessionID = "foreign" },
		func(p *AutomationV2Proposal) { p.Document.Title = "tampered" },
	} {
		bad := third
		mutate(&bad)
		if err := db.PutJSON(automationV2Key("proposal", "account", "conversation"), bad); err != nil {
			t.Fatal(err)
		}
		reject(accept)
	}
	if err := db.PutJSON(automationV2Key("proposal", "account", "conversation"), third); err != nil {
		t.Fatal(err)
	}
	restore := s.SetAutomationV2CommitHookForTest(func(string) error { return errors.New("injected") })
	reject(accept)
	restore()
	if err := accept(); err != nil {
		t.Fatal(err)
	}
	member.Status = "revoked"
	if _, err := identity.PutAccountUser(member); err != nil {
		t.Fatal(err)
	}
	reject(accept)
	reject(func() error {
		_, _, err := s.GetAutomationV2Proposal("account", "owner", workspace.WorkspaceID, "conversation")
		return err
	})
	reject(func() error {
		_, _, err := s.ListAutomationV2Records("account", "owner", workspace.WorkspaceID, "", 10)
		return err
	})
	member.Status = "active"
	if _, err := identity.PutAccountUser(member); err != nil {
		t.Fatal(err)
	}
	reject(func() error {
		_, err := s.AcceptAutomationV2("foreign", "owner", workspace.WorkspaceID, "conversation", third.AutomationV2Review, fixtureAutomationV2Validator)
		return err
	})
	if err := NewWorkspaceStore(db).DeleteForAccount("account", "owner", workspace.Path); err != nil {
		t.Fatal(err)
	}
	reject(accept)
	reject(func() error {
		_, _, err := s.ListAutomationV2Records("account", "owner", workspace.WorkspaceID, "", 10)
		return err
	})
}

func TestAutomationV2ArchivedAndDeletedSessionLifecycle(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := NewSessionStore(db)
	identity := NewIdentityStore(db)
	if _, err := identity.PutUser(UserRecord{ID: "owner", Username: "owner"}); err != nil {
		t.Fatal(err)
	}
	if _, err := identity.PutAccountScope(AccountScopeRecord{ID: "account", Type: AccountScopeTypePersonal, CreatedByUserID: "owner"}); err != nil {
		t.Fatal(err)
	}
	if _, err := identity.PutAccountUser(AccountUserRecord{ID: "membership", AccountScopeID: "account", UserID: "owner", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	workspace, err := NewWorkspaceStore(db).AddForAccount("account", t.TempDir(), "Workspace")
	if err != nil {
		t.Fatal(err)
	}
	available := true
	if err := s.CreateSession(SessionSnapshot{ID: "auto-session", AccountScopeID: "account", UserID: "owner", WorkspacePath: workspace.Path, WorkspaceGrants: []WorkspaceGrant{{Kind: WorkspaceGrantPrimary, WorkspaceID: workspace.WorkspaceID, Path: workspace.Path, Available: &available}}}); err != nil {
		t.Fatal(err)
	}
	doc := SessionPlanDocument{Title: "Review", Info: SessionPlanInfo{Goal: "Work"}, Checkpoints: []SessionPlanCheckpoint{{ID: "cp-1", Title: "Work", Objective: "Implement", Status: "pending", Order: 1, AcceptanceCriteria: []string{"Works"}}}, AutomationV2: &AutomationV2Settings{SchemaVersion: 2, Schedule: AutomationV2Schedule{Kind: "interval", IntervalSeconds: 60}, Missed: "skip", Overlap: "serialize", ActivateOnAccept: true, Expiration: AutomationV2Expiration{Kind: "indefinite"}}}
	p, err := s.ProposeAutomationV2("account", "owner", workspace.WorkspaceID, "auto-session", doc, AutomationV2Review{}, fixtureAutomationV2Validator)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := s.AcceptAutomationV2("account", "owner", workspace.WorkspaceID, "auto-session", p.AutomationV2Review, fixtureAutomationV2Validator)
	if err != nil {
		t.Fatal(err)
	}
	if accepted.Archived {
		t.Fatal("expected newly accepted automation not to be archived")
	}

	// Active listing
	activeRows, _, err := s.ListAutomationV2Records("account", "owner", workspace.WorkspaceID, "", 10)
	if err != nil || len(activeRows) != 1 || activeRows[0].Archived {
		t.Fatalf("expected 1 active unarchived record: rows=%+v, err=%v", activeRows, err)
	}

	// Archive the session
	if err := s.ArchiveSession("auto-session"); err != nil {
		t.Fatalf("archive session failed: %v", err)
	}

	// Active listing (exclude) should return 0 records without conflict error
	excludeRows, _, err := s.ListAutomationV2Records("account", "owner", workspace.WorkspaceID, "", 10, "exclude")
	if err != nil || len(excludeRows) != 0 {
		t.Fatalf("expected 0 exclude records: rows=%+v, err=%v", excludeRows, err)
	}

	// Archived only listing should return 1 archived record
	onlyRows, _, err := s.ListAutomationV2Records("account", "owner", workspace.WorkspaceID, "", 10, "only")
	if err != nil || len(onlyRows) != 1 || !onlyRows[0].Archived {
		t.Fatalf("expected 1 archived record: rows=%+v, err=%v", onlyRows, err)
	}

	// Include listing should return 1 archived record
	includeRows, _, err := s.ListAutomationV2Records("account", "owner", workspace.WorkspaceID, "", 10, "include")
	if err != nil || len(includeRows) != 1 || !includeRows[0].Archived {
		t.Fatalf("expected 1 include record: rows=%+v, err=%v", includeRows, err)
	}

	// GetAutomationV2Record should return the record with Archived=true
	rec, found, err := s.GetAutomationV2Record("account", "owner", workspace.WorkspaceID, "auto-session")
	if err != nil || !found || !rec.Archived || rec.ArchivedAt <= 0 {
		t.Fatalf("expected found archived record with timestamp: found=%v, rec=%+v, err=%v", found, rec, err)
	}

	// Unarchive the session
	tombstone, ok, err := s.GetV3SessionTombstone("auto-session")
	if err != nil || !ok {
		t.Fatalf("tombstone missing: ok=%v, err=%v", ok, err)
	}
	if err := s.ReactivateArchivedSessions([]string{"auto-session"}, map[string]int64{"auto-session": tombstone.UpdatedAt}); err != nil {
		t.Fatalf("reactivate failed: %v", err)
	}

	// Active listing should now return 1 record again
	restoredRows, _, err := s.ListAutomationV2Records("account", "owner", workspace.WorkspaceID, "", 10)
	if err != nil || len(restoredRows) != 1 || restoredRows[0].Archived {
		t.Fatalf("expected 1 active restored record: rows=%+v, err=%v", restoredRows, err)
	}

	// Delete the session permanently
	if err := s.DeleteSessions([]string{"auto-session"}); err != nil {
		t.Fatalf("delete sessions failed: %v", err)
	}

	// List should return 0 records and no error
	deletedListRows, _, err := s.ListAutomationV2Records("account", "owner", workspace.WorkspaceID, "", 10, "include")
	if err != nil || len(deletedListRows) != 0 {
		t.Fatalf("expected 0 records after deletion: rows=%+v, err=%v", deletedListRows, err)
	}

	// Get should return conflict error or not found
	_, foundAfterDelete, _ := s.GetAutomationV2Record("account", "owner", workspace.WorkspaceID, "auto-session")
	if foundAfterDelete {
		t.Fatal("expected record not to be found after session deletion")
	}
}

func TestAutomationV2Decline(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := NewSessionStore(db)
	identity := NewIdentityStore(db)
	if _, err := identity.PutUser(UserRecord{ID: "owner", Username: "owner"}); err != nil {
		t.Fatal(err)
	}
	if _, err := identity.PutAccountScope(AccountScopeRecord{ID: "account", Type: AccountScopeTypePersonal, CreatedByUserID: "owner"}); err != nil {
		t.Fatal(err)
	}
	if _, err := identity.PutAccountUser(AccountUserRecord{ID: "membership", AccountScopeID: "account", UserID: "owner", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	workspace, err := NewWorkspaceStore(db).AddForAccount("account", t.TempDir(), "Workspace")
	if err != nil {
		t.Fatal(err)
	}
	available := true
	if err := s.CreateSession(SessionSnapshot{ID: "decline-session", AccountScopeID: "account", UserID: "owner", WorkspacePath: workspace.Path, WorkspaceGrants: []WorkspaceGrant{{Kind: WorkspaceGrantPrimary, WorkspaceID: workspace.WorkspaceID, Path: workspace.Path, Available: &available}}}); err != nil {
		t.Fatal(err)
	}

	doc := SessionPlanDocument{
		Title: "Pending to decline",
		Info:  SessionPlanInfo{Goal: "Goal"},
		AutomationV2: &AutomationV2Settings{
			SchemaVersion:    2,
			Schedule:         AutomationV2Schedule{Kind: "interval", IntervalSeconds: 120},
			Missed:           "skip",
			Overlap:          "serialize",
			ActivateOnAccept: true,
			Expiration:       AutomationV2Expiration{Kind: "indefinite"},
		},
		Checkpoints: []SessionPlanCheckpoint{{ID: "cp-1", Title: "Task", Objective: "Run", Status: "pending", Order: 1, AcceptanceCriteria: []string{"Done"}}},
	}

	proposal, err := s.ProposeAutomationV2("account", "owner", workspace.WorkspaceID, "decline-session", doc, AutomationV2Review{}, fixtureAutomationV2Validator)
	if err != nil {
		t.Fatal(err)
	}

	// Stale review decline fails with conflict
	staleReview := AutomationV2Review{ProposalID: proposal.ProposalID, Revision: proposal.Revision + 1, Digest: "deadbeef"}
	if err := s.DeclineAutomationV2("account", "owner", workspace.WorkspaceID, "decline-session", staleReview); !errors.Is(err, ErrAutomationV2Conflict) {
		t.Fatalf("expected conflict for stale decline, got: %v", err)
	}

	// Foreign user decline fails
	if err := s.DeclineAutomationV2("account", "foreign", workspace.WorkspaceID, "decline-session", proposal.AutomationV2Review); err == nil {
		t.Fatal("expected error for foreign decline")
	}

	// Valid decline
	if err := s.DeclineAutomationV2("account", "owner", workspace.WorkspaceID, "decline-session", proposal.AutomationV2Review); err != nil {
		t.Fatalf("decline failed: %v", err)
	}

	// Proposal is deleted
	if _, found, err := s.GetAutomationV2Proposal("account", "owner", workspace.WorkspaceID, "decline-session"); err != nil || found {
		t.Fatalf("expected proposal not found: found=%v err=%v", found, err)
	}

	// Permission is denied
	ps := NewPermissionStore(db)
	perm, found, err := ps.GetPermission("decline-session", AutomationV2PermissionID(proposal.ProposalID))
	if err != nil || !found || perm.Status != PermissionStatusDenied || perm.Decision != "decline_automation" {
		t.Fatalf("expected permission denied: found=%v, perm=%+v, err=%v", found, perm, err)
	}

	// Plan snapshot is declined
	plan, found, err := s.GetPlan("decline-session", proposal.ProposalID)
	if err != nil || !found || plan.Status != "declined" || plan.ApprovalState != "declined" {
		t.Fatalf("expected plan declined: found=%v, plan=%+v, err=%v", found, plan, err)
	}

	// Subsequent acceptance fails
	if _, err := s.AcceptAutomationV2("account", "owner", workspace.WorkspaceID, "decline-session", proposal.AutomationV2Review, fixtureAutomationV2Validator); !errors.Is(err, ErrAutomationV2Conflict) {
		t.Fatalf("expected conflict on accepting declined proposal, got: %v", err)
	}
}

// Store tests inject a bounded fixture validator; API/session tests exercise the
// canonical executable validator. This is not a production alternate validator.
func fixtureAutomationV2Validator(doc *SessionPlanDocument) error {
	if doc == nil || len(doc.Checkpoints) == 0 {
		return errors.New("incomplete fixture")
	}
	return nil
}

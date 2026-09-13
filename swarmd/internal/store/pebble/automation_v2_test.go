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

// Store tests inject a bounded fixture validator; API/session tests exercise the
// canonical executable validator. This is not a production alternate validator.
func fixtureAutomationV2Validator(doc *SessionPlanDocument) error {
	if doc == nil || len(doc.Checkpoints) == 0 {
		return errors.New("incomplete fixture")
	}
	return nil
}

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
	// Requirement: acceptance grants a separate worker and keeps authoring chat
	// writable. Threat: accepting the worker silently commandeers the authoring
	// session or converts the next ordinary message into scheduled execution.
	current, _, err = s.GetSession(original.ID)
	if err != nil || current.AutomationV2 != nil || !accepted.Independent {
		t.Fatal("authoring chat was bound to worker", err)
	}
	beforeSeq, err := s.readV3SessionSequence(original.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: original.ID, AccountScopeID: "account", UserID: "owner", Kind: V3SessionMutationAppendMessage, ClientRequestID: "late-user", PayloadHash: "late-user", Message: &MessageSnapshot{ID: "late-user", Role: "user", Content: "Change the task now"}})
	if err != nil {
		t.Fatal("accepted worker blocked ordinary conversation", err)
	}
	afterSeq, err := s.readV3SessionSequence(original.ID)
	if err != nil || afterSeq <= beforeSeq {
		t.Fatal("ordinary message was not published", err)
	}
	if _, exists, err := s.GetV3SessionActiveRunIntent(original.ID); err != nil || exists {
		t.Fatal("ordinary message admitted a worker run", err)
	}
	if _, err := s.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: original.ID, AccountScopeID: "account", UserID: "owner", Kind: V3SessionMutationRecordRunIntent, ClientRequestID: "ordinary-run", PayloadHash: "ordinary-run", RunIntent: &V3SessionRunIntent{RunID: "ordinary-run", Status: V3RunIntentPendingExecutor}}); err != nil {
		t.Fatal("accepted worker blocked ordinary chat execution", err)
	}
	if run, exists, err := s.GetV3SessionActiveRunIntent(original.ID); err != nil || !exists || run.RunID != "ordinary-run" || run.PlanID != "" {
		t.Fatal("chat run became worker execution", err)
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

// Requirement: accepting a pending worker must not depend on the authoring run
// having finished. Threat: a valid review gets a spurious 409 while its proposal
// tool call is still active. The real session mutation/store boundary proves the
// exact review succeeds during a running intent, while stale and foreign reviews
// cannot create a binding, receipt, or additional event.
func TestAutomationV2AcceptDuringAuthoringRun(t *testing.T) {
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
	if err := s.CreateSession(SessionSnapshot{ID: "author", AccountScopeID: "account", UserID: "owner", WorkspacePath: workspace.Path, WorkspaceGrants: []WorkspaceGrant{{Kind: WorkspaceGrantPrimary, WorkspaceID: workspace.WorkspaceID, Path: workspace.Path, Available: &available}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: "author", AccountScopeID: "account", UserID: "owner", Kind: V3SessionMutationRecordRunIntent, ClientRequestID: "author-pending", PayloadHash: "author-pending", RunIntent: &V3SessionRunIntent{RunID: "run-author", Status: V3RunIntentPendingExecutor}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: "author", AccountScopeID: "account", UserID: "owner", Kind: V3SessionMutationRecordRunIntent, ClientRequestID: "author-running", PayloadHash: "author-running", RunIntent: &V3SessionRunIntent{RunID: "run-author", Status: V3RunIntentRunning}}); err != nil {
		t.Fatal(err)
	}
	doc := SessionPlanDocument{Title: "Triggered worker", Info: SessionPlanInfo{Goal: "Run on demand"}, Checkpoints: []SessionPlanCheckpoint{{ID: "cp-1", Title: "Work", Objective: "Implement", Status: "pending", Order: 1, AcceptanceCriteria: []string{"Works"}}}, AutomationV2: &AutomationV2Settings{SchemaVersion: 2, Schedule: AutomationV2Schedule{Kind: "trigger"}, Missed: "skip", Overlap: "serialize", ActivateOnAccept: true, Expiration: AutomationV2Expiration{Kind: "indefinite"}}}
	proposal, err := s.ProposeAutomationV2("account", "owner", workspace.WorkspaceID, "author", doc, AutomationV2Review{}, fixtureAutomationV2Validator)
	if err != nil {
		t.Fatal(err)
	}
	before, err := s.readV3SessionSequence("author")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		user   string
		review AutomationV2Review
	}{{"owner", AutomationV2Review{ProposalID: proposal.ProposalID, Revision: proposal.Revision, Digest: "stale"}}, {"foreign", proposal.AutomationV2Review}} {
		if _, err := s.AcceptAutomationV2("account", tc.user, workspace.WorkspaceID, "author", tc.review, fixtureAutomationV2Validator); !errors.Is(err, ErrAutomationV2Conflict) {
			t.Fatalf("stale/foreign review: %v", err)
		}
		seq, err := s.readV3SessionSequence("author")
		if err != nil || seq != before {
			t.Fatalf("rejected acceptance changed events: %d != %d: %v", seq, before, err)
		}
		if session, _, err := s.GetSession("author"); err != nil || session.AutomationV2 != nil {
			t.Fatalf("rejected acceptance bound worker: %v", err)
		}
		if _, found, err := s.GetAutomationV2Record("account", "owner", workspace.WorkspaceID, "author"); err != nil || found {
			t.Fatalf("rejected acceptance created record: %v", err)
		}
	}
	accepted, err := s.AcceptAutomationV2("account", "owner", workspace.WorkspaceID, "author", proposal.AutomationV2Review, fixtureAutomationV2Validator)
	if err != nil || !accepted.Enabled || accepted.Digest != proposal.Digest {
		t.Fatalf("valid pending review rejected during authoring: %+v %v", accepted, err)
	}
	if run, found, err := s.GetV3SessionActiveRunIntent("author"); err != nil || !found || run.RunID != "run-author" {
		t.Fatalf("authoring run was changed by acceptance: %+v %v", run, err)
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
	// A revised accepted worker retains its identity and the original chat
	// remains writable. Old review bytes cannot activate a newer revision.
	initial, found, err := s.GetAutomationV2Record("account", "owner", workspace.WorkspaceID, "conversation")
	if err != nil || !found || !initial.Independent {
		t.Fatal("independent acceptance missing", err)
	}
	fourthDoc := third.Document
	fourthDoc.Title = "Revised worker instructions"
	fourth, err := s.ProposeAutomationV2("account", "owner", workspace.WorkspaceID, "conversation", fourthDoc, third.AutomationV2Review, fixtureAutomationV2Validator)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AcceptAutomationV2("account", "owner", workspace.WorkspaceID, "conversation", third.AutomationV2Review, fixtureAutomationV2Validator); !errors.Is(err, ErrAutomationV2Conflict) {
		t.Fatal("stale worker acceptance changed accepted policy", err)
	}
	revised, err := s.AcceptAutomationV2("account", "owner", workspace.WorkspaceID, "conversation", fourth.AutomationV2Review, fixtureAutomationV2Validator)
	if err != nil || revised.AutomationID != initial.AutomationID || !revised.Independent || revised.Generation <= initial.Generation {
		t.Fatal("independent revision lost worker identity", err)
	}
	chat, found, err := s.GetSession("conversation")
	if err != nil || !found || chat.AutomationV2 != nil {
		t.Fatal("revision commandeered authoring chat", err)
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

	// Author-chat archive suspends its worker under the existing lifecycle.
	excludeRows, _, err := s.ListAutomationV2Records("account", "owner", workspace.WorkspaceID, "", 10, "exclude")
	if err != nil || len(excludeRows) != 0 {
		t.Fatalf("archived worker remained active: rows=%+v, err=%v", excludeRows, err)
	}

	// Archived-only discovery retains the worker and its ownership.
	onlyRows, _, err := s.ListAutomationV2Records("account", "owner", workspace.WorkspaceID, "", 10, "only")
	if err != nil || len(onlyRows) != 1 || !onlyRows[0].Archived {
		t.Fatalf("archived worker not discoverable: rows=%+v, err=%v", onlyRows, err)
	}

	// Include listing retains the archived worker receipt.
	includeRows, _, err := s.ListAutomationV2Records("account", "owner", workspace.WorkspaceID, "", 10, "include")
	if err != nil || len(includeRows) != 1 || !includeRows[0].Archived {
		t.Fatalf("expected archived record: rows=%+v, err=%v", includeRows, err)
	}

	// Direct lookup reports archived state without losing worker identity.
	rec, found, err := s.GetAutomationV2Record("account", "owner", workspace.WorkspaceID, "auto-session")
	if err != nil || !found || !rec.Archived || rec.ArchivedAt <= 0 {
		t.Fatalf("worker archive receipt missing: found=%v, rec=%+v, err=%v", found, rec, err)
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

// Purpose: legacy Automation V2 records created before WorkerV2 support must retain valid
// document digests and pass integrity checks during ListAutomationV2Records and GetAutomationV2Record.
func TestAutomationV2LegacyDocumentDigestCompatibility(t *testing.T) {
	path := t.TempDir()
	db, err := Open(path)
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

	w, err := NewWorkspaceStore(db).AddForAccount("account", t.TempDir(), "Workspace")
	if err != nil {
		t.Fatal(err)
	}
	yes := true
	if err = s.CreateSession(SessionSnapshot{ID: "legacy-session", AccountScopeID: "account", UserID: "owner", Mode: "auto", WorkspacePath: w.Path, WorkspaceGrants: []WorkspaceGrant{{Kind: WorkspaceGrantPrimary, WorkspaceID: w.WorkspaceID, Path: w.Path, Available: &yes}}}); err != nil {
		t.Fatal(err)
	}

	// Create legacy document where WorkerV2 is nil and digest was computed strictly without WorkerV2.
	legacyDoc := SessionPlanDocument{
		Title: "Legacy Automation",
		Info:  SessionPlanInfo{Goal: "Do legacy work"},
		Checkpoints: []SessionPlanCheckpoint{{
			ID: "cp-1", Title: "Legacy Checkpoint", Objective: "Run check", Status: "pending", Order: 1, AcceptanceCriteria: []string{"Done"},
		}},
		AutomationV2: &AutomationV2Settings{
			SchemaVersion:    2,
			Schedule:         AutomationV2Schedule{Kind: "interval", IntervalSeconds: 300},
			Missed:           "skip",
			Overlap:          "serialize",
			ActivateOnAccept: true,
			Expiration:       AutomationV2Expiration{Kind: "indefinite"},
		},
	}
	p, err := s.ProposeAutomationV2("account", "owner", w.WorkspaceID, "legacy-session", legacyDoc, AutomationV2Review{}, fixtureAutomationV2Validator)
	if err != nil {
		t.Fatalf("ProposeAutomationV2 failed for legacy proposal: %v", err)
	}

	accepted, err := s.AcceptAutomationV2("account", "owner", w.WorkspaceID, "legacy-session", p.AutomationV2Review, fixtureAutomationV2Validator)
	if err != nil {
		t.Fatalf("AcceptAutomationV2 failed for legacy proposal: %v", err)
	}
	if accepted.AutomationID == "" {
		t.Fatal("empty automation_id on accepted record")
	}

	// ListAutomationV2Records must succeed and return the record without conflict error
	records, _, err := s.ListAutomationV2Records("account", "owner", w.WorkspaceID, "", 20)
	if err != nil {
		t.Fatalf("ListAutomationV2Records failed with legacy record: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	if records[0].SessionID != "legacy-session" {
		t.Fatalf("unexpected record session_id: %s", records[0].SessionID)
	}
	// A pre-existing bound record without the independent marker retains its
	// read-only chat and archive behavior until an exact reviewed revision.
	legacy := accepted
	legacy.Independent = false
	if err := db.PutJSON(automationV2Key("accepted", "account", "legacy-session"), legacy); err != nil {
		t.Fatal(err)
	}
	chat, _, err := s.GetSession("legacy-session")
	if err != nil {
		t.Fatal(err)
	}
	chat.AutomationV2 = &SessionAutomationV2Binding{AutomationID: legacy.AutomationID, WorkspaceID: w.WorkspaceID, Digest: legacy.Digest}
	if err := s.UpdateSession(chat); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: chat.ID, AccountScopeID: "account", UserID: "owner", Kind: V3SessionMutationAppendMessage, ClientRequestID: "legacy-user", PayloadHash: "legacy-user", Message: &MessageSnapshot{ID: "legacy-user", Role: "user", Content: "Still read-only"}}); err == nil {
		t.Fatal("legacy bound chat unexpectedly became writable")
	}
	revisionDoc := legacyDoc
	revisionDoc.Title = "Reviewed independent revision"
	updated, err := s.ProposeAutomationV2("account", "owner", w.WorkspaceID, chat.ID, revisionDoc, p.AutomationV2Review, fixtureAutomationV2Validator)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AcceptAutomationV2("account", "owner", w.WorkspaceID, chat.ID, p.AutomationV2Review, fixtureAutomationV2Validator); !errors.Is(err, ErrAutomationV2Conflict) {
		t.Fatal("stale legacy review accepted", err)
	}
	migrated, err := s.AcceptAutomationV2("account", "owner", w.WorkspaceID, chat.ID, updated.AutomationV2Review, fixtureAutomationV2Validator)
	if err != nil || migrated.AutomationID != legacy.AutomationID || !migrated.Independent {
		t.Fatal("reviewed legacy revision did not detach", err)
	}
	chat, _, err = s.GetSession(chat.ID)
	if err != nil || chat.AutomationV2 != nil {
		t.Fatal("legacy binding not removed atomically", err)
	}
	if _, err := s.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: chat.ID, AccountScopeID: "account", UserID: "owner", Kind: V3SessionMutationAppendMessage, ClientRequestID: "migrated-user", PayloadHash: "migrated-user", Message: &MessageSnapshot{ID: "migrated-user", Role: "user", Content: "Ordinary chat"}}); err != nil {
		t.Fatal("migrated chat is not writable", err)
	}
}

// Purpose: Sessions must be able to propose and manage workers for any authorized workspace
// without primary-only workspace restrictions, and a single session must be able to host
// multiple distinct workers without key collisions or accidental overwriting.
func TestAutomationV2MultiWorkerAndCrossWorkspace(t *testing.T) {
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

	w1, err := NewWorkspaceStore(db).AddForAccount("account", t.TempDir(), "Workspace 1")
	if err != nil {
		t.Fatal(err)
	}
	w2, err := NewWorkspaceStore(db).AddForAccount("account", t.TempDir(), "Workspace 2")
	if err != nil {
		t.Fatal(err)
	}

	yes := true
	sessionID := "orchestrator-session"
	if err = s.CreateSession(SessionSnapshot{
		ID:             sessionID,
		AccountScopeID: "account",
		UserID:         "owner",
		Mode:           "auto",
		WorkspacePath:  w1.Path,
		WorkspaceGrants: []WorkspaceGrant{{
			Kind:        WorkspaceGrantPrimary,
			WorkspaceID: w1.WorkspaceID,
			Path:        w1.Path,
			Available:   &yes,
		}},
	}); err != nil {
		t.Fatal(err)
	}

	// 1. Cross-workspace worker: session is on w1, but proposes worker for w2!
	worker1Doc := SessionPlanDocument{
		Title: "Worker 1 on Workspace 2",
		Info:  SessionPlanInfo{Goal: "Cross-workspace worker"},
		WorkerV2: &AutomationV2Settings{
			SchemaVersion:    2,
			WorkspaceID:      w2.WorkspaceID,
			Schedule:         AutomationV2Schedule{Kind: "trigger"},
			Missed:           "skip",
			Overlap:          "serialize",
			ActivateOnAccept: true,
			Expiration:       AutomationV2Expiration{Kind: "indefinite"},
		},
		Checkpoints: []SessionPlanCheckpoint{{
			ID:                 "cp-1",
			Title:              "Task 1",
			Objective:          "Do task 1 in w2",
			Status:             "pending",
			Order:              1,
			AcceptanceCriteria: []string{"Done"},
		}},
	}
	p1, err := s.ProposeAutomationV2("account", "owner", w2.WorkspaceID, sessionID, worker1Doc, AutomationV2Review{}, fixtureAutomationV2Validator)
	if err != nil {
		t.Fatalf("cross-workspace proposal failed: %v", err)
	}
	acc1, err := s.AcceptAutomationV2("account", "owner", w2.WorkspaceID, sessionID, p1.AutomationV2Review, fixtureAutomationV2Validator)
	if err != nil {
		t.Fatalf("cross-workspace acceptance failed: %v", err)
	}
	if acc1.WorkspaceID != w2.WorkspaceID {
		t.Fatalf("expected worker 1 workspace %s, got %s", w2.WorkspaceID, acc1.WorkspaceID)
	}
	if acc1.AutomationID == "" {
		t.Fatal("empty automation ID for worker 1")
	}

	// 2. Multi-worker in same session: propose worker 2 for w1 in the same orchestrator-session!
	worker2Doc := SessionPlanDocument{
		Title: "Worker 2 on Workspace 1",
		Info:  SessionPlanInfo{Goal: "Second worker in same session"},
		WorkerV2: &AutomationV2Settings{
			SchemaVersion:    2,
			WorkspaceID:      w1.WorkspaceID,
			Schedule:         AutomationV2Schedule{Kind: "trigger"},
			Missed:           "skip",
			Overlap:          "serialize",
			ActivateOnAccept: true,
			Expiration:       AutomationV2Expiration{Kind: "indefinite"},
		},
		Checkpoints: []SessionPlanCheckpoint{{
			ID:                 "cp-2",
			Title:              "Task 2",
			Objective:          "Do task 2 in w1",
			Status:             "pending",
			Order:              1,
			AcceptanceCriteria: []string{"Done"},
		}},
	}
	p2, err := s.ProposeAutomationV2("account", "owner", w1.WorkspaceID, sessionID, worker2Doc, AutomationV2Review{}, fixtureAutomationV2Validator)
	if err != nil {
		t.Fatalf("second worker proposal failed: %v", err)
	}
	if p2.ProposalID == p1.ProposalID {
		t.Fatalf("worker 2 re-used worker 1 proposal ID: %s", p2.ProposalID)
	}
	acc2, err := s.AcceptAutomationV2("account", "owner", w1.WorkspaceID, sessionID, p2.AutomationV2Review, fixtureAutomationV2Validator)
	if err != nil {
		t.Fatalf("second worker acceptance failed: %v", err)
	}
	if acc2.WorkspaceID != w1.WorkspaceID {
		t.Fatalf("expected worker 2 workspace %s, got %s", w1.WorkspaceID, acc2.WorkspaceID)
	}
	if acc2.AutomationID == "" || acc2.AutomationID == acc1.AutomationID {
		t.Fatalf("worker 2 automation ID collision with worker 1: %s == %s", acc2.AutomationID, acc1.AutomationID)
	}

	// 3. Verify discovery and retrieval of both workers
	rec1, ok1, err := s.GetAutomationV2Record("account", "owner", w2.WorkspaceID, acc1.AutomationID)
	if err != nil || !ok1 {
		t.Fatalf("failed to retrieve worker 1: %v (found=%v)", err, ok1)
	}
	if rec1.Document.Title != "Worker 1 on Workspace 2" {
		t.Fatalf("worker 1 corrupted: %s", rec1.Document.Title)
	}

	rec2, ok2, err := s.GetAutomationV2Record("account", "owner", w1.WorkspaceID, acc2.AutomationID)
	if err != nil || !ok2 {
		t.Fatalf("failed to retrieve worker 2: %v (found=%v)", err, ok2)
	}
	if rec2.Document.Title != "Worker 2 on Workspace 1" {
		t.Fatalf("worker 2 corrupted: %s", rec2.Document.Title)
	}

	w2Records, _, err := s.ListAutomationV2Records("account", "owner", w2.WorkspaceID, "", 10)
	if err != nil {
		t.Fatalf("failed to list w2 records: %v", err)
	}
	if len(w2Records) != 1 || w2Records[0].AutomationID != acc1.AutomationID {
		t.Fatalf("unexpected w2 records: %v", w2Records)
	}

	w1Records, _, err := s.ListAutomationV2Records("account", "owner", w1.WorkspaceID, "", 10)
	if err != nil {
		t.Fatalf("failed to list w1 records: %v", err)
	}
	if len(w1Records) != 1 || w1Records[0].AutomationID != acc2.AutomationID {
		t.Fatalf("unexpected w1 records: %v", w1Records)
	}

	// Verify account-wide listing across all workspaces when workspace is empty or "all"
	allRecordsEmpty, _, err := s.ListAutomationV2Records("account", "owner", "", "", 10)
	if err != nil {
		t.Fatalf("failed to list all records with empty workspace: %v", err)
	}
	if len(allRecordsEmpty) != 2 {
		t.Fatalf("expected 2 records across workspaces with empty workspace, got %d", len(allRecordsEmpty))
	}

	allRecordsAll, _, err := s.ListAutomationV2Records("account", "owner", "all", "", 10)
	if err != nil {
		t.Fatalf("failed to list all records with 'all' workspace: %v", err)
	}
	if len(allRecordsAll) != 2 {
		t.Fatalf("expected 2 records across workspaces with 'all', got %d", len(allRecordsAll))
	}

	// 4. Edit worker 1 without affecting worker 2
	revisedDoc1 := worker1Doc
	revisedDoc1.Title = "Worker 1 Revised"
	p1Rev, err := s.ProposeAutomationV2("account", "owner", w2.WorkspaceID, sessionID, revisedDoc1, acc1.AutomationV2Review, fixtureAutomationV2Validator)
	if err != nil {
		t.Fatalf("worker 1 revision proposal failed: %v", err)
	}
	acc1Rev, err := s.AcceptAutomationV2("account", "owner", w2.WorkspaceID, sessionID, p1Rev.AutomationV2Review, fixtureAutomationV2Validator)
	if err != nil {
		t.Fatalf("worker 1 revision acceptance failed: %v", err)
	}
	if acc1Rev.AutomationID != acc1.AutomationID || acc1Rev.Generation != acc1.Generation+1 {
		t.Fatalf("worker 1 revision mismatch: ID %s vs %s, Gen %d vs %d", acc1Rev.AutomationID, acc1.AutomationID, acc1Rev.Generation, acc1.Generation+1)
	}

	// Verify worker 2 is completely unchanged
	rec2After, ok2After, err := s.GetAutomationV2Record("account", "owner", w1.WorkspaceID, acc2.AutomationID)
	if err != nil || !ok2After || rec2After.Generation != 1 || rec2After.Document.Title != "Worker 2 on Workspace 1" {
		t.Fatalf("worker 2 was affected by worker 1 edit: %v", rec2After)
	}

	// 5. Multi-workspace worker visible in both workspaces
	multiWSDoc := SessionPlanDocument{
		Title: "Multi-Workspace Worker",
		Info:  SessionPlanInfo{Goal: "Visible in w1 and w2"},
		WorkerV2: &AutomationV2Settings{
			SchemaVersion:    2,
			WorkspaceID:      w1.WorkspaceID,
			WorkspaceIDs:     []string{w1.WorkspaceID, w2.WorkspaceID},
			Schedule:         AutomationV2Schedule{Kind: "trigger"},
			Missed:           "skip",
			Overlap:          "serialize",
			ActivateOnAccept: true,
			Expiration:       AutomationV2Expiration{Kind: "indefinite"},
		},
		Checkpoints: []SessionPlanCheckpoint{{
			ID:                 "cp-1",
			Title:              "Multi Task",
			Objective:          "Multi workspace task",
			Status:             "pending",
			Order:              1,
			AcceptanceCriteria: []string{"Done"},
		}},
	}
	pMulti, err := s.ProposeAutomationV2("account", "owner", w1.WorkspaceID, sessionID, multiWSDoc, AutomationV2Review{}, fixtureAutomationV2Validator)
	if err != nil {
		t.Fatalf("multi-workspace proposal failed: %v", err)
	}
	accMulti, err := s.AcceptAutomationV2("account", "owner", w1.WorkspaceID, sessionID, pMulti.AutomationV2Review, fixtureAutomationV2Validator)
	if err != nil {
		t.Fatalf("multi-workspace acceptance failed: %v", err)
	}
	// Must appear when listing w1
	w1List, _, err := s.ListAutomationV2Records("account", "owner", w1.WorkspaceID, "", 10)
	if err != nil {
		t.Fatalf("listing w1 failed: %v", err)
	}
	foundInW1 := false
	for _, r := range w1List {
		if r.AutomationID == accMulti.AutomationID {
			foundInW1 = true
			break
		}
	}
	if !foundInW1 {
		t.Fatalf("multi-workspace worker %s not found when listing w1", accMulti.AutomationID)
	}
	// Must ALSO appear when listing w2!
	w2List, _, err := s.ListAutomationV2Records("account", "owner", w2.WorkspaceID, "", 10)
	if err != nil {
		t.Fatalf("listing w2 failed: %v", err)
	}
	foundInW2 := false
	for _, r := range w2List {
		if r.AutomationID == accMulti.AutomationID {
			foundInW2 = true
			break
		}
	}
	if !foundInW2 {
		t.Fatalf("multi-workspace worker %s not found when listing w2", accMulti.AutomationID)
	}
	// Must be retrievable in w2 via GetAutomationV2Record
	recInW2, foundInW2Get, err := s.GetAutomationV2Record("account", "owner", w2.WorkspaceID, accMulti.AutomationID)
	if err != nil || !foundInW2Get || recInW2.AutomationID != accMulti.AutomationID {
		t.Fatalf("multi-workspace worker %s not retrievable in w2: found=%v err=%v", accMulti.AutomationID, foundInW2Get, err)
	}
}

func TestAutomationV2DeduplicateWorkersByStableIdentity(t *testing.T) {
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

	w, err := NewWorkspaceStore(db).AddForAccount("account", t.TempDir(), "Deduplication Workspace")
	if err != nil {
		t.Fatal(err)
	}
	yes := true
	sessionID := "worker-session-dedup"
	if err = s.CreateSession(SessionSnapshot{
		ID:             sessionID,
		AccountScopeID: "account",
		UserID:         "owner",
		Mode:           "auto",
		WorkspacePath:  w.Path,
		WorkspaceGrants: []WorkspaceGrant{{
			Kind:        WorkspaceGrantPrimary,
			WorkspaceID: w.WorkspaceID,
			Path:        w.Path,
			Available:   &yes,
		}},
	}); err != nil {
		t.Fatal(err)
	}

	doc1 := SessionPlanDocument{
		Title: "Specialist Worker",
		Info:  SessionPlanInfo{Goal: "First proposal version"},
		WorkerV2: &AutomationV2Settings{
			SchemaVersion:    2,
			WorkspaceID:      w.WorkspaceID,
			Schedule:         AutomationV2Schedule{Kind: "trigger"},
			Missed:           "skip",
			Overlap:          "serialize",
			ActivateOnAccept: true,
			Expiration:       AutomationV2Expiration{Kind: "indefinite"},
		},
		Checkpoints: []SessionPlanCheckpoint{{
			ID:                 "cp-1",
			Title:              "Task 1",
			Objective:          "Do task 1",
			Status:             "pending",
			Order:              1,
			AcceptanceCriteria: []string{"Done"},
		}},
	}

	p1, err := s.ProposeAutomationV2("account", "owner", w.WorkspaceID, sessionID, doc1, AutomationV2Review{}, fixtureAutomationV2Validator)
	if err != nil {
		t.Fatalf("first proposal failed: %v", err)
	}
	acc1, err := s.AcceptAutomationV2("account", "owner", w.WorkspaceID, sessionID, p1.AutomationV2Review, fixtureAutomationV2Validator)
	if err != nil {
		t.Fatalf("first acceptance failed: %v", err)
	}

	// Verify initial listing shows exactly 1 worker
	list1, _, err := s.ListAutomationV2Records("account", "owner", w.WorkspaceID, "", 10)
	if err != nil {
		t.Fatalf("list 1 failed: %v", err)
	}
	if len(list1) != 1 {
		t.Fatalf("expected 1 record, got %d", len(list1))
	}

	// Simulate an unlinked re-proposal in the same session with new proposal/generation
	doc2 := doc1
	doc2.Info.Goal = "Second updated version of the worker"
	p2, err := s.ProposeAutomationV2("account", "owner", w.WorkspaceID, sessionID, doc2, AutomationV2Review{}, fixtureAutomationV2Validator)
	if err != nil {
		t.Fatalf("second proposal failed: %v", err)
	}
	acc2, err := s.AcceptAutomationV2("account", "owner", w.WorkspaceID, sessionID, p2.AutomationV2Review, fixtureAutomationV2Validator)
	if err != nil {
		t.Fatalf("second acceptance failed: %v", err)
	}

	// ListAutomationV2Records must return exactly 1 record, which must be the newer worker (acc2)
	list2, _, err := s.ListAutomationV2Records("account", "owner", w.WorkspaceID, "", 10)
	if err != nil {
		t.Fatalf("list 2 failed: %v", err)
	}
	if len(list2) != 1 {
		t.Fatalf("expected duplicate to be collapsed to 1 record, got %d", len(list2))
	}
	if list2[0].AutomationID != acc2.AutomationID {
		t.Fatalf("expected active record %s, got %s", acc2.AutomationID, list2[0].AutomationID)
	}

	// Verify that the obsolete acc1 AutomationID key was removed from the store
	var oldRec AutomationV2Record
	foundOld, _ := s.store.GetJSON(automationV2Key("accepted", "account", acc1.AutomationID), &oldRec)
	if foundOld {
		t.Fatalf("expected obsolete worker record %s to be cleaned up from Pebble store", acc1.AutomationID)
	}
}

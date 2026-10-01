package pebblestore

import (
	"errors"
	"path/filepath"
	"testing"
)

// Purpose: ListProjectDesignRequests must discover preexisting admissions and
// historical task attempts without trusting session metadata or leaking another
// principal's work. Real Pebble is the narrowest layer proving pagination,
// reopen, revocation and reference-only hydration postconditions.
func TestProjectDesignCatalogMembershipPaginationAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	ss := NewSessionStore(s)
	p := designTestOwner
	for _, id := range []string{"parent", "historical", "foreign"} {
		user := p.PrincipalID
		if id == "foreign" {
			user = "another"
		}
		_, err := ss.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: id, UserID: user, AccountScopeID: p.AccountID, IdempotencyKey: "create", PayloadHash: "create", Kind: V3SessionMutationCreateSession, Session: &SessionSnapshot{ID: id}})
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"one", "two", "three"} {
		in := designTestSubmit(id, DesignHTML)
		in.Candidates[0].ArtifactID = "artifact-" + id
		if id == "three" {
			in.ParentSessionID = "historical"
		}
		if _, err := s.SubmitDesignRequest(p, in); err != nil {
			t.Fatal(err)
		}
		if _, err := s.SubmitDesignRequest(p, in); err != nil {
			t.Fatal(err)
		}
	}
	project := &ProjectRecord{ID: "project", Name: "Project", PrimarySessionID: "parent"}
	if err := ss.PutProject(p.AccountID, project); err != nil {
		t.Fatal(err)
	}
	task := &ProjectTaskRecord{ID: "task", ProjectID: project.ID, Title: "Task", Agent: "swarm", SessionID: "foreign", Attempts: []ProjectTaskAttempt{{ID: "old", SessionID: "historical"}}}
	if err := ss.PutProjectTask(p.AccountID, task); err != nil {
		t.Fatal(err)
	}
	rows, cursor, err := ss.ListProjectDesignRequests(p, project.ID, "", 1)
	if err != nil || len(rows) != 1 || cursor == "" {
		t.Fatal(rows, cursor, err)
	}
	if rows[0].Request.Candidates[0].Spec.Brief != "" || rows[0].Title == "" {
		t.Fatal("unredacted or untitled projection", rows)
	}
	for _, foreign := range []DesignPrincipal{{AccountID: "other", PrincipalID: p.PrincipalID}, {AccountID: p.AccountID, PrincipalID: "other"}} {
		got, _, err := ss.ListProjectDesignRequests(foreign, project.ID, cursor, 1)
		if err == nil || len(got) != 0 {
			t.Fatal("foreign cursor disclosed rows", got, err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ss = NewSessionStore(s)
	seen := map[string]bool{rows[0].Request.ID: true}
	for pages := 0; cursor != "" && pages < 20; pages++ {
		var got []ProjectDesignEntry
		got, cursor, err = ss.ListProjectDesignRequests(p, project.ID, cursor, 1)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range got {
			if seen[row.Request.ID] {
				t.Fatal("duplicate admission", row)
			}
			seen[row.Request.ID] = true
			if row.Request.ParentSessionID == "historical" && (row.TaskID != "task" || row.AttemptID != "old") {
				t.Fatal(row)
			}
		}
	}
	if cursor != "" || len(seen) != 3 {
		t.Fatal("bounded traversal did not finish", seen, cursor)
	}
	if _, err := ss.UpdateProject(p.AccountID, project.ID, func(v *ProjectRecord) error { v.PrimarySessionID = ""; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := ss.DeleteProjectTask(p.AccountID, project.ID, task.ID); err != nil {
		t.Fatal(err)
	}
	got, next, err := ss.ListProjectDesignRequests(p, project.ID, "", 20)
	if err != nil || len(got) != 0 || next != "" {
		t.Fatal("stale membership leaked", got, next, err)
	}
	if projectID, err := ss.designProjectLocators(p, "parent"); err != nil || len(projectID) != 0 {
		t.Fatal("stale locator authorized", projectID, err)
	}
	if _, _, err := ss.ListProjectDesignRequests(p, "missing", "", 1); !errors.Is(err, ErrDesignNotFound) {
		t.Fatal(err)
	}
}

// Purpose: setDesignProjectInvalidation participates in the canonical V3 batch,
// and failed CAS must not emit project wakeups. Real store mutations prove the
// same committed endpoint is retained for reconnect without a second history.
func TestProjectDesignInvalidationAtomicUpdate(t *testing.T) {
	s := openTaskProgramTestStore(t)
	ss := NewSessionStore(s)
	p := designTestOwner
	_, err := ss.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: "parent", UserID: p.PrincipalID, AccountScopeID: p.AccountID, IdempotencyKey: "create", PayloadHash: "create", Kind: V3SessionMutationCreateSession, Session: &SessionSnapshot{ID: "parent"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := ss.PutProject(p.AccountID, &ProjectRecord{ID: "project", Name: "Project", PrimarySessionID: "parent"}); err != nil {
		t.Fatal(err)
	}
	var notices []V3RealtimeOutboxRecord
	s.SetProjectPublisher(func(record V3RealtimeOutboxRecord) { notices = append(notices, record) })
	r, err := s.SubmitDesignRequest(p, designTestSubmit("request", DesignHTML))
	if err != nil {
		t.Fatal(err)
	}
	r = designTestStart(t, s, r, 0, "child")
	if len(notices) != 1 || notices[0].Event.EventType != ProjectUpdatedEventType {
		t.Fatal(notices)
	}
	stored, found, err := ss.GetV3RealtimeOutbox(notices[0].EndpointSeq)
	if err != nil || !found || string(stored.Event.Payload) != `{"project_id":"project","resource":"designs"}` {
		t.Fatal(stored, err)
	}
	_, err = s.RecordDesignAttempt(p, r.ID, DesignAttemptMutation{IdempotencyKey: "stale", ExpectedRevision: 0, State: DesignFailed, Candidate: 0, ChildSessionID: "child", RunID: "run-child"})
	if err == nil || len(notices) != 1 {
		t.Fatal("failed CAS published", notices, err)
	}
}

// Purpose: project discovery must retain partial success and exact historical
// edit bases rather than resolving to selected/latest. Store-level projection
// assertions are narrower than provider execution and prove reference fidelity.
func TestProjectDesignCatalogPartialAndHistoricalBase(t *testing.T) {
	s := openTaskProgramTestStore(t)
	ss := NewSessionStore(s)
	p := designTestOwner
	_, err := ss.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: "parent", UserID: p.PrincipalID, AccountScopeID: p.AccountID, IdempotencyKey: "create", PayloadHash: "create", Kind: V3SessionMutationCreateSession, Session: &SessionSnapshot{ID: "parent"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := ss.PutProject(p.AccountID, &ProjectRecord{ID: "project", Name: "Project", PrimarySessionID: "parent"}); err != nil {
		t.Fatal(err)
	}
	in := designTestSubmit("original", DesignHTML)
	in.Candidates = append(in.Candidates, DesignCandidateSpec{ArtifactID: "failed-artifact", Kind: DesignHTML, Operation: DesignGenerate, Brief: "Other variant"})
	r, err := s.SubmitDesignRequest(p, in)
	if err != nil {
		t.Fatal(err)
	}
	r = designTestStart(t, s, r, 0, "child")
	r, first := designTestPublish(t, s, r, 0, []byte("<html>first</html>"))
	r = designTestStart(t, s, r, 1, "failed-child")
	r, err = s.RecordDesignAttempt(p, r.ID, DesignAttemptMutation{IdempotencyKey: "failure", ExpectedRevision: r.Revision, Candidate: 1, State: DesignFailed, ChildSessionID: "failed-child", RunID: "failed-child-run", ReasonCode: "provider_failed"})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"second", "historical-edit"} {
		edit := designTestSubmit(id, DesignHTML)
		edit.Candidates[0].Operation, edit.Candidates[0].Base = DesignEdit, &first
		next, err := s.SubmitDesignRequest(p, edit)
		if err != nil {
			t.Fatal(err)
		}
		if id == "second" {
			next = designTestStart(t, s, next, 0, "second-child")
			_, _ = designTestPublish(t, s, next, 0, []byte("<html>second</html>"))
		}
	}
	rows, _, err := ss.ListProjectDesignRequests(p, "project", "", 20)
	if err != nil || len(rows) != 3 {
		t.Fatal(rows, err)
	}
	for _, row := range rows {
		switch row.Request.ID {
		case "original":
			if row.Request.State != DesignPartial || row.Request.Candidates[0].Attempts[0].Result == nil || *row.Request.Candidates[0].Attempts[0].Result != first || row.Request.Candidates[1].State != DesignFailed {
				t.Fatal(row)
			}
		case "historical-edit":
			if row.Request.State != DesignQueued || row.Request.Candidates[0].Spec.Base == nil || *row.Request.Candidates[0].Spec.Base != first {
				t.Fatal(row)
			}
		}
	}
}

// Purpose: a failed admission batch must not expose a request, artifact or
// project catalog row after restart. submitDesignRequestInBatch and the existing
// session admission index are the narrowest transactional boundary for this.
func TestProjectDesignCatalogAbandonedBatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	ss := NewSessionStore(s)
	p := designTestOwner
	_, err = ss.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: "parent", UserID: p.PrincipalID, AccountScopeID: p.AccountID, IdempotencyKey: "create", PayloadHash: "create", Kind: V3SessionMutationCreateSession, Session: &SessionSnapshot{ID: "parent"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := ss.PutProject(p.AccountID, &ProjectRecord{ID: "project", Name: "Project", PrimarySessionID: "parent"}); err != nil {
		t.Fatal(err)
	}
	batch := s.db.NewBatch()
	s.designMu.Lock()
	_, err = s.submitDesignRequestInBatch(p, designTestSubmit("abandoned", DesignHTML), batch)
	s.designMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err := batch.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	rows, cursor, err := NewSessionStore(s).ListProjectDesignRequests(p, "project", "", 20)
	if err != nil || len(rows) != 0 || cursor != "" {
		t.Fatal("uncommitted admission visible", rows, cursor, err)
	}
	if _, err := s.GetDesignRequest(p, "abandoned"); !errors.Is(err, ErrDesignNotFound) {
		t.Fatal(err)
	}
	if _, err := s.GetDesignArtifact(p, "artifact"); !errors.Is(err, ErrDesignNotFound) {
		t.Fatal(err)
	}
}

// Purpose: ApplyV3SessionMutation must invalidate every canonical project for a
// shared session, without duplicates or notices for revoked membership. A real
// Pebble mutation proves the atomic outbox postconditions at the owning boundary.
func TestProjectDesignInvalidationMultipleMemberships(t *testing.T) {
	s := openTaskProgramTestStore(t)
	ss := NewSessionStore(s)
	p := designTestOwner
	_, err := ss.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: "parent", UserID: p.PrincipalID, AccountScopeID: p.AccountID, IdempotencyKey: "create", PayloadHash: "create", Kind: V3SessionMutationCreateSession, Session: &SessionSnapshot{ID: "parent"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"one", "two", "revoked"} {
		if err := ss.PutProject(p.AccountID, &ProjectRecord{ID: id, Name: id, PrimarySessionID: "parent"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := ss.PutProjectTask(p.AccountID, &ProjectTaskRecord{ID: "duplicate", ProjectID: "one", Title: "Duplicate membership", Agent: "swarm", SessionID: "parent"}); err != nil {
		t.Fatal(err)
	}
	if _, err := ss.UpdateProject(p.AccountID, "revoked", func(v *ProjectRecord) error { v.PrimarySessionID = ""; return nil }); err != nil {
		t.Fatal(err)
	}
	var notices []V3RealtimeOutboxRecord
	s.SetProjectPublisher(func(record V3RealtimeOutboxRecord) { notices = append(notices, record) })
	r, err := s.SubmitDesignRequest(p, designTestSubmit("multi-project", DesignHTML))
	if err != nil {
		t.Fatal(err)
	}
	r = designTestStart(t, s, r, 0, "child")
	if len(notices) != 2 {
		t.Fatalf("expected two distinct project wakeups: %+v", notices)
	}
	for index, id := range []string{"one", "two"} {
		stored, found, err := ss.GetV3RealtimeOutbox(notices[index].EndpointSeq)
		if err != nil || !found || string(stored.Event.Payload) != `{"project_id":"`+id+`","resource":"designs"}` {
			t.Fatal(stored, err)
		}
	}
	if notices[0].EndpointSeq == notices[1].EndpointSeq {
		t.Fatal("outbox endpoint collision")
	}
	_, err = s.RecordDesignAttempt(p, r.ID, DesignAttemptMutation{IdempotencyKey: "stale", ExpectedRevision: 0, State: DesignFailed, Candidate: 0, ChildSessionID: "child", RunID: "run-child"})
	if err == nil || len(notices) != 2 {
		t.Fatal("failed mutation emitted wakeups", notices, err)
	}
}

package pebblestore

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Purpose: admission order, not random IDs or clocks, must drive bounded catalog
// discovery. Real submit/reopen/list calls prove durable continuation and input
// redaction at the narrow storage boundary, including cross-scope rejection.
func TestDesignCatalogAdmissionPagination(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	p := designTestOwner
	ids := make([]string, 40)
	for i := range ids {
		ids[i] = designDigest([]byte(fmt.Sprintf("request-%d", i)))
		in := designTestSubmit(ids[i], DesignHTML)
		in.Candidates[0].ArtifactID = ids[i]
		if _, err := s.SubmitDesignRequest(p, in); err != nil {
			t.Fatal(err)
		}
	}
	// An idempotent replay is not a new admission and must not reorder history.
	replay := designTestSubmit(ids[0], DesignHTML)
	replay.Candidates[0].ArtifactID = ids[0]
	if _, err := s.SubmitDesignRequest(p, replay); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	rows, cursor, err := s.ListSessionDesignRequests(p, "parent", "", 20)
	if err != nil || len(rows) != 20 || cursor == "" {
		t.Fatal(rows, cursor, err)
	}
	// A new admission between pages must appear on refresh without duplicating
	// or displacing older entries in the ongoing traversal.
	in := designTestSubmit("newest", DesignHTML)
	in.Candidates[0].ArtifactID = "newest"
	if _, err := s.SubmitDesignRequest(p, in); err != nil {
		t.Fatal(err)
	}
	fresh, _, err := s.ListSessionDesignRequests(p, "parent", "", 20)
	if err != nil || len(fresh) != 20 || fresh[0].ID != "newest" {
		t.Fatal(fresh, err)
	}
	last, next, err := s.ListSessionDesignRequests(p, "parent", cursor, 20)
	if err != nil || len(last) != 20 || next != "" {
		t.Fatal(last, next, err)
	}
	rows = append(rows, last...)
	for i, row := range rows {
		if row.ID != ids[len(ids)-1-i] {
			t.Fatalf("position %d: %s", i, row.ID)
		}
	}
	data, err := json.Marshal(rows)
	if err != nil || strings.Contains(string(data), "Redesign the task card") || strings.Contains(string(data), ".task") {
		t.Fatal("catalog leaked inputs", err)
	}
	stored, err := s.GetDesignRequest(p, ids[0])
	if err != nil || stored.Candidates[0].Spec.Brief == "" {
		t.Fatal("redaction mutated source", err)
	}
	for _, scope := range []struct {
		p       DesignPrincipal
		session string
		cursor  string
	}{
		{p, "other", cursor}, {DesignPrincipal{AccountID: "foreign", PrincipalID: p.PrincipalID}, "parent", cursor},
		{p, "parent", rows[0].ID},
	} {
		if _, _, err := s.ListSessionDesignRequests(scope.p, scope.session, scope.cursor, 20); !errors.Is(err, ErrDesignInvalid) {
			t.Fatal("invalid cursor accepted", err)
		}
	}
}

// Purpose: commitDesignChange/ApplyV3SessionMutation must commit selection and
// design.updated outbox together. A genuine read-only write failure and racing
// CAS writers prove no partial state/event or duplicate successful mutation at
// the real Pebble boundary; retry after reopen must remain possible.
func TestDesignCatalogOutboxWriteFailureAndConcurrentCAS(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	p := designTestOwner
	ss := NewSessionStore(s)
	_, err = ss.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: "parent", UserID: p.PrincipalID, AccountScopeID: p.AccountID, IdempotencyKey: "create", PayloadHash: "create", Kind: V3SessionMutationCreateSession, Session: &SessionSnapshot{ID: "parent"}})
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.SubmitDesignRequest(p, designTestSubmit("request", DesignHTML))
	if err != nil {
		t.Fatal(err)
	}
	r = designTestStart(t, s, r, 0, "child")
	_, ref := designTestPublish(t, s, r, 0, []byte("<html>ready</html>"))
	before, err := ss.ListV3RealtimeOutboxForSessionAfterEndpoint("parent", 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	selection := DesignSelection{IdempotencyKey: "retry", Ref: ref}
	if _, err := s.SelectDesignRevision(p, selection); err == nil {
		t.Fatal("read-only selection succeeded")
	}
	a, err := s.GetDesignArtifact(p, ref.ArtifactID)
	if err != nil || a.Selected != nil || a.SelectionVersion != 0 {
		t.Fatal("partial selection", a, err)
	}
	after, err := NewSessionStore(s).ListV3RealtimeOutboxForSessionAfterEndpoint("parent", 0, 50)
	if err != nil || len(after) != len(before) {
		t.Fatal("partial outbox", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SelectDesignRevision(p, selection); err != nil {
		t.Fatal("failed write left receipt", err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func(i int) {
			<-start
			_, err := s.SelectDesignRevision(p, DesignSelection{IdempotencyKey: fmt.Sprintf("race-%d", i), Ref: ref, ExpectedCurrent: &ref, ExpectedVersion: 1})
			results <- err
		}(i)
	}
	close(start)
	success, conflict := 0, 0
	for i := 0; i < 2; i++ {
		select {
		case err := <-results:
			if err == nil {
				success++
			} else if errors.Is(err, ErrDesignConflict) {
				conflict++
			} else {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("concurrent selection timed out")
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatal(success, conflict)
	}
	a, err = s.GetDesignArtifact(p, ref.ArtifactID)
	if err != nil || a.SelectionVersion != 2 || a.Selected == nil || *a.Selected != ref {
		t.Fatal(a, err)
	}
	after, err = NewSessionStore(s).ListV3RealtimeOutboxForSessionAfterEndpoint("parent", 0, 50)
	if err != nil || len(after) != len(before)+2 {
		t.Fatal("outbox count", len(after), err)
	}
	for _, out := range after[len(before):] {
		if out.Event.EventType != "design.updated" || out.SessionID != "parent" || out.AccountScopeID != p.AccountID || out.UserID != p.PrincipalID {
			t.Fatal("incorrect invalidation", out)
		}
	}
	events, err := NewSessionStore(s).ListV3SessionEvents("parent", 0, 50)
	if err != nil || len(events) != len(after) {
		t.Fatal("event/outbox mismatch", err)
	}
}

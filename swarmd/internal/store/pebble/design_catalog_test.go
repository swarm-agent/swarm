package pebblestore

import (
	"errors"
	"path/filepath"
	"testing"
)

// Purpose: the independent catalog and selection must survive reopen, remain
// principal/session scoped, and publish durable V3 invalidations atomically.
// Real Pebble is the narrowest layer proving failed CAS leaves history/events
// unchanged; no provider or renderer is needed for these storage postconditions.
func TestDesignCatalogDurableSelectionAndIsolation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db")
	s, err := Open(path)
	if err != nil { t.Fatal(err) }
	defer func() { _ = s.Close() }()
	ss := NewSessionStore(s)
	p := designTestOwner
	_, err = ss.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: "parent", UserID: p.PrincipalID, AccountScopeID: p.AccountID, IdempotencyKey: "create", PayloadHash: "create", Kind: V3SessionMutationCreateSession, Session: &SessionSnapshot{ID: "parent"}})
	if err != nil { t.Fatal(err) }
	r, err := s.SubmitDesignRequest(p, designTestSubmit("first", DesignHTML))
	if err != nil { t.Fatal(err) }
	r = designTestStart(t, s, r, 0, "child")
	_, first := designTestPublish(t, s, r, 0, []byte("<html>first</html>"))
	base := first
	for _, id := range []string{"edit-one", "edit-two"} {
		in := designTestSubmit(id, DesignHTML)
		in.Candidates[0].Operation, in.Candidates[0].Base = DesignEdit, &base
		r, err = s.SubmitDesignRequest(p, in)
		if err != nil { t.Fatal(err) }
		r = designTestStart(t, s, r, 0, id+"-child")
		_, base = designTestPublish(t, s, r, 0, []byte("<html>"+id+"</html>"))
	}
	selected, err := s.SelectDesignRevision(p, DesignSelection{IdempotencyKey: "select", Ref: first})
	if err != nil || selected.SelectionVersion != 1 { t.Fatal(selected, err) }
	before, err := ss.ListV3SessionEvents("parent", 0, 50)
	if err != nil { t.Fatal(err) }
	if before[len(before)-1].EventType != "design.updated" { t.Fatal("missing selection invalidation") }
	if _, err = s.SelectDesignRevision(p, DesignSelection{IdempotencyKey: "stale", Ref: base}); !errors.Is(err, ErrDesignConflict) { t.Fatal(err) }
	after, err := ss.ListV3SessionEvents("parent", 0, 50)
	if err != nil || len(before) != len(after) { t.Fatal("failed CAS emitted event", err) }
	for _, owner := range []DesignPrincipal{{AccountID: "foreign", PrincipalID: p.PrincipalID}, {AccountID: p.AccountID, PrincipalID: "foreign"}} {
		rows, err := s.ListSessionDesignRequests(owner, "parent", "", 5)
		if err != nil || len(rows) != 0 { t.Fatal("foreign catalog", rows, err) }
		if _, err := s.ReadDesignRevision(owner, first); !errors.Is(err, ErrDesignNotFound) { t.Fatal(err) }
	}
	if _, err := s.RequireDesignArtifactSession(p, "other-session", first.ArtifactID); !errors.Is(err, ErrDesignNotFound) { t.Fatal(err) }
	bad := designTestSubmit("cross-session", DesignHTML)
	bad.ParentSessionID = "other-session"
	bad.Candidates[0].Operation, bad.Candidates[0].Base = DesignEdit, &first
	if _, err := s.SubmitDesignRequest(p, bad); !errors.Is(err, ErrDesignNotFound) { t.Fatal(err) }
	if _, err := s.GetDesignRequest(p, bad.RequestID); !errors.Is(err, ErrDesignNotFound) { t.Fatal("partial edit", err) }
	if err := s.Close(); err != nil { t.Fatal(err) }
	s, err = Open(path)
	if err != nil { t.Fatal(err) }
	rows, err := s.ListSessionDesignRequests(p, "parent", "", 2)
	if err != nil || len(rows) != 2 || rows[0].Candidates[0].Spec.Brief != "" { t.Fatal(rows, err) }
	last, err := s.ListSessionDesignRequests(p, "parent", rows[1].ID, 2)
	if err != nil || len(last) != 1 { t.Fatal(last, err) }
	history, err := s.DesignHistory(p, first.ArtifactID, 0, 5)
	if err != nil || len(history) != 3 || history[1].Base == nil || *history[1].Base != first || history[0].Content != nil { t.Fatal(history, err) }
	old, err := s.ReadDesignRevision(p, first)
	if err != nil || string(old.Content) != "<html>first</html>" { t.Fatal(old, err) }
	events, err := NewSessionStore(s).ListV3SessionEvents("parent", 0, 50)
	if err != nil || len(events) != len(before) { t.Fatal("events not durable", err) }
	outbox, err := NewSessionStore(s).ListV3RealtimeOutboxForSessionAfterEndpoint("parent", 0, 50)
	if err != nil || len(outbox) != len(events) || outbox[len(outbox)-1].Event.EventType != "design.updated" { t.Fatal("scoped outbox not durable", err) }
}

// Purpose: authenticated preview reads must never expose validation evidence
// before ready publication, nor accept a foreign session or changed digest.
// Real store calls prove the authority boundary used by HTTP and media_inspect.
func TestDesignCatalogPreviewReadyOnly(t *testing.T) {
	s := openTaskProgramTestStore(t)
	p := designTestOwner
	r, err := s.SubmitDesignRequest(p, designTestSubmit("preview", DesignHTML))
	if err != nil { t.Fatal(err) }
	r = designTestStart(t, s, r, 0, "child")
	content := []byte("<html>ready</html>")
	response := evidenceResponse(r, 0, content)
	r, err = s.RecordDesignResponse(p, response)
	if err != nil { t.Fatal(err) }
	r, err = s.RecordDesignValidation(p, DesignValidationMutation{IdempotencyKey: "validation", ExpectedRevision: r.Revision, Output: response.Response.Ref, Passed: true, Code: "renderable", PNG: evidencePNG(t)})
	if err != nil { t.Fatal(err) }
	ref := *r.Candidates[0].Attempts[0].Validation.Preview
	if _, err := s.ReadSessionDesignPreview(p, "parent", ref); !errors.Is(err, ErrDesignConflict) { t.Fatal("unpublished preview", err) }
	_, _ = designTestPublish(t, s, r, 0, content)
	if data, err := s.ReadSessionDesignPreview(p, "parent", ref); err != nil || !designValidPNG(data) { t.Fatal(err) }
	if _, err := s.ReadSessionDesignPreview(p, "foreign", ref); !errors.Is(err, ErrDesignNotFound) { t.Fatal(err) }
	ref.SHA256 = designDigest([]byte("wrong"))
	if _, err := s.ReadSessionDesignPreview(p, "parent", ref); !errors.Is(err, ErrDesignConflict) { t.Fatal(err) }
}

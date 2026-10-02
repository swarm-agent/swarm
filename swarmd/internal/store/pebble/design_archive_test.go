package pebblestore

import (
	"errors"
	"path/filepath"
	"reflect"
	"testing"
)

// Purpose: SetDesignArchived is a durable artifact visibility CAS, not selection,
// deletion or cancellation. Real Pebble is the narrowest boundary proving replay,
// ownership rejection, restart and subsequent publication cannot resurrect it.
func TestDesignArchiveDurabilityCASAndPublication(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	p := designTestOwner
	r, err := s.SubmitDesignRequest(p, designTestSubmit("first", DesignHTML))
	if err != nil {
		t.Fatal(err)
	}
	r = designTestStart(t, s, r, 0, "child")
	_, ref := designTestPublish(t, s, r, 0, []byte("<html>retained</html>"))
	selected, err := s.SelectDesignRevision(p, DesignSelection{IdempotencyKey: "select", Ref: ref})
	if err != nil {
		t.Fatal(err)
	}
	in := DesignArchive{IdempotencyKey: "archive", Ref: ref, Archived: true}
	archived, err := s.SetDesignArchived(p, "parent", in)
	if err != nil || !archived.Archived || archived.ArchiveVersion != 1 || !designRefEqual(archived.Selected, selected.Selected) || archived.SelectionVersion != selected.SelectionVersion {
		t.Fatal(archived, err)
	}
	for _, wrong := range []DesignPrincipal{{AccountID: "other", PrincipalID: p.PrincipalID}, {AccountID: p.AccountID, PrincipalID: "other"}} {
		if _, err := s.SetDesignArchived(wrong, "parent", in); !errors.Is(err, ErrDesignNotFound) {
			t.Fatal("foreign owner", err)
		}
	}
	if _, err := s.SetDesignArchived(p, "other-session", in); !errors.Is(err, ErrDesignNotFound) {
		t.Fatal("foreign session replay", err)
	}
	for _, bad := range []DesignArchive{
		{IdempotencyKey: "stale", Ref: ref, Archived: false},
		{IdempotencyKey: "archive", Ref: ref, Archived: false},
		{IdempotencyKey: "bad-ref", Ref: DesignRef{ArtifactID: ref.ArtifactID, Revision: ref.Revision, SHA256: "wrong"}, ExpectedVersion: 1},
	} {
		if _, err := s.SetDesignArchived(p, "parent", bad); err == nil {
			t.Fatal("invalid archive accepted", bad)
		}
		got, err := s.GetDesignArtifact(p, ref.ArtifactID)
		if err != nil || !reflect.DeepEqual(got, archived) {
			t.Fatal("rejection changed artifact", got, err)
		}
	}
	edit := designTestSubmit("edit", DesignHTML)
	edit.Candidates[0].Operation, edit.Candidates[0].Base = DesignEdit, &ref
	r, err = s.SubmitDesignRequest(p, edit)
	if err != nil {
		t.Fatal(err)
	}
	r = designTestStart(t, s, r, 0, "edit-child")
	_, newer := designTestPublish(t, s, r, 0, []byte("<html>newer</html>"))
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	rows, _, err := s.ListSessionDesignRequests(p, "parent", "", 20)
	if err != nil || len(rows) != 2 || len(FilterDesignCatalogView(rows, false)) != 0 || len(FilterDesignCatalogView(rows, true)) != 2 {
		t.Fatal("archived design resurfaced", rows, err)
	}
	replay, err := s.SetDesignArchived(p, "parent", in)
	if err != nil || !reflect.DeepEqual(replay, archived) {
		t.Fatal("receipt lost", replay, err)
	}
	got, err := s.GetDesignArtifact(p, ref.ArtifactID)
	if err != nil || got.RevisionCount != newer.Revision || !got.Archived || !designRefEqual(got.Selected, &ref) {
		t.Fatal("history/selection changed", got, err)
	}
	for _, exact := range []DesignRef{ref, newer} {
		if _, err := s.ReadDesignRevision(p, exact); err != nil {
			t.Fatal("retained revision missing", err)
		}
	}
	got, err = s.SetDesignArchived(p, "parent", DesignArchive{IdempotencyKey: "restore", Ref: newer, ExpectedVersion: 1})
	if err != nil || got.Archived || got.ArchiveVersion != 2 {
		t.Fatal("restore", got, err)
	}
	rows, _, err = s.ListSessionDesignRequests(p, "parent", "", 20)
	if err != nil || len(FilterDesignCatalogView(rows, false)) != 2 {
		t.Fatal("restored catalog", rows, err)
	}
}

// Purpose: SetDesignArchived must reject a missing canonical parent without
// persisting archive metadata or a success receipt. A retained canonical request
// without its parent exercises the V3 ownership precondition at the store layer.
func TestDesignArchiveCommitFailureAtomicity(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p := designTestOwner
	r, err := s.SubmitDesignRequest(p, designTestSubmit("first", DesignHTML))
	if err != nil {
		t.Fatal(err)
	}
	r = designTestStart(t, s, r, 0, "child")
	_, ref := designTestPublish(t, s, r, 0, []byte("<html>retained</html>"))
	r, err = s.GetDesignRequest(p, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	r.Canonical = true
	b := s.db.NewBatch()
	defer b.Close()
	if err := designSet(b, designKey(p, "request", r.ID), r); err != nil {
		t.Fatal(err)
	}
	if err := b.Commit(nil); err != nil {
		t.Fatal(err)
	}
	before, err := s.GetDesignArtifact(p, ref.ArtifactID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.SetDesignArchived(p, "parent", DesignArchive{IdempotencyKey: "archive", Ref: ref, Archived: true})
	if !errors.Is(err, ErrDesignNotFound) {
		t.Fatal("wrong parent accepted", err)
	}
	after, err := s.GetDesignArtifact(p, ref.ArtifactID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("partial archive", after, err)
	}
	if _, found, err := s.GetBytes(designKey(p, "archive", ref.ArtifactID+"/archive")); err != nil || found {
		t.Fatal("partial receipt", found, err)
	}
}

// Purpose: catalog refresh under projectsMu must not rewrite an existing locator
// or repair corrupted authority silently. Pebble's WAL counter proves the no-write
// postcondition without timing claims; corrupt membership must remain unchanged.
func TestDesignMembershipHydrationNoRepeatedWrites(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ss := NewSessionStore(s)
	if err := ss.hydrateDesignMembership("a", "s", "p", "t"); err != nil {
		t.Fatal(err)
	}
	before := s.db.Metrics().WAL.BytesWritten
	if err := ss.hydrateDesignMembership("a", "s", "p", "t"); err != nil {
		t.Fatal(err)
	}
	if after := s.db.Metrics().WAL.BytesWritten; after != before {
		t.Fatal("existing locator rewritten", before, after)
	}
	b := s.db.NewBatch()
	defer b.Close()
	key := designMembershipKey("a", "s", "p", "t")
	if err := designSet(b, key, designMembership{Project: "wrong", Task: "t"}); err != nil {
		t.Fatal(err)
	}
	if err := b.Commit(nil); err != nil {
		t.Fatal(err)
	}
	if err := ss.hydrateDesignMembership("a", "s", "p", "t"); !errors.Is(err, ErrDesignConflict) {
		t.Fatal("corrupt locator overwritten", err)
	}
	var binding designMembership
	if err := s.designGet(key, &binding); err != nil || binding.Project != "wrong" {
		t.Fatal(binding, err)
	}
}

// Purpose: bounded catalog filtering must retain original candidate indexes and
// exact refs for mixed active/archived batches. The pure projection function is
// the narrowest layer proving filtering cannot renumber preview identities.
func TestDesignArchiveMixedCatalogIndexes(t *testing.T) {
	rows := []DesignRequest{{ID: "mixed", Candidates: []DesignCandidate{
		{Spec: DesignCandidateSpec{ArtifactID: "one"}, Archived: true, ArchiveVersion: 1},
		{Spec: DesignCandidateSpec{ArtifactID: "two"}},
	}}}
	for _, archived := range []bool{false, true} {
		got := FilterDesignCatalogView(rows, archived)
		if !reflect.DeepEqual(got, rows) || got[0].Candidates[1].Spec.ArtifactID != "two" {
			t.Fatal("mixed candidate indexes changed", got)
		}
	}
}

package pebblestore

import (
	"bytes"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

var designTestOwner = DesignPrincipal{AccountID: "account", PrincipalID: "principal"}

func designTestSubmit(id string, kind string) DesignSubmit {
	content := []byte(".task { color: blue; }\n")
	return DesignSubmit{
		RequestID: id, IdempotencyKey: id, ParentSessionID: "parent", ParentRunID: "parent-run",
		Candidates: []DesignCandidateSpec{{ArtifactID: "artifact", Kind: kind, Operation: DesignGenerate, Brief: "Redesign the task card"}},
		Context: []DesignContextSnapshot{{Path: "src/task.css", Content: content, SHA256: designDigest(content)}},
	}
}

func designTestStart(t *testing.T, s *Store, r DesignRequest, candidate int, child string) DesignRequest {
	t.Helper()
	next, err := s.RecordDesignAttempt(designTestOwner, r.ID, DesignAttemptMutation{IdempotencyKey: child, ExpectedRevision: r.Revision, Candidate: candidate, State: DesignRunning, ChildSessionID: child, RunID: child + "-run"})
	if err != nil { t.Fatal(err) }
	return next
}

func designTestPublish(t *testing.T, s *Store, r DesignRequest, candidate int, content []byte) (DesignRequest, DesignRef) {
	t.Helper()
	c := r.Candidates[candidate]
	a := c.Attempts[len(c.Attempts)-1]
	next, err := s.PublishDesignRevision(designTestOwner, r.ID, DesignPublication{IdempotencyKey: "publish", ExpectedRevision: r.Revision, Candidate: candidate, ChildSessionID: a.ChildSessionID, RunID: a.RunID, Kind: c.Spec.Kind, Content: content})
	if err != nil { t.Fatal(err) }
	return next, *next.Candidates[candidate].Attempts[len(c.Attempts)-1].Result
}

// Purpose: SubmitDesignRequest, PublishDesignRevision and SelectDesignRevision
// must durably preserve exact source/output bytes and immutable history. The
// narrow real-Pebble layer proves reopen and branching without renderer/provider
// dependencies; stale hashes, failed edits and ABA must not replace good output.
func TestDesignStoreImmutableBranchRestartAndSelection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db")
	s, err := Open(path)
	if err != nil { t.Fatal(err) }
	defer func() { _ = s.Close() }()
	in := designTestSubmit("initial", DesignHTML)
	r, err := s.SubmitDesignRequest(designTestOwner, in)
	if err != nil { t.Fatal(err) }
	r = designTestStart(t, s, r, 0, "child-one")
	original := []byte("<!doctype html>\r\n<html><body>α task</body></html>\n")
	r, first := designTestPublish(t, s, r, 0, original)
	selection := DesignSelection{IdempotencyKey: "select-first", Ref: first}
	selected, err := s.SelectDesignRevision(designTestOwner, selection)
	if err != nil || selected.SelectionVersion != 1 { t.Fatalf("select: %+v %v", selected, err) }
	edit := designTestSubmit("edit", DesignHTML)
	edit.Candidates[0].Operation, edit.Candidates[0].Base = DesignEdit, &first
	er, err := s.SubmitDesignRequest(designTestOwner, edit)
	if err != nil { t.Fatal(err) }
	er = designTestStart(t, s, er, 0, "child-edit")
	failed, err := s.RecordDesignAttempt(designTestOwner, er.ID, DesignAttemptMutation{IdempotencyKey: "failure", ExpectedRevision: er.Revision, State: DesignFailed, ChildSessionID: "child-edit", RunID: "child-edit-run", ReasonCode: "invalid_output"})
	if err != nil || failed.State != DesignFailed { t.Fatalf("failure: %+v %v", failed, err) }
	still, err := s.GetDesignArtifact(designTestOwner, first.ArtifactID)
	if err != nil || still.RevisionCount != 1 || !designRefEqual(still.Selected, &first) { t.Fatalf("failed attempt changed artifact: %+v %v", still, err) }
	er = designTestStart(t, s, failed, 0, "child-retry")
	er, second := designTestPublish(t, s, er, 0, []byte("<!doctype html><html>second</html>"))
	if len(er.Candidates[0].Attempts) != 2 || er.Candidates[0].Attempts[0].State != DesignFailed { t.Fatal("failed attempt lost") }
	branch := designTestSubmit("branch", DesignHTML)
	branch.Candidates[0].Operation, branch.Candidates[0].Base = DesignEdit, &first
	br, err := s.SubmitDesignRequest(designTestOwner, branch)
	if err != nil { t.Fatal(err) }
	br = designTestStart(t, s, br, 0, "child-branch")
	_, third := designTestPublish(t, s, br, 0, []byte("<!doctype html><html>branch</html>"))
	if third.Revision != 3 { t.Fatalf("branch revision: %+v", third) }
	for i, ref := range []DesignRef{second, first} {
		selected, err = s.SelectDesignRevision(designTestOwner, DesignSelection{IdempotencyKey: fmt.Sprintf("select-%d", i), ExpectedVersion: selected.SelectionVersion, ExpectedCurrent: selected.Selected, Ref: ref})
		if err != nil { t.Fatal(err) }
	}
	_, err = s.SelectDesignRevision(designTestOwner, DesignSelection{IdempotencyKey: "aba", ExpectedVersion: 1, ExpectedCurrent: &first, Ref: third})
	if !errors.Is(err, ErrDesignConflict) { t.Fatalf("ABA accepted: %v", err) }
	if err := s.Close(); err != nil { t.Fatal(err) }
	s, err = Open(path)
	if err != nil { t.Fatal(err) }
	read, err := s.ReadDesignRevision(designTestOwner, first)
	if err != nil || !bytes.Equal(read.Content, original) { t.Fatalf("bytes changed after reopen: %+v %v", read, err) }
	read.Content[0] = 'X'
	again, err := s.ReadDesignRevision(designTestOwner, first)
	if err != nil || !bytes.Equal(again.Content, original) { t.Fatal("returned bytes alias store") }
	ctx, err := s.ReadDesignContext(designTestOwner, r.ID)
	if err != nil || len(ctx) != 1 || !bytes.Equal(ctx[0].Content, in.Context[0].Content) { t.Fatalf("context: %+v %v", ctx, err) }
	history, err := s.DesignHistory(designTestOwner, first.ArtifactID, 0, 2)
	if err != nil || len(history) != 2 || history[0].Content != nil || history[1].Ref != second { t.Fatalf("history: %+v %v", history, err) }
	tail, err := s.DesignHistory(designTestOwner, first.ArtifactID, 2, 2)
	if err != nil || len(tail) != 1 || !designRefEqual(tail[0].Base, &first) { t.Fatalf("branch history: %+v %v", tail, err) }
	selections, err := s.DesignSelectionHistory(designTestOwner, first.ArtifactID, 0, 50)
	if err != nil || len(selections) != 3 || selections[2].SelectionVersion != 3 { t.Fatalf("selection history: %+v %v", selections, err) }
	// A duplicate returns its original result, not the current selection version.
	replay, err := s.SelectDesignRevision(designTestOwner, selection)
	if err != nil || replay.SelectionVersion != 1 { t.Fatalf("selection replay: %+v %v", replay, err) }
	bad := first
	bad.SHA256 = designDigest([]byte("wrong"))
	if _, err := s.ReadDesignRevision(designTestOwner, bad); !errors.Is(err, ErrDesignConflict) { t.Fatalf("bad hash: %v", err) }
}

// Purpose: authenticated principal partitioning and exact idempotency in all
// design mutation boundaries must reject hostile cross-account/principal access,
// stale publications and same-key payload changes without partial writes. Real
// Pebble records, not status-only mocks, establish unchanged counters and bytes.
func TestDesignStoreAuthorityIdempotencyAndFailureAtomicity(t *testing.T) {
	s := openTaskProgramTestStore(t)
	in := designTestSubmit("request", DesignPlan)
	r, err := s.SubmitDesignRequest(designTestOwner, in)
	if err != nil { t.Fatal(err) }
	duplicate, err := s.SubmitDesignRequest(designTestOwner, in)
	if err != nil || duplicate.Revision != 1 { t.Fatalf("replay: %+v %v", duplicate, err) }
	conflict := in
	conflict.ParentRunID = "different"
	if _, err := s.SubmitDesignRequest(designTestOwner, conflict); !errors.Is(err, ErrDesignConflict) { t.Fatalf("submit conflict: %v", err) }
	start := DesignAttemptMutation{IdempotencyKey: "start", ExpectedRevision: r.Revision, State: DesignRunning, ChildSessionID: "child", RunID: "run"}
	r, err = s.RecordDesignAttempt(designTestOwner, r.ID, start)
	if err != nil { t.Fatal(err) }
	if replay, err := s.RecordDesignAttempt(designTestOwner, r.ID, start); err != nil || replay.Revision != r.Revision { t.Fatalf("start replay: %v", err) }
	changed := start
	changed.RunID = "changed"
	if _, err := s.RecordDesignAttempt(designTestOwner, r.ID, changed); !errors.Is(err, ErrDesignConflict) { t.Fatalf("attempt conflict: %v", err) }
	pub := DesignPublication{IdempotencyKey: "pub", ExpectedRevision: r.Revision, ChildSessionID: "child", RunID: "run", Kind: DesignPlan, Content: []byte("# Plan\nDo not execute automatically.\n")}
	bad := pub
	bad.Kind = DesignHTML
	if _, err := s.PublishDesignRevision(designTestOwner, r.ID, bad); !errors.Is(err, ErrDesignConflict) { t.Fatalf("kind mismatch: %v", err) }
	bad = pub
	bad.ExpectedRevision--
	if _, err := s.PublishDesignRevision(designTestOwner, r.ID, bad); !errors.Is(err, ErrDesignConflict) { t.Fatalf("stale publication: %v", err) }
	bad = pub
	bad.ChildSessionID = "unrelated"
	if _, err := s.PublishDesignRevision(designTestOwner, r.ID, bad); !errors.Is(err, ErrDesignConflict) { t.Fatalf("foreign child: %v", err) }
	a, err := s.GetDesignArtifact(designTestOwner, "artifact")
	if err != nil || a.RevisionCount != 0 || a.Selected != nil { t.Fatalf("partial publication: %+v %v", a, err) }
	published, err := s.PublishDesignRevision(designTestOwner, r.ID, pub)
	if err != nil { t.Fatal(err) }
	ref := *published.Candidates[0].Attempts[0].Result
	if published.State != DesignSucceeded { t.Fatalf("plan status: %s", published.State) }
	if replay, err := s.PublishDesignRevision(designTestOwner, r.ID, pub); err != nil || replay.Revision != published.Revision { t.Fatalf("pub replay: %v", err) }
	bad = pub
	bad.Content = []byte("different plan")
	if _, err := s.PublishDesignRevision(designTestOwner, r.ID, bad); !errors.Is(err, ErrDesignConflict) { t.Fatalf("pub conflict: %v", err) }
	for _, foreign := range []DesignPrincipal{{AccountID: "other", PrincipalID: "principal"}, {AccountID: "account", PrincipalID: "other"}} {
		if _, err := s.GetDesignRequest(foreign, r.ID); !errors.Is(err, ErrDesignNotFound) { t.Fatalf("foreign request: %v", err) }
		if _, err := s.ReadDesignRevision(foreign, ref); !errors.Is(err, ErrDesignNotFound) { t.Fatalf("foreign bytes: %v", err) }
		if _, err := s.ReadDesignContext(foreign, r.ID); !errors.Is(err, ErrDesignNotFound) { t.Fatalf("foreign context: %v", err) }
		if _, err := s.PublishDesignRevision(foreign, r.ID, pub); !errors.Is(err, ErrDesignNotFound) { t.Fatalf("foreign publication: %v", err) }
		if _, err := s.SelectDesignRevision(foreign, DesignSelection{IdempotencyKey: "select", Ref: ref}); !errors.Is(err, ErrDesignNotFound) { t.Fatalf("foreign selection: %v", err) }
		edit := designTestSubmit("foreign-edit", DesignPlan)
		edit.Candidates[0].Operation, edit.Candidates[0].Base = DesignEdit, &ref
		if _, err := s.SubmitDesignRequest(foreign, edit); !errors.Is(err, ErrDesignNotFound) { t.Fatalf("foreign base: %v", err) }
		if _, err := s.GetDesignRequest(foreign, edit.RequestID); !errors.Is(err, ErrDesignNotFound) { t.Fatal("foreign request partially created") }
	}
	read, err := s.ReadDesignRevision(designTestOwner, ref)
	if err != nil || !bytes.Equal(read.Content, pub.Content) { t.Fatal("rejected operations altered original") }
	a, err = s.GetDesignArtifact(designTestOwner, "artifact")
	if err != nil || a.RevisionCount != 1 || a.Selected != nil { t.Fatalf("publication selected implicitly: %+v %v", a, err) }
}

// Purpose: bounded batches are independent candidates, with honest partial,
// interrupted and cancellation provenance. RecordDesignAttempt and publication
// must retain failed attempts across reopen, block late cancelled output, and
// require fresh child identities. Store tests avoid inventing a session executor.
func TestDesignStoreBatchCancellationAndInterruptedRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db")
	s, err := Open(path)
	if err != nil { t.Fatal(err) }
	defer func() { _ = s.Close() }()
	in := designTestSubmit("batch", DesignHTML)
	in.Candidates = append(in.Candidates, DesignCandidateSpec{ArtifactID: "second", Kind: DesignPlan, Operation: DesignGenerate, Brief: "Plan only"})
	r, err := s.SubmitDesignRequest(designTestOwner, in)
	if err != nil { t.Fatal(err) }
	r = designTestStart(t, s, r, 0, "one")
	r, _ = designTestPublish(t, s, r, 0, []byte("<html>one</html>"))
	r = designTestStart(t, s, r, 1, "two")
	if err := s.Close(); err != nil { t.Fatal(err) }
	s, err = Open(path)
	if err != nil { t.Fatal(err) }
	r, err = s.GetDesignRequest(designTestOwner, r.ID)
	if err != nil || r.State != DesignRunning { t.Fatalf("reopen invented outcome: %+v %v", r, err) }
	r, err = s.RecordDesignAttempt(designTestOwner, r.ID, DesignAttemptMutation{IdempotencyKey: "restart-reconcile", ExpectedRevision: r.Revision, Candidate: 1, State: DesignInterrupted, ChildSessionID: "two", RunID: "two-run", ReasonCode: "canonical_run_interrupted"})
	if err != nil || r.State != DesignPartial { t.Fatalf("partial: %+v %v", r, err) }
	if _, err := s.RecordDesignAttempt(designTestOwner, r.ID, DesignAttemptMutation{IdempotencyKey: "reuse-child", ExpectedRevision: r.Revision, Candidate: 1, State: DesignRunning, ChildSessionID: "two", RunID: "new-run"}); !errors.Is(err, ErrDesignConflict) { t.Fatalf("reused context: %v", err) }
	r = designTestStart(t, s, r, 1, "fresh")
	r, err = s.RecordDesignAttempt(designTestOwner, r.ID, DesignAttemptMutation{IdempotencyKey: "cancel", ExpectedRevision: r.Revision, Candidate: 1, State: DesignCancelRequested, ChildSessionID: "fresh", RunID: "fresh-run"})
	if err != nil || r.State != DesignCancelRequested { t.Fatalf("cancel: %+v %v", r, err) }
	if _, err := s.PublishDesignRevision(designTestOwner, r.ID, DesignPublication{IdempotencyKey: "late", ExpectedRevision: r.Revision, Candidate: 1, ChildSessionID: "fresh", RunID: "fresh-run", Kind: DesignPlan, Content: []byte("late output")}); !errors.Is(err, ErrDesignConflict) { t.Fatalf("cancelled publication: %v", err) }
	r, err = s.RecordDesignAttempt(designTestOwner, r.ID, DesignAttemptMutation{IdempotencyKey: "cancel-confirm", ExpectedRevision: r.Revision, Candidate: 1, State: DesignCancelled, ChildSessionID: "fresh", RunID: "fresh-run", ReasonCode: "canonical_cancelled"})
	if err != nil || r.State != DesignPartial || r.Candidates[1].Attempts[0].State != DesignInterrupted { t.Fatalf("cancel result: %+v %v", r, err) }
	first, _ := s.GetDesignArtifact(designTestOwner, "artifact")
	second, _ := s.GetDesignArtifact(designTestOwner, "second")
	if first.RequestGroupID != second.RequestGroupID || first.ID == second.ID || first.RevisionCount != 1 || second.RevisionCount != 0 { t.Fatalf("batch identity/content corrupted: %+v %+v", first, second) }
}

// Purpose: the Store-owned mutex plus Pebble batch must serialize concurrent
// create/publication/selection CAS operations. Actual competing calls assert a
// single winner and no duplicate revision/selection history; race execution is
// the narrowest additional check for unsynchronized shared metadata.
func TestDesignStoreConcurrentCAS(t *testing.T) {
	s := openTaskProgramTestStore(t)
	in := designTestSubmit("concurrent", DesignHTML)
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := s.SubmitDesignRequest(designTestOwner, in); errs <- err }()
	}
	wg.Wait()
	close(errs)
	for err := range errs { if err != nil { t.Fatal(err) } }
	r, err := s.GetDesignRequest(designTestOwner, in.RequestID)
	if err != nil { t.Fatal(err) }
	r = designTestStart(t, s, r, 0, "child")
	errs = make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := s.PublishDesignRevision(designTestOwner, r.ID, DesignPublication{IdempotencyKey: fmt.Sprintf("pub-%d", i), ExpectedRevision: r.Revision, ChildSessionID: "child", RunID: "child-run", Kind: DesignHTML, Content: []byte("<html>unique winner</html>")})
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	wins := 0
	for err := range errs { if err == nil { wins++ } else if !errors.Is(err, ErrDesignConflict) { t.Fatal(err) } }
	if wins != 1 { t.Fatalf("publication winners: %d", wins) }
	history, err := s.DesignHistory(designTestOwner, "artifact", 0, 50)
	if err != nil || len(history) != 1 { t.Fatalf("duplicate revisions: %+v %v", history, err) }
	ref := history[0].Ref
	errs = make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) { defer wg.Done(); _, err := s.SelectDesignRevision(designTestOwner, DesignSelection{IdempotencyKey: fmt.Sprintf("select-%d", i), Ref: ref}); errs <- err }(i)
	}
	wg.Wait()
	close(errs)
	wins = 0
	for err := range errs { if err == nil { wins++ } else if !errors.Is(err, ErrDesignConflict) { t.Fatal(err) } }
	if wins != 1 { t.Fatalf("selection winners: %d", wins) }
	a, err := s.GetDesignArtifact(designTestOwner, "artifact")
	if err != nil || a.SelectionVersion != 1 || a.RevisionCount != 1 { t.Fatalf("CAS postconditions: %+v %v", a, err) }
}

// Purpose: SubmitDesignRequest and bounded readers reject invalid kinds, missing
// exact edit bases, duplicate IDs, oversized inputs and unsafe key components
// before any request/artifact/context writes. This layer directly observes the
// all-or-nothing durable boundary rather than merely checking validation strings.
func TestDesignStoreValidationNoPartialState(t *testing.T) {
	cases := map[string]func(*DesignSubmit){
		"duplicate": func(in *DesignSubmit) { in.Candidates = append(in.Candidates, in.Candidates[0]) },
		"kind": func(in *DesignSubmit) { in.Candidates[0].Kind = "image" },
		"missing-base": func(in *DesignSubmit) { in.Candidates[0].Operation = DesignEdit },
		"path-key": func(in *DesignSubmit) { in.Candidates[0].ArtifactID = "../artifact" },
		"empty-batch": func(in *DesignSubmit) { in.Candidates = nil },
		"oversize-batch": func(in *DesignSubmit) { for len(in.Candidates) <= MaxDesignCandidates { in.Candidates = append(in.Candidates, in.Candidates[0]) } },
		"context-hash": func(in *DesignSubmit) { in.Context[0].SHA256 = "wrong" },
		"context-size": func(in *DesignSubmit) { in.Context[0].Content = bytes.Repeat([]byte("x"), MaxDesignContextBytes+1); in.Context[0].SHA256 = designDigest(in.Context[0].Content) },
	}
	for name, mutate := range cases { t.Run(name, func(t *testing.T) {
		s := openTaskProgramTestStore(t)
		in := designTestSubmit("invalid", DesignHTML)
		mutate(&in)
		if _, err := s.SubmitDesignRequest(designTestOwner, in); err == nil { t.Fatal("invalid request accepted") }
		if _, err := s.GetDesignRequest(designTestOwner, in.RequestID); !errors.Is(err, ErrDesignNotFound) { t.Fatalf("partial request: %v", err) }
		if _, err := s.GetDesignArtifact(designTestOwner, "artifact"); !errors.Is(err, ErrDesignNotFound) { t.Fatalf("partial artifact: %v", err) }
		if _, ok, err := s.GetBytes(designKey(designTestOwner, "context", in.RequestID)); err != nil || ok { t.Fatal("partial context") }
	}) }
	s := openTaskProgramTestStore(t)
	if _, err := s.DesignHistory(designTestOwner, "artifact", 0, MaxDesignHistoryPage+1); !errors.Is(err, ErrDesignInvalid) { t.Fatalf("unbounded history: %v", err) }
	if _, err := s.DesignSelectionHistory(designTestOwner, "artifact", 0, 0); !errors.Is(err, ErrDesignInvalid) { t.Fatalf("unbounded selections: %v", err) }
}

// Purpose: a genuine Pebble write rejection at SubmitDesignRequest and
// PublishDesignRevision must leave no receipt or partial revision. A read-only
// reopened database supplies deterministic storage failure without a fake
// persistence layer, and a subsequent writable retry must still succeed.
func TestDesignStoreWriteFailurePreservesPriorState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db")
	s, err := Open(path)
	if err != nil { t.Fatal(err) }
	defer func() { _ = s.Close() }()
	in := designTestSubmit("request", DesignHTML)
	r, err := s.SubmitDesignRequest(designTestOwner, in)
	if err != nil { t.Fatal(err) }
	r = designTestStart(t, s, r, 0, "child")
	if err := s.Close(); err != nil { t.Fatal(err) }
	s, err = OpenReadOnly(path)
	if err != nil { t.Fatal(err) }
	pub := DesignPublication{IdempotencyKey: "publish", ExpectedRevision: r.Revision, ChildSessionID: "child", RunID: "child-run", Kind: DesignHTML, Content: []byte("<html>preserved</html>")}
	if _, err := s.PublishDesignRevision(designTestOwner, r.ID, pub); err == nil { t.Fatal("read-only publish succeeded") }
	other := designTestSubmit("other", DesignHTML)
	other.Candidates[0].ArtifactID = "other-artifact"
	if _, err := s.SubmitDesignRequest(designTestOwner, other); err == nil { t.Fatal("read-only submit succeeded") }
	for _, key := range []string{designKey(designTestOwner, "publish", r.ID+"/publish"), designRevisionKey(designTestOwner, "artifact", 1), designKey(designTestOwner, "request", "other"), designKey(designTestOwner, "artifact", "other-artifact")} {
		if _, ok, err := s.GetBytes(key); err != nil || ok { t.Fatalf("partial durable write: %s %v", key, err) }
	}
	a, err := s.GetDesignArtifact(designTestOwner, "artifact")
	if err != nil || a.RevisionCount != 0 { t.Fatalf("counter advanced: %+v %v", a, err) }
	unchanged, err := s.GetDesignRequest(designTestOwner, r.ID)
	if err != nil || unchanged.Revision != r.Revision || unchanged.State != DesignRunning { t.Fatalf("request changed: %+v %v", unchanged, err) }
	if err := s.Close(); err != nil { t.Fatal(err) }
	s, err = Open(path)
	if err != nil { t.Fatal(err) }
	if _, err := s.PublishDesignRevision(designTestOwner, r.ID, pub); err != nil { t.Fatalf("retry after failure: %v", err) }
}

package pebblestore

import (
	"bytes"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

func evidenceResponse(r DesignRequest, candidate int, content []byte) DesignResponseMutation {
	a := r.Candidates[candidate].Attempts[len(r.Candidates[candidate].Attempts)-1]
	return DesignResponseMutation{IdempotencyKey: "response", ExpectedRevision: r.Revision, Response: DesignResponse{
		Ref:      DesignOutputRef{RequestID: r.ID, Candidate: candidate, Attempt: a.Number, ChildSessionID: a.ChildSessionID, RunID: a.RunID, SHA256: designDigest(content)},
		Provider: "test", Model: "configured", Content: content,
	}}
}

func evidencePNG(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// Purpose: RecordDesignResponse/Validation and PublishDesignRevision must retain
// failed bytes without making them ready or changing selected/sibling revisions.
// Real Pebble is the narrowest layer proving immutable evidence, atomic rejection,
// principal isolation, exact digest reads, receipt replay and restart durability.
func TestDesignEvidenceFailureIsolationAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	p := designTestOwner
	submit := designTestSubmit("initial", DesignHTML)
	submit.Candidates = append(submit.Candidates, DesignCandidateSpec{ArtifactID: "sibling", Kind: DesignHTML, Operation: DesignGenerate, Brief: "Sibling"})
	r, err := s.SubmitDesignRequest(p, submit)
	if err != nil {
		t.Fatal(err)
	}
	r = designTestStart(t, s, r, 0, "original")
	r, base := designTestPublish(t, s, r, 0, []byte("<html>original</html>"))
	selected, err := s.SelectDesignRevision(p, DesignSelection{IdempotencyKey: "select", Ref: base})
	if err != nil {
		t.Fatal(err)
	}
	r = designTestStart(t, s, r, 1, "sibling-child")
	r, err = s.PublishDesignRevision(p, r.ID, DesignPublication{IdempotencyKey: "publish-sibling", ExpectedRevision: r.Revision, Candidate: 1, ChildSessionID: "sibling-child", RunID: "sibling-child-run", Kind: DesignHTML, Content: []byte("<html>sibling</html>")})
	if err != nil {
		t.Fatal(err)
	}
	sibling := *r.Candidates[1].Attempts[0].Result
	edit := designTestSubmit("edit", DesignHTML)
	edit.Candidates[0].Operation, edit.Candidates[0].Base = DesignEdit, &base
	r, err = s.SubmitDesignRequest(p, edit)
	if err != nil {
		t.Fatal(err)
	}
	r = designTestStart(t, s, r, 0, "repairable")
	in := evidenceResponse(r, 0, []byte("PRIVATE_BROKEN_OUTPUT"))
	in.Response.Usage = &DesignResponseUsage{InputTokens: 17, OutputTokens: 9}
	ref := in.Response.Ref
	for _, foreign := range []DesignPrincipal{{AccountID: "other", PrincipalID: p.PrincipalID}, {AccountID: p.AccountID, PrincipalID: "other"}} {
		if _, err := s.RecordDesignResponse(foreign, in); !errors.Is(err, ErrDesignNotFound) {
			t.Fatalf("foreign write: %v", err)
		}
		if _, err := s.ReadDesignResponse(foreign, ref); !errors.Is(err, ErrDesignNotFound) {
			t.Fatalf("foreign read: %v", err)
		}
	}
	for name, mutate := range map[string]func(*DesignResponseMutation){
		"digest": func(x *DesignResponseMutation) { x.Response.Ref.SHA256 = designDigest(nil) },
		"oversize": func(x *DesignResponseMutation) {
			x.Response.Content = make([]byte, MaxDesignContentBytes+1)
			x.Response.Ref.SHA256 = designDigest(x.Response.Content)
		},
		"usage": func(x *DesignResponseMutation) { x.Response.Usage = &DesignResponseUsage{InputTokens: -1} },
		"child": func(x *DesignResponseMutation) { x.Response.Ref.ChildSessionID = "wrong" },
		"stale": func(x *DesignResponseMutation) { x.ExpectedRevision++ },
	} {
		x := in
		mutate(&x)
		if _, err := s.RecordDesignResponse(p, x); err == nil {
			t.Fatalf("accepted %s", name)
		}
		got, err := s.GetDesignRequest(p, r.ID)
		if err != nil || !reflect.DeepEqual(got, r) {
			t.Fatalf("partial %s: %v", name, err)
		}
		if _, err := s.ReadDesignResponse(p, ref); !errors.Is(err, ErrDesignNotFound) {
			t.Fatalf("partial bytes %s: %v", name, err)
		}
	}
	r, err = s.RecordDesignResponse(p, in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordDesignResponse(p, in); err != nil {
		t.Fatal(err)
	}
	x := in
	x.IdempotencyKey, x.ExpectedRevision = "duplicate", r.Revision
	if _, err := s.RecordDesignResponse(p, x); !errors.Is(err, ErrDesignConflict) {
		t.Fatalf("duplicate: %v", err)
	}
	x = in
	x.Response.Content = []byte("different")
	x.Response.Ref.SHA256 = designDigest(x.Response.Content)
	if _, err := s.RecordDesignResponse(p, x); !errors.Is(err, ErrDesignConflict) {
		t.Fatalf("changed replay: %v", err)
	}
	stale := ref
	stale.SHA256 = designDigest(nil)
	if _, err := s.ReadDesignResponse(p, stale); !errors.Is(err, ErrDesignConflict) {
		t.Fatalf("stale ref: %v", err)
	}
	pub := DesignPublication{IdempotencyKey: "publish-evidence", ExpectedRevision: r.Revision, ChildSessionID: ref.ChildSessionID, RunID: ref.RunID, Kind: DesignHTML, Content: in.Response.Content}
	if _, err := s.PublishDesignRevision(p, r.ID, pub); err == nil {
		t.Fatal("unvalidated response published")
	}
	v := DesignValidationMutation{IdempotencyKey: "validate", ExpectedRevision: r.Revision, Output: ref, Code: "invalid_html"}
	bad := v
	bad.Code = "console contains private output"
	if _, err := s.RecordDesignValidation(p, bad); !errors.Is(err, ErrDesignInvalid) {
		t.Fatalf("unsafe diagnostic: %v", err)
	}
	r, err = s.RecordDesignValidation(p, v)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordDesignValidation(p, v); err != nil {
		t.Fatal(err)
	}
	bad = v
	bad.IdempotencyKey, bad.ExpectedRevision, bad.Code = "rewrite", r.Revision, "browser_runtime_error"
	if _, err := s.RecordDesignValidation(p, bad); !errors.Is(err, ErrDesignConflict) {
		t.Fatalf("validation overwrite: %v", err)
	}
	pub.ExpectedRevision = r.Revision
	if _, err := s.PublishDesignRevision(p, r.ID, pub); !errors.Is(err, ErrDesignConflict) {
		t.Fatalf("failed response published: %v", err)
	}
	status, _ := json.Marshal(r)
	if bytes.Contains(status, in.Response.Content) || bytes.Contains(status, []byte(`"content"`)) {
		t.Fatal("raw response in status")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	out, err := s.ReadDesignResponse(p, ref)
	if err != nil || !reflect.DeepEqual(out, in.Response) {
		t.Fatalf("reopen output: %+v %v", out, err)
	}
	if _, err := s.RecordDesignResponse(p, in); err != nil {
		t.Fatal(err)
	}
	if got, err := s.ReadDesignValidation(p, ref); err != nil || got.Passed || got.Code != "invalid_html" {
		t.Fatalf("reopen validation: %+v %v", got, err)
	}
	got, err := s.GetDesignArtifact(p, base.ArtifactID)
	if err != nil || !reflect.DeepEqual(got, selected) {
		t.Fatalf("selection changed: %+v %v", got, err)
	}
	if _, err := s.ReadDesignRevision(p, sibling); err != nil {
		t.Fatal(err)
	}
}

// Purpose: evidence-backed HTML publication requires the exact validated response
// and an authentic bounded PNG; plans require text validation without rendering.
// Store-level positive and negative assertions prove publication cannot substitute
// bytes or treat a plan receipt as HTML approval; no browser execution is claimed.
func TestDesignEvidenceSuccessfulPublication(t *testing.T) {
	for _, kind := range []string{DesignHTML, DesignPlan} {
		t.Run(kind, func(t *testing.T) {
			s, err := Open(filepath.Join(t.TempDir(), "db"))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			p := designTestOwner
			r, err := s.SubmitDesignRequest(p, designTestSubmit("valid", kind))
			if err != nil {
				t.Fatal(err)
			}
			r = designTestStart(t, s, r, 0, "child")
			in := evidenceResponse(r, 0, []byte("<html>exact response</html>"))
			r, err = s.RecordDesignResponse(p, in)
			if err != nil {
				t.Fatal(err)
			}
			v := DesignValidationMutation{IdempotencyKey: "validate", ExpectedRevision: r.Revision, Output: in.Response.Ref, Passed: true, Code: "plan_valid"}
			if kind == DesignHTML {
				if _, err := s.RecordDesignValidation(p, v); err == nil {
					t.Fatal("plan evidence accepted for HTML")
				}
				v.Code = "renderable"
				for _, data := range [][]byte{nil, []byte("not PNG"), make([]byte, MaxDesignPreviewBytes+1)} {
					v.PNG = data
					if _, err := s.RecordDesignValidation(p, v); !errors.Is(err, ErrDesignInvalid) {
						t.Fatalf("invalid PNG: %v", err)
					}
				}
				v.PNG = evidencePNG(t)
			}
			r, err = s.RecordDesignValidation(p, v)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.RecordDesignValidation(p, v); err != nil {
				t.Fatal(err)
			}
			if kind == DesignHTML {
				preview := *r.Candidates[0].Attempts[0].Validation.Preview
				foreign := DesignPrincipal{AccountID: p.AccountID, PrincipalID: "foreign"}
				if _, err := s.ReadDesignPreview(foreign, preview); !errors.Is(err, ErrDesignNotFound) {
					t.Fatalf("foreign preview: %v", err)
				}
				if _, err := s.RecordDesignValidation(foreign, v); !errors.Is(err, ErrDesignNotFound) {
					t.Fatalf("foreign validation: %v", err)
				}
				data, err := s.ReadDesignPreview(p, preview)
				if err != nil || !bytes.Equal(data, v.PNG) {
					t.Fatalf("preview: %v", err)
				}
				preview.SHA256 = designDigest(nil)
				if _, err := s.ReadDesignPreview(p, preview); !errors.Is(err, ErrDesignConflict) {
					t.Fatalf("stale preview: %v", err)
				}
			}
			pub := DesignPublication{IdempotencyKey: "publish", ExpectedRevision: r.Revision, ChildSessionID: in.Response.Ref.ChildSessionID, RunID: in.Response.Ref.RunID, Kind: kind, Content: []byte("substituted")}
			if _, err := s.PublishDesignRevision(p, r.ID, pub); !errors.Is(err, ErrDesignConflict) {
				t.Fatalf("substitution: %v", err)
			}
			pub.Content = in.Response.Content
			r, err = s.PublishDesignRevision(p, r.ID, pub)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.PublishDesignRevision(p, r.ID, pub); err != nil {
				t.Fatal(err)
			}
			a, err := s.GetDesignArtifact(p, "artifact")
			if err != nil || a.RevisionCount != 1 || a.Selected != nil {
				t.Fatalf("publication: %+v %v", a, err)
			}
			history, err := s.DesignHistory(p, a.ID, 0, 1)
			if err != nil || len(history) != 1 || len(history[0].Content) != 0 || history[0].Attempt.Output == nil {
				t.Fatalf("history: %+v %v", history, err)
			}
		})
	}
}

// Purpose: canonical allocation (setDesignAllocationInBatch) must opt runtime
// attempts into evidence before a provider returns, preventing a publication
// bypass when no response was retained. Real canonical mutations are the narrowest
// layer verifying the allocation flag and resolved model binding, not a fixture flag.
func TestDesignEvidenceCanonicalAllocationRequiresResponse(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p := designTestOwner
	ss := NewSessionStore(s)
	for _, in := range []V3SessionMutationInput{
		{SessionID: "parent", UserID: p.PrincipalID, AccountScopeID: p.AccountID, Kind: V3SessionMutationCreateSession, IdempotencyKey: "parent", PayloadHash: "parent", Session: &SessionSnapshot{ID: "parent"}},
		{SessionID: "parent", UserID: p.PrincipalID, AccountScopeID: p.AccountID, Kind: V3SessionMutationRecordRunIntent, IdempotencyKey: "run", PayloadHash: "run", RunIntent: &V3SessionRunIntent{SessionID: "parent", RunID: "parent-run", Status: V3RunIntentPendingExecutor}},
	} {
		if _, err := ss.ApplyV3SessionMutation(in); err != nil {
			t.Fatal(err)
		}
	}
	r, err := s.SubmitDesignRequest(p, designTestSubmit("canonical", DesignHTML))
	if err != nil {
		t.Fatal(err)
	}
	allocation := DesignAllocation{RequestID: r.ID, ExpectedRevision: r.Revision, Attempt: 1, Preference: ModelPreference{Provider: "test", Model: "configured"}}
	if _, err := ss.ApplyV3SessionMutation(NewDesignAllocationMutation(p, allocation)); err != nil {
		t.Fatal(err)
	}
	r, err = s.GetDesignRequest(p, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	a := r.Candidates[0].Attempts[0]
	if !a.EvidenceRequired || a.Provider != "test" || a.Model != "configured" {
		t.Fatalf("missing evidence binding: %+v", a)
	}
	if _, err := s.PublishDesignRevision(p, r.ID, DesignPublication{IdempotencyKey: "bypass", ExpectedRevision: r.Revision, ChildSessionID: a.ChildSessionID, RunID: a.RunID, Kind: DesignHTML, Content: []byte("<html>bypass</html>")}); !errors.Is(err, ErrDesignConflict) {
		t.Fatalf("bypass: %v", err)
	}
	in := evidenceResponse(r, 0, []byte{0xff, 0x00})
	wrong := in
	wrong.Response.Model = "wrong-model"
	if _, err := s.RecordDesignResponse(p, wrong); !errors.Is(err, ErrDesignConflict) {
		t.Fatalf("model substitution: %v", err)
	}
	if _, err := s.RecordDesignResponse(p, in); err != nil {
		t.Fatal(err)
	}
	out, err := s.ReadDesignResponse(p, in.Response.Ref)
	if err != nil || !bytes.Equal(out.Content, in.Response.Content) {
		t.Fatalf("invalid bytes lost: %v", err)
	}
	artifact, err := s.GetDesignArtifact(p, "artifact")
	if err != nil || artifact.RevisionCount != 0 || artifact.Selected != nil {
		t.Fatalf("partial publication: %+v %v", artifact, err)
	}
}

// Purpose: RecordDesignResponse and RecordDesignValidation must commit evidence,
// request metadata and idempotency receipts together. Real read-only Pebble commit
// failures are the narrowest deterministic layer proving no partial response,
// validation or preview survives and the identical operation can later succeed.
func TestDesignEvidenceWriteFailureAtomicity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	p := designTestOwner
	r, err := s.SubmitDesignRequest(p, designTestSubmit("atomic", DesignHTML))
	if err != nil {
		t.Fatal(err)
	}
	r = designTestStart(t, s, r, 0, "child")
	in := evidenceResponse(r, 0, []byte("<html>atomic</html>"))
	reopen := func(readOnly bool) {
		t.Helper()
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		if readOnly {
			s, err = OpenReadOnly(path)
		} else {
			s, err = Open(path)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	assertAbsent := func(keys ...string) {
		t.Helper()
		for _, key := range keys {
			if _, ok, err := s.GetBytes(key); err != nil || ok {
				t.Fatalf("partial evidence %s: %v", key, err)
			}
		}
		got, err := s.GetDesignRequest(p, r.ID)
		if err != nil || !reflect.DeepEqual(got, r) {
			t.Fatalf("partial request: %v", err)
		}
	}
	reopen(true)
	if _, err := s.RecordDesignResponse(p, in); err == nil {
		t.Fatal("read-only response succeeded")
	}
	assertAbsent(designEvidenceKey(p, "response", in.Response.Ref), designKey(p, "response-receipt", r.ID+"/response"))
	reopen(false)
	r, err = s.RecordDesignResponse(p, in)
	if err != nil {
		t.Fatal(err)
	}
	v := DesignValidationMutation{IdempotencyKey: "validate", ExpectedRevision: r.Revision, Output: in.Response.Ref, Passed: true, Code: "renderable", PNG: evidencePNG(t)}
	reopen(true)
	if _, err := s.RecordDesignValidation(p, v); err == nil {
		t.Fatal("read-only validation succeeded")
	}
	assertAbsent(designEvidenceKey(p, "validation", v.Output), designEvidenceKey(p, "preview", v.Output), designKey(p, "validation-receipt", r.ID+"/validate"))
	reopen(false)
	if _, err := s.RecordDesignValidation(p, v); err != nil {
		t.Fatal(err)
	}
}

// Purpose: concurrent RecordDesignResponse calls at the same revision must have
// one winner and one conflict, never replacing immutable bytes or leaving a loser
// receipt. The real-Pebble store boundary is sufficient to prove its mutex/CAS
// contract with two bounded goroutines and no provider workload simulation.
func TestDesignEvidenceConcurrentCAS(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p := designTestOwner
	r, err := s.SubmitDesignRequest(p, designTestSubmit("concurrent", DesignHTML))
	if err != nil {
		t.Fatal(err)
	}
	r = designTestStart(t, s, r, 0, "child")
	inputs := []DesignResponseMutation{evidenceResponse(r, 0, []byte("first")), evidenceResponse(r, 0, []byte("second"))}
	inputs[1].IdempotencyKey = "second"
	results := make([]error, 2)
	var wg sync.WaitGroup
	for i := range inputs {
		wg.Add(1)
		go func(i int) { defer wg.Done(); _, results[i] = s.RecordDesignResponse(p, inputs[i]) }(i)
	}
	wg.Wait()
	winner := -1
	for i, err := range results {
		if err == nil {
			if winner != -1 {
				t.Fatal("multiple winners")
			}
			winner = i
		} else if !errors.Is(err, ErrDesignConflict) {
			t.Fatal(err)
		}
	}
	if winner == -1 {
		t.Fatal("no winner")
	}
	got, err := s.GetDesignRequest(p, r.ID)
	if err != nil || got.Revision != r.Revision+1 {
		t.Fatalf("CAS revision: %+v %v", got, err)
	}
	out, err := s.ReadDesignResponse(p, inputs[winner].Response.Ref)
	if err != nil || !bytes.Equal(out.Content, inputs[winner].Response.Content) {
		t.Fatalf("winner lost: %v", err)
	}
	loser := inputs[1-winner]
	if _, ok, err := s.GetBytes(designKey(p, "response-receipt", r.ID+"/"+loser.IdempotencyKey)); err != nil || ok {
		t.Fatalf("loser receipt: %v", err)
	}
}

package pebblestore

import (
	"errors"
	"path/filepath"
	"testing"
)

// Purpose: submitDesignRequestInBatch must resolve plan_source by exact owned
// plan revision, not mutable selection or arbitrary HTML. Real Pebble is the
// narrowest layer proving rejection leaves no request/artifact/context writes.
func TestDesignStorePlanSource(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r, err := s.SubmitDesignRequest(designTestOwner, designTestSubmit("plan", DesignPlan))
	if err != nil {
		t.Fatal(err)
	}
	if r.State != DesignQueued || len(r.Candidates[0].Attempts) != 0 {
		t.Fatal("plan started execution")
	}
	r = designTestStart(t, s, r, 0, "planner")
	_, ref := designTestPublish(t, s, r, 0, []byte("Use a blue card."))
	in := designTestSubmit("render", DesignHTML)
	in.Candidates[0].ArtifactID = "rendered"
	in.Candidates[0].PlanSource = &ref
	accepted, err := s.SubmitDesignRequest(designTestOwner, in)
	if err != nil || accepted.Candidates[0].Spec.PlanSource == nil || *accepted.Candidates[0].Spec.PlanSource != ref {
		t.Fatal("lost exact plan", err)
	}
	for _, tc := range []struct {
		name  string
		owner DesignPrincipal
		kind  string
		hash  string
	}{
		{"hash", designTestOwner, DesignHTML, designDigest([]byte("wrong"))},
		{"kind", designTestOwner, DesignPlan, ref.SHA256},
		{"account", DesignPrincipal{"foreign", "principal"}, DesignHTML, ref.SHA256},
		{"principal", DesignPrincipal{"account", "foreign"}, DesignHTML, ref.SHA256},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := designTestSubmit(tc.name, tc.kind)
			bad.Candidates[0].ArtifactID = "bad-" + tc.name
			badRef := ref
			badRef.SHA256 = tc.hash
			bad.Candidates[0].PlanSource = &badRef
			if _, err := s.SubmitDesignRequest(tc.owner, bad); err == nil {
				t.Fatal("accepted invalid plan")
			}
			if _, err := s.GetDesignRequest(tc.owner, bad.RequestID); !errors.Is(err, ErrDesignNotFound) {
				t.Fatal("partial request", err)
			}
			if _, err := s.GetDesignArtifact(tc.owner, bad.Candidates[0].ArtifactID); !errors.Is(err, ErrDesignNotFound) {
				t.Fatal("partial artifact", err)
			}
		})
	}
	accepted = designTestStart(t, s, accepted, 0, "renderer")
	_, html := designTestPublish(t, s, accepted, 0, []byte("<!doctype html><html></html>"))
	bad := designTestSubmit("html-as-plan", DesignHTML)
	bad.Candidates[0].ArtifactID = "bad-html"
	bad.Candidates[0].PlanSource = &html
	if _, err := s.SubmitDesignRequest(designTestOwner, bad); !errors.Is(err, ErrDesignInvalid) {
		t.Fatal("HTML accepted as plan", err)
	}
	if _, err := s.GetDesignRequest(designTestOwner, bad.RequestID); !errors.Is(err, ErrDesignNotFound) {
		t.Fatal("partial request")
	}
}

// Purpose: submitDesignRequestInBatch owns durable snapshot validation. Invalid
// digest/range/binary provenance must fail atomically without a request or
// artifact. Real Pebble proves these retained postconditions.
func TestDesignStoreSourceProvenance(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, name := range []string{"range", "digest", "binary", "source-hash"} {
		in := designTestSubmit(name, DesignHTML)
		c := &in.Context[0]
		switch name {
		case "range":
			c.LineStart, c.LineEnd = 2, 1
		case "digest":
			c.SHA256 = designDigest([]byte("other"))
		case "binary":
			c.Content = []byte{0}
			c.SHA256 = designDigest(c.Content)
		case "source-hash":
			c.SourceSHA256 = designDigest([]byte("other"))
		}
		if _, err := s.SubmitDesignRequest(designTestOwner, in); !errors.Is(err, ErrDesignInvalid) {
			t.Fatal(name, err)
		}
		if _, err := s.GetDesignRequest(designTestOwner, in.RequestID); !errors.Is(err, ErrDesignNotFound) {
			t.Fatal("partial request", name, err)
		}
		if _, err := s.GetDesignArtifact(designTestOwner, "artifact"); !errors.Is(err, ErrDesignNotFound) {
			t.Fatal("partial artifact", name, err)
		}
	}
}

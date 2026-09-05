package artifactgit

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func openTest(t *testing.T) *Repository {
	t.Helper()
	r, e := Open(context.Background(), filepath.Join(t.TempDir(), "repos"), "artifact_1", Limits{MaxBlobBytes: 1024, MaxCompositionBytes: 4096, MaxParts: 8})
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func boolp(v bool) *bool { return &v }

// Requirement: daemon startup owns a real Git-backed artifact authority, so an
// incomplete installation without Git must fail before readiness with a stable,
// diagnosable prerequisite error instead of crashing on first publication.
func TestOpenRejectsMissingNativeGitBeforeStorageMutation(t *testing.T) {
	oldLookPath := lookPath
	lookPath = func(name string) (string, error) {
		if name != "git" {
			t.Fatalf("looked up %q, want git", name)
		}
		return "", exec.ErrNotFound
	}
	t.Cleanup(func() { lookPath = oldLookPath })

	root := filepath.Join(t.TempDir(), "repos")
	_, err := Open(context.Background(), root, "artifact_1", Limits{})
	if err == nil || !strings.Contains(err.Error(), "artifactgit: native git required") {
		t.Fatalf("Open error = %v, want native Git prerequisite", err)
	}
	if _, statErr := os.Stat(root); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("missing-Git preflight mutated repository root: %v", statErr)
	}
}

func TestHistoricalForkMergeLocksCASRestartAndBundle(t *testing.T) {
	ctx := context.Background()
	r := openTest(t)
	gen, e := r.Genesis(ctx, Genesis{MediaType: "application/test", Parts: map[string]BlobInput{"a": {MediaType: "text/plain", Bytes: []byte("a0")}, "b": {MediaType: "text/plain", Bytes: []byte("b0")}}, Construction: Construction{Kind: "concat-v1", Entries: []ConstructionEntry{{PartID: "b"}, {PartID: "a"}}}})
	if e != nil {
		t.Fatal(e)
	}
	left, e := r.Candidate(ctx, CandidateRequest{ID: "left", Base: gen, Parts: map[string]PartChange{"a": {MediaType: "text/plain", Bytes: []byte("a1")}}})
	if e != nil {
		t.Fatal(e)
	}
	right, e := r.Candidate(ctx, CandidateRequest{ID: "right", Base: gen, Parts: map[string]PartChange{"b": {MediaType: "text/plain", Bytes: []byte("b1")}}})
	if e != nil {
		t.Fatal(e)
	}
	lc, _ := r.ReadCommit(ctx, left)
	gc, _ := r.ReadCommit(ctx, gen)
	if lc.Manifest.Parts[1].Blob != gc.Manifest.Parts[1].Blob {
		t.Fatal("unchanged blob not reused")
	}
	merged, e := r.Merge(ctx, MergeRequest{ID: "merged", Parents: []string{left, right}, Selections: map[string]Selection{"b": {Commit: right, Lock: boolp(true)}}})
	if e != nil {
		t.Fatal(e)
	}
	mc, e := r.ReadCommit(ctx, merged)
	if e != nil || len(mc.Parents) != 2 {
		t.Fatalf("merge parents: %#v %v", mc.Parents, e)
	}
	if mc.Tree == "" {
		t.Fatal("merge tree oid is missing")
	}
	var mergedB Part
	for _, part := range mc.Manifest.Parts {
		if part.ID == "b" {
			mergedB = part
		}
	}
	if mergedB.SourceCommit != right || mergedB.SourcePart != "b" {
		t.Fatalf("merge provenance = %#v", mergedB)
	}
	if _, e = r.AdvanceOfficial(ctx, gen, merged, "accept_1"); e != nil {
		t.Fatal(e)
	}
	if _, e = r.AdvanceOfficial(ctx, gen, left, "stale"); !errors.Is(e, ErrConflict) {
		t.Fatalf("stale CAS=%v", e)
	}
	refs, e := r.ListRefs(ctx, "refs/swarm/candidates/")
	if e != nil || len(refs) != 3 {
		t.Fatalf("sibling refs=%v %v", refs, e)
	}
	if _, e = r.Candidate(ctx, CandidateRequest{ID: "locked", Base: merged, Parts: map[string]PartChange{"b": {MediaType: "text/plain", Bytes: []byte("bad")}}}); !errors.Is(e, ErrLockedPart) {
		t.Fatalf("lock=%v", e)
	}
	if e = r.DeleteCandidate(ctx, "refs/swarm/candidates/left", left); e != nil {
		t.Fatal(e)
	}
	if refs, e := r.ListRefs(ctx, "refs/swarm/candidates/"); e != nil || len(refs) != 2 {
		t.Fatalf("candidate delete=%v %v", refs, e)
	}
	if e = r.IntegrityCheck(ctx); e != nil {
		t.Fatal(e)
	}
	bundle := filepath.Join(t.TempDir(), "artifact.bundle")
	if e = r.Bundle(ctx, bundle); e != nil {
		t.Fatal(e)
	}
	if s, e := os.Stat(bundle); e != nil || s.Size() == 0 {
		t.Fatalf("bundle %v %v", s, e)
	}
	r2, e := Open(ctx, r.root, "artifact_1", Limits{})
	if e != nil {
		t.Fatal(e)
	}
	if got, e := r2.Official(ctx); e != nil || got != merged {
		t.Fatalf("restart official=%s %v", got, e)
	}
}

func TestGenesisRetryRequiresExactTreeAndTransactionsAreImmutable(t *testing.T) {
	ctx := context.Background()
	r := openTest(t)
	genesis := Genesis{MediaType: "text/plain", Content: &BlobInput{MediaType: "text/plain", Bytes: []byte("first")}}
	commit, err := r.Genesis(ctx, genesis)
	if err != nil {
		t.Fatal(err)
	}
	if retried, retryErr := r.Genesis(ctx, genesis); retryErr != nil || retried != commit {
		t.Fatalf("exact genesis retry=%s err=%v", retried, retryErr)
	}
	if _, conflictErr := r.Genesis(ctx, Genesis{MediaType: "text/plain", Content: &BlobInput{MediaType: "text/plain", Bytes: []byte("different")}}); !errors.Is(conflictErr, ErrConflict) {
		t.Fatalf("mismatched genesis retry=%v", conflictErr)
	}
	if err := r.RecordTransaction(ctx, "publish_1", commit); err != nil {
		t.Fatal(err)
	}
	if err := r.RecordTransaction(ctx, "publish_1", commit); err != nil {
		t.Fatalf("transaction replay=%v", err)
	}
	candidate, err := r.Candidate(ctx, CandidateRequest{ID: "candidate", Base: commit, Content: &BlobInput{MediaType: "text/plain", Bytes: []byte("second")}})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.RecordTransaction(ctx, "publish_1", candidate); !errors.Is(err, ErrTransactionReuse) {
		t.Fatalf("transaction reuse=%v", err)
	}
	if got, err := r.Transaction(ctx, "publish_1"); err != nil || got != commit {
		t.Fatalf("transaction=%s err=%v", got, err)
	}
	refs, err := r.ListRefs(ctx, "refs/swarm/transactions/")
	if err != nil || len(refs) != 1 || refs[0].Commit != commit {
		t.Fatalf("transaction refs=%v err=%v", refs, err)
	}
}

func TestMoveMaterializeDeleteAndBounds(t *testing.T) {
	ctx := context.Background()
	r := openTest(t)
	gen, e := r.Genesis(ctx, Genesis{MediaType: "text/plain", Content: &BlobInput{MediaType: "text/plain", Bytes: []byte("hello")}})
	if e != nil {
		t.Fatal(e)
	}
	out := filepath.Join(t.TempDir(), "out")
	if e = r.Materialize(ctx, gen, out); e != nil {
		t.Fatal(e)
	}
	if b, _ := os.ReadFile(filepath.Join(out, "content")); string(b) != "hello" {
		t.Fatalf("content=%q", b)
	}
	newRoot := filepath.Join(t.TempDir(), "moved")
	if e = os.MkdirAll(newRoot, 0o700); e != nil {
		t.Fatal(e)
	}
	if e = os.Rename(r.path, filepath.Join(newRoot, "artifact_1.git")); e != nil {
		t.Fatal(e)
	}
	moved, e := Open(ctx, newRoot, "artifact_1", Limits{})
	if e != nil {
		t.Fatal(e)
	}
	if got, _ := moved.Official(ctx); got != gen {
		t.Fatalf("moved=%s", got)
	}
	if e = moved.Delete(); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(moved.path); !errors.Is(e, os.ErrNotExist) {
		t.Fatalf("delete=%v", e)
	}
	tiny, e := Open(ctx, filepath.Join(t.TempDir(), "repos"), "tiny", Limits{MaxBlobBytes: 2})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = tiny.Genesis(ctx, Genesis{Content: &BlobInput{Bytes: []byte("large")}}); !errors.Is(e, ErrQuotaExceeded) {
		t.Fatalf("quota=%v", e)
	}
}

func TestRejectsHostileIDs(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "repos")
	for _, id := range []string{"../x", "a/b", "refs/heads/x", "", ".abc"} {
		if _, e := Open(ctx, root, id, Limits{}); !errors.Is(e, ErrInvalidID) {
			t.Fatalf("id %q=%v", id, e)
		}
	}
}

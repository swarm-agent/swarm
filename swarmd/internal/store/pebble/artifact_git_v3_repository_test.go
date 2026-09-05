package pebblestore

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Requirement: every Artifact V3 revision is one complete conventional Git
// tree. Regression threat: synthetic per-Part blob stores or stale-base
// selection could replace project history. This package-level test exercises the
// native repository/ref boundary, including owner and path rejection.
func TestArtifactV3RepositoryCompleteTreesCASRestartAndPagination(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	owner := ArtifactV3Owner{AccountScopeID: "account-1", UserID: "user-1", SessionID: "session-1"}
	repository, err := OpenArtifactV3Repository(ctx, root, "artifact-1", owner, ArtifactV3Limits{MaxPageSize: 2, MaxFileBytes: 1024, MaxTreeBytes: 8192, MaxFiles: 32})
	if err != nil { t.Fatal(err) }

	rootProject := artifactV3TestProject(t, "Starter", "free")
	genesis, err := repository.Genesis(ctx, ArtifactV3GenesisRequest{TransactionID: "genesis-1", Project: rootProject})
	if err != nil { t.Fatal(err) }
	if len(genesis.Parents) != 0 || genesis.FileCount != 4 || genesis.Manifest.SchemaVersion != ArtifactV3ManifestVersion { t.Fatalf("genesis = %+v", genesis) }
	if _, err := os.Stat(filepath.Join(root, "artifact-1.git", "objects")); err != nil { t.Fatal(err) }
	if _, err := os.Stat(filepath.Join(root, "artifact-1.git", "parts")); !errors.Is(err, os.ErrNotExist) { t.Fatalf("synthetic parts store exists: %v", err) }

	candidateProject := artifactV3TestProject(t, "Pro", "paid")
	candidate, err := repository.Candidate(ctx, ArtifactV3CandidateRequest{TurnID: "turn-1", CandidateID: "candidate-1", TransactionID: "candidate-tx", BaseCommit: genesis.CommitOID, Project: candidateProject})
	if err != nil { t.Fatal(err) }
	if len(candidate.Parents) != 1 || candidate.Parents[0] != genesis.CommitOID || candidate.FileCount != genesis.FileCount { t.Fatalf("candidate = %+v", candidate) }
	if head, _ := repository.Head(ctx); head != genesis.CommitOID { t.Fatalf("candidate moved head: %s", head) }

	page1, err := repository.ListFiles(ctx, candidate.CommitOID, "", 2)
	if err != nil || len(page1.Files) != 2 || page1.NextCursor == "" { t.Fatalf("page1=%+v err=%v", page1, err) }
	page2, err := repository.ListFiles(ctx, candidate.CommitOID, page1.NextCursor, 2)
	if err != nil || len(page2.Files) != 2 || page2.NextCursor != "" { t.Fatalf("page2=%+v err=%v", page2, err) }
	if len(candidate.Manifest.Parts) != 2 || candidate.Manifest.Parts[1].ID != "pricing" { t.Fatalf("parts lost navigation-only manifest identity: %+v", candidate.Manifest.Parts) }

	selected, err := repository.Select(ctx, ArtifactV3SelectionRequest{TurnID: "turn-1", CandidateID: "candidate-1", TransactionID: "select-tx", ExpectedHead: genesis.CommitOID, Candidate: candidate.CommitOID})
	if err != nil || selected.CommitOID != candidate.CommitOID { t.Fatalf("select=%+v err=%v", selected, err) }
	if _, err := repository.Candidate(ctx, ArtifactV3CandidateRequest{TurnID: "turn-1", CandidateID: "candidate-1", TransactionID: "candidate-tx", BaseCommit: genesis.CommitOID, Project: rootProject}); !errors.Is(err, ErrArtifactV3TxReuse) { t.Fatalf("transaction reuse = %v", err) }
	if _, err := repository.Select(ctx, ArtifactV3SelectionRequest{TurnID: "turn-1", CandidateID: "candidate-1", TransactionID: "stale-select", ExpectedHead: genesis.CommitOID, Candidate: candidate.CommitOID}); !errors.Is(err, ErrArtifactV3Conflict) { t.Fatalf("stale CAS = %v", err) }

	restarted, err := OpenArtifactV3Repository(ctx, root, "artifact-1", owner, ArtifactV3Limits{MaxPageSize: 2, MaxFileBytes: 1024, MaxTreeBytes: 8192, MaxFiles: 32})
	if err != nil { t.Fatal(err) }
	transaction, err := restarted.Transaction(ctx, "select-tx")
	if err != nil || transaction.State != ArtifactV3TransactionApplied || transaction.CommitOID != candidate.CommitOID { t.Fatalf("transaction=%+v err=%v", transaction, err) }
	if err := restarted.IntegrityCheck(ctx); err != nil { t.Fatal(err) }
	if _, err := OpenArtifactV3Repository(ctx, root, "artifact-1", ArtifactV3Owner{AccountScopeID: "account-1", UserID: "other", SessionID: "session-1"}, ArtifactV3Limits{}); !errors.Is(err, ErrArtifactV3Unauthorized) { t.Fatalf("foreign owner = %v", err) }
}

// Requirement: quotas and path containment reject a whole candidate before any
// candidate/head ref becomes visible.
func TestArtifactV3RepositoryRejectsUnsafePathsAndQuotasWithoutRefs(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	r, err := OpenArtifactV3Repository(ctx, root, "artifact-2", ArtifactV3Owner{AccountScopeID: "a", UserID: "u", SessionID: "s"}, ArtifactV3Limits{MaxFileBytes: 1024, MaxTreeBytes: 4096, MaxFiles: 8})
	if err != nil { t.Fatal(err) }
	project := artifactV3TestProject(t, "Starter", "free")
	project.Files["../escape"] = []byte("no")
	if _, err := r.Genesis(ctx, ArtifactV3GenesisRequest{TransactionID: "bad-path", Project: project}); !errors.Is(err, ErrArtifactV3Invalid) { t.Fatalf("unsafe path = %v", err) }
	if _, err := r.Head(ctx); !errors.Is(err, ErrArtifactV3NotFound) { t.Fatalf("head changed: %v", err) }

	project = artifactV3TestProject(t, "Starter", "free")
	project.Files["src/app.js"] = make([]byte, 1025)
	if _, err := r.Genesis(ctx, ArtifactV3GenesisRequest{TransactionID: "too-large", Project: project}); !errors.Is(err, ErrArtifactV3Quota) { t.Fatalf("quota = %v", err) }
	refs, err := r.ListRefs(ctx, "refs/swarm/transactions/", "", 10)
	if err != nil || len(refs.Refs) != 0 { t.Fatalf("transaction refs=%+v err=%v", refs, err) }
}

// Requirement: strict Git verification detects corrupted revision bytes rather
// than falling back to another record or reporting a healthy head.
func TestArtifactV3RepositoryDetectsCorruptGitObject(t *testing.T) {
	ctx := context.Background()
	r, err := OpenArtifactV3Repository(ctx, t.TempDir(), "artifact-3", ArtifactV3Owner{AccountScopeID: "a", UserID: "u", SessionID: "s"}, ArtifactV3Limits{})
	if err != nil { t.Fatal(err) }
	revision, err := r.Genesis(ctx, ArtifactV3GenesisRequest{TransactionID: "genesis", Project: artifactV3TestProject(t, "Starter", "free")})
	if err != nil { t.Fatal(err) }
	objectPath, err := r.GitObjectPath(revision.ManifestBlobOID)
	if err != nil { t.Fatal(err) }
	if err := os.WriteFile(objectPath, []byte("corrupt"), 0o600); err != nil { t.Skipf("Git packed the object before corruption could be injected: %v", err) }
	if err := r.IntegrityCheck(ctx); !errors.Is(err, ErrArtifactV3Integrity) { t.Fatalf("integrity = %v", err) }
}

func artifactV3TestProject(t *testing.T, plan, tier string) ArtifactV3Project {
	t.Helper()
	manifest := ArtifactV3Manifest{SchemaVersion: ArtifactV3ManifestVersion, Entrypoint: "index.html", Parts: []ArtifactV3Part{
		{ID: "hero", Label: "Hero", Locator: ArtifactV3Locator{Kind: "selector", Path: "index.html", Value: "#hero"}},
		{ID: "pricing", Label: "Pricing", Locator: ArtifactV3Locator{Kind: "semantic", Value: "pricing cards", Paths: []string{"index.html", "styles/theme.css", "src/app.js"}}},
	}}
	body, err := json.Marshal(manifest); if err != nil { t.Fatal(err) }
	return ArtifactV3Project{Files: map[string][]byte{
		ArtifactV3ManifestFilename: body,
		"index.html": []byte("<main id=hero>" + plan + "</main><section id=pricing>" + tier + "</section>"),
		"styles/theme.css": []byte("#hero{display:grid}#pricing{color:blue}"),
		"src/app.js": []byte("document.body.dataset.ready='true'"),
	}}
}

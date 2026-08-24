package artifact

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"swarm/packages/swarmd/internal/artifactgit"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestCreateUsesOnlyGitRepositoryAndSurvivesWorkspaceMove(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	t.Setenv("STATE_DIRECTORY", state)
	workspace := filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	resolver := &registryResolver{sessions: []pebblestore.SessionSnapshot{{ID: "session-1", AccountScopeID: "account-1", UserID: "user-1", WorkspacePath: workspace}}}
	metadata := &authorityMetadata{}
	authority := NewAuthority(NewRegistry(resolver, Limits{}), metadata)
	principal := Principal{SessionID: "session-1", AccountScopeID: "account-1", UserID: "user-1"}
	created, err := authority.Create(context.Background(), principal, CreateInput{RequestID: "git-only-create", CollectionID: "collection", CollectionName: "Git", VariantID: "variant", Filename: "note.txt", MediaType: "text/plain", Body: []byte("git only")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(state, "workspaces")); !os.IsNotExist(err) {
		t.Fatalf("former workspace artifact tree exists: %v", err)
	}
	moved := filepath.Join(t.TempDir(), "moved-workspace")
	if err := os.Rename(workspace, moved); err != nil {
		t.Fatal(err)
	}
	resolver.sessions[0].WorkspacePath = moved
	reopened := NewAuthority(NewRegistry(resolver, Limits{}), metadata)
	body, err := reopened.ReadVariant(context.Background(), principal, created, 1024)
	if err != nil || string(body) != "git only" {
		t.Fatalf("body=%q err=%v", body, err)
	}
}

func TestPublishGitVariantAutoAcceptsContinuationFromNonOfficialIteration(t *testing.T) {
	authority, metadata, principal := authorityFixture(t)
	ctx := context.Background()
	repo, err := authority.repository(ctx, "artifact-chain-iteration")
	if err != nil {
		t.Fatal(err)
	}
	root, err := repo.Genesis(ctx, artifactgit.Genesis{MediaType: "text/html", Content: &artifactgit.BlobInput{MediaType: "text/html", Bytes: []byte("root")}})
	if err != nil {
		t.Fatal(err)
	}
	sibling, err := repo.Candidate(ctx, artifactgit.CandidateRequest{ID: "iteration-sibling", Base: root, Content: &artifactgit.BlobInput{MediaType: "text/html", Bytes: []byte("sibling")}})
	if err != nil {
		t.Fatal(err)
	}

	metadata.sourceCollection = pebblestore.SessionArtifactCollection{ID: "iterations", AccountScopeID: principal.AccountScopeID, SessionID: "source-session", Status: pebblestore.SessionArtifactStatusReady}
	metadata.sourceVariant = pebblestore.SessionArtifactVariant{ID: "selected-sibling", CollectionID: "iterations", AccountScopeID: principal.AccountScopeID, SessionID: "source-session", Status: pebblestore.SessionArtifactStatusReady, EventSeq: 41, ArtifactChainID: "artifact-chain-iteration", RepositoryID: "artifact-chain-iteration", CommitOID: sibling, MediaType: "text/html"}
	input := CreateInput{RequestID: "continue-sibling", ArtifactStepID: "continue-step", AutoAccept: true, SourceSessionID: "source-session", SourceCollectionID: "iterations", SourceVariantID: "selected-sibling", SourceEventSeq: 41}
	variant := pebblestore.SessionArtifactVariant{ID: "continued", MediaType: "text/html"}
	if err := authority.publishGitVariant(ctx, principal, input, &variant, []byte("continued")); err != nil {
		t.Fatalf("publish continuation from non-official iteration: %v", err)
	}
	commit, err := repo.ReadCommit(ctx, variant.CommitOID)
	if err != nil {
		t.Fatal(err)
	}
	if len(commit.Parents) != 2 || commit.Parents[0] != root || commit.Parents[1] != sibling {
		t.Fatalf("continued parents = %v, want official then selected sibling", commit.Parents)
	}
	if len(variant.ParentCommitOIDs) != 2 || variant.ParentCommitOIDs[0] != root || variant.ParentCommitOIDs[1] != sibling {
		t.Fatalf("variant parents = %v, want official then selected sibling", variant.ParentCommitOIDs)
	}
	if official, err := repo.Official(ctx); err != nil || official != variant.CommitOID {
		t.Fatalf("official = %q err=%v, want %q", official, err, variant.CommitOID)
	}
}

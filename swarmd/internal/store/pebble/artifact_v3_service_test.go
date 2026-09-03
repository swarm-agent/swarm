package pebblestore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

// Requirement: Git and the canonical V3 mutation/event batch form one
// recoverable Artifact V3 authority. The regression threat is a selected head
// without exact build/preview evidence, stale CAS, or restart-only divergence.
func TestArtifactV3ServiceLifecycleCASAndRecovery(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3SessionForTest(t, sessions, "artifact-v3-service")
	service, err := NewArtifactV3Service(sessions, t.TempDir(), ArtifactV3Limits{MaxFiles: 256, MaxParts: 256})
	if err != nil {
		t.Fatal(err)
	}
	owner := ArtifactV3Owner{AccountScopeID: "account-1", UserID: "user-1", SessionID: "artifact-v3-service"}
	created, err := service.Create(context.Background(), ArtifactV3CreateInput{Owner: owner, ArtifactID: "artifact-1", TransactionID: "genesis", Project: artifactV3TestProject(t, "Starter", "free"), Build: preparedArtifactV3Evidence("build"), Preview: preparedArtifactV3Evidence("preview")})
	if err != nil {
		t.Fatal(err)
	}
	head := created.Repository.HeadCommitOID
	if _, err := service.OpenTurn(context.Background(), ArtifactV3OpenTurnInput{Owner: owner, ArtifactID: "artifact-1", TurnID: "turn-1", ExpectedHead: gitOID("f")}); !errors.Is(err, ErrArtifactV3Conflict) {
		t.Fatalf("stale turn = %v", err)
	}
	if created.Revision.Build.CommitOID != head || created.Revision.Preview.CommitOID != head || created.Candidate == nil || created.Candidate.Status != "selected" {
		t.Fatalf("genesis evidence=%+v", created)
	}
	if _, err := service.OpenTurn(context.Background(), ArtifactV3OpenTurnInput{Owner: owner, ArtifactID: "artifact-1", TurnID: "turn-1", ExpectedHead: head, TargetPartID: "pricing"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.OpenTurn(context.Background(), ArtifactV3OpenTurnInput{Owner: owner, ArtifactID: "artifact-1", TurnID: "turn-1", ExpectedHead: head, TargetPartID: "pricing"}); err != nil {
		t.Fatalf("idempotent turn open: %v", err)
	}

	project := artifactV3TestProject(t, "Pro", "paid")
	badEvidence := ArtifactV3EvidenceProjection{Status: "failed", CommitOID: head, DigestSHA256: "build", Reference: "build-ref"}
	if _, err := service.SubmitCandidate(context.Background(), ArtifactV3SubmitCandidateInput{Owner: owner, ArtifactID: "artifact-1", TurnID: "turn-1", CandidateID: "bad", TransactionID: "bad", ExpectedHead: head, Project: project, Build: badEvidence, Preview: badEvidence}); err == nil {
		t.Fatal("candidate evidence was not bound to candidate commit")
	}
	if got, _ := service.open(context.Background(), owner, "artifact-1"); func() bool { value, _ := got.Head(context.Background()); return value == head }() == false {
		t.Fatal("failed candidate moved head")
	}

	repository, err := service.open(context.Background(), owner, "artifact-1")
	if err != nil {
		t.Fatal(err)
	}
	candidateOID, err := repository.commitProject(context.Background(), project, []string{head}, "candidate")
	if err != nil {
		t.Fatal(err)
	}
	evidence := ArtifactV3EvidenceProjection{Status: "succeeded", CommitOID: candidateOID, DigestSHA256: "evidence", Reference: "immutable-ref"}
	candidate, err := service.SubmitCandidate(context.Background(), ArtifactV3SubmitCandidateInput{Owner: owner, ArtifactID: "artifact-1", TurnID: "turn-1", CandidateID: "candidate-1", TransactionID: "candidate-1", ExpectedHead: head, Project: project, Message: "candidate", Build: evidence, Preview: evidence})
	if err != nil {
		t.Fatal(err)
	}
	if len(candidate.Revision.ChangedFiles) == 0 || candidate.Candidate.CommitOID != candidateOID || candidate.Turn == nil || candidate.Turn.Status != "awaiting_selection" {
		t.Fatalf("candidate projection = %+v", candidate)
	}
	secondProject := artifactV3TestProject(t, "Enterprise", "paid")
	secondOID, err := repository.commitProject(context.Background(), secondProject, []string{head}, "candidate two")
	if err != nil {
		t.Fatal(err)
	}
	secondEvidence := ArtifactV3EvidenceProjection{Status: "succeeded", CommitOID: secondOID, DigestSHA256: "evidence-two", Reference: "immutable-ref-two"}
	if _, err := service.SubmitCandidate(context.Background(), ArtifactV3SubmitCandidateInput{Owner: owner, ArtifactID: "artifact-1", TurnID: "turn-1", CandidateID: "candidate-2", TransactionID: "candidate-2", ExpectedHead: head, Project: secondProject, Message: "candidate two", Build: secondEvidence, Preview: secondEvidence}); err != nil {
		t.Fatalf("second candidate: %v", err)
	}
	selected, err := service.Select(context.Background(), ArtifactV3SelectInput{Owner: owner, ArtifactID: "artifact-1", TurnID: "turn-1", CandidateID: "candidate-1", TransactionID: "select-1", ExpectedHead: head})
	if err != nil {
		t.Fatal(err)
	}
	if selected.Repository.HeadCommitOID != candidateOID || selected.Turn.Status != "selected" {
		t.Fatalf("selected = %+v", selected)
	}
	if _, err := service.Select(context.Background(), ArtifactV3SelectInput{Owner: owner, ArtifactID: "artifact-1", TurnID: "turn-1", CandidateID: "candidate-1", TransactionID: "select-stale", ExpectedHead: head}); err == nil {
		t.Fatal("stale head CAS accepted")
	}

	restarted, err := NewArtifactV3Service(NewSessionStore(store), service.root, service.limits)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := restarted.Recover(context.Background(), owner, "artifact-1")
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Repository.HeadCommitOID != candidateOID || recovered.Revision == nil || recovered.Revision.Build.CommitOID != candidateOID {
		t.Fatalf("recovered = %+v", recovered)
	}
	if _, ok, err := restarted.sessions.GetArtifactV3Repository("account-1", "foreign", "artifact-1"); err == nil || ok {
		t.Fatalf("foreign owner read: ok=%v err=%v", ok, err)
	}
}

// Requirement: a failed initial authoring turn may leave an owner-bound bare
// repository without a head, and daemon recovery must preserve that retryable
// state without projecting success or failing startup.
func TestArtifactV3ServiceRecoveryAllowsEmptyOwnedRepository(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3SessionForTest(t, sessions, "artifact-v3-empty-recovery")
	root := t.TempDir()
	service, err := NewArtifactV3Service(sessions, root, ArtifactV3Limits{})
	if err != nil {
		t.Fatal(err)
	}
	owner := ArtifactV3Owner{AccountScopeID: "account-1", UserID: "user-1", SessionID: "artifact-v3-empty-recovery"}
	if _, err := OpenArtifactV3Repository(context.Background(), root, "artifact-empty", owner, ArtifactV3Limits{}); err != nil {
		t.Fatal(err)
	}
	recovered, err := service.Recover(context.Background(), owner, "artifact-empty")
	if err != nil {
		t.Fatalf("recover empty owned repository: %v", err)
	}
	if recovered.Repository != nil || recovered.Revision != nil || recovered.Turn != nil || recovered.Candidate != nil {
		t.Fatalf("empty recovery projected success: %+v", recovered)
	}
	if _, ok, err := sessions.GetArtifactV3Repository(owner.AccountScopeID, owner.UserID, "artifact-empty"); err != nil || ok {
		t.Fatalf("empty recovery persisted projection: ok=%v err=%v", ok, err)
	}
}

// Requirement: Parts are unbounded by the retired 64-Part V2 schema and remain
// locator projections of one complete commit rather than independently writable
// byte revisions.
func TestArtifactV3ServiceProjectsNinetySixPartManifest(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3SessionForTest(t, sessions, "artifact-v3-96")
	service, err := NewArtifactV3Service(sessions, t.TempDir(), ArtifactV3Limits{MaxFiles: 256, MaxParts: 256})
	if err != nil {
		t.Fatal(err)
	}
	project := artifactV3TestProject(t, "Many", "parts")
	manifest := ArtifactV3Manifest{SchemaVersion: ArtifactV3ManifestVersion, Entrypoint: "index.html"}
	for i := 0; i < 96; i++ {
		manifest.Parts = append(manifest.Parts, ArtifactV3Part{ID: fmt.Sprintf("part-%d", i), Label: fmt.Sprintf("Part %d", i), Locator: ArtifactV3Locator{Kind: "selector", Path: "index.html", Value: fmt.Sprintf("#part-%d", i)}})
	}
	project.Files[ArtifactV3ManifestFilename] = mustArtifactV3JSON(t, manifest)
	owner := ArtifactV3Owner{AccountScopeID: "account-1", UserID: "user-1", SessionID: "artifact-v3-96"}
	created, err := service.Create(context.Background(), ArtifactV3CreateInput{Owner: owner, ArtifactID: "artifact-96", TransactionID: "genesis-96", Project: project, Build: preparedArtifactV3Evidence("build"), Preview: preparedArtifactV3Evidence("preview")})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(created.Revision.Parts); got != 96 {
		t.Fatalf("parts = %d", got)
	}
}

func mustArtifactV3JSON(t *testing.T, value any) []byte {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return body
}
func preparedArtifactV3Evidence(kind string) ArtifactV3EvidenceProjection {
	return ArtifactV3EvidenceProjection{Status: "succeeded", DigestSHA256: kind + "-digest", Reference: kind + "-reference"}
}

// Requirement: cancellation and failure are durable terminal records but never
// advance the official Git head.
func TestArtifactV3ServiceTerminalCandidatePreservesHead(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3SessionForTest(t, sessions, "artifact-v3-terminal")
	service, err := NewArtifactV3Service(sessions, t.TempDir(), ArtifactV3Limits{})
	if err != nil {
		t.Fatal(err)
	}
	owner := ArtifactV3Owner{AccountScopeID: "account-1", UserID: "user-1", SessionID: "artifact-v3-terminal"}
	created, err := service.Create(context.Background(), ArtifactV3CreateInput{Owner: owner, ArtifactID: "artifact-terminal", TransactionID: "genesis", Project: artifactV3TestProject(t, "Starter", "free"), Build: preparedArtifactV3Evidence("build"), Preview: preparedArtifactV3Evidence("preview")})
	if err != nil {
		t.Fatal(err)
	}
	head := created.Repository.HeadCommitOID
	if _, err := service.OpenTurn(context.Background(), ArtifactV3OpenTurnInput{Owner: owner, ArtifactID: "artifact-terminal", TurnID: "turn-1", ExpectedHead: head}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RecordCandidateTerminal(owner, "artifact-terminal", "turn-1", "candidate-failed", "failed-tx", "failed", "build_failed", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RecordCandidateTerminal(owner, "artifact-terminal", "turn-1", "candidate-cancelled", "cancelled-tx", "cancelled", "", 0); err != nil {
		t.Fatal(err)
	}
	repository, err := service.open(context.Background(), owner, "artifact-terminal")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := repository.Head(context.Background()); err != nil || got != head {
		t.Fatalf("head=%s err=%v", got, err)
	}
}

// Requirement: if Git commits but the durable event handoff is interrupted,
// restart recovery deterministically projects the transaction ref and head.
func TestArtifactV3ServiceRecoversInterruptedGitEventHandoff(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	createV3SessionForTest(t, sessions, "artifact-v3-recovery")
	service, err := NewArtifactV3Service(sessions, t.TempDir(), ArtifactV3Limits{})
	if err != nil {
		t.Fatal(err)
	}
	owner := ArtifactV3Owner{AccountScopeID: "account-1", UserID: "user-1", SessionID: "artifact-v3-recovery"}
	restore := sessions.SetArtifactV3CommitHookForTest(func(string) error { return errors.New("injected durable handoff failure") })
	_, err = service.Create(context.Background(), ArtifactV3CreateInput{Owner: owner, ArtifactID: "artifact-recovery", TransactionID: "genesis-recovery", Project: artifactV3TestProject(t, "Starter", "free"), Build: preparedArtifactV3Evidence("build"), Preview: preparedArtifactV3Evidence("preview")})
	restore()
	if err == nil {
		t.Fatal("injected handoff failure was ignored")
	}
	if _, ok, readErr := sessions.GetArtifactV3Repository("account-1", "user-1", "artifact-recovery"); readErr != nil || ok {
		t.Fatalf("projection committed: ok=%v err=%v", ok, readErr)
	}
	recovered, err := service.Recover(context.Background(), owner, "artifact-recovery")
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Repository == nil || recovered.Repository.HeadCommitOID == "" || recovered.Revision == nil {
		t.Fatalf("recovered = %+v", recovered)
	}
}

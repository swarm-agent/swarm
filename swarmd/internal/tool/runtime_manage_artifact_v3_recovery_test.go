package tool

import (
	"context"
	"testing"

	"swarm/packages/swarmd/internal/artifact"
)

type recoveryLocatorFake struct {
	directArtifactV3RepoFake
	turn, candidate string
}

func (r *recoveryLocatorFake) LocateArtifactV3ExactDraft(_ context.Context, _ ArtifactV3AuthorPrincipal, id, turn, candidate string) (ArtifactV3DraftResumeRequest, any, error) {
	r.turn, r.candidate = turn, candidate
	return ArtifactV3DraftResumeRequest{SessionID: "session-1", ArtifactID: id, TurnID: turn, CandidateID: candidate, ExpectedSequence: 2, ExpectedProjectionSeq: 9}, []string{"preview repair required"}, nil
}

// Requirement: status forwards exact candidate identity and rejects foreign
// scope before repository access. This tool-layer test prevents silent newest
// fallback or principal confusion; runtime/store tests own durable authorization.
func TestArtifactV3RecoveryStatusExactTuple(t *testing.T) {
	repo := &recoveryLocatorFake{}
	r := NewRuntime(1)
	r.SetArtifactV3AuthorService(NewArtifactV3AuthorService(t.TempDir(), repo, &artifactV3BuilderFake{}, &artifactV3PreviewerFake{}))
	principal := artifact.Principal{AccountScopeID: "account-1", UserID: "user-1", SessionID: "session-1"}
	args := map[string]any{"action": "draft_status_v3", "artifact_id": "artifact", "turn_id": "turn", "candidate_id": "older-failed"}
	result, err := r.locateDirectArtifactV3Draft(context.Background(), WorkspaceScope{SessionID: "session-1"}, principal, args)
	if err != nil || repo.turn != "turn" || repo.candidate != "older-failed" {
		t.Fatalf("exact lookup: %v %v", result, err)
	}
	request := result["resume_draft"].(ArtifactV3DraftResumeRequest)
	if request.CandidateID != "older-failed" || request.ExpectedSequence != 2 || result["diagnostics"] == nil {
		t.Fatal("incomplete status")
	}
	repo.turn = ""
	if _, err := r.locateDirectArtifactV3Draft(context.Background(), WorkspaceScope{SessionID: "foreign"}, principal, args); err == nil || repo.turn != "" {
		t.Fatal("foreign scope reached repository")
	}
	if len(repo.turns) != 0 || len(repo.submits) != 0 || len(repo.selected) != 0 {
		t.Fatal("status mutated author state")
	}
}

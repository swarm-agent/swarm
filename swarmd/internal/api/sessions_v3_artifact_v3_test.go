package api

import (
	"context"

	"errors"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type artifactV3APIServiceFake struct {
	artifact       ArtifactV3Artifact
	lastSelect     ArtifactV3SelectCandidateRequest
	selectErr      error
	calls          int
	selectionCount int
}

func (f *artifactV3APIServiceFake) ListArtifacts(_ context.Context, _ ArtifactV3Principal, sessionID string, _ int) ([]ArtifactV3Artifact, error) {
	f.calls++
	if sessionID != f.artifact.OwnerSessionID {
		return nil, pebblestore.ErrArtifactV3Unauthorized
	}
	return []ArtifactV3Artifact{f.artifact}, nil
}
func (f *artifactV3APIServiceFake) GetArtifact(_ context.Context, _ ArtifactV3Principal, sessionID, artifactID string) (ArtifactV3Artifact, error) {
	f.calls++
	if sessionID != f.artifact.OwnerSessionID || artifactID != f.artifact.ID {
		return ArtifactV3Artifact{}, pebblestore.ErrArtifactV3NotFound
	}
	return f.artifact, nil
}
func (f *artifactV3APIServiceFake) ListRevisions(_ context.Context, _ ArtifactV3Principal, sessionID, artifactID, _ string, _ int) (ArtifactV3RevisionPage, error) {
	f.calls++
	if sessionID != f.artifact.OwnerSessionID || artifactID != f.artifact.ID {
		return ArtifactV3RevisionPage{}, pebblestore.ErrArtifactV3NotFound
	}
	return ArtifactV3RevisionPage{Revisions: f.artifact.Revisions}, nil
}
func (f *artifactV3APIServiceFake) GetRevision(_ context.Context, _ ArtifactV3Principal, sessionID, artifactID, commit string) (ArtifactV3Revision, error) {
	f.calls++
	if sessionID != f.artifact.OwnerSessionID || artifactID != f.artifact.ID || (commit != f.artifact.Head.CommitOID && commit != f.artifact.Head.RevisionRef) {
		return ArtifactV3Revision{}, pebblestore.ErrArtifactV3NotFound
	}
	return *f.artifact.Head, nil
}
func (f *artifactV3APIServiceFake) OpenPreview(_ context.Context, _ ArtifactV3Principal, sessionID, artifactID, revision, assetPath, _ string) (ArtifactV3Preview, error) {
	f.calls++
	if sessionID != f.artifact.OwnerSessionID || artifactID != f.artifact.ID || revision != "rev-root" {
		return ArtifactV3Preview{}, pebblestore.ErrArtifactV3NotFound
	}
	if assetPath != "" {
		return ArtifactV3Preview{RevisionRef: revision, CommitOID: f.artifact.Head.CommitOID, MediaType: "text/css", Body: []byte("body{color:navy}"), ETag: `"artifact-v3-root"`}, nil
	}
	return ArtifactV3Preview{RevisionRef: revision, CommitOID: f.artifact.Head.CommitOID, MediaType: "text/html; charset=utf-8", Body: []byte("<!doctype html><title>Artifact V3</title>"), ETag: `"artifact-v3-root"`}, nil
}
func (f *artifactV3APIServiceFake) OpenTurn(_ context.Context, _ ArtifactV3Principal, req ArtifactV3OpenTurnRequest) (ArtifactV3Turn, error) {
	f.calls++
	if req.ArtifactID != f.artifact.ID {
		return ArtifactV3Turn{}, pebblestore.ErrArtifactV3NotFound
	}
	return f.artifact.Turns[0], nil
}
func (f *artifactV3APIServiceFake) SelectCandidate(_ context.Context, _ ArtifactV3Principal, req ArtifactV3SelectCandidateRequest) (ArtifactV3SelectionResult, error) {
	f.calls++
	f.lastSelect = req
	if f.selectErr != nil {
		return ArtifactV3SelectionResult{}, f.selectErr
	}
	if req.ExpectedHeadRef != f.artifact.Head.RevisionRef || req.ExpectedTurnRevision != f.artifact.Turns[0].Revision {
		return ArtifactV3SelectionResult{}, pebblestore.ErrArtifactV3Conflict
	}
	candidate := f.artifact.Turns[0].Candidates[0]
	if candidate.Revision == nil {
		return ArtifactV3SelectionResult{}, errors.New("missing candidate")
	}
	f.selectionCount++
	turn := f.artifact.Turns[0]
	turn.Status, turn.SelectedCandidateID = "selected", candidate.CandidateID
	return ArtifactV3SelectionResult{Head: *candidate.Revision, Turn: turn}, nil
}

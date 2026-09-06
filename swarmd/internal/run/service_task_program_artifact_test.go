package run

import (
	"context"
	"strings"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	"testing"
)

// Purpose: authenticate actual native Git/Pebble genesis and unselected candidate
// outputs against exact child/job metadata and revision evidence. Forged sequence,
// job and owner must reject without changing the selected head.
func TestTaskProgramNativeArtifactAuthority(t *testing.T) {
	svc, id, cleanup := newTaskLaunchPermissionTestService(t)
	defer cleanup()
	parent, _, err := svc.sessions.GetSession(id)
	if err != nil {
		t.Fatal(err)
	}
	owner := pebblestore.ArtifactV3Owner{AccountScopeID: parent.AccountScopeID, UserID: parent.UserID, SessionID: id}
	artifactRoot := t.TempDir()
	artifacts, err := pebblestore.NewArtifactV3Service(svc.sessions.Store(), artifactRoot, pebblestore.ArtifactV3Limits{})
	if err != nil {
		t.Fatal(err)
	}
	evidence := pebblestore.ArtifactV3EvidenceProjection{Status: "succeeded", DigestSHA256: strings.Repeat("a", 64), Reference: "fixture"}
	project := pebblestore.ArtifactV3Project{Files: map[string][]byte{"swarm-artifact.json": []byte(`{"schema_version":"swarm.artifact/v3","entrypoint":"index.html","parts":[{"id":"main","label":"Main","locator":{"kind":"selector","path":"index.html","value":"#main"}}]}`), "index.html": []byte(`<!doctype html><html><body><main id="main">Fixture</main></body></html>`)}}
	created, err := artifacts.Create(context.Background(), pebblestore.ArtifactV3CreateInput{Owner: owner, ArtifactID: "artifact", TransactionID: "genesis", Project: project, Build: evidence, Preview: evidence})
	if err != nil {
		t.Fatal(err)
	}
	child := pebblestore.SessionSnapshot{ID: "native-child", UserID: parent.UserID, AccountScopeID: parent.AccountScopeID, WorkspacePath: parent.WorkspacePath, Metadata: map[string]any{"parent_session_id": id, "parent_task_call_id": "call", "task_program_id": "program", "task_program_job_id": "design", "artifact_v3_owner_session_id": id, "artifact_v3_artifact_id": "artifact", "artifact_v3_turn_id": "turn", "artifact_v3_candidate_id": "candidate", "artifact_v3_initial": true}}
	if _, err := svc.sessions.ApplySessionMutation(sessionruntime.SessionMutationInput{SessionID: child.ID, UserID: child.UserID, AccountScopeID: child.AccountScopeID, Kind: sessionruntime.SessionMutationCreateSession, Session: &child, IdempotencyKey: "child", ClientRequestID: "child", PayloadHash: "child", RequestHash: "child"}); err != nil {
		t.Fatal(err)
	}
	ref := pebblestore.TaskProgramArtifactRef{SessionID: id, ArtifactID: "artifact", CommitOID: created.Revision.CommitOID, ProjectionSeq: created.Revision.EventSeq, TurnID: "turn", CandidateID: "candidate"}
	if err := svc.sessions.ValidateTaskProgramArtifact(parent, child.ID, "call", "program", "design", ref); err != nil {
		t.Fatal(err)
	}
	if _, err := artifacts.OpenTurn(context.Background(), pebblestore.ArtifactV3OpenTurnInput{Owner: owner, ArtifactID: "artifact", TurnID: "turn", ExpectedHead: ref.CommitOID}); err != nil {
		t.Fatal(err)
	}
	project.Files["index.html"] = []byte(`<!doctype html><html><body><main id="main">Candidate</main></body></html>`)
	gitRepo, err := pebblestore.OpenArtifactV3Repository(context.Background(), artifactRoot, "artifact", owner, pebblestore.ArtifactV3Limits{})
	if err != nil {
		t.Fatal(err)
	}
	gitCandidate, err := gitRepo.Candidate(context.Background(), pebblestore.ArtifactV3CandidateRequest{TurnID: "turn", CandidateID: "candidate", TransactionID: "candidate-tx", BaseCommit: ref.CommitOID, Project: project})
	if err != nil {
		t.Fatal(err)
	}
	evidence.CommitOID = gitCandidate.CommitOID
	candidate, err := artifacts.SubmitCandidate(context.Background(), pebblestore.ArtifactV3SubmitCandidateInput{Owner: owner, ArtifactID: "artifact", TurnID: "turn", CandidateID: "candidate", TransactionID: "candidate-tx", ExpectedHead: ref.CommitOID, Project: project, Build: evidence, Preview: evidence})
	if err != nil {
		t.Fatal(err)
	}
	child.Metadata["artifact_v3_initial"] = false
	if _, err := svc.sessions.ApplySessionMutation(sessionruntime.SessionMutationInput{SessionID: child.ID, UserID: child.UserID, AccountScopeID: child.AccountScopeID, Kind: sessionruntime.SessionMutationUpdateMetadata, Session: &child, IdempotencyKey: "child-update", ClientRequestID: "child-update", PayloadHash: "child-update", RequestHash: "child-update"}); err != nil {
		t.Fatal(err)
	}
	ref.CommitOID = candidate.Revision.CommitOID
	ref.ProjectionSeq = candidate.Revision.EventSeq
	if err := svc.sessions.ValidateTaskProgramArtifact(parent, child.ID, "call", "program", "design", ref); err != nil {
		t.Fatal(err)
	}
	bad := ref
	bad.ProjectionSeq++
	if err := svc.sessions.ValidateTaskProgramArtifact(parent, child.ID, "call", "program", "design", bad); err == nil {
		t.Fatal("forged sequence accepted")
	}
	if err := svc.sessions.ValidateTaskProgramArtifact(parent, child.ID, "call", "program", "other-job", ref); err == nil {
		t.Fatal("wrong job accepted")
	}
	bad = ref
	bad.SessionID = "other"
	if err := svc.sessions.ValidateTaskProgramArtifact(parent, child.ID, "call", "program", "design", bad); err == nil {
		t.Fatal("wrong owner accepted")
	}
	repo, _, err := svc.sessions.Store().GetArtifactV3Repository(parent.AccountScopeID, parent.UserID, "artifact")
	if err != nil || repo.HeadCommitOID != created.Revision.CommitOID {
		t.Fatal("validation advanced selected head")
	}
}

// Purpose: cohort-local candidate index one must not collide between distinct
// program jobs sharing the same task call. Verify the coordinator input identity.
func TestTaskProgramDesignerIdentitySurvivesCohorts(t *testing.T) {
	coordinator := &artifactV3CoordinatorFake{}
	runtime := tool.NewRuntime(1)
	runtime.SetArtifactV3AuthorService(tool.NewArtifactV3AuthorService(t.TempDir(), coordinator, nil, nil))
	svc := &Service{tools: runtime}
	parent := pebblestore.SessionSnapshot{ID: "parent", UserID: "user", AccountScopeID: "account"}
	for _, job := range []string{"first", "second"} {
		if _, err := svc.allocateManagedDesignerArtifactV3(context.Background(), parent, "call", []taskLaunchSpec{{RequestedSubagentType: "designer", OutputMode: "managed", SourceArguments: map[string]any{"program_id": "program", "program_job_id": job}}}); err != nil {
			t.Fatal(err)
		}
	}
	if coordinator.prepared[0].TaskCallID == coordinator.prepared[1].TaskCallID {
		t.Fatal("distinct jobs share artifact identity")
	}
}

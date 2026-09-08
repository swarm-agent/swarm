package pebblestore

import (
	"path/filepath"
	"strings"
	"testing"
)

// Purpose: native outputs and repository lane bindings must survive reopening
// Pebble and reject stale/rebinding/cross-parent transitions without mutation.
// This store-level test proves durable bytes rather than a status projection.
func TestTaskProgramNativeOutputAndLaneReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	sessions := NewSessionStore(db)
	fixture := taskProgramStoreFixture("parent", "native", "hash")
	fixture.Definition.Jobs[0].AgentType = "designer"
	fixture.Definition.Jobs[0].OutputMode = "managed"
	fixture.Definition.Jobs[0].OwnedScope = nil
	record, _, err := sessions.CreateTaskProgram(fixture)
	if err != nil {
		t.Fatal(err)
	}
	ref := TaskProgramArtifactRef{SessionID: "parent", ArtifactID: "artifact", CommitOID: strings.Repeat("a", 40), ProjectionSeq: 7, TurnID: "turn", CandidateID: "candidate"}
	lane := TaskProgramRepositoryLane{SourcePath: "/source", WorkspacePath: "/managed", Branch: "agent/lane", BaseCommit: strings.Repeat("b", 40)}
	record, _, err = sessions.TransitionTaskProgram("parent", "native", TaskProgramTransition{ExpectedRevision: record.Revision, MutationID: "output", RepositoryLane: &lane, Jobs: []TaskProgramJobTransition{{JobID: "api", ExpectedState: "declared", State: "completed", ChildSessionID: "child", ArtifactRef: &ref}}})
	if err != nil {
		t.Fatal(err)
	}
	ref.CommitOID = strings.Repeat("c", 40)
	if _, _, err = sessions.TransitionTaskProgram("parent", "native", TaskProgramTransition{ExpectedRevision: record.Revision, MutationID: "rebind", Jobs: []TaskProgramJobTransition{{JobID: "api", ArtifactRef: &ref}}}); err == nil {
		t.Fatal("artifact rebinding accepted")
	}
	lane.WorkspacePath = "/different"
	if _, _, err = sessions.TransitionTaskProgram("parent", "native", TaskProgramTransition{ExpectedRevision: record.Revision, MutationID: "lane-rebind", RepositoryLane: &lane}); err == nil {
		t.Fatal("lane rebinding accepted")
	}
	if _, _, err = sessions.TransitionTaskProgram("other", "native", TaskProgramTransition{ExpectedRevision: record.Revision, MutationID: "foreign", RepositoryLane: &lane}); err == nil {
		t.Fatal("cross-parent mutation accepted")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	got, ok, err := NewSessionStore(db).GetTaskProgram("parent", "native")
	if err != nil || !ok {
		t.Fatal(err)
	}
	if got.Revision != record.Revision || got.Jobs[0].ArtifactRef == nil || *got.Jobs[0].ArtifactRef != *record.Jobs[0].ArtifactRef || got.RepositoryLane == nil || *got.RepositoryLane != *record.RepositoryLane {
		t.Fatalf("rejected changes leaked or reopen lost identity: %+v", got)
	}
}

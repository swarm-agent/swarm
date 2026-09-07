package run

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement: authorization rejection of a later cohort preserves earlier
// committed and dirty sibling evidence and creates no phantom child. Authority:
// taskProgramScheduler.failUnlaunchedCohort -> TransitionTaskProgram. This narrow
// injected-denial test proves durable rejection postconditions, not a live
// permission dialog or denial inside a provider's executing tool call.
func TestWorkspaceLaunchLaterCohortDenied(t *testing.T) {
	svc, id, cleanup := newTaskLaunchPermissionTestService(t)
	defer cleanup()
	parent, _, err := svc.sessions.GetSession(id)
	if err != nil {
		t.Fatal(err)
	}
	repo := programFixtureRepo(t)
	parent.WorkspacePath = repo
	head := programFixtureGit(t, repo, "rev-parse", "HEAD")
	dirty := filepath.Join(repo, "retained.txt")
	if err := os.WriteFile(dirty, []byte("uncommitted sibling"), 0600); err != nil {
		t.Fatal(err)
	}
	inventory := programFixtureGit(t, repo, "worktree", "list", "--porcelain")
	status := programFixtureGit(t, repo, "status", "--porcelain")
	record := pebblestore.TaskProgramRecord{ParentSessionID: id, ProgramID: "later-denial", DefinitionHash: "fixture", State: "running", ActiveStageID: "later",
		Definition: pebblestore.TaskProgramDefinition{Stages: []pebblestore.TaskProgramStageSpec{{ID: "earlier", DependencyEvidence: "ready"}, {ID: "later", DependsOn: []string{"earlier"}, DependencyEvidence: "prior result"}}, Jobs: []pebblestore.TaskProgramJobSpec{{ID: "done", StageID: "earlier", AgentType: "coder"}, {ID: "dirty", StageID: "earlier", AgentType: "coder"}, {ID: "denied", StageID: "later", AgentType: "coder"}}},
		Jobs:       []pebblestore.TaskProgramJobRecord{{JobID: "done", StageID: "earlier", State: "integrated", ChildSessionID: "done-child", ChildHead: head}, {JobID: "dirty", StageID: "earlier", State: "blocked", ChildSessionID: "dirty-child", WorkspacePath: repo}, {JobID: "denied", StageID: "later", State: "declared"}}}
	record, _, err = svc.sessions.CreateTaskProgram(record)
	if err != nil {
		t.Fatal(err)
	}
	before, err := svc.sessions.ListSessionsForAccountUser(parent.AccountScopeID, parent.UserID, 100)
	if err != nil {
		t.Fatal(err)
	}
	scheduler := taskProgramScheduler{service: svc, parentSession: parent, record: record}
	denial := errors.New("permission denied at later cohort admission")
	if err := scheduler.failUnlaunchedCohort([]int{2}, denial); !errors.Is(err, denial) {
		t.Fatalf("denial lost: %v", err)
	}
	got, ok, err := svc.sessions.GetTaskProgram(id, record.ProgramID)
	if err != nil || !ok {
		t.Fatalf("read: %v %v", ok, err)
	}
	if !reflect.DeepEqual(record.Jobs[:2], got.Jobs[:2]) {
		t.Fatal("earlier sibling evidence rewritten")
	}
	job := got.Jobs[2]
	if got.State != "failed" || job.State != "failed" || job.IntegrationState != "launch_rejected" || job.ChildSessionID != "" || job.Blocker == nil || job.Blocker.Code != "permission_denied" {
		t.Fatalf("invalid denied result: %+v", job)
	}
	after, err := svc.sessions.ListSessionsForAccountUser(parent.AccountScopeID, parent.UserID, 100)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("rejection changed sessions")
	}
	if body, err := os.ReadFile(dirty); err != nil || string(body) != "uncommitted sibling" {
		t.Fatal("dirty bytes lost")
	}
	if programFixtureGit(t, repo, "rev-parse", "HEAD") != head || programFixtureGit(t, repo, "worktree", "list", "--porcelain") != inventory || programFixtureGit(t, repo, "status", "--porcelain") != status {
		t.Fatal("rejection mutated Git state")
	}
}

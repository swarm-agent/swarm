package api

import (
	"fmt"
	"reflect"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: AuthenticateTaskRepositoryContinuation must accept exactly the three
// canonical successful integration receipts, including subsequent cleanup
// failure, without treating any receipt as authority for another job/head/owner.
// The API validator plus real two-repository Git/Pebble fixture is the narrowest
// joined boundary proving valid results remain readable and rejection performs
// no reservation, allocation or source mutation. Actual cleanup outcomes and
// downstream bases are separately exercised by the real scheduler regression.
func TestProjectTaskRepositoryContinuationIntegrationStates(t *testing.T) {
	f, p, project, task, repos := followupSourcesFixture(t)
	stopFollowupSourceRun(t, f, p, project.ID, task.ID)
	task, _, _ = f.server.sessions.Store().GetProjectTask(p.AccountScopeID, project.ID, task.ID)
	program := retainContinuationProgram(t, f, task, repos[:2], "useful", "sentinel")
	refs, err := f.server.resolveTaskRepositoryContinuations(p, task, task.ProgramSources)
	if err != nil || len(refs) != 2 {
		t.Fatalf("cleaned-up fixture selection: %+v %v", refs, err)
	}
	ref := refs[0]
	jobIndex := -1
	for i, job := range program.Jobs {
		if job.SourceWorkspacePath == ref.Source.Path {
			jobIndex = i
		}
	}
	if jobIndex < 0 {
		t.Fatal("fixture lost repository job")
	}
	before := [2]string{followupSourceGit(t, repos[0], "worktree", "list", "--porcelain"), followupSourceGit(t, repos[1], "worktree", "list", "--porcelain")}
	sequence := 0
	check := func(t *testing.T, state string, mutate func(*pebblestore.TaskProgramRecord, *pebblestore.ProjectTaskRepositoryContinuation), wantOK bool) {
		t.Helper()
		sequence++
		candidate := program
		candidate.ProgramID = fmt.Sprintf("state-case-%d", sequence)
		candidate.Definition = program.Definition
		candidate.Definition.ID = candidate.ProgramID
		candidate.Definition.Jobs = append([]pebblestore.TaskProgramJobSpec(nil), program.Definition.Jobs...)
		candidate.DefinitionHash = candidate.ProgramID
		candidate.Revision = 0
		candidate.Jobs = append([]pebblestore.TaskProgramJobRecord(nil), program.Jobs...)
		candidate.Jobs[jobIndex].IntegrationState = state
		proof := ref
		proof.ProgramID = candidate.ProgramID
		if mutate != nil {
			mutate(&candidate, &proof)
		}
		stored, _, err := f.server.sessions.Store().CreateTaskProgram(candidate)
		if err != nil {
			t.Fatal(err)
		}
		proof.ProgramRevision = stored.Revision
		err = f.server.validateTaskRepositoryContinuation(p, task, proof)
		if (err == nil) != wantOK {
			t.Fatalf("state %q: accepted=%v, want %v: %v", state, err == nil, wantOK, err)
		}
		after, found, err := f.server.sessions.Store().GetTaskProgram(task.SessionID, candidate.ProgramID)
		if err != nil || !found || !reflect.DeepEqual(stored, after) {
			t.Fatal("authentication mutated durable program")
		}
		current, found, err := f.server.sessions.Store().GetProjectTask(p.AccountScopeID, project.ID, task.ID)
		if err != nil || !found || !reflect.DeepEqual(task, current) {
			t.Fatal("authentication mutated task or reserved execution")
		}
		for i, repo := range repos[:2] {
			if followupSourceGit(t, repo, "worktree", "list", "--porcelain") != before[i] || followupSourceGit(t, repo, "status", "--porcelain") != "" {
				t.Fatal("authentication changed source or allocated worktrees")
			}
			lane := program.RepositoryLanes[repo]
			if followupSourceGit(t, lane.WorkspacePath, "rev-parse", "HEAD") != program.LaneHeads[repo] {
				t.Fatal("authentication changed retained result")
			}
		}
	}
	for _, state := range []string{"integrated", "integrated_worktree_removed", "integrated_worktree_cleanup_failed"} {
		t.Run(state, func(t *testing.T) {
			check(t, state, nil, true)
			for _, guard := range []struct {
				name   string
				mutate func(*pebblestore.TaskProgramRecord, *pebblestore.ProjectTaskRepositoryContinuation)
			}{
				{"handoff-job", func(r *pebblestore.TaskProgramRecord, _ *pebblestore.ProjectTaskRepositoryContinuation) {
					r.Jobs[jobIndex].State = pebblestore.TaskProgramJobHandoffReady
				}},
				{"failed-job", func(r *pebblestore.TaskProgramRecord, _ *pebblestore.ProjectTaskRepositoryContinuation) {
					r.Jobs[jobIndex].State = pebblestore.TaskProgramJobFailed
				}},
				{"failed-program", func(r *pebblestore.TaskProgramRecord, _ *pebblestore.ProjectTaskRepositoryContinuation) {
					r.State = pebblestore.TaskProgramStateFailed
				}},
				{"empty-child-head", func(r *pebblestore.TaskProgramRecord, _ *pebblestore.ProjectTaskRepositoryContinuation) {
					r.Jobs[jobIndex].ChildHead = ""
				}},
				{"no-child-delta", func(r *pebblestore.TaskProgramRecord, _ *pebblestore.ProjectTaskRepositoryContinuation) {
					r.Jobs[jobIndex].ChildHead = r.Jobs[jobIndex].ImmutableStageBase
				}},
				{"wrong-result-head", func(_ *pebblestore.TaskProgramRecord, ref *pebblestore.ProjectTaskRepositoryContinuation) {
					ref.HeadCommit = ref.Lane.BaseCommit
				}},
				{"foreign-run", func(r *pebblestore.TaskProgramRecord, _ *pebblestore.ProjectTaskRepositoryContinuation) {
					r.ReservationRunID = "foreign"
				}},
				{"stale-generation", func(_ *pebblestore.TaskProgramRecord, ref *pebblestore.ProjectTaskRepositoryContinuation) {
					ref.Source.WorkspaceGeneration++
				}},
				{"foreign-attempt", func(_ *pebblestore.TaskProgramRecord, ref *pebblestore.ProjectTaskRepositoryContinuation) {
					ref.AttemptID = "foreign"
				}},
				{"missing-defined-job", func(r *pebblestore.TaskProgramRecord, _ *pebblestore.ProjectTaskRepositoryContinuation) {
					// One valid receipt must not authorize another Coder definition
					// with no matching integrated job in this repository.
					r.Definition.Jobs[1-jobIndex].WorkspacePath = ref.Source.Path
				}},
			} {
				t.Run(guard.name, func(t *testing.T) { check(t, state, guard.mutate, false) })
			}
		})
	}
	for _, state := range []string{"", "handoff_ready", "failed", "unknown", "integrated_spoofed", "integrated_worktree_removed_spoofed", "integrated_worktree_cleanup_failed_spoofed"} {
		t.Run("reject-"+state, func(t *testing.T) { check(t, state, nil, false) })
	}
}

package pebblestore

import (
	"reflect"
	"strings"
	"testing"
)

// Purpose: ArchiveProjectTaskIfRevision must permit metadata-only archival of
// stale programs whose parent sessions are missing or inactive, without changing
// retained execution records. Active parent programs and foreign ownership must
// still reject archival without partial writes. This real temporary Pebble store
// is the narrowest layer proving the archive guard and durable postconditions.
func TestArchiveProjectTaskStaleProgramSession(t *testing.T) {
	parents := []struct {
		name      string
		exists    bool
		lifecycle *SessionLifecycleSnapshot
		account   string
		blocked   bool
	}{
		{name: "missing"},
		{name: "inactive", exists: true, lifecycle: &SessionLifecycleSnapshot{Active: false, Phase: "completed"}},
		{name: "no_lifecycle", exists: true},
		{name: "active_program", exists: true, lifecycle: &SessionLifecycleSnapshot{Active: true, Phase: "waiting"}, blocked: true},
		{name: "active_run", exists: true, lifecycle: &SessionLifecycleSnapshot{Active: true, Phase: "running"}, blocked: true},
		{name: "foreign_inactive", exists: true, account: "account-b", lifecycle: &SessionLifecycleSnapshot{Active: false, Phase: "completed"}, blocked: true},
	}
	for _, parent := range parents {
		for _, state := range []string{TaskProgramStateDeclared, TaskProgramStateRunning} {
			t.Run(parent.name+"/"+state, func(t *testing.T) {
				db := openTaskProgramTestStore(t)
				s := NewSessionStore(db)
				const account = "account-a"
				const sessionID = "parent"
				if parent.exists {
					sessionAccount := parent.account
					if sessionAccount == "" {
						sessionAccount = account
					}
					if err := s.CreateSession(SessionSnapshot{ID: sessionID, AccountScopeID: sessionAccount, Lifecycle: parent.lifecycle}); err != nil {
						t.Fatal(err)
					}
				}
				fixture := taskProgramStoreFixture(sessionID, "program", "hash")
				fixture.State = state
				program, _, err := s.CreateTaskProgram(fixture)
				if err != nil {
					t.Fatal(err)
				}
				if err := s.PutProject(account, &ProjectRecord{ID: "project", Name: "Project"}); err != nil {
					t.Fatal(err)
				}
				task := &ProjectTaskRecord{ID: "task", ProjectID: "project", Title: "Retained task", Agent: "coder", Status: "in_progress", SessionID: sessionID, TaskProgramID: program.ProgramID, WorktreeBranch: "agent/retained", FullPlanMarkdown: "retained plan", Revision: 1}
				if err := s.PutProjectTask(account, task); err != nil {
					t.Fatal(err)
				}
				before, found, err := s.GetProjectTask(account, task.ProjectID, task.ID)
				if err != nil || !found {
					t.Fatalf("read task: found=%v err=%v", found, err)
				}
				archived, archiveErr := s.ArchiveProjectTaskIfRevision(account, task.ProjectID, task.ID, before.Revision)
				if parent.blocked {
					if archiveErr == nil || !strings.Contains(archiveErr.Error(), "lifecycle") || archived != nil {
						t.Fatalf("active or foreign parent accepted: task=%+v err=%v", archived, archiveErr)
					}
				} else {
					if archiveErr != nil || archived == nil || !archived.Archived || archived.Revision != before.Revision+1 {
						t.Fatalf("stale program prevented archival: task=%+v err=%v", archived, archiveErr)
					}
					before.Archived = true
					before.Revision++
				}
				after, found, err := s.GetProjectTask(account, task.ProjectID, task.ID)
				if err != nil || !found {
					t.Fatalf("read archived task: found=%v err=%v", found, err)
				}
				// UpdatedAt is the only normal persistence metadata besides revision
				// and archive state that a successful archive may change.
				if !parent.blocked {
					before.UpdatedAt = after.UpdatedAt
				}
				if !reflect.DeepEqual(before, after) {
					t.Fatalf("archive changed retained task: before=%+v after=%+v", before, after)
				}
				retained, found, err := s.GetTaskProgram(sessionID, program.ProgramID)
				if err != nil || !found || !reflect.DeepEqual(program, retained) {
					t.Fatalf("archive changed program: program=%+v retained=%+v err=%v", program, retained, err)
				}
			})
		}
	}
}

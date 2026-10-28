package tool

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/session"
	store "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: the runtime must obtain Start tasks from the account, never legacy
// session metadata. A real store with conflicting legacy values proves the
// authority choice at the narrow runtime boundary for both supported values.
func TestAccountTaskRule(t *testing.T) {
	for _, value := range []string{"approve", "auto"} {
		t.Run(value, func(t *testing.T) {
			db, err := store.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			ss := store.NewSessionStore(db)
			if err := db.PutJSON(store.KeySession("s"), store.SessionSnapshot{ID: "s", AccountScopeID: "a", Metadata: map[string]any{store.SessionAccessMetadataKey: store.SessionAccessMetadata(store.SessionAccess{Tasks: "free"})}}); err != nil {
				t.Fatal(err)
			}
			p := store.DefaultAccountPermissions()
			p.Tasks = value
			if _, err := db.SetAccountPermissions("a", 0, p); err != nil {
				t.Fatal(err)
			}
			r := NewRuntime(1)
			defer r.Close()
			r.SetManageSessionService(session.NewService(ss, nil))
			if got := r.sessionTasks("s"); got != value {
				t.Fatalf("got %q want %q", got, value)
			}
		})
	}
}

type startTasksLifecycle struct {
	created  []ProjectTaskCreateInput
	approved []string
}

func (f *startTasksLifecycle) CreateProjectTask(_ context.Context, _ identity.Principal, projectID string, input ProjectTaskCreateInput) (*store.ProjectTaskRecord, error) {
	f.created = append(f.created, input)
	// Like a supplied structured plan: creation leaves the task waiting.
	return &store.ProjectTaskRecord{ID: "task-" + fmt.Sprint(len(f.created)), ProjectID: projectID, Title: input.Title, Status: "pending_approval"}, nil
}
func (f *startTasksLifecycle) DeployProjectTask(context.Context, identity.Principal, string, string) error {
	return errors.New("unexpected deploy")
}
func (f *startTasksLifecycle) ApproveProjectTask(_ context.Context, _ identity.Principal, projectID, taskID string, _ ...ProjectTaskApprovalGuards) (*store.ProjectTaskRecord, error) {
	f.approved = append(f.approved, taskID)
	return &store.ProjectTaskRecord{ID: taskID, ProjectID: projectID, Status: "in_progress"}, nil
}
func (f *startTasksLifecycle) SubmitProjectTaskPlan(context.Context, session.ProjectTaskPlanSubmissionInput) (session.ProjectTaskPlanSubmissionResult, error) {
	return session.ProjectTaskPlanSubmissionResult{}, errors.New("unexpected plan submission")
}

// Purpose: Start tasks is the only thing that decides whether a new task waits.
// Under auto a manage_projects create starts even with auto_approve false, and
// a task creation left waiting is approved at once; under approve a create
// waits even with auto_approve true. A real account store and a recording
// lifecycle isolate the runtime decision from task execution.
func TestStartTasksAutoNeverWaits(t *testing.T) {
	for _, tc := range []struct {
		rule, autoApprove string
		wantAuto          bool
		wantStatus        string
	}{
		{"auto", "false", true, "in_progress"},
		{"auto", "true", true, "in_progress"},
		{"approve", "true", false, "pending_approval"},
	} {
		t.Run(tc.rule+"/"+tc.autoApprove, func(t *testing.T) {
			db, err := store.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			ss := store.NewSessionStore(db)
			if err := db.PutJSON(store.KeySession("s"), store.SessionSnapshot{ID: "s", AccountScopeID: "a", UserID: "u"}); err != nil {
				t.Fatal(err)
			}
			if err := ss.PutProject("a", &store.ProjectRecord{ID: "p", Name: "P"}); err != nil {
				t.Fatal(err)
			}
			p := store.DefaultAccountPermissions()
			p.Tasks = tc.rule
			if _, err := db.SetAccountPermissions("a", 0, p); err != nil {
				t.Fatal(err)
			}
			lifecycle := &startTasksLifecycle{}
			r := NewRuntime(1)
			defer r.Close()
			r.SetManageSessionService(session.NewService(ss, nil))
			r.SetManageProjectStore(ss)
			r.SetProjectTaskLifecycleService(lifecycle)
			scope := WorkspaceScope{SessionID: "s", Principal: identity.Principal{Type: identity.PrincipalTypeUser, UserID: "u", AccountScopeID: "a"}}
			out, err := r.ExecuteForWorkspaceScopeWithRuntime(context.Background(), scope, Call{Name: "manage_projects", Arguments: `{"action":"create_task","project_id":"p","title":"Plan it","agent":"image","auto_approve":` + tc.autoApprove + `}`})
			if err != nil {
				t.Fatal(err)
			}
			if len(lifecycle.created) != 1 || lifecycle.created[0].AutoApprove != tc.wantAuto {
				t.Fatalf("created: %+v", lifecycle.created)
			}
			if started := len(lifecycle.approved) == 1; started != tc.wantAuto {
				t.Fatalf("approved after create: %v", lifecycle.approved)
			}
			if !strings.Contains(out, `"task_status":"`+tc.wantStatus+`"`) {
				t.Fatalf("status: %s", out)
			}
		})
	}
}

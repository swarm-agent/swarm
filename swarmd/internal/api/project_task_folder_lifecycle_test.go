package api

import (
	"path/filepath"
	"testing"
	"time"

	sessionruntime "swarm/packages/swarmd/internal/session"
	store "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: the real terminal bridge must refresh folder receipts after follow-up
// runs and settle capture failures visibly rather than strand execution. Durable
// session mutations and real Git test the owning boundary without a provider.
// A blocked capture permits store updates, and a task rejected during capture
// must reject stale evidence: the terminal bridge is the narrowest lock boundary.
func TestFolderTaskTerminalRefreshAndFailure(t *testing.T) {
	f := newConfirmationFixture(t, filepath.Join(t.TempDir(), "state"), nil)
	defer f.close()
	p := testPrincipal()
	sid := f.createSession("folder")
	sess, _, _ := f.sessions.GetSession(sid)
	sess.Metadata["project_id"] = "folder-project"
	sess.Metadata["task_id"] = "folder-task"
	apply := func(in sessionruntime.SessionMutationInput) {
		t.Helper()
		in.SessionID, in.AccountScopeID, in.UserID = sid, p.AccountScopeID, p.UserID
		in.PayloadHash = in.ClientRequestID
		if _, err := f.sessions.ApplySessionMutation(in); err != nil {
			t.Fatal(err)
		}
	}
	apply(sessionruntime.SessionMutationInput{Kind: sessionruntime.SessionMutationUpdateMetadata, ClientRequestID: "bind", Session: &sess})
	folder := t.TempDir()
	outboxPinGit(t, folder, "init", "-q", "-b", "main")
	outboxPinGit(t, folder, "commit", "-q", "--allow-empty", "-m", "base")
	task := &store.ProjectTaskRecord{ID: "folder-task", Title: "Design", ProjectID: "folder-project", AccountID: p.AccountScopeID, SessionID: sid, Agent: "designer", Status: "in_progress", WorkspacePath: folder, SourceWorkspace: store.ProjectTaskSource{Path: folder, WorkspaceID: "folder", WorkspaceGeneration: 1, Provenance: "task"}}
	if err := captureFolderTaskBase(task); err != nil {
		t.Fatal(err)
	}
	base := task.BaseCommit
	db := f.sessions.Store()
	if err := db.PutProjectTask(p.AccountScopeID, task); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"first", "send-back", "branch-failure", "stale"} {
		input := sessionruntime.SessionMutationInput{Kind: sessionruntime.SessionMutationAppendMessage, ClientRequestID: id, Message: &store.MessageSnapshot{ID: "message-" + id, Role: "user", Content: "continue"}, RunIntent: &store.V3SessionRunIntent{RunID: id, Status: sessionruntime.RunIntentPendingExecutor}}
		if id == "first" {
			input.Kind = sessionruntime.SessionMutationRecordRunIntent
			input.Message = nil
		}
		apply(input)
		outboxPinGit(t, folder, "commit", "-q", "--allow-empty", "-m", id)
		head := outboxPinGit(t, folder, "rev-parse", "HEAD")
		if id == "branch-failure" {
			outboxPinGit(t, folder, "checkout", "-q", "-b", "other")
		}
		apply(sessionruntime.SessionMutationInput{Kind: sessionruntime.SessionMutationRecordRunIntent, ClientRequestID: id + "-end", RunIntent: &store.V3SessionRunIntent{RunID: id, Status: sessionruntime.RunIntentCompleted}})
		capture := func(snapshot *store.ProjectTaskRecord) error {
			// A concurrent store mutation must finish while capture is still
			// executing, not wait behind a Git-held global project lock.
			done := make(chan error, 1)
			go func() {
				_, err := db.UpdateProjectTask(p.AccountScopeID, task.ProjectID, task.ID, func(current *store.ProjectTaskRecord) error {
					if id == "stale" {
						current.Status = "rejected"
					}
					return nil
				})
				done <- err
			}()
			select {
			case err := <-done:
				if err != nil {
					return err
				}
			case <-time.After(5 * time.Second):
				t.Error("capture holds project store lock")
				return nil
			}
			return captureFolderTaskDelivery(snapshot)
		}
		if err := f.server.reconcileProjectTaskRunLifecycleWithCapture(sessionV3ExecutorJob{Principal: p, SessionID: sid, RunID: id}, sessionruntime.RunIntentCompleted, "", capture); err != nil {
			t.Fatal(err)
		}
		current, _, err := db.GetProjectTask(p.AccountScopeID, task.ProjectID, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if id == "stale" {
			if current.Status != "rejected" || current.Integration != nil {
				t.Fatalf("stale capture applied: %+v", current)
			}
			continue
		}
		if current.Status != "needs_review" || current.BaseCommit != base {
			t.Fatalf("terminal state: %+v", current)
		}
		if id == "branch-failure" {
			if current.Integration != nil || current.LastError == "" || current.ActionNeeded == "" {
				t.Fatalf("invisible failure: %+v", current)
			}
		} else if current.Integration == nil || current.Integration.SourceHead != head {
			t.Fatalf("stale receipt: %+v", current)
		}
	}
}

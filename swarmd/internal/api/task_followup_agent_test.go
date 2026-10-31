package api

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/jobautomation"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// Purpose: a follow-up (message_task) to a finished finder task keeps agent
// finder, so the task's next job end starts only the workflows that apply to
// finder tasks; a workflow scoped to swarm tasks never fires for it. A task
// result that positively has no commits says so (NoCommits), which is what
// lets a review step skip it (Runner.skipEmptyReview) instead of failing with
// "no head to review"; a result whose Git state was not read never claims it.
// Threat: the live defect where a read-only follow-up turned a finder task
// into a swarm task, fired the swarm-scoped review loop and left the task
// needs_you with no head to review.
// Authority: SessionStore.ReserveTaskFollowup (the reservation
// ReopenProjectTask and MessageProjectTask use), the Role taskJobResult
// copies from task.Agent, Automation.Matches (the workflow scope), and
// taskViewAssessedEmpty (taskJobResult's no-commits rule). The store plus the
// pure scope matcher are the narrowest layers that decide which workflows run.
func TestFollowupKeepsFinderAgentAndItsWorkflows(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	db := pebblestore.NewSessionStore(store)
	if err := db.PutProject("account", &pebblestore.ProjectRecord{ID: "project", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if err := db.PutProjectTask("account", &pebblestore.ProjectTaskRecord{ID: "task_find", ProjectID: "project", AccountID: "account", Title: "Find the limit", Agent: "finder", Status: "needs_review"}); err != nil {
		t.Fatal(err)
	}
	task, _, err := db.GetProjectTask("account", "project", "task_find")
	if err != nil {
		t.Fatal(err)
	}
	followup, err := db.ReserveTaskFollowup("account", "project", task.ID, "user", "message-1", "Which file sets it?", task.Revision, 1)
	if err != nil {
		t.Fatal(err)
	}
	if followup.Agent != "finder" || followup.OutcomeType != task.OutcomeType || followup.ActiveAttempt().Role != "finder" {
		t.Fatalf("follow-up agent %q outcome %q role %q, want finder %q finder", followup.Agent, followup.OutcomeType, followup.ActiveAttempt().Role, task.OutcomeType)
	}

	folder := t.TempDir()
	// The result taskJobResult builds for the follow-up's end: Role is the
	// task's agent; a finder commits nothing and its lane is assessed empty.
	result := &jobautomation.Result{V: jobautomation.ResultVersion, JobID: taskRunJobID("project", task.ID), Kind: jobautomation.KindTask,
		Role: followup.Agent, Folder: folder, Status: jobautomation.StatusSucceeded,
		NoCommits: taskViewAssessedEmpty(map[string]any{"delivery_state": "empty"})}
	workflow := func(role string) jobautomation.Automation {
		return jobautomation.Automation{Name: role + " review", Enabled: true, Scope: jobautomation.Scope{Folder: folder, Role: role},
			Steps: []jobautomation.Step{{ID: "review", Type: jobautomation.StepAgent, Worker: "reviewer", Brief: "Review"}}}
	}
	if workflow("swarm").Matches(result) {
		t.Fatal("a workflow scoped to swarm tasks matched the finder task's follow-up")
	}
	if !workflow("finder").Matches(result) {
		t.Fatal("a workflow scoped to finder tasks did not match the finder task's follow-up")
	}
	if !result.NoCommits || result.Git != nil {
		t.Fatalf("finder result = %+v, want positively no commits", result)
	}

	for state, want := range map[string]bool{"empty": true, "report_only": true, "": false, "unavailable": false, "not_integrated": false, "in_folder": false} {
		if got := taskViewAssessedEmpty(map[string]any{"delivery_state": state}); got != want {
			t.Fatalf("delivery_state %q: assessed empty = %v, want %v", state, got, want)
		}
	}
}

// Purpose: a follow-up (message_task or reopen_task) to a designer media task
// (outcome media_bundle, as the task router records) still runs a Swarm
// follow-up session: the reservation continues it as a swarm, general task, so
// deployProjectTaskExecution never takes the direct-media path, which would
// clear the follow-up session, find no deliverables to generate and drop the
// feedback while the attempt is marked launched. A designer artifact task
// keeps its agent and is not a media task either. Threat: the review finding
// where keeping designer plus media_bundle made the follow-up a direct media
// generation. Authority: SessionStore.ReserveTaskFollowup (the reservation
// ReopenProjectTask and MessageProjectTask use) and isDirectMediaTask, the
// rule deployProjectTaskExecution branches on.
func TestFollowupToADesignerMediaTaskRunsASession(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	db := pebblestore.NewSessionStore(store)
	if err := db.PutProject("account", &pebblestore.ProjectRecord{ID: "project", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	// The router records designer media tasks with outcome media_bundle; the
	// create-time contract does not admit them, so the record is written as
	// the router leaves it.
	media := &pebblestore.ProjectTaskRecord{ID: "task_media", ProjectID: "project", AccountID: "account", Title: "Poster set", Agent: "designer", OutcomeType: "media_bundle", Tier: "direct", Status: "needs_review", Revision: 1}
	if err := store.PutJSON(pebblestore.KeyProjectTask("account", "project", media.ID), media); err != nil {
		t.Fatal(err)
	}
	if !isDirectMediaTask(media) {
		t.Fatal("fixture is not a direct media task")
	}
	if err := db.PutProjectTask("account", &pebblestore.ProjectTaskRecord{ID: "task_artifact", ProjectID: "project", AccountID: "account", Title: "Landing page", Agent: "designer", OutcomeType: "artifact", Status: "needs_review"}); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string][2]string{"task_media": {"swarm", "general"}, "task_artifact": {"designer", "artifact"}} {
		task, ok, err := db.GetProjectTask("account", "project", id)
		if err != nil || !ok {
			t.Fatalf("%s: %v %v", id, ok, err)
		}
		followup, err := db.ReserveTaskFollowup("account", "project", id, "user", "message-"+id, "Make the title larger.", task.Revision, 1)
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if followup.Agent != want[0] || followup.OutcomeType != want[1] || followup.ActiveAttempt().Role != want[0] || !strings.HasPrefix(followup.SessionID, "task-followup-") {
			t.Fatalf("%s: follow-up agent %q outcome %q role %q session %q, want %v", id, followup.Agent, followup.OutcomeType, followup.ActiveAttempt().Role, followup.SessionID, want)
		}
		if isDirectMediaTask(followup) {
			t.Fatalf("%s: follow-up takes the direct-media path", id)
		}
		stored, _, _ := db.GetProjectTask("account", "project", id)
		if stored == nil || isDirectMediaTask(stored) || stored.Agent != want[0] {
			t.Fatalf("%s: stored follow-up %+v", id, stored)
		}
	}
}

// Purpose: a follow-up to a design job, through message_task
// (MessageProjectTask) or reopen_task (ReopenProjectTask), is refused with
// errDesignJobNotAgentTask before anything is reserved, as approve and deploy
// refuse it, and the task record (agent, outcome, status, session, attempts,
// revision) is unchanged. Threat: the review finding where a kept
// designer/ui_design agent let a design job's follow-up pass the store's task
// contract and start a task-followup- session that never reports back
// (session sync, Git reconcile and the run-end signal skip design jobs),
// leaving the task in_progress. Authority: MessageProjectTask
// (projectTaskControlTarget) and ReopenProjectTask over a real store; the
// service functions are what the catalog tools and the HTTP reopen route call.
func TestFollowupToADesignJobIsRefused(t *testing.T) {
	for _, state := range []string{pebblestore.DesignJobRunning, pebblestore.DesignJobCompleted} {
		s, store, _, p := newDesignJobTestServer(t)
		if err := store.PutProject(p.AccountScopeID, &pebblestore.ProjectRecord{ID: "project-a", AccountID: p.AccountScopeID, Name: "Project"}); err != nil {
			t.Fatal(err)
		}
		putDesignTask(t, store, p, state)
		before, _, err := store.GetProjectTask(p.AccountScopeID, "project-a", "task_design01")
		if err != nil || before == nil {
			t.Fatalf("%s: %v", state, err)
		}
		_, messageErr := s.MessageProjectTask(context.Background(), p, "project-a", "task_design01", tool.ProjectTaskMessageInput{Message: "Make the header darker.", ClientRequestID: "msg-" + state})
		_, reopenErr := s.ReopenProjectTask(context.Background(), p, "project-a", "task_design01", tool.ProjectTaskFollowupInput{Feedback: "Make the header darker.", ClientRequestID: "reopen-" + state, Revision: before.Revision})
		for name, err := range map[string]error{"message_task": messageErr, "reopen_task": reopenErr} {
			var failure *projectTaskFollowupError
			if !errors.Is(err, errDesignJobNotAgentTask) || !errors.As(err, &failure) || failure.status != http.StatusConflict {
				t.Fatalf("%s %s: %v", state, name, err)
			}
		}
		after, _, err := store.GetProjectTask(p.AccountScopeID, "project-a", "task_design01")
		if err != nil || after == nil || after.Agent != before.Agent || after.OutcomeType != before.OutcomeType || after.Status != before.Status ||
			after.SessionID != before.SessionID || after.Revision != before.Revision || len(after.Attempts) != len(before.Attempts) || after.ActiveAttemptID != before.ActiveAttemptID || after.Design == nil {
			t.Fatalf("%s: refused follow-up changed the task:\nbefore %+v\nafter  %+v", state, before, after)
		}
	}
}

// Purpose: a follow-up attempt keeps the task's agent (finder, coder) for its
// role and workflows, but its session runs as Swarm
// (projectTaskExecutionAgent), so its model is the account's Swarm model and
// thinking, at deployment (resolveTaskModelPreference) and in the model
// preview; the task's initial attempt still resolves the Finder or Coder
// model. Threat: the review finding where a kept agent made the Swarm
// follow-up session run on the account's Finder or Coder model. Authority:
// resolveTaskModelPreference and buildTaskModelPreview over a server with
// seeded account agent-model settings and a follow-up reserved through
// SessionStore.ReserveTaskFollowup.
func TestFollowupAttemptResolvesTheSwarmModel(t *testing.T) {
	s, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	p := testPrincipal()
	db := s.sessions.Store()
	if err := db.PutProject(p.AccountScopeID, &pebblestore.ProjectRecord{ID: "project-m", AccountID: p.AccountScopeID, Name: "Models"}); err != nil {
		t.Fatal(err)
	}
	// Finder and Coder get their own thinking too, so thinking proves which
	// agent's settings a follow-up resolved.
	record := testAgentModelSettingsRecord(p.AccountScopeID)
	record.SystemAgents.Finder.Thinking, record.SystemAgents.Coder.Thinking, record.UpdatedAt = "low", "medium", 2
	if _, err := pebblestore.NewAgentModelSettingsStore(db.Underlying()).PutForAccount(record); err != nil {
		t.Fatal(err)
	}
	settings, err := s.agentModelSettings.GetForAccount(p.AccountScopeID)
	if err != nil {
		t.Fatal(err)
	}
	swarm := settings.Swarm.Action
	for _, c := range []struct {
		agent         string
		own, thinking string
	}{{"finder", settings.SystemAgents.Finder.Model, settings.SystemAgents.Finder.Thinking}, {"coder", settings.SystemAgents.Coder.Model, settings.SystemAgents.Coder.Thinking}} {
		if own := strings.TrimSpace(c.own); own == "" || own == swarm.Model || c.thinking == swarm.Thinking {
			t.Fatalf("fixture: %s %q/%q is not distinct from Swarm's %q/%q", c.agent, own, c.thinking, swarm.Model, swarm.Thinking)
		}
		id := "task-model-" + c.agent
		if err := db.PutProjectTask(p.AccountScopeID, &pebblestore.ProjectTaskRecord{ID: id, ProjectID: "project-m", AccountID: p.AccountScopeID, Title: "Look", Agent: c.agent, Status: "needs_review"}); err != nil {
			t.Fatal(err)
		}
		initial, _, err := db.GetProjectTask(p.AccountScopeID, "project-m", id)
		if err != nil {
			t.Fatal(err)
		}
		pref, _, err := s.resolveTaskModelPreference(p, initial)
		if err != nil || pref.Model != c.own || pref.Thinking != c.thinking {
			t.Fatalf("%s initial attempt resolved %+v %v, want its own %q/%q", c.agent, pref, err, c.own, c.thinking)
		}
		followup, err := db.ReserveTaskFollowup(p.AccountScopeID, "project-m", id, p.UserID, "model-"+id, "Which file?", initial.Revision, 1)
		if err != nil {
			t.Fatal(err)
		}
		if followup.Agent != c.agent {
			t.Fatalf("%s: follow-up agent %q", c.agent, followup.Agent)
		}
		pref, source, err := s.resolveTaskModelPreference(p, followup)
		if err != nil || pref.Model != swarm.Model || pref.Thinking != swarm.Thinking {
			t.Fatalf("%s follow-up resolved %+v (%s) %v, want Swarm %s/%s", c.agent, pref, source, err, swarm.Model, swarm.Thinking)
		}
		preview := s.buildTaskModelPreview(p, followup)
		if preview.Agent != c.agent || preview.ResolvedAgent != "swarm" || preview.ResolvedModel == nil || preview.ResolvedModel.Model != swarm.Model || preview.ResolvedModel.Thinking != swarm.Thinking {
			t.Fatalf("%s follow-up preview %+v", c.agent, preview)
		}
	}
}

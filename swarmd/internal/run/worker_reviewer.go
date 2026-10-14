package run

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"swarm/packages/swarmd/internal/sandbox"
	store "swarm/packages/swarmd/internal/store/pebble"
	"time"
)

func validateReviewRequest(role string, input map[string]any) error {
	job, _ := input["review_job"].(string)
	if raw, exists := input["review_job"]; exists {
		if _, ok := raw.(string); !ok {
			return errors.New("review_job must be a job id string")
		}
	}
	if _, exists := input["review_ref"]; exists {
		return errors.New("review_ref is unsupported; use review_job")
	}
	if role != store.WorkerRoleReviewer {
		if _, exists := input["review_job"]; exists {
			return errors.New("review_job is only accepted for reviewer workers")
		}
		return nil
	}
	parts := strings.Split(job, "/")
	if len(parts) != 3 || parts[0] != "worker" || parts[1] == "" || parts[2] == "" {
		return errors.New("reviewer requires review_job: worker/<worker id>/<run id>")
	}
	return nil
}

// resolveReviewTarget uses only account-authorized durable job/task identities;
// it never treats a caller-supplied path or head as source authority.
func (s *WorkerExecutionService) resolveReviewTarget(ctx context.Context, r store.WorkerRunRecord, w store.WorkerRecord) (map[string]any, error) {
	if err := validateReviewRequest(w.Role, r.Input); err != nil {
		return nil, err
	}
	project, err := s.resolveWorkerProject(r, w)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(r.Input["review_job"].(string), "/")
	ws, err := s.workerStore()
	if err != nil {
		return nil, err
	}
	target, found, err := ws.GetWorkerRun(r.AccountScopeID, parts[1], parts[2])
	if err != nil {
		return nil, err
	}
	if !found || target.UserID != r.UserID {
		return nil, errors.New("review job not found")
	}
	task, found, err := s.host.runs.sessions.Store().GetProjectTask(r.AccountScopeID, project.ID, "task_"+target.ID)
	if err != nil {
		return nil, err
	}
	if !found || task.WorkerID != target.WorkerID || task.WorkerRunID != target.ID || task.SourceWorkspace.WorkspaceID != r.WorkspaceID {
		return nil, errors.New("review job must exist in the same project and workspace")
	}
	if err := s.host.trees.ValidateSessionRepositoryLaneForRead(task.SourceWorkspace.Path, task.WorkspacePath, target.SessionID, task.WorktreeBranch); err != nil {
		return nil, err
	}
	state, err := s.host.trees.InspectTaskWorkspace(task.WorkspacePath)
	if err != nil {
		return nil, err
	}
	if task.BaseCommit == "" || state.HeadCommit == task.BaseCommit {
		return nil, errors.New("review job must have commits")
	}
	descends, err := s.host.trees.TaskCommitDescendsFrom(task.SourceWorkspace.Path, task.BaseCommit, state.HeadCommit)
	if err != nil {
		return nil, err
	}
	if !descends {
		return nil, errors.New("review job head does not descend from its base")
	}
	base := task.BaseBranch
	if base == "" {
		return nil, errors.New("review job has no landing base branch")
	}
	published := "refs/swarm/publish/" + base
	cmd := sandbox.Command(ctx, "-C", task.SourceWorkspace.Path, "rev-parse", "--verify", published+"^{commit}")
	if err := cmd.Run(); err == nil {
		base = published
	}
	report := task.PlanSummary
	if len(task.Attempts) > 0 {
		report = task.Attempts[len(task.Attempts)-1].Summary
	}
	if len(report) > 24000 {
		report = report[:24000]
	}
	brief := task.Description
	if len(brief) > 24000 {
		brief = brief[:24000]
	}
	return map[string]any{"head": state.HeadCommit, "base": base, "job": r.Input["review_job"], "brief": brief, "report": report}, nil
}

func reviewEnvironment(meta map[string]any) map[string]string {
	review, _ := meta["worker_review_target"].(map[string]any)
	env := map[string]string{}
	for name, key := range map[string]string{"SWARM_REVIEW_HEAD": "head", "SWARM_REVIEW_BASE": "base", "SWARM_REVIEW_JOB": "job"} {
		if value, ok := review[key].(string); ok {
			env[name] = value
		}
	}
	return env
}

func (s *WorkerExecutionService) registerReview(snapshot store.SessionSnapshot) error {
	if snapshot.Metadata["worker_role"] != store.WorkerRoleReviewer {
		return nil
	}
	m := sandbox.Default()
	if m == nil {
		return sandbox.ErrSandboxRequired
	}
	if err := m.RegisterReviewCheckout(snapshot.WorktreeRootPath); err != nil {
		return err
	}
	if err := m.BindReviewSession(snapshot.ID, snapshot.WorktreeRootPath); err != nil {
		return err
	}
	return m.SetReviewEnvironment(snapshot.WorktreeRootPath, reviewEnvironment(snapshot.Metadata))
}

func (s *WorkerExecutionService) cleanupReview(r store.WorkerRunRecord) error {
	if r.Role != store.WorkerRoleReviewer {
		return nil
	}
	snapshot, found, err := s.host.runs.sessions.GetSession(r.SessionID)
	if err != nil || !found {
		return err
	}
	head := reviewEnvironment(snapshot.Metadata)["SWARM_REVIEW_HEAD"]
	if head == "" {
		return fmt.Errorf("review cleanup missing captured head")
	}
	remove := func() error {
		return s.host.trees.RemoveReviewWorkspace(snapshot.WorkspacePath, snapshot.WorktreeRootPath, r.SessionID, head)
	}
	if m := sandbox.Default(); m != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return m.CleanupReview(ctx, snapshot.WorktreeRootPath, remove)
	}
	return remove()
}

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
	// Unlanded commits exist only in the job clone validated above.
	descends, err := s.host.trees.TaskCommitDescendsFrom(task.WorkspacePath, task.BaseCommit, state.HeadCommit)
	if err != nil {
		return nil, err
	}
	if !descends {
		return nil, errors.New("review job head does not descend from its base")
	}
	// The task's immutable fork is the normal review base. Pipeline rebase
	// reviews supply their exact comparison commits; these are evidence only,
	// never repository or publication authority, and allocation verifies them.
	base, commits, err := reviewComparisonCommits(task.BaseCommit, r.Input)
	if err != nil {
		return nil, err
	}
	for _, commit := range commits {
		available := false
		for _, source := range []string{task.WorkspacePath, task.SourceWorkspace.Path} {
			out, err := sandbox.Command(ctx, "-C", source, "rev-parse", "--verify", commit+"^{commit}").Output()
			if err == nil && strings.TrimSpace(string(out)) == commit {
				available = true
				break
			}
		}
		if !available {
			return nil, fmt.Errorf("review infrastructure: comparison commit %s unavailable", commit)
		}
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
	captured := map[string]any{"head": state.HeadCommit, "base": base, "commits": commits, "job": r.Input["review_job"], "brief": brief, "report": report}
	// Capture from the same folder definition as landing, never caller input.
	env, found, err := store.NewSandboxEnvironmentStore(s.host.runs.sessions.Store().Underlying()).Get(task.SourceWorkspace.Path)
	if err != nil {
		return nil, err
	}
	if found && env.Account != r.AccountScopeID {
		return nil, errors.New("environment belongs to another account")
	}
	if len(env.Definition.Checks) > 0 {
		commands := make([]string, 0, len(env.Definition.Checks))
		for _, command := range env.Definition.Checks {
			// Landing starts each check in a separate login shell at the checkout.
			commands = append(commands, "bash -lc '"+strings.ReplaceAll(command, "'", "'\"'\"'")+"'")
		}
		captured["checks"] = strings.Join(commands, " && ")
		minutes := env.Definition.CheckTimeoutMinutes
		if minutes == 0 {
			minutes = 20 // Same total checks budget as prepareLandWorktree.
		}
		captured["checks_timeout_seconds"] = fmt.Sprint(minutes * 60)
	}
	return captured, nil
}

func reviewEnvironment(meta map[string]any) map[string]string {
	review, _ := meta["worker_review_target"].(map[string]any)
	env := map[string]string{}
	for name, key := range map[string]string{"SWARM_REVIEW_HEAD": "head", "SWARM_REVIEW_BASE": "base", "SWARM_REVIEW_JOB": "job", "SWARM_REVIEW_CHECKS": "checks", "SWARM_REVIEW_CHECKS_TIMEOUT_SECONDS": "checks_timeout_seconds"} {
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
	db := s.host.runs.sessions.Store()
	state, found, err := db.GetReviewCleanup(snapshot.AccountScopeID, snapshot.ID)
	if err != nil {
		return err
	}
	if found {
		if state.Checkout != snapshot.WorktreeRootPath || state.Session != snapshot.ID {
			return fmt.Errorf("review identity mismatch")
		}
		if err := m.RestoreReviewIdentity(state); err != nil {
			return err
		}
		return m.SetReviewEnvironment(state.Checkout, reviewEnvironment(snapshot.Metadata))
	}
	if err := m.RegisterReviewCheckout(snapshot.WorktreeRootPath); err != nil {
		return err
	}
	if err := m.BindReviewSession(snapshot.ID, snapshot.WorktreeRootPath); err != nil {
		return err
	}
	state, err = m.CaptureReviewIdentity(snapshot.ID, snapshot.WorktreeRootPath)
	if err != nil {
		return err
	}
	if err := db.PutReviewCleanup(snapshot.AccountScopeID, state); err != nil {
		return err
	}
	return m.SetReviewEnvironment(snapshot.WorktreeRootPath, reviewEnvironment(snapshot.Metadata))
}

func (s *WorkerExecutionService) cleanupReview(r store.WorkerRunRecord) error {
	if r.Role != store.WorkerRoleReviewer {
		return nil
	}
	snapshot, found, err := s.host.runs.sessions.GetSession(r.SessionID)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("review cleanup session is missing")
	}
	head := reviewEnvironment(snapshot.Metadata)["SWARM_REVIEW_HEAD"]
	if head == "" {
		return fmt.Errorf("review cleanup missing captured head")
	}
	validate := func() error {
		return s.host.trees.ValidateReviewWorkspaceRemoval(snapshot.WorkspacePath, snapshot.WorktreeRootPath, r.SessionID, head)
	}
	remove := func() error {
		return s.host.trees.RemoveValidatedReviewWorkspace(snapshot.WorkspacePath, snapshot.WorktreeRootPath)
	}
	if m := sandbox.Default(); m != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := s.registerReview(snapshot); err != nil {
			return err
		}
		db := s.host.runs.sessions.Store()
		state, found, err := db.GetReviewCleanup(snapshot.AccountScopeID, snapshot.ID)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("missing durable review identity")
		}
		return m.CleanupReviewDurable(ctx, &state, func(next sandbox.ReviewCleanupState) error {
			return db.PutReviewCleanup(snapshot.AccountScopeID, next)
		}, validate, remove)
	}
	return sandbox.ErrSandboxRequired
}

func reviewComparisonCommits(base string, input map[string]any) (string, []string, error) {
	commits := []string{base}
	if raw, exists := input["review_commits"]; exists {
		commits = nil
		switch values := raw.(type) {
		case []string:
			commits = values
		case []any:
			for _, value := range values {
				text, _ := value.(string)
				commits = append(commits, text)
			}
		}
		if len(commits) == 0 || len(commits) > 4 {
			return "", nil, errors.New("review infrastructure: missing comparison commits")
		}
		base = commits[0]
	}
	for _, commit := range commits {
		if len(commit) != 40 && len(commit) != 64 || strings.Trim(commit, "0123456789abcdef") != "" {
			return "", nil, errors.New("review infrastructure: comparison commit must be an exact SHA")
		}
	}
	return base, commits, nil
}

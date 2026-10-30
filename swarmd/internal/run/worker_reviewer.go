package run

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/sandbox"
	store "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/worktree"
)

// ReviewFixesSecurity is the only review_fixes value: a reviewer an
// automation agent step (fixes "security") started may commit small security
// fixes on its own branch at the reviewed head.
const ReviewFixesSecurity = "security"

// reviewFixesRequestSource is the only request source that may set
// review_fixes: a job automation's agent step. A reviewer started any other
// way stays read-only; Swarm never decides on its own to let one write.
const reviewFixesRequestSource = "automation"

// reviewFixesAllowed reports whether a run's input lets its reviewer commit
// security fixes (admission validated it).
func reviewFixesAllowed(input map[string]any) bool {
	value, _ := input["review_fixes"].(string)
	return value == ReviewFixesSecurity
}

// sessionReviewFixes reports whether a reviewer session was admitted with
// review fixes (its pinned metadata).
func sessionReviewFixes(meta map[string]any) bool {
	return mapString(meta, "worker_role") == store.WorkerRoleReviewer && mapString(meta, "worker_review_fixes") == ReviewFixesSecurity
}

// Where a reviewer works (input review_checkout): "same" is a new session in
// the reviewed job's own worktree, read-only (the default); "new" is its own
// disposable checkout at the reviewed head. A reviewer allowed to commit
// security fixes always gets its own branch checkout.
const (
	ReviewCheckoutSame = "same"
	ReviewCheckoutNew  = "new"
)

// reviewSharesCheckout reports whether a reviewer run reads the reviewed
// job's own worktree (admission validated the input).
func reviewSharesCheckout(input map[string]any) bool {
	if reviewFixesAllowed(input) {
		return false
	}
	value, _ := input["review_checkout"].(string)
	return value == "" || value == ReviewCheckoutSame
}

// sessionReviewLaneOwner is the session that owns the worktree a reviewer
// session reads in place ("" for a reviewer in its own checkout): pinned at
// admission from the captured review target.
func sessionReviewLaneOwner(meta map[string]any) string {
	if mapString(meta, "worker_role") != store.WorkerRoleReviewer {
		return ""
	}
	return mapString(meta, "worker_review_lane_owner")
}

// sessionLaneOwner is the session whose worktree a session works in: the
// reviewed job's session for a reviewer reading it in place, else itself.
func sessionLaneOwner(session store.SessionSnapshot) string {
	if owner := sessionReviewLaneOwner(session.Metadata); owner != "" {
		return owner
	}
	return session.ID
}

// reviewFixBranch is the writable branch of a reviewer allowed to commit
// security fixes: agent/review-<run>.
func reviewFixBranch(runID string) string {
	return "agent/review-" + runID
}

// workerExecutionAgentID is the code-owned system agent a worker run's
// session starts with, and whether it is the reviewer allowed to commit
// security fixes: only a reviewer whose admitted input carries review_fixes
// (validated at admission: automation source only) gets the fixer agent,
// which may edit and commit but never delegates.
func workerExecutionAgentID(role string, input map[string]any) (string, bool) {
	if role == store.WorkerRoleReviewer && reviewFixesAllowed(input) {
		return agentruntime.ReviewerFixerAgentID, true
	}
	return workerRoleAgentID(role), false
}

// allocateReviewerCheckout allocates a reviewer run's checkout from its
// captured review target: a writable agent/review-<run> branch at the
// reviewed head, a worktree of the folder, when fixes are allowed; otherwise
// the detached, disposable review checkout.
func allocateReviewerCheckout(trees *worktree.Service, p identity.Principal, source string, r store.WorkerRunRecord, fixes bool) (worktree.Allocation, error) {
	target, ok := r.Input["captured_review_target"].(map[string]any)
	if !ok {
		return worktree.Allocation{}, errors.New("missing captured review target")
	}
	head, _ := target["head"].(string)
	reviewBase, _ := target["base"].(string)
	commits := []string{reviewBase}
	switch values := target["commits"].(type) {
	case []string:
		commits = values
	case []any:
		commits = nil
		for _, value := range values {
			text, _ := value.(string)
			commits = append(commits, text)
		}
	}
	if fixes {
		return trees.AllocateReviewFixWorkspace(p, source, r.SessionID, head, reviewFixBranch(r.ID))
	}
	if lane, _ := target["lane"].(string); lane != "" {
		// Read in place: the reviewed job's own worktree, still owned by its
		// session; nothing is allocated, so nothing is removed afterwards.
		owner, _ := target["lane_owner"].(string)
		branch, _ := target["lane_branch"].(string)
		if owner == "" || branch == "" || owner == r.SessionID {
			return worktree.Allocation{}, errors.New("captured review worktree is incomplete")
		}
		if err := trees.ValidateSessionRepositoryLaneForRead(source, lane, owner, branch); err != nil {
			return worktree.Allocation{}, err
		}
		state, err := trees.InspectTaskWorkspace(lane)
		if err != nil {
			return worktree.Allocation{}, err
		}
		if state.HeadCommit != head || !state.TrackedClean {
			return worktree.Allocation{}, errors.New("the reviewed job's worktree moved past the head being reviewed or has uncommitted changes")
		}
		return worktree.Allocation{WorkspacePath: lane, BranchName: branch, BaseBranch: head, BaseCommit: head, RepoRoot: source}, nil
	}
	return trees.AllocateReviewWorkspace(p, source, r.SessionID, head, commits...)
}

// AllocateTaskReviewCheckout reuses the same captured-target allocation as worker reviews.
func (s *WorkerExecutionService) AllocateTaskReviewCheckout(p identity.Principal, task *store.ProjectTaskRecord) (worktree.Allocation, error) {
	if task == nil || task.ReviewTarget == nil {
		return worktree.Allocation{}, errors.New("review target required")
	}
	return allocateReviewerCheckout(s.host.trees, p, task.SourceWorkspace.Path, store.WorkerRunRecord{ID: task.ID, SessionID: task.SessionID, Input: map[string]any{"captured_review_target": task.ReviewTarget}}, task.ReviewFixes == ReviewFixesSecurity)
}

// PrepareTaskReviewSession pins the read-only review identity and registers its sandbox.
func (s *WorkerExecutionService) PrepareTaskReviewSession(snapshot *store.SessionSnapshot, task *store.ProjectTaskRecord) error {
	snapshot.Metadata["worker_role"] = store.WorkerRoleReviewer
	pinReviewerMetadata(snapshot.Metadata, map[string]any{"captured_review_target": task.ReviewTarget}, task.ReviewFixes == ReviewFixesSecurity)
	if owner := sessionReviewLaneOwner(snapshot.Metadata); owner != "" {
		snapshot.Metadata["swarm_v3_worktree_owner_session_id"] = owner
	}
	return s.registerReview(*snapshot)
}

// pinReviewerMetadata pins a reviewer session's review target and, only for
// the reviewer allowed to fix, worker_review_fixes (what the sandbox, the
// per-call identity check and cleanup read).
func pinReviewerMetadata(meta map[string]any, input map[string]any, fixes bool) {
	meta["worker_review_target"] = input["captured_review_target"]
	if fixes {
		meta["worker_review_fixes"] = ReviewFixesSecurity
	}
	if target, _ := input["captured_review_target"].(map[string]any); target != nil && !fixes {
		if owner, _ := target["lane_owner"].(string); owner != "" {
			meta["worker_review_lane_owner"] = owner
		}
	}
}

func validateReviewRequest(role, source string, input map[string]any) error {
	if raw, exists := input["review_fixes"]; exists {
		value, ok := raw.(string)
		if !ok || value != ReviewFixesSecurity {
			return errors.New("review_fixes must be \"security\"")
		}
		if role != store.WorkerRoleReviewer {
			return errors.New("review_fixes is only accepted for reviewer workers")
		}
		if source != reviewFixesRequestSource {
			return errors.New("review_fixes is only accepted from a job automation agent step")
		}
	}
	job, _ := input["review_job"].(string)
	if raw, exists := input["review_job"]; exists {
		if _, ok := raw.(string); !ok {
			return errors.New("review_job must be a job id string")
		}
	}
	if _, exists := input["review_ref"]; exists {
		return errors.New("review_ref is unsupported; use review_job")
	}
	if raw, exists := input["review_checkout"]; exists {
		value, ok := raw.(string)
		if !ok || (value != ReviewCheckoutSame && value != ReviewCheckoutNew) {
			return errors.New("review_checkout must be \"same\" (the reviewed job's own worktree, read-only) or \"new\" (the reviewer's own checkout)")
		}
		if role != store.WorkerRoleReviewer {
			return errors.New("review_checkout is only accepted for reviewer workers")
		}
		if value == ReviewCheckoutSame && reviewFixesAllowed(input) {
			return errors.New("a reviewer with review_fixes commits, so it works in its own checkout: review_checkout must be \"new\"")
		}
	}
	if role != store.WorkerRoleReviewer {
		if _, exists := input["review_job"]; exists {
			return errors.New("review_job is only accepted for reviewer workers")
		}
		return nil
	}
	parts := strings.Split(job, "/")
	if len(parts) != 3 || (parts[0] != "worker" && parts[0] != "task") || parts[1] == "" || parts[2] == "" {
		return errors.New("reviewer requires review_job: task/<project id>/<task id> (a task of Swarm's own agents) or worker/<worker id>/<run id>")
	}
	return nil
}

// resolveReviewTarget uses only account-authorized durable job/task identities;
// it never treats a caller-supplied path or head as source authority.
func (s *WorkerExecutionService) resolveReviewTarget(ctx context.Context, r store.WorkerRunRecord, w store.WorkerRecord) (map[string]any, error) {
	if err := validateReviewRequest(w.Role, r.RequestSource, r.Input); err != nil {
		return nil, err
	}
	project, err := s.resolveWorkerProject(r, w)
	if err != nil {
		return nil, err
	}
	return s.resolveJobReviewTarget(ctx, r, project.ID)
}

// ResolveTaskReviewTarget captures a task review without resolving or creating a reviewer worker.
func (s *WorkerExecutionService) ResolveTaskReviewTarget(ctx context.Context, p identity.Principal, task *store.ProjectTaskRecord, job, checkout, head string) (map[string]any, error) {
	if strings.TrimSpace(head) == "" {
		return nil, errors.New("no head to review")
	}
	input := map[string]any{"review_job": job, "review_checkout": checkout}
	if task.ReviewFixes != "" {
		input["review_fixes"] = task.ReviewFixes
	}
	if err := validateReviewRequest(store.WorkerRoleReviewer, "automation", input); err != nil {
		return nil, err
	}
	target, err := s.resolveJobReviewTarget(ctx, store.WorkerRunRecord{AccountScopeID: p.AccountScopeID, UserID: p.UserID, SessionID: task.SessionID, WorkspaceID: task.SourceWorkspace.WorkspaceID, Input: input}, task.ProjectID)
	if err != nil {
		return nil, err
	}
	if target["head"] != head {
		return nil, errors.New("review job moved past the delivery head")
	}
	return target, nil
}

func (s *WorkerExecutionService) resolveJobReviewTarget(ctx context.Context, r store.WorkerRunRecord, projectID string) (map[string]any, error) {
	parts := strings.Split(r.Input["review_job"].(string), "/")
	var task *store.ProjectTaskRecord
	laneOwner := ""
	if parts[0] == "task" {
		// A task of Swarm's own agents (swarm_run_task): it is in the
		// reviewer's project and folder, worked in its own session's worktree.
		if parts[1] != projectID {
			return nil, errors.New("review job must exist in the same project and workspace")
		}
		target, found, err := s.host.runs.sessions.Store().GetProjectTask(r.AccountScopeID, projectID, parts[2])
		if err != nil {
			return nil, err
		}
		if !found || target == nil || target.WorkerRunID != "" || target.SourceWorkspace.WorkspaceID != r.WorkspaceID || target.SessionID == "" {
			return nil, errors.New("review job must exist in the same project and workspace")
		}
		task, laneOwner = target, target.SessionID
	} else {
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
		jobTask, found, err := s.host.runs.sessions.Store().GetProjectTask(r.AccountScopeID, projectID, "task_"+target.ID)
		if err != nil {
			return nil, err
		}
		if !found || jobTask.WorkerID != target.WorkerID || jobTask.WorkerRunID != target.ID || jobTask.SourceWorkspace.WorkspaceID != r.WorkspaceID {
			return nil, errors.New("review job must exist in the same project and workspace")
		}
		task, laneOwner = jobTask, target.SessionID
	}
	folder := task.WorktreeBranch == "" && task.WorkspacePath == task.SourceWorkspace.Path && task.WorkspacePath != "" && task.WorkerRunID == ""
	if !folder {
		// The reviewed job's checkout is claimed under the evaluation slot
		// (recreated from its kept branch if it was removed), so no landed
		// removal or sweep removes it before this review is recorded.
		if _, err := s.ClaimSessionWorktree(laneOwner); err != nil {
			return nil, fmt.Errorf("the reviewed job's checkout cannot be reserved or recreated from its branch: %w", err)
		}
		if err := s.host.trees.ValidateSessionRepositoryLaneForRead(task.SourceWorkspace.Path, task.WorkspacePath, laneOwner, task.WorktreeBranch); err != nil {
			return nil, err
		}
	}
	state, err := s.host.trees.InspectTaskWorkspace(task.WorkspacePath)
	if err != nil {
		return nil, err
	}
	if folder {
		if task.Integration == nil || task.Integration.State != "in_folder" || task.Integration.SessionID != task.SessionID || task.Integration.SourceHead == "" {
			return nil, errors.New("folder task has no captured delivery head")
		}
		state.HeadCommit = task.Integration.SourceHead
	}
	if task.BaseCommit == "" || state.HeadCommit == task.BaseCommit {
		return nil, errors.New("review job must have commits")
	}
	// A job rebased onto (or merged with) a newer target after it was
	// created is reviewed from that newer base, so the range holds only its
	// own commits (worktree.EffectiveTaskBase); otherwise its recorded fork.
	jobBase := task.BaseCommit
	if !folder {
		jobBase = worktree.EffectiveTaskBase(ctx, task.WorkspacePath, task.SourceWorkspace.Path, task.BaseBranch, task.BaseCommit, state.HeadCommit)
	}
	if state.HeadCommit == jobBase {
		return nil, errors.New("review job must have commits")
	}
	// The job's commits are in its worktree of the folder, validated above.
	descends, err := s.host.trees.TaskCommitDescendsFrom(task.WorkspacePath, jobBase, state.HeadCommit)
	if err != nil {
		return nil, err
	}
	if !descends {
		return nil, errors.New("review job head does not descend from its base")
	}
	// The job's effective base is the normal review base. Pipeline rebase
	// reviews supply their exact comparison commits; these are evidence only,
	// never repository or publication authority, and allocation verifies them.
	base, commits, err := reviewComparisonCommits(jobBase, r.Input)
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
	// The folder's target branch as the review starts, so the reviewer can
	// tell what is already on it (its checkout is a worktree of the folder,
	// so the folder's branches are its own refs).
	if tip := worktree.TargetBranchTip(ctx, task.SourceWorkspace.Path, task.BaseBranch); tip != "" {
		captured["target"] = task.BaseBranch + " at " + tip
	}
	if reviewFixesAllowed(r.Input) {
		// The fix branch starts at the reviewed head, in a worktree of the
		// folder.
		captured["fixes"] = ReviewFixesSecurity
	}
	// Folder tasks always use a detached review checkout, never the person's folder.
	if reviewSharesCheckout(r.Input) && !folder {
		// The reviewer reads the job's own worktree as it is: only when that
		// is exactly the reviewed head, with nothing uncommitted.
		if !state.TrackedClean {
			return nil, errors.New("the job's worktree has uncommitted changes, so it is not the head being reviewed: commit or discard them, or review with checkout new")
		}
		if task.WorktreeBranch == "" || laneOwner == "" || laneOwner == r.SessionID {
			return nil, errors.New("the job's worktree has no owning session to review it in place; review with checkout new")
		}
		captured["lane"] = task.WorkspacePath
		captured["lane_owner"] = laneOwner
		captured["lane_branch"] = task.WorktreeBranch
	}
	return captured, nil
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

func reviewIdentityMatches(state sandbox.ReviewCleanupState, snapshot store.SessionSnapshot) bool {
	return state.Checkout == snapshot.WorktreeRootPath && state.Session == snapshot.ID && state.Borrowed == (sessionReviewLaneOwner(snapshot.Metadata) != "")
}

func (s *WorkerExecutionService) registerReview(snapshot store.SessionSnapshot) error {
	if snapshot.Metadata["worker_role"] != store.WorkerRoleReviewer {
		return nil
	}
	// A reviewer allowed to commit security fixes runs in its own writable
	// branch checkout like a coder job: it is never registered as a read-only
	// review checkout (read-only .git mounts, detached identity).
	if sessionReviewFixes(snapshot.Metadata) {
		return nil
	}
	// The owner turned the sandbox off: every agent runs on the machine, and
	// so does the reviewer (read-only by its tools and the review checks).
	if sandbox.OwnerTurnedOff() {
		return nil
	}
	m := sandbox.Default()
	if m == nil || !m.Active() {
		return sandbox.ErrSandboxRequired
	}
	db := s.host.runs.sessions.Store()
	state, found, err := db.GetReviewCleanup(snapshot.AccountScopeID, snapshot.ID)
	if err != nil {
		return err
	}
	if found {
		if !reviewIdentityMatches(state, snapshot) {
			return fmt.Errorf("review identity mismatch")
		}
		if state.Phase == "volume_removed" {
			return nil
		}
		if err := m.RestoreReviewIdentity(state); err != nil {
			return err
		}
		return m.SetReviewEnvironment(state.Checkout, reviewEnvironment(snapshot.Metadata))
	}
	var registerErr error
	if sessionReviewLaneOwner(snapshot.Metadata) != "" {
		// The reviewed job's own worktree, read in place by this session only.
		registerErr = m.RegisterBorrowedReviewCheckout(snapshot.WorktreeRootPath, snapshot.ID)
	} else {
		registerErr = m.RegisterReviewCheckout(snapshot.WorktreeRootPath)
	}
	if registerErr != nil {
		return registerErr
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
	// A reviewer allowed to commit security fixes keeps its branch checkout
	// like a coder job's worktree: the automation delivery reads its head from
	// it after the job ends.
	if reviewFixesAllowed(r.Input) {
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
	source := snapshot.WorkspacePath
	if mapString(snapshot.Metadata, "worker_execution_run_id") == "" {
		source = mapString(snapshot.Metadata, "swarm_v3_source_workspace_path")
	}
	validate := func() error {
		return s.host.trees.ValidateReviewWorkspaceRemoval(source, snapshot.WorktreeRootPath, r.SessionID, head)
	}
	remove := func() error {
		return s.host.trees.RemoveValidatedReviewWorkspace(source, snapshot.WorktreeRootPath)
	}
	if sessionReviewLaneOwner(snapshot.Metadata) != "" {
		// A reviewer that read the job's own worktree in place leaves it as
		// it was: it stays the job's session's worktree. Only its read-only
		// sandbox is closed.
		validate = func() error { return nil }
		remove = func() error { return nil }
	}
	if sandbox.OwnerTurnedOff() {
		// No review sandbox was registered (registerReview): remove the
		// disposable checkout directly; a borrowed one is left as it is.
		if err := validate(); err != nil {
			return err
		}
		return remove()
	}
	if m := sandbox.Default(); m != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		// Cleanup restores the captured identity itself. Do not re-admit an
		// active review or rebuild its execution environment during recovery.
		db := s.host.runs.sessions.Store()
		state, found, err := db.GetReviewCleanup(snapshot.AccountScopeID, snapshot.ID)
		if err != nil {
			return err
		}
		if !found {
			// Admission may have allocated the checkout before persisting its
			// identity. Recover through the same registration path as admission.
			if err := s.registerReview(snapshot); err != nil {
				return err
			}
			state, found, err = db.GetReviewCleanup(snapshot.AccountScopeID, snapshot.ID)
			if err != nil {
				return err
			}
			if !found {
				return fmt.Errorf("missing durable review identity")
			}
		}
		if !reviewIdentityMatches(state, snapshot) {
			return fmt.Errorf("review identity mismatch")
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

// TaskReviewModel identifies the resolved session model, not the task's optional
// submission override. Incomplete preferences must not produce a slash sentinel.
func TaskReviewModel(snapshot store.SessionSnapshot) string {
	provider, model := strings.TrimSpace(snapshot.Preference.Provider), strings.TrimSpace(snapshot.Preference.Model)
	if provider == "" || model == "" {
		return ""
	}
	return provider + "/" + model
}

// FinishTaskReview checks the borrowed head before persisting the verdict, then
// uses the existing review cleanup protocol without any reviewer worker record.
func (s *WorkerExecutionService) FinishTaskReview(task *store.ProjectTaskRecord, review store.JobReview) (store.JobReview, error) {
	snapshot, found, err := s.host.runs.sessions.GetSession(task.SessionID)
	if err != nil {
		return review, err
	}
	if !found {
		return review, errors.New("review session missing")
	}
	review.Model = TaskReviewModel(snapshot)
	if task.ReviewFixes == ReviewFixesSecurity {
		review = withReviewFixHead(review, snapshot, task.ID)
	}
	if sessionReviewLaneOwner(snapshot.Metadata) != "" {
		review = withBorrowedReviewHead(review, snapshot, s.host.trees)
	}
	updated, err := s.host.runs.sessions.Store().UpdateProjectTask(task.AccountID, task.ProjectID, task.ID, func(current *store.ProjectTaskRecord) error {
		if current.SessionID != task.SessionID || current.StartCommit != review.Head {
			return errors.New("review task changed")
		}
		if current.Review == nil {
			current.Review = &review
		}
		return nil
	})
	if err != nil {
		return review, err
	}
	review = *updated.Review
	if err := s.CleanupTaskReview(task); err != nil {
		return review, err
	}
	return review, nil
}

// ErrTaskReviewCleanupPending indicates a scheduled cleanup retry, not a
// persistence or cleanup failure. Callers may wait only for this condition.
var ErrTaskReviewCleanupPending = errors.New("task review cleanup pending")

// ErrTaskReviewCleanup distinguishes cleanup from verdict persistence failures.
var ErrTaskReviewCleanup = errors.New("task review cleanup failed")

func permanentTaskReviewCleanup(message string) bool {
	for _, reason := range []string{"missing captured head", "identity mismatch", "missing durable review identity", sandbox.ErrSandboxRequired.Error()} {
		if strings.Contains(message, reason) {
			return true
		}
	}
	return false
}

func (s *WorkerExecutionService) CleanupTaskReview(task *store.ProjectTaskRecord) (resultErr error) {
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(ErrTaskReviewCleanup, resultErr)
		}
	}()
	// Settlement and workflow recovery can arrive together. Neither may report
	// completion while the other's shared sandbox cleanup is still running.
	s.taskReviewCleanupMu.Lock()
	defer s.taskReviewCleanupMu.Unlock()
	now := time.Now()
	attempt := false
	_, err := s.host.runs.sessions.Store().UpdateProjectTask(task.AccountID, task.ProjectID, task.ID, func(current *store.ProjectTaskRecord) error {
		if current.ReviewCleanup != nil {
			if current.ReviewCleanup.State == "done" {
				return nil
			}
			if c := current.ReviewCleanup; c.State == "failed" && (c.Attempts >= 5 || c.NextAttemptAt == 0 || permanentTaskReviewCleanup(c.LastError)) {
				return errors.New(c.LastError)
			}
			if current.ReviewCleanup.NextAttemptAt > now.UnixMilli() {
				return fmt.Errorf("%w: %s", ErrTaskReviewCleanupPending, current.ReviewCleanup.LastError)
			}
		}
		if current.ReviewCleanup == nil {
			current.ReviewCleanup = &store.WorkerRunCleanup{}
		}
		c := current.ReviewCleanup
		c.State = "pending"
		c.Attempts++
		c.NextAttemptAt = now.Add(time.Minute << min(c.Attempts-1, 6)).UnixMilli()
		attempt = true
		return nil
	})
	if err != nil || !attempt {
		return err
	}
	cleanupErr := s.cleanupReview(store.WorkerRunRecord{AccountScopeID: task.AccountID, SessionID: task.SessionID, Role: store.WorkerRoleReviewer, Input: map[string]any{"review_fixes": task.ReviewFixes}})
	_, err = s.host.runs.sessions.Store().UpdateProjectTask(task.AccountID, task.ProjectID, task.ID, func(current *store.ProjectTaskRecord) error {
		c := current.ReviewCleanup
		if cleanupErr != nil {
			c.State, c.LastError = "failed", cleanupErr.Error()
			c.FailureReported = c.Attempts >= 5
			if permanentTaskReviewCleanup(c.LastError) {
				c.NextAttemptAt = 0
			} else if c.Attempts < 5 {
				cleanupErr = fmt.Errorf("%w: %v", ErrTaskReviewCleanupPending, cleanupErr)
			}
		} else {
			c.State, c.LastError, c.NextAttemptAt = "done", "", 0
		}
		return nil
	})
	if err != nil {
		return err
	}
	return cleanupErr
}

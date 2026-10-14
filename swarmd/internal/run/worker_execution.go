package run

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/identity"
	sessions "swarm/packages/swarmd/internal/session"
	store "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/worktree"
)

// WorkerExecutionService is the shared worker admission and dispatch boundary.
// It uses the existing V3 session/plan executor, not a second execution engine.
type WorkerExecutionService struct {
	host          *AutomationV2ExecutionHost
	dispatchMu    sync.Mutex
	scheduleMu    sync.Mutex
	presetMu      sync.Mutex
	lastScheduled time.Time
}

func NewWorkerExecutionService(runs *Service, repository *store.SessionStore, trees *worktree.Service, apply func(sessions.SessionMutationInput) (sessions.SessionMutationResult, error), enqueue func(identity.Principal, store.V3SessionRunIntent) bool) (*WorkerExecutionService, error) {
	h, err := NewAutomationV2ExecutionHost(runs, repository, trees, apply, enqueue)
	if err != nil {
		return nil, err
	}
	return &WorkerExecutionService{host: h}, nil
}

// MarkWorkerSweep completes one bounded local sweep; it advances the cursor
// only after the daemon has visited its scheduled workers.
func (s *WorkerExecutionService) MarkWorkerSweep(now time.Time) {
	s.scheduleMu.Lock()
	if now.After(s.lastScheduled) {
		s.lastScheduled = now
	}
	s.scheduleMu.Unlock()
}

// WorkerStoreForScheduling is read-only at the runtime wiring boundary; all
// state transitions remain on WorkerExecutionService.
func (s *WorkerExecutionService) WorkerStoreForScheduling() (*store.WorkerStore, error) {
	return s.workerStore()
}

func (s *WorkerExecutionService) workerStore() (*store.WorkerStore, error) {
	if s == nil || s.host == nil || s.host.runs == nil || s.host.runs.sessions == nil || s.host.runs.sessions.Store() == nil {
		return nil, errors.New("worker execution authorities unavailable")
	}
	return s.host.runs.sessions.Store().WorkerStore(), nil
}

// Activate validates every declared role against principal-authorized workspace
// IDs. Authenticated user ingress owns approval; AI tools cannot self-approve.
// An unbound worker cannot execute or implicitly cut over a legacy schedule.
func (s *WorkerExecutionService) Activate(account, user, id string, revision uint64, bindings map[string]string) (store.WorkerRecord, error) {
	return s.ConfigureBindings(account, user, id, revision, bindings, true)
}

func (s *WorkerExecutionService) ConfigureBindings(account, user, id string, revision uint64, bindings map[string]string, activate bool) (store.WorkerRecord, error) {
	if err := s.authorizeWorkerOwner(account, user); err != nil {
		return store.WorkerRecord{}, err
	}
	ws, err := s.workerStore()
	if err != nil {
		return store.WorkerRecord{}, err
	}
	w, found, err := ws.GetWorker(account, id)
	if err != nil {
		return store.WorkerRecord{}, err
	}
	if !found {
		return store.WorkerRecord{}, store.ErrWorkerNotFound
	}
	if w.Revision != revision || (w.LifecycleState != store.WorkerLifecycleStateIdle && w.LifecycleState != store.WorkerLifecycleStatePaused) {
		return store.WorkerRecord{}, store.ErrWorkerConflict
	}
	if len(w.RequestedCapabilities) != 0 {
		return store.WorkerRecord{}, errors.New("worker capability grants require explicit approval; unsupported activation")
	}
	// A box-level worker has no workspace roles and takes no bindings: each of
	// its jobs names the workspace it works in.
	primary, err := store.WorkerPrimaryBinding(w.WorkspaceRequirements, bindings)
	if err != nil {
		return store.WorkerRecord{}, err
	}
	if primary == "" {
		return ws.ConfigureWorkerBindings(account, user, id, revision, nil, activate)
	}
	entry, err := s.activeWorkspace(account, user, primary)
	if err != nil {
		return store.WorkerRecord{}, err
	}
	if activate {
		// Every worker run executes as a project task. Refuse activation now
		// rather than returning an active worker whose every run fails.
		candidate := w
		candidate.LocalBindings = map[string]string{"primary": strings.TrimSpace(bindings["primary"])}
		if _, err := s.resolveWorkerProject(store.WorkerRunRecord{AccountScopeID: account, UserID: user, WorkerID: w.ID}, candidate); errors.Is(err, errWorkerProjectNotAssociated) {
			return store.WorkerRecord{}, workerProjectRequiredError(w.ID, entry.WorkspaceID, entry.Path)
		}
	}
	return ws.ConfigureWorkerBindings(account, user, id, revision, bindings, activate)
}

// activeWorkspace resolves a workspace ID to a registered, active catalog entry
// visible to this account owner.
func (s *WorkerExecutionService) activeWorkspace(account, user, workspaceID string) (store.WorkspaceEntry, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		return store.WorkspaceEntry{}, store.ErrWorkerWorkspaceRequired
	}
	if s.host.runs.workspace == nil {
		return store.WorkspaceEntry{}, errors.New("workspace authority unavailable")
	}
	p := identity.Principal{Type: identity.PrincipalTypeUser, UserID: user, AccountScopeID: account, AccountScopeSource: identity.AccountScopeSourceServerState}
	entry, found, err := s.host.runs.workspace.GetByWorkspaceIDForPrincipal(p, workspaceID)
	if err != nil {
		return store.WorkspaceEntry{}, err
	}
	if !found || !strings.EqualFold(entry.State, "active") {
		return store.WorkspaceEntry{}, fmt.Errorf("%w: workspace %q is not a registered active workspace", store.ErrWorkerConflict, workspaceID)
	}
	return entry, nil
}

// workerRunRole is the role pinned on the run, or the pinned revision's role
// for receipts admitted before roles existed.
func workerRunRole(r store.WorkerRunRecord, pinned store.WorkerRecord) string {
	if role := strings.TrimSpace(r.Role); role != "" {
		return role
	}
	return strings.TrimSpace(pinned.Role)
}

// workerRunPlanID is the plan a run's intents belong to: the run's own plan
// for plan-executed roles, none for roles that run a direct auto session.
func workerRunPlanID(r store.WorkerRunRecord) string {
	if store.WorkerRoleRunsPlan(r.Role) {
		return r.ID
	}
	return ""
}

// workerRoleAgentID maps a worker role to its code-owned system agent.
func workerRoleAgentID(role string) string {
	switch strings.TrimSpace(role) {
	case store.WorkerRoleCoder:
		// No parent agent builds or tests for a worker job: its Coder runs
		// its own commands in the project sandbox.
		return agentruntime.WorkerCoderAgentID
	case store.WorkerRoleDesigner:
		return agentruntime.DesignerAgentID
	case store.WorkerRoleFinder:
		return agentruntime.FinderAgentID
	case store.WorkerRoleReviewer:
		return agentruntime.ReviewerAgentID
	default:
		return agentruntime.SwarmAgentID
	}
}

// workerProjectRequiredError names the fix for a worker whose primary
// workspace belongs to no project.
func workerProjectRequiredError(workerID, workspaceID, workspacePath string) error {
	return fmt.Errorf("%w: worker %q is bound to workspace %q (%s), which belongs to no project; create a project that includes this workspace (POST /v3/projects) or set the worker's metadata.project_id, then retry", errWorkerProjectNotAssociated, workerID, workspaceID, workspacePath)
}

// Accept validates exact revision, proposed bindings and workspace authorization,
// transitioning a pending worker into an active worker without dispatching unsolicited work.
func (s *WorkerExecutionService) Accept(account, user, id string, revision uint64, models ...*store.SessionModelProfileSnapshot) (store.WorkerRecord, error) {
	if err := s.authorizeWorkerOwner(account, user); err != nil {
		return store.WorkerRecord{}, err
	}
	ws, err := s.workerStore()
	if err != nil {
		return store.WorkerRecord{}, err
	}
	w, found, err := ws.GetWorker(account, id)
	if err != nil {
		return store.WorkerRecord{}, err
	}
	if !found || w.AccountScopeID != account {
		return store.WorkerRecord{}, store.ErrWorkerNotFound
	}
	if revision == 0 || w.Revision != revision {
		return store.WorkerRecord{}, store.ErrWorkerConflict
	}
	if w.PendingReview != nil {
		candidate := *w.PendingReview
		candidate.Revision = w.Revision
		w = candidate
	}
	if w.LifecycleState != store.WorkerLifecycleStatePending {
		return store.WorkerRecord{}, fmt.Errorf("%w: worker is not pending review", store.ErrWorkerConflict)
	}
	// A box-level proposal has no workspace roles; otherwise its one proposed
	// primary workspace must be registered and active for this principal.
	primaryWS, err := store.WorkerPrimaryBinding(w.WorkspaceRequirements, w.ProposedBindings)
	if err != nil {
		return store.WorkerRecord{}, fmt.Errorf("%w: %v", store.ErrWorkerConflict, err)
	}
	bindings := map[string]string{}
	if primaryWS != "" {
		if _, err := s.activeWorkspace(account, user, primaryWS); err != nil {
			return store.WorkerRecord{}, err
		}
		bindings["primary"] = primaryWS
	}
	model := w.ModelProfile
	if len(models) > 1 {
		return store.WorkerRecord{}, store.ErrWorkerConflict
	}
	if len(models) == 1 {
		model = models[0]
	}
	// Revalidate the saved candidate too: catalog support may have changed since
	// proposal. Resolve inherited slots without replacing explicit slot policies.
	model, err = s.ResolveRoleModelProfile(account, w.Role, model)
	if err != nil {
		return store.WorkerRecord{}, err
	}
	return ws.AcceptWorker(account, user, id, revision, bindings, model)
}

// Dispatch admits once. The pinned receipt survives wake failure and can be
// retried with the same key. Dispatch of a newly created receipt is not allowed
// to bypass workspace, capability or legacy-cutover checks.
func (s *WorkerExecutionService) Dispatch(ctx context.Context, account, user string, req store.WorkerRunAdmission) (store.WorkerRunRecord, error) {
	if req.Placement != nil {
		return store.WorkerRunRecord{}, store.ErrWorkerRemoteUnavailable
	}
	ws, err := s.workerStore()
	if err != nil {
		return store.WorkerRunRecord{}, err
	}
	if ctx.Err() != nil {
		return store.WorkerRunRecord{}, ctx.Err()
	}
	if err := s.authorizeWorkerOwner(account, user); err != nil {
		return store.WorkerRunRecord{}, err
	}
	w, found, err := ws.GetWorker(account, req.WorkerID)
	if err != nil {
		return store.WorkerRunRecord{}, err
	}
	if !found || w.ID != req.WorkerID {
		return store.WorkerRunRecord{}, store.ErrWorkerNotFound
	}
	if w.LifecycleState == store.WorkerLifecycleStatePending {
		return store.WorkerRunRecord{}, fmt.Errorf("%w: pending worker cannot be dispatched before human acceptance", store.ErrWorkerConflict)
	}
	if err := validateReviewRequest(w.Role, req.Input); err != nil {
		return store.WorkerRunRecord{}, err
	}
	if w.Provenance != nil && (w.Provenance.MigratedAt != 0 || w.Provenance.SourceProposalID != "") {
		return store.WorkerRunRecord{}, store.ErrWorkerConflict
	}
	if len(w.RequestedCapabilities) != 0 {
		return store.WorkerRunRecord{}, errors.New("worker capability grant is not approved")
	}
	// Per-job workspace: the request's workspace, else the automation's, else
	// the worker's approved primary binding. Admission re-applies the same rule
	// under the worker lock and pins the result on the receipt.
	req.WorkspaceID = strings.TrimSpace(req.WorkspaceID)
	workspaceID, workspaceSource, err := store.ResolveWorkerRunWorkspace(w, req.AutomationID, req.WorkspaceID)
	if err != nil {
		return store.WorkerRunRecord{}, err
	}
	entry, err := s.activeWorkspace(account, user, workspaceID)
	if err != nil {
		return store.WorkerRunRecord{}, err
	}
	req.ResolvedWorkspaceID = workspaceID
	w, err = s.initializeWorkerModel(w, user)
	if err != nil {
		return store.WorkerRunRecord{}, err
	}
	selected := w.ModelProfile
	req.ModelSource = "pinned"
	if selected == nil || selected.UseAccountDefault || selected.ActionUseAccountDefault {
		req.ModelSource = "role default"
	}
	if req.ModelOverride != nil {
		selected = req.ModelOverride
		req.ModelSource = "job override"
	}
	req.ResolvedModelProfile, err = s.ResolveRoleModelProfile(account, w.Role, selected)
	if err != nil {
		return store.WorkerRunRecord{}, err
	}
	// Admission pins resolved values, never a mutable default policy.
	req.ResolvedModelProfile.UseAccountDefault = false
	req.ResolvedModelProfile.ActionUseAccountDefault = false
	req.ResolvedModelProfile.PlanUseAccountDefault = false
	req.ExpectedWorkerRevision = w.Revision
	req.UserID = user
	// A new admission needs a resolvable project, or its receipt would only
	// fail during preparation and every same-key retry would replay that
	// failure. Existing receipts still replay unchanged.
	replay, err := ws.WorkerRunAdmissionExists(account, req)
	if err != nil {
		return store.WorkerRunRecord{}, err
	}
	if !replay {
		candidate := store.WorkerRunRecord{AccountScopeID: account, UserID: user, WorkerID: w.ID, AutomationID: req.AutomationID, Input: req.Input, WorkspaceID: workspaceID, WorkspaceSource: workspaceSource}
		if w.Role == store.WorkerRoleReviewer {
			target, err := s.resolveReviewTarget(ctx, candidate, w)
			if err != nil {
				return store.WorkerRunRecord{}, err
			}
			req.CapturedReviewTarget = target
		}
		if _, err := s.resolveWorkerProject(candidate, w); errors.Is(err, errWorkerProjectNotAssociated) {
			return store.WorkerRunRecord{}, workerProjectRequiredError(w.ID, entry.WorkspaceID, entry.Path)
		} else if err != nil && workspaceSource == store.WorkerRunWorkspaceSourceJob {
			// A named job workspace must resolve to exactly one project now,
			// not fail on every same-key retry after admission.
			return store.WorkerRunRecord{}, err
		}
	}
	rec, err := ws.AdmitWorkerRun(account, req)
	if err != nil {
		return store.WorkerRunRecord{}, err
	}
	if rec.Status == "admitted" {
		if err := s.Start(ctx, rec); err != nil {
			return rec, err
		}
	}
	latest, found, err := ws.GetWorkerRun(account, rec.WorkerID, rec.ID)
	if err != nil {
		return rec, err
	}
	if found {
		return latest, nil
	}
	return rec, nil
}

// Start recovers precisely the admitted revision, never the mutable current plan.
func (s *WorkerExecutionService) Start(ctx context.Context, receipt store.WorkerRunRecord) error {
	s.dispatchMu.Lock()
	defer s.dispatchMu.Unlock()
	return s.startLocked(ctx, receipt)
}

func (s *WorkerExecutionService) startLocked(ctx context.Context, receipt store.WorkerRunRecord) error {
	if err := s.authorizeWorkerOwner(receipt.AccountScopeID, receipt.UserID); err != nil {
		return err
	}
	ws, err := s.workerStore()
	if err != nil {
		return err
	}
	actual, found, err := ws.GetWorkerRun(receipt.AccountScopeID, receipt.WorkerID, receipt.ID)
	if err != nil {
		return err
	}
	if !found || actual.SessionID != receipt.SessionID || actual.UserID != receipt.UserID {
		return store.ErrWorkerConflict
	}
	if actual.Placement != nil {
		return store.ErrWorkerRemoteUnavailable
	}
	if receipt.WorkerRevision != actual.WorkerRevision || receipt.AutomationRevision != actual.AutomationRevision {
		return store.ErrWorkerConflict
	}
	if err := s.host.runs.sessions.Store().CheckWorkerBudgetAdmission(actual.AccountScopeID, actual.WorkerID); err != nil {
		return err
	}
	if actual.CancelRequested {
		return store.ErrWorkerConflict
	}
	if actual.Status == "running" {
		return nil
	}
	if actual.Status != "admitted" {
		return store.ErrWorkerConflict
	}
	current, found, err := ws.GetWorker(actual.AccountScopeID, actual.WorkerID)
	if err != nil {
		return err
	}
	if !found || !workerRunAdmissionOpen(current, actual) {
		return store.ErrWorkerConflict
	}
	if current.Provenance != nil && (current.Provenance.MigratedAt != 0 || current.Provenance.SourceProposalID != "") {
		return store.ErrWorkerConflict
	}
	if len(current.RequestedCapabilities) != 0 {
		return store.ErrWorkerConflict
	}
	if _, err := store.WorkerPrimaryBinding(current.WorkspaceRequirements, current.LocalBindings); err != nil {
		return fmt.Errorf("%w: %v", store.ErrWorkerConflict, err)
	}
	if actual.AutomationID != "" {
		enabled := false
		for _, a := range current.Automations {
			if a.ID == actual.AutomationID && (a.Enabled || (actual.RequestSource == "test_run" && a.Revision == actual.AutomationRevision)) {
				enabled = true
				break
			}
		}
		if !enabled {
			return store.ErrWorkerConflict
		}
	}
	history, found, err := ws.GetWorkerRevision(actual.AccountScopeID, actual.WorkerID, actual.WorkerRevision)
	if err != nil {
		return err
	}
	if !found {
		return store.ErrWorkerConflict
	}
	w := history.Worker
	if store.WorkerRunWorkspace(actual, w) == "" {
		return fmt.Errorf("%w: %w", store.ErrWorkerConflict, store.ErrWorkerWorkspaceRequired)
	}
	if actual.ModelProfile != nil {
		w.ModelProfile = store.CloneSessionModelProfileSnapshot(actual.ModelProfile)
	}
	var doc store.SessionPlanDocument
	if actual.AutomationID != "" {
		found = false
		for _, a := range w.Automations {
			if a.ID == actual.AutomationID && a.Revision == actual.AutomationRevision {
				doc = a.PlanDocument
				found = true
				break
			}
		}
		if !found {
			return store.ErrWorkerConflict
		}
	} else {
		prompt, ok := actual.Input["prompt"].(string)
		if !ok || strings.TrimSpace(prompt) == "" {
			return errors.New("direct worker request requires prompt")
		}
		doc = store.SessionPlanDocument{Title: w.Name, Info: store.SessionPlanInfo{Goal: prompt, Context: w.Instructions}, Checkpoints: []store.SessionPlanCheckpoint{{ID: "cp-1", Order: 1, Title: "Worker request", Objective: prompt, Tasks: []string{prompt}, AcceptanceCriteria: []string{"Complete the requested task"}, Status: "pending"}}}
	}
	if len(doc.Checkpoints) == 0 {
		return errors.New("worker execution requires a checkpoint")
	}
	return s.startPlan(ctx, actual, w, doc)
}

func (s *WorkerExecutionService) startPlan(ctx context.Context, r store.WorkerRunRecord, w store.WorkerRecord, doc store.SessionPlanDocument) error {
	h := s.host
	if err := ctx.Err(); err != nil {
		return err
	}
	if w.ModelProfile != nil && (w.ModelProfile.UseAccountDefault || w.ModelProfile.ActionUseAccountDefault || w.ModelProfile.PlanUseAccountDefault) {
		return errors.New("admitted worker run has unresolved model policy")
	}
	if err := store.ValidateWorkerModelProfile(w.ModelProfile); err != nil {
		return fmt.Errorf("worker revision has no pinned model: %w", err)
	}
	proj, err := s.resolveWorkerProject(r, w)
	if err != nil {
		return err
	}
	// The role picks the code-owned system agent. Swarm executes the accepted
	// plan (plan_manage owns checkpoint outcomes); Coder, Designer and Finder
	// have no plan_manage and run their compiled profile in an auto session,
	// the way project tasks assigned to them run.
	role := firstNonEmptyString(workerRunRole(r, w), store.WorkerRoleSwarm)
	planned := store.WorkerRoleRunsPlan(role)
	workspaceID := store.WorkerRunWorkspace(r, w)
	taskID := "task_" + r.ID
	taskRecord, taskFound, err := h.runs.sessions.Store().GetProjectTask(r.AccountScopeID, proj.ID, taskID)
	if err != nil {
		return err
	}
	taskTitle := w.Name
	taskDesc := firstNonEmptyString(doc.Info.Goal, w.Description, taskTitle)
	if taskFound {
		if taskRecord.AccountID != r.AccountScopeID ||
			taskRecord.ProjectID != proj.ID ||
			taskRecord.WorkerID != w.ID ||
			taskRecord.WorkerRunID != r.ID ||
			taskRecord.AutomationID != r.AutomationID ||
			(taskRecord.SessionID != "" && r.SessionID != "" && taskRecord.SessionID != r.SessionID) {
			return fmt.Errorf("%w: existing task %q identity mismatch", store.ErrWorkerConflict, taskID)
		}
	}
	if !taskFound {
		taskRecord = &store.ProjectTaskRecord{
			ID:           taskID,
			ProjectID:    proj.ID,
			AccountID:    r.AccountScopeID,
			Title:        taskTitle,
			Description:  taskDesc,
			Status:       "queued",
			SessionID:    "", // Do not set SessionID before session exists in pebble
			Agent:        role,
			RouterAlert:  w.ModelProfile.ResolutionWarning,
			WorkerID:     w.ID,
			WorkerName:   w.Name,
			WorkerRunID:  r.ID,
			AutomationID: r.AutomationID,
			Tier:         "complex",
			// Empty: the task store gives each role its own outcome (coder
			// code_pr, finder audit_report, designer artifact, swarm general).
			OutcomeType: "",
			CreatedAt:   r.CreatedAt,
			UpdatedAt:   r.CreatedAt,
		}
		if planned {
			taskRecord.PlanBinding = &store.ProjectTaskPlanBinding{PlanID: r.ID, SessionID: r.SessionID}
		}
		if err := h.runs.sessions.Store().PutProjectTask(r.AccountScopeID, taskRecord); err != nil {
			return err
		}
	} else {
		needsUpdate := taskRecord.WorkerID != w.ID ||
			taskRecord.WorkerName != w.Name ||
			taskRecord.WorkerRunID != r.ID ||
			taskRecord.AutomationID != r.AutomationID ||
			(planned && taskRecord.PlanBinding == nil)
		if needsUpdate {
			taskRecord, err = h.runs.sessions.Store().UpdateProjectTask(r.AccountScopeID, proj.ID, taskID, func(t *store.ProjectTaskRecord) error {
				if t.AccountID != r.AccountScopeID ||
					t.ProjectID != proj.ID ||
					t.WorkerID != w.ID ||
					t.WorkerRunID != r.ID ||
					t.AutomationID != r.AutomationID ||
					(t.SessionID != "" && r.SessionID != "" && t.SessionID != r.SessionID) {
					return fmt.Errorf("%w: existing task %q identity mismatch in update", store.ErrWorkerConflict, taskID)
				}
				t.WorkerID = w.ID
				t.WorkerName = w.Name
				t.WorkerRunID = r.ID
				t.AutomationID = r.AutomationID
				if planned && t.PlanBinding == nil {
					t.PlanBinding = &store.ProjectTaskPlanBinding{PlanID: r.ID, SessionID: r.SessionID}
				}
				return nil
			})
			if err != nil {
				return err
			}
		}
	}
	_, err = h.runs.sessions.Store().UpdateProject(r.AccountScopeID, proj.ID, func(p *store.ProjectRecord) error {
		for _, tid := range p.ActiveTaskIDs {
			if tid == taskID {
				return nil
			}
		}
		p.ActiveTaskIDs = append(p.ActiveTaskIDs, taskID)
		return nil
	})
	if err != nil {
		return err
	}
	snapshot, found, err := h.runs.sessions.GetSession(r.SessionID)
	if err != nil {
		return err
	}
	if !found {
		if h.runs.workspace == nil || h.runs.agents == nil || h.runs.agentModelSettings == nil || h.runs.sessionDeployCanonicalize == nil {
			return errors.New("worker execution preparation authorities unavailable")
		}
		p := identity.Principal{Type: identity.PrincipalTypeUser, UserID: r.UserID, AccountScopeID: r.AccountScopeID, AccountScopeSource: identity.AccountScopeSourceServerState}
		entry, found, err := h.runs.workspace.GetByWorkspaceIDForPrincipal(p, workspaceID)
		if err != nil {
			return err
		}
		if !found || !strings.EqualFold(entry.State, "active") {
			return store.ErrWorkerConflict
		}
		selection := w.ModelProfile.Action
		if w.ExecutionMode == "plan" {
			selection = *w.ModelProfile.Plan
		}
		profile, err := h.runs.agents.ResolveSystemAgent(workerRoleAgentID(role), store.AgentProfile{Provider: selection.Provider, Model: selection.Model, Thinking: selection.Thinking, AutoServiceTier: selection.ServiceTier, ContextMode: selection.ContextMode})
		if err != nil {
			return err
		}
		if _, _, err = h.runs.CompileStoredV3AgentToolContract(r.AccountScopeID, profile); err != nil {
			return err
		}
		model := store.CloneSessionModelProfileSnapshot(w.ModelProfile)
		if err := store.ValidateWorkerModelProfile(model); err != nil {
			return fmt.Errorf("worker revision has no pinned model: %w", err)
		}
		runtimeMode, sessionMode := store.AgentRuntimeModePlanAuto, sessions.ModePlan
		if !planned {
			runtimeMode, sessionMode = store.AgentProfileRuntimeMode(profile), sessions.ModeAuto
		}
		canonical, err := h.runs.sessionDeployCanonicalize(SessionDeployCanonicalizeInput{Principal: p, WorkspacePath: entry.Path, AgentProfile: profile, ModelProfile: model, RuntimeMode: runtimeMode, Metadata: map[string]any{}})
		if err != nil {
			return err
		}
		if canonical.SourceWorkspaceID != entry.WorkspaceID || canonical.Metadata == nil {
			return store.ErrWorkerConflict
		}
		// Fork from the source workspace's current branch (as project tasks do) so
		// the run's task records a real integration target, not a literal HEAD.
		// A detached HEAD has no branch to integrate into: fork from the HEAD
		// commit as before.
		base := ""
		current, branchErr := worktree.CurrentBranch(canonical.SourceWorkspacePath)
		if role == store.WorkerRoleReviewer {
			current = ""
		}
		if branchErr == nil && current == "" {
			base = "HEAD"
		}
		// A folder that publishes as pull requests and keeps up with its forge
		// starts from the forge's latest base, so yesterday's merged pull
		// request is today's starting point and the next one can merge.
		forgeBranch := ""
		if h.runs.publishBase != nil && role != store.WorkerRoleReviewer {
			commit, branch, ok, err := h.runs.publishBase(ctx, canonical.SourceWorkspacePath, current)
			if err != nil {
				return fmt.Errorf("the folder's publish rule could not bring its forge's base branch: %w", err)
			}
			if ok {
				base, forgeBranch = commit, branch
			}
		}
		var allocation worktree.Allocation
		if role == store.WorkerRoleReviewer {
			target, ok := r.Input["captured_review_target"].(map[string]any)
			if !ok {
				return errors.New("missing captured review target")
			}
			head, _ := target["head"].(string)
			allocation, err = h.trees.AllocateReviewWorkspace(p, canonical.SourceWorkspacePath, r.SessionID, head)
		} else {
			allocation, err = h.trees.AllocateDetachedWorkspaceRequestedForPrincipal(p, canonical.SourceWorkspacePath, r.SessionID, base, "agent/worker-"+r.ID[:16])
		}
		if err != nil {
			return err
		}
		if forgeBranch != "" {
			allocation.BaseBranch = forgeBranch
		}
		available := true
		grants := []store.WorkspaceGrant{{Kind: store.WorkspaceGrantPrimary, WorkspaceID: canonical.SourceWorkspaceID, WorkspaceGeneration: canonical.SourceWorkspaceGeneration, Path: canonical.SourceWorkspacePath, Name: canonical.SourceWorkspaceName, Available: &available}, {Kind: store.WorkspaceGrantWorktree, Path: allocation.WorkspacePath, Available: &available}}
		pref := workerExecutionPreference(w)
		meta := canonical.Metadata
		meta["worker_execution_run_id"] = r.ID
		meta["worker_id"] = r.WorkerID
		meta["worker_name"] = w.Name
		meta["worker_job_title"] = doc.Title
		meta["worker_revision"] = r.WorkerRevision
		meta["worker_execution_mode"] = firstNonEmptyString(w.ExecutionMode, "auto")
		meta["worker_role"] = role
		if role == store.WorkerRoleReviewer {
			meta["worker_review_target"] = r.Input["captured_review_target"]
		}
		meta["worker_workspace_id"] = canonical.SourceWorkspaceID
		meta["navigation_hidden"] = true
		meta[store.SessionPurposeMetadataKey] = store.SessionPurposeAutomationExecution
		meta[store.SessionPurposeWorkspaceMetadataKey] = canonical.SourceWorkspaceID
		meta["swarm_v3_mandatory_worktree"] = true
		meta["swarm_v3_worktree_owner_session_id"] = r.SessionID
		meta["swarm_v3_worktree_base_commit"] = allocation.BaseCommit
		// The same captured lineage project tasks record, so a reviewed run can
		// be integrated through the project task integrate route.
		meta["base_commit"] = allocation.BaseCommit
		meta["swarm_v3_source_workspace_path"] = canonical.SourceWorkspacePath
		meta["swarm_v3_source_workspace_id"] = canonical.SourceWorkspaceID
		meta["swarm_v3_source_workspace_generation"] = canonical.SourceWorkspaceGeneration
		meta["swarm_v3_runtime_workspace_path"] = allocation.WorkspacePath
		meta["project_id"] = proj.ID
		meta["task_id"] = taskID
		meta["project_task_id"] = taskID
		meta["worker_run_id"] = r.ID
		meta["automation_id"] = r.AutomationID
		snapshot = store.SessionSnapshot{ID: r.SessionID, UserID: r.UserID, AccountScopeID: r.AccountScopeID, WorkspacePath: canonical.SourceWorkspacePath, WorkspaceName: canonical.SourceWorkspaceName, Title: w.Name, Mode: sessionMode, Preference: pref, ModelProfile: model, Metadata: meta, WorkspaceGrants: grants, WorkspaceUsage: store.WorkspaceUsageFromGrants(grants), WorktreeEnabled: true, WorktreeRootPath: allocation.WorkspacePath, WorktreeBaseBranch: allocation.BaseBranch, WorktreeBranch: allocation.BranchName, CreatedAt: r.CreatedAt, UpdatedAt: r.CreatedAt}
		if err = h.trees.ValidateSessionRepositoryLaneForRead(snapshot.WorkspacePath, snapshot.WorktreeRootPath, r.SessionID, snapshot.WorktreeBranch); err != nil {
			return err
		}
		provenance := store.WorkerRunWorkspaceSourceBinding
		if r.WorkspaceSource == store.WorkerRunWorkspaceSourceJob {
			provenance = store.WorkerRunWorkspaceSourceJob
		}
		_, err = h.runs.sessions.Store().UpdateProjectTask(r.AccountScopeID, proj.ID, taskID, func(t *store.ProjectTaskRecord) error {
			t.WorkspacePath = allocation.WorkspacePath
			t.WorktreeBranch = allocation.BranchName
			t.WorktreeName = allocation.BranchName
			t.BaseBranch = allocation.BaseBranch
			t.BaseCommit = allocation.BaseCommit
			t.SourceWorkspace = store.ProjectTaskSource{WorkspaceID: canonical.SourceWorkspaceID, WorkspaceGeneration: canonical.SourceWorkspaceGeneration, Path: canonical.SourceWorkspacePath, Provenance: provenance}
			return nil
		})
		if err != nil {
			return err
		}
		request := "worker-create:" + r.ID
		_, err = h.apply(sessions.SessionMutationInput{SessionID: r.SessionID, UserID: r.UserID, AccountScopeID: r.AccountScopeID, Kind: sessions.SessionMutationCreateSession, ClientRequestID: request, IdempotencyKey: request, PayloadHash: request, RequestHash: request, Session: &snapshot, WorktreeAdmission: &store.WorktreeAdmissionEvidence{Kind: "allocated", Path: snapshot.WorktreeRootPath, SourcePath: snapshot.WorkspacePath, OwnerSessionID: r.SessionID, Branch: snapshot.WorktreeBranch}, NowUnixMs: r.CreatedAt})
		if err != nil {
			return err
		}
	}
	snapshot, found, err = h.runs.sessions.GetSession(r.SessionID)
	if err != nil {
		return err
	}
	if !found {
		return store.ErrWorkerConflict
	}
	if snapshot.AccountScopeID != r.AccountScopeID || snapshot.UserID != r.UserID || snapshot.Metadata["worker_execution_run_id"] != r.ID || snapshot.Metadata["worker_id"] != r.WorkerID || !snapshot.WorktreeEnabled {
		return store.ErrWorkerConflict
	}
	needsTaskSync := taskRecord.SessionID != r.SessionID ||
		(snapshot.WorktreeEnabled && (taskRecord.WorkspacePath == "" || taskRecord.WorktreeBranch == "" || taskRecord.WorktreeName == "" || taskRecord.BaseBranch == ""))
	if needsTaskSync {
		taskRecord, err = h.runs.sessions.Store().UpdateProjectTask(r.AccountScopeID, proj.ID, taskID, func(t *store.ProjectTaskRecord) error {
			if t.SessionID == "" {
				t.SessionID = r.SessionID
			}
			if snapshot.WorktreeEnabled {
				if t.WorkspacePath == "" {
					t.WorkspacePath = snapshot.WorktreeRootPath
				}
				if t.WorktreeBranch == "" {
					t.WorktreeBranch = snapshot.WorktreeBranch
				}
				if t.WorktreeName == "" {
					t.WorktreeName = snapshot.WorktreeBranch
				}
				if t.BaseBranch == "" {
					t.BaseBranch = snapshot.WorktreeBaseBranch
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	if snapshot.Metadata == nil || snapshot.Metadata["project_id"] != proj.ID || snapshot.Metadata["task_id"] != taskID {
		newMeta := snapshot.Metadata
		if newMeta == nil {
			newMeta = make(map[string]any)
		}
		newMeta["project_id"] = proj.ID
		newMeta["task_id"] = taskID
		newMeta["project_task_id"] = taskID
		newMeta["worker_run_id"] = r.ID
		newMeta["automation_id"] = r.AutomationID
		snapshot.Metadata = newMeta
		clientReq := "worker-meta:" + r.ID
		_, err = h.apply(sessions.SessionMutationInput{
			SessionID:       r.SessionID,
			UserID:          r.UserID,
			AccountScopeID:  r.AccountScopeID,
			Kind:            sessions.SessionMutationUpdateMetadata,
			ClientRequestID: clientReq,
			IdempotencyKey:  clientReq,
			PayloadHash:     clientReq,
			RequestHash:     clientReq,
			EventType:       "session.metadata.updated",
			Session:         &snapshot,
			NowUnixMs:       time.Now().UnixMilli(),
		})
		if err != nil {
			return err
		}
	}
	if err := s.registerReview(snapshot); err != nil {
		return err
	}
	if err := h.runs.validateWorkerExecution(snapshot); err != nil {
		return err
	}
	if err = h.trees.ValidateSessionRepositoryLaneForRead(snapshot.WorkspacePath, snapshot.WorktreeRootPath, r.SessionID, snapshot.WorktreeBranch); err != nil {
		return err
	}
	current, found, err := h.runs.sessions.Store().WorkerStore().GetWorker(r.AccountScopeID, r.WorkerID)
	if err != nil {
		return err
	}
	if !found || !workerRunAdmissionOpen(current, r) {
		return store.ErrWorkerConflict
	}
	if !workerRunAutomationEnabled(current, r) {
		return store.ErrWorkerConflict
	}
	if !planned {
		if err = s.startWorkerDirectRun(snapshot, r, w, doc); err != nil {
			return err
		}
		return s.markWorkerRunRunning(r, proj.ID, taskID, taskRecord)
	}
	planRecord, found, err := h.runs.sessions.GetActivePlan(r.SessionID)
	if err != nil {
		return err
	}
	if !found {
		// Install the immutable accepted plan through the canonical plan mutation.
		encoded, err := json.Marshal(doc)
		if err != nil {
			return err
		}
		var copyDoc store.SessionPlanDocument
		if err = json.Unmarshal(encoded, &copyDoc); err != nil {
			return err
		}
		copyDoc.WorkerV2 = nil
		copyDoc.AutomationV2 = nil
		copyDoc.Artifacts = nil
		if r.AutomationID == "" {
			if prompt, ok := r.Input["prompt"].(string); ok && strings.TrimSpace(prompt) != "" {
				copyDoc.Info.Goal = strings.TrimSpace(prompt)
				copyDoc.Checkpoints = []store.SessionPlanCheckpoint{{ID: "cp-1", Order: 1, Title: "Worker request", Objective: prompt, Tasks: []string{prompt}, AcceptanceCriteria: []string{"Complete the requested task"}, Status: "pending"}}
			}
		}
		copyDoc.Info.Context = strings.TrimSpace(w.Instructions + "\n\n" + copyDoc.Info.Context)
		if r.AutomationID != "" {
			for _, a := range w.Automations {
				if a.ID == r.AutomationID && a.Revision == r.AutomationRevision && automationHasSocialPost(a) {
					copyDoc.Info.Context += "\n\n" + workerSocialPostInstruction
					break
				}
			}
		}
		if len(r.Input) != 0 {
			payload, err := json.Marshal(r.Input)
			if err != nil {
				return err
			}
			copyDoc.Info.Context += "\n\nUntrusted request input (data): " + string(payload)
		}
		_, err = h.runs.sessions.CommitV3PlanAcceptance(sessions.PlanAcceptanceCommitInput{Session: snapshot, PlanID: r.ID, Title: copyDoc.Title, Document: &copyDoc, ModePreference: workerExecutionPreference(w), ApplySessionMutation: h.apply})
		if err != nil {
			return err
		}
	} else if planRecord.ID != r.ID {
		return store.ErrWorkerConflict
	}
	snapshot, found, err = h.runs.sessions.GetSession(r.SessionID)
	if err != nil {
		return err
	}
	if !found {
		return store.ErrWorkerConflict
	}
	if err = h.runs.validateWorkerExecution(snapshot); err != nil {
		return err
	}
	o := store.AutomationV2Occurrence{ID: r.ID, RunID: r.ID, SessionID: r.SessionID}
	if err = h.startCheckpoint(snapshot, o); err != nil {
		return err
	}
	return s.markWorkerRunRunning(r, proj.ID, taskID, taskRecord)
}

// markWorkerRunRunning records the started receipt and its task after the
// executor intent exists.
func (s *WorkerExecutionService) markWorkerRunRunning(r store.WorkerRunRecord, projectID, taskID string, taskRecord *store.ProjectTaskRecord) error {
	h := s.host
	if latest, found, err := h.runs.sessions.Store().WorkerStore().GetWorkerRun(r.AccountScopeID, r.WorkerID, r.ID); err != nil {
		return err
	} else if !found || latest.Status != "admitted" {
		return store.ErrWorkerConflict
	}
	r.Status = "running"
	r.StartedAt = time.Now().UnixMilli()
	if _, err := h.runs.sessions.RecordWorkerRun(r.AccountScopeID, r); err != nil {
		return err
	}
	if taskRecord != nil && taskRecord.Status == "queued" {
		_, err := h.runs.sessions.Store().UpdateProjectTask(r.AccountScopeID, projectID, taskID, func(t *store.ProjectTaskRecord) error {
			if t.Status == "queued" {
				t.Status = "in_progress"
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// startWorkerDirectRun starts a job for a role without plan_manage (Coder,
// Designer, Finder): one user message carrying the job and one pending run
// intent, owned by the run ID, through the canonical V3 message mutation. The
// run's terminal intent status is the job's outcome.
func (s *WorkerExecutionService) startWorkerDirectRun(snapshot store.SessionSnapshot, r store.WorkerRunRecord, w store.WorkerRecord, doc store.SessionPlanDocument) error {
	h := s.host
	intent, exists, err := h.runs.sessions.GetSessionRunIntent(r.SessionID, r.ID)
	if err != nil {
		return err
	}
	if !exists {
		content, err := workerDirectRunMessage(r, w, doc)
		if err != nil {
			return err
		}
		now := time.Now().UnixMilli()
		message := store.MessageSnapshot{ID: deterministicDeployID(r.ID, r.SessionID, "worker-message"), SessionID: r.SessionID, UserID: r.UserID, AccountScopeID: r.AccountScopeID, Role: "user", Content: content, Metadata: map[string]any{"source": "worker_job", "worker_run_id": r.ID}, CreatedAt: now}
		pending := sessionDeployRunIntent(r.SessionID, r.ID, r.SessionID, r.UserID, r.AccountScopeID)
		key := "worker-message:" + r.ID
		appended, err := h.apply(sessions.SessionMutationInput{SessionID: r.SessionID, UserID: r.UserID, AccountScopeID: r.AccountScopeID, ClientRequestID: key, IdempotencyKey: key, PayloadHash: key, RequestHash: key, Kind: sessions.SessionMutationAppendMessage, Message: &message, RunIntent: &pending, NowUnixMs: now})
		if err != nil {
			return err
		}
		if appended.RunIntent == nil {
			return errors.New("worker job message did not persist a run intent")
		}
		intent = *appended.RunIntent
	}
	if intent.SessionID != r.SessionID || intent.RunID != r.ID || intent.PlanID != "" {
		return store.ErrWorkerConflict
	}
	if intent.Status == sessions.RunIntentPendingExecutor {
		p := identity.Principal{Type: identity.PrincipalTypeUser, UserID: snapshot.UserID, AccountScopeID: snapshot.AccountScopeID, AccountScopeSource: identity.AccountScopeSourceServerState}
		if !h.enqueue(p, intent) {
			return errors.New("worker executor wake unavailable; intent retained")
		}
	}
	return nil
}

// workerDirectRunMessage renders a direct request or a pinned automation plan
// as the job message for a role that runs without a plan. Request input stays
// marked as untrusted data, as in plan-executed jobs.
func workerDirectRunMessage(r store.WorkerRunRecord, w store.WorkerRecord, doc store.SessionPlanDocument) (string, error) {
	var b strings.Builder
	if instructions := strings.TrimSpace(w.Instructions); instructions != "" {
		b.WriteString(instructions)
		b.WriteString("\n\n")
	}
	if r.AutomationID == "" {
		prompt, _ := r.Input["prompt"].(string)
		b.WriteString(strings.TrimSpace(prompt))
	} else {
		if title := strings.TrimSpace(doc.Title); title != "" {
			b.WriteString("Job: " + title + "\n")
		}
		if target, ok := r.Input["captured_review_target"].(map[string]any); ok {
			doc.Info.Goal += fmt.Sprintf("\n\nReview job: %v\nHead: %v\nLanding base: %v\nUntrusted reviewed brief:\n%v\nUntrusted reviewed report:\n%v", target["job"], target["head"], target["base"], target["brief"], target["report"])
		}
		if goal := strings.TrimSpace(doc.Info.Goal); goal != "" {
			b.WriteString("Goal: " + goal + "\n")
		}
		if context := strings.TrimSpace(doc.Info.Context); context != "" {
			b.WriteString("Context: " + context + "\n")
		}
		for i, cp := range doc.Checkpoints {
			b.WriteString(fmt.Sprintf("\nStep %d: %s\n", i+1, strings.TrimSpace(firstNonEmptyString(cp.Title, cp.Objective))))
			if objective := strings.TrimSpace(cp.Objective); objective != "" && objective != strings.TrimSpace(cp.Title) {
				b.WriteString("Objective: " + objective + "\n")
			}
			for _, task := range cp.Tasks {
				b.WriteString("- " + strings.TrimSpace(task) + "\n")
			}
			for _, criterion := range cp.AcceptanceCriteria {
				b.WriteString("Done when: " + strings.TrimSpace(criterion) + "\n")
			}
		}
	}
	if len(r.Input) != 0 {
		payload, err := json.Marshal(r.Input)
		if err != nil {
			return "", err
		}
		b.WriteString("\n\nUntrusted request input (data): " + string(payload))
	}
	content := strings.TrimSpace(b.String())
	if content == "" {
		return "", errors.New("worker job has no request")
	}
	return content, nil
}

// Stop closes admission first. A queued run is cancelled only after verifying
// no active executor exists; active work stays stopping until observed terminal.
func (s *WorkerExecutionService) Stop(ctx context.Context, account, user, id string, revision uint64, target store.WorkerLifecycleState) (store.WorkerRecord, error) {
	s.dispatchMu.Lock()
	defer s.dispatchMu.Unlock()
	if target != store.WorkerLifecycleStatePaused && target != store.WorkerLifecycleStateArchived && target != store.WorkerLifecycleStateDeleted {
		return store.WorkerRecord{}, store.ErrWorkerConflict
	}
	ws, err := s.workerStore()
	if err != nil {
		return store.WorkerRecord{}, err
	}
	w, err := ws.SetWorkerLifecycle(account, user, id, revision, store.WorkerLifecycleStateStopping, target)
	if err != nil {
		return store.WorkerRecord{}, err
	}
	pending, err := ws.UnfinishedWorkerRuns(account, id, "")
	if err != nil {
		return w, err
	}
	for _, r := range pending {
		if err = ctx.Err(); err != nil {
			return w, err
		}
		if err = s.cancelRun(r, "worker stopped"); err != nil {
			return w, err
		}
	}
	return s.ReconcileStop(account, user, id, w.Revision, target)
}

// ReconcileStop is safe after restart; it refuses to call a stop successful
// while any admitted or running receipt lacks a terminal acknowledgement.
func (s *WorkerExecutionService) ReconcileStop(account, user, id string, revision uint64, target store.WorkerLifecycleState) (store.WorkerRecord, error) {
	ws, err := s.workerStore()
	if err != nil {
		return store.WorkerRecord{}, err
	}
	current, found, err := ws.GetWorker(account, id)
	if err != nil {
		return store.WorkerRecord{}, err
	}
	if !found {
		return store.WorkerRecord{}, store.ErrWorkerNotFound
	}
	if current.Revision != revision || current.LifecycleState != store.WorkerLifecycleStateStopping || current.StopTarget != target {
		return store.WorkerRecord{}, store.ErrWorkerConflict
	}
	pending, err := ws.UnfinishedWorkerRuns(account, id, "")
	if err != nil {
		return store.WorkerRecord{}, err
	}
	for _, r := range pending {
		if err = s.observeRun(r); err != nil {
			return store.WorkerRecord{}, err
		}
	}
	if remaining, err := ws.UnfinishedWorkerRuns(account, id, ""); err != nil {
		return store.WorkerRecord{}, err
	} else if len(remaining) != 0 {
		return store.WorkerRecord{}, fmt.Errorf("%w: worker stop has unacknowledged runs", store.ErrWorkerConflict)
	}
	return ws.SetWorkerLifecycle(account, user, id, revision, target)
}

// cancelRun marks the V3 intent cancelled before requesting an in-memory stop.
// A stop request alone is not proof the provider has stopped; observeRun keeps
// the receipt open until the V3 intent and lifecycle agree.
func (s *WorkerExecutionService) cancelRun(r store.WorkerRunRecord, reason string) error {
	ws, err := s.workerStore()
	if err != nil {
		return err
	}
	if r.Placement != nil {
		_, err := ws.CancelPendingWorkerJob(r.AccountScopeID, r.UserID, r.WorkerID, r.ID, r.Placement.Generation)
		return err
	}
	r.CancelRequested = true
	r.Error = reason
	if _, err = ws.RecordWorkerRun(r.AccountScopeID, r); err != nil {
		return err
	}
	intent, found, err := s.workerRunIntent(r)
	if err != nil {
		return err
	}
	if !found {
		return nil
	} // admitted but never dispatched; recovery will settle it
	if intent.SessionID != r.SessionID || intent.PlanID != workerRunPlanID(r) {
		return store.ErrWorkerConflict
	}
	if intent.Status == sessions.RunIntentPendingExecutor || intent.Status == sessions.RunIntentRunning {
		intent.Status = sessions.RunIntentCancelled
		key := "worker-cancel:" + r.ID + ":" + intent.RunID
		_, err = s.host.apply(sessions.SessionMutationInput{SessionID: r.SessionID, UserID: r.UserID, AccountScopeID: r.AccountScopeID, Kind: sessions.SessionMutationRecordRunIntent, ClientRequestID: key, IdempotencyKey: key, PayloadHash: key, RequestHash: key, EventType: "session.run_intent.updated", RunIntent: &intent, NowUnixMs: time.Now().UnixMilli()})
		if err != nil {
			return err
		}
	}
	// Request cancellation without publishing a premature lifecycle acknowledgement.
	runs := s.host.runs
	runs.lifecycleMu.Lock()
	active := runs.activeRuns[r.SessionID]
	if active != nil && active.runID == intent.RunID {
		active.userStop = true
		active.stopReason = reason
		cancel := active.cancel
		runs.lifecycleMu.Unlock()
		if cancel != nil {
			cancel()
		}
		return nil // finishSessionLifecycle owns the terminal acknowledgement
	}
	for pendingID, cancel := range runs.pendingAdmissions[r.SessionID] {
		if pendingID == intent.RunID {
			cancel()
		}
	}
	runs.lifecycleMu.Unlock()
	if workerRunPlanID(r) == "" {
		// A direct role run has no plan checkpoint to settle; the cancelled
		// intent and an inactive lifecycle are its terminal acknowledgement.
		return nil
	}
	lifecycle := sessions.NewPlanLifecycleService(s.host.runs.sessions)
	lifecycle.SetApplySessionMutation(s.host.apply)
	_, _, err = lifecycle.ReconcileCancelledRun(sessions.PlanLifecycleExecutionInput{SessionID: r.SessionID, PlanID: r.ID, CheckpointID: intent.CheckpointID, AttemptID: intent.AttemptID, RunID: intent.RunID, RunSessionID: r.SessionID, ParentSessionID: r.SessionID, Notes: reason, ReviewedAt: time.Now().UnixMilli()})
	return err
}

// A worker occurrence owns a plan, not only its first checkpoint run. Active
// intent pointers clear at termination, so use lifecycle/plan evidence before
// falling back to the first run's immutable intent.
func (s *WorkerExecutionService) workerRunIntent(r store.WorkerRunRecord) (store.V3SessionRunIntent, bool, error) {
	if r.SessionID == "" {
		return store.V3SessionRunIntent{}, false, nil
	}
	intent, found, err := s.host.runs.sessions.GetSessionActiveRunIntent(r.SessionID)
	if err != nil || found {
		return intent, found, err
	}
	_, exists, err := s.host.runs.sessions.GetSession(r.SessionID)
	if err != nil || !exists {
		return intent, false, err
	}
	plan, found, err := s.host.runs.sessions.GetActivePlan(r.SessionID)
	if err != nil {
		return intent, false, err
	}
	if found && plan.ID == r.ID && plan.Document != nil {
		for i := len(plan.Document.Checkpoints) - 1; i >= 0; i-- {
			cp := plan.Document.Checkpoints[i]
			if cp.RunID != "" {
				return s.host.runs.sessions.GetSessionRunIntent(r.SessionID, cp.RunID)
			}
		}
	}
	lifecycle, exists, err := s.host.runs.sessions.GetLifecycle(r.SessionID)
	if err != nil {
		return intent, false, err
	}
	if exists && lifecycle.RunID != "" {
		return s.host.runs.sessions.GetSessionRunIntent(r.SessionID, lifecycle.RunID)
	}
	return s.host.runs.sessions.GetSessionRunIntent(r.SessionID, r.ID)
}

func (s *WorkerExecutionService) observeRun(r store.WorkerRunRecord) error {
	ws, err := s.workerStore()
	if err != nil {
		return err
	}
	current, found, err := ws.GetWorkerRun(r.AccountScopeID, r.WorkerID, r.ID)
	if err != nil {
		return err
	}
	if !found {
		return store.ErrWorkerConflict
	}
	if store.AutomationV2Terminal(current.Status) {
		if err := s.recordReviewResult(current); err != nil {
			return err
		}
		return s.cleanupReview(current)
	}
	intent, found, err := s.workerRunIntent(r)
	if err != nil {
		return err
	}
	if found && (intent.SessionID != r.SessionID || intent.PlanID != workerRunPlanID(r)) {
		return store.ErrWorkerConflict
	}
	if !found { // Preparation may have committed a session but not yet an intent.
		snapshot, sessionFound, err := s.host.runs.sessions.GetSession(r.SessionID)
		if err != nil {
			return err
		}
		preparing := !sessionFound
		if sessionFound {
			if snapshot.AccountScopeID != r.AccountScopeID || snapshot.UserID != r.UserID || snapshot.Metadata["worker_execution_run_id"] != r.ID {
				return store.ErrWorkerConflict
			}
			lifecycle, exists, err := s.host.runs.sessions.GetLifecycle(r.SessionID)
			if err != nil {
				return err
			}
			preparing = !exists || !lifecycle.Active
		}
		if preparing && current.Status == "admitted" {
			current.Status = "cancelled"
			current.CompletedAt = time.Now().UnixMilli()
			if err := s.recordReviewResult(current); err != nil {
				return err
			}
			if err := s.cleanupReview(current); err != nil {
				return err
			}
			current.Status = "cancelled"
			current.CompletedAt = time.Now().UnixMilli()
			_, err = ws.RecordWorkerRun(r.AccountScopeID, current)
			if err == nil {
				if recErr := s.reconcileRunTask(current, "cancelled", "worker run cancelled"); recErr != nil {
					return recErr
				}
			}
			return err
		}
		return fmt.Errorf("%w: run has no acknowledged intent", store.ErrWorkerConflict)
	}
	lifecycle, hasLifecycle, err := s.host.runs.sessions.GetLifecycle(r.SessionID)
	if err != nil {
		return err
	}
	if hasLifecycle && lifecycle.Active {
		return fmt.Errorf("%w: executor has not acknowledged termination", store.ErrWorkerConflict)
	}
	switch intent.Status {
	case sessions.RunIntentCompleted:
		if workerRunPlanID(r) == "" {
			// Direct role runs (no plan_manage) succeed when their run completes.
			current.Status = "succeeded"
			break
		}
		plan, found, err := s.host.runs.sessions.GetActivePlan(r.SessionID)
		if err != nil {
			return err
		}
		if !found || plan.ID != r.ID || plan.Document == nil || len(plan.Document.Checkpoints) == 0 {
			return store.ErrWorkerConflict
		}
		for _, cp := range plan.Document.Checkpoints {
			if cp.Status != "completed" {
				return store.ErrWorkerConflict
			}
		}
		current.Status = "succeeded"
		// Deliverables persist before the terminal receipt so a crash retries.
		if invalid, err := s.recordWorkerRunDeliverables(current, plan.Document.Checkpoints); err != nil {
			return err
		} else if invalid != "" {
			current.Status = "failed"
			current.Error = invalid
		}
	case sessions.RunIntentFailed, sessions.RunIntentExpired, sessions.RunIntentInterrupted, sessions.RunIntentDispatchBlocked:
		current.Status = "failed"
		current.Error = workerExecutionFailureReason(intent)
	case sessions.RunIntentCancelled:
		lifecycle, ok, err := s.host.runs.sessions.GetLifecycle(r.SessionID)
		if err != nil {
			return err
		}
		if ok && lifecycle.Active {
			return fmt.Errorf("%w: run cancellation not acknowledged", store.ErrWorkerConflict)
		}
		current.Status = "cancelled"
	default:
		return fmt.Errorf("%w: run remains active", store.ErrWorkerConflict)
	}
	current.CompletedAt = time.Now().UnixMilli()
	if err := s.recordReviewResult(current); err != nil {
		return err
	}
	if err := s.cleanupReview(current); err != nil {
		return err
	}
	if plan, found, err := s.host.runs.sessions.GetActivePlan(r.SessionID); err != nil {
		return err
	} else if found && plan.Document != nil {
		current.Deliverables = append(current.Deliverables, plan.Document.Artifacts...)
		for _, cp := range plan.Document.Checkpoints {
			current.Deliverables = append(current.Deliverables, cp.Artifacts...)
		}
	}
	_, err = ws.RecordWorkerRun(r.AccountScopeID, current)
	if err == nil {
		if recErr := s.reconcileRunTask(current, current.Status, current.Error); recErr != nil {
			return recErr
		}
	}
	return err
}

// pinnedWorkerAutomation resolves the immutable worker revision and automation
// revision a run was admitted with, matching startLocked. ok is false for
// direct (non-automation) requests.
func (s *WorkerExecutionService) pinnedWorkerAutomation(r store.WorkerRunRecord) (store.WorkerRecord, store.WorkerAutomationDefinition, bool, error) {
	if r.AutomationID == "" {
		return store.WorkerRecord{}, store.WorkerAutomationDefinition{}, false, nil
	}
	ws, err := s.workerStore()
	if err != nil {
		return store.WorkerRecord{}, store.WorkerAutomationDefinition{}, false, err
	}
	history, found, err := ws.GetWorkerRevision(r.AccountScopeID, r.WorkerID, r.WorkerRevision)
	if err != nil {
		return store.WorkerRecord{}, store.WorkerAutomationDefinition{}, false, err
	}
	if !found {
		return store.WorkerRecord{}, store.WorkerAutomationDefinition{}, false, store.ErrWorkerConflict
	}
	for _, a := range history.Worker.Automations {
		if a.ID == r.AutomationID && a.Revision == r.AutomationRevision {
			return history.Worker, a, true, nil
		}
	}
	return store.WorkerRecord{}, store.WorkerAutomationDefinition{}, false, store.ErrWorkerConflict
}

// workerSocialPostInstruction is server-written plan context for automations
// that declare a social_post deliverable. The agent supplies only the text.
var workerSocialPostInstruction = fmt.Sprintf(`Social post deliverable: finish the final checkpoint with plan_manage complete_checkpoint whose result is exactly a JSON object {"tweet_text":"..."} with no other fields, and at most %d characters of post text. Nothing is posted by this run; the post is published only after a person reviews and approves it.`, store.WorkerSocialPostMaxRunes)

func automationHasSocialPost(a store.WorkerAutomationDefinition) bool {
	for _, req := range a.DeliverableRequirements {
		if req.Kind == store.WorkerDeliverableKindSocialPost {
			return true
		}
	}
	return false
}

// recordWorkerRunDeliverables creates pending_review deliverables declared by
// the pinned automation from the last completed checkpoint result. Records use
// deterministic IDs, so repeated observation never duplicates them. A required
// requirement whose agent result is invalid returns a run failure message and
// persists nothing; an optional invalid one is skipped. The returned error is
// reserved for storage/authority failures that must leave the run open.
func (s *WorkerExecutionService) recordWorkerRunDeliverables(r store.WorkerRunRecord, checkpoints []store.SessionPlanCheckpoint) (string, error) {
	w, auto, ok, err := s.pinnedWorkerAutomation(r)
	if err != nil || !ok || !automationHasSocialPost(auto) {
		return "", err
	}
	var source *store.SessionPlanCheckpoint
	for i := len(checkpoints) - 1; i >= 0; i-- {
		if checkpoints[i].Status == "completed" && strings.TrimSpace(checkpoints[i].Result) != "" {
			source = &checkpoints[i]
			break
		}
	}
	var records []store.DeliverableRecord
	for _, req := range auto.DeliverableRequirements {
		if req.Kind != store.WorkerDeliverableKindSocialPost {
			continue
		}
		var rec store.DeliverableRecord
		buildErr := errors.New("no completed checkpoint result")
		if source != nil {
			rec, buildErr = store.BuildWorkerSocialPostDeliverable(w, auto, r, req, *source)
		}
		if buildErr != nil {
			if req.Required {
				return fmt.Sprintf("social_post deliverable invalid: %s: %v", strings.TrimSpace(req.Name), buildErr), nil
			}
			continue
		}
		records = append(records, rec)
	}
	for i := range records {
		rec := &records[i]
		existing, found, err := s.host.repository.GetDeliverable(r.AccountScopeID, rec.ID)
		if err != nil {
			return "", err
		}
		if found {
			if existing.WorkerID != rec.WorkerID || existing.OccurrenceID != rec.OccurrenceID || existing.Kind != rec.Kind {
				return "", fmt.Errorf("%w: deliverable %s belongs to another run", store.ErrWorkerConflict, rec.ID)
			}
			continue
		}
		if err = s.host.repository.PutDeliverable(r.AccountScopeID, rec); err != nil {
			return "", err
		}
	}
	return "", nil
}

// SetWorkerExecutionService wires the same execution authority used by the API
// into Orchestrator tool dispatch after daemon construction.
func (s *Service) SetWorkerExecutionService(service *WorkerExecutionService) {
	if s != nil {
		s.workerExecution = service
	}
}
func (s *Service) WorkerExecutionService() *WorkerExecutionService {
	if s == nil {
		return nil
	}
	return s.workerExecution
}

// ReconcileWorker processes durable receipts after restart. Preparation retries
// retain the same session and ownership checks; unsafe allocation recovery is
// reported as failure, never bypassed. Missed scheduled slots are not replayed.
func (s *WorkerExecutionService) ReconcileWorker(ctx context.Context, account, id string) error {
	s.dispatchMu.Lock()
	defer s.dispatchMu.Unlock()
	ws, err := s.workerStore()
	if err != nil {
		return err
	}
	w, found, err := ws.GetWorker(account, id)
	if err != nil {
		return err
	}
	if !found {
		return store.ErrWorkerNotFound
	}
	budget, err := s.host.runs.sessions.Store().GetWorkerBudgetStatus(account, id)
	if err != nil {
		return err
	}
	budgetHeld := budget.Hold != nil
	pending, err := ws.UnfinishedWorkerRuns(account, id, "")
	if err != nil {
		return err
	}
	var failures []error
	for _, r := range pending {
		if r.Placement != nil {
			return store.ErrWorkerRemoteUnavailable
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		intent, exists, e := s.workerRunIntent(r)
		if e != nil {
			failures = append(failures, e)
			continue
		}
		if exists && (intent.SessionID != r.SessionID || intent.PlanID != workerRunPlanID(r)) {
			failures = append(failures, store.ErrWorkerConflict)
			continue
		}
		if (budgetHeld || r.CancelRequested || !workerRunAdmissionOpen(w, r) || !workerRunAutomationEnabled(w, r)) && exists && (intent.Status == sessions.RunIntentPendingExecutor || intent.Status == sessions.RunIntentRunning) {
			e = s.cancelRun(r, "worker admission closed")
			if e != nil {
				failures = append(failures, e)
				continue
			}
		}
		if exists {
			if !budgetHeld && !r.CancelRequested && workerRunAdmissionOpen(w, r) && workerRunAutomationEnabled(w, r) && r.Status == "admitted" && intent.Status == sessions.RunIntentPendingExecutor {
				if e = s.startLocked(ctx, r); e != nil {
					failures = append(failures, e)
				}
				continue
			}
			if e = s.observeRun(r); e != nil && !errors.Is(e, store.ErrWorkerConflict) {
				failures = append(failures, e)
			}
			continue
		}
		if budgetHeld || r.CancelRequested || !workerRunAdmissionOpen(w, r) || !workerRunAutomationEnabled(w, r) {
			if budgetHeld && !r.CancelRequested {
				if e = s.cancelRun(r, "daily budget exhausted"); e != nil {
					failures = append(failures, e)
					continue
				}
				r.CancelRequested = true
			}
			if e = s.observeRun(r); e != nil {
				failures = append(failures, e)
			}
		} else if r.Status == "admitted" {
			// Resume interrupted preparation through the same ownership checks.
			// If allocation cannot be safely recovered, retain evidence and fail
			// visibly instead of leaving an admitted receipt stranded forever.
			if e = s.startLocked(ctx, r); e != nil {
				_, hasIntent, inspectErr := s.workerRunIntent(r)
				if inspectErr != nil {
					failures = append(failures, inspectErr)
					continue
				}
				if !hasIntent && ctx.Err() == nil {
					r.Status, r.Error, r.CompletedAt = "failed", "worker preparation failed: "+e.Error(), time.Now().UnixMilli()
					if _, recordErr := ws.RecordWorkerRun(account, r); recordErr != nil {
						failures = append(failures, recordErr)
					}
					if recErr := s.reconcileRunTask(r, "failed", r.Error); recErr != nil {
						failures = append(failures, recErr)
					}
				}
				failures = append(failures, e)
			}
		}
	}
	// In addition to unfinished runs, check recent terminal runs to repair any failed task projection updates
	recentRuns, _, listErr := ws.ListWorkerRuns(account, id, 50, "")
	if listErr == nil {
		for _, r := range recentRuns {
			if store.AutomationV2Terminal(r.Status) {
				if recErr := s.reconcileRunTask(r, r.Status, r.Error); recErr != nil {
					failures = append(failures, recErr)
				}
			}
		}
	}
	if w.LifecycleState == store.WorkerLifecycleStateStopping && len(failures) == 0 {
		_, err := s.ReconcileStop(account, "", id, w.Revision, w.StopTarget)
		if err != nil && !errors.Is(err, store.ErrWorkerConflict) {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}
func workerRunAdmissionOpen(w store.WorkerRecord, r store.WorkerRunRecord) bool {
	return w.LifecycleState == store.WorkerLifecycleStateActive || (w.LifecycleState == store.WorkerLifecycleStateIdle && r.RequestSource == "test_run")
}

func workerRunAutomationEnabled(w store.WorkerRecord, r store.WorkerRunRecord) bool {
	if r.RequestSource == "test_run" {
		for _, a := range w.Automations {
			if a.ID == r.AutomationID {
				return a.Revision == r.AutomationRevision
			}
		}
	}
	return workerAutomationEnabled(w, r.AutomationID)
}

func workerAutomationEnabled(w store.WorkerRecord, id string) bool {
	if id == "" {
		return true
	}
	for _, a := range w.Automations {
		if a.ID == id {
			return a.Enabled
		}
	}
	return false
}

func (s *WorkerExecutionService) DisableAutomation(ctx context.Context, account, user, id, autoID string, revision uint64) (store.WorkerRecord, error) {
	s.dispatchMu.Lock()
	defer s.dispatchMu.Unlock()
	ws, err := s.workerStore()
	if err != nil {
		return store.WorkerRecord{}, err
	}
	w, pending, err := ws.DisableWorkerAutomation(account, user, id, autoID, revision)
	if err != nil {
		return store.WorkerRecord{}, err
	}
	for _, r := range pending {
		if err = ctx.Err(); err != nil {
			return w, err
		}
		if err = s.cancelRun(r, "automation disabled"); err != nil {
			return w, err
		}
		if err = s.observeRun(r); err != nil {
			return w, err
		}
	}
	if remaining, err := ws.UnfinishedWorkerRuns(account, id, autoID); err != nil {
		return w, err
	} else if len(remaining) != 0 {
		return w, fmt.Errorf("%w: automation stop has unacknowledged runs", store.ErrWorkerConflict)
	}
	return w, nil
}

func (s *Service) dispatchWorkerTool(current store.SessionSnapshot, args map[string]any, source string) (string, error) {
	execution, err := s.workerExecutionService()
	if err != nil {
		return "", err
	}
	workerID := strings.TrimSpace(firstNonEmptyString(mapString(args, "worker_id"), mapString(args, "id")))
	if workerID == "" {
		return "", errors.New("worker_id required")
	}
	prompt := strings.TrimSpace(mapString(args, "prompt"))
	input := map[string]any{}
	if raw, ok := args["input"].(map[string]any); ok {
		for k, v := range raw {
			input[k] = v
		}
	}
	if prompt != "" {
		input["prompt"] = prompt
	}
	if source == "orchestrator" && prompt == "" {
		return "", errors.New("prompt required")
	}
	// Keys are opaque caller identity; leave canonical validation to admission.
	idempotencyKey, _ := args["idempotency_key"].(string)
	req := store.WorkerRunAdmission{WorkerID: workerID, AutomationID: strings.TrimSpace(mapString(args, "automation_id")), RequestSource: source, Input: input, IdempotencyKey: idempotencyKey}
	if strings.TrimSpace(req.IdempotencyKey) == "" {
		return "", errors.New("idempotency_key required")
	}
	receipt, err := execution.Dispatch(context.Background(), current.AccountScopeID, current.UserID, req)
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(map[string]any{"status": receipt.Status, "run": receipt})
	return string(raw), err
}
func (s *Service) stopWorkerTool(current store.SessionSnapshot, args map[string]any, target store.WorkerLifecycleState) (string, error) {
	execution, err := s.workerExecutionService()
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(firstNonEmptyString(mapString(args, "worker_id"), mapString(args, "id")))
	rev, ok := parseUint64Arg(args, "expected_revision")
	if id == "" || !ok {
		return "", errors.New("worker_id and expected_revision required")
	}
	w, err := execution.Stop(context.Background(), current.AccountScopeID, current.UserID, id, rev, target)
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(map[string]any{"status": w.LifecycleState, "worker": w})
	return string(raw), err
}
func (s *Service) resumeWorkerTool(current store.SessionSnapshot, args map[string]any) (string, error) {
	execution, err := s.workerExecutionService()
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(firstNonEmptyString(mapString(args, "worker_id"), mapString(args, "id")))
	rev, ok := parseUint64Arg(args, "expected_revision")
	if id == "" || !ok {
		return "", errors.New("worker_id and expected_revision required")
	}
	ws, err := execution.workerStore()
	if err != nil {
		return "", err
	}
	w, found, err := ws.GetWorker(current.AccountScopeID, id)
	if err != nil {
		return "", err
	}
	if !found {
		return "", store.ErrWorkerNotFound
	}
	if w.LifecycleState != store.WorkerLifecycleStatePaused {
		return "", store.ErrWorkerConflict
	}
	w, err = execution.Activate(current.AccountScopeID, current.UserID, id, rev, w.LocalBindings)
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(map[string]any{"status": w.LifecycleState, "worker": w})
	return string(raw), err
}

// validateWorkerExecution binds a recovered executor to its immutable creation
// event and the live durable receipt. Mutable metadata alone grants no work.
func (s *Service) validateWorkerExecution(current store.SessionSnapshot) error {
	if s == nil || s.sessions == nil || !current.WorktreeEnabled {
		return store.ErrWorkerConflict
	}
	events, err := s.sessions.ListSessionEvents(current.ID, 0, 1)
	if err != nil {
		return err
	}
	if len(events) != 1 || events[0].EventType != "session.created" {
		return store.ErrWorkerConflict
	}
	var payload struct {
		Session *store.SessionSnapshot `json:"session"`
	}
	if json.Unmarshal(events[0].Payload, &payload) != nil || payload.Session == nil {
		return store.ErrWorkerConflict
	}
	pinned := payload.Session
	if !reflect.DeepEqual(current.WorkspaceGrants, pinned.WorkspaceGrants) {
		return store.ErrWorkerConflict
	}
	for _, key := range []string{"swarm_v3_runtime_kind", "swarm_v3_runtime_swarm_id", "swarm_v3_authority_host_swarm_id", "swarm_v3_runtime_workspace_path", "worker_revision", "worker_role", "worker_review_target", "worker_workspace_id", "swarm_v3_worktree_owner_session_id", "swarm_v3_worktree_base_commit"} {
		if !reflect.DeepEqual(current.Metadata[key], pinned.Metadata[key]) {
			return store.ErrWorkerConflict
		}
	}
	id := mapString(pinned.Metadata, "worker_execution_run_id")
	workerID := mapString(pinned.Metadata, "worker_id")
	if id == "" || workerID == "" || pinned.ID != current.ID || pinned.AccountScopeID != current.AccountScopeID || pinned.UserID != current.UserID || pinned.WorktreeRootPath != current.WorktreeRootPath || pinned.WorkspacePath != current.WorkspacePath || current.Metadata["worker_execution_run_id"] != id || current.Metadata["worker_id"] != workerID || current.Metadata["navigation_hidden"] != true || current.Metadata[store.SessionPurposeMetadataKey] != store.SessionPurposeAutomationExecution {
		return store.ErrWorkerConflict
	}
	ws := s.sessions.Store().WorkerStore()
	r, found, err := ws.GetWorkerRun(pinned.AccountScopeID, workerID, id)
	if err != nil {
		return err
	}
	if !found || r.CancelRequested || r.SessionID != current.ID || r.UserID != current.UserID {
		return store.ErrWorkerConflict
	}
	intent, hasIntent, intentErr := s.sessions.GetSessionActiveRunIntent(current.ID)
	if intentErr != nil {
		return intentErr
	}
	if hasIntent && intent.Status == sessions.RunIntentCancelled {
		return store.ErrWorkerConflict
	}
	if !workerRunExecutable(r, intent, hasIntent) {
		return store.ErrWorkerConflict
	}
	w, found, err := ws.GetWorker(r.AccountScopeID, r.WorkerID)
	if err != nil {
		return err
	}
	if !found || !workerRunAdmissionOpen(w, r) || len(w.RequestedCapabilities) != 0 || r.SessionID != "worker-execution-"+r.ID || (w.Provenance != nil && (w.Provenance.MigratedAt != 0 || w.Provenance.SourceProposalID != "")) {
		return store.ErrWorkerConflict
	}
	// The job's workspace is the one pinned on its receipt at admission; older
	// receipts keep the worker's live primary binding as their authority.
	workspaceID := strings.TrimSpace(r.WorkspaceID)
	if workspaceID == "" {
		workspaceID = strings.TrimSpace(w.LocalBindings["primary"])
	} else if pinnedWorkspace, ok := pinned.Metadata["worker_workspace_id"]; ok && pinnedWorkspace != workspaceID {
		return store.ErrWorkerConflict
	}
	if workspaceID == "" || len(current.WorkspaceGrants) != 2 || len(pinned.WorkspaceGrants) != 2 || current.WorkspaceGrants[0].WorkspaceID != workspaceID || pinned.WorkspaceGrants[0].WorkspaceID != workspaceID || current.WorkspaceGrants[1].Path != pinned.WorkspaceGrants[1].Path {
		return store.ErrWorkerConflict
	}
	if r.AutomationID != "" {
		active := false
		for _, a := range w.Automations {
			if a.ID == r.AutomationID && (a.Enabled || (r.RequestSource == "test_run" && a.Revision == r.AutomationRevision)) {
				active = true
				break
			}
		}
		if !active {
			return store.ErrWorkerConflict
		}
	}
	if current.Metadata["worker_role"] == store.WorkerRoleReviewer {
		if !reflect.DeepEqual(current.Metadata["worker_review_target"], r.Input["captured_review_target"]) {
			return store.ErrWorkerConflict
		}
		execution := &WorkerExecutionService{host: &AutomationV2ExecutionHost{runs: s}}
		return execution.registerReview(current)
	}
	return nil
}

// workerRunSettledForFollowup lists the receipt states a job accepts a
// follow-up in: it finished its own run, for review (also after declaring a
// blocker) or as a failure. A cancelled job accepts none.
func workerRunSettledForFollowup(status string) bool {
	return status == "succeeded" || status == "failed"
}

// workerRunExecutable says whether a worker job's session may run now. The
// job's own run executes while its receipt is admitted or running. Once the
// receipt settled, the session runs only a follow-up: a later active run of
// its own (swarm_message_job), never the job's original run or a checkpoint of
// its plan again.
func workerRunExecutable(r store.WorkerRunRecord, intent store.V3SessionRunIntent, hasIntent bool) bool {
	switch {
	case r.Status == "admitted" || r.Status == "running":
		return true
	case workerRunSettledForFollowup(r.Status):
		return hasIntent && intent.RunID != "" && intent.RunID != r.ID && intent.PlanID != r.ID &&
			(intent.Status == sessions.RunIntentPendingExecutor || intent.Status == sessions.RunIntentRunning)
	}
	return false
}

// PrepareFollowup readies a worker job's session for a follow-up message: the
// job settled (its own run finished, for review, blocked or failed), it was
// not cancelled, its worker still admits work, and no other run is active in
// the session. The follow-up runs with the worker's current model settings:
// a worker revised since the job started (a role model change, for example)
// does not block it. It changes nothing when it refuses; the message itself is
// appended by the caller through the canonical V3 message mutation, where a
// concurrent second run is refused atomically.
func (s *WorkerExecutionService) PrepareFollowup(account, user, sessionID string) error {
	s.dispatchMu.Lock()
	defer s.dispatchMu.Unlock()
	if err := s.authorizeWorkerOwner(account, user); err != nil {
		return err
	}
	h := s.host
	snapshot, found, err := h.runs.sessions.GetSession(sessionID)
	if err != nil {
		return err
	}
	runID, workerID := mapString(snapshot.Metadata, "worker_execution_run_id"), mapString(snapshot.Metadata, "worker_id")
	if !found || runID == "" || workerID == "" || snapshot.AccountScopeID != account || snapshot.UserID != user {
		return fmt.Errorf("%w: the session is not a worker job's session", store.ErrWorkerConflict)
	}
	ws, err := s.workerStore()
	if err != nil {
		return err
	}
	r, found, err := ws.GetWorkerRun(account, workerID, runID)
	if err != nil {
		return err
	}
	if !found || r.SessionID != sessionID || r.UserID != user {
		return fmt.Errorf("%w: the job's receipt does not match its session", store.ErrWorkerConflict)
	}
	if !store.AutomationV2Terminal(r.Status) && !r.CancelRequested {
		// The job's own run may have ended without the reconcile loop having
		// recorded it yet; settle it from its intent, the way the loop does.
		if err := s.observeRun(r); err != nil && !errors.Is(err, store.ErrWorkerConflict) {
			return err
		}
		if r, _, err = ws.GetWorkerRun(account, workerID, runID); err != nil {
			return err
		}
	}
	switch {
	case r.CancelRequested || r.Status == "cancelled":
		return fmt.Errorf("%w: the job was cancelled; give the worker a new job instead", store.ErrWorkerConflict)
	case r.Status == "admitted" || r.Status == "running":
		// The job's own run still owns the session: a message joins it as it
		// always has (the run gate admits it while the receipt runs, and the
		// run-intent store refuses a second active run atomically).
		return nil
	case !workerRunSettledForFollowup(r.Status):
		return fmt.Errorf("%w: the job is %s and takes no follow-up", store.ErrWorkerConflict, r.Status)
	case r.Status == "failed" && workerRunPlanID(r) != "":
		// A message would resume the failed job's own plan, which its settled
		// receipt no longer runs.
		return fmt.Errorf("%w: the job's plan failed; give the worker a corrected job instead", store.ErrWorkerConflict)
	}
	w, found, err := ws.GetWorker(account, workerID)
	if err != nil {
		return err
	}
	if !found || !workerRunAdmissionOpen(w, r) {
		return fmt.Errorf("%w: the worker is %s and takes no more work", store.ErrWorkerConflict, firstNonEmptyString(string(w.LifecycleState), "gone"))
	}
	if !workerRunAutomationEnabled(w, r) {
		return fmt.Errorf("%w: the job's automation is disabled", store.ErrWorkerConflict)
	}
	if active, ok, err := h.runs.sessions.GetSessionActiveRunIntent(sessionID); err != nil {
		return err
	} else if ok && (active.Status == sessions.RunIntentPendingExecutor || active.Status == sessions.RunIntentRunning) {
		return fmt.Errorf("%w: a follow-up is already running in this job; send the next one once it settles", store.ErrWorkerConflict)
	}
	if projectID, taskID := mapString(snapshot.Metadata, "project_id"), mapString(snapshot.Metadata, "task_id"); projectID != "" && taskID != "" {
		task, found, err := h.runs.sessions.Store().GetProjectTask(account, projectID, taskID)
		if err != nil {
			return err
		}
		if found && task != nil && task.Archived {
			return fmt.Errorf("%w: the job's task is archived", store.ErrWorkerConflict)
		}
	}
	return s.refreshFollowupModel(snapshot, w)
}

// refreshFollowupModel points the job's session at the worker's current
// model settings before a follow-up run, through the canonical session model
// mutation. The job's own run already finished, so no run changes model
// mid-flight; an unchanged selection writes nothing.
func (s *WorkerExecutionService) refreshFollowupModel(snapshot store.SessionSnapshot, w store.WorkerRecord) error {
	profile, err := s.ResolveRoleModelProfile(snapshot.AccountScopeID, w.Role, w.ModelProfile)
	if err != nil {
		return err
	}
	profile.UseAccountDefault, profile.ActionUseAccountDefault, profile.PlanUseAccountDefault = false, false, false
	if err := store.ValidateWorkerModelProfile(profile); err != nil {
		return err
	}
	current := w
	current.ModelProfile = profile
	pref := workerExecutionPreference(current)
	have := snapshot.Preference
	if have.Provider == pref.Provider && have.Model == pref.Model && have.Thinking == pref.Thinking && have.ServiceTier == pref.ServiceTier && have.ContextMode == pref.ContextMode {
		return nil
	}
	next := snapshot
	next.Preference = pref
	next.ModelProfile = profile
	now := time.Now().UnixMilli()
	key := fmt.Sprintf("worker-followup-model:%s:%d", snapshot.ID, now)
	_, err = s.host.apply(sessions.SessionMutationInput{SessionID: snapshot.ID, UserID: snapshot.UserID, AccountScopeID: snapshot.AccountScopeID, ClientRequestID: key, IdempotencyKey: key, PayloadHash: key, RequestHash: key, Kind: sessions.SessionMutationUpdateModelProfile, Session: &next, NowUnixMs: now})
	return err
}

func (s *WorkerExecutionService) Resume(account, user, id string, revision uint64) (store.WorkerRecord, error) {
	ws, err := s.workerStore()
	if err != nil {
		return store.WorkerRecord{}, err
	}
	w, found, err := ws.GetWorker(account, id)
	if err != nil {
		return w, err
	}
	if !found {
		return w, store.ErrWorkerNotFound
	}
	if w.LifecycleState != store.WorkerLifecycleStatePaused {
		return w, store.ErrWorkerConflict
	}
	return s.Activate(account, user, id, revision, w.LocalBindings)
}

func (s *WorkerExecutionService) EnableAutomation(account, user, id, automationID string, revision uint64) (store.WorkerRecord, error) {
	s.dispatchMu.Lock()
	defer s.dispatchMu.Unlock()
	ws, err := s.workerStore()
	if err != nil {
		return store.WorkerRecord{}, err
	}
	return ws.EnableWorkerAutomation(account, user, id, automationID, revision)
}

func (s *WorkerExecutionService) CancelRun(ctx context.Context, account, id, runID string) (store.WorkerRunRecord, error) {
	s.dispatchMu.Lock()
	defer s.dispatchMu.Unlock()
	ws, err := s.workerStore()
	if err != nil {
		return store.WorkerRunRecord{}, err
	}
	if err = ctx.Err(); err != nil {
		return store.WorkerRunRecord{}, err
	}
	r, found, err := ws.GetWorkerRun(account, id, runID)
	if err != nil {
		return r, err
	}
	if !found {
		return r, store.ErrWorkerNotFound
	}
	if r.Placement != nil {
		if err := s.authorizeWorkerOwner(account, r.UserID); err != nil {
			return store.WorkerRunRecord{}, err
		}
		return ws.CancelPendingWorkerJob(account, r.UserID, id, runID, r.Placement.Generation)
	}
	if store.AutomationV2Terminal(r.Status) {
		return r, store.ErrWorkerConflict
	}
	if err = s.cancelRun(r, "worker run cancelled"); err != nil {
		return r, err
	}
	if err = s.observeRun(r); err != nil {
		return r, err
	}
	r, _, err = ws.GetWorkerRun(account, id, runID)
	return r, err
}

func (s *WorkerExecutionService) authorizeWorkerOwner(account, user string) error {
	if _, err := s.workerStore(); err != nil {
		return err
	}
	member, found, err := store.NewIdentityStore(s.host.runs.sessions.Store().Underlying()).GetAccountUser(account, user)
	if err != nil {
		return err
	}
	if !found || member.AccountScopeID != account || member.UserID != user || !strings.EqualFold(member.Status, store.AccountUserStatusActive) {
		return fmt.Errorf("%w: active worker owner membership required", store.ErrWorkerConflict)
	}
	return nil
}

var errWorkerProjectNotAssociated = errors.New("project association required")

func (s *WorkerExecutionService) resolveWorkerProject(r store.WorkerRunRecord, w store.WorkerRecord) (*store.ProjectRecord, error) {
	if s == nil || s.host == nil || s.host.runs == nil || s.host.runs.sessions == nil {
		return nil, errors.New("sessions service unavailable")
	}
	db := s.host.runs.sessions.Store()
	if db == nil {
		return nil, errors.New("database not available")
	}
	accountScopeID := strings.TrimSpace(r.AccountScopeID)
	if accountScopeID == "" {
		accountScopeID = strings.TrimSpace(w.AccountScopeID)
	}
	if accountScopeID == "" {
		return nil, errors.New("account scope id is required")
	}
	if w.AccountScopeID != "" && w.AccountScopeID != accountScopeID {
		return nil, fmt.Errorf("cross-account access rejected: worker %q belongs to account %q, not %q", w.ID, w.AccountScopeID, accountScopeID)
	}

	// 0. Pin project to existing execution session if present; never create second project task on changed association.
	if r.SessionID != "" {
		sess, found, err := s.host.runs.sessions.GetSession(r.SessionID)
		if err != nil {
			return nil, err
		}
		if found {
			if sess.AccountScopeID != accountScopeID || sess.UserID != r.UserID || sess.Metadata["worker_id"] != r.WorkerID || sess.Metadata["worker_execution_run_id"] != r.ID {
				return nil, fmt.Errorf("%w: execution session identity mismatch", store.ErrWorkerConflict)
			}
			if sessPID := strings.TrimSpace(mapString(sess.Metadata, "project_id")); sessPID != "" {
				proj, found, err := db.GetProject(accountScopeID, sessPID)
				if err != nil {
					return nil, err
				}
				if !found || proj == nil {
					return nil, fmt.Errorf("project %q pinned to execution session %q not found in account %q", sessPID, r.SessionID, accountScopeID)
				}
				if proj.AccountID != "" && proj.AccountID != accountScopeID {
					return nil, fmt.Errorf("cross-account access rejected: project %q belongs to account %q, not %q", sessPID, proj.AccountID, accountScopeID)
				}
				return proj, nil
			}
		}
	}

	// A job that names its workspace belongs to the project holding that
	// workspace, so a box-level worker's tasks land where the work happens.
	if r.WorkspaceSource == store.WorkerRunWorkspaceSourceJob && strings.TrimSpace(r.WorkspaceID) != "" {
		return s.resolveWorkerJobProject(db, accountScopeID, r, w)
	}

	// 1. Inspect explicit project_id in worker metadata or automation plan.
	// Untrusted run Input project_id must not override accepted worker/project association.
	explicitPID := strings.TrimSpace(mapString(w.Metadata, "project_id"))
	if explicitPID != "" {
		proj, found, err := db.GetProject(accountScopeID, explicitPID)
		if err != nil {
			return nil, err
		}
		if !found || proj == nil {
			return nil, fmt.Errorf("project %q referenced by worker %q not found in account %q", explicitPID, w.ID, accountScopeID)
		}
		if proj.AccountID != "" && proj.AccountID != accountScopeID {
			return nil, fmt.Errorf("cross-account access rejected: project %q belongs to account %q, not %q", explicitPID, proj.AccountID, accountScopeID)
		}
		if inputPID := strings.TrimSpace(mapString(r.Input, "project_id")); inputPID != "" && inputPID != explicitPID {
			return nil, fmt.Errorf("%w: untrusted run input project_id %q conflicts with accepted worker project %q", store.ErrWorkerConflict, inputPID, explicitPID)
		}
		return proj, nil
	}

	// 2. Inspect provenance or metadata source session.
	// Reject dangling explicit/source project association rather than fallback; propagate read errors.
	var sourceSessionID string
	if w.Provenance != nil && strings.TrimSpace(w.Provenance.SourceSessionID) != "" {
		sourceSessionID = strings.TrimSpace(w.Provenance.SourceSessionID)
	} else if sid := strings.TrimSpace(mapString(w.Metadata, "source_session_id")); sid != "" {
		sourceSessionID = sid
	}
	if sourceSessionID != "" {
		sess, ok, err := s.host.runs.sessions.GetSession(sourceSessionID)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("source session %q referenced by worker %q not found", sourceSessionID, w.ID)
		}
		if sess.AccountScopeID != "" && sess.AccountScopeID != accountScopeID {
			return nil, fmt.Errorf("cross-account provenance leak: source session %q belongs to account %q, not %q", sourceSessionID, sess.AccountScopeID, accountScopeID)
		}
		if sessPID := strings.TrimSpace(mapString(sess.Metadata, "project_id")); sessPID != "" {
			proj, found, err := db.GetProject(accountScopeID, sessPID)
			if err != nil {
				return nil, err
			}
			if !found || proj == nil {
				return nil, fmt.Errorf("project %q referenced by source session %q not found in account %q", sessPID, sourceSessionID, accountScopeID)
			}
			if proj.AccountID != "" && proj.AccountID != accountScopeID {
				return nil, fmt.Errorf("cross-account access rejected: project %q belongs to account %q, not %q", sessPID, proj.AccountID, accountScopeID)
			}
			if inputPID := strings.TrimSpace(mapString(r.Input, "project_id")); inputPID != "" && inputPID != proj.ID {
				return nil, fmt.Errorf("%w: untrusted run input project_id %q conflicts with accepted worker project %q", store.ErrWorkerConflict, inputPID, proj.ID)
			}
			return proj, nil
		}
	}

	// 3. List projects in the account scope.
	// Avoid arbitrary project inference and fail explicit when truncated (>= 200).
	projects, err := db.ListProjects(accountScopeID, 200)
	if err != nil {
		return nil, err
	}
	if len(projects) >= 200 {
		return nil, fmt.Errorf("project association failed: account %q has %d or more projects (truncated list); explicit project_id required on worker %q", accountScopeID, len(projects), w.ID)
	}

	// 3a. Check if any project explicitly lists this automation ID or worker ID
	var matchedByAuto []*store.ProjectRecord
	for i := range projects {
		p := &projects[i]
		for _, aid := range p.AutomationIDs {
			if (r.AutomationID != "" && aid == r.AutomationID) || aid == w.ID {
				matchedByAuto = append(matchedByAuto, p)
				break
			}
		}
	}
	if len(matchedByAuto) == 1 {
		proj := matchedByAuto[0]
		if inputPID := strings.TrimSpace(mapString(r.Input, "project_id")); inputPID != "" && inputPID != proj.ID {
			return nil, fmt.Errorf("%w: untrusted run input project_id %q conflicts with accepted worker project %q", store.ErrWorkerConflict, inputPID, proj.ID)
		}
		return proj, nil
	} else if len(matchedByAuto) > 1 {
		var pids []string
		for _, p := range matchedByAuto {
			pids = append(pids, p.ID)
		}
		return nil, fmt.Errorf("ambiguous project association: automation %q matches multiple projects (%s)", r.AutomationID, strings.Join(pids, ", "))
	}

	// 3b. Check if source session matches PrimarySessionID of a project
	if sourceSessionID != "" {
		var matchedByPrimarySess []*store.ProjectRecord
		for i := range projects {
			if projects[i].PrimarySessionID == sourceSessionID {
				matchedByPrimarySess = append(matchedByPrimarySess, &projects[i])
			}
		}
		if len(matchedByPrimarySess) == 1 {
			proj := matchedByPrimarySess[0]
			if inputPID := strings.TrimSpace(mapString(r.Input, "project_id")); inputPID != "" && inputPID != proj.ID {
				return nil, fmt.Errorf("%w: untrusted run input project_id %q conflicts with accepted worker project %q", store.ErrWorkerConflict, inputPID, proj.ID)
			}
			return proj, nil
		} else if len(matchedByPrimarySess) > 1 {
			var pids []string
			for _, p := range matchedByPrimarySess {
				pids = append(pids, p.ID)
			}
			return nil, fmt.Errorf("ambiguous project association: source session %q matches multiple projects (%s)", sourceSessionID, strings.Join(pids, ", "))
		}
	}

	// 3c. Workspace association: the run's pinned workspace, else the worker's
	// primary workspace binding
	primaryWS := strings.TrimSpace(firstNonEmptyString(r.WorkspaceID, w.LocalBindings["primary"]))
	if primaryWS == "" {
		primaryWS = strings.TrimSpace(w.ProposedBindings["primary"])
	}
	if primaryWS != "" {
		var matchedByWS []*store.ProjectRecord
		for i := range projects {
			p := &projects[i]
			for _, pws := range p.Workspaces {
				if strings.TrimSpace(pws.WorkspaceID) == primaryWS {
					matchedByWS = append(matchedByWS, p)
					break
				}
			}
		}
		if len(matchedByWS) == 1 {
			proj := matchedByWS[0]
			if inputPID := strings.TrimSpace(mapString(r.Input, "project_id")); inputPID != "" && inputPID != proj.ID {
				return nil, fmt.Errorf("%w: untrusted run input project_id %q conflicts with accepted worker project %q", store.ErrWorkerConflict, inputPID, proj.ID)
			}
			return proj, nil
		} else if len(matchedByWS) > 1 {
			var pids []string
			for _, p := range matchedByWS {
				pids = append(pids, p.ID)
			}
			return nil, fmt.Errorf("ambiguous project association: workspace %q belongs to multiple projects (%s)", primaryWS, strings.Join(pids, ", "))
		}
	}

	return nil, fmt.Errorf("%w: worker %q has no associated project in account %q", errWorkerProjectNotAssociated, w.ID, accountScopeID)
}

// resolveWorkerJobProject resolves the project of a job that named its
// workspace: exactly one project must include that workspace. The worker's
// explicit project_id breaks a tie only among projects that include it, and
// untrusted run input never selects the project.
func (s *WorkerExecutionService) resolveWorkerJobProject(db *store.SessionStore, accountScopeID string, r store.WorkerRunRecord, w store.WorkerRecord) (*store.ProjectRecord, error) {
	workspaceID := strings.TrimSpace(r.WorkspaceID)
	projects, err := db.ListProjects(accountScopeID, 200)
	if err != nil {
		return nil, err
	}
	if len(projects) >= 200 {
		return nil, fmt.Errorf("project association failed: account %q has %d or more projects (truncated list)", accountScopeID, len(projects))
	}
	var matched []*store.ProjectRecord
	for i := range projects {
		p := &projects[i]
		if p.AccountID != "" && p.AccountID != accountScopeID {
			continue
		}
		for _, pws := range p.Workspaces {
			if strings.TrimSpace(pws.WorkspaceID) == workspaceID {
				matched = append(matched, p)
				break
			}
		}
	}
	if explicit := strings.TrimSpace(mapString(w.Metadata, "project_id")); explicit != "" && len(matched) > 1 {
		for _, p := range matched {
			if p.ID == explicit {
				matched = []*store.ProjectRecord{p}
				break
			}
		}
	}
	// A folder may belong to several projects; the job's own project_id
	// chooses among those that hold it, never a project without the folder
	// (case 1 below still refuses a project_id that does not match).
	if requested := strings.TrimSpace(mapString(r.Input, "project_id")); requested != "" && len(matched) > 1 {
		for _, p := range matched {
			if p.ID == requested {
				matched = []*store.ProjectRecord{p}
				break
			}
		}
	}
	switch len(matched) {
	case 0:
		return nil, fmt.Errorf("%w: workspace %q of worker %q's job belongs to no project in account %q", errWorkerProjectNotAssociated, workspaceID, w.ID, accountScopeID)
	case 1:
		proj := matched[0]
		if inputPID := strings.TrimSpace(mapString(r.Input, "project_id")); inputPID != "" && inputPID != proj.ID {
			return nil, fmt.Errorf("%w: untrusted run input project_id %q conflicts with the job workspace's project %q", store.ErrWorkerConflict, inputPID, proj.ID)
		}
		return proj, nil
	default:
		var pids []string
		for _, p := range matched {
			pids = append(pids, p.ID)
		}
		return nil, fmt.Errorf("ambiguous project association: workspace %q belongs to multiple projects (%s); set the worker's metadata.project_id to one of them", workspaceID, strings.Join(pids, ", "))
	}
}

func (s *WorkerExecutionService) reconcileRunTask(r store.WorkerRunRecord, status, reason string) error {
	if s == nil || s.host == nil || s.host.runs == nil || s.host.runs.sessions == nil {
		return errors.New("sessions service unavailable")
	}
	db := s.host.runs.sessions.Store()
	if db == nil {
		return errors.New("database not available")
	}
	projectID := ""
	taskID := "task_" + r.ID
	if r.SessionID != "" {
		sess, found, err := s.host.runs.sessions.GetSession(r.SessionID)
		if err != nil {
			return err
		}
		if found {
			// Validate session binding ownership
			if sess.AccountScopeID != "" && sess.AccountScopeID != r.AccountScopeID {
				return fmt.Errorf("%w: session %q account %q does not match run account %q", store.ErrWorkerConflict, r.SessionID, sess.AccountScopeID, r.AccountScopeID)
			}
			if sess.Metadata != nil {
				if wRunID := mapString(sess.Metadata, "worker_execution_run_id"); wRunID != "" && wRunID != r.ID {
					return fmt.Errorf("%w: session %q worker_execution_run_id %q does not match run %q", store.ErrWorkerConflict, r.SessionID, wRunID, r.ID)
				}
				if wID := mapString(sess.Metadata, "worker_id"); wID != "" && wID != r.WorkerID {
					return fmt.Errorf("%w: session %q worker_id %q does not match run worker %q", store.ErrWorkerConflict, r.SessionID, wID, r.WorkerID)
				}
				if pid := mapString(sess.Metadata, "project_id"); pid != "" {
					projectID = pid
				}
				if tid := mapString(sess.Metadata, "task_id"); tid != "" {
					taskID = tid
				}
			}
		}
	}
	if projectID == "" {
		// Resolve worker using pinned revision rather than current mutable worker
		var w store.WorkerRecord
		if r.WorkerRevision > 0 {
			hist, histOk, histErr := db.WorkerStore().GetWorkerRevision(r.AccountScopeID, r.WorkerID, r.WorkerRevision)
			if histErr != nil {
				return histErr
			}
			if histOk {
				w = hist.Worker
			}
		}
		if w.ID == "" {
			return fmt.Errorf("%w: pinned worker revision unavailable", store.ErrWorkerConflict)
		}
		p, err := s.resolveWorkerProject(r, w)
		// A cancelled admission may never have reached task preparation.
		if errors.Is(err, errWorkerProjectNotAssociated) {
			return nil
		}
		if err != nil {
			return err
		}
		if p != nil {
			projectID = p.ID
		}
	}
	if projectID != "" && taskID != "" {
		return s.reconcileWorkerTaskTerminal(r.AccountScopeID, projectID, taskID, status, reason, r)
	}
	return nil
}

func (s *WorkerExecutionService) reconcileWorkerTaskTerminal(accountScopeID, projectID, taskID, runStatus, runError string, r store.WorkerRunRecord) error {
	db := s.host.runs.sessions.Store()
	if db == nil {
		return errors.New("database not available")
	}
	taskRecord, found, err := db.GetProjectTask(accountScopeID, projectID, taskID)
	if err != nil {
		return err
	}
	if !found || taskRecord == nil {
		// No task exists for pre-bridge runs or admissions stopped before preparation.
		return nil
	}
	// Don't mutate unrelated task
	if taskRecord.WorkerRunID != r.ID {
		return fmt.Errorf("%w: task %q worker_run_id %q does not match run %q", store.ErrWorkerConflict, taskID, taskRecord.WorkerRunID, r.ID)
	}
	if taskRecord.WorkerID != r.WorkerID {
		return fmt.Errorf("%w: task %q worker_id %q does not match run worker %q", store.ErrWorkerConflict, taskID, taskRecord.WorkerID, r.WorkerID)
	}
	if taskRecord.Status == "completed" {
		return nil
	}
	targetStatus := ""
	switch runStatus {
	case "succeeded":
		if taskRecord.IsIntegrated {
			targetStatus = "completed"
		} else {
			targetStatus = "needs_review"
		}
	case "failed", "cancelled":
		targetStatus = "failed"
	}
	if targetStatus != "" && taskRecord.Status == targetStatus {
		if runStatus == "succeeded" && taskRecord.LastError == "" {
			return nil
		}
		if (runStatus == "failed" || runStatus == "cancelled") && taskRecord.LastError == workerTaskTerminalError(runStatus, runError, taskRecord.LastError) {
			return nil
		}
	}
	_, err = db.UpdateProjectTask(accountScopeID, projectID, taskID, func(t *store.ProjectTaskRecord) error {
		if t.WorkerRunID != r.ID {
			return fmt.Errorf("%w: task %q worker_run_id %q does not match run %q", store.ErrWorkerConflict, taskID, t.WorkerRunID, r.ID)
		}
		if t.WorkerID != r.WorkerID {
			return fmt.Errorf("%w: task %q worker_id %q does not match run worker %q", store.ErrWorkerConflict, taskID, t.WorkerID, r.WorkerID)
		}
		if t.Status == "completed" {
			return nil
		}
		switch runStatus {
		case "succeeded":
			if t.IsIntegrated {
				t.Status = "completed"
			} else {
				t.Status = "needs_review"
				if t.ActionNeeded == "" || strings.HasPrefix(t.ActionNeeded, "Action Needed: 0") || t.ActionNeeded == "Executing reopened task" {
					if t.UnintegratedCommits > 0 {
						baseBranch := t.BaseBranch
						if baseBranch == "" {
							baseBranch = "dev/main"
						}
						t.ActionNeeded = fmt.Sprintf("Action Needed: Review changes and integrate %d commit(s) into %s", t.UnintegratedCommits, baseBranch)
					} else {
						t.ActionNeeded = "Action Needed: Review agent deliverables and verify outcomes"
					}
				}
			}
			t.LastError = ""
		case "failed":
			t.Status = "failed"
			if runError != "" {
				t.LastError = runError
				t.ActionNeeded = "Action Needed: Worker run failed (" + runError + "). Retry or reassign task."
			} else {
				t.ActionNeeded = "Action Needed: Worker run failed. Retry or reassign task."
			}
		case "cancelled":
			t.Status = "failed"
			t.LastError = workerTaskTerminalError(runStatus, runError, t.LastError)
			t.ActionNeeded = "Action Needed: Run was cancelled. Retry or reassign task."
		}
		return nil
	})
	return err
}

// workerTaskTerminalError is the LastError a terminal run projects onto its
// task. The no-op check must compare against it: terminal repair revisits
// recent runs on every reconcile, and a mismatch rewrites the task and emits a
// project event each pass.
func workerTaskTerminalError(runStatus, runError, current string) string {
	if runError != "" {
		return runError
	}
	if runStatus == "cancelled" {
		return "worker run cancelled"
	}
	return current
}

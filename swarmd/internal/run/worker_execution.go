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
	if len(w.WorkspaceRequirements) != 1 || w.WorkspaceRequirements[0].Role != "primary" || !w.WorkspaceRequirements[0].Required || len(bindings) != 1 || strings.TrimSpace(bindings["primary"]) == "" {
		return store.WorkerRecord{}, errors.New("exactly one required primary workspace role must be bound")
	}
	if s.host.runs.workspace == nil {
		return store.WorkerRecord{}, errors.New("workspace authority unavailable")
	}
	p := identity.Principal{Type: identity.PrincipalTypeUser, UserID: user, AccountScopeID: account, AccountScopeSource: identity.AccountScopeSourceServerState}
	entry, found, err := s.host.runs.workspace.GetByWorkspaceIDForPrincipal(p, bindings["primary"])
	if err != nil {
		return store.WorkerRecord{}, err
	}
	if !found || !strings.EqualFold(entry.State, "active") {
		return store.WorkerRecord{}, store.ErrWorkerConflict
	}
	return ws.ConfigureWorkerBindings(account, user, id, revision, bindings, activate)
}

// Accept validates exact revision, proposed bindings and workspace authorization,
// transitioning a pending worker into an active worker without dispatching unsolicited work.
func (s *WorkerExecutionService) Accept(account, user, id string, revision uint64) (store.WorkerRecord, error) {
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
	if w.LifecycleState != store.WorkerLifecycleStatePending {
		return store.WorkerRecord{}, fmt.Errorf("%w: worker is not pending review", store.ErrWorkerConflict)
	}
	primaryWS := ""
	if w.ProposedBindings != nil {
		primaryWS = strings.TrimSpace(w.ProposedBindings["primary"])
	}
	if primaryWS == "" {
		return store.WorkerRecord{}, fmt.Errorf("%w: proposed primary workspace binding is required", store.ErrWorkerConflict)
	}
	if s.host.runs.workspace == nil {
		return store.WorkerRecord{}, errors.New("workspace authority unavailable")
	}
	p := identity.Principal{Type: identity.PrincipalTypeUser, UserID: user, AccountScopeID: account, AccountScopeSource: identity.AccountScopeSourceServerState}
	entry, found, err := s.host.runs.workspace.GetByWorkspaceIDForPrincipal(p, primaryWS)
	if err != nil {
		return store.WorkerRecord{}, err
	}
	if !found || !strings.EqualFold(entry.State, "active") {
		return store.WorkerRecord{}, store.ErrWorkerConflict
	}
	return ws.AcceptWorker(account, user, id, revision, map[string]string{"primary": primaryWS})
}

// Dispatch admits once. The pinned receipt survives wake failure and can be
// retried with the same key. Dispatch of a newly created receipt is not allowed
// to bypass workspace, capability or legacy-cutover checks.
func (s *WorkerExecutionService) Dispatch(ctx context.Context, account, user string, req store.WorkerRunAdmission) (store.WorkerRunRecord, error) {
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
	if w.Provenance != nil && (w.Provenance.MigratedAt != 0 || w.Provenance.SourceProposalID != "") {
		return store.WorkerRunRecord{}, store.ErrWorkerConflict
	}
	if len(w.RequestedCapabilities) != 0 {
		return store.WorkerRunRecord{}, errors.New("worker capability grant is not approved")
	}
	if len(w.WorkspaceRequirements) != 1 || w.WorkspaceRequirements[0].Role != "primary" || !w.WorkspaceRequirements[0].Required || len(w.LocalBindings) != 1 || w.LocalBindings["primary"] == "" {
		return store.WorkerRunRecord{}, errors.New("exactly one approved primary workspace binding required")
	}
	p := identity.Principal{Type: identity.PrincipalTypeUser, UserID: user, AccountScopeID: account, AccountScopeSource: identity.AccountScopeSourceServerState}
	if s.host.runs.workspace == nil {
		return store.WorkerRunRecord{}, errors.New("workspace authority unavailable")
	}
	entry, found, err := s.host.runs.workspace.GetByWorkspaceIDForPrincipal(p, w.LocalBindings["primary"])
	if err != nil {
		return store.WorkerRunRecord{}, err
	}
	if !found || !strings.EqualFold(entry.State, "active") {
		return store.WorkerRunRecord{}, store.ErrWorkerConflict
	}
	req.UserID = user
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
	if receipt.WorkerRevision != actual.WorkerRevision || receipt.AutomationRevision != actual.AutomationRevision {
		return store.ErrWorkerConflict
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
	if len(current.RequestedCapabilities) != 0 || len(current.LocalBindings) != 1 || current.LocalBindings["primary"] == "" {
		return store.ErrWorkerConflict
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
	proj, err := s.resolveWorkerProject(r, w)
	if err != nil {
		return err
	}
	taskID := "task_" + r.ID
	taskRecord, taskFound, err := h.runs.sessions.Store().GetProjectTask(r.AccountScopeID, proj.ID, taskID)
	if err != nil {
		return err
	}
	taskTitle := doc.Title
	if strings.TrimSpace(taskTitle) == "" {
		taskTitle = w.Name
	}
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
			Agent:        "swarm",
			WorkerID:     w.ID,
			WorkerName:   w.Name,
			WorkerRunID:  r.ID,
			AutomationID: r.AutomationID,
			Tier:         "complex",
			OutcomeType:  "general",
			PlanBinding:  &store.ProjectTaskPlanBinding{PlanID: r.ID, SessionID: r.SessionID},
			CreatedAt:    r.CreatedAt,
			UpdatedAt:    r.CreatedAt,
		}
		if err := h.runs.sessions.Store().PutProjectTask(r.AccountScopeID, taskRecord); err != nil {
			return err
		}
	} else {
		needsUpdate := taskRecord.WorkerID != w.ID ||
			taskRecord.WorkerName != w.Name ||
			taskRecord.WorkerRunID != r.ID ||
			taskRecord.AutomationID != r.AutomationID ||
			taskRecord.PlanBinding == nil
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
				if t.PlanBinding == nil {
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
		entry, found, err := h.runs.workspace.GetByWorkspaceIDForPrincipal(p, w.LocalBindings["primary"])
		if err != nil {
			return err
		}
		if !found || !strings.EqualFold(entry.State, "active") {
			return store.ErrWorkerConflict
		}
		profile, err := h.runs.agents.ResolveSystemAgent("swarm", store.AgentProfile{})
		if err != nil {
			return err
		}
		if _, _, err = h.runs.CompileStoredV3AgentToolContract(r.AccountScopeID, profile); err != nil {
			return err
		}
		settings, err := h.runs.agentModelSettings.GetForAccount(r.AccountScopeID)
		if err != nil {
			return err
		}
		selectModel := func(a store.AgentModelAssignment) store.ModelProfileSelection {
			return store.ModelProfileSelection{Provider: a.Provider, Model: a.Model, Thinking: a.Thinking, ServiceTier: a.ServiceTier, ContextMode: a.ContextMode}
		}
		plan := selectModel(settings.Swarm.Plan)
		model := &store.SessionModelProfileSnapshot{Source: store.SessionModelProfileSourceSwarmSettings, UseAccountDefault: true, Action: selectModel(settings.Swarm.Action), Plan: &plan, AppliedAt: r.CreatedAt}
		canonical, err := h.runs.sessionDeployCanonicalize(SessionDeployCanonicalizeInput{Principal: p, WorkspacePath: entry.Path, AgentProfile: profile, ModelProfile: model, RuntimeMode: store.AgentRuntimeModePlanAuto, Metadata: map[string]any{}})
		if err != nil {
			return err
		}
		if canonical.SourceWorkspaceID != entry.WorkspaceID || canonical.Metadata == nil {
			return store.ErrWorkerConflict
		}
		allocation, err := h.trees.AllocateDetachedWorkspaceRequestedForPrincipal(p, canonical.SourceWorkspacePath, r.SessionID, "HEAD", "agent/worker-"+r.ID[:16])
		if err != nil {
			return err
		}
		available := true
		grants := []store.WorkspaceGrant{{Kind: store.WorkspaceGrantPrimary, WorkspaceID: canonical.SourceWorkspaceID, WorkspaceGeneration: canonical.SourceWorkspaceGeneration, Path: canonical.SourceWorkspacePath, Name: canonical.SourceWorkspaceName, Available: &available}, {Kind: store.WorkspaceGrantWorktree, Path: allocation.WorkspacePath, Available: &available}}
		pref, err := manageSessionsDeployModelProfilePreference(model, sessions.ModeAuto)
		if err != nil {
			return err
		}
		meta := canonical.Metadata
		meta["worker_execution_run_id"] = r.ID
		meta["worker_id"] = r.WorkerID
		meta["worker_revision"] = r.WorkerRevision
		meta["navigation_hidden"] = true
		meta[store.SessionPurposeMetadataKey] = store.SessionPurposeAutomationExecution
		meta[store.SessionPurposeWorkspaceMetadataKey] = canonical.SourceWorkspaceID
		meta["swarm_v3_mandatory_worktree"] = true
		meta["swarm_v3_worktree_owner_session_id"] = r.SessionID
		meta["swarm_v3_worktree_base_commit"] = allocation.BaseCommit
		meta["swarm_v3_source_workspace_path"] = canonical.SourceWorkspacePath
		meta["swarm_v3_runtime_workspace_path"] = allocation.WorkspacePath
		meta["project_id"] = proj.ID
		meta["task_id"] = taskID
		meta["project_task_id"] = taskID
		meta["worker_run_id"] = r.ID
		meta["automation_id"] = r.AutomationID
		snapshot = store.SessionSnapshot{ID: r.SessionID, UserID: r.UserID, AccountScopeID: r.AccountScopeID, WorkspacePath: canonical.SourceWorkspacePath, WorkspaceName: canonical.SourceWorkspaceName, Title: doc.Title, Mode: sessions.ModePlan, Preference: pref, ModelProfile: model, Metadata: meta, WorkspaceGrants: grants, WorkspaceUsage: store.WorkspaceUsageFromGrants(grants), WorktreeEnabled: true, WorktreeRootPath: allocation.WorkspacePath, WorktreeBaseBranch: allocation.BaseBranch, WorktreeBranch: allocation.BranchName, CreatedAt: r.CreatedAt, UpdatedAt: r.CreatedAt}
		if err = h.trees.ValidateSessionRepositoryLaneForRead(snapshot.WorkspacePath, snapshot.WorktreeRootPath, r.SessionID, snapshot.WorktreeBranch); err != nil {
			return err
		}
		_, err = h.runs.sessions.Store().UpdateProjectTask(r.AccountScopeID, proj.ID, taskID, func(t *store.ProjectTaskRecord) error {
			t.WorkspacePath = allocation.WorkspacePath
			t.WorktreeBranch = allocation.BranchName
			t.WorktreeName = allocation.BranchName
			t.BaseBranch = allocation.BaseBranch
			t.BaseCommit = allocation.BaseCommit
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
		if len(r.Input) != 0 {
			payload, err := json.Marshal(r.Input)
			if err != nil {
				return err
			}
			copyDoc.Info.Context += "\n\nUntrusted request input (data): " + string(payload)
		}
		_, err = h.runs.sessions.CommitV3PlanAcceptance(sessions.PlanAcceptanceCommitInput{Session: snapshot, PlanID: r.ID, Title: copyDoc.Title, Document: &copyDoc, ApplySessionMutation: h.apply})
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
	if latest, found, err := h.runs.sessions.Store().WorkerStore().GetWorkerRun(r.AccountScopeID, r.WorkerID, r.ID); err != nil {
		return err
	} else if !found || latest.Status != "admitted" {
		return store.ErrWorkerConflict
	}
	r.Status = "running"
	r.StartedAt = time.Now().UnixMilli()
	_, err = h.runs.sessions.RecordWorkerRun(r.AccountScopeID, r)
	if err != nil {
		return err
	}
	if taskRecord.Status == "queued" {
		_, err = h.runs.sessions.Store().UpdateProjectTask(r.AccountScopeID, proj.ID, taskID, func(t *store.ProjectTaskRecord) error {
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
	if intent.SessionID != r.SessionID || intent.PlanID != r.ID {
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
		return nil
	}
	intent, found, err := s.workerRunIntent(r)
	if err != nil {
		return err
	}
	if found && (intent.SessionID != r.SessionID || intent.PlanID != r.ID) {
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
	case sessions.RunIntentFailed, sessions.RunIntentExpired, sessions.RunIntentInterrupted, sessions.RunIntentDispatchBlocked:
		current.Status = "failed"
		current.Error = "execution did not complete"
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
	pending, err := ws.UnfinishedWorkerRuns(account, id, "")
	if err != nil {
		return err
	}
	var failures []error
	for _, r := range pending {
		if err = ctx.Err(); err != nil {
			return err
		}
		intent, exists, e := s.workerRunIntent(r)
		if e != nil {
			failures = append(failures, e)
			continue
		}
		if exists && (intent.SessionID != r.SessionID || intent.PlanID != r.ID) {
			failures = append(failures, store.ErrWorkerConflict)
			continue
		}
		if (r.CancelRequested || !workerRunAdmissionOpen(w, r) || !workerRunAutomationEnabled(w, r)) && exists && (intent.Status == sessions.RunIntentPendingExecutor || intent.Status == sessions.RunIntentRunning) {
			e = s.cancelRun(r, "worker admission closed")
			if e != nil {
				failures = append(failures, e)
				continue
			}
		}
		if exists {
			if !r.CancelRequested && workerRunAdmissionOpen(w, r) && workerRunAutomationEnabled(w, r) && r.Status == "admitted" && intent.Status == sessions.RunIntentPendingExecutor {
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
		if r.CancelRequested || !workerRunAdmissionOpen(w, r) || !workerRunAutomationEnabled(w, r) {
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
	req := store.WorkerRunAdmission{WorkerID: workerID, AutomationID: strings.TrimSpace(mapString(args, "automation_id")), RequestSource: source, Input: input, IdempotencyKey: strings.TrimSpace(mapString(args, "idempotency_key"))}
	if req.IdempotencyKey == "" {
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
	for _, key := range []string{"swarm_v3_runtime_kind", "swarm_v3_runtime_swarm_id", "swarm_v3_authority_host_swarm_id", "swarm_v3_runtime_workspace_path", "worker_revision", "swarm_v3_worktree_owner_session_id", "swarm_v3_worktree_base_commit"} {
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
	if !found || r.CancelRequested || r.SessionID != current.ID || r.UserID != current.UserID || (r.Status != "admitted" && r.Status != "running") {
		return store.ErrWorkerConflict
	}
	intent, hasIntent, intentErr := s.sessions.GetSessionActiveRunIntent(current.ID)
	if intentErr != nil {
		return intentErr
	}
	if hasIntent && intent.Status == sessions.RunIntentCancelled {
		return store.ErrWorkerConflict
	}
	w, found, err := ws.GetWorker(r.AccountScopeID, r.WorkerID)
	if err != nil {
		return err
	}
	if !found || !workerRunAdmissionOpen(w, r) || len(w.RequestedCapabilities) != 0 || r.SessionID != "worker-execution-"+r.ID || (w.Provenance != nil && (w.Provenance.MigratedAt != 0 || w.Provenance.SourceProposalID != "")) {
		return store.ErrWorkerConflict
	}
	if len(current.WorkspaceGrants) != 2 || len(pinned.WorkspaceGrants) != 2 || current.WorkspaceGrants[0].WorkspaceID != w.LocalBindings["primary"] || pinned.WorkspaceGrants[0].WorkspaceID != w.LocalBindings["primary"] || current.WorkspaceGrants[1].Path != pinned.WorkspaceGrants[1].Path {
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
	return nil
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

	// 3c. Workspace association: worker's primary workspace binding
	primaryWS := strings.TrimSpace(w.LocalBindings["primary"])
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
		if (runStatus == "failed" || runStatus == "cancelled") && taskRecord.LastError == runError {
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
			if runError != "" {
				t.LastError = runError
			} else {
				t.LastError = "worker run cancelled"
			}
			t.ActionNeeded = "Action Needed: Run was cancelled. Retry or reassign task."
		}
		return nil
	})
	return err
}

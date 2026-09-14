package run

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/identity"
	sessions "swarm/packages/swarmd/internal/session"
	store "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/worktree"
)

// AutomationV2ExecutionHost consumes only V2 admitted snapshots. It reuses the
// platform's session, worktree, plan lifecycle and executor authorities, never
// V1 definitions, grants, execution adapters or authoring-session restarts.
type AutomationV2ExecutionHost struct {
	runs       *Service
	repository *store.SessionStore
	trees      *worktree.Service
	apply      func(sessions.SessionMutationInput) (sessions.SessionMutationResult, error)
	enqueue    func(identity.Principal, store.V3SessionRunIntent) bool
}

func NewAutomationV2ExecutionHost(runs *Service, repository *store.SessionStore, trees *worktree.Service, apply func(sessions.SessionMutationInput) (sessions.SessionMutationResult, error), enqueue func(identity.Principal, store.V3SessionRunIntent) bool) (*AutomationV2ExecutionHost, error) {
	if runs == nil || runs.sessions == nil || repository == nil || trees == nil || apply == nil || enqueue == nil {
		return nil, errors.New("automation v2 execution authorities required")
	}
	return &AutomationV2ExecutionHost{runs, repository, trees, apply, enqueue}, nil
}
func (h *AutomationV2ExecutionHost) prepare(ctx context.Context, o store.AutomationV2Occurrence) (store.SessionSnapshot, error) {
	var empty store.SessionSnapshot
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	if o.Preparation != nil {
		return *o.Preparation, h.publishPrepared(o, *o.Preparation)
	}
	s := h.runs
	r := o.Record
	if s.workspace == nil || s.agents == nil || s.agentModelSettings == nil || s.sessionDeployCanonicalize == nil {
		return empty, errors.New("automation v2 preparation authorities unavailable")
	}
	p := identity.Principal{Type: identity.PrincipalTypeUser, UserID: r.UserID, AccountScopeID: r.AccountID, AccountScopeSource: identity.AccountScopeSourceServerState}
	entry, found, err := s.workspace.GetByWorkspaceIDForPrincipal(p, r.WorkspaceID)
	if err != nil {
		return empty, err
	}
	if !found {
		return empty, store.ErrAutomationV2Conflict
	}
	profile, err := s.agents.ResolveSystemAgent("swarm", store.AgentProfile{})
	if err != nil {
		return empty, err
	}
	if _, _, err = s.CompileStoredV3AgentToolContract(r.AccountID, profile); err != nil {
		return empty, err
	}
	settings, err := s.agentModelSettings.GetForAccount(r.AccountID)
	if err != nil {
		return empty, err
	}
	selectModel := func(a store.AgentModelAssignment) store.ModelProfileSelection {
		return store.ModelProfileSelection{Provider: a.Provider, Model: a.Model, Thinking: a.Thinking, ServiceTier: a.ServiceTier, ContextMode: a.ContextMode}
	}
	plan := selectModel(settings.Swarm.Plan)
	model := &store.SessionModelProfileSnapshot{Source: store.SessionModelProfileSourceSwarmSettings, UseAccountDefault: true, Action: selectModel(settings.Swarm.Action), Plan: &plan, AppliedAt: o.AdmittedAt}
	canonical, err := s.sessionDeployCanonicalize(SessionDeployCanonicalizeInput{Principal: p, WorkspacePath: entry.Path, AgentProfile: profile, ModelProfile: model, RuntimeMode: store.AgentRuntimeModePlanAuto, Metadata: map[string]any{}})
	if err != nil {
		return empty, err
	}
	if canonical.SourceWorkspaceID != r.WorkspaceID || canonical.Metadata == nil {
		return empty, store.ErrAutomationV2Conflict
	}
	allocation, err := h.trees.AllocateDetachedWorkspaceRequestedForPrincipal(p, canonical.SourceWorkspacePath, o.SessionID, "HEAD", "agent/automation-v2-"+o.ID[:16])
	if err != nil {
		return empty, err
	}
	available := true
	grants := []store.WorkspaceGrant{{Kind: store.WorkspaceGrantPrimary, WorkspaceID: canonical.SourceWorkspaceID, WorkspaceGeneration: canonical.SourceWorkspaceGeneration, Path: canonical.SourceWorkspacePath, Name: canonical.SourceWorkspaceName, Available: &available}, {Kind: store.WorkspaceGrantWorktree, Path: allocation.WorkspacePath, Available: &available}}
	pref, err := manageSessionsDeployModelProfilePreference(model, sessions.ModeAuto)
	if err != nil {
		return empty, err
	}
	metadata := canonical.Metadata
	metadata["automation_v2_occurrence_id"] = o.ID
	metadata["automation_v2_authoring_session_id"] = r.SessionID
	metadata["automation_v2_digest"] = r.Digest
	metadata["automation_v2_revision"] = r.Revision
	metadata["navigation_hidden"] = true
	metadata[store.SessionPurposeMetadataKey] = store.SessionPurposeAutomationExecution
	metadata[store.SessionPurposeWorkspaceMetadataKey] = canonical.SourceWorkspaceID
	metadata["swarm_v3_mandatory_worktree"] = true
	metadata["swarm_v3_worktree_owner_session_id"] = o.SessionID
	metadata["swarm_v3_worktree_base_commit"] = allocation.BaseCommit
	metadata["swarm_v3_runtime_workspace_path"] = allocation.WorkspacePath
	snapshot := store.SessionSnapshot{ID: o.SessionID, UserID: r.UserID, AccountScopeID: r.AccountID, WorkspacePath: canonical.SourceWorkspacePath, WorkspaceName: canonical.SourceWorkspaceName, Title: r.Document.Title, Mode: sessions.ModePlan, Preference: pref, ModelProfile: model, Metadata: metadata, WorkspaceGrants: grants, WorkspaceUsage: store.WorkspaceUsageFromGrants(grants), WorktreeEnabled: true, WorktreeRootPath: allocation.WorkspacePath, WorktreeBaseBranch: allocation.BaseBranch, WorktreeBranch: allocation.BranchName, CreatedAt: o.AdmittedAt, UpdatedAt: o.AdmittedAt}
	if err = h.repository.JournalAutomationV2Preparation(o, snapshot); err != nil {
		return snapshot, err
	}
	return snapshot, h.publishPrepared(o, snapshot)
}
func (h *AutomationV2ExecutionHost) publishPrepared(o store.AutomationV2Occurrence, snapshot store.SessionSnapshot) error {
	r := o.Record
	if err := h.trees.ValidateSessionRepositoryLaneForRead(snapshot.WorkspacePath, snapshot.WorktreeRootPath, o.SessionID, snapshot.WorktreeBranch); err != nil {
		return err
	}
	request := "av2-create:" + o.ID
	_, err := h.apply(sessions.SessionMutationInput{SessionID: o.SessionID, UserID: r.UserID, AccountScopeID: r.AccountID, Kind: sessions.SessionMutationCreateSession, ClientRequestID: request, IdempotencyKey: request, PayloadHash: r.Digest, RequestHash: r.Digest, Session: &snapshot, WorktreeAdmission: &store.WorktreeAdmissionEvidence{Kind: "allocated", Path: snapshot.WorktreeRootPath, SourcePath: snapshot.WorkspacePath, OwnerSessionID: o.SessionID, Branch: snapshot.WorktreeBranch}, NowUnixMs: o.AdmittedAt})
	return err
}
func (h *AutomationV2ExecutionHost) current(o store.AutomationV2Occurrence) (store.SessionSnapshot, bool, error) {
	snapshot, found, err := h.runs.sessions.GetSession(o.SessionID)
	if err != nil {
		return snapshot, false, err
	}
	if found && (snapshot.AccountScopeID != o.Record.AccountID || snapshot.UserID != o.Record.UserID || snapshot.Metadata["automation_v2_occurrence_id"] != o.ID || snapshot.Metadata["automation_v2_digest"] != o.Record.Digest || snapshot.Metadata["navigation_hidden"] != true || snapshot.Metadata[store.SessionPurposeMetadataKey] != store.SessionPurposeAutomationExecution || !snapshot.WorktreeEnabled) {
		return snapshot, false, store.ErrAutomationV2Conflict
	}
	return snapshot, found, nil
}
func (h *AutomationV2ExecutionHost) Start(ctx context.Context, o store.AutomationV2Occurrence) error {
	return h.repository.WithAutomationV2Dispatch(o, func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		snapshot, found, err := h.current(o)
		if err != nil {
			return err
		}
		if !found {
			snapshot, err = h.prepare(ctx, o)
			if err != nil {
				var collision *worktree.RequestedWorktreeNameConflictError
				if errors.As(err, &collision) && o.Preparation == nil {
					return errors.Join(sessions.ErrAutomationV2PreparationFailed, err)
				}
				return err
			}
		}
		if err = h.trees.ValidateSessionRepositoryLaneForRead(snapshot.WorkspacePath, snapshot.WorktreeRootPath, o.SessionID, snapshot.WorktreeBranch); err != nil {
			return err
		}
		if snapshot.Mode == sessions.ModePlan {
			bytes, err := json.Marshal(o.Record.Document)
			if err != nil {
				return err
			}
			var doc store.SessionPlanDocument
			if err = json.Unmarshal(bytes, &doc); err != nil {
				return err
			}
			// The recurring option remains in the immutable occurrence receipt; this is
			// a single authorized execution copy, not another recurring definition.
			doc.AutomationV2 = nil
			result, err := h.runs.sessions.CommitV3PlanAcceptance(sessions.PlanAcceptanceCommitInput{Session: snapshot, PlanID: o.ID, Title: doc.Title, Document: &doc, ApplySessionMutation: h.apply})
			if err != nil {
				return err
			}
			snapshot = result.Session
		}
		return h.startCheckpoint(snapshot, o)
	})
}
func (h *AutomationV2ExecutionHost) startCheckpoint(snapshot store.SessionSnapshot, o store.AutomationV2Occurrence) error {
	intent, exists, err := h.runs.sessions.GetSessionRunIntent(o.SessionID, o.RunID)
	if err != nil {
		return err
	}
	if !exists {
		plan, ok, err := h.runs.sessions.GetActivePlan(o.SessionID)
		if err != nil {
			return err
		}
		if !ok || plan.ID != o.ID || plan.Document == nil || len(plan.Document.Checkpoints) == 0 {
			return store.ErrAutomationV2Conflict
		}
		cp := plan.Document.Checkpoints[0]
		attempt := cp.ID + ":attempt-1"
		if cp.RunID != o.RunID {
			lifecycle := sessions.NewPlanLifecycleService(h.runs.sessions)
			lifecycle.SetApplySessionMutation(h.apply)
			result, err := lifecycle.StartCheckpoint(sessions.PlanLifecycleExecutionInput{SessionID: o.SessionID, PlanID: plan.ID, CheckpointID: cp.ID, AttemptID: attempt, RunID: o.RunID, RunSessionID: o.SessionID, ParentSessionID: o.SessionID, StartedAt: time.Now().UnixMilli()})
			if err != nil {
				return err
			}
			attempt = result.AttemptID
		} else {
			attempt = cp.AttemptID
		}
		request := "av2-start:" + o.ID
		_, err = h.apply(sessions.SessionMutationInput{SessionID: o.SessionID, UserID: snapshot.UserID, AccountScopeID: snapshot.AccountScopeID, Kind: sessions.SessionMutationRecordRunIntent, ClientRequestID: request, IdempotencyKey: request, PayloadHash: request, RequestHash: request, EventType: "session.run_intent.recorded", RunIntent: &store.V3SessionRunIntent{RunID: o.RunID, Status: sessions.RunIntentPendingExecutor, PlanID: o.ID, CheckpointID: cp.ID, AttemptID: attempt, RunSessionID: o.SessionID, ParentSessionID: o.SessionID}, NowUnixMs: time.Now().UnixMilli()})
		if err != nil {
			return err
		}
		intent, exists, err = h.runs.sessions.GetSessionRunIntent(o.SessionID, o.RunID)
		if err != nil {
			return err
		}
		if !exists {
			return errors.New("automation v2 run intent unavailable")
		}
	}
	if intent.Status == sessions.RunIntentPendingExecutor {
		p := identity.Principal{Type: identity.PrincipalTypeUser, UserID: snapshot.UserID, AccountScopeID: snapshot.AccountScopeID, AccountScopeSource: identity.AccountScopeSourceServerState}
		if !h.enqueue(p, intent) {
			return errors.New("automation v2 executor wake unavailable; intent retained")
		}
	}
	return nil
}
func (h *AutomationV2ExecutionHost) Cancel(ctx context.Context, o store.AutomationV2Occurrence) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	snapshot, found, err := h.current(o)
	if err != nil || !found {
		return err
	}
	intent, found, err := h.runs.sessions.GetSessionActiveRunIntent(o.SessionID)
	if err != nil || !found {
		return err
	}
	if intent.PlanID != o.ID {
		return store.ErrAutomationV2Conflict
	}
	if intent.Status != sessions.RunIntentPendingExecutor && intent.Status != sessions.RunIntentRunning && intent.Status != sessions.RunIntentCancelled {
		return nil
	}
	intent.Status = sessions.RunIntentCancelled
	request := "av2-cancel:" + o.ID + ":" + intent.RunID
	_, err = h.apply(sessions.SessionMutationInput{SessionID: o.SessionID, UserID: snapshot.UserID, AccountScopeID: snapshot.AccountScopeID, Kind: sessions.SessionMutationRecordRunIntent, ClientRequestID: request, IdempotencyKey: request, PayloadHash: request, RequestHash: request, EventType: "session.run_intent.updated", RunIntent: &intent, NowUnixMs: time.Now().UnixMilli()})
	if err != nil {
		return err
	}
	lifecycle := sessions.NewPlanLifecycleService(h.runs.sessions)
	lifecycle.SetApplySessionMutation(h.apply)
	_, _, err = lifecycle.ReconcileCancelledRun(sessions.PlanLifecycleExecutionInput{SessionID: o.SessionID, PlanID: o.ID, CheckpointID: intent.CheckpointID, AttemptID: intent.AttemptID, RunID: intent.RunID, RunSessionID: o.SessionID, ParentSessionID: o.SessionID, Notes: "Automation V2 cancelled", ReviewedAt: time.Now().UnixMilli()})
	if err != nil {
		return err
	}
	err = h.runs.StopSessionRun(o.SessionID, intent.RunID, "Automation V2 cancelled")
	if errors.Is(err, ErrSessionRunNotActive) {
		return nil
	}
	return err
}

type automationRunClosing struct {
	closingState string
	summary      string
	detail       string
	deliverables []store.SessionPlanArtifactReference
	report       string
	result       string
}

func normalizeClosingState(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "-", "_")
	s = strings.ReplaceAll(s, " ", "_")
	switch s {
	case "routine_clean", "routine", "clean":
		return "routine_clean"
	case "deliverable_ready", "deliverable", "deliverables":
		return "deliverable_ready"
	case "attention_alert", "alert", "attention", "warning":
		return "attention_alert"
	case "blocked", "block":
		return "blocked"
	default:
		return ""
	}
}

func extractAutomationRunClosing(doc *store.SessionPlanDocument, execSummary sessions.PlanExecutionSummary, allCompleted bool) automationRunClosing {
	var targetCP *store.SessionPlanCheckpoint
	deliverables := append([]store.SessionPlanArtifactReference(nil), doc.Artifacts...)
	for i := range doc.Checkpoints {
		cp := &doc.Checkpoints[i]
		deliverables = append(deliverables, cp.Artifacts...)
		if targetCP == nil || cp.Status == "completed" || cp.Status == "failed" || cp.Status == "blocked" || cp.Status == "in_progress" {
			targetCP = cp
		}
	}

	// 1. Explicit closing state or fallback
	closingState := ""
	if targetCP != nil && targetCP.ClosingState != "" {
		closingState = normalizeClosingState(targetCP.ClosingState)
	}
	if closingState == "" {
		if execSummary.Failed || (targetCP != nil && targetCP.Status == "failed") {
			closingState = "attention_alert"
		} else if execSummary.Blocked || (targetCP != nil && targetCP.Status == "blocked") {
			closingState = "blocked"
		} else if len(deliverables) > 0 {
			closingState = "deliverable_ready"
		} else if allCompleted {
			closingState = "routine_clean"
		}
	}

	// 2. Explicit summary or fallback
	summary := ""
	if targetCP != nil && strings.TrimSpace(targetCP.Summary) != "" {
		summary = strings.TrimSpace(targetCP.Summary)
	}
	if summary == "" && targetCP != nil {
		if targetCP.Handoff != nil && strings.TrimSpace(targetCP.Handoff.Overview) != "" {
			summary = strings.TrimSpace(targetCP.Handoff.Overview)
		} else if strings.TrimSpace(targetCP.Result) != "" && !strings.EqualFold(targetCP.Result, "done") {
			summary = strings.TrimSpace(targetCP.Result)
		} else if strings.TrimSpace(targetCP.Report) != "" {
			line := strings.TrimSpace(strings.Split(targetCP.Report, "\n")[0])
			line = strings.TrimLeft(line, "# -*")
			if len(line) > 120 {
				line = line[:120]
			}
			summary = strings.TrimSpace(line)
		}
	}
	if summary == "" {
		switch closingState {
		case "routine_clean":
			summary = "Clean run · All good"
		case "deliverable_ready":
			summary = "Deliverable ready"
		case "attention_alert":
			summary = "Attention needed"
		case "blocked":
			summary = "Execution blocked"
		}
	}

	// 3. Detail mapping to calm one-liner or alert badge text
	var detail string
	switch closingState {
	case "routine_clean":
		if summary != "" {
			detail = summary
		} else {
			detail = "All checkpoints completed · All good"
		}
	case "deliverable_ready":
		if strings.Contains(strings.ToLower(summary), "deliverable") || strings.Contains(strings.ToLower(summary), "report") {
			detail = summary
		} else {
			detail = "Deliverable ready: " + summary
		}
	case "attention_alert":
		if strings.HasPrefix(strings.ToLower(summary), "alert") {
			detail = summary
		} else {
			detail = "Alert · " + summary
		}
	case "blocked":
		if strings.HasPrefix(strings.ToLower(summary), "blocked") {
			detail = summary
		} else {
			detail = "Blocked: " + summary
		}
	default:
		detail = summary
	}

	report := ""
	result := ""
	if targetCP != nil {
		report = targetCP.Report
		result = targetCP.Result
	}

	return automationRunClosing{
		closingState: closingState,
		summary:      summary,
		detail:       detail,
		deliverables: deliverables,
		report:       report,
		result:       result,
	}
}

// Outcome is observed from canonical checkpoint state, never from a successful
// wake or a completed provider turn. Missing evidence stays explicitly unavailable.
func (h *AutomationV2ExecutionHost) Outcome(o store.AutomationV2Occurrence) (string, string, error) {
	_, found, err := h.current(o)
	if err != nil {
		return "unavailable", "session identity unavailable", err
	}
	if !found {
		return "admitted", "execution session not yet prepared", nil
	}
	plan, found, err := h.runs.sessions.GetActivePlan(o.SessionID)
	if err != nil {
		return "unavailable", "plan read failed", err
	}
	if !found {
		return "admitted", "plan not yet installed", nil
	}
	if plan.ID != o.ID || plan.Document == nil {
		return "unavailable", "canonical plan unavailable", store.ErrAutomationV2Conflict
	}
	summary := sessions.SummarizePlanExecution(plan.Document)
	allCompleted := len(plan.Document.Checkpoints) > 0
	for _, cp := range plan.Document.Checkpoints {
		if cp.Status != "completed" {
			allCompleted = false
		}
	}
	if summary.Failed {
		closing := extractAutomationRunClosing(plan.Document, summary, allCompleted)
		_ = h.repository.PersistAutomationV2ClosingState(o, closing.closingState, closing.summary, closing.deliverables, closing.report, closing.result, closing.detail, time.Now().UnixMilli())
		detail := closing.detail
		if detail == "" {
			detail = "canonical checkpoint failed"
		}
		return "failed", detail, nil
	}
	if allCompleted {
		closing := extractAutomationRunClosing(plan.Document, summary, allCompleted)
		_ = h.repository.PersistAutomationV2ClosingState(o, closing.closingState, closing.summary, closing.deliverables, closing.report, closing.result, closing.detail, time.Now().UnixMilli())
		detail := closing.detail
		if detail == "" {
			detail = "all canonical checkpoints completed; user review is separate"
		}
		return "succeeded", detail, nil
	}
	intent, found, err := h.runs.sessions.GetSessionActiveRunIntent(o.SessionID)
	if err != nil {
		return "unavailable", "run evidence unavailable", err
	}
	if found && intent.PlanID == o.ID {
		switch intent.Status {
		case sessions.RunIntentRunning:
			return "running", "canonical executor running", nil
		case sessions.RunIntentCancelled:
			return "cancelled", "canonical run cancelled", nil
		case sessions.RunIntentPendingExecutor:
			return "admitted", "awaiting executor", nil
		}
	}
	if summary.Blocked || summary.Paused || summary.ReviewRequired {
		closing := extractAutomationRunClosing(plan.Document, summary, allCompleted)
		if closing.closingState != "" {
			_ = h.repository.PersistAutomationV2ClosingState(o, closing.closingState, closing.summary, closing.deliverables, closing.report, closing.result, closing.detail, time.Now().UnixMilli())
		}
		return "unavailable", "checkpoint awaits resolution or review", nil
	}
	return "unavailable", "no active execution evidence", nil
}

// Recovered executor turns and tool calls recheck the immutable creation event,
// durable occurrence and cancellation fence. Mutable display metadata is never
// a grant, and cancellation also fences later canonical checkpoint turns.
func (s *Service) validateAutomationV2Execution(current store.SessionSnapshot) error {
	events, err := s.sessions.ListSessionEvents(current.ID, 0, 1)
	if err != nil {
		return err
	}
	if len(events) != 1 || events[0].EventType != "session.created" {
		return store.ErrAutomationV2Conflict
	}
	var payload struct {
		Session *store.SessionSnapshot `json:"session"`
	}
	if json.Unmarshal(events[0].Payload, &payload) != nil || payload.Session == nil {
		return store.ErrAutomationV2Conflict
	}
	pinned := payload.Session
	if !reflect.DeepEqual(current.WorkspaceGrants, pinned.WorkspaceGrants) {
		return store.ErrAutomationV2Conflict
	}
	for _, key := range []string{"swarm_v3_runtime_kind", "swarm_v3_runtime_swarm_id", "swarm_v3_authority_host_swarm_id", "swarm_v3_runtime_workspace_path", "automation_v2_occurrence_id", "automation_v2_authoring_session_id", "automation_v2_digest", "navigation_hidden", store.SessionPurposeMetadataKey} {
		if !reflect.DeepEqual(current.Metadata[key], pinned.Metadata[key]) {
			return store.ErrAutomationV2Conflict
		}
	}
	if current.ID != pinned.ID || current.AccountScopeID != pinned.AccountScopeID || current.UserID != pinned.UserID || current.WorktreeRootPath != pinned.WorktreeRootPath || current.WorkspacePath != pinned.WorkspacePath || !current.WorktreeEnabled {
		return store.ErrAutomationV2Conflict
	}
	id := mapString(pinned.Metadata, "automation_v2_occurrence_id")
	author := mapString(pinned.Metadata, "automation_v2_authoring_session_id")
	workspaceID := ""
	for _, g := range pinned.WorkspaceGrants {
		if g.Kind == store.WorkspaceGrantPrimary {
			workspaceID = g.WorkspaceID
		}
	}
	o, found, err := s.sessions.Store().GetAutomationV2Occurrence(pinned.AccountScopeID, pinned.UserID, workspaceID, author, id)
	if err != nil {
		return err
	}
	if !found || o.SessionID != current.ID || o.Record.Digest != mapString(pinned.Metadata, "automation_v2_digest") || o.State == "cancelled" {
		return store.ErrAutomationV2Conflict
	}
	r, found, err := s.sessions.GetAutomationV2Record(pinned.AccountScopeID, pinned.UserID, workspaceID, author)
	if err != nil {
		return err
	}
	if !found || int64(o.Record.Generation) <= r.CancelThrough {
		return store.ErrAutomationV2Conflict
	}
	return nil
}

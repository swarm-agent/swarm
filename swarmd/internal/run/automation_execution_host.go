package run

import (
	"context"
	"errors"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/automation"
	"swarm/packages/swarmd/internal/identity"
	sessions "swarm/packages/swarmd/internal/session"
	store "swarm/packages/swarmd/internal/store/pebble"
)

// AutomationExecutionHost admits durable intents; the existing V3 executor owns
// dispatch and recovery. apply must be the runtime's canonical mutation publisher.
type AutomationExecutionHost struct {
	runs  *Service
	store *store.SessionStore
	apply func(sessions.SessionMutationInput) (sessions.SessionMutationResult, error)
}

var _ automation.V3ExecutionHost = (*AutomationExecutionHost)(nil)

func NewAutomationExecutionHost(runs *Service, repository *store.SessionStore, apply func(sessions.SessionMutationInput) (sessions.SessionMutationResult, error)) (*AutomationExecutionHost, error) {
	if runs == nil || runs.sessions == nil || repository == nil || apply == nil {
		return nil, automation.ErrInvalid
	}
	return &AutomationExecutionHost{runs: runs, store: repository, apply: apply}, nil
}

func (h *AutomationExecutionHost) Prepare(ctx context.Context, p automation.Principal, def store.AutomationRecord) (store.SessionSnapshot, error) {
	var empty store.SessionSnapshot
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	s := h.runs
	if p.AccountID == "" || p.SubjectID == "" || p.AccountID != def.Scope.AccountID || def.Definition == nil {
		return empty, automation.ErrDenied
	}
	if err := automation.ValidateExecutionPolicy(def.Definition.Authorization); err != nil {
		return empty, err
	}
	if s.workspace == nil || s.agents == nil || s.agentModelSettings == nil || s.sessionDeployCanonicalize == nil {
		return empty, errors.New("automation preparation authorities are not configured")
	}
	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: p.SubjectID, AccountScopeID: p.AccountID, AccountScopeSource: identity.AccountScopeSourceServerState}
	entry, found, err := s.workspace.GetByWorkspaceIDForPrincipal(principal, def.Scope.WorkspaceID)
	if err != nil {
		return empty, err
	}
	if !found {
		return empty, automation.ErrDenied
	}
	profile, err := s.agents.ResolveSystemAgent("swarm", store.AgentProfile{})
	if err != nil {
		return empty, err
	}
	_, _, err = s.CompileStoredV3AgentToolContract(p.AccountID, profile)
	if err != nil {
		return empty, err
	}
	settings, err := s.agentModelSettings.GetForAccount(p.AccountID)
	if err != nil {
		return empty, err
	}
	selection := func(a store.AgentModelAssignment) store.ModelProfileSelection {
		return store.ModelProfileSelection{Provider: a.Provider, Model: a.Model, Thinking: a.Thinking, ServiceTier: a.ServiceTier, ContextMode: a.ContextMode}
	}
	plan := selection(settings.Swarm.Plan)
	model := &store.SessionModelProfileSnapshot{Source: store.SessionModelProfileSourceSwarmSettings, UseAccountDefault: true, Action: selection(settings.Swarm.Action), Plan: &plan, AppliedAt: time.Now().UnixMilli()}
	canonical, err := s.sessionDeployCanonicalize(SessionDeployCanonicalizeInput{Principal: principal, WorkspacePath: entry.Path, AgentProfile: profile, ModelProfile: model, RuntimeMode: store.AgentRuntimeModePlanAuto, Metadata: map[string]any{}})
	if err != nil {
		return empty, err
	}
	if canonical.SourceWorkspaceID != def.Scope.WorkspaceID || canonical.Metadata == nil {
		return empty, automation.ErrDenied
	}
	if err := automationTargetPolicy(def.Definition.Authorization, canonical.Metadata); err != nil {
		return empty, err
	}
	known := map[string]bool{}
	for _, definition := range s.ListAgentToolDefinitionsForAccount(p.AccountID) {
		known[definition.Name] = true
	}
	for _, name := range def.Definition.Authorization.AllowedTools {
		if !known[name] || !automationToolPermitted(&def.Definition.Authorization, name) {
			return empty, automation.ErrDenied
		}
	}
	available := true
	grants := []store.WorkspaceGrant{{Kind: store.WorkspaceGrantPrimary, WorkspaceID: canonical.SourceWorkspaceID, WorkspaceGeneration: canonical.SourceWorkspaceGeneration, Path: canonical.SourceWorkspacePath, Name: canonical.SourceWorkspaceName, Available: &available}}
	pref, err := manageSessionsDeployModelProfilePreference(model, sessions.ModePlan)
	if err != nil {
		return empty, err
	}
	return store.SessionSnapshot{UserID: p.SubjectID, AccountScopeID: p.AccountID, WorkspacePath: canonical.SourceWorkspacePath, WorkspaceName: canonical.SourceWorkspaceName, Title: def.Definition.Name, Mode: sessions.ModePlan, Preference: pref, ModelProfile: model, Metadata: canonical.Metadata, WorkspaceGrants: grants, WorkspaceUsage: store.WorkspaceUsageFromGrants(grants)}, nil
}

func (h *AutomationExecutionHost) current(snapshot store.SessionSnapshot, key string) (store.SessionSnapshot, bool, error) {
	if snapshot.ID != "automation-"+key || strings.TrimSpace(snapshot.AccountScopeID) == "" {
		return store.SessionSnapshot{}, false, automation.ErrDenied
	}
	current, found, err := h.runs.sessions.GetSession(snapshot.ID)
	if err != nil {
		return current, found, err
	}
	if found && (current.AccountScopeID != snapshot.AccountScopeID || current.Metadata["automation_execution_key"] != key || !current.WorktreeEnabled) {
		return current, false, automation.ErrDenied
	}
	return current, found, nil
}

func (h *AutomationExecutionHost) Start(ctx context.Context, snapshot store.SessionSnapshot, key string) error {
	return h.store.WithAutomationExecutionFence(snapshot.AccountScopeID, key, false, func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		current, found, err := h.current(snapshot, key)
		if err != nil {
			return err
		}
		if !found {
			return automation.ErrNotFound
		}
		if _, err := h.runs.automationPolicy(current.ID); err != nil {
			return err
		}
		runID := "automation-run:" + key
		_, exists, err := h.runs.sessions.GetSessionRunIntent(current.ID, runID)
		if err != nil || exists {
			return err
		}
		plan, ok, err := h.runs.sessions.GetActivePlan(current.ID)
		if err != nil {
			return err
		}
		if !ok || plan.Document == nil || len(plan.Document.Checkpoints) == 0 {
			return automation.ErrInvalid
		}
		cp := plan.Document.Checkpoints[0]
		attempt := cp.ID + ":attempt-1"
		// Recover the exact activation after an ambiguous intent write, never
		// advance to another checkpoint or generate a second attempt.
		if cp.RunID != runID {
			result, err := sessions.NewPlanLifecycleService(h.runs.sessions).StartCheckpoint(sessions.PlanLifecycleExecutionInput{SessionID: current.ID, PlanID: plan.ID, CheckpointID: cp.ID, AttemptID: attempt, RunID: runID, RunSessionID: current.ID, ParentSessionID: current.ID, StartedAt: time.Now().UnixMilli()})
			if err != nil {
				return err
			}
			attempt = result.AttemptID
		} else {
			attempt = cp.AttemptID
		}
		request := "automation-start:" + key
		_, err = h.apply(sessions.SessionMutationInput{SessionID: current.ID, UserID: current.UserID, AccountScopeID: current.AccountScopeID, ClientRequestID: request, IdempotencyKey: request, PayloadHash: request, RequestHash: request, Kind: sessions.SessionMutationRecordRunIntent, EventType: "session.run_intent.recorded", RunIntent: &store.V3SessionRunIntent{RunID: runID, Status: sessions.RunIntentPendingExecutor, PlanID: plan.ID, CheckpointID: cp.ID, AttemptID: attempt, RunSessionID: current.ID, ParentSessionID: current.ID}, NowUnixMs: time.Now().UnixMilli()})
		return err
	})
}

func (h *AutomationExecutionHost) Cancel(ctx context.Context, snapshot store.SessionSnapshot, key string) error {
	// Validate identity before creating even a cancellation tombstone.
	if _, _, err := h.current(snapshot, key); err != nil {
		return err
	}
	return h.store.WithAutomationExecutionFence(snapshot.AccountScopeID, key, true, func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		current, found, err := h.current(snapshot, key)
		if err != nil || !found {
			return err
		}
		intent, found, err := h.runs.sessions.GetSessionActiveRunIntent(current.ID)
		if err != nil {
			return err
		}
		if !found {
			plan, ok, planErr := h.runs.sessions.GetActivePlan(current.ID)
			if planErr != nil {
				return planErr
			}
			runID := "automation-run:" + key
			if ok && plan.Document != nil && plan.Document.ExecutionState != nil && plan.Document.ExecutionState.CurrentRunID != "" {
				runID = plan.Document.ExecutionState.CurrentRunID
			}
			intent, found, err = h.runs.sessions.GetSessionRunIntent(current.ID, runID)
		}
		if err != nil || !found {
			return err
		}
		if intent.Status != sessions.RunIntentPendingExecutor && intent.Status != sessions.RunIntentRunning && intent.Status != sessions.RunIntentCancelled {
			return nil
		}
		intent.Status = sessions.RunIntentCancelled
		request := "automation-cancel:" + key
		_, err = h.apply(sessions.SessionMutationInput{SessionID: current.ID, UserID: current.UserID, AccountScopeID: current.AccountScopeID, ClientRequestID: request, IdempotencyKey: request, PayloadHash: request, RequestHash: request, Kind: sessions.SessionMutationRecordRunIntent, EventType: "session.run_intent.updated", RunIntent: &intent, NowUnixMs: time.Now().UnixMilli()})
		if err != nil {
			return err
		}
		_, _, err = sessions.NewPlanLifecycleService(h.runs.sessions).ReconcileCancelledRun(sessions.PlanLifecycleExecutionInput{SessionID: current.ID, PlanID: intent.PlanID, CheckpointID: intent.CheckpointID, AttemptID: intent.AttemptID, RunID: intent.RunID, RunSessionID: current.ID, ParentSessionID: current.ID, Notes: "Automation cancelled", ReviewedAt: time.Now().UnixMilli()})
		if err != nil {
			return err
		}
		err = h.runs.StopSessionRun(current.ID, intent.RunID, "Automation cancelled")
		if errors.Is(err, ErrSessionRunNotActive) {
			return nil
		}
		return err
	})
}

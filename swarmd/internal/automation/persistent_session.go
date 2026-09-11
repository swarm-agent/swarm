package automation

import (
	"context"
	sessions "swarm/packages/swarmd/internal/session"
	store "swarm/packages/swarmd/internal/store/pebble"
	"time"
)

// ConfigureSessionAcceptance is startup-only. Only ApproveUser invokes it after
// authenticating an explicit user and the exact displayed definition digest.
func (a *PolicyApproval) ConfigureSessionAcceptance(v *V3Runtime) { a.acceptSession = v.acceptSession }

func (v *V3Runtime) acceptSession(ctx context.Context, p Principal, def store.AutomationRecord) error {
	if p.Role != "user" || def.Definition == nil || def.Definition.SessionID == "" {
		return ErrDenied
	}
	snapshot, found, err := v.sessions.GetSession(def.Definition.SessionID)
	if err != nil {
		return err
	}
	if !found || snapshot.AccountScopeID != p.AccountID || snapshot.UserID != p.SubjectID || !snapshot.WorktreeEnabled {
		return ErrDenied
	}
	if err := v.worktrees.ValidateSessionRepositoryLaneForRead(snapshot.WorkspacePath, snapshot.WorktreeRootPath, snapshot.ID, snapshot.WorktreeBranch); err != nil {
		return err
	}
	binding := &store.SessionAutomationBinding{AutomationID: def.AutomationID, WorkspaceID: def.Scope.WorkspaceID, Policy: def.Definition.Authorization}
	if snapshot.Automation != nil {
		if snapshot.Automation.AutomationID != binding.AutomationID || snapshot.Automation.WorkspaceID != binding.WorkspaceID {
			return ErrDenied
		}
		// The binding is permanent; fresh approval changes the next occurrence policy,
		// never the policy underneath an existing execution.
		return nil
	}
	request := executionKey("accept-session", def.Scope, def.AutomationID, def.Revision, snapshot.ID)
	_, err = v.apply(sessions.SessionMutationInput{SessionID: snapshot.ID, UserID: snapshot.UserID, AccountScopeID: p.AccountID, Kind: sessions.SessionMutationUpdateMetadata, EventType: "session.automation.accepted", ClientRequestID: request, IdempotencyKey: request, PayloadHash: request, RequestHash: request, AutomationBinding: binding, AutomationDefinitionRevision: def.Revision, NowUnixMs: time.Now().UnixMilli()})
	return err
}

func (v *V3Runtime) ensurePersistent(ctx context.Context, p Principal, def, occurrence store.AutomationRecord) (string, error) {
	id := def.Definition.SessionID
	snapshot, found, err := v.sessions.GetSession(id)
	if err != nil {
		return "", err
	}
	if !found || snapshot.AccountScopeID != p.AccountID || snapshot.Automation == nil || snapshot.Automation.AutomationID != def.AutomationID || snapshot.Automation.WorkspaceID != def.Scope.WorkspaceID || !snapshot.WorktreeEnabled {
		return "", ErrDenied
	}
	if err := v.worktrees.ValidateSessionRepositoryLaneForRead(snapshot.WorkspacePath, snapshot.WorktreeRootPath, id, snapshot.WorktreeBranch); err != nil {
		return "", err
	}
	// Reuse canonical model/tool/target admission without allocating its template.
	if _, err := v.host.Prepare(ctx, p, def); err != nil {
		return "", err
	}
	key := executionKey(def.Scope, def.AutomationID, occurrence.ID)
	planID := "automation-plan:" + key
	doc, err := v.pinnedDocument(ctx, p, def, planID)
	if err != nil {
		return "", err
	}
	binding := &store.SessionAutomationBinding{AutomationID: def.AutomationID, WorkspaceID: def.Scope.WorkspaceID, Policy: def.Definition.Authorization, OccurrenceID: occurrence.ID, ExecutionKey: key, PlanID: planID}
	request := "automation-reserve:" + key
	if snapshot.Automation.ExecutionKey != key {
		_, err = v.apply(sessions.SessionMutationInput{SessionID: id, UserID: snapshot.UserID, AccountScopeID: p.AccountID, Kind: sessions.SessionMutationUpdateMetadata, EventType: "session.automation.reserved", ClientRequestID: request, IdempotencyKey: request, PayloadHash: request, RequestHash: request, AutomationBinding: binding, AutomationDefinitionRevision: def.Revision, NowUnixMs: time.Now().UnixMilli()})
		if err != nil {
			return "", err
		}
	}
	// Occurrence-specific plans preserve all earlier plan revisions and results.
	if _, exists, err := v.sessions.GetPlan(id, planID); err != nil {
		return "", err
	} else if !exists {
		doc.Status = "approved"
		if _, err := sessions.ApplyPlanAcceptanceExecutionPolicy(doc, sessions.PlanAcceptanceExecutionOptions{}); err != nil {
			return "", err
		}
		now := time.Now().UnixMilli()
		plan := store.SessionPlanSnapshot{ID: planID, SessionID: id, UserID: snapshot.UserID, AccountScopeID: p.AccountID, Title: doc.Title, Plan: "# " + doc.Title, Status: "approved", ApprovalState: "approved", Active: true, Version: 1, CreatedAt: now, UpdatedAt: now, Document: doc}
		request = "automation-plan-install:" + key
		_, err = v.apply(sessions.SessionMutationInput{SessionID: id, UserID: snapshot.UserID, AccountScopeID: p.AccountID, Kind: sessions.SessionMutationSavePlan, ClientRequestID: request, IdempotencyKey: request, PayloadHash: request, RequestHash: request, PlanSave: &store.V3PlanSaveMutation{Plan: plan, Activate: true}, NowUnixMs: now})
		if err != nil {
			return "", err
		}
	}
	snapshot, found, err = v.sessions.GetSession(id)
	if err != nil {
		return "", err
	}
	if !found {
		return "", ErrNotFound
	}
	if err := v.approval.Verify(ctx, p, def); err != nil {
		return "", err
	}
	claims, ok := v.domain.repo.(interface {
		ClaimAutomationDispatch(store.AutomationScope, string, string) error
	})
	if !ok {
		return "", ErrInvalid
	}
	if err := claims.ClaimAutomationDispatch(def.Scope, def.AutomationID, occurrence.ID); err != nil {
		return "", err
	}
	if err := v.host.Start(ctx, snapshot, key); err != nil {
		return "", err
	}
	return id, nil
}

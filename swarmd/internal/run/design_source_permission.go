package run

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/permission"
	store "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// Every captured source requires an account-scoped sharing decision. Submission
// approval and default read allowance are not sharing authority.
func (s *Service) withDesignSourcePermission(ctx context.Context, bound tool.WorkspaceScope, runID string, emit StreamHandler) context.Context {
	return tool.WithDesignSourceReadAuthorizer(ctx, func(ctx context.Context, scope tool.WorkspaceScope, path string) error {
		denied := errors.New("design source sensitive read denied")
		if err := ctx.Err(); err != nil {
			return err
		}
		principal, ok := identity.PrincipalFromContext(ctx)
		if !ok || principal.AccountScopeID == "" || principal.UserID == "" || runID == "" || bound.SessionID == "" || scope.SessionID != bound.SessionID || scope.PrimaryPath != bound.PrimaryPath || !reflect.DeepEqual(scope.Roots, bound.Roots) || !reflect.DeepEqual(scope.Principal, bound.Principal) || principal.AccountScopeID != bound.Principal.AccountScopeID || principal.UserID != bound.Principal.UserID || (principal.SessionID != "" && principal.SessionID != bound.SessionID) || s.permissions == nil || s.sessions == nil {
			return denied
		}
		session, found, err := s.sessions.GetSession(bound.SessionID)
		if err != nil {
			return err
		}
		if !found || session.AccountScopeID != principal.AccountScopeID || session.UserID != principal.UserID {
			return denied
		}
		intent, found, err := s.sessions.Store().GetV3SessionRunIntent(bound.SessionID, runID)
		if err != nil {
			return err
		}
		if !found || intent.AccountScopeID != principal.AccountScopeID || intent.UserID != principal.UserID {
			return denied
		}
		inside := false
		for _, root := range append([]string{bound.PrimaryPath}, bound.Roots...) {
			if root == "" || !filepath.IsAbs(root) || !filepath.IsAbs(path) {
				continue
			}
			rel, err := filepath.Rel(root, path)
			if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				inside = true
				break
			}
		}
		if !inside {
			return denied
		}
		args, err := json.Marshal(map[string]any{"path": path, "critical": true, "purpose": "Capture source bytes for delegated Designer provider context"})
		if err != nil {
			return err
		}
		decision, err := s.permissions.ExplainDesignSourceRead(principal.AccountScopeID, string(args))
		if err != nil {
			return err
		}
		switch decision.Decision {
		case permission.PolicyDecisionAllow:
			return ctx.Err()
		case permission.PolicyDecisionDeny:
			return denied
		}
		record, err := s.permissions.CreatePending(permission.CreateInput{SessionID: bound.SessionID, RunID: runID, ToolName: "read", ToolArguments: string(args), ToolCallArguments: string(args), Requirement: "design_source_sensitive_read", Mode: "ask"})
		if err != nil {
			return err
		}
		if emit != nil {
			emit(StreamEvent{Type: StreamEventPermissionReq, SessionID: bound.SessionID, Permission: &record})
		}
		resolved, err := s.permissions.WaitForResolution(ctx, bound.SessionID, record.ID)
		if err != nil {
			// Resolve only this capture's request, not unrelated waits in the run.
			cancelled, cancelErr := s.permissions.Resolve(bound.SessionID, record.ID, permission.ActionCancel, "source capture interrupted before permission resolution")
			if cancelErr == nil && emit != nil {
				emit(StreamEvent{Type: StreamEventPermissionUpdate, SessionID: bound.SessionID, Permission: &cancelled})
			}
			return errors.Join(err, cancelErr)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if emit != nil {
			emit(StreamEvent{Type: StreamEventPermissionUpdate, SessionID: bound.SessionID, Permission: &resolved})
		}
		if resolved.Status != store.PermissionStatusApproved {
			return denied
		}
		// Approval edits cannot silently retarget the already-open rooted source.
		if resolved.ApprovedArguments != "" && resolved.ApprovedArguments != "{}" && resolved.ApprovedArguments != string(args) {
			return denied
		}
		return nil
	})
}

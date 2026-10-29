package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"swarm/packages/swarmd/internal/fleet"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/permission"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// resolve_task_permission: the home answers one pending permission or
// question its task's run waits on, in the chat the person already uses.
//
//   - On a linked machine it goes over the existing link to the machine's own
//     route, POST /v3/sessions/{id}/permissions/{pid}/resolve, for the session
//     of a task this home sent there (its linked-task record). The session is
//     the one the machine reports for the task, never one named by the caller,
//     and the permission must be pending in it. linked.{name}.work gates it
//     (the permission service): off refuses, ask waits for the person, auto
//     runs.
//   - Locally it resolves the session of one of the home's own tasks, with
//     the same permission service calls as that route.
//
// Each decision covers that one request (allow once or deny once): a lasting
// rule is never saved through here.

// taskPermissionAction maps approve/deny to a one-time decision.
func taskPermissionAction(action string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "approve":
		return permission.ActionAllowOnce, nil
	case "deny":
		return permission.ActionDenyOnce, nil
	}
	return "", errors.New("action must be approve or deny")
}

// ResolveTaskPermission answers one pending request of a task's run: on
// machine (a task this home sent there), or of the home's own task when
// machine is empty.
func (s *Server) ResolveTaskPermission(ctx context.Context, p identity.Principal, machine, projectID, taskID, permissionID, action, reason string) (map[string]any, error) {
	if s == nil || s.sessions == nil {
		return nil, linkedTaskFail(http.StatusServiceUnavailable, "session service unavailable")
	}
	if !p.Valid() {
		return nil, linkedTaskFail(http.StatusUnauthorized, "%s", identity.ErrPrincipalRequired.Error())
	}
	machine, projectID, taskID, permissionID = strings.TrimSpace(machine), strings.TrimSpace(projectID), strings.TrimSpace(taskID), strings.TrimSpace(permissionID)
	if projectID == "" || taskID == "" || permissionID == "" {
		return nil, linkedTaskFail(http.StatusBadRequest, "project_id, task_id and permission_id are required")
	}
	if strings.ContainsAny(permissionID, "/\\?#%") {
		return nil, linkedTaskFail(http.StatusBadRequest, "invalid permission_id")
	}
	decision, err := taskPermissionAction(action)
	if err != nil {
		return nil, linkedTaskFail(http.StatusBadRequest, "%v", err)
	}
	if len([]rune(reason)) > permission.MaxReplyCharacters {
		return nil, linkedTaskFail(http.StatusBadRequest, "reason exceeds %d characters", permission.MaxReplyCharacters)
	}
	if machine == "" {
		return s.resolveLocalTaskPermission(p, projectID, taskID, permissionID, decision, reason)
	}
	return s.resolveLinkedTaskPermission(ctx, p, machine, projectID, taskID, permissionID, decision, reason)
}

func (s *Server) resolveLocalTaskPermission(p identity.Principal, projectID, taskID, permissionID, decision, reason string) (map[string]any, error) {
	if s.perm == nil {
		return nil, linkedTaskFail(http.StatusServiceUnavailable, "permission service is not configured")
	}
	task, found, err := s.sessions.Store().GetProjectTask(p.AccountScopeID, projectID, taskID)
	if err != nil {
		return nil, linkedTaskFail(http.StatusInternalServerError, "read task: %v", err)
	}
	if !found || task.ProjectID != projectID || (task.AccountID != "" && task.AccountID != p.AccountScopeID) {
		return nil, linkedTaskFail(http.StatusNotFound, "task %s not found in project %s", taskID, projectID)
	}
	sessionID := taskLiveSessionID(*task)
	if sessionID == "" {
		return nil, linkedTaskFail(http.StatusConflict, "task %s has no session yet", taskID)
	}
	if session, ok, err := s.sessions.GetSession(sessionID); err != nil || !ok || session.AccountScopeID != p.AccountScopeID || session.UserID != p.UserID {
		return nil, linkedTaskFail(http.StatusNotFound, "the session of task %s is not yours", taskID)
	}
	record, ok, err := s.perm.GetPermission(sessionID, permissionID)
	if err != nil {
		return nil, linkedTaskFail(http.StatusInternalServerError, "read permission: %v", err)
	}
	if !ok || record.Status != pebblestore.PermissionStatusPending {
		return nil, linkedTaskFail(http.StatusNotFound, "permission %s is not pending in the session of task %s; get_task shows its pending_request", permissionID, taskID)
	}
	if record.Requirement == permission.RequirementClientTool {
		return nil, linkedTaskFail(http.StatusConflict, "permission %s waits on an app's client tool, not a person", permissionID)
	}
	if err := s.validateProjectPermissionReply(p, sessionID, decision); err != nil {
		return nil, linkedTaskFail(http.StatusConflict, "%v", err)
	}
	resolved, _, err := s.perm.ResolveFromClient(sessionID, permissionID, decision, reason, "", "")
	if err != nil {
		return nil, linkedTaskFail(http.StatusConflict, "%v", err)
	}
	if _, _, err := s.publishSessionV3PermissionUpdatedFromRecord(p, sessionID, resolved); err != nil {
		return nil, linkedTaskFail(http.StatusInternalServerError, "the request was answered, but publishing it failed: %v", err)
	}
	s.settleClientToolConfirmationAfterAnswer(sessionID, resolved)
	return map[string]any{"project_id": projectID, "task_id": taskID, "session_id": sessionID, "permission": taskPermissionView(resolved)}, nil
}

func (s *Server) resolveLinkedTaskPermission(ctx context.Context, p identity.Principal, machine, projectID, taskID, permissionID, decision, reason string) (map[string]any, error) {
	if s.fleet == nil || s.fleetTokens == nil {
		return nil, linkedTaskFail(http.StatusServiceUnavailable, "fleet service unavailable")
	}
	access, err := s.sessions.Store().GetAccountPermissions(p.AccountScopeID)
	if err != nil {
		return nil, linkedTaskFail(http.StatusInternalServerError, "read permissions: %v", err)
	}
	// The permission service decides first; the rule is read again here so a
	// call that reaches the service by another path is refused the same way.
	if !pebblestore.LinkedWorkOn(access.Link(machine).Work) {
		return nil, linkedTaskFail(http.StatusForbidden, "linked work for machine %q is off; the person turns it on in the app's Permissions page", machine)
	}
	// Only a task this home sent to the machine.
	if _, sent, err := s.fleet.GetLinkedTask(p.AccountScopeID, machine, projectID, taskID); err != nil {
		return nil, linkedTaskFail(http.StatusInternalServerError, "read linked task: %v", err)
	} else if !sent {
		return nil, linkedTaskFail(http.StatusForbidden, "task %s on %s was not sent from this home; answer its requests on that machine", taskID, machine)
	}
	sessionID, err := s.linkedTaskSession(ctx, p, machine, projectID, taskID)
	if err != nil {
		return nil, err
	}
	client, linked, status, err := s.fleetMachineClient(p.AccountScopeID, machine)
	if err != nil {
		if status == 0 {
			status = http.StatusBadGateway
		}
		return nil, linkedTaskFail(status, "%v", err)
	}
	base := "/v3/sessions/" + url.PathEscape(sessionID) + "/permissions"
	// The permission must be pending in the task's own session.
	var pending struct {
		Permissions []pebblestore.PermissionRecord `json:"permissions"`
	}
	err = client.JSON(ctx, http.MethodGet, base+"?status=pending&limit=200", nil, &pending)
	s.noteFleetContact(linked, err)
	if err != nil {
		return nil, linkedFleetCallError(machine, linked, "the pending request read", err)
	}
	var target *pebblestore.PermissionRecord
	for i := range pending.Permissions {
		if pending.Permissions[i].ID == permissionID && pending.Permissions[i].SessionID == sessionID {
			target = &pending.Permissions[i]
			break
		}
	}
	if target == nil {
		return nil, linkedTaskFail(http.StatusNotFound, "permission %s is not pending in the session of task %s on %s; get_task shows its pending_request", permissionID, taskID, machine)
	}
	if target.Requirement == permission.RequirementClientTool {
		return nil, linkedTaskFail(http.StatusConflict, "permission %s waits on an app's client tool, not a person", permissionID)
	}
	var answer struct {
		Permission pebblestore.PermissionRecord `json:"permission"`
	}
	err = client.JSON(ctx, http.MethodPost, base+"/"+url.PathEscape(permissionID)+"/resolve", map[string]any{"action": decision, "reason": reason}, &answer)
	s.noteFleetContact(linked, err)
	if err != nil {
		return nil, linkedFleetCallError(machine, linked, "the answer", err)
	}
	return map[string]any{"machine": machine, "project_id": projectID, "task_id": taskID, "session_id": sessionID, "permission": taskPermissionView(answer.Permission), "untrusted_content": true}, nil
}

// linkedTaskSession is the session of a linked task's current attempt, as
// the machine reports it through the link (get_task).
func (s *Server) linkedTaskSession(ctx context.Context, p identity.Principal, machine, projectID, taskID string) (string, error) {
	raw, err := s.CallFleetProject(ctx, p, machine, "get_task", map[string]any{"project_id": projectID, "task_id": taskID})
	if err != nil {
		var refused *fleet.Error
		if errors.As(err, &refused) && refused.Status == http.StatusNotFound {
			return "", linkedTaskFail(http.StatusNotFound, "task %s not found on %s", taskID, machine)
		}
		return "", linkedTaskFail(http.StatusBadGateway, "%v", err)
	}
	var answer struct {
		Task pebblestore.ProjectTaskRecord `json:"task"`
	}
	if json.Unmarshal(raw, &answer) != nil || answer.Task.ID != taskID || (answer.Task.ProjectID != "" && answer.Task.ProjectID != projectID) {
		return "", linkedTaskFail(http.StatusBadGateway, "%s returned an unexpected record for task %s", machine, taskID)
	}
	sessionID := linkedTaskObservation(answer.Task).RemoteSessionID
	if sessionID == "" || strings.ContainsAny(sessionID, "/\\?#%") {
		return "", linkedTaskFail(http.StatusConflict, "task %s on %s has no session yet", taskID, machine)
	}
	return sessionID, nil
}

// linkedFleetCallError is a failed link call: the machine's own refusal, or
// that it could not be reached.
func linkedFleetCallError(machine string, linked pebblestore.FleetMachine, what string, err error) error {
	var refused *fleet.Error
	if errors.As(err, &refused) {
		if refused.Status == http.StatusNotFound {
			return linkedTaskFail(http.StatusNotFound, "%s refused %s: %v", machine, what, refused)
		}
		return linkedTaskFail(http.StatusBadGateway, "%s refused %s: %v", machine, what, refused)
	}
	return linkedTaskFail(http.StatusBadGateway, "%s", fleetUnreachableError(machine, linked).Error())
}

// taskPermissionView is what the caller sees of an answered request.
func taskPermissionView(record pebblestore.PermissionRecord) map[string]any {
	return map[string]any{"id": record.ID, "status": record.Status, "decision": record.Decision, "tool_name": record.ToolName}
}

// TaskPermissionResolver is the tool's view of this service.
var _ tool.TaskPermissionResolver = (*Server)(nil)

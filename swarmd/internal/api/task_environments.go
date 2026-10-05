package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"swarm-refactor/swarmtui/pkg/environments"
	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/environments/lifecycle"
	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

func (s *Server) authorizeTaskEnvironment(p identity.Principal, sessionID, projectID, taskID string) (*pebblestore.ProjectTaskRecord, error) {
	if !p.Valid() || s.sessions == nil || projectID == "" || taskID == "" || (p.SessionID != "" && p.SessionID != sessionID) {
		return nil, errors.New("authenticated exact task identity required")
	}
	task, found, err := s.sessions.Store().GetProjectTask(p.AccountScopeID, projectID, taskID)
	if err != nil || !found || task.Archived { return nil, errors.New("task unavailable") }
	if sessionID != "" {
		caller, found, err := s.sessions.GetSession(sessionID)
		if err != nil || !found || caller.AccountScopeID != p.AccountScopeID || caller.UserID != p.UserID || !tool.EnvironmentToolAllowed(caller.Metadata, "manage_environments") {
			return nil, errors.New("task environment caller denied")
		}
		var profile pebblestore.AgentProfile
		raw, _ := json.Marshal(caller.Metadata["agent_profile"])
		if err := json.Unmarshal(raw, &profile); err != nil { return nil, err }
		if agentruntime.IsOrchestratorAgentName(profile.Name) {
			if err := s.validateProjectInspectionCaller(p, caller, projectID); err != nil { return nil, err }
		} else {
			if err := verifyProjectTaskSession(task, caller, p.AccountScopeID); err != nil { return nil, err }
			task.EnsureTaskAttempts()
			a := task.ActiveAttempt()
			if a == nil || a.SessionID != sessionID { return nil, errors.New("task consumer attempt is stale") }
		}
	}
	return task, nil
}

func (s *Server) validateTaskEnvironmentSource(p identity.Principal, projectID string, source environments.PreparedDeploymentSource) error {
	proj, found, err := s.sessions.Store().GetProject(p.AccountScopeID, projectID)
	if err != nil || !found { return errors.New("project unavailable") }
	if source.AccountScopeID != p.AccountScopeID { return errors.New("source account mismatch") }
	for _, ref := range []environments.CommittedBuildSource{source.Build.Product, source.Build.Recipe} {
		if ref.WorkspaceID == "" || ref.WorkspaceGeneration <= 0 { return errors.New("source catalog generation required") }
		if _, err := s.resolveProjectTaskSource(p, proj, "", ref.WorkspaceID, ref.WorkspaceGeneration, false); err != nil { return err }
	}
	if source.WorkspaceID != source.Build.Product.WorkspaceID { return errors.New("deployment must belong to product workspace") }
	return nil
}

// ValidateTaskEnvironmentLease is called both before receipt creation and directly
// before supervised execution. It reloads task, session, catalog and deployment;
// detach/reassignment/reopen never changes another consumer's receipt.
func (s *Server) ValidateTaskEnvironmentLease(ctx context.Context, lease environments.DeploymentLease) error {
	if err := ctx.Err(); err != nil { return err }
	b := lease.TaskBinding
	if b == nil || lease.PreparedSource == nil || b.UserID == "" { return errors.New("task binding required") }
	p := identity.Principal{Type: identity.PrincipalTypeUser, UserID: b.UserID, AccountScopeID: lease.AccountScopeID}
	sessionID := ""
	if lease.ConsumerType == environments.ConsumerTypeSession { sessionID = lease.ConsumerID } else if lease.ConsumerType != environments.ConsumerTypeCustom || lease.ConsumerID != b.UserID { return errors.New("invalid task consumer") }
	task, err := s.authorizeTaskEnvironment(p, sessionID, b.ProjectID, b.TaskID)
	if err != nil { return err }
	if b.AttemptID == "" || task.ActiveAttemptID != b.AttemptID { return errors.New("task attempt changed; explicitly reassign attachment") }
	for _, a := range task.EnvironmentAttachments {
		if a.ID != b.AttachmentID { continue }
		if a.Revision != b.AttachmentRevision || a.AttemptID != b.AttemptID || a.EffectiveState(task.ActiveAttemptID, time.Now().UnixMilli()) != "ready" || a.Source != *lease.PreparedSource {
			return errors.New("attachment expired, replaced or reassigned")
		}
		if err := s.validateTaskEnvironmentSource(p, b.ProjectID, a.Source); err != nil { return err }
		if s.deployments == nil { return errors.New("deployment service unavailable") }
		d, found, err := s.deployments.GetDeployment(p.AccountScopeID, a.Source.WorkspaceID, a.Source.DeploymentID)
		if err != nil { return err }
		if !found || !a.Source.Matches(d) { return errors.New("deployment generation changed; rebuild and reattach explicitly") }
		return nil
	}
	return errors.New("attachment detached")
}

func (s *Server) taskEnvironmentProjection(ctx context.Context, p identity.Principal, task *pebblestore.ProjectTaskRecord) []environments.TaskEnvironmentAttachment {
	out := append([]environments.TaskEnvironmentAttachment{}, task.EnvironmentAttachments...)
	for i := range out {
		a := &out[i]
		a.State = a.EffectiveState(task.ActiveAttemptID, time.Now().UnixMilli())
		if a.State == "stale" { continue }
		if s.validateTaskEnvironmentSource(p, task.ProjectID, a.Source) != nil || s.deployments == nil { a.State = "stale"; continue }
		if a.Source.DeploymentID == "" {
			op, found, err := s.deployments.Get(ctx, p.AccountScopeID, a.Source.WorkspaceID, a.OperationID)
			if err != nil || !found { a.State = "stale"; continue }
			switch op.Status {
			case environments.OperationStatusQueued: a.State = "preparing"
			case environments.OperationStatusRunning: a.State = "building"
			case environments.OperationStatusSucceeded: a.State = "stale" // explicit deployment selection still required
			default: a.State = "failed"
			}
			continue
		}
		d, found, err := s.deployments.GetDeployment(p.AccountScopeID, a.Source.WorkspaceID, a.Source.DeploymentID)
		if err != nil || !found || d.Build == nil || *d.Build != a.Source.Build || d.CreatedAt != a.Source.CreatedAt || d.Runtime.ContainerID != a.Source.ContainerID { a.State = "stale"; continue }
		switch d.Status {
		case environments.DeploymentStatusStopped, environments.DeploymentStatusTerminated: a.State = "stopped"
		case environments.DeploymentStatusFailed: a.State = "failed"
		case environments.DeploymentStatusPending, environments.DeploymentStatusProvisioning, environments.DeploymentStatusStarting: a.State = "preparing"
		default:
			if a.Source.Matches(d) { a.State = "ready" } else { a.State = "stale" }
		}
	}
	return out
}

// ManageTaskEnvironment is the single tool/HTTP boundary. It never starts a task,
// expands workspace scope, accepts source claims or copies another consumer lease.
func (s *Server) ManageTaskEnvironment(ctx context.Context, p identity.Principal, sessionID string, req tool.TaskEnvironmentRequest) (tool.TaskEnvironmentResult, error) {
	var result tool.TaskEnvironmentResult
	if err := ctx.Err(); err != nil { return result, err }
	var task *pebblestore.ProjectTaskRecord
	var err error
	if req.Action == "release" && p.Valid() && (p.SessionID == "" || p.SessionID == sessionID) && s.sessions != nil {
		// Revocation removes execution authority, not the ability to release one's
		// own receipt. The receipt ownership check below remains mandatory.
		if sessionID != "" {
			caller, found, e := s.sessions.GetSession(sessionID)
			if e != nil || !found || caller.AccountScopeID != p.AccountScopeID || caller.UserID != p.UserID || !tool.EnvironmentToolAllowed(caller.Metadata, "manage_environments") { return result, errors.New("release caller denied") }
		}
		task, _, err = s.sessions.Store().GetProjectTask(p.AccountScopeID, req.ProjectID, req.TaskID)
		if task == nil { return result, errors.New("task unavailable") }
	} else {
		task, err = s.authorizeTaskEnvironment(p, sessionID, req.ProjectID, req.TaskID)
	}
	if err != nil { return result, err }
	if req.Action == "attach_task" && sessionID != "" {
		caller, _, _ := s.sessions.GetSession(sessionID)
		if caller.Metadata["task_id"] != nil && req.AttemptID != task.ActiveAttemptID { return result, errors.New("task consumer must attach to its current attempt") }
	}
	if req.TimeoutMS < 0 || req.TimeoutMS > int64((time.Hour)/time.Millisecond) { return result, errors.New("timeout_ms must be between zero and one hour") }
	if req.Action != "list_attachments" && req.AttachmentID == "" { return result, errors.New("explicit attachment_id required") }
	switch req.Action {
	case "list_attachments":
	case "detach_task":
		task, err = s.sessions.Store().MutateProjectTaskEnvironment(p.AccountScopeID, req.ProjectID, req.TaskID, pebblestore.TaskEnvironmentMutation{ExpectedTaskRevision: req.ExpectedTaskRevision, ExpectedAttachmentRevision: req.ExpectedAttachmentRevision, AttachmentID: req.AttachmentID})
	case "attach_task":
		if s.deployments == nil || s.environments == nil { return result, errors.New("environment services unavailable") }
		now := time.Now().UnixMilli()
		if req.ExpiresAt <= now || req.ExpiresAt > now + int64((24*time.Hour)/time.Millisecond) { return result, errors.New("attachment expiry must be within 24 hours") }
		if req.WorkspaceID == "" || (req.DeploymentID == "") == (req.OperationID == "") { return result, errors.New("select workspace_id and exactly one deployment_id or operation_id") }
		a := environments.TaskEnvironmentAttachment{ID: req.AttachmentID, Revision: req.ExpectedAttachmentRevision+1, AccountScopeID: p.AccountScopeID, ProjectID: req.ProjectID, TaskID: req.TaskID, AttemptID: req.AttemptID, ExpiresAt: req.ExpiresAt}
		if req.DeploymentID != "" {
			d, found, e := s.deployments.GetDeployment(p.AccountScopeID, req.WorkspaceID, req.DeploymentID)
			if e != nil || !found || d.Build == nil || !d.IsUsable() { return result, errors.New("exact ready managed deployment required") }
			a.Source = environments.PreparedDeploymentSource{AccountScopeID: p.AccountScopeID, WorkspaceID: d.WorkspaceID, EnvironmentID: d.EnvironmentID, DeploymentID: d.ID, CreatedAt: d.CreatedAt, ContainerID: d.Runtime.ContainerID, Build: *d.Build}
			a.State = "ready"
		} else {
			op, found, e := s.deployments.Get(ctx, p.AccountScopeID, req.WorkspaceID, req.OperationID)
			if e != nil || !found || op.Action != environments.OperationActionBuild { return result, errors.New("managed build operation required") }
			// Use the operation's captured definition, never the mutable catalog definition.
			if op.BuildDefinition == nil { return result, errors.New("captured build provenance unavailable") }
			build := environments.ImageBuildResult{OperationID: op.OperationID, ConnectionID: op.BuildConnectionID, Product: op.BuildDefinition.Product, Recipe: op.BuildDefinition.Recipe, RecipeFile: op.BuildDefinition.RecipeFile, DefinitionDigest: op.BuildDefinition.Digest()}
			if op.Result.Build != nil { build = *op.Result.Build }
			a.Source = environments.PreparedDeploymentSource{AccountScopeID: p.AccountScopeID, WorkspaceID: req.WorkspaceID, EnvironmentID: op.EnvironmentID, Build: build}
			a.OperationID, a.State = op.OperationID, "preparing"
		}
		a.EnvironmentID = a.Source.EnvironmentID
		env, found, e := s.environments.Get(p.AccountScopeID, req.WorkspaceID, a.EnvironmentID)
		if e != nil || !found { return result, errors.New("environment definition unavailable") }
		a.EnvironmentName = env.Name
		if e := s.validateTaskEnvironmentSource(p, req.ProjectID, a.Source); e != nil { return result, e }
		if e := a.Validate(); e != nil { return result, e }
		task, err = s.sessions.Store().MutateProjectTaskEnvironment(p.AccountScopeID, req.ProjectID, req.TaskID, pebblestore.TaskEnvironmentMutation{ExpectedTaskRevision: req.ExpectedTaskRevision, ExpectedAttachmentRevision: req.ExpectedAttachmentRevision, AttachmentID: req.AttachmentID, Attachment: &a})
	case "exec", "release", "get_operation", "cancel_operation", "get_deployment":
		reader, ok := s.deployments.(interface { GetLease(string, string, string) (environments.DeploymentLease, bool, error) })
		if !ok || req.LeaseID == "" || req.WorkspaceID == "" { return result, errors.New("exact workspace_id and own lease_id required") }
		lease, found, e := reader.GetLease(p.AccountScopeID, req.WorkspaceID, req.LeaseID)
		if e != nil || !found || lease.TaskBinding == nil { return result, errors.New("task lease unavailable") }
		kind, id := environments.ConsumerTypeCustom, p.UserID
		if sessionID != "" { kind, id = environments.ConsumerTypeSession, sessionID }
		b := lease.TaskBinding
		if lease.ConsumerType != kind || lease.ConsumerID != id || b.UserID != p.UserID || b.ProjectID != req.ProjectID || b.TaskID != req.TaskID || b.AttachmentID != req.AttachmentID { return result, errors.New("receipt does not belong to this consumer and attachment") }
		if req.Action != "release" {
			if !lease.IsHeld(time.Now().UnixMilli()) { return result, errors.New("lease expired or released") }
			if e := s.ValidateTaskEnvironmentLease(ctx, lease); e != nil { return result, e }
		}
		attribution := environments.OperationAttribution{Actor: p.UserID, SessionID: sessionID}
		if req.Action == "get_operation" || req.Action == "cancel_operation" {
			op, found, e := s.deployments.Get(ctx, p.AccountScopeID, req.WorkspaceID, req.OperationID)
			if e != nil || !found || op.LeaseID != lease.ID || op.Attribution.SessionID != sessionID || op.Attribution.Actor != p.UserID { return result, errors.New("operation does not belong to receipt owner") }
			if req.Action == "cancel_operation" {
				cancelled, e := s.deployments.Cancel(ctx, lifecycle.CancelOperationRequest{AccountScopeID: p.AccountScopeID, WorkspaceID: req.WorkspaceID, OperationID: op.OperationID, Reason: "task consumer cancellation"})
				if e != nil { return result, e }
				op = *cancelled
			}
			result.Operation = &op
		} else if req.Action != "get_deployment" {
			op, e := s.deployments.Submit(ctx, lifecycle.SubmitOperationRequest{AccountScopeID: p.AccountScopeID, WorkspaceID: req.WorkspaceID, DeploymentID: lease.DeploymentID, LeaseID: lease.ID, Action: req.Action, Attribution: attribution, Command: req.Command, WorkingDir: req.WorkingDir, Timeout: time.Duration(req.TimeoutMS)*time.Millisecond, MaxOutput: req.MaxOutput})
			if e != nil { return result, e }
			result.Operation = op
		}
	case "acquire_attachment":
		if req.ExpectedAttachmentRevision <= 0 || req.AttemptID == "" { return result, errors.New("exact attachment revision and attempt required") }
		service, ok := s.deployments.(interface { AcquirePreparedLease(context.Context, lifecycle.AcquirePreparedLeaseRequest) (environments.DeploymentLease, error) })
		if !ok { return result, errors.New("prepared lease service unavailable") }
		var selected *environments.TaskEnvironmentAttachment
		for i := range task.EnvironmentAttachments { if task.EnvironmentAttachments[i].ID == req.AttachmentID { selected = &task.EnvironmentAttachments[i] } }
		if selected == nil { return result, errors.New("attachment unavailable") }
		binding := &environments.TaskLeaseBinding{ProjectID: req.ProjectID, TaskID: req.TaskID, AttemptID: req.AttemptID, AttachmentID: req.AttachmentID, AttachmentRevision: req.ExpectedAttachmentRevision, UserID: p.UserID}
		kind, id := environments.ConsumerTypeCustom, p.UserID
		if sessionID != "" { kind, id = environments.ConsumerTypeSession, sessionID }
		candidate := environments.DeploymentLease{AccountScopeID: p.AccountScopeID, ConsumerType: kind, ConsumerID: id, TaskBinding: binding, PreparedSource: &selected.Source}
		if e := s.ValidateTaskEnvironmentLease(ctx, candidate); e != nil { return result, e }
		lease, e := service.AcquirePreparedLease(ctx, lifecycle.AcquirePreparedLeaseRequest{Source: selected.Source, Binding: binding, Attribution: environments.OperationAttribution{Actor: p.UserID, SessionID: sessionID}, TTLMillis: req.TTLMillis})
		if e != nil { return result, e }
		result.Lease = &lease
	default:
		return result, errors.New("unknown task environment action")
	}
	if err != nil { return result, err }
	result.TaskRevision, result.Attachments = task.Revision, s.taskEnvironmentProjection(ctx, p, task)
	return result, nil
}

func (s *Server) handleTaskEnvironments(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost { w.WriteHeader(http.StatusMethodNotAllowed); return }
	p, ok := PrincipalFromRequest(r)
	if !ok || !p.Valid() { writeError(w, http.StatusUnauthorized, errors.New("principal required")); return }
	var req tool.TaskEnvironmentRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil { writeError(w, http.StatusBadRequest, err); return }
	result, err := s.ManageTaskEnvironment(r.Context(), p, p.SessionID, req)
	if err != nil { writeError(w, http.StatusForbidden, err); return }
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

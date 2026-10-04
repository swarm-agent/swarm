package run

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/automation"
	"swarm/packages/swarmd/internal/identity"
	store "swarm/packages/swarmd/internal/store/pebble"
)

// authorizeWorkerControl consumes transport-bound identity, never request JSON.
// Agent origins must resolve to a canonical Orchestrator session; only explicit
// users can approve or publish context. It is shared by headless and AI ingress.
func (s *WorkerExecutionService) authorizeWorkerControl(ctx context.Context, approve bool) (string, string, error) {
	p, err := automation.RuntimePrincipal(ctx)
	if err != nil {
		return "", "", automation.ErrDenied
	}
	user := p.SubjectID
	if p.Role == "agent" && !approve {
		if _, err = s.workerStore(); err != nil {
			return "", "", err
		}
		snap, ok, e := s.host.runs.sessions.GetSession(p.SubjectID)
		if e != nil {
			return "", "", e
		}
		if !ok || snap.AccountScopeID != p.AccountID {
			return "", "", automation.ErrDenied
		}
		profile, e := sessionV3AgentProfileFromMetadataMap(snap.Metadata)
		if e != nil || !agentruntime.IsOrchestratorAgentName(profile.Name) {
			return "", "", automation.ErrDenied
		}
		user = snap.UserID
	} else if p.Role != "user" {
		return "", "", automation.ErrDenied
	}
	if approve {
		if _, err = automation.RuntimeApprovalIdentity().ExplicitUser(ctx); err != nil {
			return "", "", automation.ErrDenied
		}
	}
	if err = s.authorizeWorkerOwner(p.AccountID, user); err != nil {
		return "", "", automation.ErrDenied
	}
	return p.AccountID, user, nil
}

// ResolveWorkerTarget checks references in existing account-owned catalogs. No
// network call, credentials, topology duplication or readiness claim is made.
func (s *WorkerExecutionService) ResolveWorkerTarget(ctx context.Context, t store.WorkerTargetReference) (store.WorkerTargetReference, error) {
	account, user, err := s.authorizeWorkerControl(ctx, false)
	if err != nil {
		return t, err
	}
	if t.Capacity < 1 || t.Capacity > 100 {
		return t, store.ErrWorkerInvalid
	}
	db := s.host.runs.sessions.Store().Underlying()
	var canonical any
	switch t.Kind {
	case "ssh":
		if s.host.runs.workspace == nil {
			return t, store.ErrWorkerInvalid
		}
		p := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: account, UserID: user, AccountScopeSource: identity.AccountScopeSourceServerState}
		entry, found, e := s.host.runs.workspace.GetByWorkspaceIDForPrincipal(p, t.WorkspaceID)
		if e != nil {
			return t, e
		}
		if !found || entry.State != "active" {
			return t, store.ErrWorkerNotFound
		}
		conn, ok, e := store.NewConnectionStore(db).Get(account, t.WorkspaceID, t.ReferenceID)
		if e != nil {
			return t, e
		}
		if !ok || string(conn.Kind) != "ssh" {
			return t, store.ErrWorkerNotFound
		}
		canonical = conn
	case "gcp":
		runtime, ok, e := store.NewTopologyStore(db).GetRuntimeForAccount(account, t.ReferenceID)
		if e != nil {
			return t, e
		}
		if !ok || runtime.Transport != "gcp" {
			return t, store.ErrWorkerNotFound
		}
		if t.WorkspaceID != "" {
			return t, store.ErrWorkerInvalid
		}
		canonical = runtime
	default:
		return t, store.ErrWorkerInvalid
	}
	raw, err := json.Marshal(canonical)
	if err != nil {
		return t, err
	}
	hash := sha256.Sum256(raw)
	t.ReferenceDigest = hex.EncodeToString(hash[:])
	return t, nil
}
func (s *WorkerExecutionService) ProposeDeployment(ctx context.Context, worker string, req store.WorkerDeploymentRequest) (store.WorkerDeploymentRecord, error) {
	account, user, err := s.authorizeWorkerControl(ctx, false)
	if err != nil {
		return store.WorkerDeploymentRecord{}, err
	}
	req.Target, err = s.ResolveWorkerTarget(ctx, req.Target)
	if err != nil {
		return store.WorkerDeploymentRecord{}, err
	}
	ws, err := s.workerStore()
	if err != nil {
		return store.WorkerDeploymentRecord{}, err
	}
	return ws.ProposeWorkerDeployment(account, user, worker, req)
}
func (s *WorkerExecutionService) ApproveDeployment(ctx context.Context, worker, id string, revision uint64, digest string) (store.WorkerDeploymentRecord, error) {
	account, user, err := s.authorizeWorkerControl(ctx, true)
	if err != nil {
		return store.WorkerDeploymentRecord{}, err
	}
	ws, err := s.workerStore()
	if err != nil {
		return store.WorkerDeploymentRecord{}, err
	}
	d, err := ws.GetWorkerDeployment(account, worker, id)
	if err != nil {
		return d, err
	}
	target, err := s.ResolveWorkerTarget(ctx, d.Target)
	if err != nil {
		return store.WorkerDeploymentRecord{}, err
	}
	if target != d.Target {
		return store.WorkerDeploymentRecord{}, fmt.Errorf("%w: target reference changed", store.ErrWorkerConflict)
	}
	return ws.ApproveWorkerDeployment(account, user, worker, id, revision, digest)
}
func (s *WorkerExecutionService) UpdateContext(ctx context.Context, worker string, req store.WorkerContextUpdate) (store.WorkerContextRecord, error) {
	account, user, err := s.authorizeWorkerControl(ctx, true)
	if err != nil {
		return store.WorkerContextRecord{}, err
	}
	ws, err := s.workerStore()
	if err != nil {
		return store.WorkerContextRecord{}, err
	}
	return ws.PutWorkerContext(account, user, worker, req)
}

// QueueDeploymentJob admits intent without invoking Start. Pins the resolved
// canonical account model. Remote execution remains explicitly unavailable.
func (s *WorkerExecutionService) QueueDeploymentJob(ctx context.Context, req store.WorkerRunAdmission) (store.WorkerRunRecord, error) {
	account, user, err := s.authorizeWorkerControl(ctx, false)
	if err != nil {
		return store.WorkerRunRecord{}, err
	}
	if req.Placement == nil || req.ExpectedWorkerRevision == 0 || req.RequestSource != "direct" || req.OccurrenceID != "" {
		return store.WorkerRunRecord{}, store.ErrWorkerInvalid
	}
	ws, err := s.workerStore()
	if err != nil {
		return store.WorkerRunRecord{}, err
	}
	w, ok, err := ws.GetWorker(account, req.WorkerID)
	if err != nil {
		return store.WorkerRunRecord{}, err
	}
	if !ok {
		return store.WorkerRunRecord{}, store.ErrWorkerNotFound
	}
	d, err := ws.GetWorkerDeployment(account, w.ID, req.Placement.DeploymentID)
	if err != nil {
		return store.WorkerRunRecord{}, err
	}
	target, err := s.ResolveWorkerTarget(ctx, d.Target)
	if err != nil {
		return store.WorkerRunRecord{}, err
	}
	if target != d.Target {
		return store.WorkerRunRecord{}, store.ErrWorkerConflict
	}
	req.ResolvedModelProfile, err = s.ResolveModelProfile(account, w.ModelProfile)
	if err != nil {
		return store.WorkerRunRecord{}, err
	}
	req.ResolvedModelProfile.UseAccountDefault = false
	req.ResolvedModelProfile.ActionUseAccountDefault = false
	req.ResolvedModelProfile.PlanUseAccountDefault = false
	req.UserID = user
	return ws.AdmitWorkerRun(account, req)
}
func (s *WorkerExecutionService) DeploymentCommand(ctx context.Context, worker, id string, req store.WorkerCommandRequest) (store.WorkerCommandRecord, error) {
	account, user, err := s.authorizeWorkerControl(ctx, false)
	if err != nil {
		return store.WorkerCommandRecord{}, err
	}
	ws, err := s.workerStore()
	if err != nil {
		return store.WorkerCommandRecord{}, err
	}
	if _, err = ws.GetWorkerDeployment(account, worker, id); err != nil {
		return store.WorkerCommandRecord{}, err
	}
	return ws.QueueWorkerCommand(account, user, worker, id, req)
}

// RegisterSSHTarget requires an explicit user and an authorized canonical
// workspace. It registers configuration only; no host contact or provisioning.
func (s *WorkerExecutionService) RegisterSSHTarget(ctx context.Context, req store.WorkerSSHRegistration) (store.WorkerTargetReference, error) {
	account, user, err := s.authorizeWorkerControl(ctx, true)
	if err != nil {
		return store.WorkerTargetReference{}, err
	}
	if s.host.runs.workspace == nil {
		return store.WorkerTargetReference{}, store.ErrWorkerInvalid
	}
	p := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: account, UserID: user, AccountScopeSource: identity.AccountScopeSourceServerState}
	entry, found, err := s.host.runs.workspace.GetByWorkspaceIDForPrincipal(p, req.WorkspaceID)
	if err != nil {
		return store.WorkerTargetReference{}, err
	}
	if !found || entry.State != "active" {
		return store.WorkerTargetReference{}, store.ErrWorkerNotFound
	}
	ws, err := s.workerStore()
	if err != nil {
		return store.WorkerTargetReference{}, err
	}
	conn, err := ws.RegisterWorkerSSHConnection(account, req)
	if err != nil {
		return store.WorkerTargetReference{}, err
	}
	return s.ResolveWorkerTarget(ctx, store.WorkerTargetReference{Kind: "ssh", WorkspaceID: req.WorkspaceID, ReferenceID: conn.ID, Capacity: 1})
}

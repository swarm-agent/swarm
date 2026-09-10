package automation

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"

	"swarm/packages/swarmd/internal/identity"
	sessions "swarm/packages/swarmd/internal/session"
	store "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/worktree"
)

// ExecutionApproval resolves an actual user-approved, revocable policy. Implementations
// must compare the entire definition, principal and workspace, not just its reference.
// Request is the user-gesture adapter; Ensure never requests or grants permission.
type ExecutionApproval interface {
	Request(context.Context, Principal, store.AutomationRecord) (string, error)
	Verify(context.Context, Principal, store.AutomationRecord) error
}

// V3ExecutionHost supplies canonical workspace/model metadata and checkpoint start
// and cancellation authorities. Prepare must resolve the saved workspace by ID,
// authenticate the owner and compile the permitted agent/tool/model contract.
// Start must durably deduplicate key, then enter canonical checkpoint execution;
// Cancel must durably prevent a concurrent or recovered Start with the same key.
type V3ExecutionHost interface {
	Prepare(context.Context, Principal, store.AutomationRecord) (store.SessionSnapshot, error)
	Start(context.Context, store.SessionSnapshot, string) error
	Cancel(context.Context, store.SessionSnapshot, string) error
}

type V3Runtime struct {
	domain *Service
	sessions *sessions.Service
	worktrees *worktree.Service
	approval ExecutionApproval
	host V3ExecutionHost
	apply func(sessions.SessionMutationInput) (sessions.SessionMutationResult, error)
}

func NewV3Runtime(domain *Service, service *sessions.Service, trees *worktree.Service, approval ExecutionApproval, host V3ExecutionHost, apply func(sessions.SessionMutationInput) (sessions.SessionMutationResult, error)) (*V3Runtime, error) {
	if domain == nil || service == nil || trees == nil || approval == nil || host == nil || apply == nil {
		return nil, ErrInvalid
	}
	return &V3Runtime{domain: domain, sessions: service, worktrees: trees, approval: approval, host: host, apply: apply}, nil
}

func executionDocumentDigest(data []byte) string { return fmt.Sprintf("%x", sha256.Sum256(data)) }

func (v *V3Runtime) Ensure(ctx context.Context, p Principal, def, occurrence store.AutomationRecord) (string, error) {
	if def.Definition == nil || occurrence.Occurrence == nil || def.Scope != occurrence.Scope || def.AutomationID != occurrence.AutomationID || def.Revision != occurrence.Occurrence.DefinitionRevision || p.AccountID != def.Scope.AccountID {
		return "", ErrInvalid
	}
	// The canonical executor has no automation-specific tool/target overlay.
	// Never reinterpret a restricted approval as the default Swarm contract,
	// including recovery of an already-created session.
	if err := ValidateExecutionPolicy(def.Definition.Authorization); err != nil { return "", err }
	if err := v.approval.Verify(ctx, p, def); err != nil { return "", err }
	key := executionKey(def.Scope, def.AutomationID, occurrence.ID)
	id := "automation-" + key
	snapshot, found, err := v.sessions.GetSession(id)
	if err != nil { return "", err }
	if found {
		if snapshot.AccountScopeID != p.AccountID || snapshot.Metadata["automation_execution_key"] != key || !snapshot.WorktreeEnabled {
			return "", ErrDenied
		}
		if err := v.worktrees.ValidateSessionRepositoryLaneForRead(snapshot.WorkspacePath, snapshot.WorktreeRootPath, id, snapshot.WorktreeBranch); err != nil { return "", err }
	} else {
		if _, err := v.pinnedDocument(ctx, p, def, id); err != nil { return "", err }
		snapshot, err = v.host.Prepare(ctx, p, def)
		if err != nil { return "", err }
		if snapshot.AccountScopeID != p.AccountID || snapshot.UserID == "" || snapshot.WorkspacePath == "" || snapshot.Metadata == nil { return "", ErrDenied }
		principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: snapshot.UserID, AccountScopeID: snapshot.AccountScopeID}
		allocation, err := v.worktrees.AllocateDetachedWorkspaceRequestedForPrincipal(principal, snapshot.WorkspacePath, id, "", "agent/automation-"+key[:16])
		if err != nil { return "", err }
		snapshot.ID, snapshot.Mode = id, sessions.ModePlan
		snapshot.CreatedAt, snapshot.UpdatedAt = occurrence.WrittenAt, occurrence.WrittenAt
		snapshot.WorktreeEnabled = true
		snapshot.WorktreeRootPath, snapshot.WorktreeBaseBranch, snapshot.WorktreeBranch = allocation.WorkspacePath, allocation.BaseBranch, allocation.BranchName
		snapshot.Metadata["automation_execution_key"] = key
		snapshot.Metadata["automation_definition_revision"] = def.Revision
		snapshot.Metadata["automation_occurrence_id"] = occurrence.ID
		snapshot.Metadata["swarm_v3_mandatory_worktree"] = true
		snapshot.Metadata["swarm_v3_worktree_owner_session_id"] = id
		snapshot.Metadata["swarm_v3_worktree_base_commit"] = allocation.BaseCommit
		snapshot.Metadata["swarm_v3_runtime_workspace_path"] = allocation.WorkspacePath
		available := true
		snapshot.WorkspaceGrants = append(snapshot.WorkspaceGrants, store.WorkspaceGrant{Kind: store.WorkspaceGrantWorktree, Path: allocation.WorkspacePath, Available: &available})
		snapshot.WorkspaceUsage = store.WorkspaceUsageFromGrants(snapshot.WorkspaceGrants)
		mutationKey := "automation-create-"+key
		_, err = v.apply(sessions.SessionMutationInput{SessionID: id, UserID: snapshot.UserID, AccountScopeID: p.AccountID, ClientRequestID: mutationKey, IdempotencyKey: mutationKey, PayloadHash: key, RequestHash: key, Kind: sessions.SessionMutationCreateSession, Session: &snapshot, NowUnixMs: occurrence.WrittenAt})
		// An ambiguous create error must retain the lane for recovery, never delete it.
		if err != nil { return "", err }
	}
	if snapshot.Mode == sessions.ModePlan {
		doc, err := v.pinnedDocument(ctx, p, def, id)
		if err != nil { return "", err }
		accepted, err := v.sessions.CommitV3PlanAcceptance(sessions.PlanAcceptanceCommitInput{Session: snapshot, PlanID: id, Title: doc.Title, Document: doc, ApplySessionMutation: v.apply})
		if err != nil { return "", err }
		snapshot = accepted.Session
	}
	if err := v.approval.Verify(ctx, p, def); err != nil { return "", err }
	// Recheck the durable occurrence fence after preparation. The host's own
	// Start/Cancel fence still owns the race after this check.
	claims, ok := v.domain.repo.(interface { ClaimAutomationDispatch(store.AutomationScope, string, string) error })
	if !ok { return "", ErrInvalid }
	if err := claims.ClaimAutomationDispatch(occurrence.Scope, occurrence.AutomationID, occurrence.ID); err != nil { return "", err }
	if err := v.host.Start(ctx, snapshot, key); err != nil { return "", err }
	return id, nil
}

// The canonical engine executes bindings in declared topological order. Each
// source checkpoint keeps its complete editorial contract but no prior results.
func (v *V3Runtime) pinnedDocument(ctx context.Context, p Principal, def store.AutomationRecord, id string) (*store.SessionPlanDocument, error) {
	doc := &store.SessionPlanDocument{ID: id, Title: def.Definition.Name, Info: store.SessionPlanInfo{Goal: def.Definition.Name}}
	for i, binding := range def.Definition.Plans {
		ref := binding.Plan
		if ref.DocumentSHA256 == "" { return nil, ErrDenied }
		if err := v.domain.plan(ctx, p, def.Scope, &ref); err != nil { return nil, err }
		source, found, err := v.domain.plans.GetPlanRevision(ref.SessionID, ref.PlanID, int(ref.Revision))
		if err != nil { return nil, err }
		if !found || source.Document == nil { return nil, ErrDenied }
		data, err := json.Marshal(source.Document)
		if err != nil { return nil, err }
		if executionDocumentDigest(data) != ref.DocumentSHA256 { return nil, ErrDenied }
		var copy store.SessionPlanDocument
		if err := json.Unmarshal(data, &copy); err != nil { return nil, err }
		info, err := json.Marshal(copy.Info)
		if err != nil { return nil, err }
		for _, cp := range copy.Checkpoints {
			fresh := store.SessionPlanCheckpoint{ID: fmt.Sprintf("binding-%d-%s", i+1, cp.ID), Title: cp.Title, Status: "pending", Objective: cp.Objective, Tasks: cp.Tasks, AcceptanceCriteria: cp.AcceptanceCriteria, TaskProgram: cp.TaskProgram, Artifacts: cp.Artifacts, Notes: "Pinned plan context: "+string(info)+"\n"+cp.Notes, Order: len(doc.Checkpoints)+1}
			for _, sub := range cp.Subtasks { fresh.Subtasks = append(fresh.Subtasks, store.SessionPlanSubtask{ID: sub.ID, Title: sub.Title, Status: "pending", Notes: sub.Notes, Order: sub.Order}) }
			doc.Checkpoints = append(doc.Checkpoints, fresh)
		}
		doc.Artifacts = append(doc.Artifacts, copy.Artifacts...)
	}
	return doc, nil
}

func (v *V3Runtime) Cancel(ctx context.Context, p Principal, r store.AutomationRecord) error {
	if r.Occurrence == nil || r.Occurrence.State != "cancelling" || p.AccountID != r.Scope.AccountID { return ErrDenied }
	key := executionKey(r.Scope, r.AutomationID, r.ID)
	id := "automation-"+key
	snapshot, found, err := v.sessions.GetSession(id)
	if err != nil { return err }
	if found && (snapshot.AccountScopeID != p.AccountID || snapshot.Metadata["automation_execution_key"] != key) { return ErrDenied }
	if !found { snapshot = store.SessionSnapshot{ID: id, AccountScopeID: p.AccountID} }
	return v.host.Cancel(ctx, snapshot, key)
}

// ValidateExecutionPolicy fails closed for restrictions that the canonical run
// executor cannot yet enforce. Empty lists retain the default permissioned local
// Swarm contract; they are not an automation permission bypass.
func ValidateExecutionPolicy(policy store.AutomationAuthorizationPolicy) error {
	if len(policy.AllowedTools) != 0 || len(policy.TargetIDs) != 0 { return ErrDenied }
	return nil
}

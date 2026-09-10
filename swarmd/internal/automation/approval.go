package automation

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"time"

	store "swarm/packages/swarmd/internal/store/pebble"
)

// ApprovalOwnership is the canonical, current ownership boundary. Implementations
// resolve workspace and session access, including agent session identity, not paths
// or owner IDs supplied by tool arguments.
type ApprovalOwnership interface {
	Workspace(context.Context, Principal, store.AutomationScope, string) error
	PlanSession(context.Context, Principal, store.AutomationScope, string) error
	OccurrenceSession(context.Context, Principal, store.AutomationScope, string) error
}
type ApprovalRepository interface {
	Repository
	GetAutomationApproval(store.AutomationScope, string) (store.AutomationApproval, bool, error)
	CreateAutomationApproval(store.AutomationApproval) (store.AutomationApproval, error)
	RevokeAutomationApproval(store.AutomationScope, string, string, uint64, int64) (store.AutomationApproval, error)
}

// ApprovalIdentity resolves trusted transport/runtime context on EVERY call. The
// explicitUser function must reject agent/tool origins even when their enclosing
// session belongs to a user. Neither function may read a request JSON role/bool.
type ApprovalIdentity struct {
	Current      func(context.Context) (Principal, error)
	ExplicitUser func(context.Context) (Principal, error)
}
type PolicyApproval struct {
	repo      ApprovalRepository
	plans     CanonicalPlans
	ownership ApprovalOwnership
	identity  ApprovalIdentity
	now       func() time.Time
}

func NewPolicyApproval(repo ApprovalRepository, plans CanonicalPlans, ownership ApprovalOwnership, identity ApprovalIdentity, now func() time.Time) (*PolicyApproval, error) {
	if repo == nil || plans == nil || ownership == nil || identity.Current == nil || identity.ExplicitUser == nil || now == nil {
		return nil, ErrInvalid
	}
	return &PolicyApproval{repo: repo, plans: plans, ownership: ownership, identity: identity, now: now}, nil
}

var _ ExecutionApproval = (*PolicyApproval)(nil)
var _ Access = (*PolicyApproval)(nil)

func (a *PolicyApproval) Workspace(ctx context.Context, p Principal, scope store.AutomationScope, action string) error {
	actual, err := a.identity.Current(ctx)
	if err != nil {
		return err
	}
	if actual != p || p.SubjectID == "" || p.AccountID == "" || p.AccountID != scope.AccountID || scope.WorkspaceID == "" || (p.Role != "user" && p.Role != "agent" && p.Role != "system") {
		return ErrDenied
	}
	return a.ownership.Workspace(ctx, p, scope, action)
}
func (a *PolicyApproval) PlanSession(ctx context.Context, p Principal, scope store.AutomationScope, id string) error {
	if err := a.Workspace(ctx, p, scope, "read"); err != nil {
		return err
	}
	return a.ownership.PlanSession(ctx, p, scope, id)
}
func (a *PolicyApproval) OccurrenceSession(ctx context.Context, p Principal, scope store.AutomationScope, id string) error {
	if err := a.Workspace(ctx, p, scope, "context"); err != nil {
		return err
	}
	return a.ownership.OccurrenceSession(ctx, p, scope, id)
}

// ApprovalRequest is the explicit user route body: approve the exact stored
// revision with the displayed policy digest. No principal, role or approved bool.
// The response reference is saved with approved_policy on a subsequent definition
// CAS. Only Enabled, authorization Mode and the generated reference are excluded
// from the digest; plans, documents, tools, targets, schedule and expiry are pinned.
type ApprovalRequest struct {
	Scope              store.AutomationScope `json:"scope"`
	AutomationID       string                `json:"automation_id"`
	DefinitionRevision uint64                `json:"definition_revision"`
	PolicySHA256       string                `json:"policy_sha256"`
}

func ApprovalPolicyDigest(d store.AutomationDefinition) (string, error) {
	d.Enabled = false
	d.Authorization.Mode = "approval_required"
	d.Authorization.ApprovalReference = ""
	data, err := json.Marshal(d)
	if err != nil {
		return "", err
	}
	return executionDocumentDigest(data), nil
}

func (a *PolicyApproval) ApproveUser(ctx context.Context, input ApprovalRequest) (store.AutomationApproval, error) {
	p, err := a.identity.ExplicitUser(ctx)
	if err != nil {
		return store.AutomationApproval{}, err
	}
	if p.Role != "user" {
		return store.AutomationApproval{}, ErrDenied
	}
	if err := a.Workspace(ctx, p, input.Scope, "approve"); err != nil {
		return store.AutomationApproval{}, err
	}
	r, found, err := a.repo.GetAutomationRecord(input.Scope, input.AutomationID, "definition", input.AutomationID, 0)
	if err != nil {
		return store.AutomationApproval{}, err
	}
	if !found || r.Definition == nil || input.DefinitionRevision == 0 || r.Revision != input.DefinitionRevision {
		return store.AutomationApproval{}, ErrDenied
	}
	digest, err := ApprovalPolicyDigest(*r.Definition)
	if err != nil {
		return store.AutomationApproval{}, err
	}
	if input.PolicySHA256 != digest || r.Definition.Authorization.ExpiresAt <= a.now().UnixMilli() {
		return store.AutomationApproval{}, ErrDenied
	}
	if err := a.checkPlans(ctx, p, r.Scope, *r.Definition); err != nil {
		return store.AutomationApproval{}, err
	}
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return store.AutomationApproval{}, err
	}
	return a.repo.CreateAutomationApproval(store.AutomationApproval{Scope: r.Scope, ID: hex.EncodeToString(nonce[:]), AutomationID: r.AutomationID, DefinitionRevision: r.Revision, PolicySHA256: digest, SubjectID: p.SubjectID, ExpiresAt: r.Definition.Authorization.ExpiresAt, WrittenAt: a.now().UnixMilli()})
}

// Request still requires an explicit authenticated user gesture; runtime/tool
// callers cannot turn a supplied Principal{Role:"user"} into approval authority.
func (a *PolicyApproval) Request(ctx context.Context, p Principal, r store.AutomationRecord) (string, error) {
	if r.Definition == nil {
		return "", ErrInvalid
	}
	if err := a.Workspace(ctx, p, r.Scope, "approve"); err != nil {
		return "", err
	}
	digest, err := ApprovalPolicyDigest(*r.Definition)
	if err != nil {
		return "", err
	}
	g, err := a.ApproveUser(ctx, ApprovalRequest{Scope: r.Scope, AutomationID: r.AutomationID, DefinitionRevision: r.Revision, PolicySHA256: digest})
	return g.ID, err
}

// CurrentGrant returns only the grant linked to this authorized definition.
// Clients need its own revision for revocation; a definition revision is not a grant CAS.
func (a *PolicyApproval) CurrentGrant(ctx context.Context, p Principal, r store.AutomationRecord) (*store.AutomationApproval, error) {
	if err := a.Workspace(ctx, p, r.Scope, "read"); err != nil {
		return nil, err
	}
	if r.Definition == nil || r.Definition.Authorization.ApprovalReference == "" {
		return nil, nil
	}
	g, found, err := a.repo.GetAutomationApproval(r.Scope, r.Definition.Authorization.ApprovalReference)
	if err != nil {
		return nil, err
	}
	if !found || g.AutomationID != r.AutomationID || g.Scope != r.Scope {
		return nil, ErrDenied
	}
	return &g, nil
}

func (a *PolicyApproval) RevokeUser(ctx context.Context, scope store.AutomationScope, reference string, expected uint64) (store.AutomationApproval, error) {
	p, err := a.identity.ExplicitUser(ctx)
	if err != nil {
		return store.AutomationApproval{}, err
	}
	if p.Role != "user" {
		return store.AutomationApproval{}, ErrDenied
	}
	if err := a.Workspace(ctx, p, scope, "revoke"); err != nil {
		return store.AutomationApproval{}, err
	}
	return a.repo.RevokeAutomationApproval(scope, reference, p.SubjectID, expected, a.now().UnixMilli())
}

func (a *PolicyApproval) checkPlans(ctx context.Context, p Principal, scope store.AutomationScope, d store.AutomationDefinition) error {
	if err := store.ValidateAutomationBindings(d.Plans); err != nil {
		return err
	}
	s := Service{plans: a.plans, access: a}
	for _, binding := range d.Plans {
		ref := binding.Plan
		if ref.DocumentSHA256 == "" {
			return ErrDenied
		}
		if err := s.plan(ctx, p, scope, &ref); err != nil {
			return err
		}
	}
	return nil
}

func (a *PolicyApproval) Execution(ctx context.Context, p Principal, scope store.AutomationScope, d store.AutomationDefinition, action string) error {
	if err := a.Workspace(ctx, p, scope, action); err != nil {
		return err
	}
	g, found, err := a.repo.GetAutomationApproval(scope, d.Authorization.ApprovalReference)
	if err != nil {
		return err
	}
	if !found || g.Scope != scope || g.RevokedAt != 0 || g.ExpiresAt <= a.now().UnixMilli() || d.Authorization.Mode != "approved_policy" || d.Authorization.ExpiresAt != g.ExpiresAt {
		return ErrDenied
	}
	// Recheck the approving user's CURRENT ownership; an agent does not become
	// that user. Its own ownership was checked separately above.
	owner := Principal{AccountID: scope.AccountID, SubjectID: g.SubjectID, Role: "user"}
	if err := a.ownership.Workspace(ctx, owner, scope, "approve"); err != nil {
		return err
	}
	digest, err := ApprovalPolicyDigest(d)
	if err != nil {
		return err
	}
	if digest != g.PolicySHA256 {
		return ErrDenied
	}
	return a.checkPlans(ctx, p, scope, d)
}

func (a *PolicyApproval) Verify(ctx context.Context, p Principal, r store.AutomationRecord) error {
	if r.Definition == nil || r.Revision == 0 {
		return ErrDenied
	}
	if err := a.Execution(ctx, p, r.Scope, *r.Definition, "run"); err != nil {
		return err
	}
	g, found, err := a.repo.GetAutomationApproval(r.Scope, r.Definition.Authorization.ApprovalReference)
	if err != nil {
		return err
	}
	if !found || g.AutomationID != r.AutomationID || g.RevokedAt != 0 {
		return ErrDenied
	}
	current, found, err := a.repo.GetAutomationRecord(r.Scope, r.AutomationID, "definition", r.AutomationID, 0)
	if err != nil {
		return err
	}
	if !found || current.Revision != r.Revision || !reflect.DeepEqual(current.Definition, r.Definition) || !r.Definition.Enabled {
		return ErrDenied
	}
	return nil
}

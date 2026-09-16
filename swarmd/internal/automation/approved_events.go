package automation

import (
	"context"
	"net/http"

	store "swarm/packages/swarmd/internal/store/pebble"
)

// ApprovedEventAuthority derives local event intake authority from a durable,
// explicitly user-approved definition. It does not authenticate external webhooks:
// the caller must be the authenticated approving user on the local API.
type ApprovedEventAuthority struct {
	approval *PolicyApproval
}

func NewApprovedEventAuthority(approval *PolicyApproval) *ApprovedEventAuthority {
	return &ApprovedEventAuthority{approval: approval}
}

func (a *ApprovedEventAuthority) VerifyAdmission(ctx context.Context, p Principal, scope store.AutomationScope, id string, revision uint64, t Trigger) error {
	actual, err := RuntimePrincipal(ctx)
	if err != nil || actual != p || p.Role != "user" || a == nil || a.approval == nil || t.Kind != "event" || !eventID(t.Identity) || !eventID(t.Source) || t.ScheduledAt <= 0 {
		return ErrDenied
	}
	r, found, err := a.approval.repo.GetAutomationRecord(scope, id, "definition", id, 0)
	if err != nil {
		return err
	}
	if !found || r.Definition == nil || revision == 0 || r.Revision != revision || r.Definition.Schedule.Kind != "event" || r.Definition.Schedule.TriggerSource != t.Source {
		return ErrDenied
	}
	if err := a.approval.Verify(ctx, p, r); err != nil {
		return err
	}
	grant, found, err := a.approval.repo.GetAutomationApproval(scope, r.Definition.Authorization.ApprovalReference)
	if err != nil {
		return err
	}
	if !found || grant.SubjectID != p.SubjectID || grant.RevokedAt != 0 || grant.ExpiresAt <= a.approval.now().UnixMilli() {
		return ErrDenied
	}
	return nil
}

func (a *ApprovedEventAuthority) VerifyEvent(r *http.Request, p Principal, scope store.AutomationScope, id string, revision uint64, t Trigger) error {
	return a.VerifyAdmission(r.Context(), p, scope, id, revision, t)
}

func (a *ApprovedEventAuthority) Verify(ctx context.Context, p Principal, scope store.AutomationScope, t Trigger) error {
	return (RuntimeTriggers{}).Verify(ctx, p, scope, t)
}

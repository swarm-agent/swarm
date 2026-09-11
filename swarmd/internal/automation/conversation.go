package automation

import (
	"context"

	store "swarm/packages/swarmd/internal/store/pebble"
)

// PrepareConversationDefinition resolves a deliberately omitted plan from the
// authenticated conversation once, then pins exact bytes. It neither saves nor
// approves anything. Existing definitions keep their canonical execution session.
func (s *Service) PrepareConversationDefinition(ctx context.Context, p Principal, scope store.AutomationScope, current *store.AutomationDefinition, d store.AutomationDefinition) (store.AutomationDefinition, error) {
	if err := s.authorize(ctx, p, scope, "manage"); err != nil {
		return d, err
	}
	actual, err := RuntimePrincipal(ctx)
	if err != nil || actual != p || p.Role != "agent" {
		return d, ErrDenied
	}
	if d.Enabled || d.Authorization.Mode != "approval_required" || d.Authorization.ApprovalReference != "" {
		return d, ErrDenied
	}
	if current != nil {
		if d.SessionID != "" && d.SessionID != current.SessionID {
			return d, ErrDenied
		}
		d.SessionID = current.SessionID
		if len(d.Plans) == 0 {
			d.Plans = append([]store.AutomationPlanBinding(nil), current.Plans...)
		}
	} else {
		if d.SessionID != "" && d.SessionID != p.SubjectID {
			return d, ErrDenied
		}
		d.SessionID = p.SubjectID
	}
	if d.SessionID != "" {
		if err := s.access.PlanSession(ctx, p, scope, d.SessionID); err != nil {
			return d, err
		}
	}
	if len(d.Plans) == 0 {
		plans, ok := s.plans.(interface {
			GetActivePlan(string) (store.SessionPlanSnapshot, bool, error)
		})
		if !ok {
			return d, ErrInvalid
		}
		if err := s.access.PlanSession(ctx, p, scope, p.SubjectID); err != nil {
			return d, err
		}
		plan, found, err := plans.GetActivePlan(p.SubjectID)
		if err != nil {
			return d, err
		}
		if !found || plan.Version <= 0 {
			return d, ErrNotFound
		}
		d.Plans = []store.AutomationPlanBinding{{ID: "primary", Plan: store.AutomationPlanReference{SessionID: p.SubjectID, PlanID: plan.ID, Revision: uint64(plan.Version)}}}
	}
	if err := store.ValidateAutomationBindings(d.Plans); err != nil {
		return d, err
	}
	d.Plans = append([]store.AutomationPlanBinding(nil), d.Plans...)
	for i := range d.Plans {
		if err := s.plan(ctx, p, scope, &d.Plans[i].Plan); err != nil {
			return d, err
		}
	}
	d.Schedule, err = NormalizeSchedule(d.Schedule)
	return d, err
}

// ConversationState is bounded evidence. It does not confer authority and never
// copies another session's transcript into a management conversation.
type ConversationState struct {
	Context     ContextBundle            `json:"context"`
	Definition  store.AutomationRecord   `json:"definition"`
	Occurrences []store.AutomationRecord `json:"occurrences"`
	NextCursor  string                   `json:"next_cursor,omitempty"`
}

func (s *Service) ConversationState(ctx context.Context, p Principal, scope store.AutomationScope, id string) (ConversationState, error) {
	var b ConversationState
	rows, _, err := s.History(ctx, p, scope, id, "definition", id, 0, 1)
	if err != nil {
		return b, err
	}
	if len(rows) != 1 || rows[0].Definition == nil {
		return b, ErrNotFound
	}
	b.Definition = rows[0]
	b.Context, err = s.Context(ctx, p, scope, id)
	if err != nil {
		return b, err
	}
	b.Occurrences, b.NextCursor, err = s.Search(ctx, p, store.AutomationSearch{Scope: scope, AutomationID: id, Kind: "occurrence", Limit: 10})
	return b, err
}

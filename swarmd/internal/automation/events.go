package automation

import (
	"context"
	"net/http"
	"strings"
	"unicode/utf8"

	store "swarm/packages/swarmd/internal/store/pebble"
)

// EventRegistration is explicit startup authority, not request data. No network
// endpoint or credential is accepted. The authenticated local API identity must
// match every field; changing a definition revision requires re-registration.
type EventRegistration struct {
	Principal    Principal
	Scope        store.AutomationScope
	AutomationID string
	Revision     uint64
	Source       string
}

type EventAuthority struct{ registrations []EventRegistration }

func eventID(v string) bool {
	return v != "" && len(v) <= 256 && utf8.ValidString(v) && strings.TrimSpace(v) == v
}

func NewEventAuthority(registrations []EventRegistration) (*EventAuthority, error) {
	if len(registrations) > 256 {
		return nil, ErrInvalid
	}
	for _, r := range registrations {
		if r.Principal.Role != "user" || r.Principal.AccountID != r.Scope.AccountID || !eventID(r.Principal.SubjectID) || !eventID(r.Scope.AccountID) || !eventID(r.Scope.WorkspaceID) || !eventID(r.AutomationID) || r.Revision == 0 || !eventID(r.Source) {
			return nil, ErrInvalid
		}
	}
	return &EventAuthority{registrations: append([]EventRegistration(nil), registrations...)}, nil
}

func (a *EventAuthority) VerifyAdmission(ctx context.Context, p Principal, scope store.AutomationScope, id string, revision uint64, t Trigger) error {
	actual, err := RuntimePrincipal(ctx)
	if err != nil || actual != p || t.Kind != "event" || !eventID(t.Identity) || !eventID(t.Source) || t.ScheduledAt <= 0 {
		return ErrDenied
	}
	if a != nil {
		for _, r := range a.registrations {
			if r.Principal == p && r.Scope == scope && r.AutomationID == id && r.Revision == revision && r.Source == t.Source {
				return nil
			}
		}
	}
	return ErrDenied
}

// VerifyEvent implements the API verifier. The handler must pass the request with
// its authenticated runtime identity bound; request headers are not authority.
func (a *EventAuthority) VerifyEvent(r *http.Request, p Principal, scope store.AutomationScope, id string, revision uint64, t Trigger) error {
	return a.VerifyAdmission(r.Context(), p, scope, id, revision, t)
}

func (a *EventAuthority) Verify(ctx context.Context, p Principal, scope store.AutomationScope, t Trigger) error {
	// Exact event admission is checked by VerifyAdmission, never by source alone.
	return (RuntimeTriggers{}).Verify(ctx, p, scope, t)
}

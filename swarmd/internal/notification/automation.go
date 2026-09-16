package notification

import (
	"context"
	"errors"
	"time"

	store "swarm/packages/swarmd/internal/store/pebble"
)

type AutomationOutbox interface {
	ClaimAutomationDelivery(store.AutomationDeliveryReference, int64) (store.AutomationRecord, store.AutomationDelivery, bool, error)
	AckAutomationDelivery(store.AutomationDelivery) error
}

type AutomationNotifications interface {
	UpsertSystemNotificationForAccount(string, store.NotificationRecord) (store.NotificationRecord, bool, error)
}

type AutomationDeliveryService struct {
	outbox        AutomationOutbox
	notifications AutomationNotifications
	swarmID       string
}

// NewAutomationDeliveryService binds local configured authority only. The daemon
// feeds exact terminal occurrence revisions on outcome and bounded recovery pages.
// No execution dependency is held: failed delivery cannot retry an automation.
func NewAutomationDeliveryService(outbox AutomationOutbox, notifications AutomationNotifications, swarmID string) (*AutomationDeliveryService, error) {
	if outbox == nil || notifications == nil || swarmID == "" {
		return nil, errors.New("automation notification configuration required")
	}
	return &AutomationDeliveryService{outbox, notifications, swarmID}, nil
}

// Deliver acknowledges durable local notification persistence, not optional
// best-effort Web Push. Five leased attempts are allowed; exhaustion is retained
// in the outbox rather than silently reported as delivery success.
func (s *AutomationDeliveryService) Deliver(ctx context.Context, ref store.AutomationDeliveryReference) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r, attempt, claimed, err := s.outbox.ClaimAutomationDelivery(ref, time.Now().UnixMilli())
	if err != nil {
		return err
	}
	if !claimed {
		if attempt.Acked {
			return nil
		}
		return errors.New("automation notification deferred or exhausted")
	}
	// Never copy summaries, facts, source payloads, paths, titles or credentials.
	_, _, err = s.notifications.UpsertSystemNotificationForAccount(ref.Scope.AccountID, store.NotificationRecord{
		ID: ref.NotificationID(), SwarmID: s.swarmID, SessionID: r.Occurrence.SessionID,
		Title: "Automation outcome", Body: "Automation " + r.Occurrence.State + ". Open the execution session for details.",
		SourceEventType: "automation.outcome", CreatedAt: r.WrittenAt, UpdatedAt: r.WrittenAt,
	})
	if err != nil {
		return errors.New("automation notification persistence failed")
	}
	return s.outbox.AckAutomationDelivery(attempt)
}

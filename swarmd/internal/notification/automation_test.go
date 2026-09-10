package notification

import (
	"context"
	"errors"
	"testing"

	store "swarm/packages/swarmd/internal/store/pebble"
)

type automationOutboxFake struct { ack int }
func (f *automationOutboxFake) ClaimAutomationDelivery(r store.AutomationDeliveryReference, _ int64) (store.AutomationRecord, store.AutomationDelivery, bool, error) {
	return store.AutomationRecord{WrittenAt: 1, Occurrence: &store.AutomationOccurrence{State: "failed", SessionID: "execution"}}, store.AutomationDelivery{Reference: r, Attempts: 1}, true, nil
}
func (f *automationOutboxFake) AckAutomationDelivery(store.AutomationDelivery) error { f.ack++; return nil }
type automationNotificationsFake struct { err error; record store.NotificationRecord; account string }
func (f *automationNotificationsFake) UpsertSystemNotificationForAccount(a string, r store.NotificationRecord) (store.NotificationRecord, bool, error) { f.account, f.record = a, r; return r, true, f.err }

// Purpose: AutomationDeliveryService must acknowledge only durable local delivery,
// redact arbitrary execution evidence and preserve account/reference identity.
// Fake dependencies isolate failure handling; no execution interface is available.
func TestAutomationDeliveryFailureAndRedaction(t *testing.T) {
	o := &automationOutboxFake{}
	n := &automationNotificationsFake{err: errors.New("private failure detail")}
	s, err := NewAutomationDeliveryService(o, n, "local")
	if err != nil { t.Fatal(err) }
	r := store.AutomationDeliveryReference{Scope: store.AutomationScope{AccountID: "account", WorkspaceID: "workspace"}, AutomationID: "automation", OccurrenceID: "occurrence", Revision: 3}
	if err := s.Deliver(context.Background(), r); err == nil || err.Error() != "automation notification persistence failed" { t.Fatalf("failure: %v", err) }
	if o.ack != 0 { t.Fatal("failed persistence acknowledged") }
	n.err = nil
	if err := s.Deliver(context.Background(), r); err != nil { t.Fatal(err) }
	if o.ack != 1 || n.account != r.Scope.AccountID || n.record.ID != r.NotificationID() || n.record.Body != "Automation failed. Open the execution session for details." || n.record.ActionURL != "" { t.Fatal("delivery identity/redaction mismatch") }
}

package notification

import (
	"context"
	"sync"
	"testing"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestServiceWebPushDispatchOnlyForNewActiveUnmutedNotifications(t *testing.T) {
	service := &Service{}
	var mu sync.Mutex
	var delivered []string
	done := make(chan struct{}, 1)
	service.SetWebPushDispatcher(func(_ context.Context, account string, record pebblestore.NotificationRecord) error {
		mu.Lock()
		delivered = append(delivered, account+":"+record.ID)
		mu.Unlock()
		done <- struct{}{}
		return nil
	})
	service.dispatchWebPushLocked(pebblestore.NotificationRecord{ID: "active", AccountScopeID: "account-a", Status: pebblestore.NotificationStatusActive})
	service.dispatchWebPushLocked(pebblestore.NotificationRecord{ID: "muted", AccountScopeID: "account-a", Status: pebblestore.NotificationStatusActive, MutedAt: 1})
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("active notification was not dispatched")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(delivered) != 1 || delivered[0] != "account-a:active" {
		t.Fatalf("delivered = %#v", delivered)
	}
}

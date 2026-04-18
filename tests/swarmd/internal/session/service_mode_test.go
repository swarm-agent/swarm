package session

import (
	"path/filepath"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestServiceSessionModeLifecycle(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "session-mode.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() {
		_ = store.Close()
	}()

	eventLog, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}

	svc := NewService(pebblestore.NewSessionStore(store), eventLog)
	created, _, err := svc.CreateSession("Mode Test", "/tmp/work", "work")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if created.Mode != ModePlan {
		t.Fatalf("expected default mode plan, got %s", created.Mode)
	}

	mode, err := svc.GetMode(created.ID)
	if err != nil {
		t.Fatalf("get mode: %v", err)
	}
	if mode != ModePlan {
		t.Fatalf("expected mode plan from GetMode, got %s", mode)
	}

	updated, _, err := svc.SetMode(created.ID, ModeAuto)
	if err != nil {
		t.Fatalf("set mode auto: %v", err)
	}
	if updated.Mode != ModeAuto {
		t.Fatalf("expected mode auto, got %s", updated.Mode)
	}

	if _, _, err := svc.SetMode(created.ID, "invalid"); err == nil {
		t.Fatalf("expected invalid mode error")
	}
}

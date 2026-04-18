package ui

import (
	"testing"
	"time"

	"swarm-refactor/swarmtui/internal/model"
)

func TestHomePageShowToastSetsState(t *testing.T) {
	p := NewHomePage(model.EmptyHome())

	p.ShowToast(ToastSuccess, "saved")

	if got := p.toast.Message; got != "saved" {
		t.Fatalf("toast message = %q, want %q", got, "saved")
	}
	if got := p.toast.Level; got != ToastSuccess {
		t.Fatalf("toast level = %v, want %v", got, ToastSuccess)
	}
	if p.toast.ExpiresAt.IsZero() {
		t.Fatalf("toast expiry not set")
	}
}

func TestToastTickExpires(t *testing.T) {
	var toast toastState
	if shown := toast.show(ToastInfo, "hello", time.Second); !shown {
		t.Fatalf("show() = false, want true")
	}
	toast.ExpiresAt = time.Now().Add(-time.Second)

	changed := toast.tick(time.Now())
	if !changed {
		t.Fatalf("tick() = false, want true when expired")
	}
	if toast.visible(time.Now()) {
		t.Fatalf("toast should not be visible after expiry")
	}
}

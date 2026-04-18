package ui

import (
	"testing"

	"github.com/gdamore/tcell/v2"
)

func TestChatConsumeQuitScrollbackJump(t *testing.T) {
	page := NewChatPage(ChatPageOptions{
		SessionID:      "session-1",
		ShowHeader:     true,
		AuthConfigured: true,
	})
	if consumed := page.ConsumeQuitScrollbackJump(); consumed {
		t.Fatalf("ConsumeQuitScrollbackJump() = true, want false at bottom")
	}

	for i := 0; i < 16; i++ {
		page.AppendSystemMessage("scrollback line")
	}
	page.HandleKey(tcell.NewEventKey(tcell.KeyUp, 0, tcell.ModNone))

	if consumed := page.ConsumeQuitScrollbackJump(); !consumed {
		t.Fatalf("ConsumeQuitScrollbackJump() = false, want true when scrolled up")
	}
	if consumed := page.ConsumeQuitScrollbackJump(); consumed {
		t.Fatalf("ConsumeQuitScrollbackJump() = true, want false after jumping to bottom")
	}
}

func TestChatHandleEscape_IdleRequiresDoublePress(t *testing.T) {
	page := NewChatPage(ChatPageOptions{
		SessionID:      "session-1",
		ShowHeader:     true,
		AuthConfigured: true,
	})

	if consumed := page.HandleEscape(); !consumed {
		t.Fatalf("HandleEscape() consumed = false, want true on first Esc")
	}

	if consumed := page.HandleEscape(); consumed {
		t.Fatalf("HandleEscape() consumed = true, want false on second Esc to allow home route")
	}
}

func TestChatHandleEscape_BusyAbortsRunAndArmsHome(t *testing.T) {
	page := NewChatPage(ChatPageOptions{
		SessionID:      "session-1",
		ShowHeader:     true,
		AuthConfigured: true,
	})
	page.busy = true
	cancelCalled := 0
	page.runCancel = func() { cancelCalled++ }

	if consumed := page.HandleEscape(); !consumed {
		t.Fatalf("HandleEscape() consumed = false, want true when aborting run")
	}
	if cancelCalled != 1 {
		t.Fatalf("cancelCalled = %d, want 1", cancelCalled)
	}
	if !page.runAbort {
		t.Fatalf("runAbort = false, want true")
	}

	if consumed := page.HandleEscape(); consumed {
		t.Fatalf("HandleEscape() consumed = true, want false on second Esc to allow home route")
	}
}

func TestChatHandleEscape_DoesNotInterceptPermissionEsc(t *testing.T) {
	page := NewChatPage(ChatPageOptions{
		SessionID:      "session-1",
		ShowHeader:     true,
		AuthConfigured: true,
	})
	page.pendingPerms = []ChatPermissionRecord{
		{ID: "perm-1", Status: "pending"},
	}

	if consumed := page.HandleEscape(); consumed {
		t.Fatalf("HandleEscape() consumed = true, want false when permission modal is active")
	}
}

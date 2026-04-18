package ui

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/model"
)

func TestSessionsEnterQueuesOpenSessionAction(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	m := model.EmptyHome()
	m.RecentSessions = []model.SessionSummary{
		{
			ID:            "session-123",
			WorkspacePath: "/tmp/workspace-a",
			WorkspaceName: "workspace-a",
			Title:         "Session A",
			UpdatedAgo:    "1m",
		},
	}
	p.SetModel(m)

	p.HandleKey(tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModCtrl))
	if !p.SessionsFocused() {
		t.Fatalf("sessions mode should be focused after Ctrl+Down")
	}
	p.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))

	action, ok := p.PopHomeAction()
	if !ok {
		t.Fatalf("expected home action after Enter in sessions mode")
	}
	if action.Kind != HomeActionOpenSession {
		t.Fatalf("action kind = %q, want %q", action.Kind, HomeActionOpenSession)
	}
	if action.SessionID != "session-123" {
		t.Fatalf("session id = %q, want %q", action.SessionID, "session-123")
	}
	if action.SessionTitle != "Session A" {
		t.Fatalf("session title = %q, want %q", action.SessionTitle, "Session A")
	}
	if action.WorkspacePath != "/tmp/workspace-a" {
		t.Fatalf("workspace path = %q, want %q", action.WorkspacePath, "/tmp/workspace-a")
	}
	if action.WorkspaceName != "workspace-a" {
		t.Fatalf("workspace name = %q, want %q", action.WorkspaceName, "workspace-a")
	}
	if _, ok := p.PopHomeAction(); ok {
		t.Fatalf("expected home action queue to be empty after pop")
	}
}

func TestSessionsEnterWithoutIDDoesNotQueueAction(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	m := model.EmptyHome()
	m.RecentSessions = []model.SessionSummary{
		{
			Title:      "Session Without ID",
			UpdatedAgo: "2m",
		},
	}
	p.SetModel(m)

	p.HandleKey(tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModCtrl))
	p.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))

	if _, ok := p.PopHomeAction(); ok {
		t.Fatalf("expected no home action when session id is missing")
	}
	if status := p.Status(); !strings.Contains(status, "missing id") {
		t.Fatalf("status = %q, expected missing id message", status)
	}
}

func TestHomeShiftTabCyclesMode(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	if got := p.SessionMode(); got != "plan" {
		t.Fatalf("initial mode = %q, want plan", got)
	}

	p.HandleKey(tcell.NewEventKey(tcell.KeyBacktab, 0, tcell.ModNone))
	if got := p.SessionMode(); got != "auto" {
		t.Fatalf("mode after first Shift+Tab = %q, want auto", got)
	}
	if got := p.Status(); got != "mode: auto" {
		t.Fatalf("status after first Shift+Tab = %q, want %q", got, "mode: auto")
	}

	p.HandleKey(tcell.NewEventKey(tcell.KeyBacktab, 0, tcell.ModNone))
	if got := p.SessionMode(); got != "plan" {
		t.Fatalf("mode after second Shift+Tab = %q, want plan", got)
	}
}

func TestHomeShiftTabDoesNotChangeExecutionCapabilityModes(t *testing.T) {
	for _, current := range []string{"read", "readwrite"} {
		p := NewHomePage(model.EmptyHome())
		p.SetSessionMode(current)
		p.HandleKey(tcell.NewEventKey(tcell.KeyBacktab, 0, tcell.ModNone))
		if got := p.SessionMode(); got != current {
			t.Fatalf("mode after Shift+Tab from %q = %q, want unchanged", current, got)
		}
	}
}

func TestNormalizeHomeSessionMode_RemovesLegacyYolo(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.SetSessionMode("yolo")
	if got := p.SessionMode(); got != "plan" {
		t.Fatalf("SessionMode() = %q, want %q", got, "plan")
	}
}

func TestNormalizeHomeSessionMode_PreservesExecutionCapabilityModes(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.SetSessionMode("read")
	if got := p.SessionMode(); got != "read" {
		t.Fatalf("SessionMode() = %q, want %q", got, "read")
	}
	p.SetSessionMode("readwrite")
	if got := p.SessionMode(); got != "readwrite" {
		t.Fatalf("SessionMode() = %q, want %q", got, "readwrite")
	}
}
